package components

import (
	"image"
	"image/draw"
	"image/gif"
	"log"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
	"github.com/Hashy-Software/hasherino-go/hasherino"
)

const (
	// gifFrameTick is how often running gif animations are advanced. It
	// bounds the frame rate and repaints of every animation, no matter how
	// many emotes animate at once. Chat emotes look smooth at this rate,
	// and every advance repaints the whole window, which is the main cost
	// of animating.
	gifFrameTick = 66 * time.Millisecond
	// gifMinFrameTime is the shortest time a gif frame is displayed. Gif
	// files can request delays that are far too fast to display, which
	// would otherwise busy loop the animation.
	gifMinFrameTime = 20 * time.Millisecond
	// gifDebtLimit caps how much time an animation that could not keep up
	// accumulates, so a stalled app doesn't burst through many frames at
	// once when it catches up.
	gifDebtLimit = 250 * time.Millisecond
)

// gifAnimation is a decoded gif prepared for playback: every frame is
// pre-composited the way the animation renders it, so that playing it only
// swaps finished frames instead of drawing them over and over. Animations
// are shared by every widget showing the emote and are never modified after
// they were built.
type gifAnimation struct {
	frames    []*image.NRGBA
	delays    []time.Duration
	loopCount int
}

// buildAnimation composites every frame of the gif once, replicating how a
// gif renderer draws the frames onto a canvas, and the time each frame is
// displayed.
func buildAnimation(src *gif.GIF) *gifAnimation {
	if len(src.Image) == 0 {
		return nil
	}
	bounds := src.Image[0].Bounds()
	target := image.NewNRGBA(bounds)
	animation := &gifAnimation{
		frames:    make([]*image.NRGBA, len(src.Image)),
		delays:    make([]time.Duration, len(src.Image)),
		loopCount: src.LoopCount,
	}
	noDisposeIndex := -1
	for i, frame := range src.Image {
		dispose := byte(gif.DisposalNone)
		if i > 0 && i-1 < len(src.Disposal) {
			dispose = src.Disposal[i-1]
		}
		switch dispose {
		case gif.DisposalNone:
			// do not dispose the old frame, draw the new frame over it
			draw.Draw(target, bounds, frame, image.Point{}, draw.Over)
			// will be used in case of disposalPrevious
			noDisposeIndex = i - 1
		case gif.DisposalBackground:
			// clear with background then render new frame over it,
			// replacing entirely with the new frame achieves this
			draw.Draw(target, bounds, frame, image.Point{}, draw.Src)
		case gif.DisposalPrevious:
			// restore the frame before the last fully drawn one, then
			// render the new frame over it
			if noDisposeIndex >= 0 && noDisposeIndex < len(src.Image) {
				draw.Draw(target, bounds, src.Image[noDisposeIndex], image.Point{}, draw.Src)
				draw.Draw(target, bounds, frame, image.Point{}, draw.Over)
			} else {
				// there was no previous graphic, render background instead
				draw.Draw(target, bounds, frame, image.Point{}, draw.Src)
			}
		default:
			// disposal unspecified, simply draw the new frame over the old
			draw.Draw(target, bounds, frame, image.Point{}, draw.Over)
		}
		snapshot := image.NewNRGBA(bounds)
		draw.Draw(snapshot, bounds, target, image.Point{}, draw.Src)
		animation.frames[i] = snapshot

		delay := time.Duration(0)
		if i < len(src.Delay) {
			delay = time.Duration(src.Delay[i]) * 10 * time.Millisecond
		}
		if delay < gifMinFrameTime {
			delay = gifMinFrameTime
		}
		animation.delays[i] = delay
	}
	return animation
}

// emptyAnimation is shown when the real gif failed to load. It renders as
// nothing and plays a single frame.
var emptyAnimation = &gifAnimation{
	frames:    []*image.NRGBA{image.NewNRGBA(image.Rect(0, 0, 1, 1))},
	delays:    []time.Duration{gifMinFrameTime},
	loopCount: -1,
}

// gifAnimator advances every running EmoteGif from a single ticker, so that
// any number of animated emotes causes one bounded stream of ui updates
// instead of one goroutine with per-frame draws per emote. The ticker runs
// only while at least one animation is running.
type gifAnimator struct {
	mu      sync.Mutex
	gifs    map[*EmoteGif]struct{}
	started bool
}

var animator = &gifAnimator{gifs: make(map[*EmoteGif]struct{})}

func (a *gifAnimator) add(g *EmoteGif) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.gifs[g] = struct{}{}
	if !a.started {
		a.started = true
		go a.run()
	}
}

func (a *gifAnimator) remove(g *EmoteGif) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.gifs, g)
}

func (a *gifAnimator) run() {
	ticker := time.NewTicker(gifFrameTick)
	defer ticker.Stop()
	for range ticker.C {
		a.mu.Lock()
		if len(a.gifs) == 0 {
			a.started = false
			a.mu.Unlock()
			return
		}
		gifs := make([]*EmoteGif, 0, len(a.gifs))
		for g := range a.gifs {
			gifs = append(gifs, g)
		}
		a.mu.Unlock()

		now := time.Now()
		var swaps []func()
		for _, g := range gifs {
			if swap := g.tick(now); swap != nil {
				swaps = append(swaps, swap)
			}
		}
		if len(swaps) == 0 {
			continue
		}
		fyne.Do(func() {
			for _, swap := range swaps {
				swap()
			}
		})
	}
}

// EmoteGif widget shows a Gif image with many frames.
type EmoteGif struct {
	widget.BaseWidget
	min fyne.Size

	dst *canvas.Image

	// animation state, guarded by runLock
	animation   *gifAnimation
	running     bool
	stopping    bool
	current     int // frame that is currently displayed
	remaining   int // loop count left, -1 loops forever
	debt        time.Duration
	lastFrameAt time.Time
	// loaded tracks whether animation holds the real gif, so that a failed
	// load is retried on the next start
	loaded bool

	runLock sync.RWMutex

	// custom attributes
	emote         *hasherino.Emote
	clickCallback func(string) error
	lazyLoad      bool
}

// NewEmoteGif creates a new widget loaded to show the specified image resource.
// If there is an error loading the image it will be returned in the error value.
// If lazyLoad is true, only load the real image when Start() is called
func NewEmoteGif(emote *hasherino.Emote, clickCallback func(string) error, lazyLoad bool) (*EmoteGif, error) {
	ret := newGif()
	ret.emote = emote
	ret.clickCallback = clickCallback
	ret.lazyLoad = lazyLoad

	if lazyLoad {
		return ret, nil
	}

	animation, err := getEmoteCache().Gif(emote)
	if err != nil {
		return ret, err
	}
	ret.animation = animation
	ret.loaded = true
	firstFrame := animation.frames[0]
	fyne.Do(func() {
		ret.dst.Image = firstFrame
		ret.dst.Refresh()
	})
	return ret, nil
}

// CreateRenderer loads the widget renderer for this widget. This is an internal requirement for Fyne.
func (g *EmoteGif) CreateRenderer() fyne.WidgetRenderer {
	return &gifRenderer{gif: g}
}

// MinSize returns the minimum size that this GIF can occupy.
// Because gif images are measured in pixels we cannot use the dimensions, so this defaults to 0x0.
// You can set a minimum size if required using SetMinSize.
func (g *EmoteGif) MinSize() fyne.Size {
	return g.min
}

// SetMinSize sets the smallest possible size that this AnimatedGif should be drawn at.
// Be careful not to set this based on pixel sizes as that will vary based on output device.
func (g *EmoteGif) SetMinSize(min fyne.Size) {
	g.min = min
}

// Start begins the animation. The speed of the animation is controlled by
// the frame delays of the loaded gif, and bounded to a smooth frame rate.
func (g *EmoteGif) Start() {
	g.runLock.Lock()
	running := g.running
	needsLoad := g.lazyLoad && !g.loaded
	g.runLock.Unlock()
	if running {
		return
	}

	// load outside of the lock, a download must not block Stop
	var animation *gifAnimation
	var loaded bool
	if needsLoad {
		var err error
		animation, err = getEmoteCache().Gif(g.emote)
		if err != nil {
			log.Println("Error loading lazy gif", err)
			animation = emptyAnimation
		} else {
			loaded = true
		}
	}

	g.runLock.Lock()
	if g.running {
		// another Start loaded the gif while this one was downloading
		g.runLock.Unlock()
		return
	}
	if animation != nil {
		g.animation = animation
		g.loaded = loaded
	}
	if g.animation == nil || len(g.animation.frames) == 0 {
		g.runLock.Unlock()
		return
	}
	switch g.animation.loopCount {
	case -1: // don't loop
		g.remaining = 1
	case 0: // loop forever
		g.remaining = -1
	default:
		g.remaining = g.animation.loopCount + 1
	}
	g.current = 0
	g.debt = 0
	g.lastFrameAt = time.Now()
	firstFrame := g.animation.frames[0]
	g.running = true
	g.runLock.Unlock()

	fyne.Do(func() {
		g.dst.Image = firstFrame
		g.dst.Refresh()
	})

	animator.add(g)
}

// tick advances the animation to the frame that should be visible at the
// given time. It returns a swap function that displays the new frame and
// must run on the ui thread, or nil when the displayed frame stays the same.
// Animations that stopped or finished remove themselves from their animator.
func (g *EmoteGif) tick(now time.Time) (swap func()) {
	g.runLock.Lock()
	defer g.runLock.Unlock()

	if !g.running || g.stopping || g.animation == nil {
		g.finalizeLocked()
		animator.remove(g)
		return nil
	}

	animation := g.animation
	delays := animation.delays

	g.debt += now.Sub(g.lastFrameAt)
	g.lastFrameAt = now
	if g.debt > gifDebtLimit {
		// the animation stalled, skip the frames it missed
		g.debt = delays[g.current]
	}

	advanced := false
	for g.debt >= delays[g.current] {
		g.debt -= delays[g.current]
		last := g.current == len(delays)-1
		g.current = (g.current + 1) % len(delays)
		advanced = true
		if last && g.remaining > 0 {
			g.remaining--
			if g.remaining == 0 {
				// the animation finished its last loop, keep the final
				// frame visible
				frame := animation.frames[len(delays)-1]
				g.finalizeLocked()
				animator.remove(g)
				return func() {
					g.dst.Image = frame
					g.dst.Refresh()
				}
			}
		}
	}
	if !advanced {
		return nil
	}
	frame := animation.frames[g.current]
	return func() {
		g.dst.Image = frame
		g.dst.Refresh()
	}
}

// finalizeLocked stops the animation and releases the shared frames when
// they were lazily loaded. The emote cache keeps them for the next start.
func (g *EmoteGif) finalizeLocked() {
	g.running = false
	g.stopping = false
	if g.lazyLoad {
		g.animation = nil
		g.loaded = false
	}
}

// Stop will request that the animation stops running, the last frame will remain visible
func (g *EmoteGif) Stop() {
	if !g.isRunning() {
		return
	}
	g.runLock.Lock()
	g.stopping = true
	g.runLock.Unlock()
}

func (g *EmoteGif) isRunning() bool {
	g.runLock.RLock()
	defer g.runLock.RUnlock()
	return g.running
}

func (g *EmoteGif) Tapped(event *fyne.PointEvent) {
	if err := g.clickCallback(g.emote.Name + " "); err != nil {
		log.Println("Error running emote tap callback", err)
	}
}

func (c *EmoteGif) LazyLoad() error {
	c.Start()
	return nil
}

func (c *EmoteGif) LazyUnload() error {
	c.Stop()
	return nil
}

func newGif() *EmoteGif {
	ret := &EmoteGif{}
	ret.ExtendBaseWidget(ret)
	ret.dst = &canvas.Image{}
	ret.dst.FillMode = canvas.ImageFillContain
	return ret
}

type gifRenderer struct {
	gif *EmoteGif
}

func (g *gifRenderer) Destroy() {
	g.gif.Stop()
}

func (g *gifRenderer) Layout(size fyne.Size) {
	g.gif.dst.Resize(size)
}

func (g *gifRenderer) MinSize() fyne.Size {
	return g.gif.MinSize()
}

func (g *gifRenderer) Objects() []fyne.CanvasObject {
	if g.gif.isRunning() {
		return []fyne.CanvasObject{g.gif.dst}
	}
	return []fyne.CanvasObject{}
}

func (g *gifRenderer) Refresh() {
	g.gif.dst.Refresh()
}

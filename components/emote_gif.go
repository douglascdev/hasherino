package components

import (
	"bytes"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"io"
	"log"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
	"github.com/Hashy-Software/hasherino-go/hasherino"
)

// EmoteGif widget shows a Gif image with many frames.
type EmoteGif struct {
	widget.BaseWidget
	min fyne.Size

	src               *gif.GIF
	dst               *canvas.Image
	noDisposeIndex    int
	remaining         int
	stopping, running bool
	runLock           sync.RWMutex

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

	res, err := tempFileResource(emote)
	if err != nil {
		return ret, err
	}
	return ret, ret.LoadResource(res)
}

// CreateRenderer loads the widget renderer for this widget. This is an internal requirement for Fyne.
func (g *EmoteGif) CreateRenderer() fyne.WidgetRenderer {
	return &gifRenderer{gif: g}
}

// Load is used to change the gif file shown.
// It will change the loaded content and prepare the new frames for animation.
func (g *EmoteGif) Load(u fyne.URI) error {
	fyne.Do(func() {
		g.dst.Image = nil
		g.dst.Refresh()
	})

	if u == nil {
		return nil
	}

	read, err := storage.Reader(u)
	if err != nil {
		return err
	}

	return g.load(read)
}

// LoadResource is used to change the gif resource shown.
// It will change the loaded content and prepare the new frames for animation.
func (g *EmoteGif) LoadResource(r fyne.Resource) error {
	fyne.Do(func() {
		g.dst.Image = nil
		g.dst.Refresh()
	})

	if r == nil || len(r.Content()) == 0 {
		return nil
	}
	return g.load(bytes.NewReader(r.Content()))
}

func (g *EmoteGif) load(read io.Reader) error {
	pix, err := gif.DecodeAll(read)
	if err != nil {
		return err
	}
	g.src = pix
	fyne.Do(func() {
		g.dst.Image = pix.Image[0]
		g.dst.Refresh()
	})

	return nil
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

func (g *EmoteGif) draw(dst draw.Image, frame image.Image, dispose byte, index int) {
	bounds := dst.Bounds()
	switch dispose {
	case gif.DisposalNone:
		// Do not dispose old frame, draw new frame over old
		draw.Draw(dst, bounds, frame, image.Point{}, draw.Over)
		// will be used in case of disposalPrevious
		g.noDisposeIndex = index - 1
	case gif.DisposalBackground:
		// clear with background then render new frame Over it
		// replacing entirely with new frame should achieve this?
		draw.Draw(dst, bounds, frame, image.Point{}, draw.Src)
	case gif.DisposalPrevious:
		// restore frame with previous image then render new frame over it
		if g.noDisposeIndex >= 0 && g.src != nil && g.noDisposeIndex < len(g.src.Image) {
			draw.Draw(dst, bounds, g.src.Image[g.noDisposeIndex], image.Point{}, draw.Src)
			draw.Draw(dst, bounds, frame, image.Point{}, draw.Over)
		} else {
			// there was no previous graphic, render background instead?
			draw.Draw(dst, bounds, frame, image.Point{}, draw.Src)
		}
	default:
		// Disposal = Unspecified/Reserved, simply draw new frame over previous
		draw.Draw(dst, bounds, frame, image.Point{}, draw.Over)
	}

	g.dst.Refresh()
}

func (g *EmoteGif) loadEmpty() {
	g.src = &gif.GIF{}
	g.src.Image = []*image.Paletted{image.NewPaletted(image.Rect(0, 0, 1, 1), palette.Plan9)}
	g.src.Disposal = []byte{gif.DisposalNone}
	g.src.LoopCount = -1
}

// Start begins the animation. The speed of the transition is controlled by the loaded gif file.
func (g *EmoteGif) Start() {
	if g.isRunning() {
		return
	}
	if g.lazyLoad {
		res, err := tempFileResource(g.emote)
		if err != nil {
			log.Println("Error loading lazy gif", err)
			g.loadEmpty()
		} else {
			err = g.LoadResource(res)
			if err != nil {
				log.Println("Error loading lazy gif", err)
				g.loadEmpty()
			}
		}
	}
	g.runLock.Lock()
	g.running = true
	g.runLock.Unlock()

	if g.src == nil || len(g.src.Image) == 0 {
		return
	}
	buffer := image.NewNRGBA(g.src.Image[0].Bounds())
	fyne.Do(func() {
		g.dst.Image = buffer
		g.noDisposeIndex = -1
		g.draw(buffer, g.src.Image[0], gif.DisposalNone, 0)
	})

	go func() {
		switch g.src.LoopCount {
		case -1: // don't loop
			g.remaining = 1
		case 0: // loop forever
			g.remaining = -1
		default:
			g.remaining = g.src.LoopCount + 1
		}
	loop:
		for g.remaining != 0 {
			frames := g.src.Image
			for c, frame := range frames {
				dispose := byte(gif.DisposalNone)
				if c > 0 {
					dispose = g.src.Disposal[c-1]
				}
				if g.isStopping() {
					if g.lazyLoad {
						g.loadEmpty()
					}
					break loop
				}
				fyne.Do(func() {
					g.draw(buffer, frame, dispose, c)
				})

				if g.src == nil || c >= len(g.src.Delay) {
					break loop
				}
				time.Sleep(time.Millisecond * time.Duration(g.src.Delay[c]) * 10)
			}
			if g.remaining > -1 { // don't underflow int
				g.remaining--
			}
		}
		g.runLock.Lock()
		g.running = false
		g.stopping = false
		g.runLock.Unlock()
	}()
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

func (g *EmoteGif) isStopping() bool {
	g.runLock.RLock()
	defer g.runLock.RUnlock()
	return g.stopping
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

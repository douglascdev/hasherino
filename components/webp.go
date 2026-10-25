package components

import (
	"image"
	"log"
	"sync"

	"github.com/Hashy-Software/hasherino-go/hasherino"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

// emptyImage is displayed while the emote is not loaded.
var emptyImage = image.NewNRGBA(image.Rect(0, 0, 1, 1))

type WebpWidget struct {
	widget.BaseWidget
	min fyne.Size

	dst           *canvas.Image
	emote         *hasherino.Emote
	clickCallback func(string) error

	// mu guards img and the image displayed by dst, so that concurrent lazy
	// loads and unloads of the same widget can't race
	mu  sync.Mutex
	img image.Image
}

func NewWebpWidget(emote *hasherino.Emote, clickCallback func(string) error) (*WebpWidget, error) {
	ret := newAnimatedGif()
	ret.dst.Image = emptyImage
	ret.emote = emote
	ret.clickCallback = clickCallback
	return ret, nil
}

func (g *WebpWidget) CreateRenderer() fyne.WidgetRenderer {
	return &WebpRenderer{webp: g}
}

func (g *WebpWidget) MinSize() fyne.Size {
	return g.min
}

func (g *WebpWidget) SetMinSize(min fyne.Size) {
	g.min = min
}

func newAnimatedGif() *WebpWidget {
	ret := &WebpWidget{}
	ret.ExtendBaseWidget(ret)
	ret.dst = &canvas.Image{}
	ret.dst.FillMode = canvas.ImageFillContain
	return ret
}

// applyImage displays the current image state of the widget. The last state
// change to run wins, so a load finishing after an unload does not leave a
// stale image behind.
func (g *WebpWidget) applyImage() {
	fyne.Do(func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.img == nil {
			g.dst.Image = emptyImage
		} else {
			g.dst.Image = g.img
		}
		g.dst.Refresh()
	})
}

func (g *WebpWidget) LazyLoad() error {
	img, err := getEmoteCache().Image(g.emote)
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.img = img
	g.mu.Unlock()
	g.applyImage()
	return nil
}

func (g *WebpWidget) LazyUnload() error {
	g.mu.Lock()
	g.img = nil
	g.mu.Unlock()
	g.applyImage()
	return nil
}

func (g *WebpWidget) Tapped(event *fyne.PointEvent) {
	if err := g.clickCallback(g.emote.Name + " "); err != nil {
		log.Println("Error running emote tap callback", err)
	}
}

type WebpRenderer struct {
	webp *WebpWidget
}

func (g *WebpRenderer) Destroy() {
}

func (g *WebpRenderer) Layout(size fyne.Size) {
	g.webp.dst.Resize(size)
}

func (g *WebpRenderer) MinSize() fyne.Size {
	return g.webp.MinSize()
}

func (g *WebpRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{g.webp.dst}
}

func (g *WebpRenderer) Refresh() {
	g.webp.dst.Refresh()
}

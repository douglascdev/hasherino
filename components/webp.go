package components

import (
	"bytes"
	"image"
	"io"
	"log"

	"github.com/Hashy-Software/hasherino-go/hasherino"
	"golang.org/x/image/webp"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"
)

var emptyImg *image.Image = nil

type WebpWidget struct {
	widget.BaseWidget
	min fyne.Size

	dst           *canvas.Image
	emote         *hasherino.Emote
	clickCallback func(string) error
}

func NewWebpWidget(emote *hasherino.Emote, clickCallback func(string) error) (*WebpWidget, error) {
	ret := newAnimatedGif()
	ret.loadEmptyDst()
	ret.emote = emote
	ret.clickCallback = clickCallback
	return ret, nil
}

func (g *WebpWidget) loadEmptyDst() {
	if emptyImg == nil {
		img := canvas.NewImageFromImage(image.NewNRGBA(image.Rect(0, 0, 1, 1)))
		emptyImg = &img.Image
	}
	g.dst.Image = *emptyImg
}

func (g *WebpWidget) CreateRenderer() fyne.WidgetRenderer {
	return &WebpRenderer{webp: g}
}

func (g *WebpWidget) Load(u fyne.URI) error {
	if u == nil {
		return nil
	}

	read, err := storage.Reader(u)
	if err != nil {
		return err
	}

	return g.load(read)
}

func (g *WebpWidget) LoadResource(r fyne.Resource) error {
	if r == nil || len(r.Content()) == 0 {
		return nil
	}
	return g.load(bytes.NewReader(r.Content()))
}

func (g *WebpWidget) load(read io.Reader) error {
	pix, err := webp.Decode(read)
	if err != nil {
		return err
	}
	fyne.Do(func() {
		g.dst.Image = pix
		g.dst.Refresh()
	})

	return nil
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

func (g *WebpWidget) LazyLoad() error {
	res, err := tempFileResource(g.emote)
	if err != nil {
		return err
	}
	return g.LoadResource(res)
}

func (g *WebpWidget) LazyUnload() error {
	fyne.Do(func() {
		g.loadEmptyDst()
	})
	return nil
}

func (g *WebpWidget) Tapped(event *fyne.PointEvent) {
	if err := g.clickCallback(g.emote.Name + " "); err != nil {
		log.Println("Error running emote tap callback", err)
	}
}

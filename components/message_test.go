package components

import (
	"image/color"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

var (
	testEmoteSize   = fyne.NewSize(28, 28)
	testFontSize    = theme.TextSize()
	testMessageTime = time.Unix(0, 0).UTC()
)

func TestFlowLayoutKeepsObjectsOnOneLineWhenTheyFit(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	first := canvas.NewText("hello", theme.ForegroundColor())
	second := canvas.NewText("world", theme.ForegroundColor())
	content := container.New(NewFlowLayout(4, 2), first, second)

	width := first.MinSize().Width + 4 + second.MinSize().Width
	content.Resize(fyne.NewSize(width, 100))

	if first.Position().Y != second.Position().Y {
		t.Errorf("expected objects on the same line, got %f and %f", first.Position().Y, second.Position().Y)
	}
	if second.Position().X != first.MinSize().Width+4 {
		t.Errorf("expected second object at %f, got %f", first.MinSize().Width+4, second.Position().X)
	}
	if content.MinSize().Height != first.MinSize().Height {
		t.Errorf("expected a single line of height %f, got %f", first.MinSize().Height, content.MinSize().Height)
	}
}

func TestFlowLayoutWrapsObjectsThatDoNotFit(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	first := canvas.NewText("hello", theme.ForegroundColor())
	second := canvas.NewText("world", theme.ForegroundColor())
	layout := NewFlowLayout(4, 2)
	content := container.New(layout, first, second)

	content.Resize(fyne.NewSize(first.MinSize().Width, 100))

	if second.Position().Y <= first.Position().Y {
		t.Errorf("expected second object to wrap to a new line, got %f and %f", first.Position().Y, second.Position().Y)
	}
	if second.Position().X != 0 {
		t.Errorf("expected wrapped object at x 0, got %f", second.Position().X)
	}
	if content.MinSize().Height != first.MinSize().Height*2+2 {
		t.Errorf("expected two lines of height %f, got %f", first.MinSize().Height*2+2, content.MinSize().Height)
	}
}

func TestFlowLayoutCentersObjectsVertically(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	text := canvas.NewText("hello", theme.ForegroundColor())
	tall := canvas.NewText("world", theme.ForegroundColor())
	tall.TextSize = theme.TextSize() * 3
	content := container.New(NewFlowLayout(4, 2), text, tall)

	content.Resize(fyne.NewSize(500, 100))

	lineHeight := tall.MinSize().Height
	expected := (lineHeight - text.MinSize().Height) / 2
	if text.Position().Y != expected {
		t.Errorf("expected text centered at %f, got %f", expected, text.Position().Y)
	}
}

func TestMessageRendersTextAndEmotes(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	emote := &hasherino.Emote{Id: "id", Source: hasherino.SevenTV, Name: "FeelsDankMan"}
	emotes := map[string]*hasherino.Emote{emote.Name: emote}
	message := NewMessage("author", "", "hello FeelsDankMan world", testMessageTime, emotes, testEmoteSize, testFontSize, false, nil)

	emoteWidgets := 0
	words := 0
	for _, object := range message.content.Objects {
		switch object.(type) {
		case *WebpWidget:
			emoteWidgets++
		case *canvas.Text:
			words++
		}
	}

	if emoteWidgets != 1 {
		t.Errorf("expected 1 emote widget, got %d", emoteWidgets)
	}
	if words != 3 {
		t.Errorf("expected author and 2 words, got %d", words)
	}
	if len(message.emoteWidgets) != 1 {
		t.Errorf("expected 1 emote to be tracked, got %d", len(message.emoteWidgets))
	}
	if message.emoteWidgets[0].MinSize() != testEmoteSize {
		t.Errorf("expected emote min size %v, got %v", testEmoteSize, message.emoteWidgets[0].MinSize())
	}
}

func TestMessageSetEmotesOnlyRebuildsOnChange(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	emote := &hasherino.Emote{Id: "id", Source: hasherino.SevenTV, Name: "FeelsDankMan"}
	message := NewMessage("author", "", "hello FeelsDankMan", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)

	if len(message.emoteWidgets) != 0 {
		t.Errorf("expected no emotes to be rendered, got %d", len(message.emoteWidgets))
	}
	if !message.SetEmotes(map[string]*hasherino.Emote{emote.Name: emote}) {
		t.Error("expected message to be rebuilt when an emote becomes available")
	}
	if len(message.emoteWidgets) != 1 {
		t.Errorf("expected 1 emote to be rendered, got %d", len(message.emoteWidgets))
	}
	if message.SetEmotes(map[string]*hasherino.Emote{emote.Name: emote}) {
		t.Error("expected message not to be rebuilt when the emotes did not change")
	}
}

func TestMessageSetStyleOnlyRebuildsOnChange(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	emote := &hasherino.Emote{Id: "id", Source: hasherino.SevenTV, Name: "FeelsDankMan"}
	emotes := map[string]*hasherino.Emote{emote.Name: emote}
	message := NewMessage("author", "", "hello FeelsDankMan", testMessageTime, emotes, testEmoteSize, testFontSize, false, nil)

	if message.SetStyle(testEmoteSize, testFontSize, false) {
		t.Error("expected message not to be rebuilt when the style did not change")
	}

	biggerEmoteSize := fyne.NewSize(36, 36)
	if !message.SetStyle(biggerEmoteSize, testFontSize, false) {
		t.Error("expected message to be rebuilt when the emote size changed")
	}
	if message.emoteWidgets[0].MinSize() != biggerEmoteSize {
		t.Errorf("expected emote min size %v, got %v", biggerEmoteSize, message.emoteWidgets[0].MinSize())
	}

	if !message.SetStyle(biggerEmoteSize, 20, false) {
		t.Error("expected message to be rebuilt when the font size changed")
	}
	author := message.content.Objects[0].(*canvas.Text)
	if author.TextSize != 20 {
		t.Errorf("expected author text size 20, got %f", author.TextSize)
	}

	if !message.SetStyle(biggerEmoteSize, 20, true) {
		t.Error("expected message to be rebuilt when show message time changed")
	}
	timeText := message.content.Objects[0].(*canvas.Text)
	if timeText.TextStyle.Bold {
		t.Error("expected the message time to be non-bold")
	}
	if timeText.Text != "00:00" {
		t.Errorf("expected the message time 00:00, got %q", timeText.Text)
	}
}

func TestMessageColorsAuthorWithIrcColor(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	message := NewMessage("author", "#FF4500", "hello", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)

	author, ok := message.content.Objects[0].(*canvas.Text)
	if !ok {
		t.Fatalf("expected the first object to be the author, got %T", message.content.Objects[0])
	}
	want := color.RGBA{R: 0xff, G: 0x45, B: 0x00, A: 0xff}
	if author.Color != want {
		t.Errorf("expected the author color %v, got %v", want, author.Color)
	}
}

func TestMessageFallsBackToDefaultAuthorColor(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	for _, authorColor := range []string{"", "#GGGGGG", "#FF45"} {
		message := NewMessage("author", authorColor, "hello", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)

		author, ok := message.content.Objects[0].(*canvas.Text)
		if !ok {
			t.Fatalf("expected the first object to be the author, got %T", message.content.Objects[0])
		}
		if author.Color != theme.ForegroundColor() {
			t.Errorf("expected the default author color for %q, got %v", authorColor, author.Color)
		}
	}
}

func TestMessageRendersTimeBeforeAuthorWhenEnabled(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	timestamp := time.Unix(3600*15+120, 0).UTC()
	message := NewMessage("author", "", "hello", timestamp, nil, testEmoteSize, testFontSize, true, nil)

	if len(message.content.Objects) != 3 {
		t.Fatalf("expected the message time, author and word, got %d objects", len(message.content.Objects))
	}
	timeText, ok := message.content.Objects[0].(*canvas.Text)
	if !ok {
		t.Fatalf("expected the first object to be the message time, got %T", message.content.Objects[0])
	}
	if timeText.Text != "15:02" {
		t.Errorf("expected the message time 15:02, got %q", timeText.Text)
	}
	if timeText.TextStyle.Bold {
		t.Error("expected the message time to be non-bold")
	}
	if timeText.TextSize != testFontSize {
		t.Errorf("expected the message time to use font size %f, got %f", testFontSize, timeText.TextSize)
	}
	author := message.content.Objects[1].(*canvas.Text)
	if author.Text != "author:" || !author.TextStyle.Bold {
		t.Errorf("expected a bold author after the message time, got %q", author.Text)
	}
}

func TestMessageWrapsWhenNarrow(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	message := NewMessage("author", "", "a fairly long message that has to wrap over multiple lines", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)

	message.Resize(fyne.NewSize(500, 10))
	wide := message.MinSize().Height
	message.Resize(fyne.NewSize(120, 10))
	narrow := message.MinSize().Height

	if narrow <= wide {
		t.Errorf("expected the message to grow when narrowed, got %f and %f", wide, narrow)
	}
}

// blockingEmoteWidget blocks in LazyLoad until it is allowed to finish.
type blockingEmoteWidget struct {
	widget.BaseWidget
	min     fyne.Size
	starts  atomic.Int32
	loads   atomic.Int32
	unloads atomic.Int32
	allow   chan struct{}
}

func newBlockingEmoteWidget() *blockingEmoteWidget {
	w := &blockingEmoteWidget{allow: make(chan struct{})}
	w.ExtendBaseWidget(w)
	return w
}

func (w *blockingEmoteWidget) SetMinSize(size fyne.Size) {
	w.min = size
}

func (w *blockingEmoteWidget) MinSize() fyne.Size {
	return w.min
}

func (w *blockingEmoteWidget) LazyLoad() error {
	w.starts.Add(1)
	<-w.allow
	w.loads.Add(1)
	return nil
}

func (w *blockingEmoteWidget) LazyUnload() error {
	w.unloads.Add(1)
	return nil
}

func (w *blockingEmoteWidget) CreateRenderer() fyne.WidgetRenderer {
	return blockingEmoteWidgetRenderer{}
}

type blockingEmoteWidgetRenderer struct{}

func (blockingEmoteWidgetRenderer) Layout(fyne.Size)             {}
func (blockingEmoteWidgetRenderer) MinSize() fyne.Size           { return fyne.NewSize(1, 1) }
func (blockingEmoteWidgetRenderer) Refresh()                     {}
func (blockingEmoteWidgetRenderer) Objects() []fyne.CanvasObject { return nil }
func (blockingEmoteWidgetRenderer) Destroy()                     {}

func TestMessageLoadEmotesLoadsEmotesConcurrently(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	first, second := newBlockingEmoteWidget(), newBlockingEmoteWidget()
	message := NewMessage("author", "", "hello", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)
	message.emoteWidgets = []EmoteWidget{first, second}

	message.LoadEmotes()

	// both emotes start loading even though neither finished, so the loads
	// run concurrently instead of one after another
	deadline := time.Now().Add(time.Second)
	for (first.starts.Load() == 0 || second.starts.Load() == 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if first.starts.Load() == 0 || second.starts.Load() == 0 {
		t.Fatal("expected both emotes to start loading concurrently")
	}

	close(first.allow)
	close(second.allow)

	deadline = time.Now().Add(time.Second)
	for (first.loads.Load() == 0 || second.loads.Load() == 0) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if first.loads.Load() == 0 || second.loads.Load() == 0 {
		t.Fatal("expected both emotes to finish loading")
	}

	// the message stays loaded, so nothing is unloaded
	if first.unloads.Load() != 0 || second.unloads.Load() != 0 {
		t.Errorf("expected no emotes to be unloaded, got %d and %d", first.unloads.Load(), second.unloads.Load())
	}
}

func TestMessageLoadEmotesUnloadsEmotesThatFinishAfterUnload(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	emote := newBlockingEmoteWidget()
	message := NewMessage("author", "", "hello", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)
	message.emoteWidgets = []EmoteWidget{emote}

	message.LoadEmotes()

	deadline := time.Now().Add(time.Second)
	for emote.starts.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if emote.starts.Load() == 0 {
		t.Fatal("expected the emote to start loading")
	}

	// the message scrolls out of view while the emote is still loading
	message.UnloadEmotes()
	close(emote.allow)

	deadline = time.Now().Add(time.Second)
	for emote.loads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if emote.loads.Load() == 0 {
		t.Fatal("expected the emote to finish loading")
	}
	if emote.unloads.Load() == 0 {
		t.Error("expected the emote to be unloaded after finishing its load")
	}
}

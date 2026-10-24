package components

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"github.com/Hashy-Software/hasherino-go/hasherino"
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
	message := NewMessage("author", "hello FeelsDankMan world", emotes, nil)

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
	if len(message.emotes) != 1 {
		t.Errorf("expected 1 emote to be tracked, got %d", len(message.emotes))
	}
	if message.emotes[0].MinSize() != chatEmoteSize {
		t.Errorf("expected emote min size %v, got %v", chatEmoteSize, message.emotes[0].MinSize())
	}
}

func TestMessageSetEmotesOnlyRebuildsOnChange(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	emote := &hasherino.Emote{Id: "id", Source: hasherino.SevenTV, Name: "FeelsDankMan"}
	message := NewMessage("author", "hello FeelsDankMan", nil, nil)

	if len(message.emotes) != 0 {
		t.Errorf("expected no emotes to be rendered, got %d", len(message.emotes))
	}
	if !message.SetEmotes(map[string]*hasherino.Emote{emote.Name: emote}) {
		t.Error("expected message to be rebuilt when an emote becomes available")
	}
	if len(message.emotes) != 1 {
		t.Errorf("expected 1 emote to be rendered, got %d", len(message.emotes))
	}
	if message.SetEmotes(map[string]*hasherino.Emote{emote.Name: emote}) {
		t.Error("expected message not to be rebuilt when the emotes did not change")
	}
}

func TestMessageWrapsWhenNarrow(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	message := NewMessage("author", "a fairly long message that has to wrap over multiple lines", nil, nil)

	message.Resize(fyne.NewSize(500, 10))
	wide := message.MinSize().Height
	message.Resize(fyne.NewSize(120, 10))
	narrow := message.MinSize().Height

	if narrow <= wide {
		t.Errorf("expected the message to grow when narrowed, got %f and %f", wide, narrow)
	}
}

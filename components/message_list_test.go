package components

import (
	"testing"

	"fyne.io/fyne/v2"
	fyneCanvas "fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

func TestMessageListLaysOutWrappedMessages(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	list := NewMessageList()
	canvas := test.NewCanvas()
	canvas.SetContent(list)
	canvas.Resize(fyne.NewSize(300, 100))

	first := NewMessage("author", "a fairly long message that has to wrap over multiple lines", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)
	second := NewMessage("author", "another message that is long enough to wrap as well", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)
	list.AddMessage(first)
	list.AddMessage(second)

	if first.MinSize().Height <= first.Size().Height-0.001 || first.Size().Height > first.MinSize().Height {
		t.Errorf("expected the first message to be laid out at its minimum height %f, got %f", first.MinSize().Height, first.Size().Height)
	}
	if first.Size().Height <= 20 {
		t.Errorf("expected the first message to wrap over multiple lines, got height %f", first.Size().Height)
	}
	if second.Position().Y < first.Position().Y+first.Size().Height {
		t.Errorf("expected the second message at or below %f, got %f", first.Position().Y+first.Size().Height, second.Position().Y)
	}
}

func TestMessageListRelayoutsOnResize(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	list := NewMessageList()
	canvas := test.NewCanvas()
	canvas.SetContent(list)
	canvas.Resize(fyne.NewSize(400, 200))

	message := NewMessage("author", "a fairly long message that has to wrap over multiple lines", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)
	list.AddMessage(message)
	wide := message.Size().Height

	canvas.Resize(fyne.NewSize(200, 200))
	narrow := message.Size().Height

	if narrow <= wide {
		t.Errorf("expected the message to grow when the list is narrowed, got %f and %f", wide, narrow)
	}
	if message.Size().Width > 200 {
		t.Errorf("expected the message to fit the new width, got %f", message.Size().Width)
	}
}

func TestMessageListDropsOldestMessages(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	list := NewMessageList()
	list.SetLimit(2)
	canvas := test.NewCanvas()
	canvas.SetContent(list)
	canvas.Resize(fyne.NewSize(300, 200))

	first := NewMessage("first", "hello", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)
	second := NewMessage("second", "hello", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)
	third := NewMessage("third", "hello", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)
	list.AddMessage(first)
	list.AddMessage(second)
	list.AddMessage(third)

	messages := list.Messages()
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
	if messages[0] != second || messages[1] != third {
		t.Error("expected the oldest message to be dropped")
	}
	if len(list.content.Objects) != 2 {
		t.Errorf("expected 2 rendered messages, got %d", len(list.content.Objects))
	}
}

func TestMessageListRendersEmotesOfMessages(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	emote := &hasherino.Emote{Id: "id", Source: hasherino.SevenTV, Name: "FeelsDankMan"}
	list := NewMessageList()
	canvas := test.NewCanvas()
	canvas.SetContent(list)
	canvas.Resize(fyne.NewSize(300, 200))

	message := NewMessage("author", "hello FeelsDankMan", testMessageTime, nil, testEmoteSize, testFontSize, false, nil)
	list.AddMessage(message)
	list.UpdateEmotes(map[string]*hasherino.Emote{emote.Name: emote})

	if len(message.emoteWidgets) != 1 {
		t.Errorf("expected the message to render 1 emote, got %d", len(message.emoteWidgets))
	}
	if message.emoteWidgets[0].Position().X <= 0 {
		t.Errorf("expected the emote to be laid out after the first word, got %f", message.emoteWidgets[0].Position().X)
	}
}

func TestMessageListSetStyle(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	emote := &hasherino.Emote{Id: "id", Source: hasherino.SevenTV, Name: "FeelsDankMan"}
	list := NewMessageList()
	canvas := test.NewCanvas()
	canvas.SetContent(list)
	canvas.Resize(fyne.NewSize(300, 200))

	message := NewMessage("author", "hello FeelsDankMan", testMessageTime, map[string]*hasherino.Emote{emote.Name: emote}, testEmoteSize, testFontSize, false, nil)
	list.AddMessage(message)

	list.SetStyle(fyne.NewSize(36, 36), 20, false)

	if message.emoteWidgets[0].MinSize() != fyne.NewSize(36, 36) {
		t.Errorf("expected emote min size 36x36, got %v", message.emoteWidgets[0].MinSize())
	}
	if message.Size().Height <= testEmoteSize.Height {
		t.Errorf("expected the message to grow with the new style, got height %f", message.Size().Height)
	}

	unchangedEmote := message.emoteWidgets[0]
	list.SetStyle(fyne.NewSize(36, 36), 20, false)
	if message.emoteWidgets[0] != unchangedEmote {
		t.Error("expected the message not to be rebuilt when the style did not change")
	}

	list.SetStyle(fyne.NewSize(36, 36), 20, true)
	timeText, ok := message.content.Objects[0].(*fyneCanvas.Text)
	if !ok || timeText.Text != "00:00" {
		t.Errorf("expected the message to be rebuilt with the message time, got %T", message.content.Objects[0])
	}
}

package components

import (
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

// MessageList is a scrollable list of chat messages. Only the emotes of the
// messages that are currently visible are loaded, so that offscreen animated
// emotes don't consume any cpu.
type MessageList struct {
	widget.BaseWidget

	scroll  *container.Scroll
	content *fyne.Container

	lock     sync.Mutex
	messages []*Message
	limit    int
}

func NewMessageList() *MessageList {
	list := &MessageList{
		content: container.New(layout.NewVBoxLayout()),
	}
	list.ExtendBaseWidget(list)
	list.scroll = container.NewVScroll(list.content)
	list.scroll.OnScrolled = func(_ fyne.Position) {
		list.updateVisibleEmotes()
	}
	return list
}

func (l *MessageList) CreateRenderer() fyne.WidgetRenderer {
	return &messageListRenderer{list: l}
}

// SetLimit sets the maximum amount of messages that are kept, the oldest
// messages are dropped first. A limit of zero keeps every message.
func (l *MessageList) SetLimit(limit int) {
	l.lock.Lock()
	l.limit = limit
	l.lock.Unlock()
}

// AddMessage appends a message to the list and drops the oldest messages
// once the limit is reached.
func (l *MessageList) AddMessage(message *Message) {
	l.lock.Lock()
	l.messages = append(l.messages, message)
	var dropped []*Message
	for l.limit > 0 && len(l.messages) > l.limit {
		dropped = append(dropped, l.messages[0])
		l.messages = l.messages[1:]
	}
	l.lock.Unlock()

	l.content.Add(message)
	for _, message := range dropped {
		message.UnloadEmotes()
		l.content.Remove(message)
	}

	l.Refresh()
	l.updateVisibleEmotes()
}

// UpdateEmotes re-renders the messages whose emotes changed, for example
// because the emotes of the channel finished loading.
func (l *MessageList) UpdateEmotes(emotes map[string]*hasherino.Emote) {
	changed := false
	for _, message := range l.Messages() {
		if message.SetEmotes(emotes) {
			changed = true
		}
	}
	if changed {
		l.Refresh()
		l.updateVisibleEmotes()
	}
}

// SetStyle re-renders the messages whose emote or font size changed, for
// example because the user changed the settings.
func (l *MessageList) SetStyle(emoteSize fyne.Size, fontSize float32) {
	changed := false
	for _, message := range l.Messages() {
		if message.SetStyle(emoteSize, fontSize) {
			changed = true
		}
	}
	if changed {
		l.Refresh()
		l.updateVisibleEmotes()
	}
}

func (l *MessageList) Messages() []*Message {
	l.lock.Lock()
	defer l.lock.Unlock()

	messages := make([]*Message, len(l.messages))
	copy(messages, l.messages)
	return messages
}

func (l *MessageList) ScrollToBottom() {
	l.scroll.ScrollToBottom()
	l.updateVisibleEmotes()
}

func (l *MessageList) updateVisibleEmotes() {
	viewport := l.scroll.Size()
	offset := l.scroll.Offset
	for _, message := range l.Messages() {
		position := message.Position()
		size := message.Size()
		isVisible := position.Y+size.Height > offset.Y &&
			position.Y < offset.Y+viewport.Height
		if isVisible {
			message.LoadEmotes()
		} else {
			message.UnloadEmotes()
		}
	}
}

// relayout runs the layout of the content a second time. The height of a
// message is only known once it received its width, as its text wraps.
func (l *MessageList) relayout() {
	size := l.content.Size()
	if size.Width <= 0 {
		return
	}
	l.content.Layout.Layout(l.content.Objects, size)
}

type messageListRenderer struct {
	list *MessageList
}

func (r *messageListRenderer) Destroy() {
}

func (r *messageListRenderer) Layout(size fyne.Size) {
	r.list.scroll.Resize(size)
	r.list.relayout()
}

func (r *messageListRenderer) MinSize() fyne.Size {
	return r.list.scroll.MinSize()
}

func (r *messageListRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.list.scroll}
}

func (r *messageListRenderer) Refresh() {
	r.list.scroll.Refresh()
	r.list.relayout()
}

package components

import (
	"log"
	"slices"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

var chatEmoteSize = fyne.NewSize(28, 28)

// Message renders a single chat message, replacing the names of known emotes
// with their emote widget and displaying the rest as text.
type Message struct {
	widget.BaseWidget

	author        string
	text          string
	onEmoteTapped func(string) error
	content       *fyne.Container

	lock         sync.Mutex
	emotes       []EmoteWidget
	emoteNames   []string
	emotesLoaded bool
}

func NewMessage(author string, text string, emotes map[string]*hasherino.Emote, onEmoteTapped func(string) error) *Message {
	spaceWidth := fyne.MeasureText(" ", theme.TextSize(), fyne.TextStyle{}).Width

	message := &Message{
		author:        author,
		text:          text,
		onEmoteTapped: onEmoteTapped,
		content:       container.New(NewFlowLayout(spaceWidth, theme.Padding())),
	}
	message.ExtendBaseWidget(message)
	message.emoteNames = message.matchedEmoteNames(emotes)
	message.render(emotes)
	return message
}

func (m *Message) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(m.content)
}

// SetEmotes rebuilds the message if the emotes it should render changed.
// It returns true when the message was rebuilt.
func (m *Message) SetEmotes(emotes map[string]*hasherino.Emote) bool {
	names := m.matchedEmoteNames(emotes)

	m.lock.Lock()
	if slices.Equal(names, m.emoteNames) {
		m.lock.Unlock()
		return false
	}
	reload := m.emotesLoaded
	m.emoteNames = names
	m.emotes = nil
	m.emotesLoaded = false
	m.lock.Unlock()

	m.render(emotes)

	if reload {
		m.LoadEmotes()
	}
	return true
}

// LoadEmotes starts loading the emotes of this message. It is meant to be
// called when the message scrolls into view.
func (m *Message) LoadEmotes() {
	m.lock.Lock()
	if m.emotesLoaded {
		m.lock.Unlock()
		return
	}
	emotes := m.emotes
	m.emotesLoaded = len(emotes) > 0
	m.lock.Unlock()

	if len(emotes) == 0 {
		return
	}

	go func() {
		for _, emote := range emotes {
			if err := emote.LazyLoad(); err != nil {
				log.Printf("Failed to load emote: %s", err)
			}
			if !m.IsEmotesLoaded() {
				if err := emote.LazyUnload(); err != nil {
					log.Printf("Failed to unload emote: %s", err)
				}
				return
			}
		}
	}()
}

// UnloadEmotes stops the emotes of this message. It is meant to be called when
// the message scrolls out of view.
func (m *Message) UnloadEmotes() {
	m.lock.Lock()
	if !m.emotesLoaded {
		m.lock.Unlock()
		return
	}
	m.emotesLoaded = false
	emotes := m.emotes
	m.lock.Unlock()

	for _, emote := range emotes {
		if err := emote.LazyUnload(); err != nil {
			log.Printf("Failed to unload emote: %s", err)
		}
	}
}

func (m *Message) IsEmotesLoaded() bool {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.emotesLoaded
}

func (m *Message) matchedEmoteNames(emotes map[string]*hasherino.Emote) []string {
	names := []string{}
	for _, word := range strings.Fields(m.text) {
		if emote, ok := emotes[word]; ok {
			names = append(names, emote.Name)
		}
	}
	return names
}

func (m *Message) render(emotes map[string]*hasherino.Emote) {
	objects := []fyne.CanvasObject{newMessageText(m.author+":", fyne.TextStyle{Bold: true})}
	emoteWidgets := []EmoteWidget{}

	for _, word := range strings.Fields(m.text) {
		emote, ok := emotes[word]
		if !ok {
			objects = append(objects, newMessageText(word, fyne.TextStyle{}))
			continue
		}

		emoteWidget, err := NewEmote(emote, m.onEmoteTapped)
		if err != nil {
			log.Printf("Failed to create emote %s: %s", emote.Name, err)
			objects = append(objects, newMessageText(word, fyne.TextStyle{}))
			continue
		}

		emoteWidget.SetMinSize(chatEmoteSize)
		emoteWidgets = append(emoteWidgets, emoteWidget)
		objects = append(objects, emoteWidget)
	}

	m.content.RemoveAll()
	for _, object := range objects {
		m.content.Add(object)
	}

	m.lock.Lock()
	m.emotes = emoteWidgets
	m.lock.Unlock()

	m.Refresh()
}

func newMessageText(text string, style fyne.TextStyle) *canvas.Text {
	object := canvas.NewText(text, theme.ForegroundColor())
	object.TextSize = theme.TextSize()
	object.TextStyle = style
	return object
}

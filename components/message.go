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

// Message renders a single chat message, replacing the names of known emotes
// with their emote widget and displaying the rest as text.
type Message struct {
	widget.BaseWidget

	author        string
	text          string
	onEmoteTapped func(string) error
	emoteSize     fyne.Size
	fontSize      float32
	content       *fyne.Container

	lock         sync.Mutex
	emotes       map[string]*hasherino.Emote
	emoteWidgets []EmoteWidget
	emoteNames   []string
	emotesLoaded bool
}

func NewMessage(author string, text string, emotes map[string]*hasherino.Emote, emoteSize fyne.Size, fontSize float32, onEmoteTapped func(string) error) *Message {
	message := &Message{
		author:        author,
		text:          text,
		onEmoteTapped: onEmoteTapped,
		emoteSize:     emoteSize,
		fontSize:      fontSize,
		emotes:        emotes,
		content:       container.New(newMessageLayout(fontSize)),
	}
	message.ExtendBaseWidget(message)
	message.emoteNames = message.matchedEmoteNames(emotes)
	message.render(emotes)
	return message
}

func newMessageLayout(fontSize float32) *FlowLayout {
	spaceWidth := fyne.MeasureText(" ", fontSize, fyne.TextStyle{}).Width
	return NewFlowLayout(spaceWidth, theme.Padding())
}

func (m *Message) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(m.content)
}

// SetEmotes rebuilds the message if the emotes it should render changed.
// It returns true when the message was rebuilt.
func (m *Message) SetEmotes(emotes map[string]*hasherino.Emote) bool {
	names := m.matchedEmoteNames(emotes)

	m.lock.Lock()
	m.emotes = emotes
	if slices.Equal(names, m.emoteNames) {
		m.lock.Unlock()
		return false
	}
	discarded := m.emoteWidgets
	reload := m.emotesLoaded
	m.emoteNames = names
	m.emoteWidgets = nil
	m.emotesLoaded = false
	m.lock.Unlock()
	unloadEmotes(discarded)

	m.render(emotes)

	if reload {
		m.LoadEmotes()
	}
	return true
}

// SetStyle rebuilds the message if its emote or font size changed.
// It returns true when the message was rebuilt.
func (m *Message) SetStyle(emoteSize fyne.Size, fontSize float32) bool {
	m.lock.Lock()
	if m.emoteSize == emoteSize && m.fontSize == fontSize {
		m.lock.Unlock()
		return false
	}
	emotes := m.emotes
	discarded := m.emoteWidgets
	reload := m.emotesLoaded
	m.emoteSize = emoteSize
	m.fontSize = fontSize
	m.emoteWidgets = nil
	m.emotesLoaded = false
	m.lock.Unlock()
	unloadEmotes(discarded)

	m.content.Layout = newMessageLayout(fontSize)
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
	emoteWidgets := m.emoteWidgets
	m.emotesLoaded = len(emoteWidgets) > 0
	m.lock.Unlock()

	if len(emoteWidgets) == 0 {
		return
	}

	go func() {
		for _, emote := range emoteWidgets {
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
	emoteWidgets := m.emoteWidgets
	m.lock.Unlock()
	unloadEmotes(emoteWidgets)
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
	objects := []fyne.CanvasObject{newMessageText(m.author+":", fyne.TextStyle{Bold: true}, m.fontSize)}
	emoteWidgets := []EmoteWidget{}

	for _, word := range strings.Fields(m.text) {
		emote, ok := emotes[word]
		if !ok {
			objects = append(objects, newMessageText(word, fyne.TextStyle{}, m.fontSize))
			continue
		}

		emoteWidget, err := NewEmote(emote, m.onEmoteTapped)
		if err != nil {
			log.Printf("Failed to create emote %s: %s", emote.Name, err)
			objects = append(objects, newMessageText(word, fyne.TextStyle{}, m.fontSize))
			continue
		}

		emoteWidget.SetMinSize(m.emoteSize)
		emoteWidgets = append(emoteWidgets, emoteWidget)
		objects = append(objects, emoteWidget)
	}

	m.lock.Lock()
	m.emoteWidgets = emoteWidgets
	m.lock.Unlock()

	m.content.RemoveAll()
	for _, object := range objects {
		m.content.Add(object)
	}

	m.Refresh()
}

func unloadEmotes(emoteWidgets []EmoteWidget) {
	for _, emote := range emoteWidgets {
		if err := emote.LazyUnload(); err != nil {
			log.Printf("Failed to unload emote: %s", err)
		}
	}
}

func newMessageText(text string, style fyne.TextStyle, fontSize float32) *canvas.Text {
	object := canvas.NewText(text, theme.ForegroundColor())
	object.TextSize = fontSize
	object.TextStyle = style
	return object
}

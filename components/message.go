package components

import (
	"image/color"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

const messageTimeFormat = "15:04"

// Message renders a single chat message, replacing the names of known emotes
// with their emote widget and displaying the rest as text.
type Message struct {
	widget.BaseWidget

	author          string
	authorColor     string
	text            string
	timestamp       time.Time
	onEmoteTapped   func(string) error
	emoteSize       fyne.Size
	fontSize        float32
	showMessageTime bool
	content         *fyne.Container

	lock         sync.Mutex
	emotes       map[string]*hasherino.Emote
	emoteWidgets []EmoteWidget
	emoteNames   []string
	emotesLoaded bool
}

// NewMessage creates a message. The authorColor is the hex color of the
// author's name from the irc tags, an empty or invalid color falls back to
// the default text color.
func NewMessage(author string, authorColor string, text string, timestamp time.Time, emotes map[string]*hasherino.Emote, emoteSize fyne.Size, fontSize float32, showMessageTime bool, onEmoteTapped func(string) error) *Message {
	message := &Message{
		author:          author,
		authorColor:     authorColor,
		text:            text,
		timestamp:       timestamp,
		onEmoteTapped:   onEmoteTapped,
		emoteSize:       emoteSize,
		fontSize:        fontSize,
		showMessageTime: showMessageTime,
		emotes:          emotes,
		content:         container.New(newMessageLayout(fontSize)),
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

// SetStyle rebuilds the message if settings like emote or font size changed.
// It returns true when the message was rebuilt.
func (m *Message) SetStyle(emoteSize fyne.Size, fontSize float32, showMessageTime bool) bool {
	m.lock.Lock()
	if m.emoteSize == emoteSize && m.fontSize == fontSize && m.showMessageTime == showMessageTime {
		m.lock.Unlock()
		return false
	}
	emotes := m.emotes
	discarded := m.emoteWidgets
	reload := m.emotesLoaded
	m.emoteSize = emoteSize
	m.fontSize = fontSize
	m.showMessageTime = showMessageTime
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
// called when the message scrolls into view. The emotes load concurrently, so
// a slow emote does not delay the others.
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

	var wg sync.WaitGroup
	for _, emote := range emoteWidgets {
		wg.Add(1)
		go func(emote EmoteWidget) {
			defer wg.Done()
			if err := emote.LazyLoad(); err != nil {
				log.Printf("Failed to load emote: %s", err)
			}
		}(emote)
	}
	go func() {
		wg.Wait()
		// the message scrolled out of view while loading, so undo the load
		if !m.IsEmotesLoaded() {
			unloadEmotes(emoteWidgets)
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
	objects := []fyne.CanvasObject{}
	if m.showMessageTime {
		objects = append(objects, newMessageText(m.timestamp.Format(messageTimeFormat), fyne.TextStyle{}, m.fontSize))
	}
	author := newMessageText(m.author+":", fyne.TextStyle{Bold: true}, m.fontSize)
	if ircColor, ok := parseHexColor(m.authorColor); ok {
		author.Color = ircColor
	}
	objects = append(objects, author)
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

// parseHexColor parses the #RRGGBB colors that irc messages carry.
func parseHexColor(hex string) (color.Color, bool) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return nil, false
	}
	value, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return nil, false
	}
	return color.RGBA{R: uint8(value >> 16), G: uint8(value >> 8), B: uint8(value), A: 0xff}, true
}

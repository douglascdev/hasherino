package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"gioui.org/app"
	"gioui.org/io/clipboard"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	giotext "gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/exp/shiny/materialdesign/icons"
	_ "golang.org/x/image/webp"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

const deviceClientID = "9ofmajflpp4p2hrq81vjdyvcwlad97"

type tabItem struct {
	id          string
	login       string
	title       string
	button      widget.Clickable
	closeButton widget.Clickable
	chat        *chat
}

type chatPart struct {
	text  string
	emote *hasherino.Emote
	media *chatMedia
}

type chatMessage struct {
	author      string
	authorColor color.NRGBA
	text        string
	sentAt      time.Time
	media       *chatMedia
	parts       []chatPart
	// ownEmotes are the emotes the message itself carries from its irc tags,
	// keyed by name. They win over the emotes of the channel.
	ownEmotes map[string]*hasherino.Emote
	// rawBadges are the badges the message carries from its irc tags. The
	// image urls are resolved from the channel's badge sets into badgeURLs.
	rawBadges []hasherino.Badge
	badgeURLs []string
}

type chatMedia struct {
	frames []paint.ImageOp
	delays []time.Duration
	total  time.Duration
	start  time.Time
	width  int
	height int
}

type chat struct {
	list        widget.List
	mu          sync.Mutex
	messages    []chatMessage
	limit       int
	scrollToEnd bool
	// emotes are the emotes that can be rendered in the channel, keyed by
	// name. They are replaced when the channel emotes finish loading.
	emotes map[string]*hasherino.Emote
	// badges are the badge image urls of the channel, keyed by badge set and
	// version. They are replaced when the channel badges finish loading.
	badges map[string]map[string]string
	// showTime controls whether messages render their timestamp.
	showTime bool
	// emoteSize is the height inline emotes are rendered at.
	emoteSize unit.Dp
	// window is used to repaint the chat when an emote finishes loading.
	window *app.Window
}

const (
	defaultChatMessageLimit = 500

	// chatMessageTimeFormat matches the timestamp format the Fyne UI uses.
	chatMessageTimeFormat = "15:04"

	// The emotes of a channel are loaded in the background after its tab is
	// created. The first checks are fast so freshly loaded emotes are picked
	// up quickly, later checks back off to not hammer the database.
	channelEmotePollFirstInterval = 100 * time.Millisecond
	channelEmotePollMaxInterval   = 2 * time.Second
	channelEmotePollAttempts      = 40
)

func (c *chat) setLimit(limit int) {
	if limit <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.limit = limit
	if len(c.messages) > limit {
		c.messages = c.messages[len(c.messages)-limit:]
	}
}

// setShowTime controls whether timestamps are rendered before messages.
func (c *chat) setShowTime(show bool) {
	c.showTime = show
}

// setEmoteSize sets the height inline emotes are rendered at.
func (c *chat) setEmoteSize(size int) {
	if size <= 0 {
		return
	}
	c.emoteSize = unit.Dp(float32(size))
}

// requestScrollToEnd asks the chat to scroll to its last message the next
// time it is laid out.
func (c *chat) requestScrollToEnd() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scrollToEnd = true
}

// takeScrollToEnd reports and clears a pending scroll-to-end request.
func (c *chat) takeScrollToEnd() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.scrollToEnd {
		return false
	}
	c.scrollToEnd = false
	return true
}

func (c *chat) append(message chatMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.limit <= 0 {
		c.limit = defaultChatMessageLimit
	}
	message.parts = c.buildPartsLocked(message.text, message.ownEmotes)
	message.badgeURLs = c.resolveBadgesLocked(message.rawBadges)
	c.messages = append(c.messages, message)
	if len(c.messages) > c.limit {
		c.messages = c.messages[len(c.messages)-c.limit:]
	}
}

// setBadges replaces the badge sets that can be rendered in the channel and
// resolves the badges of the messages that were already appended.
func (c *chat) setBadges(badges map[string]map[string]string) {
	c.mu.Lock()
	c.badges = badges
	for i := range c.messages {
		message := &c.messages[i]
		message.badgeURLs = c.resolveBadgesLocked(message.rawBadges)
	}
	c.mu.Unlock()
}

// resolveBadgesLocked returns the image urls of the badges in display order.
// Badges without a known image are skipped.
func (c *chat) resolveBadgesLocked(badges []hasherino.Badge) []string {
	if len(badges) == 0 || len(c.badges) == 0 {
		return nil
	}
	urls := make([]string, 0, len(badges))
	for _, badge := range badges {
		versions := c.badges[badge.Name]
		if versions == nil {
			continue
		}
		if url := versions[badge.Version]; url != "" {
			urls = append(urls, url)
		}
	}
	return urls
}

// setEmotes replaces the emotes that can be rendered in the channel and
// rebuilds the messages whose text may now render as emotes.
func (c *chat) setEmotes(emotes []*hasherino.Emote) {
	byName := make(map[string]*hasherino.Emote, len(emotes))
	for _, emote := range emotes {
		if emote != nil && emote.Name != "" {
			byName[emote.Name] = emote
		}
	}

	c.mu.Lock()
	c.emotes = byName
	for i := range c.messages {
		message := &c.messages[i]
		message.parts = c.buildPartsLocked(message.text, message.ownEmotes)
	}
	c.mu.Unlock()
}

// buildPartsLocked splits the text of a message into text and emote parts.
// The emotes of the message win over the emotes of the channel, because the
// irc tags mark exactly what twitch rendered as an emote.
func (c *chat) buildPartsLocked(text string, ownEmotes map[string]*hasherino.Emote) []chatPart {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	parts := make([]chatPart, 0, len(words))
	var textPart strings.Builder
	flushText := func() {
		if textPart.Len() == 0 {
			return
		}
		parts = append(parts, chatPart{text: textPart.String()})
		textPart.Reset()
	}

	for _, word := range words {
		emote := ownEmotes[word]
		if emote == nil {
			emote = c.emotes[word]
		}
		if emote == nil {
			if textPart.Len() > 0 {
				textPart.WriteByte(' ')
			}
			textPart.WriteString(word)
			continue
		}

		flushText()
		parts = append(parts, chatPart{
			emote: emote,
			media: cachedChatEmoteMedia(emote),
		})
	}
	flushText()
	return parts
}

// requestEmotes starts loading the emotes of a message. It is called while
// the message is laid out, so only the emotes of messages in view are loaded.
func (c *chat) requestEmotes(message chatMessage) {
	if c.window == nil {
		return
	}
	for _, part := range message.parts {
		if part.emote != nil {
			loadChatEmote(c.window, part.emote)
		}
	}
}

// requestBadges starts loading the badge images of a message. It is called
// while the message is laid out, so only the badges of messages in view are
// loaded.
func (c *chat) requestBadges(message chatMessage) {
	if c.window == nil {
		return
	}
	for _, url := range message.badgeURLs {
		loadChatBadge(c.window, url)
	}
}

func (c *chat) snapshot() []chatMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	messages := make([]chatMessage, len(c.messages))
	copy(messages, c.messages)
	return messages
}

type userEntry struct {
	id     string
	login  string
	token  string
	active bool
	button widget.Clickable
}

type usersMenuState struct {
	open     bool
	selected int
	next     int
	users    []userEntry
	add      widget.Clickable
	remove   widget.Clickable
	onAdd    func()
	onRemove func(index int)
}

type deviceAuthEvent struct {
	device *hasherino.DeviceAuthorization
	token  *hasherino.DeviceToken
	id     string
	login  string
	err    error
}

type deviceAuthState struct {
	open            bool
	pending         bool
	userCode        string
	verificationURI string
	errText         string
	openErr         string
	events          <-chan deviceAuthEvent
	cancel          context.CancelFunc
	onAccount       func(id, login, token string) error
	backdrop        widget.Clickable
	openButton      widget.Clickable
	copyButton      widget.Clickable
	copyLinkButton  widget.Clickable
	closeButton     widget.Clickable
}

type addTabEvent struct {
	tab *hasherino.Tab
	err error
}

type addTabState struct {
	open         bool
	pending      bool
	errText      string
	editor       widget.Editor
	events       <-chan addTabEvent
	backdrop     widget.Clickable
	addButton    widget.Clickable
	cancelButton widget.Clickable
}

// settingsState holds the mobile-friendly settings dialog. It mirrors the
// general options of the Fyne settings window and saves them through the
// controller.
type settingsState struct {
	open bool

	settings hasherino.AppSettings
	loaded   bool
	errText  string

	backdrop   widget.Clickable
	panel      widget.Clickable
	doneButton widget.Clickable

	// list keeps the scroll position of the settings.
	list layout.List

	chatLimitEditor widget.Editor
	emoteSizeEditor widget.Editor
	fontSizeEditor  widget.Editor
	showTime        widget.Bool
	chatHistory     widget.Bool

	onSave func(hasherino.AppSettings)
}

const (
	emoteSuggestionLimit = 10
	emoteSearchDelay     = 200 * time.Millisecond
	minEmoteQueryLength  = 3
	maxMessageLength     = 500
)

type emoteSearchEvent struct {
	requestID uint64
	query     string
	emotes    []*hasherino.Emote
	err       error
}

type emoteSuggestionRow struct {
	emote  *hasherino.Emote
	button widget.Clickable
}

type messageInputState struct {
	editor       widget.Editor
	sendButton   widget.Clickable
	suggestions  []*hasherino.Emote
	rows         []emoteSuggestionRow
	selected     int
	visible      bool
	errText      string
	lastText     string
	searchTimer  *time.Timer
	requestID    uint64
	searchEvents chan emoteSearchEvent
}

func newMessageInputState() *messageInputState {
	state := &messageInputState{
		searchEvents: make(chan emoteSearchEvent, 1),
	}
	state.editor.SingleLine = true
	return state
}

func (s *messageInputState) drainSearchEvents() {
	for {
		select {
		case event := <-s.searchEvents:
			if event.requestID != s.requestID {
				continue
			}
			if event.err != nil {
				log.Printf("Failed to search emotes for %q: %s", event.query, event.err)
				s.hideSuggestions()
				continue
			}
			s.showSuggestions(event.emotes)
		default:
			return
		}
	}
}

func (s *messageInputState) showSuggestions(emotes []*hasherino.Emote) {
	suggestions := make([]*hasherino.Emote, 0, len(emotes))
	for _, emote := range emotes {
		if emote != nil && emote.Name != "" {
			suggestions = append(suggestions, emote)
		}
	}
	if len(suggestions) == 0 {
		s.hideSuggestions()
		return
	}

	s.suggestions = suggestions
	s.rows = make([]emoteSuggestionRow, len(suggestions))
	for i, suggestion := range suggestions {
		s.rows[i] = emoteSuggestionRow{emote: suggestion}
	}
	if s.selected >= len(suggestions) {
		s.selected = 0
	}
	s.visible = true
}

func (s *messageInputState) hideSuggestions() {
	s.cancelSearch()
	s.suggestions = nil
	s.rows = nil
	s.selected = 0
	s.visible = false
}

func (s *messageInputState) cancelSearch() {
	s.requestID++
	if s.searchTimer != nil {
		s.searchTimer.Stop()
		s.searchTimer = nil
	}
}

func (s *messageInputState) scheduleSearch(window *app.Window, hc *hasherino.HasherinoController, query string) {
	s.cancelSearch()
	requestID := s.requestID

	s.searchTimer = time.AfterFunc(emoteSearchDelay, func() {
		emotes, err := hc.SearchEmotes(query, emoteSuggestionLimit)
		s.searchEvents <- emoteSearchEvent{
			requestID: requestID,
			query:     query,
			emotes:    emotes,
			err:       err,
		}
		window.Invalidate()
	})
}

func (s *messageInputState) setText(text string) {
	s.editor.SetText(text)
	s.lastText = text
}

func (s *messageInputState) acceptSuggestion(gtx layout.Context, name string) {
	_, col := s.editor.CaretPos()
	text, cursor, ok := replaceEmoteToken(s.editor.Text(), col, name)
	if !ok {
		s.hideSuggestions()
		return
	}
	s.hideSuggestions()
	s.setText(text)
	s.editor.SetCaret(cursor, cursor)
	gtx.Execute(key.FocusCmd{Tag: &s.editor})
}

func (s *messageInputState) acceptSelected(gtx layout.Context) {
	if s.selected < 0 || s.selected >= len(s.suggestions) {
		s.hideSuggestions()
		return
	}
	s.acceptSuggestion(gtx, s.suggestions[s.selected].Name)
}

func (s *messageInputState) completeTab(gtx layout.Context, hc *hasherino.HasherinoController) {
	_, col := s.editor.CaretPos()
	tokenStart, tokenEnd, query, ok := tabTokenAt(s.editor.Text(), col)
	if !ok {
		return
	}

	s.cancelSearch()
	emotes, err := hc.SearchEmotes(query, 1)
	if err != nil {
		log.Printf("Failed to search emotes for %q: %s", query, err)
		return
	}
	if len(emotes) == 0 || emotes[0] == nil {
		return
	}

	text, cursor := replaceToken(s.editor.Text(), tokenStart, tokenEnd, emotes[0].Name)
	s.hideSuggestions()
	s.setText(text)
	s.editor.SetCaret(cursor, cursor)
	gtx.Execute(key.FocusCmd{Tag: &s.editor})
}

func (s *messageInputState) send(gtx layout.Context, hc *hasherino.HasherinoController, selectedLogin string) {
	if selectedLogin == "" {
		return
	}
	text := strings.TrimSpace(s.editor.Text())
	if text == "" {
		return
	}
	if len([]rune(text)) > maxMessageLength {
		s.errText = "Message too long"
		return
	}
	if err := hc.SendMessage(selectedLogin, text); err != nil {
		s.errText = err.Error()
		return
	}
	s.errText = ""
	s.hideSuggestions()
	s.setText("")
	gtx.Execute(key.FocusCmd{Tag: &s.editor})
}

func (a *addTabState) drain(handle func(addTabEvent)) {
	for a.events != nil {
		select {
		case event, ok := <-a.events:
			if !ok {
				a.events = nil
				return
			}
			handle(event)
		default:
			return
		}
	}
}

func (a *addTabState) close() {
	a.events = nil
	a.open = false
	a.pending = false
	a.errText = ""
	a.editor.SetText("")
}

func startAddTab(window *app.Window, hc *hasherino.HasherinoController, channel string) <-chan addTabEvent {
	events := make(chan addTabEvent, 1)

	go func() {
		defer close(events)

		emit := func(event addTabEvent) {
			select {
			case events <- event:
			default:
			}
			window.Invalidate()
		}

		if err := hc.AddTab(channel); err != nil {
			emit(addTabEvent{err: err})
			return
		}

		tabs, err := hc.GetTabs()
		if err != nil {
			emit(addTabEvent{err: err})
			return
		}
		for _, tab := range tabs {
			if strings.EqualFold(tab.Login, channel) {
				emit(addTabEvent{tab: tab})
				return
			}
		}
		emit(addTabEvent{err: errors.New("tab not found after adding")})
	}()

	return events
}

func layoutTabs(gtx layout.Context, theme *material.Theme, tabs []tabItem, selected int, closeIcon *widget.Icon) []layout.Widget {
	widgets := make([]layout.Widget, len(tabs))
	for i := range tabs {
		i := i
		tab := &tabs[i]
		widgets[i] = func(gtx layout.Context) layout.Dimensions {
			return layoutTab(gtx, theme, tab, i == selected, closeIcon)
		}
	}
	return widgets
}

func layoutTab(gtx layout.Context, theme *material.Theme, tab *tabItem, selected bool, closeIcon *widget.Icon) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layoutTabButton(gtx, theme, tab, selected)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layoutTabCloseButton(gtx, theme, &tab.closeButton, closeIcon)
		}),
	)
}

func layoutTabButton(gtx layout.Context, theme *material.Theme, tab *tabItem, selected bool) layout.Dimensions {
	return tab.button.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{
			Top:    unit.Dp(4),
			Bottom: unit.Dp(4),
			Left:   unit.Dp(8),
			Right:  unit.Dp(4),
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Stack{Alignment: layout.S}.Layout(gtx,
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					return layoutTabLabel(gtx, theme, tab.title, selected)
				}),
				layout.Expanded(func(gtx layout.Context) layout.Dimensions {
					return layoutTabUnderline(gtx, theme, selected)
				}),
			)
		})
	})
}

func layoutTabLabel(gtx layout.Context, theme *material.Theme, title string, selected bool) layout.Dimensions {
	label := material.Body1(theme, title)
	label.Color = theme.Fg
	if !selected {
		label.Color.A = 0x99
	}
	return label.Layout(gtx)
}

func layoutTabUnderline(gtx layout.Context, theme *material.Theme, selected bool) layout.Dimensions {
	if !selected {
		return layout.Dimensions{}
	}
	size := image.Point{X: gtx.Constraints.Min.X, Y: gtx.Dp(unit.Dp(2))}
	paint.FillShape(gtx.Ops, theme.Fg, clip.Rect{Max: size}.Op())
	return layout.Dimensions{Size: size}
}

func layoutTabCloseButton(gtx layout.Context, theme *material.Theme, button *widget.Clickable, closeIcon *widget.Icon) layout.Dimensions {
	style := material.IconButton(theme, button, closeIcon, "Close tab")
	style.Background = color.NRGBA{}
	style.Color = theme.Fg
	style.Size = 12
	style.Inset = layout.UniformInset(2)
	return style.Layout(gtx)
}

func adjustSelection(selected *int, closed, remaining int) {
	switch {
	case remaining == 0:
		*selected = -1
	case *selected > closed:
		*selected--
	case *selected == closed && *selected >= remaining:
		*selected = remaining - 1
	}
}

func newTabItem(tab *hasherino.Tab) tabItem {
	title := tab.DisplayName
	if title == "" {
		title = tab.Login
	}
	return tabItem{
		id:    tab.Id,
		login: tab.Login,
		title: title,
		chat:  newChat(),
	}
}

// shouldRenderChatMessage reports whether the message is a real user message.
// Twitch sends system messages as PRIVMSGs from its own IRC services, like
// tmi.twitch.tv and jtv, which must not show up in chat.
func shouldRenderChatMessage(message hasherino.ChatMessage) bool {
	if message.Command != "PRIVMSG" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(message.Author)) {
	case "", "tmi.twitch.tv", "tmi", "jtv":
		return false
	}
	return true
}

func registerTabCallback(window *app.Window, callbackMap map[string]func(hasherino.ChatMessage), tab *tabItem) {
	chat := tab.chat
	chat.window = window
	callbackMap[tab.login] = func(message hasherino.ChatMessage) {
		if !shouldRenderChatMessage(message) {
			return
		}
		chat.append(chatMessage{
			author:      message.Author,
			authorColor: parseHexColor(message.AuthorColor),
			text:        message.Text,
			sentAt:      message.Time,
			ownEmotes:   message.Emotes,
			rawBadges:   message.Badges,
		})
		window.Invalidate()
	}
}

// loadChatHistory fills the tab's chat with the channel's recent messages.
// It respects the ChatHistory setting and the ChatMessageLimit from the
// controller's settings.
func loadChatHistory(window *app.Window, hc *hasherino.HasherinoController, tab *tabItem) {
	go func() {
		settings, err := hc.GetSettings()
		if err != nil {
			log.Printf("failed to load settings for chat history: %s", err)
			return
		}
		if !settings.ChatHistory {
			return
		}

		limit := settings.ChatMessageLimit
		if limit <= 0 {
			limit = 100
		}
		tab.chat.setLimit(limit)

		history, err := hasherino.GetChatHistory(tab.login, limit)
		if err != nil {
			log.Printf("failed to load chat history for %s: %s", tab.login, err)
			return
		}

		messages := *history
		sort.Slice(messages, func(i, j int) bool {
			return messages[i].Time.Before(messages[j].Time)
		})

		chat := tab.chat
		for _, message := range messages {
			if !shouldRenderChatMessage(message) {
				continue
			}
			chat.append(chatMessage{
				author:      message.Author,
				authorColor: parseHexColor(message.AuthorColor),
				text:        message.Text,
				sentAt:      message.Time,
				ownEmotes:   message.Emotes,
				rawBadges:   message.Badges,
			})
		}
		chat.requestScrollToEnd()
		window.Invalidate()
	}()
}

// loadChannelEmotes waits for the emotes of the channel to become available
// and gives them to the tab's chat, which renders the matching message words
// as emotes.
func loadChannelEmotes(window *app.Window, hc *hasherino.HasherinoController, tab *tabItem) {
	go func() {
		// The emotes of the channel are loaded in the background, usually in
		// several steps: the account's twitch emotes and the channel's 7tv
		// emotes finish at different times. Keep polling until the set stops
		// changing so none of them are missed.
		last := ""
		interval := channelEmotePollFirstInterval
		for range channelEmotePollAttempts {
			time.Sleep(interval)
			interval = min(interval*2, channelEmotePollMaxInterval)

			emotes, err := hc.GetChannelEmotes(tab.login)
			if err != nil {
				log.Printf("failed to load emotes for %s: %s", tab.login, err)
				continue
			}
			if len(emotes) == 0 {
				continue
			}

			signature := channelEmoteSignature(emotes)
			if signature == last {
				continue
			}
			last = signature
			tab.chat.setEmotes(emotes)
			window.Invalidate()
		}
	}()
}

// loadChannelBadges fetches the badge sets of the channel in the background
// and gives them to the tab's chat, which renders them next to author names.
func loadChannelBadges(window *app.Window, hc *hasherino.HasherinoController, tab *tabItem) {
	go func() {
		badges, err := hc.GetChatBadges(tab.id)
		if err != nil {
			log.Printf("failed to load badges for %s: %s", tab.login, err)
			return
		}
		tab.chat.setBadges(badges)
		window.Invalidate()
	}()
}

func channelEmoteSignature(emotes []*hasherino.Emote) string {
	entries := make([]string, 0, len(emotes))
	for _, emote := range emotes {
		if emote == nil {
			continue
		}
		entries = append(entries, strconv.FormatInt(int64(emote.Source), 10)+":"+emote.Id+":"+emote.Name)
	}
	sort.Strings(entries)
	return strings.Join(entries, "\x00")
}

func parseHexColor(value string) color.NRGBA {
	defaultColor := color.NRGBA{R: 0xe6, G: 0xe6, B: 0xe6, A: 0xff}
	value = strings.TrimPrefix(value, "#")
	if len(value) == 3 {
		value = string([]byte{value[0], value[0], value[1], value[1], value[2], value[2]})
	}
	if len(value) != 6 {
		return defaultColor
	}
	parsed, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return defaultColor
	}
	return color.NRGBA{
		R: uint8(parsed >> 16),
		G: uint8(parsed >> 8),
		B: uint8(parsed),
		A: 0xff,
	}
}

// wordTokenAt returns the range of the word at the cursor position.
func wordTokenAt(text string, cursor int) (tokenStart, tokenEnd int, ok bool) {
	runes := []rune(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}

	tokenStart = cursor
	for tokenStart > 0 && !unicode.IsSpace(runes[tokenStart-1]) {
		tokenStart--
	}
	tokenEnd = cursor
	for tokenEnd < len(runes) && !unicode.IsSpace(runes[tokenEnd]) {
		tokenEnd++
	}
	return tokenStart, tokenEnd, tokenStart != tokenEnd
}

// emoteTokenAt returns the emote search token at the cursor, the word that
// starts with the emote search colon, and the query after the colon.
func emoteTokenAt(text string, cursor int) (tokenStart, tokenEnd int, query string, ok bool) {
	tokenStart, tokenEnd, ok = wordTokenAt(text, cursor)
	if !ok {
		return 0, 0, "", false
	}
	runes := []rune(text)
	if runes[tokenStart] != ':' {
		return 0, 0, "", false
	}
	queryRunes := runes[tokenStart+1 : tokenEnd]
	if len(queryRunes) < minEmoteQueryLength {
		return 0, 0, "", false
	}
	return tokenStart, tokenEnd, string(queryRunes), true
}

// tabTokenAt returns the word at the cursor and the query to complete it
// with, which drops a leading emote search colon.
func tabTokenAt(text string, cursor int) (tokenStart, tokenEnd int, query string, ok bool) {
	tokenStart, tokenEnd, ok = wordTokenAt(text, cursor)
	if !ok {
		return 0, 0, "", false
	}
	runes := []rune(text)
	queryRunes := runes[tokenStart:tokenEnd]
	if queryRunes[0] == ':' {
		queryRunes = queryRunes[1:]
	}
	if len(queryRunes) == 0 {
		return 0, 0, "", false
	}
	return tokenStart, tokenEnd, string(queryRunes), true
}

func replaceEmoteToken(text string, cursor int, name string) (string, int, bool) {
	tokenStart, tokenEnd, _, ok := emoteTokenAt(text, cursor)
	if !ok {
		return text, cursor, false
	}
	replaced, replacementCursor := replaceToken(text, tokenStart, tokenEnd, name)
	return replaced, replacementCursor, true
}

// replaceToken replaces the token range with the replacement and returns
// the text and the cursor position after the replacement. A replacement at
// the end of the text gains a trailing space.
func replaceToken(text string, tokenStart, tokenEnd int, replacement string) (string, int) {
	runes := []rune(text)
	replacementRunes := []rune(replacement)
	if tokenEnd == len(runes) {
		replacementRunes = append(replacementRunes, ' ')
	}

	replaced := make([]rune, 0, len(runes)-(tokenEnd-tokenStart)+len(replacementRunes))
	replaced = append(replaced, runes[:tokenStart]...)
	replaced = append(replaced, replacementRunes...)
	replaced = append(replaced, runes[tokenEnd:]...)
	return string(replaced), tokenStart + len(replacementRunes)
}

func layoutMessageInput(gtx layout.Context, theme *material.Theme, state *messageInputState, hc *hasherino.HasherinoController, window *app.Window, selectedLogin string) layout.Dimensions {
	state.drainSearchEvents()
	state.handleInputKeys(gtx, hc, selectedLogin)
	state.updateSearch(gtx, window, hc)

	var inputDims layout.Dimensions
	return layout.Stack{Alignment: layout.S}.Layout(gtx,
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			inputDims = layoutMessageInputRow(gtx, theme, state, hc, selectedLogin)
			return inputDims
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			if !state.visible || len(state.suggestions) == 0 {
				return layout.Dimensions{}
			}
			offset := op.Offset(image.Pt(0, -(inputDims.Size.Y + gtx.Dp(unit.Dp(8))))).Push(gtx.Ops)
			dims := layoutEmoteSuggestions(gtx, theme, state, window)
			offset.Pop()
			return dims
		}),
	)
}

func (s *messageInputState) updateSearch(gtx layout.Context, window *app.Window, hc *hasherino.HasherinoController) {
	text := s.editor.Text()
	if text == s.lastText {
		return
	}
	s.lastText = text

	_, col := s.editor.CaretPos()
	_, _, query, ok := emoteTokenAt(text, col)
	if !ok {
		s.hideSuggestions()
		return
	}
	s.scheduleSearch(window, hc, query)
}

func (s *messageInputState) handleInputKeys(gtx layout.Context, hc *hasherino.HasherinoController, selectedLogin string) {
	s.handleInputKey(gtx, key.NameReturn, hc, selectedLogin)
	s.handleInputKey(gtx, key.NameEnter, hc, selectedLogin)
	s.handleInputKey(gtx, key.NameTab, hc, selectedLogin)
	if s.visible {
		s.handleInputKey(gtx, key.NameUpArrow, hc, selectedLogin)
		s.handleInputKey(gtx, key.NameDownArrow, hc, selectedLogin)
		s.handleInputKey(gtx, key.NameEscape, hc, selectedLogin)
	}
}

func (s *messageInputState) handleInputKey(gtx layout.Context, name key.Name, hc *hasherino.HasherinoController, selectedLogin string) {
	for {
		event, ok := gtx.Event(key.Filter{Focus: &s.editor, Name: name})
		if !ok {
			return
		}
		keyEvent, ok := event.(key.Event)
		if !ok || keyEvent.State != key.Press {
			continue
		}

		switch name {
		case key.NameReturn, key.NameEnter:
			if s.visible && len(s.suggestions) > 0 {
				s.acceptSelected(gtx)
			} else {
				s.send(gtx, hc, selectedLogin)
			}
		case key.NameTab:
			if s.visible && len(s.suggestions) > 0 {
				s.acceptSelected(gtx)
			} else {
				s.completeTab(gtx, hc)
			}
		case key.NameUpArrow:
			if len(s.suggestions) > 0 {
				s.selected = (s.selected - 1 + len(s.suggestions)) % len(s.suggestions)
			}
		case key.NameDownArrow:
			if len(s.suggestions) > 0 {
				s.selected = (s.selected + 1) % len(s.suggestions)
			}
		case key.NameEscape:
			s.hideSuggestions()
		}
	}
}

func layoutMessageInputRow(gtx layout.Context, theme *material.Theme, state *messageInputState, hc *hasherino.HasherinoController, selectedLogin string) layout.Dimensions {
	if state.sendButton.Clicked(gtx) {
		state.send(gtx, hc, selectedLogin)
	}

	return layout.Inset{
		Top:    unit.Dp(8),
		Bottom: unit.Dp(8),
		Left:   unit.Dp(12),
		Right:  unit.Dp(12),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				editor := material.Editor(theme, &state.editor, "Message")
				editor.TextSize = unit.Sp(16)
				return editor.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return material.Button(theme, &state.sendButton, "Send").Layout(gtx)
			}),
		)
	})
}

func layoutEmoteSuggestions(gtx layout.Context, theme *material.Theme, state *messageInputState, window *app.Window) layout.Dimensions {
	for i := range state.rows {
		if state.rows[i].button.Clicked(gtx) {
			state.acceptSuggestion(gtx, state.rows[i].emote.Name)
			return layout.Dimensions{}
		}
	}

	animator := chatAnimator{now: time.Now()}

	width := gtx.Dp(unit.Dp(240))
	if width > gtx.Constraints.Max.X {
		width = gtx.Constraints.Max.X
	}
	gtx.Constraints.Min.X = width
	gtx.Constraints.Max.X = width

	background := func(gtx layout.Context) layout.Dimensions {
		size := gtx.Constraints.Min
		radius := gtx.Dp(unit.Dp(8))
		panel := color.NRGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xff}
		paint.FillShape(gtx.Ops, panel, clip.UniformRRect(image.Rectangle{Max: size}, radius).Op(gtx.Ops))
		return layout.Dimensions{Size: size}
	}

	content := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{
			Top:    unit.Dp(4),
			Bottom: unit.Dp(4),
			Left:   unit.Dp(4),
			Right:  unit.Dp(4),
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			children := make([]layout.FlexChild, 0, len(state.rows))
			for i := range state.rows {
				i := i
				row := &state.rows[i]
				// Start loading the image as soon as the suggestion is shown;
				// the window is repainted when it is ready.
				loadChatEmote(window, row.emote)
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return row.button.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Inset{
							Top:    unit.Dp(4),
							Bottom: unit.Dp(4),
							Left:   unit.Dp(8),
							Right:  unit.Dp(8),
						}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
							widgets := make([]layout.FlexChild, 0, 3)
							if media := cachedChatEmoteMedia(row.emote); media != nil {
								widgets = append(widgets,
									layout.Rigid(func(gtx layout.Context) layout.Dimensions {
										return layoutInlineEmote(gtx, media, &animator)
									}),
									layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
								)
							}
							widgets = append(widgets, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								label := material.Body1(theme, row.emote.Name)
								label.Color = theme.Fg
								if i != state.selected {
									label.Color.A = 0x99
								}
								return label.Layout(gtx)
							}))
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx, widgets...)
						})
					})
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	}

	dims := layout.Background{}.Layout(gtx, background, content)
	animator.invalidate(gtx)
	return dims
}

type flowItem struct {
	call op.CallOp
	pos  image.Point
	size image.Point
}

// layoutFlow lays out widgets left to right, moving to the next row when the
// available width is exceeded.
func layoutFlow(gtx layout.Context, spaceX, spaceY unit.Dp, widgets ...layout.Widget) layout.Dimensions {
	maxWidth := gtx.Constraints.Max.X
	gapX := gtx.Dp(spaceX)
	gapY := gtx.Dp(spaceY)

	cgtx := gtx
	cgtx.Constraints.Min = image.Point{}

	items := make([]flowItem, 0, len(widgets))
	x, y, rowHeight, contentWidth := 0, 0, 0, 0

	for _, widget := range widgets {
		macro := op.Record(gtx.Ops)
		dims := widget(cgtx)
		call := macro.Stop()

		if x > 0 && x+dims.Size.X > maxWidth {
			x = 0
			y += rowHeight + gapY
			rowHeight = 0
		}

		items = append(items, flowItem{
			call: call,
			pos:  image.Point{X: x, Y: y},
			size: dims.Size,
		})

		if right := x + dims.Size.X; right > contentWidth {
			contentWidth = right
		}
		x += dims.Size.X + gapX
		if dims.Size.Y > rowHeight {
			rowHeight = dims.Size.Y
		}
	}

	for _, item := range items {
		offset := op.Offset(item.pos).Push(gtx.Ops)
		item.call.Add(gtx.Ops)
		offset.Pop()
	}

	size := image.Point{X: contentWidth, Y: y + rowHeight}
	return layout.Dimensions{Size: gtx.Constraints.Constrain(size)}
}

func newChat() *chat {
	var list widget.List
	list.Axis = layout.Vertical
	return &chat{
		list:      list,
		limit:     defaultChatMessageLimit,
		emotes:    make(map[string]*hasherino.Emote),
		showTime:  true,
		emoteSize: unit.Dp(22),
	}
}

const chatEmoteDownloadTimeout = 20 * time.Second

var (
	chatEmoteClient = &http.Client{Timeout: chatEmoteDownloadTimeout}
	chatEmoteMedia  = &chatEmoteMediaCache{
		entries: make(map[string]*chatEmoteMediaEntry),
	}
)

type chatEmoteMediaEntry struct {
	media   *chatMedia
	loading bool
	failed  bool
}

// chatEmoteMediaCache keeps the decoded files of emotes in memory, shared by
// every message and tab showing the same emote.
type chatEmoteMediaCache struct {
	mu      sync.Mutex
	entries map[string]*chatEmoteMediaEntry
}

func chatEmoteKey(emote *hasherino.Emote) string {
	return strconv.FormatInt(int64(emote.Source), 10) + ":" + emote.Id
}

func cachedChatEmoteMedia(emote *hasherino.Emote) *chatMedia {
	if emote == nil {
		return nil
	}
	chatEmoteMedia.mu.Lock()
	defer chatEmoteMedia.mu.Unlock()
	if entry, ok := chatEmoteMedia.entries[chatEmoteKey(emote)]; ok {
		return entry.media
	}
	return nil
}

// requestChatEmoteMedia returns the media of the emote when it is already
// loaded and reports whether the caller should start loading it.
func requestChatEmoteMedia(emote *hasherino.Emote) (media *chatMedia, load bool) {
	key := chatEmoteKey(emote)
	chatEmoteMedia.mu.Lock()
	defer chatEmoteMedia.mu.Unlock()
	entry, ok := chatEmoteMedia.entries[key]
	if !ok {
		entry = &chatEmoteMediaEntry{}
		chatEmoteMedia.entries[key] = entry
	}
	if entry.media != nil {
		return entry.media, false
	}
	if entry.loading || entry.failed {
		return nil, false
	}
	entry.loading = true
	return nil, true
}

func finishChatEmoteMedia(emote *hasherino.Emote, media *chatMedia) {
	chatEmoteMedia.mu.Lock()
	defer chatEmoteMedia.mu.Unlock()
	entry, ok := chatEmoteMedia.entries[chatEmoteKey(emote)]
	if !ok {
		entry = &chatEmoteMediaEntry{}
		chatEmoteMedia.entries[chatEmoteKey(emote)] = entry
	}
	entry.loading = false
	entry.media = media
	entry.failed = media == nil
}

// loadChatEmote starts downloading and decoding the emote unless it is
// already loaded or loading. The window is invalidated once it is ready.
func loadChatEmote(window *app.Window, emote *hasherino.Emote) {
	if emote == nil {
		return
	}
	_, load := requestChatEmoteMedia(emote)
	if !load {
		return
	}
	go func() {
		media, err := downloadChatEmote(emote)
		if err != nil {
			log.Printf("Failed to load emote %s: %s", emote.Name, err)
			finishChatEmoteMedia(emote, nil)
			return
		}
		finishChatEmoteMedia(emote, media)
		window.Invalidate()
	}()
}

var chatBadgeMedia = &chatBadgeMediaCache{
	entries: make(map[string]*chatBadgeMediaEntry),
}

type chatBadgeMediaEntry struct {
	media   *chatMedia
	loading bool
	failed  bool
}

// chatBadgeMediaCache keeps the decoded badge images in memory, shared by
// every message and tab showing the same badge.
type chatBadgeMediaCache struct {
	mu      sync.Mutex
	entries map[string]*chatBadgeMediaEntry
}

func cachedChatBadgeMedia(url string) *chatMedia {
	if url == "" {
		return nil
	}
	chatBadgeMedia.mu.Lock()
	defer chatBadgeMedia.mu.Unlock()
	if entry, ok := chatBadgeMedia.entries[url]; ok {
		return entry.media
	}
	return nil
}

// requestChatBadgeMedia returns the media of the badge when it is already
// loaded and reports whether the caller should start loading it.
func requestChatBadgeMedia(url string) (media *chatMedia, load bool) {
	chatBadgeMedia.mu.Lock()
	defer chatBadgeMedia.mu.Unlock()
	entry, ok := chatBadgeMedia.entries[url]
	if !ok {
		entry = &chatBadgeMediaEntry{}
		chatBadgeMedia.entries[url] = entry
	}
	if entry.media != nil {
		return entry.media, false
	}
	if entry.loading || entry.failed {
		return nil, false
	}
	entry.loading = true
	return nil, true
}

func finishChatBadgeMedia(url string, media *chatMedia) {
	chatBadgeMedia.mu.Lock()
	defer chatBadgeMedia.mu.Unlock()
	entry, ok := chatBadgeMedia.entries[url]
	if !ok {
		entry = &chatBadgeMediaEntry{}
		chatBadgeMedia.entries[url] = entry
	}
	entry.loading = false
	entry.media = media
	entry.failed = media == nil
}

// loadChatBadge starts downloading and decoding a badge image unless it is
// already loaded or loading. The window is invalidated once it is ready.
func loadChatBadge(window *app.Window, url string) {
	if url == "" {
		return
	}
	_, load := requestChatBadgeMedia(url)
	if !load {
		return
	}
	go func() {
		media, err := downloadChatBadge(url)
		if err != nil {
			log.Printf("Failed to load badge %s: %s", url, err)
			finishChatBadgeMedia(url, nil)
			return
		}
		finishChatBadgeMedia(url, media)
		window.Invalidate()
	}()
}

func downloadChatBadge(url string) (*chatMedia, error) {
	resp, err := chatEmoteClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading badge %s failed: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return decodeChatMedia(data)
}

func downloadChatEmote(emote *hasherino.Emote) (*chatMedia, error) {
	url, err := emote.GetUrl()
	if err != nil {
		return nil, err
	}
	resp, err := chatEmoteClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading emote %s failed: %s", emote.Name, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return decodeChatMedia(data)
}

func decodeChatMedia(data []byte) (*chatMedia, error) {
	if animation, err := gif.DecodeAll(bytes.NewReader(data)); err == nil {
		frames := composeGifFrames(animation)
		if len(frames) == 0 {
			return nil, errors.New("gif has no frames")
		}
		delays := make([]time.Duration, len(frames))
		for i := range delays {
			if i < len(animation.Delay) {
				delays[i] = time.Duration(animation.Delay[i]) * 10 * time.Millisecond
			}
		}
		return newChatMedia(frames, delays), nil
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return newChatMedia([]image.Image{img}, []time.Duration{0}), nil
}

// composeGifFrames draws every frame of the animation onto a canvas, honoring
// the frame offsets and disposal methods, so that gifs that only store the
// pixels that changed render completely and transparent areas do not keep
// pixels from previous frames.
func composeGifFrames(src *gif.GIF) []image.Image {
	if len(src.Image) == 0 {
		return nil
	}

	width, height := src.Config.Width, src.Config.Height
	if width <= 0 || height <= 0 {
		bounds := src.Image[0].Bounds()
		width, height = bounds.Max.X, bounds.Max.Y
	}
	if width <= 0 || height <= 0 {
		return nil
	}

	bounds := image.Rect(0, 0, width, height)
	canvas := image.NewNRGBA(bounds)
	frames := make([]image.Image, len(src.Image))
	var restore *image.NRGBA
	for i, frame := range src.Image {
		if i > 0 {
			switch gifDisposal(src, i-1) {
			case gif.DisposalBackground:
				// the previous frame is removed from the canvas, so its
				// pixels do not show through transparent pixels of this one
				previous := src.Image[i-1].Bounds().Intersect(bounds)
				if !previous.Empty() {
					draw.Draw(canvas, previous, image.Transparent, image.Point{}, draw.Src)
				}
			case gif.DisposalPrevious:
				if restore != nil {
					copy(canvas.Pix, restore.Pix)
				}
			}
		}

		var before *image.NRGBA
		if gifDisposal(src, i) == gif.DisposalPrevious {
			before = cloneNRGBA(canvas)
		}

		frameBounds := frame.Bounds()
		draw.Draw(canvas, frameBounds, frame, frameBounds.Min, draw.Over)

		frames[i] = cloneNRGBA(canvas)
		restore = before
	}
	return frames
}

func gifDisposal(src *gif.GIF, frame int) byte {
	if frame < 0 || frame >= len(src.Disposal) {
		return gif.DisposalNone
	}
	return src.Disposal[frame]
}

func cloneNRGBA(src *image.NRGBA) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}

func newChatMedia(frames []image.Image, delays []time.Duration) *chatMedia {
	if len(delays) != len(frames) {
		delays = make([]time.Duration, len(frames))
	}
	media := &chatMedia{
		frames: make([]paint.ImageOp, len(frames)),
		delays: delays,
		start:  time.Now(),
	}
	if len(frames) > 0 {
		bounds := frames[0].Bounds()
		media.width = bounds.Dx()
		media.height = bounds.Dy()
	}
	for i, frame := range frames {
		media.frames[i] = paint.NewImageOp(frame)
		media.total += delays[i]
	}
	return media
}

func (m *chatMedia) frame(now time.Time) (paint.ImageOp, time.Time) {
	if len(m.frames) == 0 {
		return paint.ImageOp{}, time.Time{}
	}
	if len(m.frames) == 1 || m.total <= 0 {
		return m.frames[0], time.Time{}
	}

	elapsed := now.Sub(m.start) % m.total
	if elapsed < 0 {
		elapsed += m.total
	}
	for i, delay := range m.delays {
		if elapsed < delay {
			return m.frames[i], now.Add(delay - elapsed)
		}
		elapsed -= delay
	}
	return m.frames[0], now.Add(m.delays[0])
}

// chatAnimationFrameInterval caps how often animated media repaints. It matches
// the ticker Fyne uses for GIF emotes.
const chatAnimationFrameInterval = 66 * time.Millisecond

// chatAnimator collects the earliest time animated media needs a repaint during
// a single layout. A single invalidation is scheduled for the whole window, so
// many emotes do not each trigger their own full-window repaint. It also
// carries the display options used while laying out the chat.
type chatAnimator struct {
	now  time.Time
	next time.Time
	// showTime controls whether message timestamps are laid out.
	showTime bool
	// emoteHeight is the pixel height of inline emotes.
	emoteHeight int
}

func (a *chatAnimator) request(next time.Time) {
	if next.IsZero() {
		return
	}
	// Do not repaint faster than the shared frame interval. Frames are picked
	// from the wall clock when they are drawn, so faster GIFs simply skip
	// frames instead of forcing extra repaints.
	if capped := a.now.Add(chatAnimationFrameInterval); next.Before(capped) {
		next = capped
	}
	if a.next.IsZero() || next.Before(a.next) {
		a.next = next
	}
}

func (a *chatAnimator) invalidate(gtx layout.Context) {
	if !a.next.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: a.next})
	}
}

func (c *chat) layout(gtx layout.Context, theme *material.Theme) layout.Dimensions {
	animator := chatAnimator{
		now:         time.Now(),
		showTime:    c.showTime,
		emoteHeight: gtx.Dp(c.emoteSize),
	}
	messages := c.snapshot()
	if c.takeScrollToEnd() {
		c.list.ScrollToEnd = true
		c.list.Position.BeforeEnd = false
	}
	c.list.Gap = gtx.Dp(unit.Dp(8))
	dims := layout.Inset{
		Top:    unit.Dp(8),
		Bottom: unit.Dp(8),
		Left:   unit.Dp(12),
		Right:  unit.Dp(12),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return material.List(theme, &c.list).Layout(gtx, len(messages), func(gtx layout.Context, index int) layout.Dimensions {
			c.requestEmotes(messages[index])
			c.requestBadges(messages[index])
			dims := layoutChatMessage(gtx, theme, messages[index], &animator)
			// Report the full available width even for short messages.
			// Otherwise the list sizes itself to the widest visible message,
			// and its scrollbar shifts horizontally as messages scroll by.
			dims.Size.X = gtx.Constraints.Max.X
			return dims
		})
	})
	animator.invalidate(gtx)
	return dims
}

func layoutChatMessage(gtx layout.Context, theme *material.Theme, message chatMessage, animator *chatAnimator) layout.Dimensions {
	if len(message.parts) > 0 {
		return layoutChatParts(gtx, theme, message, animator)
	}

	switch {
	case message.media == nil:
		return layoutChatText(gtx, theme, message)
	case message.text == "":
		gtx.Constraints.Min = image.Point{}
		maxWidth := gtx.Constraints.Max.X
		if maxWidth > gtx.Dp(unit.Dp(320)) {
			maxWidth = gtx.Dp(unit.Dp(320))
		}
		gtx.Constraints.Max = image.Point{X: maxWidth, Y: gtx.Dp(unit.Dp(220))}
		return layoutChatMedia(gtx, message.media, animator)
	default:
		// Text and media share a row, with the image on the left.
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = image.Point{}
				maxWidth := gtx.Constraints.Max.X / 2
				if maxWidth > gtx.Dp(unit.Dp(200)) {
					maxWidth = gtx.Dp(unit.Dp(200))
				}
				gtx.Constraints.Max = image.Point{X: maxWidth, Y: gtx.Dp(unit.Dp(160))}
				return layoutChatMedia(gtx, message.media, animator)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layoutChatText(gtx, theme, message)
			}),
		)
	}
}

func layoutChatText(gtx layout.Context, theme *material.Theme, message chatMessage) layout.Dimensions {
	if message.author == "" {
		label := material.Body1(theme, message.text)
		label.Color = theme.Fg
		return label.Layout(gtx)
	}

	author := material.Body1(theme, message.author+":")
	author.Color = message.authorColor
	text := material.Body1(theme, message.text)
	text.Color = theme.Fg

	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Baseline}.Layout(gtx,
		layout.Rigid(author.Layout),
		layout.Rigid(layout.Spacer{Width: unit.Dp(4)}.Layout),
		layout.Flexed(1, text.Layout),
	)
}

func layoutChatMedia(gtx layout.Context, media *chatMedia, animator *chatAnimator) layout.Dimensions {
	src, next := media.frame(animator.now)
	animator.request(next)
	img := widget.Image{Src: src, Fit: widget.Contain, Position: layout.W}
	return img.Layout(gtx)
}

func layoutChatParts(gtx layout.Context, theme *material.Theme, message chatMessage, animator *chatAnimator) layout.Dimensions {
	widgets := make([]layout.Widget, 0, len(message.parts)+len(message.badgeURLs)+2)
	if animator.showTime && !message.sentAt.IsZero() {
		sentAt := message.sentAt.Format(chatMessageTimeFormat)
		timeColor := theme.Fg
		timeColor.A = 0x99
		widgets = append(widgets, func(gtx layout.Context) layout.Dimensions {
			return layoutInlineText(gtx, theme, sentAt, timeColor)
		})
		widgets = append(widgets, layoutInlineSpacer(unit.Dp(6)))
	}
	for _, url := range message.badgeURLs {
		media := cachedChatBadgeMedia(url)
		if media == nil {
			continue
		}
		widgets = append(widgets, func(gtx layout.Context) layout.Dimensions {
			return layoutChatBadge(gtx, media)
		})
		widgets = append(widgets, layoutInlineSpacer(unit.Dp(2)))
	}
	if message.author != "" {
		author := message.author + ": "
		authorColor := message.authorColor
		widgets = append(widgets, func(gtx layout.Context) layout.Dimensions {
			return layoutInlineText(gtx, theme, author, authorColor)
		})
	}
	for _, part := range message.parts {
		part := part
		if part.emote != nil {
			media := part.media
			if media == nil {
				media = cachedChatEmoteMedia(part.emote)
			}
			if media == nil {
				// the emote is still loading, so show its name until it
				// is ready and the window is repainted
				name := part.emote.Name
				widgets = append(widgets, func(gtx layout.Context) layout.Dimensions {
					return layoutInlineText(gtx, theme, name+" ", theme.Fg)
				})
				continue
			}
			widgets = append(widgets, func(gtx layout.Context) layout.Dimensions {
				return layoutInlineEmote(gtx, media, animator)
			})
			widgets = append(widgets, layoutInlineSpacer(unit.Dp(3)))
			continue
		}
		if part.media != nil {
			widgets = append(widgets, func(gtx layout.Context) layout.Dimensions {
				return layoutInlineEmote(gtx, part.media, animator)
			})
			widgets = append(widgets, layoutInlineSpacer(unit.Dp(3)))
			continue
		}
		for _, word := range strings.Fields(part.text) {
			word := word
			widgets = append(widgets, func(gtx layout.Context) layout.Dimensions {
				return layoutInlineText(gtx, theme, word+" ", theme.Fg)
			})
		}
	}
	return layoutInline(gtx, widgets...)
}

func layoutInlineText(gtx layout.Context, theme *material.Theme, text string, col color.NRGBA) layout.Dimensions {
	label := material.Body1(theme, text)
	label.Color = col
	return label.Layout(gtx)
}

func layoutInlineSpacer(width unit.Dp) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Point{X: gtx.Dp(width)}}
	}
}

func layoutChatBadge(gtx layout.Context, media *chatMedia) layout.Dimensions {
	if media == nil || len(media.frames) == 0 {
		return layout.Dimensions{}
	}
	height := gtx.Dp(unit.Dp(18))
	width := height
	if media.width > 0 && media.height > 0 {
		width = height * media.width / media.height
	}
	gtx.Constraints.Min = image.Point{}
	gtx.Constraints.Max = image.Point{X: width, Y: height}
	return widget.Image{Src: media.frames[0], Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
}

func layoutInlineEmote(gtx layout.Context, media *chatMedia, animator *chatAnimator) layout.Dimensions {
	src, next := media.frame(animator.now)
	animator.request(next)
	height := animator.emoteHeight
	if height <= 0 {
		height = gtx.Dp(unit.Dp(22))
	}
	width := height
	if media.width > 0 && media.height > 0 {
		width = height * media.width / media.height
	}
	gtx.Constraints.Min = image.Point{}
	gtx.Constraints.Max = image.Point{X: width, Y: height}
	return widget.Image{Src: src, Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
}

// layoutInline lays out widgets like words in a paragraph, wrapping to a new
// row when needed and aligning items by their baselines.
func layoutInline(gtx layout.Context, widgets ...layout.Widget) layout.Dimensions {
	maxWidth := gtx.Constraints.Max.X
	cgtx := gtx
	cgtx.Constraints.Min = image.Point{}

	type inlineItem struct {
		call op.CallOp
		dims layout.Dimensions
	}
	items := make([]inlineItem, len(widgets))
	for i, widget := range widgets {
		macro := op.Record(gtx.Ops)
		dims := widget(cgtx)
		items[i] = inlineItem{call: macro.Stop(), dims: dims}
	}

	type inlineRow struct {
		items  []inlineItem
		width  int
		height int
	}
	var rows []inlineRow
	var row inlineRow
	for _, item := range items {
		if row.width > 0 && row.width+item.dims.Size.X > maxWidth {
			rows = append(rows, row)
			row = inlineRow{}
		}
		row.items = append(row.items, item)
		row.width += item.dims.Size.X
	}
	if len(row.items) > 0 {
		rows = append(rows, row)
	}

	for i := range rows {
		for _, item := range rows[i].items {
			if item.dims.Size.Y > rows[i].height {
				rows[i].height = item.dims.Size.Y
			}
		}
	}

	contentWidth := 0
	y := 0
	for _, row := range rows {
		if row.width > contentWidth {
			contentWidth = row.width
		}
		x := 0
		for _, item := range row.items {
			// Center every item vertically in its line, like the Fyne flow
			// layout does, so emotes sit next to the text instead of above it.
			pos := image.Point{X: x, Y: y + (row.height-item.dims.Size.Y)/2}
			offset := op.Offset(pos).Push(gtx.Ops)
			item.call.Add(gtx.Ops)
			offset.Pop()
			x += item.dims.Size.X
		}
		y += row.height
	}

	return layout.Dimensions{Size: gtx.Constraints.Constrain(image.Point{X: contentWidth, Y: y})}
}

func newUsersMenu() *usersMenuState {
	return &usersMenuState{selected: -1, next: 1}
}

func loadSavedUsers(hc *hasherino.HasherinoController, menu *usersMenuState) error {
	accounts, err := hc.GetAccounts()
	if err != nil {
		return err
	}

	menu.users = menu.users[:0]
	menu.selected = -1
	for _, account := range accounts {
		menu.users = append(menu.users, userEntry{
			id:     account.Id,
			login:  account.Login,
			token:  account.Token,
			active: account.Active,
		})
		if account.Active {
			menu.selected = len(menu.users) - 1
		}
	}
	if menu.selected < 0 && len(menu.users) > 0 {
		menu.selected = 0
	}
	return nil
}

func removeUserAt(menu *usersMenuState, index int) {
	if index < 0 || index >= len(menu.users) {
		return
	}
	menu.users = append(menu.users[:index], menu.users[index+1:]...)
	switch {
	case len(menu.users) == 0:
		menu.selected = -1
	case menu.selected > index:
		menu.selected--
	case menu.selected >= len(menu.users):
		menu.selected = len(menu.users) - 1
	}
}

func startDeviceAuth(window *app.Window, clientID, scopes string) (<-chan deviceAuthEvent, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan deviceAuthEvent, 4)

	go func() {
		defer close(events)
		defer cancel()

		emit := func(event deviceAuthEvent) {
			select {
			case events <- event:
			case <-ctx.Done():
				return
			}
			window.Invalidate()
		}

		oauth := hasherino.NewTwitchOAuth()
		device, err := oauth.StartDeviceAuth(ctx, clientID, scopes)
		if err != nil {
			emit(deviceAuthEvent{err: err})
			return
		}
		emit(deviceAuthEvent{device: device})

		interval := time.Duration(device.Interval) * time.Second
		if interval <= 0 {
			interval = 5 * time.Second
		}
		token, err := oauth.WaitForDeviceAuth(ctx, clientID, scopes, device.DeviceCode, interval)
		if err != nil {
			emit(deviceAuthEvent{err: err})
			return
		}

		id, login, err := oauth.GetUserForToken(clientID, token.AccessToken)
		if err != nil {
			emit(deviceAuthEvent{err: err})
			return
		}
		emit(deviceAuthEvent{token: token, id: id, login: login})
	}()

	return events, cancel
}

func (a *deviceAuthState) drain(menu *usersMenuState) {
	for a.events != nil {
		select {
		case event, ok := <-a.events:
			if !ok {
				a.events = nil
				return
			}
			a.apply(event, menu)
		default:
			return
		}
	}
}

func (a *deviceAuthState) apply(event deviceAuthEvent, menu *usersMenuState) {
	switch {
	case event.err != nil:
		if errors.Is(event.err, context.Canceled) {
			a.close()
			return
		}
		a.pending = false
		a.errText = friendlyDeviceAuthError(event.err)
	case event.device != nil:
		a.pending = true
		a.userCode = event.device.UserCode
		a.verificationURI = event.device.VerificationURI
	case event.token != nil:
		a.pending = false
		if event.login != "" {
			if a.onAccount != nil {
				if err := a.onAccount(event.id, event.login, event.token.AccessToken); err != nil {
					a.errText = fmt.Sprintf("Could not save %s: %s", event.login, err)
					return
				}
			}
			selectOrAddUser(menu, event.id, event.login, event.token.AccessToken)
			menu.open = false
		}
		a.close()
	}
}

func (a *deviceAuthState) close() {
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	a.events = nil
	a.open = false
	a.pending = false
	a.userCode = ""
	a.verificationURI = ""
	a.errText = ""
}

func selectOrAddUser(menu *usersMenuState, id, login, token string) {
	anyActive := false
	for i := range menu.users {
		if menu.users[i].active {
			anyActive = true
			break
		}
	}
	for i := range menu.users {
		if menu.users[i].id == id || (menu.users[i].id == "" && menu.users[i].login == login) {
			menu.users[i].id = id
			menu.users[i].login = login
			menu.users[i].token = token
			menu.users[i].active = menu.users[i].active || !anyActive
			menu.selected = i
			return
		}
	}
	menu.users = append(menu.users, userEntry{id: id, login: login, token: token, active: !anyActive})
	menu.selected = len(menu.users) - 1
}

func friendlyDeviceAuthError(err error) string {
	switch {
	case errors.Is(err, hasherino.ErrDeviceAccessDenied):
		return "Twitch authorization was denied."
	case errors.Is(err, hasherino.ErrDeviceCodeExpired):
		return "The Twitch code expired. Please try again."
	case errors.Is(err, hasherino.ErrDeviceSlowDown):
		return "Twitch asked us to slow down. Please try again."
	default:
		return err.Error()
	}
}

func layoutUsersMenu(gtx layout.Context, theme *material.Theme, state *usersMenuState, addIcon, deleteIcon *widget.Icon) layout.Dimensions {
	for i := range state.users {
		if state.users[i].button.Clicked(gtx) {
			state.selected = i
		}
	}

	if state.add.Clicked(gtx) {
		if state.onAdd != nil {
			state.onAdd()
		} else {
			state.users = append(state.users, userEntry{login: fmt.Sprintf("user%d", state.next)})
			state.selected = len(state.users) - 1
			state.next++
		}
	}
	if state.remove.Clicked(gtx) && state.selected >= 0 && state.selected < len(state.users) {
		if state.onRemove != nil {
			state.onRemove(state.selected)
		} else {
			removeUserAt(state, state.selected)
		}
	}

	width := gtx.Dp(unit.Dp(220))
	if width > gtx.Constraints.Max.X {
		width = gtx.Constraints.Max.X
	}
	gtx.Constraints.Min.X = width
	gtx.Constraints.Max.X = width

	background := func(gtx layout.Context) layout.Dimensions {
		size := gtx.Constraints.Min
		radius := gtx.Dp(unit.Dp(8))
		panel := color.NRGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xff}
		paint.FillShape(gtx.Ops, panel, clip.UniformRRect(image.Rectangle{Max: size}, radius).Op(gtx.Ops))
		return layout.Dimensions{Size: size}
	}

	content := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{
			Top:    unit.Dp(8),
			Bottom: unit.Dp(8),
			Left:   unit.Dp(8),
			Right:  unit.Dp(8),
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			children := make([]layout.FlexChild, 0, len(state.users)+3)
			for i := range state.users {
				i := i
				entry := &state.users[i]
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layoutUserRow(gtx, theme, entry, i == state.selected)
				}))
			}
			children = append(children,
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layoutMenuIconButton(gtx, theme, &state.add, addIcon, "Add user")
						}),
						layout.Rigid(layout.Spacer{Width: unit.Dp(4)}.Layout),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layoutMenuIconButton(gtx, theme, &state.remove, deleteIcon, "Remove user")
						}),
					)
				}),
			)
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	}

	return layout.Background{}.Layout(gtx, background, content)
}

func layoutUserRow(gtx layout.Context, theme *material.Theme, entry *userEntry, selected bool) layout.Dimensions {
	return entry.button.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		row := func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{
				Top:    unit.Dp(6),
				Bottom: unit.Dp(6),
				Left:   unit.Dp(8),
				Right:  unit.Dp(8),
			}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				label := material.Body1(theme, entry.login)
				label.Color = theme.Fg
				if !selected {
					label.Color.A = 0x99
				}
				return label.Layout(gtx)
			})
		}
		if !selected {
			return row(gtx)
		}

		highlight := func(gtx layout.Context) layout.Dimensions {
			size := gtx.Constraints.Min
			fill := theme.ContrastBg
			fill.A = 0x66
			radius := gtx.Dp(unit.Dp(4))
			paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: size}, radius).Op(gtx.Ops))
			return layout.Dimensions{Size: size}
		}
		return layout.Background{}.Layout(gtx, highlight, row)
	})
}

func layoutMenuIconButton(gtx layout.Context, theme *material.Theme, button *widget.Clickable, icon *widget.Icon, description string) layout.Dimensions {
	style := material.IconButton(theme, button, icon, description)
	style.Background = color.NRGBA{}
	style.Color = theme.Fg
	style.Size = 18
	style.Inset = layout.UniformInset(6)
	return style.Layout(gtx)
}

// openExternalURL opens url with the first available browser launcher. The
// launcher's output is captured so platform diagnostics, such as a missing
// desktop portal, do not get printed to the application's terminal.
func openExternalURL(url string) error {
	providers := []string{"xdg-open", "x-www-browser", "www-browser"}
	var lastErr error
	for _, provider := range providers {
		path, err := exec.LookPath(provider)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, url)
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		if err := cmd.Run(); err != nil {
			lastErr = err
			continue
		}
		if bytes.Contains(output.Bytes(), []byte("Failed to call portal")) {
			// xdg-open can exit successfully even though the portal call
			// failed, for example when the host has no usable portal.
			lastErr = errors.New("browser launcher could not reach the desktop portal")
			continue
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("no browser launcher found")
}

func layoutDeviceAuth(gtx layout.Context, theme *material.Theme, state *deviceAuthState) layout.Dimensions {
	// Consume clicks on the backdrop so they cannot reach the UI underneath.
	state.backdrop.Clicked(gtx)

	if state.closeButton.Clicked(gtx) {
		state.close()
		return layout.Dimensions{}
	}
	if state.openButton.Clicked(gtx) && state.verificationURI != "" {
		if err := openExternalURL(state.verificationURI); err != nil {
			state.openErr = "Could not open a browser automatically. Use Copy link and open it manually."
		} else {
			state.openErr = ""
		}
	}
	if state.copyButton.Clicked(gtx) && state.userCode != "" {
		gtx.Execute(clipboard.WriteCmd{
			Type: "text/plain",
			Data: io.NopCloser(strings.NewReader(state.userCode)),
		})
	}
	if state.copyLinkButton.Clicked(gtx) && state.verificationURI != "" {
		gtx.Execute(clipboard.WriteCmd{
			Type: "text/plain",
			Data: io.NopCloser(strings.NewReader(state.verificationURI)),
		})
	}
	if !state.open {
		return layout.Dimensions{}
	}

	return layoutModal(gtx, &state.backdrop, func(gtx layout.Context) layout.Dimensions {
		return layoutModalPanel(gtx, unit.Dp(360), func(gtx layout.Context) layout.Dimensions {
			return layoutDeviceAuthContent(gtx, theme, state)
		})
	})
}

// layoutModal draws a centered dialog on top of a backdrop that swallows
// pointer events, so the UI underneath cannot be interacted with.
func layoutModal(gtx layout.Context, backdrop *widget.Clickable, panel layout.Widget) layout.Dimensions {
	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return backdrop.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				paint.Fill(gtx.Ops, color.NRGBA{A: 0xb0})
				return layout.Dimensions{Size: gtx.Constraints.Max}
			})
		}),
		layout.Stacked(panel),
	)
}

// layoutModalPanel wraps content in the dialog's rounded background.
func layoutModalPanel(gtx layout.Context, width unit.Dp, content layout.Widget) layout.Dimensions {
	panelWidth := gtx.Dp(width)
	if panelWidth > gtx.Constraints.Max.X {
		panelWidth = gtx.Constraints.Max.X
	}
	gtx.Constraints.Min.X = panelWidth
	gtx.Constraints.Max.X = panelWidth
	gtx.Constraints.Min.Y = 0

	background := func(gtx layout.Context) layout.Dimensions {
		size := gtx.Constraints.Min
		radius := gtx.Dp(unit.Dp(10))
		panel := color.NRGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xff}
		paint.FillShape(gtx.Ops, panel, clip.UniformRRect(image.Rectangle{Max: size}, radius).Op(gtx.Ops))
		return layout.Dimensions{Size: size}
	}

	wrapped := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{
			Top:    unit.Dp(20),
			Bottom: unit.Dp(20),
			Left:   unit.Dp(20),
			Right:  unit.Dp(20),
		}.Layout(gtx, content)
	}
	return layout.Background{}.Layout(gtx, background, wrapped)
}

func layoutAddTab(gtx layout.Context, theme *material.Theme, state *addTabState) layout.Dimensions {
	state.backdrop.Clicked(gtx)

	if state.cancelButton.Clicked(gtx) {
		state.close()
		return layout.Dimensions{}
	}
	if !state.open {
		return layout.Dimensions{}
	}

	return layoutModal(gtx, &state.backdrop, func(gtx layout.Context) layout.Dimensions {
		return layoutModalPanel(gtx, unit.Dp(360), func(gtx layout.Context) layout.Dimensions {
			return layoutAddTabContent(gtx, theme, state)
		})
	})
}

func layoutAddTabContent(gtx layout.Context, theme *material.Theme, state *addTabState) layout.Dimensions {
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return material.H6(theme, "Add tab").Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			editor := material.Editor(theme, &state.editor, "Channel")
			editor.TextSize = unit.Sp(16)
			return editor.Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
	}

	if state.errText != "" {
		children = append(children,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body1(theme, state.errText)
				label.Color = color.NRGBA{R: 0xff, G: 0x90, B: 0x90, A: 0xff}
				return label.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
		)
	}

	children = append(children,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := "Add"
			if state.pending {
				label = "Adding..."
			}
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return material.Button(theme, &state.addButton, label).Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return material.Button(theme, &state.cancelButton, "Cancel").Layout(gtx)
				}),
			)
		}),
	)

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func newSettingsState() *settingsState {
	state := &settingsState{
		settings: hasherino.AppSettings{
			ChatMessageLimit: 100,
			ChatHistory:      hasherino.DefaultChatHistory,
			ShowMessageTime:  hasherino.DefaultShowMessageTime,
			EmoteSize:        hasherino.DefaultEmoteSize,
			FontSize:         hasherino.DefaultFontSize,
		},
	}
	state.list.Axis = layout.Vertical
	for _, editor := range []*widget.Editor{&state.chatLimitEditor, &state.emoteSizeEditor, &state.fontSizeEditor} {
		editor.SingleLine = true
		editor.Filter = "0123456789"
		editor.InputHint = key.HintNumeric
		editor.Alignment = giotext.End
	}
	state.chatLimitEditor.SetText(strconv.Itoa(state.settings.ChatMessageLimit))
	state.emoteSizeEditor.SetText(strconv.Itoa(state.settings.EmoteSize))
	state.fontSizeEditor.SetText(strconv.Itoa(state.settings.FontSize))
	state.showTime.Value = state.settings.ShowMessageTime
	state.chatHistory.Value = state.settings.ChatHistory
	return state
}

func applyAppSettings(theme *material.Theme, tabs []tabItem, settings hasherino.AppSettings) {
	if settings.FontSize > 0 {
		theme.TextSize = unit.Sp(float32(settings.FontSize))
	}
	for i := range tabs {
		applyAppSettingsToChat(tabs[i].chat, settings)
	}
}

func applyAppSettingsToChat(c *chat, settings hasherino.AppSettings) {
	if c == nil {
		return
	}
	c.setLimit(settings.ChatMessageLimit)
	c.setShowTime(settings.ShowMessageTime)
	c.setEmoteSize(settings.EmoteSize)
}

// load refreshes the values shown by the dialog.
func (s *settingsState) load(hc *hasherino.HasherinoController) {
	settings, err := hc.GetSettings()
	if err != nil {
		s.loaded = false
		s.errText = err.Error()
		return
	}
	s.settings = *settings
	s.loaded = true
	s.chatLimitEditor.SetText(strconv.Itoa(settings.ChatMessageLimit))
	s.emoteSizeEditor.SetText(strconv.Itoa(settings.EmoteSize))
	s.fontSizeEditor.SetText(strconv.Itoa(settings.FontSize))
	s.showTime.Value = settings.ShowMessageTime
	s.chatHistory.Value = settings.ChatHistory
	s.errText = ""
}

// save persists the values in the dialog. It returns false when a value is
// invalid, leaving the dialog open so the user can fix it.
func (s *settingsState) save(hc *hasherino.HasherinoController) bool {
	limit, err := strconv.Atoi(strings.TrimSpace(s.chatLimitEditor.Text()))
	if err != nil || limit <= 0 {
		s.errText = "Chat message limit must be a positive number."
		return false
	}
	emoteSize, err := strconv.Atoi(strings.TrimSpace(s.emoteSizeEditor.Text()))
	if err != nil || emoteSize <= 0 {
		s.errText = "Emote size must be a positive number."
		return false
	}
	fontSize, err := strconv.Atoi(strings.TrimSpace(s.fontSizeEditor.Text()))
	if err != nil || fontSize <= 0 {
		s.errText = "Font size must be a positive number."
		return false
	}

	s.settings.ChatMessageLimit = limit
	s.settings.EmoteSize = emoteSize
	s.settings.FontSize = fontSize
	s.settings.ShowMessageTime = s.showTime.Value
	s.settings.ChatHistory = s.chatHistory.Value
	if err := hc.SetSettings(&s.settings); err != nil {
		s.errText = err.Error()
		return false
	}
	s.errText = ""
	if s.onSave != nil {
		s.onSave(s.settings)
	}
	return true
}

func (s *settingsState) updateEditor(gtx layout.Context, editor *widget.Editor, hc *hasherino.HasherinoController) {
	for {
		event, ok := editor.Update(gtx)
		if !ok {
			return
		}
		if _, changed := event.(widget.ChangeEvent); changed {
			// Invalid intermediate values show an error instead of being saved.
			s.save(hc)
		}
	}
}

func (s *settingsState) update(gtx layout.Context, hc *hasherino.HasherinoController) {
	if s.backdrop.Clicked(gtx) {
		s.open = false
		return
	}
	if s.doneButton.Clicked(gtx) {
		if s.save(hc) {
			s.open = false
		}
		return
	}

	if s.showTime.Update(gtx) {
		s.save(hc)
	}
	if s.chatHistory.Update(gtx) {
		s.save(hc)
	}
	s.updateEditor(gtx, &s.chatLimitEditor, hc)
	s.updateEditor(gtx, &s.emoteSizeEditor, hc)
	s.updateEditor(gtx, &s.fontSizeEditor, hc)
}

func layoutSettings(gtx layout.Context, theme *material.Theme, hc *hasherino.HasherinoController, state *settingsState) layout.Dimensions {
	if !state.open {
		return layout.Dimensions{}
	}
	state.update(gtx, hc)
	if !state.open {
		return layout.Dimensions{}
	}
	return layoutModal(gtx, &state.backdrop, func(gtx layout.Context) layout.Dimensions {
		return layoutSettingsPanel(gtx, theme, state)
	})
}

func layoutSettingsPanel(gtx layout.Context, theme *material.Theme, state *settingsState) layout.Dimensions {
	margin := gtx.Dp(unit.Dp(16))
	maxWidth := gtx.Constraints.Max.X - 2*margin
	if maxWidth < gtx.Dp(unit.Dp(200)) {
		maxWidth = gtx.Constraints.Max.X
	}
	width := gtx.Dp(unit.Dp(420))
	if width > maxWidth {
		width = maxWidth
	}
	height := gtx.Constraints.Max.Y - 2*margin
	if height > gtx.Dp(unit.Dp(600)) {
		height = gtx.Dp(unit.Dp(600))
	}
	if height < gtx.Dp(unit.Dp(240)) {
		height = gtx.Constraints.Max.Y - gtx.Dp(unit.Dp(8))
	}
	if height > gtx.Constraints.Max.Y {
		height = gtx.Constraints.Max.Y
	}
	if width < 0 {
		width = 0
	}
	if height < 0 {
		height = 0
	}
	gtx.Constraints.Min = image.Pt(width, height)
	gtx.Constraints.Max = gtx.Constraints.Min

	background := func(gtx layout.Context) layout.Dimensions {
		size := gtx.Constraints.Min
		radius := gtx.Dp(unit.Dp(10))
		panel := color.NRGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xff}
		paint.FillShape(gtx.Ops, panel, clip.UniformRRect(image.Rectangle{Max: size}, radius).Op(gtx.Ops))
		return layout.Dimensions{Size: size}
	}

	content := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{
			Top:    unit.Dp(16),
			Bottom: unit.Dp(16),
			Left:   unit.Dp(16),
			Right:  unit.Dp(16),
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			rows := state.settingsRows(theme)
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return material.H6(theme, "Settings").Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return state.list.Layout(gtx, len(rows), func(gtx layout.Context, index int) layout.Dimensions {
						return rows[index](gtx)
					})
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return material.Button(theme, &state.doneButton, "Done").Layout(gtx)
					})
				}),
			)
		})
	}

	return state.panel.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Background{}.Layout(gtx, background, content)
	})
}

func (s *settingsState) settingsRows(theme *material.Theme) []layout.Widget {
	rows := []layout.Widget{
		func(gtx layout.Context) layout.Dimensions {
			return layoutSettingsField(gtx, theme, "Chat message limit", &s.chatLimitEditor, "100")
		},
		func(gtx layout.Context) layout.Dimensions {
			return layoutSettingsToggle(gtx, theme, "Chat history", &s.chatHistory)
		},
		func(gtx layout.Context) layout.Dimensions {
			return layoutSettingsToggle(gtx, theme, "Show message time", &s.showTime)
		},
		func(gtx layout.Context) layout.Dimensions {
			return layoutSettingsField(gtx, theme, "Emote size", &s.emoteSizeEditor, "36")
		},
		func(gtx layout.Context) layout.Dimensions {
			return layoutSettingsField(gtx, theme, "Font size", &s.fontSizeEditor, "18")
		},
	}
	if s.errText != "" {
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			label := material.Body1(theme, s.errText)
			label.Color = color.NRGBA{R: 0xff, G: 0x90, B: 0x90, A: 0xff}
			return label.Layout(gtx)
		})
	}
	return rows
}

func layoutSettingsField(gtx layout.Context, theme *material.Theme, label string, editor *widget.Editor, hint string) layout.Dimensions {
	maxWidth := gtx.Dp(unit.Dp(100))
	if limit := gtx.Constraints.Max.X * 2 / 5; maxWidth > limit {
		maxWidth = limit
	}
	if maxWidth < gtx.Dp(unit.Dp(48)) {
		maxWidth = gtx.Dp(unit.Dp(48))
	}
	minWidth := gtx.Dp(unit.Dp(48))
	if minWidth > maxWidth {
		minWidth = maxWidth
	}
	return layout.Inset{Bottom: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				text := material.Body1(theme, label)
				text.Color = theme.Fg
				return text.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X = minWidth
				gtx.Constraints.Max.X = maxWidth
				field := material.Editor(theme, editor, hint)
				field.TextSize = unit.Sp(16)
				return field.Layout(gtx)
			}),
		)
	})
}

func layoutSettingsToggle(gtx layout.Context, theme *material.Theme, label string, value *widget.Bool) layout.Dimensions {
	return layout.Inset{Bottom: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				text := material.Body1(theme, label)
				text.Color = theme.Fg
				return text.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return material.Switch(theme, value, "").Layout(gtx)
			}),
		)
	})
}

func layoutDeviceAuthContent(gtx layout.Context, theme *material.Theme, state *deviceAuthState) layout.Dimensions {
	children := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return material.H6(theme, "Add Twitch account").Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
	}

	switch {
	case state.errText != "":
		children = append(children,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body1(theme, state.errText)
				label.Color = color.NRGBA{R: 0xff, G: 0x90, B: 0x90, A: 0xff}
				return label.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return material.Button(theme, &state.closeButton, "Close").Layout(gtx)
				})
			}),
		)
	case state.userCode == "":
		children = append(children,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return material.Body1(theme, "Requesting a code from Twitch...").Layout(gtx)
			}),
		)
	default:
		children = append(children,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return material.Body1(theme, "Open twitch.tv/activate and enter this code:").Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(12)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				code := material.H3(theme, state.userCode)
				code.Color = theme.ContrastBg
				return code.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layoutFlow(gtx, unit.Dp(8), unit.Dp(8),
					func(gtx layout.Context) layout.Dimensions {
						return material.Button(theme, &state.openButton, "Open Twitch").Layout(gtx)
					},
					func(gtx layout.Context) layout.Dimensions {
						return material.Button(theme, &state.copyButton, "Copy code").Layout(gtx)
					},
					func(gtx layout.Context) layout.Dimensions {
						return material.Button(theme, &state.copyLinkButton, "Copy link").Layout(gtx)
					},
				)
			}),
		)
		if state.openErr != "" {
			children = append(children,
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.Body2(theme, state.openErr)
					label.Color = color.NRGBA{R: 0xff, G: 0xc0, B: 0x70, A: 0xff}
					return label.Layout(gtx)
				}),
			)
		}
		children = append(children,
			layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				status := "Waiting for authorization..."
				if !state.pending {
					status = "Finishing sign-in..."
				}
				return material.Body2(theme, status).Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: unit.Dp(16)}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.E.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return material.Button(theme, &state.closeButton, "Cancel").Layout(gtx)
				})
			}),
		)
	}

	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
}

func main() {
	go func() {
		window := new(app.Window)
		err := run(window)
		if err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run(window *app.Window) error {
	theme := material.NewTheme()
	theme.Palette = material.Palette{
		Bg:         color.NRGBA{R: 0x12, G: 0x12, B: 0x12, A: 0xff},
		Fg:         color.NRGBA{R: 0xe6, G: 0xe6, B: 0xe6, A: 0xff},
		ContrastBg: color.NRGBA{R: 0x4a, G: 0x6f, B: 0xa5, A: 0xff},
		ContrastFg: color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	}
	var ops op.Ops

	callbackMap := make(map[string]func(hasherino.ChatMessage))
	hc := &hasherino.HasherinoController{}
	hc, err := hc.NewWithClientID(callbackMap, deviceClientID)
	if err != nil {
		return err
	}

	settingsButton := new(widget.Clickable)
	settingsIcon, err := widget.NewIcon(icons.ActionSettings)
	if err != nil {
		return err
	}
	settingsStyle := material.IconButton(theme, settingsButton, settingsIcon, "Settings")
	settingsStyle.Background = color.NRGBA{} // No background layer.
	settingsStyle.Color = theme.Fg
	settingsStyle.Size = 20
	settingsStyle.Inset = layout.UniformInset(8)

	usersButton := new(widget.Clickable)
	usersIcon, err := widget.NewIcon(icons.SocialPerson)
	if err != nil {
		return err
	}
	usersStyle := material.IconButton(theme, usersButton, usersIcon, "Users")
	usersStyle.Background = color.NRGBA{} // No background layer.
	usersStyle.Color = theme.Fg
	usersStyle.Size = 20
	usersStyle.Inset = layout.UniformInset(8)

	usersMenu := newUsersMenu()
	if err := loadSavedUsers(hc, usersMenu); err != nil {
		return err
	}

	var listening atomic.Bool
	startListening := func() {
		if !listening.CompareAndSwap(false, true) {
			return
		}
		go func() {
			if err := hc.Listen(); err != nil {
				log.Printf("failed to connect: %s", err)
				listening.Store(false)
			}
		}()
	}

	deviceAuth := &deviceAuthState{}
	deviceAuth.onAccount = func(id, login, token string) error {
		hadActiveAccount := false
		if _, err := hc.GetActiveAccount(); err == nil {
			hadActiveAccount = true
		}
		if err := hc.UpsertAccount(id, login, token); err != nil {
			return err
		}
		if !hadActiveAccount {
			// The new account became the active one, so the chat connection
			// has to be established with its token.
			listening.Store(false)
		}
		startListening()
		return nil
	}
	usersMenu.onAdd = func() {
		if deviceAuth.open {
			return
		}
		deviceAuth.open = true
		deviceAuth.pending = true
		deviceAuth.userCode = ""
		deviceAuth.verificationURI = ""
		deviceAuth.errText = ""
		deviceAuth.openErr = ""
		deviceAuth.events, deviceAuth.cancel = startDeviceAuth(window, deviceClientID, hasherino.TwitchOAuthScopes)
	}
	usersMenu.onRemove = func(index int) {
		if index < 0 || index >= len(usersMenu.users) {
			return
		}
		entry := usersMenu.users[index]
		wasActive := entry.active
		if entry.id == "" {
			removeUserAt(usersMenu, index)
			if wasActive {
				listening.Store(false)
			}
			return
		}
		if err := hc.RemoveAccount(entry.id); err != nil {
			log.Printf("failed to remove account %s: %s", entry.login, err)
			return
		}
		removeUserAt(usersMenu, index)
		if wasActive {
			listening.Store(false)
		}
	}

	addTab := &addTabState{}
	addTab.editor.SingleLine = true
	addTab.editor.Submit = true
	submitAddTab := func(channel string) {
		channel = strings.ToLower(strings.TrimSpace(channel))
		if channel == "" {
			addTab.errText = "Enter a channel name."
			return
		}
		if addTab.pending {
			return
		}
		addTab.pending = true
		addTab.errText = ""
		addTab.events = startAddTab(window, hc, channel)
	}

	messageInput := newMessageInputState()

	savedTabs, err := hc.GetTabs()
	if err != nil {
		return err
	}
	tabs := make([]tabItem, 0, len(savedTabs))
	tabIds := make([]string, 0, len(savedTabs))
	selectedTab := -1
	selectedLogin := ""
	if selected, err := hc.GetSelectedTab(); err == nil {
		selectedLogin = selected.Login
	} else {
		log.Printf("failed to load selected tab: %s", err)
	}
	for _, saved := range savedTabs {
		item := newTabItem(saved)
		registerTabCallback(window, callbackMap, &item)
		loadChatHistory(window, hc, &item)
		loadChannelEmotes(window, hc, &item)
		loadChannelBadges(window, hc, &item)
		tabs = append(tabs, item)
		tabIds = append(tabIds, saved.Id)
		if selectedLogin != "" && strings.EqualFold(saved.Login, selectedLogin) {
			selectedTab = len(tabs) - 1
		}
	}
	if len(tabs) > 0 {
		if selectedTab < 0 {
			selectedTab = 0
		}
		if err := hc.SetSelectedTab(tabs[selectedTab].login); err != nil {
			log.Printf("failed to save selected tab: %s", err)
		}
		if err := hc.AddTempTabs(&tabIds); err != nil {
			log.Printf("failed to load tab emotes: %s", err)
		}
	}
	if len(usersMenu.users) > 0 {
		startListening()
	}

	settings := newSettingsState()
	settings.load(hc)
	settings.onSave = func(appSettings hasherino.AppSettings) {
		settings.settings = appSettings
		applyAppSettings(theme, tabs, appSettings)
		window.Invalidate()
	}
	if settings.loaded {
		applyAppSettings(theme, tabs, settings.settings)
	}

	userAddIcon, err := widget.NewIcon(icons.SocialPersonAdd)
	if err != nil {
		return err
	}
	userDeleteIcon, err := widget.NewIcon(icons.ActionDelete)
	if err != nil {
		return err
	}

	closeIcon, err := widget.NewIcon(icons.NavigationClose)
	if err != nil {
		return err
	}

	addButton := new(widget.Clickable)
	addIcon, err := widget.NewIcon(icons.ContentAdd)
	if err != nil {
		return err
	}
	addStyle := material.IconButton(theme, addButton, addIcon, "Add")
	addStyle.Background = color.NRGBA{} // No background layer.
	addStyle.Color = theme.Fg
	addStyle.Size = 20
	addStyle.Inset = layout.UniformInset(8)

	for {
		switch e := window.Event().(type) {
		case app.DestroyEvent:
			deviceAuth.close()
			addTab.close()
			return e.Err
		case app.FrameEvent:
			// This graphics context is used for managing the rendering state.
			gtx := app.NewContext(&ops, e)

			// Apply device authorization updates from the background poller.
			deviceAuth.drain(usersMenu)

			// Apply add-tab updates from the background controller call.
			addTab.drain(func(event addTabEvent) {
				switch {
				case event.err != nil:
					addTab.pending = false
					addTab.errText = event.err.Error()
				case event.tab != nil:
					addTab.pending = false
					item := newTabItem(event.tab)
					applyAppSettingsToChat(item.chat, settings.settings)
					registerTabCallback(window, callbackMap, &item)
					loadChatHistory(window, hc, &item)
					loadChannelEmotes(window, hc, &item)
					loadChannelBadges(window, hc, &item)
					tabs = append(tabs, item)
					selectedTab = len(tabs) - 1
					if err := hc.SetSelectedTab(item.login); err != nil {
						log.Printf("failed to save selected tab: %s", err)
					}
					addTab.close()
				default:
					addTab.pending = false
					addTab.errText = "Unexpected add-tab result."
				}
			})

			// Paint the window background using the theme's background color.
			paint.Fill(gtx.Ops, theme.Bg)

			// While a modal dialog is open, the UI underneath must not react.
			if !deviceAuth.open && !addTab.open && !settings.open {
				if settingsButton.Clicked(gtx) {
					settings.load(hc)
					settings.open = true
					settings.list.Position = layout.Position{}
					usersMenu.open = false
				}

				// Toggle the users dropdown.
				if usersButton.Clicked(gtx) {
					usersMenu.open = !usersMenu.open
				}

				// Open the add-tab dialog.
				if addButton.Clicked(gtx) {
					addTab.open = true
					addTab.pending = false
					addTab.errText = ""
					addTab.editor.SetText("")
					gtx.Execute(key.FocusCmd{Tag: &addTab.editor})
				}

				// Process tab clicks before layout so the UI updates immediately.
				closeIndex := -1
				for i := range tabs {
					if tabs[i].closeButton.Clicked(gtx) {
						closeIndex = i
						break
					}
					if tabs[i].button.Clicked(gtx) {
						selectedTab = i
						if err := hc.SetSelectedTab(tabs[i].login); err != nil {
							log.Printf("failed to save selected tab: %s", err)
						}
					}
				}
				if closeIndex >= 0 {
					closed := tabs[closeIndex]
					if err := hc.RemoveTab(closed.id); err != nil {
						log.Printf("failed to remove tab %s: %s", closed.login, err)
					} else {
						delete(callbackMap, closed.login)
						tabs = append(tabs[:closeIndex], tabs[closeIndex+1:]...)
						adjustSelection(&selectedTab, closeIndex, len(tabs))
						if selectedTab >= 0 && selectedTab < len(tabs) {
							if err := hc.SetSelectedTab(tabs[selectedTab].login); err != nil {
								log.Printf("failed to save selected tab: %s", err)
							}
						}
					}
				}
			}

			// Handle the add-tab dialog's own buttons and submit events.
			if addTab.open {
				if addTab.addButton.Clicked(gtx) && !addTab.pending {
					submitAddTab(addTab.editor.Text())
				}
				for {
					event, ok := addTab.editor.Update(gtx)
					if !ok {
						break
					}
					if submit, ok := event.(widget.SubmitEvent); ok {
						submitAddTab(submit.Text)
					}
				}
			}

			// Build the toolbar: settings and users buttons, tabs, then the add button.
			toolbar := make([]layout.Widget, 0, len(tabs)+3)
			toolbar = append(toolbar, settingsStyle.Layout, usersStyle.Layout)
			toolbar = append(toolbar, layoutTabs(gtx, theme, tabs, selectedTab, closeIcon)...)
			toolbar = append(toolbar, addStyle.Layout)

			selectedLogin := ""
			if selectedTab >= 0 && selectedTab < len(tabs) {
				selectedLogin = tabs[selectedTab].login
			}

			// Draw the toolbar, chat, message input, and overlays.
			layout.Stack{Alignment: layout.NW}.Layout(gtx,
				layout.Expanded(func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: gtx.Constraints.Max}
				}),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layoutFlow(gtx, unit.Dp(4), unit.Dp(4), toolbar...)
						}),
						layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							if selectedTab < 0 || selectedTab >= len(tabs) || tabs[selectedTab].chat == nil {
								return layout.Dimensions{}
							}
							return tabs[selectedTab].chat.layout(gtx, theme)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layoutMessageInput(gtx, theme, messageInput, hc, window, selectedLogin)
						}),
					)
				}),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					if !usersMenu.open {
						return layout.Dimensions{}
					}
					return layout.Inset{Top: unit.Dp(44), Left: unit.Dp(40)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layoutUsersMenu(gtx, theme, usersMenu, userAddIcon, userDeleteIcon)
					})
				}),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					if !deviceAuth.open {
						return layout.Dimensions{}
					}
					return layoutDeviceAuth(gtx, theme, deviceAuth)
				}),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					if !addTab.open {
						return layout.Dimensions{}
					}
					return layoutAddTab(gtx, theme, addTab)
				}),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					return layoutSettings(gtx, theme, hc, settings)
				}),
			)

			// Pass the drawing operations to the GPU.
			e.Frame(gtx.Ops)
		}
	}
}

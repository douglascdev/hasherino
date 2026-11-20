package main

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	giotext "gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

const (
	pickerTabSubs = iota
	pickerTabChannel
	pickerTabGlobal
	pickerTabEmojis
	pickerTabCount
)

var pickerTabLabels = [pickerTabCount]string{"Subs", "Channel", "Global", "Emojis"}

type emotePickerEvent struct {
	requestID uint64
	login     string
	data      *hasherino.EmotePickerData
	err       error
}

// emotePickerState backs the modal emote and emoji picker.
type emotePickerState struct {
	open   bool
	tab    int
	login  string
	search widget.Editor

	searchFocus bool
	closeButton widget.Clickable
	backdrop    widget.Clickable
	panelClick  widget.Clickable
	tabButtons  [pickerTabCount]widget.Clickable
	list        widget.List
	buttons     map[string]*widget.Clickable

	data      map[string]*hasherino.EmotePickerData
	loading   bool
	errText   string
	events    chan emotePickerEvent
	requestID uint64

	lastQuery string
	lastTab   int
}

type pickerSection struct {
	title  string
	emotes []*hasherino.Emote
	emojis []emojiEntry
}

type pickerCell struct {
	key   string
	text  string
	name  string
	emote *hasherino.Emote
	emoji string
}

type pickerRow struct {
	title string
	cells []pickerCell
}

var chatEmotePicker = newEmotePickerState()

func newEmotePickerState() *emotePickerState {
	state := &emotePickerState{
		buttons: make(map[string]*widget.Clickable),
		data:    make(map[string]*hasherino.EmotePickerData),
		events:  make(chan emotePickerEvent, 2),
		lastTab: -1,
	}
	state.search.SingleLine = true
	state.list.Axis = layout.Vertical
	state.list.Gap = 0
	return state
}

func (s *emotePickerState) openPicker(hc *hasherino.HasherinoController, window *app.Window, login string) {
	if login == "" {
		return
	}
	s.open = true
	s.login = login
	s.tab = pickerTabSubs
	s.search.SetText("")
	s.searchFocus = true
	s.errText = ""
	s.lastQuery = ""
	s.lastTab = -1
	s.list.Position = layout.Position{}
	if s.data == nil {
		s.data = make(map[string]*hasherino.EmotePickerData)
	}

	if data := s.data[login]; data != nil {
		s.loading = false
	} else {
		s.loading = true
		s.requestID++
		requestID := s.requestID
		go func() {
			data, err := hc.LoadEmotePicker(login)
			s.events <- emotePickerEvent{requestID: requestID, login: login, data: data, err: err}
			window.Invalidate()
		}()
	}
	window.Invalidate()
}

func (s *emotePickerState) drain() {
	for {
		select {
		case event := <-s.events:
			if event.requestID != s.requestID {
				continue
			}
			s.loading = false
			if event.data != nil {
				s.data[event.login] = event.data
			}
			if event.err != nil {
				s.errText = event.err.Error()
			} else {
				s.errText = ""
			}
		default:
			return
		}
	}
}

func (s *emotePickerState) closePicker() {
	s.open = false
	s.searchFocus = false
}

// insertText adds an emote name or emoji at the message editor's cursor.
func (s *messageInputState) insertText(text string) {
	if text == "" {
		return
	}
	start, end := s.editor.CaretPos()
	runes := []rune(s.editor.Text())
	if start < 0 {
		start = 0
	}
	if start > len(runes) {
		start = len(runes)
	}
	if end < start {
		end = start
	}
	if end > len(runes) {
		end = len(runes)
	}
	before := runes[:start]
	after := runes[end:]

	insert := []rune(text)
	if len(before) > 0 && !unicode.IsSpace(before[len(before)-1]) {
		insert = append([]rune{' '}, insert...)
	}
	insert = append(insert, ' ')

	result := make([]rune, 0, len(before)+len(insert)+len(after))
	result = append(result, before...)
	result = append(result, insert...)
	result = append(result, after...)
	s.setText(string(result))
	caret := len(before) + len(insert)
	s.editor.SetCaret(caret, caret)
}

func (s *emotePickerState) layout(gtx layout.Context, theme *material.Theme, window *app.Window, input *messageInputState, closeIcon *widget.Icon) layout.Dimensions {
	s.drain()
	if !s.open {
		return layout.Dimensions{}
	}
	if s.searchFocus {
		s.searchFocus = false
		gtx.Execute(key.FocusCmd{Tag: &s.search})
	}
	closePicker := func(gtx layout.Context) {
		s.closePicker()
		gtx.Execute(key.FocusCmd{Tag: &input.editor})
		gtx.Execute(op.InvalidateCmd{})
	}
	if s.closeButton.Clicked(gtx) {
		closePicker(gtx)
		return layout.Dimensions{}
	}
	if s.backdrop.Clicked(gtx) {
		closePicker(gtx)
		return layout.Dimensions{}
	}

	return layoutModal(gtx, &s.backdrop, func(gtx layout.Context) layout.Dimensions {
		return s.layoutPanel(gtx, theme, window, input, closeIcon)
	})
}

func (s *emotePickerState) layoutPanel(gtx layout.Context, theme *material.Theme, window *app.Window, input *messageInputState, closeIcon *widget.Icon) layout.Dimensions {
	margin := gtx.Dp(unit.Dp(16))
	width := gtx.Constraints.Max.X - 2*margin
	if maxWidth := gtx.Dp(unit.Dp(680)); width > maxWidth {
		width = maxWidth
	}
	height := gtx.Constraints.Max.Y - 2*margin
	if maxHeight := gtx.Dp(unit.Dp(680)); height > maxHeight {
		height = maxHeight
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
		return layout.UniformInset(unit.Dp(12)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			children := []layout.FlexChild{
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return material.H6(theme, "Emotes").Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							style := material.IconButton(theme, &s.closeButton, closeIcon, "Close emote picker")
							style.Background = color.NRGBA{}
							style.Color = theme.Fg
							style.Size = 18
							style.Inset = layout.UniformInset(2)
							return style.Layout(gtx)
						}),
					)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					editor := material.Editor(theme, &s.search, "Search emotes and emojis")
					editor.TextSize = unit.Sp(16)
					return editor.Layout(gtx)
				}),
				layout.Rigid(layout.Spacer{Height: unit.Dp(8)}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return s.layoutTabs(gtx, theme)
				}),
			}
			if s.errText != "" {
				children = append(children,
					layout.Rigid(layout.Spacer{Height: unit.Dp(4)}.Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						label := material.Caption(theme, s.errText)
						label.Color = color.NRGBA{R: 0xff, G: 0xc0, B: 0x70, A: 0xff}
						label.MaxLines = 2
						return label.Layout(gtx)
					}),
				)
			}
			children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return s.layoutContent(gtx, theme, window, input)
			}))
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	}
	return s.panelClick.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Background{}.Layout(gtx, background, content)
	})
}

func (s *emotePickerState) layoutTabs(gtx layout.Context, theme *material.Theme) layout.Dimensions {
	children := make([]layout.FlexChild, 0, pickerTabCount)
	for i := range pickerTabLabels {
		i := i
		children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return s.layoutTab(gtx, theme, i)
		}))
	}
	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, children...)
}

func (s *emotePickerState) layoutTab(gtx layout.Context, theme *material.Theme, index int) layout.Dimensions {
	label := pickerTabLabels[index]
	if s.tabButtons[index].Clicked(gtx) {
		s.tab = index
		s.list.Position = layout.Position{}
		s.lastTab = -1
	}
	selected := s.tab == index
	button := &s.tabButtons[index]
	return button.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(2)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Stack{Alignment: layout.S}.Layout(gtx,
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					text := material.Body2(theme, label)
					text.Alignment = giotext.Middle
					text.Color = theme.Fg
					if !selected {
						text.Color.A = 0x99
					}
					return text.Layout(gtx)
				}),
				layout.Expanded(func(gtx layout.Context) layout.Dimensions {
					if !selected {
						return layout.Dimensions{}
					}
					size := image.Point{X: gtx.Constraints.Min.X, Y: gtx.Dp(unit.Dp(2))}
					paint.FillShape(gtx.Ops, theme.ContrastBg, clip.Rect{Max: size}.Op())
					return layout.Dimensions{Size: size}
				}),
			)
		})
	})
}

func (s *emotePickerState) layoutContent(gtx layout.Context, theme *material.Theme, window *app.Window, input *messageInputState) layout.Dimensions {
	data := s.data[s.login]
	if s.tab != pickerTabEmojis && data == nil {
		message := "Loading emotes..."
		if s.errText != "" {
			message = s.errText
		}
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return material.Body2(theme, message).Layout(gtx)
		})
	}

	query := strings.ToLower(strings.TrimSpace(s.search.Text()))
	if query != s.lastQuery || s.tab != s.lastTab {
		s.list.Position = layout.Position{}
		s.lastQuery = query
		s.lastTab = s.tab
	}

	animator := chatAnimator{now: time.Now(), emoteHeight: gtx.Dp(unit.Dp(28))}
	return layout.Inset{Left: unit.Dp(4), Right: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		rows, cellWidth := s.buildRows(gtx, query)
		if len(rows) == 0 {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return material.Body2(theme, "No emotes found").Layout(gtx)
			})
		}
		dims := material.List(theme, &s.list).Layout(gtx, len(rows), func(gtx layout.Context, index int) layout.Dimensions {
			row := rows[index]
			if row.title != "" {
				return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(4), Left: unit.Dp(4)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					label := material.Subtitle2(theme, row.title)
					label.Color = theme.Fg
					label.Color.A = 0xcc
					return label.Layout(gtx)
				})
			}
			return s.layoutRows(gtx, theme, window, input, row.cells, cellWidth, &animator)
		})
		animator.invalidate(gtx)
		return dims
	})
}

func (s *emotePickerState) buildRows(gtx layout.Context, query string) ([]pickerRow, int) {
	sections := s.sections(query)
	available := gtx.Constraints.Max.X
	cellWidth := gtx.Dp(unit.Dp(68))
	columns := available / cellWidth
	if columns < 1 {
		columns = 1
	}
	if available > 0 {
		cellWidth = available / columns
	}
	rows := make([]pickerRow, 0, len(sections))
	for _, section := range sections {
		if section.title != "" {
			rows = append(rows, pickerRow{title: section.title})
		}
		cells := make([]pickerCell, 0, len(section.emotes)+len(section.emojis))
		for _, emote := range section.emotes {
			cells = append(cells, pickerCell{
				key:   "e:" + strconv.FormatInt(int64(emote.Source), 10) + ":" + emote.Id + ":" + emote.Name,
				text:  emote.Name,
				name:  emote.Name,
				emote: emote,
			})
		}
		for _, emoji := range section.emojis {
			cells = append(cells, pickerCell{
				key:   "m:" + emoji.Emoji,
				text:  emoji.Emoji,
				name:  emoji.Name,
				emoji: emoji.Emoji,
			})
		}
		for len(cells) > 0 {
			count := columns
			if count > len(cells) {
				count = len(cells)
			}
			rows = append(rows, pickerRow{cells: cells[:count]})
			cells = cells[count:]
		}
	}
	return rows, cellWidth
}

func (s *emotePickerState) sections(query string) []pickerSection {
	match := func(value string) bool {
		return query == "" || strings.Contains(strings.ToLower(value), query)
	}
	sections := make([]pickerSection, 0)
	addEmotes := func(title string, emotes []*hasherino.Emote) {
		section := pickerSection{title: title}
		for _, emote := range emotes {
			if emote != nil && match(emote.Name) {
				section.emotes = append(section.emotes, emote)
			}
		}
		if len(section.emotes) > 0 {
			sections = append(sections, section)
		}
	}

	data := s.data[s.login]
	switch s.tab {
	case pickerTabSubs:
		if data != nil {
			for _, group := range data.Subs {
				addEmotes(group.Title, group.Emotes)
			}
		}
	case pickerTabChannel:
		if data != nil {
			title := s.login
			if title == "" {
				title = "Channel"
			}
			addEmotes(title, data.Channel)
		}
	case pickerTabGlobal:
		if data != nil {
			addEmotes("Twitch", data.GlobalTwitch)
			addEmotes("7TV", data.GlobalSevenTV)
		}
	case pickerTabEmojis:
		section := pickerSection{title: "Emojis"}
		for _, emoji := range allEmojis() {
			if query == "" || match(emoji.Name) || match(emoji.Slug) || emoji.Emoji == query {
				section.emojis = append(section.emojis, emoji)
			}
		}
		if len(section.emojis) > 0 {
			sections = append(sections, section)
		}
	}
	return sections
}

func (s *emotePickerState) layoutRows(gtx layout.Context, theme *material.Theme, window *app.Window, input *messageInputState, cells []pickerCell, cellWidth int, animator *chatAnimator) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(cells))
	for _, cell := range cells {
		cell := cell
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = cellWidth
			gtx.Constraints.Max.X = cellWidth
			return s.layoutCell(gtx, theme, window, input, cell, cellWidth, animator)
		}))
	}
	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx, children...)
}

func (s *emotePickerState) layoutCell(gtx layout.Context, theme *material.Theme, window *app.Window, input *messageInputState, cell pickerCell, cellWidth int, animator *chatAnimator) layout.Dimensions {
	button := s.buttonFor(cell.key)
	if button.Clicked(gtx) {
		input.insertText(cell.text)
		s.closePicker()
		gtx.Execute(key.FocusCmd{Tag: &input.editor})
		gtx.Execute(op.InvalidateCmd{})
		return layout.Dimensions{}
	}
	return button.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		pointer.CursorPointer.Add(gtx.Ops)
		gtx.Constraints.Min.X = cellWidth
		gtx.Constraints.Max.X = cellWidth
		return layout.Inset{Bottom: unit.Dp(6)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			imageHeight := gtx.Dp(unit.Dp(32))
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.Y = imageHeight
					gtx.Constraints.Max.Y = imageHeight
					return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						if cell.emote != nil {
							media := cachedChatEmoteMedia(cell.emote)
							if media == nil {
								loadChatEmote(window, cell.emote)
								return layout.Dimensions{Size: image.Pt(0, imageHeight)}
							}
							return layoutInlineEmote(gtx, media, animator, nil, nil)
						}
						label := material.Body1(theme, cell.emoji)
						label.TextSize = unit.Sp(24)
						label.Color = theme.Fg
						return label.Layout(gtx)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.Caption(theme, cell.name)
					label.MaxLines = 1
					label.Alignment = giotext.Middle
					label.Color = theme.Fg
					label.Color.A = 0xbb
					gtx.Constraints.Min.X = cellWidth
					gtx.Constraints.Max.X = cellWidth
					return label.Layout(gtx)
				}),
			)
		})
	})
}

func (s *emotePickerState) buttonFor(key string) *widget.Clickable {
	if s.buttons == nil {
		s.buttons = make(map[string]*widget.Clickable)
	}
	button := s.buttons[key]
	if button == nil {
		button = new(widget.Clickable)
		s.buttons[key] = button
	}
	return button
}

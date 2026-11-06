package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	_ "image/png"
	"io"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/exp/shiny/materialdesign/icons"
	_ "golang.org/x/image/webp"

	"github.com/Hashy-Software/hasherino-go/hasherino"
	"github.com/pkg/browser"
)

const deviceClientID = "9ofmajflpp4p2hrq81vjdyvcwlad97"

type tabItem struct {
	title       string
	button      widget.Clickable
	closeButton widget.Clickable
	chat        *chat
}

type chatPart struct {
	text  string
	media *chatMedia
}

type chatMessage struct {
	text  string
	color color.NRGBA
	media *chatMedia
	parts []chatPart
}

type chatMedia struct {
	frames []paint.ImageOp
	delays []time.Duration
	total  time.Duration
	start  time.Time
}

type chat struct {
	list     widget.List
	messages []chatMessage
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
	events          <-chan deviceAuthEvent
	cancel          context.CancelFunc
	onAccount       func(id, login, token string) error
	backdrop        widget.Clickable
	openButton      widget.Clickable
	copyButton      widget.Clickable
	closeButton     widget.Clickable
}

func layoutTabs(gtx layout.Context, theme *material.Theme, tabs *[]tabItem, selected *int, closeIcon *widget.Icon) []layout.Widget {
	items := *tabs

	// Process clicks before laying out the tabs so removed tabs disappear immediately.
	closeIndex := -1
	for i := range items {
		if items[i].closeButton.Clicked(gtx) {
			closeIndex = i
		} else if items[i].button.Clicked(gtx) {
			*selected = i
		}
	}
	if closeIndex >= 0 {
		items = append(items[:closeIndex], items[closeIndex+1:]...)
		*tabs = items
		adjustSelection(selected, closeIndex, len(items))
	}

	widgets := make([]layout.Widget, len(items))
	for i := range items {
		i := i
		tab := &items[i]
		widgets[i] = func(gtx layout.Context) layout.Dimensions {
			return layoutTab(gtx, theme, tab, i == *selected, closeIcon)
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
	return &chat{list: list}
}

func decodeChatMedia(data []byte) (*chatMedia, error) {
	if animation, err := gif.DecodeAll(bytes.NewReader(data)); err == nil {
		frames := make([]image.Image, len(animation.Image))
		delays := make([]time.Duration, len(animation.Delay))
		for i, frame := range animation.Image {
			frames[i] = frame
		}
		for i, delay := range animation.Delay {
			delays[i] = time.Duration(delay) * 10 * time.Millisecond
		}
		return newChatMedia(frames, delays), nil
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return newChatMedia([]image.Image{img}, []time.Duration{0}), nil
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

func (c *chat) layout(gtx layout.Context, theme *material.Theme) layout.Dimensions {
	now := time.Now()
	c.list.Gap = gtx.Dp(unit.Dp(8))
	return layout.Inset{
		Top:    unit.Dp(8),
		Bottom: unit.Dp(8),
		Left:   unit.Dp(12),
		Right:  unit.Dp(12),
	}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return material.List(theme, &c.list).Layout(gtx, len(c.messages), func(gtx layout.Context, index int) layout.Dimensions {
			return layoutChatMessage(gtx, theme, c.messages[index], now)
		})
	})
}

func layoutChatMessage(gtx layout.Context, theme *material.Theme, message chatMessage, now time.Time) layout.Dimensions {
	if len(message.parts) > 0 {
		return layoutChatParts(gtx, theme, message, now)
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
		return layoutChatMedia(gtx, message.media, now)
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
				return layoutChatMedia(gtx, message.media, now)
			}),
			layout.Rigid(layout.Spacer{Width: unit.Dp(12)}.Layout),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layoutChatText(gtx, theme, message)
			}),
		)
	}
}

func layoutChatText(gtx layout.Context, theme *material.Theme, message chatMessage) layout.Dimensions {
	label := material.Body1(theme, message.text)
	label.Color = message.color
	return label.Layout(gtx)
}

func layoutChatMedia(gtx layout.Context, media *chatMedia, now time.Time) layout.Dimensions {
	src, next := media.frame(now)
	if !next.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: next})
	}
	img := widget.Image{Src: src, Fit: widget.Contain, Position: layout.W}
	return img.Layout(gtx)
}

func layoutChatParts(gtx layout.Context, theme *material.Theme, message chatMessage, now time.Time) layout.Dimensions {
	widgets := make([]layout.Widget, 0, len(message.parts))
	for _, part := range message.parts {
		part := part
		if part.media != nil {
			widgets = append(widgets, func(gtx layout.Context) layout.Dimensions {
				return layoutInlineEmote(gtx, part.media, now)
			})
			continue
		}
		for _, word := range strings.Fields(part.text) {
			word := word
			widgets = append(widgets, func(gtx layout.Context) layout.Dimensions {
				return layoutInlineText(gtx, theme, word+" ", message.color)
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

func layoutInlineEmote(gtx layout.Context, media *chatMedia, now time.Time) layout.Dimensions {
	src, next := media.frame(now)
	if !next.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: next})
	}
	size := gtx.Dp(unit.Dp(22))
	gtx.Constraints.Min = image.Point{}
	gtx.Constraints.Max = image.Point{X: size, Y: size}
	dims := widget.Image{Src: src, Fit: widget.Contain, Position: layout.Center}.Layout(gtx)
	dims.Baseline = dims.Size.Y
	return dims
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
		ascent int
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
		descent := 0
		for _, item := range rows[i].items {
			if item.dims.Baseline > rows[i].ascent {
				rows[i].ascent = item.dims.Baseline
			}
			if d := item.dims.Size.Y - item.dims.Baseline; d > descent {
				descent = d
			}
		}
		rows[i].height = rows[i].ascent + descent
	}

	contentWidth := 0
	y := 0
	for _, row := range rows {
		if row.width > contentWidth {
			contentWidth = row.width
		}
		x := 0
		for _, item := range row.items {
			pos := image.Point{X: x, Y: y + row.ascent - item.dims.Baseline}
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

func layoutDeviceAuth(gtx layout.Context, theme *material.Theme, state *deviceAuthState) layout.Dimensions {
	// Consume clicks on the backdrop so they cannot reach the UI underneath.
	state.backdrop.Clicked(gtx)

	if state.closeButton.Clicked(gtx) {
		state.close()
		return layout.Dimensions{}
	}
	if state.openButton.Clicked(gtx) && state.verificationURI != "" {
		browser.OpenURL(state.verificationURI)
	}
	if state.copyButton.Clicked(gtx) && state.userCode != "" {
		gtx.Execute(clipboard.WriteCmd{
			Type: "text/plain",
			Data: io.NopCloser(strings.NewReader(state.userCode)),
		})
	}
	if !state.open {
		return layout.Dimensions{}
	}

	return layout.Stack{Alignment: layout.Center}.Layout(gtx,
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return state.backdrop.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				paint.Fill(gtx.Ops, color.NRGBA{A: 0xb0})
				return layout.Dimensions{Size: gtx.Constraints.Max}
			})
		}),
		layout.Stacked(func(gtx layout.Context) layout.Dimensions {
			return layoutDeviceAuthPanel(gtx, theme, state)
		}),
	)
}

func layoutDeviceAuthPanel(gtx layout.Context, theme *material.Theme, state *deviceAuthState) layout.Dimensions {
	width := gtx.Dp(unit.Dp(360))
	if width > gtx.Constraints.Max.X {
		width = gtx.Constraints.Max.X
	}
	gtx.Constraints.Min.X = width
	gtx.Constraints.Max.X = width
	gtx.Constraints.Min.Y = 0

	background := func(gtx layout.Context) layout.Dimensions {
		size := gtx.Constraints.Min
		radius := gtx.Dp(unit.Dp(10))
		panel := color.NRGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xff}
		paint.FillShape(gtx.Ops, panel, clip.UniformRRect(image.Rectangle{Max: size}, radius).Op(gtx.Ops))
		return layout.Dimensions{Size: size}
	}

	content := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{
			Top:    unit.Dp(20),
			Bottom: unit.Dp(20),
			Left:   unit.Dp(20),
			Right:  unit.Dp(20),
		}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
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
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return material.Button(theme, &state.openButton, "Open Twitch").Layout(gtx)
							}),
							layout.Rigid(layout.Spacer{Width: unit.Dp(8)}.Layout),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return material.Button(theme, &state.copyButton, "Copy code").Layout(gtx)
							}),
						)
					}),
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
		})
	}

	return layout.Background{}.Layout(gtx, background, content)
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
	hc, err := hc.New(callbackMap)
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
	if len(usersMenu.users) > 0 {
		startListening()
	}

	deviceAuth := &deviceAuthState{}
	deviceAuth.onAccount = func(id, login, token string) error {
		if err := hc.UpsertAccount(id, login, token); err != nil {
			return err
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
	userAddIcon, err := widget.NewIcon(icons.SocialPersonAdd)
	if err != nil {
		return err
	}
	userDeleteIcon, err := widget.NewIcon(icons.ActionDelete)
	if err != nil {
		return err
	}

	tabs := []tabItem{
		{title: "Tab 1", chat: newChat()},
		{title: "Tab 2", chat: newChat()},
		{title: "Tab 3", chat: newChat()},
	}
	selectedTab := 0
	nextTab := len(tabs) + 1

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
			return e.Err
		case app.FrameEvent:
			// This graphics context is used for managing the rendering state.
			gtx := app.NewContext(&ops, e)

			// Apply device authorization updates from the background poller.
			deviceAuth.drain(usersMenu)

			// Paint the window background using the theme's background color.
			paint.Fill(gtx.Ops, theme.Bg)

			// Handle settings button clicks.
			if settingsButton.Clicked(gtx) {
				log.Println("settings clicked")
			}

			// Toggle the users dropdown.
			if usersButton.Clicked(gtx) {
				usersMenu.open = !usersMenu.open
			}

			// Handle add button clicks.
			if addButton.Clicked(gtx) {
				title := fmt.Sprintf("Tab %d", nextTab)
				tabs = append(tabs, tabItem{title: title, chat: newChat()})
				selectedTab = len(tabs) - 1
				nextTab++
			}

			// Build the toolbar: settings and users buttons, tabs, then the add button.
			toolbar := make([]layout.Widget, 0, len(tabs)+3)
			toolbar = append(toolbar, settingsStyle.Layout, usersStyle.Layout)
			toolbar = append(toolbar, layoutTabs(gtx, theme, &tabs, &selectedTab, closeIcon)...)
			toolbar = append(toolbar, addStyle.Layout)

			// Draw the toolbar, chat, and users dropdown overlay.
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
			)

			// Pass the drawing operations to the GPU.
			e.Frame(gtx.Ops)
		}
	}
}

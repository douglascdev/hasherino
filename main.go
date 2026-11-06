package main

import (
	"fmt"
	"image"
	"image/color"
	"log"
	"os"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/exp/shiny/materialdesign/icons"
)

type tabItem struct {
	title       string
	button      widget.Clickable
	closeButton widget.Clickable
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

	tabs := []tabItem{
		{title: "Tab 1"},
		{title: "Tab 2"},
		{title: "Tab 3"},
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
			return e.Err
		case app.FrameEvent:
			// This graphics context is used for managing the rendering state.
			gtx := app.NewContext(&ops, e)

			// Paint the window background using the theme's background color.
			paint.Fill(gtx.Ops, theme.Bg)

			// Handle settings button clicks.
			if settingsButton.Clicked(gtx) {
				log.Println("settings clicked")
			}

			// Handle user button clicks.
			if usersButton.Clicked(gtx) {
				log.Println("users clicked")
			}

			// Handle add button clicks.
			if addButton.Clicked(gtx) {
				tabs = append(tabs, tabItem{title: fmt.Sprintf("Tab %d", nextTab)})
				selectedTab = len(tabs) - 1
				nextTab++
			}

			// Build the toolbar: settings and users buttons, tabs, then the add button.
			toolbar := make([]layout.Widget, 0, len(tabs)+3)
			toolbar = append(toolbar, settingsStyle.Layout, usersStyle.Layout)
			toolbar = append(toolbar, layoutTabs(gtx, theme, &tabs, &selectedTab, closeIcon)...)
			toolbar = append(toolbar, addStyle.Layout)

			// Draw the toolbar, wrapping to the next row when it runs out of width.
			layout.Stack{Alignment: layout.NW}.Layout(gtx,
				layout.Expanded(func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: gtx.Constraints.Max}
				}),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					return layoutFlow(gtx, unit.Dp(4), unit.Dp(4), toolbar...)
				}),
			)

			// Pass the drawing operations to the GPU.
			e.Frame(gtx.Ops)
		}
	}
}

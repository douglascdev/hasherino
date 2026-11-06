package main

import (
	"image/color"
	"log"
	"os"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/exp/shiny/materialdesign/icons"
)

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

			// Draw the settings button in the top-right corner.
			layout.Stack{Alignment: layout.NW}.Layout(gtx,
				layout.Expanded(func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: gtx.Constraints.Max}
				}),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					return settingsStyle.Layout(gtx)
				}),
			)

			// Pass the drawing operations to the GPU.
			e.Frame(gtx.Ops)
		}
	}
}

package main

import (
	"log"
	"os"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
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
	var ops op.Ops

	settingsButton := new(widget.Clickable)
	settingsIcon, err := widget.NewIcon(icons.ActionSettings)
	if err != nil {
		return err
	}

	for {
		switch e := window.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			// This graphics context is used for managing the rendering state.
			gtx := app.NewContext(&ops, e)

			// Handle settings button clicks.
			if settingsButton.Clicked(gtx) {
				log.Println("settings clicked")
			}

			// Draw the settings button in the top-right corner.
			layout.Stack{Alignment: layout.NE}.Layout(gtx,
				layout.Expanded(func(gtx layout.Context) layout.Dimensions {
					return layout.Dimensions{Size: gtx.Constraints.Max}
				}),
				layout.Stacked(func(gtx layout.Context) layout.Dimensions {
					return material.IconButton(theme, settingsButton, settingsIcon, "Settings").Layout(gtx)
				}),
			)

			// Pass the drawing operations to the GPU.
			e.Frame(gtx.Ops)
		}
	}
}


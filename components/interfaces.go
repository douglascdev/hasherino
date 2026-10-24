package components

import "fyne.io/fyne/v2"

type LazyLoadedWidget interface {
	fyne.CanvasObject

	LazyLoad() error
	LazyUnload() error
}

type EmoteWidget interface {
	LazyLoadedWidget

	SetMinSize(fyne.Size)
}

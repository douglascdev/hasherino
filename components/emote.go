package components

import (
	"fyne.io/fyne/v2"
	"github.com/Hashy-Software/hasherino-go/hasherino"
)

func NewEmote(emote *hasherino.Emote, clickCallback func(string) error) (EmoteWidget, error) {
	if emote.Animated {
		return NewEmoteGif(emote, clickCallback, true)
	}
	return NewWebpWidget(emote, clickCallback)
}

// EmoteSizeFor returns the size an emote is rendered at. Emotes are capped
// by the height of the given size and keep their aspect ratio, so wide
// emotes are not squeezed into a square. Emotes with unknown dimensions
// fall back to the square size.
func EmoteSizeFor(emote *hasherino.Emote, size fyne.Size) fyne.Size {
	if emote.Width <= 0 || emote.Height <= 0 {
		return size
	}
	aspect := float32(emote.Width) / float32(emote.Height)
	return fyne.NewSize(size.Height*aspect, size.Height)
}

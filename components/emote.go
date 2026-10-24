package components

import (
	"github.com/Hashy-Software/hasherino-go/hasherino"
)

func NewEmote(emote *hasherino.Emote, clickCallback func(string) error) (EmoteWidget, error) {
	if emote.Animated {
		return NewEmoteGif(emote, clickCallback, true)
	}
	return NewWebpWidget(emote, clickCallback)
}

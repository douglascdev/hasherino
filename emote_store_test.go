package main

import (
	"testing"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

func TestChannelEmoteSignatureIncludesContent(t *testing.T) {
	emote := &hasherino.Emote{Id: "id", Source: hasherino.SevenTV, Name: "Name", Animated: true, Width: 32, Height: 32, Version: "v1"}
	signature := channelEmoteSignature([]*hasherino.Emote{emote})

	for _, changed := range []*hasherino.Emote{
		{Id: "id", Source: hasherino.SevenTV, Name: "Renamed", Animated: true, Width: 32, Height: 32, Version: "v1"},
		{Id: "id", Source: hasherino.SevenTV, Name: "Name", Animated: false, Width: 32, Height: 32, Version: "v1"},
		{Id: "id", Source: hasherino.SevenTV, Name: "Name", Animated: true, Width: 64, Height: 32, Version: "v1"},
		{Id: "id", Source: hasherino.SevenTV, Name: "Name", Animated: true, Width: 32, Height: 32, Version: "v2"},
	} {
		if got := channelEmoteSignature([]*hasherino.Emote{changed}); got == signature {
			t.Errorf("expected the signature to change for %+v", changed)
		}
	}
}

func TestForgetChatEmoteDiscardsInFlightLoad(t *testing.T) {
	emote := &hasherino.Emote{Id: "in-flight", Source: hasherino.SevenTV, Name: "in-flight"}
	t.Cleanup(func() { forgetChatEmote(emote) })

	_, generation, load := requestChatEmoteMedia(emote)
	if !load {
		t.Fatal("expected the first request to start a load")
	}

	forgetChatEmote(emote)
	finishChatEmoteMedia(emote, generation, &chatMedia{})

	if media := cachedChatEmoteMedia(emote); media != nil {
		t.Error("expected the load that started before the invalidation to be dropped")
	}
	if _, next, load := requestChatEmoteMedia(emote); !load {
		t.Error("expected the emote to be loadable again")
	} else {
		finishChatEmoteMedia(emote, next, nil)
	}
}

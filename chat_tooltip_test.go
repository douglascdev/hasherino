package main

import (
	"testing"

	"gioui.org/io/pointer"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

func TestEmoteTooltipUsesMaximumResolution(t *testing.T) {
	tests := []struct {
		source      hasherino.EmoteSourceEnum
		resolution  string
		sourceLabel string
	}{
		{source: hasherino.SevenTV, resolution: "4x", sourceLabel: "7TV"},
		{source: hasherino.Twitch, resolution: "3.0", sourceLabel: "Twitch"},
	}

	for _, test := range tests {
		t.Run(test.sourceLabel, func(t *testing.T) {
			hover := &emoteHover{}
			emote := &hasherino.Emote{Id: "id", Name: "Emote", Source: test.source}
			emoteTooltip.show(emote, hover, pointer.Mouse)
			defer emoteTooltip.hide()

			if !emoteTooltip.visible {
				t.Fatal("expected the tooltip to be visible")
			}
			if emoteTooltip.emote == nil {
				t.Fatal("expected the tooltip to hold an emote")
			}
			if got := emoteTooltip.emote.Resolution; got != test.resolution {
				t.Errorf("unexpected resolution: got %q, want %q", got, test.resolution)
			}
			if got := emoteSourceLabel(emoteTooltip.emote.Source); got != test.sourceLabel {
				t.Errorf("unexpected source label: got %q, want %q", got, test.sourceLabel)
			}
		})
	}
}

func TestEmoteHoverTagsAreDistinct(t *testing.T) {
	first, second := &emoteHover{}, &emoteHover{}
	if first == second {
		t.Fatal("expected every rendered emote to have its own hover tag")
	}
}

func TestEmoteTooltipSwitchesBetweenHoverTags(t *testing.T) {
	defer emoteTooltip.hide()
	first := &emoteHover{}
	second := &emoteHover{}
	emoteTooltip.show(&hasherino.Emote{Id: "one", Name: "One", Source: hasherino.SevenTV}, first, pointer.Mouse)
	emoteTooltip.show(&hasherino.Emote{Id: "two", Name: "Two", Source: hasherino.SevenTV}, second, pointer.Mouse)

	if !emoteTooltip.visible || emoteTooltip.emote == nil {
		t.Fatal("expected the tooltip to be visible")
	}
	if got := emoteTooltip.emote.Name; got != "Two" {
		t.Errorf("expected the second emote, got %q", got)
	}
	if emoteTooltip.emote.Id != "two" {
		t.Errorf("expected the second emote image, got id %q", emoteTooltip.emote.Id)
	}
}

func TestEmoteTooltipHidesWhenHoverStops(t *testing.T) {
	defer emoteTooltip.hide()
	hover := &emoteHover{}
	emoteTooltip.show(&hasherino.Emote{Id: "id", Name: "Emote", Source: hasherino.SevenTV}, hover, pointer.Mouse)

	emoteTooltip.hideIf(&emoteHover{})
	if !emoteTooltip.visible {
		t.Fatal("expected another hover tag not to hide the tooltip")
	}
	emoteTooltip.hideIf(hover)
	if emoteTooltip.visible {
		t.Error("expected leaving the emote to hide the tooltip")
	}
}

func TestEmoteTooltipTouchStaysUntilDismissed(t *testing.T) {
	defer emoteTooltip.hide()
	hover := &emoteHover{}
	emoteTooltip.show(&hasherino.Emote{Id: "id", Name: "Emote", Source: hasherino.Twitch}, hover, pointer.Touch)

	// a touch lift can produce a leave event without a mouse hover
	emoteTooltip.hideIf(hover)
	if !emoteTooltip.visible {
		t.Error("expected a touch-opened tooltip to stay after the finger lifts")
	}
	emoteTooltip.hide()
	if emoteTooltip.visible || emoteTooltip.emote != nil {
		t.Error("expected a dismiss to reset the tooltip")
	}
}

func TestChatEmoteKeySeparatesResolutions(t *testing.T) {
	emote := &hasherino.Emote{Id: "id", Source: hasherino.SevenTV}
	preview := *emote
	preview.Resolution = "4x"
	if chatEmoteKey(emote) == chatEmoteKey(&preview) {
		t.Error("expected the preview resolution to use its own media cache entry")
	}
}

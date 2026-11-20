package main

import (
	"strings"
	"testing"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

func TestAllEmojisAreLoadedAndSorted(t *testing.T) {
	emojis := allEmojis()
	if len(emojis) < 1000 {
		t.Fatalf("expected the bundled emoji list, got %d entries", len(emojis))
	}
	for i := 1; i < len(emojis); i++ {
		if strings.ToLower(emojis[i-1].Name) > strings.ToLower(emojis[i].Name) {
			t.Fatalf("emoji list is not sorted at %d", i)
		}
	}
}

func TestEmotePickerSectionsSplitGlobals(t *testing.T) {
	state := newEmotePickerState()
	state.login = "channel"
	state.data["channel"] = &hasherino.EmotePickerData{
		Subs: []hasherino.EmotePickerSection{
			{Title: "one", Emotes: []*hasherino.Emote{{Name: "Pog"}}},
			{Title: "two", Emotes: []*hasherino.Emote{{Name: "Kappa"}}},
		},
		GlobalTwitch:  []*hasherino.Emote{{Name: "Kappa"}},
		GlobalSevenTV: []*hasherino.Emote{{Name: "Pog"}},
	}

	state.tab = pickerTabGlobal
	sections := state.sections("")
	if len(sections) != 2 || sections[0].title != "Twitch" || sections[1].title != "7TV" {
		t.Fatalf("unexpected global sections: %+v", sections)
	}

	sections = state.sections("pog")
	if len(sections) != 1 || sections[0].title != "7TV" || len(sections[0].emotes) != 1 {
		t.Fatalf("expected the empty Twitch section to be dropped: %+v", sections)
	}

	state.tab = pickerTabSubs
	sections = state.sections("")
	if len(sections) != 2 || sections[0].title != "one" || sections[1].title != "two" {
		t.Fatalf("unexpected subscription sections: %+v", sections)
	}
}

func TestEmotePickerFiltersEmojis(t *testing.T) {
	state := newEmotePickerState()
	state.tab = pickerTabEmojis
	sections := state.sections("grinning")
	if len(sections) != 1 || len(sections[0].emojis) == 0 {
		t.Fatalf("expected emoji matches, got %+v", sections)
	}
}

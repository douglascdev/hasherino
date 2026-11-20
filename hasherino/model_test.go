package hasherino

import "testing"

func TestEmoteGetUrlSelectsResolution(t *testing.T) {
	tests := []struct {
		name  string
		emote Emote
		want  string
	}{
		{
			name:  "twitch default",
			emote: Emote{Id: "25", Source: Twitch},
			want:  "https://static-cdn.jtvnw.net/emoticons/v2/25/default/dark/2.0",
		},
		{
			name:  "twitch maximum",
			emote: Emote{Id: "25", Source: Twitch, Resolution: "3.0"},
			want:  "https://static-cdn.jtvnw.net/emoticons/v2/25/default/dark/3.0",
		},
		{
			name:  "7tv default still",
			emote: Emote{Id: "abc", Source: SevenTV},
			want:  "https://cdn.7tv.app/emote/abc/2x.webp",
		},
		{
			name:  "7tv maximum still",
			emote: Emote{Id: "abc", Source: SevenTV, Resolution: "4x"},
			want:  "https://cdn.7tv.app/emote/abc/4x.webp",
		},
		{
			name:  "7tv maximum animated",
			emote: Emote{Id: "abc", Source: SevenTV, Resolution: "4x", Animated: true},
			want:  "https://cdn.7tv.app/emote/abc/4x.gif",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.emote.GetUrl()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("unexpected url: got %q, want %q", got, test.want)
			}
		})
	}

	if _, err := (&Emote{Id: "x", Source: 99}).GetUrl(); err == nil {
		t.Error("expected an unknown source to fail")
	}
}

package hasherino

import (
	"testing"
	"time"
)

func TestParseMessageExtractsTheAuthorColor(t *testing.T) {
	tests := []struct {
		name    string
		message string
		color   string
	}{
		{
			name:    "color tag",
			message: `@badge-info=;badges=;color=#FF4500;display-name=User;emotes=;tmi-sent-ts=1700000000000;user-id=1 :user!user@user.tmi.twitch.tv PRIVMSG #channel :hello`,
			color:   "#FF4500",
		},
		{
			name:    "empty color tag",
			message: `@badge-info=;badges=;color=;display-name=User;tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :hello`,
			color:   "",
		},
		{
			name:    "missing color tag",
			message: `@tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :hello`,
			color:   "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			msg, err := ParseMessage(test.message)
			if err != nil {
				t.Fatal(err)
			}
			if msg.AuthorColor != test.color {
				t.Errorf("expected author color %q, got %q", test.color, msg.AuthorColor)
			}
			if msg.Author != "user" {
				t.Errorf("expected author user, got %q", msg.Author)
			}
			if msg.Text != "hello" {
				t.Errorf("expected text hello, got %q", msg.Text)
			}
			if !msg.Time.Equal(time.UnixMilli(1700000000000)) {
				t.Errorf("expected the tmi timestamp, got %v", msg.Time)
			}
		})
	}
}

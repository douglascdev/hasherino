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

func TestParseMessageExtractsMessageId(t *testing.T) {
	message := `@id=abc-123;tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :hello`
	msg, err := ParseMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Id != "abc-123" {
		t.Errorf("expected the message id, got %q", msg.Id)
	}

	msg, err = ParseMessage(`@tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :hello`)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Id != "" {
		t.Errorf("expected no message id, got %q", msg.Id)
	}
}

func TestParseMessageExtractsDisplayName(t *testing.T) {
	msg, err := ParseMessage(`@display-name=Alice;tmi-sent-ts=1700000000000 :alice!alice@alice.tmi.twitch.tv PRIVMSG #channel :hello`)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Author != "alice" {
		t.Errorf("unexpected login: %q", msg.Author)
	}
	if msg.DisplayName != "Alice" {
		t.Errorf("unexpected display name: %q", msg.DisplayName)
	}

	msg, err = ParseMessage(`@tmi-sent-ts=1700000000000 :alice!alice@alice.tmi.twitch.tv PRIVMSG #channel :hello`)
	if err != nil {
		t.Fatal(err)
	}
	if msg.DisplayName != "alice" {
		t.Errorf("expected the login as display name fallback, got %q", msg.DisplayName)
	}
}

func TestParseMessageExtractsReplyParent(t *testing.T) {
	message := `@id=child;reply-parent-msg-id=parent;reply-parent-user-login=alice;reply-parent-display-name=Alice;reply-parent-msg-body=hello\sworld :bob!bob@bob.tmi.twitch.tv PRIVMSG #channel :reply`
	msg, err := ParseMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if msg.ReplyParentID != "parent" {
		t.Errorf("unexpected reply id: %q", msg.ReplyParentID)
	}
	if msg.ReplyParentLogin != "alice" {
		t.Errorf("unexpected reply login: %q", msg.ReplyParentLogin)
	}
	if msg.ReplyParentAuthor != "Alice" {
		t.Errorf("unexpected reply author: %q", msg.ReplyParentAuthor)
	}
	if msg.ReplyParentText != "hello world" {
		t.Errorf("unexpected reply text: %q", msg.ReplyParentText)
	}

	msg, err = ParseMessage(`@tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :hello`)
	if err != nil {
		t.Fatal(err)
	}
	if msg.ReplyParentID != "" || msg.ReplyParentAuthor != "" || msg.ReplyParentText != "" {
		t.Errorf("expected no reply fields, got %+v", msg)
	}
}

func TestParseMessageExtractsTagBadges(t *testing.T) {
	tests := []struct {
		name    string
		message string
		badges  []Badge
	}{
		{
			name:    "missing badges tag",
			message: `@tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :hello`,
			badges:  nil,
		},
		{
			name:    "empty badges tag",
			message: `@badges=;tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :hello`,
			badges:  nil,
		},
		{
			name:    "multiple badges keep display order",
			message: `@badges=broadcaster/1,subscriber/12,premium/1;tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :hello`,
			badges: []Badge{
				{Name: "broadcaster", Version: "1"},
				{Name: "subscriber", Version: "12"},
				{Name: "premium", Version: "1"},
			},
		},
		{
			name:    "malformed entries are skipped",
			message: `@badges=moderator,vip/,/1,partner/1;tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :hello`,
			badges: []Badge{
				{Name: "partner", Version: "1"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			msg, err := ParseMessage(test.message)
			if err != nil {
				t.Fatal(err)
			}
			if len(msg.Badges) != len(test.badges) {
				t.Fatalf("expected %d badges, got %d: %v", len(test.badges), len(msg.Badges), msg.Badges)
			}
			for i, want := range test.badges {
				if msg.Badges[i] != want {
					t.Errorf("badge %d: expected %+v, got %+v", i, want, msg.Badges[i])
				}
			}
		})
	}
}

func TestParseMessageExtractsTagEmotes(t *testing.T) {
	tests := []struct {
		name    string
		message string
		text    string
		emotes  map[string]*Emote
	}{
		{
			name:    "missing emotes tag",
			message: `@tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :Kappa`,
			text:    "Kappa",
			emotes:  nil,
		},
		{
			name:    "empty emotes tag",
			message: `@emotes=;tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :Kappa`,
			text:    "Kappa",
			emotes:  nil,
		},
		{
			name:    "valueless emotes tag",
			message: `@emotes;tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :Kappa`,
			text:    "Kappa",
			emotes:  nil,
		},
		{
			name:    "multiple emotes and occurrences",
			message: `@emotes=25:0-4,12-16/1902:6-10;tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :Kappa Keepo Kappa`,
			text:    "Kappa Keepo Kappa",
			emotes: map[string]*Emote{
				"Kappa": {Id: "25", Source: Twitch, Name: "Kappa"},
				"Keepo": {Id: "1902", Source: Twitch, Name: "Keepo"},
			},
		},
		{
			name:    "positions count utf-16 code units",
			message: `@emotes=25:3-7;tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :😀 Kappa`,
			text:    "😀 Kappa",
			emotes: map[string]*Emote{
				"Kappa": {Id: "25", Source: Twitch, Name: "Kappa"},
			},
		},
		{
			name:    "out of bounds positions are skipped",
			message: `@emotes=25:0-99/1902:-6--4;tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :Kappa Keepo`,
			text:    "Kappa Keepo",
			emotes:  nil,
		},
		{
			name:    "malformed entries are skipped",
			message: `@emotes=25:4;tmi-sent-ts=1700000000000 :user!user@user.tmi.twitch.tv PRIVMSG #channel :Kappa`,
			text:    "Kappa",
			emotes:  nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			msg, err := ParseMessage(test.message)
			if err != nil {
				t.Fatal(err)
			}
			if msg.Text != test.text {
				t.Errorf("expected text %q, got %q", test.text, msg.Text)
			}
			if len(msg.Emotes) != len(test.emotes) {
				t.Fatalf("expected %d emotes, got %d: %v", len(test.emotes), len(msg.Emotes), msg.Emotes)
			}
			for name, want := range test.emotes {
				got, ok := msg.Emotes[name]
				if !ok {
					t.Errorf("expected the emote %q, got none", name)
					continue
				}
				if *got != *want {
					t.Errorf("expected the emote %+v, got %+v", *want, *got)
				}
			}
		})
	}
}

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

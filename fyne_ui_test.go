package main

import (
	"errors"
	"testing"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

func TestChannelEmotesRefreshHonorsLifetime(t *testing.T) {
	calls := 0
	cache := newChannelEmotes("channel", func(channel string) ([]*hasherino.Emote, error) {
		calls++
		if channel != "channel" {
			t.Errorf("expected emotes for channel, got %s", channel)
		}
		return []*hasherino.Emote{{Name: "FeelsDankMan"}, {Name: "PogChamp"}}, nil
	})

	reloaded, err := cache.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded || calls != 1 {
		t.Errorf("expected a reload on the first refresh, got %v after %d calls", reloaded, calls)
	}
	if len(cache.ByName()) != 2 {
		t.Errorf("expected 2 emotes, got %d", len(cache.ByName()))
	}
	if _, ok := cache.ByName()["FeelsDankMan"]; !ok {
		t.Error("expected the emotes to be mapped by name")
	}

	reloaded, err = cache.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded || calls != 1 {
		t.Errorf("expected no reload while the cache is fresh, got %v after %d calls", reloaded, calls)
	}

	reloaded, err = cache.ForceRefresh()
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded || calls != 2 {
		t.Errorf("expected a reload on a forced refresh, got %v after %d calls", reloaded, calls)
	}
}

func TestChannelEmotesRefreshWaitsAfterFailure(t *testing.T) {
	calls := 0
	cache := newChannelEmotes("channel", func(string) ([]*hasherino.Emote, error) {
		calls++
		return nil, errors.New("database is busy")
	})

	if _, err := cache.Refresh(); err == nil {
		t.Error("expected the refresh to fail")
	}
	reloaded, err := cache.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded || calls != 1 {
		t.Errorf("expected no retry while the cache is fresh, got %v after %d calls", reloaded, calls)
	}

	reloaded, err = cache.ForceRefresh()
	if err == nil || reloaded || calls != 2 {
		t.Errorf("expected a retry on a forced refresh, got %v and %v after %d calls", reloaded, err, calls)
	}
	if len(cache.ByName()) != 0 {
		t.Errorf("expected no emotes, got %d", len(cache.ByName()))
	}
}

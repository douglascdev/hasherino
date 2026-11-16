package hasherino

import (
	"slices"
	"testing"
)

func TestReplaceEmotesKeepsAliasesOfTheSameEmote(t *testing.T) {
	hc := newSearchEmoteController(t)
	channel := "63073510"
	scope := emoteScope{source: SevenTV, channelID: &channel}
	emotes := []Emote{
		{Id: "shared", Source: SevenTV, Name: "alias", ChannelID: &channel, Version: "1"},
		{Id: "shared", Source: SevenTV, Name: "other_alias", ChannelID: &channel, Version: "1"},
		// a malformed response can repeat an entry verbatim
		{Id: "shared", Source: SevenTV, Name: "other_alias", ChannelID: &channel, Version: "1"},
	}

	if err := hc.replaceEmotes(scope, emotes); err != nil {
		t.Fatalf("replaceEmotes failed for aliased emotes: %v", err)
	}
	assertEmoteCount(t, hc, 2)
	assertCachedEmoteCount(t, hc, 2)

	emotesInDB := []*Emote{}
	if err := hc.memDB.Order("name").Find(&emotesInDB).Error; err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, emote := range emotesInDB {
		names = append(names, emote.Name)
	}
	if got, want := names, []string{"alias", "other_alias"}; !slices.Equal(got, want) {
		t.Errorf("unexpected aliased emotes: got %v, want %v", got, want)
	}

	// refreshing the same list must stay idempotent
	if err := hc.replaceEmotes(scope, emotes); err != nil {
		t.Fatalf("replaceEmotes failed on refresh: %v", err)
	}
	assertEmoteCount(t, hc, 2)
	assertCachedEmoteCount(t, hc, 2)
}

func TestReplaceEmotesIndexesAndPrunes(t *testing.T) {
	hc := newSearchEmoteController(t)
	notified := []string{}
	hc.SetEmoteRefreshHook(func(emotes []*Emote) {
		for _, emote := range emotes {
			notified = append(notified, emote.Id)
		}
	})

	scope := emoteScope{source: SevenTV}
	first := []Emote{
		{Id: "a", Source: SevenTV, Name: "A", Version: "1"},
		{Id: "b", Source: SevenTV, Name: "B", Version: "1"},
	}
	if err := hc.replaceEmotes(scope, first); err != nil {
		t.Fatalf("replaceEmotes failed: %v", err)
	}
	assertEmoteCount(t, hc, 2)
	assertCachedEmoteCount(t, hc, 2)
	if len(notified) != 0 {
		t.Errorf("expected new emotes not to be notified, got %v", notified)
	}

	notified = nil
	second := []Emote{{Id: "a", Source: SevenTV, Name: "Renamed", Version: "2"}}
	if err := hc.replaceEmotes(scope, second); err != nil {
		t.Fatalf("replaceEmotes failed: %v", err)
	}
	assertEmoteCount(t, hc, 1)
	assertCachedEmoteCount(t, hc, 1)
	if got, want := notified, []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("expected the changed and removed emotes to be notified: got %v, want %v", got, want)
	}

	emote := Emote{}
	if err := hc.memDB.Take(&emote, "id = ?", "a").Error; err != nil {
		t.Fatal(err)
	}
	if emote.Name != "Renamed" || emote.Version != "2" {
		t.Errorf("expected the refreshed emote to replace the old one, got %+v", emote)
	}
}

func TestReplaceEmotesOnlyTouchesItsScope(t *testing.T) {
	hc := newSearchEmoteController(t)
	channel := "channel"
	global := emoteScope{source: SevenTV}
	scoped := emoteScope{source: SevenTV, channelID: &channel}

	if err := hc.replaceEmotes(global, []Emote{{Id: "global", Source: SevenTV, Name: "Global"}}); err != nil {
		t.Fatal(err)
	}
	if err := hc.replaceEmotes(scoped, []Emote{{Id: "channel", Source: SevenTV, Name: "Channel", ChannelID: &channel}}); err != nil {
		t.Fatal(err)
	}
	if err := hc.replaceEmotes(scoped, nil); err != nil {
		t.Fatal(err)
	}

	assertEmoteCount(t, hc, 1)
	assertCachedEmoteCount(t, hc, 1)
	emote := Emote{}
	if err := hc.memDB.Take(&emote).Error; err != nil {
		t.Fatal(err)
	}
	if emote.Id != "global" {
		t.Errorf("expected the global emote to survive, got %+v", emote)
	}
}

func TestHydrateEmoteCatalogLoadsOnlyActiveAccount(t *testing.T) {
	hc := newSearchEmoteController(t)
	if err := hc.permDB.Create(&Account{Id: "active", Login: "active", Active: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := hc.permDB.Create(&Account{Id: "other", Login: "other", Active: false}).Error; err != nil {
		t.Fatal(err)
	}
	cached := []CachedEmote{
		{Key: "global", Id: "global", Source: SevenTV, Name: "Global", Version: "1"},
		{Key: "active", Id: "active", Source: Twitch, Name: "Active", OwnerID: "active"},
		{Key: "other", Id: "other", Source: Twitch, Name: "Other", OwnerID: "other"},
	}
	if err := hc.permDB.Create(&cached).Error; err != nil {
		t.Fatal(err)
	}

	hc.hydrateEmoteCatalog()
	assertEmoteCount(t, hc, 2)

	// hydrating again, for example after switching back to the account, must
	// not duplicate the emotes
	hc.hydrateEmoteCatalog()
	assertEmoteCount(t, hc, 2)

	names := []string{}
	emotes := []*Emote{}
	if err := hc.memDB.Order("name").Find(&emotes).Error; err != nil {
		t.Fatal(err)
	}
	for _, emote := range emotes {
		names = append(names, emote.Name)
	}
	if got, want := names, []string{"Active", "Global"}; !slices.Equal(got, want) {
		t.Errorf("unexpected hydrated emotes: got %v, want %v", got, want)
	}
}

func TestReplaceEmotesTwitchScopeKeepsAccountsSeparate(t *testing.T) {
	hc := newSearchEmoteController(t)
	first := emoteScope{source: Twitch, ownerID: "first"}
	second := emoteScope{source: Twitch, ownerID: "second"}

	if err := hc.replaceEmotes(first, []Emote{{Id: "1", Source: Twitch, Name: "First", OwnerID: "first"}}); err != nil {
		t.Fatal(err)
	}
	if err := hc.replaceEmotes(second, []Emote{{Id: "2", Source: Twitch, Name: "Second", OwnerID: "second"}}); err != nil {
		t.Fatal(err)
	}
	if err := hc.replaceEmotes(first, nil); err != nil {
		t.Fatal(err)
	}

	assertEmoteCount(t, hc, 1)
	assertCachedEmoteCount(t, hc, 1)
	emote := Emote{}
	if err := hc.memDB.Take(&emote).Error; err != nil {
		t.Fatal(err)
	}
	if emote.OwnerID != "second" {
		t.Errorf("expected the other account's emotes to survive, got %+v", emote)
	}
}

func assertEmoteCount(t *testing.T, hc *HasherinoController, want int64) {
	t.Helper()
	var count int64
	if err := hc.memDB.Model(&Emote{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Errorf("unexpected in-memory emote count: got %d, want %d", count, want)
	}
}

func assertCachedEmoteCount(t *testing.T, hc *HasherinoController, want int64) {
	t.Helper()
	var count int64
	if err := hc.permDB.Model(&CachedEmote{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Errorf("unexpected cached emote count: got %d, want %d", count, want)
	}
}

package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/Hashy-Software/hasherino-go/components"
	"github.com/Hashy-Software/hasherino-go/hasherino"
)

func testEmotes(names ...string) []*hasherino.Emote {
	emotes := make([]*hasherino.Emote, 0, len(names))
	for _, name := range names {
		emotes = append(emotes, &hasherino.Emote{Name: name})
	}
	return emotes
}

func withoutEmotePreviews(autocomplete *emoteAutocomplete) {
	autocomplete.previewFactory = func(*hasherino.Emote, func(string) error) (components.EmoteWidget, error) {
		return nil, nil
	}
}

type stubEmotePreview struct {
	widget.BaseWidget
	min      fyne.Size
	started  chan struct{}
	allow    chan struct{}
	unloaded atomic.Bool
}

func newStubEmotePreview() *stubEmotePreview {
	preview := &stubEmotePreview{
		started: make(chan struct{}),
		allow:   make(chan struct{}),
	}
	preview.ExtendBaseWidget(preview)
	return preview
}

func (p *stubEmotePreview) SetMinSize(size fyne.Size) {
	p.min = size
}

func (p *stubEmotePreview) MinSize() fyne.Size {
	return p.min
}

func (p *stubEmotePreview) LazyLoad() error {
	close(p.started)
	<-p.allow
	return nil
}

func (p *stubEmotePreview) LazyUnload() error {
	p.unloaded.Store(true)
	return nil
}

func (p *stubEmotePreview) CreateRenderer() fyne.WidgetRenderer {
	return stubEmotePreviewRenderer{}
}

type stubEmotePreviewRenderer struct{}

func (stubEmotePreviewRenderer) Layout(fyne.Size)             {}
func (stubEmotePreviewRenderer) MinSize() fyne.Size           { return fyne.NewSize(1, 1) }
func (stubEmotePreviewRenderer) Refresh()                     {}
func (stubEmotePreviewRenderer) Objects() []fyne.CanvasObject { return nil }
func (stubEmotePreviewRenderer) Destroy()                     {}

func TestNewChatTabsSelectsFirstAppendedTab(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	chatTabs := newChatTabs()
	selected := ""
	chatTabs.OnSelected = func(tab *container.TabItem) {
		selected = tab.Text
	}

	chatTabs.Append(container.NewTabItem("first", widget.NewLabel("")))

	if selected != "first" {
		t.Fatalf("expected first tab selection callback, got %q", selected)
	}
}

func TestAutocompleteEntryInterceptsKeysWhileFocused(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	entry := newAutocompleteEntry()
	entry.SetText("hello")
	entry.CursorColumn = len("hello")
	var handled []fyne.KeyName
	entry.onTypedKey = func(key *fyne.KeyEvent) bool {
		handled = append(handled, key.Name)
		return key.Name == fyne.KeyDown
	}

	window := test.NewWindow(entry)
	defer window.Close()
	window.Canvas().Focus(entry)
	if window.Canvas().Focused() != entry {
		t.Fatal("expected the autocomplete entry to hold focus")
	}

	entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyDown})
	entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyLeft})

	if len(handled) != 2 || handled[0] != fyne.KeyDown || handled[1] != fyne.KeyLeft {
		t.Fatalf("expected both keys to reach the handler, got %v", handled)
	}
	if entry.CursorColumn != len("hello")-1 {
		t.Fatalf("expected unhandled keys to be delegated, got cursor %d", entry.CursorColumn)
	}
}

func TestInlineEmoteMenuKeepsEntryFocused(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	entry := newAutocompleteEntry()
	menu := container.NewVBox(widget.NewButton("PogChamp", func() {}))
	menu.Hide()
	content := container.NewBorder(nil, container.NewVBox(menu, entry), nil, nil, widget.NewLabel("chat"))
	window := test.NewWindow(content)
	defer window.Close()

	window.Canvas().Focus(entry)
	menu.Show()
	content.Refresh()

	if window.Canvas().Focused() != entry {
		t.Fatal("expected the entry to keep focus when the inline emote menu opens")
	}
}

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

func TestReplaceEmoteTokenPreservesSurroundingText(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		cursor     int
		emote      string
		want       string
		wantCursor int
		ok         bool
	}{
		{
			name:       "at the end adds trailing space",
			text:       "hello :pog",
			cursor:     10,
			emote:      "PogChamp",
			want:       "hello PogChamp ",
			wantCursor: 15,
			ok:         true,
		},
		{
			name:       "between words preserves whitespace",
			text:       "hello   :pog   world",
			cursor:     12,
			emote:      "PogChamp",
			want:       "hello   PogChamp   world",
			wantCursor: 16,
			ok:         true,
		},
		{
			name:       "cursor inside token replaces whole token",
			text:       ":pog world",
			cursor:     3,
			emote:      "PogChamp",
			want:       "PogChamp world",
			wantCursor: 8,
			ok:         true,
		},
		{
			name:       "unicode before token keeps rune cursor",
			text:       "😀 :pog",
			cursor:     6,
			emote:      "PogChamp",
			want:       "😀 PogChamp ",
			wantCursor: 11,
			ok:         true,
		},
		{
			name:   "query too short",
			text:   "hello :po",
			cursor: 9,
			emote:  "PogChamp",
			want:   "hello :po",
			ok:     false,
		},
		{
			name:   "not a colon token",
			text:   "hello there",
			cursor: 11,
			emote:  "PogChamp",
			want:   "hello there",
			ok:     false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, cursor, ok := replaceEmoteToken(test.text, test.cursor, test.emote)
			if ok != test.ok {
				t.Fatalf("expected ok=%v, got %v", test.ok, ok)
			}
			if got != test.want {
				t.Errorf("expected %q, got %q", test.want, got)
			}
			if ok && cursor != test.wantCursor {
				t.Errorf("expected cursor %d, got %d", test.wantCursor, cursor)
			}
		})
	}
}

func TestTabTokenAtFindsTheQueryOfTheWord(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		cursor     int
		tokenStart int
		tokenEnd   int
		query      string
		ok         bool
	}{
		{
			name:       "word at the cursor",
			text:       "hello pog",
			cursor:     9,
			tokenStart: 6,
			tokenEnd:   9,
			query:      "pog",
			ok:         true,
		},
		{
			name:       "cursor inside the word",
			text:       "pog world",
			cursor:     2,
			tokenStart: 0,
			tokenEnd:   3,
			query:      "pog",
			ok:         true,
		},
		{
			name:       "cursor before the space after a word",
			text:       "hello pog",
			cursor:     5,
			tokenStart: 0,
			tokenEnd:   5,
			query:      "hello",
			ok:         true,
		},
		{
			name:       "emote search colon token",
			text:       "hello :pog",
			cursor:     10,
			tokenStart: 6,
			tokenEnd:   10,
			query:      "pog",
			ok:         true,
		},
		{
			name:   "bare colon has no query",
			text:   "hello :",
			cursor: 7,
			ok:     false,
		},
		{
			name:   "cursor between two spaces",
			text:   "hello  pog",
			cursor: 6,
			ok:     false,
		},
		{
			name:   "cursor after a trailing space",
			text:   "hello ",
			cursor: 6,
			ok:     false,
		},
		{
			name:   "empty text",
			text:   "",
			cursor: 0,
			ok:     false,
		},
		{
			name:       "unicode counts runes",
			text:       "😀 pog",
			cursor:     3,
			tokenStart: 2,
			tokenEnd:   5,
			query:      "pog",
			ok:         true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tokenStart, tokenEnd, query, ok := tabTokenAt(test.text, test.cursor)
			if ok != test.ok {
				t.Fatalf("expected ok=%v, got %v", test.ok, ok)
			}
			if !ok {
				return
			}
			if tokenStart != test.tokenStart || tokenEnd != test.tokenEnd {
				t.Errorf("expected the token %d:%d, got %d:%d", test.tokenStart, test.tokenEnd, tokenStart, tokenEnd)
			}
			if query != test.query {
				t.Errorf("expected the query %q, got %q", test.query, query)
			}
		})
	}
}

func TestEmoteAutocompleteRefreshesOnlyWhenVisibilityChanges(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	entry := newAutocompleteEntry()
	refreshes := 0
	autocomplete := newEmoteAutocomplete(entry, nil, func() {
		refreshes++
	})
	withoutEmotePreviews(autocomplete)

	autocomplete.hide()
	if refreshes != 0 {
		t.Fatalf("expected no refresh while hidden, got %d", refreshes)
	}

	autocomplete.showSuggestions(testEmotes("Pog", "PogChamp"))
	if refreshes != 1 {
		t.Fatalf("expected one refresh when showing, got %d", refreshes)
	}

	autocomplete.showSuggestions(testEmotes("Pog", "PogChamp"))
	if refreshes != 1 {
		t.Fatalf("expected no refresh for unchanged options, got %d", refreshes)
	}

	autocomplete.showSuggestions(testEmotes("Pog", "pogU"))
	if refreshes != 1 {
		t.Fatalf("expected no parent refresh while already visible, got %d", refreshes)
	}

	autocomplete.hide()
	if refreshes != 2 {
		t.Fatalf("expected one refresh when hiding, got %d", refreshes)
	}

	autocomplete.hide()
	if refreshes != 2 {
		t.Fatalf("expected no refresh while already hidden, got %d", refreshes)
	}
}

func TestEmoteAutocompleteNavigatesAndCompletes(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	entry := newAutocompleteEntry()
	entry.SetText("hello :pog")
	entry.CursorColumn = len("hello :pog")
	autocomplete := newEmoteAutocomplete(entry, nil, nil)
	withoutEmotePreviews(autocomplete)
	autocomplete.showSuggestions(testEmotes("Pog", "PogChamp", "pogU"))

	if autocomplete.selected != 0 {
		t.Fatalf("expected the first suggestion to be selected, got %d", autocomplete.selected)
	}
	if !autocomplete.handleKey(&fyne.KeyEvent{Name: fyne.KeyUp}) || autocomplete.selected != 2 {
		t.Fatalf("expected Up to wrap to the last suggestion, got %d", autocomplete.selected)
	}
	if !autocomplete.handleKey(&fyne.KeyEvent{Name: fyne.KeyDown}) || autocomplete.selected != 0 {
		t.Fatalf("expected Down to wrap to the first suggestion, got %d", autocomplete.selected)
	}
	autocomplete.setSelection(1)
	if !autocomplete.handleKey(&fyne.KeyEvent{Name: fyne.KeyEnter}) {
		t.Fatal("expected Enter to be handled")
	}

	if entry.Text != "hello PogChamp " {
		t.Fatalf("expected the selected emote to be inserted, got %q", entry.Text)
	}
	if entry.CursorColumn != len("hello PogChamp ") {
		t.Fatalf("expected the cursor after the inserted emote, got %d", entry.CursorColumn)
	}
	if autocomplete.Menu().Visible() {
		t.Fatal("expected the suggestion menu to close after completion")
	}
}

func TestTabCompletionReplacesTokenWithBestMatch(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	tests := []struct {
		name       string
		text       string
		cursor     int
		emotes     []*hasherino.Emote
		searchErr  error
		want       string
		wantCursor int
		handled    bool
	}{
		{
			name:       "completes the word at the cursor",
			text:       "hello pog",
			cursor:     9,
			emotes:     []*hasherino.Emote{{Name: "PogChamp"}},
			want:       "hello PogChamp ",
			wantCursor: 15,
			handled:    true,
		},
		{
			name:       "completes a word followed by more text",
			text:       "pog world",
			cursor:     3,
			emotes:     []*hasherino.Emote{{Name: "PogChamp"}},
			want:       "PogChamp world",
			wantCursor: 8,
			handled:    true,
		},
		{
			name:       "completes the word of the emote search colon",
			text:       ":pog",
			cursor:     4,
			emotes:     []*hasherino.Emote{{Name: "PogChamp"}},
			want:       "PogChamp ",
			wantCursor: 9,
			handled:    true,
		},
		{
			name:       "no matching emote leaves the text alone",
			text:       "hello pog",
			cursor:     9,
			emotes:     []*hasherino.Emote{},
			want:       "hello pog",
			wantCursor: 9,
			handled:    true,
		},
		{
			name:       "failed search leaves the text alone",
			text:       "hello pog",
			cursor:     9,
			searchErr:  errors.New("database is busy"),
			want:       "hello pog",
			wantCursor: 9,
			handled:    true,
		},
		{
			name:       "no word at the cursor hands Tab to the entry",
			text:       "hello ",
			cursor:     6,
			emotes:     []*hasherino.Emote{{Name: "PogChamp"}},
			want:       "hello ",
			wantCursor: 6,
			handled:    false,
		},
		{
			name:       "empty entry hands Tab to the entry",
			text:       "",
			cursor:     0,
			emotes:     []*hasherino.Emote{{Name: "PogChamp"}},
			want:       "",
			wantCursor: 0,
			handled:    false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := newAutocompleteEntry()
			entry.SetText(test.text)
			entry.CursorColumn = test.cursor
			autocomplete := newEmoteAutocomplete(entry, func(query string, limit int) ([]*hasherino.Emote, error) {
				if query != "pog" {
					t.Errorf("expected the query pog, got %q", query)
				}
				if limit != 1 {
					t.Errorf("expected only the best match to be searched, got limit %d", limit)
				}
				return test.emotes, test.searchErr
			}, nil)
			withoutEmotePreviews(autocomplete)

			if handled := autocomplete.handleKey(&fyne.KeyEvent{Name: fyne.KeyTab}); handled != test.handled {
				t.Fatalf("expected handled %v, got %v", test.handled, handled)
			}
			if entry.Text != test.want {
				t.Errorf("expected %q, got %q", test.want, entry.Text)
			}
			if entry.CursorColumn != test.wantCursor {
				t.Errorf("expected cursor %d, got %d", test.wantCursor, entry.CursorColumn)
			}
		})
	}
}

func TestTabKeyAcceptsTheSelectedSuggestion(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	entry := newAutocompleteEntry()
	entry.SetText(":pog")
	entry.CursorColumn = len(":pog")
	autocomplete := newEmoteAutocomplete(entry, nil, nil)
	withoutEmotePreviews(autocomplete)
	autocomplete.showSuggestions(testEmotes("Pog", "PogChamp", "pogU"))
	autocomplete.setSelection(1)

	if !autocomplete.handleKey(&fyne.KeyEvent{Name: fyne.KeyTab}) {
		t.Fatal("expected Tab to be handled while the menu shows suggestions")
	}
	if entry.Text != "PogChamp " {
		t.Fatalf("expected the selected suggestion to be accepted, got %q", entry.Text)
	}
	if autocomplete.Menu().Visible() {
		t.Fatal("expected the suggestion menu to close after the completion")
	}
}

func TestAutocompleteEntryAcceptsTabOnlyWhileCompletable(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	entry := newAutocompleteEntry()
	autocomplete := newEmoteAutocomplete(entry, nil, nil)
	withoutEmotePreviews(autocomplete)

	if entry.AcceptsTab() {
		t.Fatal("expected an empty entry to leave Tab to the focus navigation")
	}

	entry.SetText("hello pog")
	entry.CursorColumn = len("hello pog")
	if !entry.AcceptsTab() {
		t.Fatal("expected the entry to accept Tab while a word can be completed")
	}

	autocomplete.showSuggestions(testEmotes("PogChamp"))
	if !entry.AcceptsTab() {
		t.Fatal("expected the entry to accept Tab while the menu shows suggestions")
	}

	autocomplete.hide()
	entry.SetText("")
	if entry.AcceptsTab() {
		t.Fatal("expected the entry to leave Tab to the focus navigation again")
	}
}

func TestAutocompleteEntryConsumesTabWhileCompletable(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	entry := newAutocompleteEntry()
	entry.SetText("hello pog")
	entry.CursorColumn = len("hello pog")
	newEmoteAutocomplete(entry, func(query string, limit int) ([]*hasherino.Emote, error) {
		if query != "pog" || limit != 1 {
			t.Errorf("expected the best match for pog, got %q with limit %d", query, limit)
		}
		return []*hasherino.Emote{{Name: "PogChamp"}}, nil
	}, nil)

	entry.TypedKey(&fyne.KeyEvent{Name: fyne.KeyTab})

	if entry.Text != "hello PogChamp " {
		t.Fatalf("expected Tab to complete the emote, got %q", entry.Text)
	}
}

func TestEmoteAutocompleteSearchShowsResults(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	entry := newAutocompleteEntry()
	entry.SetText(":pog")
	entry.CursorColumn = len(":pog")

	done := make(chan struct{})
	autocomplete := newEmoteAutocomplete(entry, func(query string, limit int) ([]*hasherino.Emote, error) {
		if query != "pog" {
			t.Errorf("expected query %q, got %q", "pog", query)
		}
		if limit != emoteSuggestionLimit {
			t.Errorf("expected limit %d, got %d", emoteSuggestionLimit, limit)
		}
		return []*hasherino.Emote{{Name: "PogChamp"}}, nil
	}, func() {
		close(done)
	})
	withoutEmotePreviews(autocomplete)
	autocomplete.searchDelay = 0
	autocomplete.onChanged(entry.Text)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the debounced search")
	}

	if !autocomplete.Menu().Visible() {
		t.Fatal("expected the suggestion menu to be visible")
	}
	if len(autocomplete.suggestions) != 1 || autocomplete.suggestions[0].Name != "PogChamp" {
		t.Fatalf("unexpected suggestions: %v", autocomplete.suggestions)
	}
}

func TestEmoteOptionRowCreatesTappablePreview(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	tapped := false
	row := newEmoteOptionRow(&hasherino.Emote{Name: "PogChamp", Animated: true}, components.NewEmote, func() {
		tapped = true
	})
	defer row.release()

	if row.preview == nil {
		t.Fatal("expected the row to have an emote preview")
	}
	if got, want := row.preview.MinSize(), fyne.NewSize(emoteSuggestionPreviewSize, emoteSuggestionPreviewSize); got != want {
		t.Fatalf("unexpected preview size: got %v, want %v", got, want)
	}
	preview, ok := row.preview.(fyne.Tappable)
	if !ok {
		t.Fatal("expected the preview to be tappable")
	}
	preview.Tapped(&fyne.PointEvent{})
	if !tapped {
		t.Fatal("expected tapping the preview to complete the suggestion")
	}
}

func TestEmoteOptionRowDefersUnloadUntilPreviewLoads(t *testing.T) {
	app := test.NewApp()
	defer app.Quit()

	preview := newStubEmotePreview()
	row := newEmoteOptionRow(&hasherino.Emote{Name: "PogChamp"}, func(*hasherino.Emote, func(string) error) (components.EmoteWidget, error) {
		return preview, nil
	}, nil)
	row.loadPreview()

	select {
	case <-preview.started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the preview to start loading")
	}

	row.release()
	if preview.unloaded.Load() {
		t.Fatal("expected unload to wait for the in-flight load")
	}

	close(preview.allow)
	deadline := time.Now().Add(time.Second)
	for !preview.unloaded.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !preview.unloaded.Load() {
		t.Fatal("expected the preview to unload after the load finished")
	}

	row.release()
}

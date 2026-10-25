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

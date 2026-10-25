package main

import (
	"image/color"
	"log"
	"slices"
	"sync"
	"time"
	"unicode"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/Hashy-Software/hasherino-go/components"
	"github.com/Hashy-Software/hasherino-go/hasherino"
)

const (
	emoteSuggestionLimit       = 10
	emoteSearchDelay           = 200 * time.Millisecond
	minEmoteQueryLength        = 3
	emoteSuggestionPreviewSize = 28
)

type emoteSearchFunc func(string, int) ([]*hasherino.Emote, error)
type emotePreviewFactory func(*hasherino.Emote, func(string) error) (components.EmoteWidget, error)

type autocompleteEntry struct {
	widget.Entry
	onTypedKey func(*fyne.KeyEvent) bool
}

func newAutocompleteEntry() *autocompleteEntry {
	entry := &autocompleteEntry{Entry: widget.Entry{Wrapping: fyne.TextWrap(fyne.TextTruncateClip)}}
	entry.ExtendBaseWidget(entry)
	return entry
}

func (e *autocompleteEntry) TypedKey(key *fyne.KeyEvent) {
	if e.onTypedKey != nil && e.onTypedKey(key) {
		return
	}
	e.Entry.TypedKey(key)
}

type emoteAutocomplete struct {
	entry   *autocompleteEntry
	search  emoteSearchFunc
	refresh func()

	menu           *fyne.Container
	rows           []*emoteOptionRow
	suggestions    []*hasherino.Emote
	selected       int
	previewFactory emotePreviewFactory

	searchDelay time.Duration
	searchTimer *time.Timer
	requestID   uint64
	updating    bool
}

func newEmoteAutocomplete(entry *autocompleteEntry, search emoteSearchFunc, refresh func()) *emoteAutocomplete {
	autocomplete := &emoteAutocomplete{
		entry:          entry,
		search:         search,
		refresh:        refresh,
		menu:           container.NewVBox(),
		previewFactory: components.NewEmote,
		searchDelay:    emoteSearchDelay,
	}
	autocomplete.menu.Hide()
	entry.onTypedKey = autocomplete.handleKey
	entry.OnChanged = autocomplete.onChanged
	return autocomplete
}

func (a *emoteAutocomplete) Menu() *fyne.Container {
	return a.menu
}

func (a *emoteAutocomplete) onChanged(text string) {
	if a.updating {
		return
	}

	a.cancelSearch()
	_, _, query, ok := emoteTokenAt(text, a.entry.CursorColumn)
	if !ok {
		a.hide()
		return
	}

	requestID := a.requestID
	a.searchTimer = time.AfterFunc(a.searchDelay, func() {
		emotes, err := a.search(query, emoteSuggestionLimit)
		fyne.Do(func() {
			if requestID != a.requestID {
				return
			}
			if err != nil {
				log.Printf("Failed to search emotes for %q: %v", query, err)
				a.hide()
				return
			}

			limit := min(len(emotes), emoteSuggestionLimit)
			suggestions := make([]*hasherino.Emote, 0, limit)
			for _, emote := range emotes[:limit] {
				if emote != nil && emote.Name != "" {
					suggestions = append(suggestions, emote)
				}
			}
			if len(suggestions) == 0 {
				a.hide()
				return
			}
			a.showSuggestions(suggestions)
		})
	})
}

func (a *emoteAutocomplete) handleKey(key *fyne.KeyEvent) bool {
	if !a.menu.Visible() || len(a.suggestions) == 0 {
		return false
	}

	switch key.Name {
	case fyne.KeyDown:
		a.setSelection(a.selected + 1)
	case fyne.KeyUp:
		a.setSelection(a.selected - 1)
	case fyne.KeyEnter, fyne.KeyReturn:
		a.accept(a.suggestions[a.selected].Name)
	case fyne.KeyEscape:
		a.hide()
	default:
		return false
	}
	return true
}

func (a *emoteAutocomplete) showSuggestions(suggestions []*hasherino.Emote) {
	if slices.EqualFunc(a.suggestions, suggestions, sameEmoteName) {
		a.showMenu()
		return
	}

	selectedName := ""
	if a.selected >= 0 && a.selected < len(a.suggestions) {
		selectedName = a.suggestions[a.selected].Name
	}

	a.clearRows()
	a.menu.RemoveAll()
	a.suggestions = append([]*hasherino.Emote(nil), suggestions...)
	a.rows = make([]*emoteOptionRow, 0, len(suggestions))
	for _, suggestion := range suggestions {
		suggestion := suggestion
		row := newEmoteOptionRow(suggestion, a.previewFactory, func() {
			a.accept(suggestion.Name)
		})
		a.rows = append(a.rows, row)
		a.menu.Add(row)
	}

	a.selected = slices.IndexFunc(a.suggestions, func(suggestion *hasherino.Emote) bool {
		return suggestion != nil && suggestion.Name == selectedName
	})
	if a.selected < 0 {
		a.selected = 0
	}
	a.refreshSelection()
	a.showMenu()
	for _, row := range a.rows {
		row.loadPreview()
	}
}

func sameEmoteName(a, b *hasherino.Emote) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Name == b.Name
}

func (a *emoteAutocomplete) accept(name string) {
	text, cursor, ok := replaceEmoteToken(a.entry.Text, a.entry.CursorColumn, name)
	if !ok {
		a.hide()
		return
	}

	a.hide()
	a.updating = true
	a.entry.SetText(text)
	a.entry.CursorRow = 0
	a.entry.CursorColumn = cursor
	a.entry.Refresh()
	a.updating = false

	if canvas := fyne.CurrentApp().Driver().CanvasForObject(a.entry); canvas != nil {
		canvas.Focus(a.entry)
	}
}

func (a *emoteAutocomplete) setSelection(index int) {
	if len(a.suggestions) == 0 {
		return
	}
	a.selected = (index%len(a.suggestions) + len(a.suggestions)) % len(a.suggestions)
	a.refreshSelection()
}

func (a *emoteAutocomplete) refreshSelection() {
	for i, row := range a.rows {
		row.setSelected(i == a.selected)
	}
}

func (a *emoteAutocomplete) showMenu() {
	if a.menu.Visible() {
		return
	}
	a.menu.Show()
	a.refreshContent()
}

func (a *emoteAutocomplete) hide() {
	a.cancelSearch()
	wasVisible := a.menu.Visible()
	a.clearRows()
	a.suggestions = nil
	a.selected = 0
	if !wasVisible {
		return
	}
	a.menu.Hide()
	a.menu.RemoveAll()
	a.refreshContent()
}

func (a *emoteAutocomplete) clearRows() {
	for _, row := range a.rows {
		row.release()
	}
	a.rows = nil
}

func (a *emoteAutocomplete) cancelSearch() {
	a.requestID++
	if a.searchTimer != nil {
		a.searchTimer.Stop()
		a.searchTimer = nil
	}
}

func (a *emoteAutocomplete) refreshContent() {
	if a.refresh != nil {
		a.refresh()
	}
}

func emoteTokenAt(text string, cursor int) (tokenStart, tokenEnd int, query string, ok bool) {
	runes := []rune(text)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}

	tokenStart = cursor
	for tokenStart > 0 && !unicode.IsSpace(runes[tokenStart-1]) {
		tokenStart--
	}
	tokenEnd = cursor
	for tokenEnd < len(runes) && !unicode.IsSpace(runes[tokenEnd]) {
		tokenEnd++
	}

	if tokenStart == tokenEnd || runes[tokenStart] != ':' {
		return 0, 0, "", false
	}
	queryRunes := runes[tokenStart+1 : tokenEnd]
	if len(queryRunes) < minEmoteQueryLength {
		return 0, 0, "", false
	}
	return tokenStart, tokenEnd, string(queryRunes), true
}

func replaceEmoteToken(text string, cursor int, name string) (string, int, bool) {
	tokenStart, tokenEnd, _, ok := emoteTokenAt(text, cursor)
	if !ok {
		return text, cursor, false
	}

	runes := []rune(text)
	replacement := []rune(name)
	if tokenEnd == len(runes) {
		replacement = append(replacement, ' ')
	}

	replaced := make([]rune, 0, len(runes)-(tokenEnd-tokenStart)+len(replacement))
	replaced = append(replaced, runes[:tokenStart]...)
	replaced = append(replaced, replacement...)
	replaced = append(replaced, runes[tokenEnd:]...)
	return string(replaced), tokenStart + len(replacement), true
}

type emoteOptionRow struct {
	widget.BaseWidget
	emote    *hasherino.Emote
	selected bool
	onTapped func()
	preview  components.EmoteWidget

	previewMutex   sync.Mutex
	previewLoading bool
	previewLoaded  bool
	released       bool
}

func newEmoteOptionRow(emote *hasherino.Emote, previewFactory emotePreviewFactory, onTapped func()) *emoteOptionRow {
	row := &emoteOptionRow{emote: emote, onTapped: onTapped}
	if previewFactory != nil && emote != nil {
		preview, err := previewFactory(emote, func(string) error {
			if onTapped != nil {
				onTapped()
			}
			return nil
		})
		if err != nil {
			log.Printf("Failed to create preview for emote %s: %v", emote.Name, err)
		} else if preview != nil {
			preview.SetMinSize(components.EmoteSizeFor(emote, fyne.NewSize(emoteSuggestionPreviewSize, emoteSuggestionPreviewSize)))
			row.preview = preview
		}
	}
	row.ExtendBaseWidget(row)
	return row
}

func (r *emoteOptionRow) Tapped(*fyne.PointEvent) {
	if r.onTapped != nil {
		r.onTapped()
	}
}

func (r *emoteOptionRow) setSelected(selected bool) {
	if r.selected == selected {
		return
	}
	r.selected = selected
	r.Refresh()
}

func (r *emoteOptionRow) loadPreview() {
	if r.preview == nil {
		return
	}

	r.previewMutex.Lock()
	if r.released || r.previewLoading || r.previewLoaded {
		r.previewMutex.Unlock()
		return
	}
	r.previewLoading = true
	r.previewMutex.Unlock()

	go func() {
		r.previewMutex.Lock()
		released := r.released
		r.previewMutex.Unlock()

		var err error
		if !released {
			err = r.preview.LazyLoad()
		}

		r.previewMutex.Lock()
		r.previewLoading = false
		r.previewLoaded = err == nil
		unload := r.released && r.previewLoaded
		if unload {
			r.previewLoaded = false
		}
		r.previewMutex.Unlock()

		if err != nil {
			log.Printf("Failed to load preview for emote %s: %v", r.emote.Name, err)
		}
		if unload {
			if err := r.preview.LazyUnload(); err != nil {
				log.Printf("Failed to unload preview for emote %s: %v", r.emote.Name, err)
			}
		}
	}()
}

func (r *emoteOptionRow) release() {
	if r.preview == nil {
		return
	}

	r.previewMutex.Lock()
	if r.released {
		r.previewMutex.Unlock()
		return
	}
	r.released = true
	unload := !r.previewLoading && r.previewLoaded
	if unload {
		r.previewLoaded = false
	}
	r.previewMutex.Unlock()

	if unload {
		if err := r.preview.LazyUnload(); err != nil {
			log.Printf("Failed to unload preview for emote %s: %v", r.emote.Name, err)
		}
	}
}

func (r *emoteOptionRow) CreateRenderer() fyne.WidgetRenderer {
	background := canvas.NewRectangle(color.Transparent)
	label := widget.NewLabel(r.emote.Name)
	label.Alignment = fyne.TextAlignLeading
	label.Truncation = fyne.TextTruncateEllipsis
	return &emoteOptionRowRenderer{
		row:        r,
		background: background,
		preview:    r.preview,
		label:      label,
	}
}

type emoteOptionRowRenderer struct {
	row        *emoteOptionRow
	background *canvas.Rectangle
	preview    fyne.CanvasObject
	label      *widget.Label
}

func (r *emoteOptionRowRenderer) Layout(size fyne.Size) {
	padding := theme.Padding()
	r.background.Move(fyne.NewPos(0, 0))
	r.background.Resize(size)

	textX := padding
	if r.preview != nil {
		previewSize := r.preview.MinSize()
		if previewSize.Width <= 0 {
			previewSize.Width = emoteSuggestionPreviewSize
		}
		if previewSize.Height <= 0 {
			previewSize.Height = emoteSuggestionPreviewSize
		}
		r.preview.Move(fyne.NewPos(padding, (size.Height-previewSize.Height)/2))
		r.preview.Resize(previewSize)
		textX += previewSize.Width + padding
	}

	labelWidth := max(float32(0), size.Width-textX-padding)
	labelHeight := max(float32(0), size.Height-2*padding)
	r.label.Move(fyne.NewPos(textX, padding))
	r.label.Resize(fyne.NewSize(labelWidth, labelHeight))
}

func (r *emoteOptionRowRenderer) MinSize() fyne.Size {
	padding := theme.Padding()
	labelSize := r.label.MinSize()
	width := labelSize.Width + 2*padding
	height := labelSize.Height + 2*padding
	if r.preview != nil {
		previewSize := r.preview.MinSize()
		if previewSize.Width <= 0 {
			previewSize.Width = emoteSuggestionPreviewSize
		}
		if previewSize.Height <= 0 {
			previewSize.Height = emoteSuggestionPreviewSize
		}
		width += previewSize.Width + padding
		height = max(height, previewSize.Height+2*padding)
	}
	return fyne.NewSize(width, height)
}

func (r *emoteOptionRowRenderer) Refresh() {
	if r.row.selected {
		variant := fyne.CurrentApp().Settings().ThemeVariant()
		r.background.FillColor = r.row.Theme().Color(theme.ColorNameSelection, variant)
		r.background.CornerRadius = theme.SelectionRadiusSize()
	} else {
		r.background.FillColor = color.Transparent
		r.background.CornerRadius = 0
	}
	r.background.Refresh()
	if r.preview != nil {
		r.preview.Refresh()
	}
	r.label.Refresh()
}

func (r *emoteOptionRowRenderer) Objects() []fyne.CanvasObject {
	objects := []fyne.CanvasObject{r.background}
	if r.preview != nil {
		objects = append(objects, r.preview)
	}
	return append(objects, r.label)
}

func (r *emoteOptionRowRenderer) Destroy() {
	r.row.release()
}

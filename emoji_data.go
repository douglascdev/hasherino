package main

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

//go:embed data-by-emoji.json
var emojiJSONData []byte

type emojiEntry struct {
	Emoji string
	Name  string
	Slug  string
	Group string
}

type emojiJSONEntry struct {
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Group string `json:"group"`
}

var (
	emojiOnce    sync.Once
	emojiEntries []emojiEntry
)

// allEmojis parses the bundled emoji index once and returns it sorted by name.
func allEmojis() []emojiEntry {
	emojiOnce.Do(func() {
		raw := map[string]emojiJSONEntry{}
		if err := json.Unmarshal(emojiJSONData, &raw); err != nil {
			return
		}
		emojiEntries = make([]emojiEntry, 0, len(raw))
		for char, entry := range raw {
			emojiEntries = append(emojiEntries, emojiEntry{
				Emoji: char,
				Name:  entry.Name,
				Slug:  entry.Slug,
				Group: entry.Group,
			})
		}
		sort.Slice(emojiEntries, func(i, j int) bool {
			return strings.ToLower(emojiEntries[i].Name) < strings.ToLower(emojiEntries[j].Name)
		})
	})
	return emojiEntries
}

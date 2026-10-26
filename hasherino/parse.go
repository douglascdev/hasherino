package hasherino

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"gopkg.in/irc.v4"
)

type ChatMessage struct {
	Channel     string
	Command     string
	Author      string
	AuthorColor string // hex color of the author's name from the irc tags, empty when unset
	Text        string
	Time        time.Time
	Emotes      map[string]*Emote // twitch emotes of the message from the irc emotes tag, keyed by name
}

func ParseMessage(message string) (*ChatMessage, error) {
	msg, err := irc.ParseMessage(message)
	if err != nil {
		return nil, err
	}
	channel := ""
	if len(msg.Params) > 0 {
		if len(msg.Params[0]) > 0 && msg.Params[0][0] == '#' {
			channel = msg.Params[0][1:]
		} else {
			channel = msg.Params[0]
		}
	}
	paramsText := ""
	if len(msg.Params) > 1 {
		paramsText = strings.Join(msg.Params[1:], " ")
	}
	sentAt := time.Now()
	if ts, ok := msg.Tags["tmi-sent-ts"]; ok {
		if millis, err := strconv.ParseInt(ts, 10, 64); err == nil {
			sentAt = time.UnixMilli(millis)
		}
	}
	return &ChatMessage{
		Channel:     channel,
		Command:     msg.Command,
		Author:      msg.Name,
		AuthorColor: msg.Tags["color"],
		Text:        paramsText,
		Time:        sentAt,
		Emotes:      parseTagEmotes(msg.Tags["emotes"], paramsText),
	}, nil

}

// parseTagEmotes parses the emotes tag of a privmsg, e.g.
// "25:0-4,12-16/1902:6-10", into the twitch emotes it marks in the text,
// keyed by their name. Every entry holds the id of the emote and the
// positions of its occurrences in the message, which are 0-based start and
// end indices of utf-16 code units. Entries with invalid positions are
// skipped.
func parseTagEmotes(tag string, text string) map[string]*Emote {
	if tag == "" {
		return nil
	}
	units := utf16.Encode([]rune(text))
	emotes := map[string]*Emote{}
	for _, entry := range strings.Split(tag, "/") {
		id, positions, found := strings.Cut(entry, ":")
		if !found {
			continue
		}
		for _, position := range strings.Split(positions, ",") {
			startIndex, endIndex, found := strings.Cut(position, "-")
			if !found {
				continue
			}
			start, startErr := strconv.Atoi(startIndex)
			end, endErr := strconv.Atoi(endIndex)
			if startErr != nil || endErr != nil || start < 0 || end < start || end >= len(units) {
				continue
			}
			name := string(utf16.Decode(units[start : end+1]))
			emotes[name] = &Emote{Id: id, Source: Twitch, Name: name}
		}
	}
	return emotes
}

package main

import (
	"testing"
	"time"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

func testChatMessage(id, text string, at time.Time) hasherino.ChatMessage {
	return hasherino.ChatMessage{
		Id:      id,
		Command: "PRIVMSG",
		Author:  "user",
		Text:    text,
		Time:    at,
	}
}

func chatTexts(c *chat) []string {
	messages := c.snapshot()
	texts := make([]string, len(messages))
	for i := range messages {
		texts[i] = messages[i].text
	}
	return texts
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestChatAppendHistoryInsertsMissedMessagesInOrder(t *testing.T) {
	chat := newChat()
	base := time.Unix(1_700_000_000, 0)
	t0, t1, t2, t3 := base, base.Add(time.Second), base.Add(2*time.Second), base.Add(3*time.Second)

	if !chat.appendLive(testChatMessage("m1", "one", t1)) {
		t.Fatal("expected the first live message to be added")
	}
	cursor := chat.backfillCursor()
	if !cursor.Equal(t1) {
		t.Fatalf("unexpected cursor: got %s, want %s", cursor, t1)
	}

	// a live message arrives while the history request is in flight
	if !chat.appendLive(testChatMessage("m3", "three", t3)) {
		t.Fatal("expected the second live message to be added")
	}

	history := []hasherino.ChatMessage{
		testChatMessage("m0", "zero", t0),
		testChatMessage("m1", "one", t1),
		testChatMessage("m2", "two", t2),
		testChatMessage("m3", "three", t3),
	}
	if added := chat.appendHistory(history, cursor); added != 1 {
		t.Fatalf("expected 1 missed message to be added, got %d", added)
	}

	if got, want := chatTexts(chat), []string{"one", "two", "three"}; !equalStrings(got, want) {
		t.Errorf("unexpected chat order: got %v, want %v", got, want)
	}
}

func TestChatAppendHistoryDeduplicatesMessageIDs(t *testing.T) {
	chat := newChat()
	at := time.Unix(1_700_000_000, 0)
	chat.appendLive(testChatMessage("same", "hello", at))

	cursor := chat.backfillCursor()
	history := []hasherino.ChatMessage{testChatMessage("same", "hello", at)}
	if added := chat.appendHistory(history, cursor); added != 0 {
		t.Fatalf("expected the known message to be skipped, got %d additions", added)
	}
	if got := len(chat.snapshot()); got != 1 {
		t.Errorf("expected 1 message, got %d", got)
	}
}

func TestChatAppendHistoryKeepsUnseenMessageWithSameTimestamp(t *testing.T) {
	chat := newChat()
	at := time.Unix(1_700_000_000, 0)
	chat.appendLive(testChatMessage("first", "first", at))

	cursor := chat.backfillCursor()
	history := []hasherino.ChatMessage{testChatMessage("second", "second", at)}
	if added := chat.appendHistory(history, cursor); added != 1 {
		t.Fatalf("expected the unseen message to be added, got %d", added)
	}
	if got := len(chat.snapshot()); got != 2 {
		t.Errorf("expected 2 messages, got %d", got)
	}
}

func TestChatAppendLiveDeduplicatesMessageIDs(t *testing.T) {
	chat := newChat()
	at := time.Unix(1_700_000_000, 0)
	message := testChatMessage("same", "hello", at)

	if !chat.appendLive(message) {
		t.Fatal("expected the first message to be added")
	}
	if chat.appendLive(message) {
		t.Fatal("expected the duplicate message to be skipped")
	}
	if got := len(chat.snapshot()); got != 1 {
		t.Errorf("expected 1 message, got %d", got)
	}
}

func TestChatBackfillCursorTracksNewestMessage(t *testing.T) {
	chat := newChat()
	first := time.Unix(1_700_000_000, 0)
	second := first.Add(time.Minute)
	chat.appendLive(testChatMessage("first", "first", first))
	chat.appendLive(testChatMessage("second", "second", second))

	if got := chat.backfillCursor(); !got.Equal(second) {
		t.Errorf("unexpected cursor: got %s, want %s", got, second)
	}
}

func TestChatHistoryLoadedIsRecorded(t *testing.T) {
	chat := newChat()
	if chat.historyIsLoaded() {
		t.Fatal("expected history not to be loaded initially")
	}
	chat.markHistoryLoaded()
	if !chat.historyIsLoaded() {
		t.Error("expected history to be marked loaded")
	}
}

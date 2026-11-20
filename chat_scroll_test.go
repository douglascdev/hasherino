package main

import (
	"testing"
	"time"

	"gioui.org/layout"
)

func TestChatFollowsBottomWhenNearBottom(t *testing.T) {
	chat := newChat()
	now := time.Unix(1_700_000_000, 0)

	chat.atBottom = true
	if !chat.appendLive(testChatMessage("first", "first", now)) {
		t.Fatal("expected the message to be added")
	}
	if !chat.takeScrollToEnd() {
		t.Error("expected a new message near the bottom to scroll to the end")
	}

	chat.atBottom = false
	if !chat.appendLive(testChatMessage("second", "second", now.Add(time.Second))) {
		t.Fatal("expected the message to be added")
	}
	if chat.takeScrollToEnd() {
		t.Error("expected a new message away from the bottom not to scroll")
	}
}

func TestChatRestoreAnchorKeepsFirstVisibleMessage(t *testing.T) {
	chat := newChat()
	base := time.Unix(1_700_000_000, 0)
	for i, text := range []string{"one", "two", "three"} {
		chat.appendLive(testChatMessage(text, text, base.Add(time.Duration(i)*time.Second)))
	}

	messages := chat.snapshot()
	chat.list.Position.First = 1
	chat.list.Position.Offset = 17
	chat.anchorSeq = messages[1].seq
	chat.anchorOffset = 17

	// simulate a backfilled message inserted above the viewport
	chat.mu.Lock()
	chat.nextSeq++
	older := chatMessage{id: "older", seq: chat.nextSeq, author: "user", text: "older", sentAt: base.Add(-time.Second)}
	older.parts = chat.buildPartsLocked(older.text, older.ownEmotes)
	chat.messages = append([]chatMessage{older}, chat.messages...)
	chat.mu.Unlock()

	chat.restoreAnchor(chat.snapshot())

	if chat.list.Position.First != 2 {
		t.Errorf("expected the anchor to move with the inserted message, got first=%d", chat.list.Position.First)
	}
	if chat.list.Position.Offset != 17 {
		t.Errorf("expected the offset to be preserved, got %d", chat.list.Position.Offset)
	}
}

func TestChatRestoreAnchorAfterTrimKeepsVisibleMessage(t *testing.T) {
	chat := newChat()
	chat.setLimit(3)
	base := time.Unix(1_700_000_000, 0)
	for i, text := range []string{"one", "two", "three"} {
		chat.appendLive(testChatMessage(text, text, base.Add(time.Duration(i)*time.Second)))
	}

	messages := chat.snapshot()
	chat.list.Position.First = 1
	chat.list.Position.Offset = 9
	chat.anchorSeq = messages[1].seq
	chat.anchorOffset = 9

	// the oldest message is trimmed when the next one arrives
	chat.appendLive(testChatMessage("four", "four", base.Add(3*time.Second)))
	chat.restoreAnchor(chat.snapshot())

	if chat.list.Position.First != 0 {
		t.Errorf("expected the anchored message to move to the front, got first=%d", chat.list.Position.First)
	}
	if chat.list.Position.Offset != 9 {
		t.Errorf("expected the offset to be preserved, got %d", chat.list.Position.Offset)
	}
}

func TestChatRestoreAnchorFallsBackWhenMessageWasTrimmed(t *testing.T) {
	chat := newChat()
	chat.appendLive(testChatMessage("one", "one", time.Unix(1_700_000_000, 0)))
	chat.appendLive(testChatMessage("two", "two", time.Unix(1_700_000_001, 0)))

	chat.anchorSeq = 999
	chat.anchorOffset = 30
	chat.list.Position.First = 1
	chat.restoreAnchor(chat.snapshot())

	if chat.list.Position.First != 0 || chat.list.Position.Offset != 0 {
		t.Errorf("expected the oldest message to be shown, got first=%d offset=%d", chat.list.Position.First, chat.list.Position.Offset)
	}
}

func TestChatScrollStateDetectsNearBottom(t *testing.T) {
	messages := []chatMessage{{seq: 1}, {seq: 2}, {seq: 3}, {seq: 4}}
	tests := []struct {
		name     string
		position layout.Position
		near     bool
	}{
		{
			name:     "last message and close to the bottom",
			position: layout.Position{First: 1, Count: 3, OffsetLast: 20, Length: 1000},
			near:     true,
		},
		{
			name:     "last message but far from the bottom",
			position: layout.Position{First: 1, Count: 3, OffsetLast: -100, Length: 1000},
			near:     false,
		},
		{
			name:     "more messages below",
			position: layout.Position{First: 0, Count: 2, OffsetLast: 100, Length: 1000},
			near:     false,
		},
		{
			name:     "all messages fit",
			position: layout.Position{First: 0, Count: 4, OffsetLast: 300, Length: 500},
			near:     true,
		},
		{
			name:     "scrolled slightly past the bottom",
			position: layout.Position{First: 1, Count: 3, OffsetLast: -20, Length: 1000},
			near:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chat := newChat()
			chat.list.Position = test.position
			chat.updateScrollState(messages, 1, 48)
			if chat.atBottom != test.near {
				t.Errorf("expected atBottom=%t, got %t", test.near, chat.atBottom)
			}
		})
	}
}

func TestChatScrollStateCapturesAnchor(t *testing.T) {
	messages := []chatMessage{{seq: 1}, {seq: 2}, {seq: 3}}
	chat := newChat()
	chat.list.Position = layout.Position{First: 1, Offset: 12, Count: 2, OffsetLast: 0, Length: 500}

	chat.updateScrollState(messages, 7, 48)

	if chat.anchorSeq != 2 || chat.anchorOffset != 12 || chat.anchorVersion != 7 {
		t.Errorf("unexpected anchor: seq=%d offset=%d version=%d", chat.anchorSeq, chat.anchorOffset, chat.anchorVersion)
	}
}

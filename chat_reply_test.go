package main

import (
	"testing"
	"time"

	"github.com/Hashy-Software/hasherino-go/hasherino"
)

func TestChatAppendLiveKeepsReplyContext(t *testing.T) {
	chat := newChat()
	message := hasherino.ChatMessage{
		Id:                "child",
		Command:           "PRIVMSG",
		Author:            "bob",
		Text:              "@alice reply",
		Time:              time.Unix(1_700_000_000, 0),
		ReplyParentID:     "parent",
		ReplyParentLogin:  "alice",
		ReplyParentAuthor: "Alice",
		ReplyParentText:   "original message",
	}
	if !chat.appendLive(message) {
		t.Fatal("expected the message to be added")
	}

	messages := chat.snapshot()
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	if messages[0].replyID != "parent" || messages[0].replyAuthor != "Alice" || messages[0].replyText != "original message" {
		t.Errorf("unexpected reply context: %+v", messages[0])
	}
	if messages[0].text != "reply" {
		t.Errorf("expected the reply mention to be removed, got %q", messages[0].text)
	}
}

func TestReplyTargetSnapshot(t *testing.T) {
	message := chatMessage{id: "message-id", author: "alice", text: "hello"}
	reply := &replyTarget{id: message.id, author: message.author, text: message.text}
	if reply.id != "message-id" || reply.author != "alice" || reply.text != "hello" {
		t.Errorf("unexpected reply target: %+v", reply)
	}
}

func TestChatFocusMessageScrollsToItAndHighlightsIt(t *testing.T) {
	chat := newChat()
	base := time.Unix(1_700_000_000, 0)
	for i, id := range []string{"one", "two", "three"} {
		chat.appendLive(testChatMessage(id, id, base.Add(time.Duration(i)*time.Second)))
	}

	chat.focusMessage("two")
	if chat.list.Position.First != 1 {
		t.Errorf("expected the list to scroll to the second message, got first=%d", chat.list.Position.First)
	}
	if chat.focusedID != "two" {
		t.Errorf("expected the second message to be focused, got %q", chat.focusedID)
	}
	if !chat.focusUntil.After(time.Now()) {
		t.Error("expected the focus highlight to have a deadline")
	}

	chat.focusMessage("missing")
	if chat.focusedID != "two" {
		t.Error("expected an unknown message id to leave the focus unchanged")
	}
}

func TestStripReplyMention(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		login       string
		displayName string
		want        string
	}{
		{name: "login", text: "@alice hello", login: "alice", displayName: "Alice", want: "hello"},
		{name: "case insensitive", text: "@Alice hello", login: "alice", displayName: "Alice", want: "hello"},
		{name: "display name fallback", text: "@Alice hello", displayName: "Alice", want: "hello"},
		{name: "only the mention", text: "@alice", login: "alice", want: ""},
		{name: "not a mention prefix", text: "@alicex hello", login: "alice", want: "@alicex hello"},
		{name: "not a reply", text: "@alice hello", want: "@alice hello"},
		{name: "normal text", text: "hello", login: "alice", want: "hello"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := stripReplyMention(test.text, test.login, test.displayName); got != test.want {
				t.Errorf("unexpected text: got %q, want %q", got, test.want)
			}
		})
	}
}

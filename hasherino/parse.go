package hasherino

import (
	"strconv"
	"strings"
	"time"

	"gopkg.in/irc.v4"
)

type ChatMessage struct {
	Channel string
	Command string
	Author  string
	Text    string
	Time    time.Time
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
		Channel: channel,
		Command: msg.Command,
		Author:  msg.Name,
		Text:    paramsText,
		Time:    sentAt,
	}, nil

}

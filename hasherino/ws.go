package hasherino

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"nhooyr.io/websocket"
)

type WebsocketState int64

const (
	Disconnected WebsocketState = iota
	Connected
)

type TwitchChatWebsocket struct {
	State WebsocketState

	url              string
	context          context.Context
	cancel           context.CancelFunc
	connection       *websocket.Conn
	initial_messages *[]string
	channels         map[string]struct{}
}

func (w *TwitchChatWebsocket) New(token string, user string) (*TwitchChatWebsocket, error) {
	initial_messages := []string{
		"CAP REQ :twitch.tv/commands twitch.tv/tags",
		"PASS " + token,
		"NICK " + user,
	}
	url := "wss://irc-ws.chat.twitch.tv"
	c, ctx, cancel, err := w.dial(url)
	if err != nil {
		return nil, err
	}
	return &TwitchChatWebsocket{
		url:              url,
		State:            Disconnected,
		context:          *ctx,
		cancel:           *cancel,
		connection:       c,
		initial_messages: &initial_messages,
		channels:         make(map[string]struct{}),
	}, nil
}

func (w *TwitchChatWebsocket) dial(url string) (*websocket.Conn, *context.Context, *context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(context.Background())
	c, _, err := websocket.Dial(ctx, url, nil)
	return c, &ctx, &cancel, err
}

func (w *TwitchChatWebsocket) Connect() error {
	if w.State != Disconnected {
		return errors.New("Not disconnected")
	}
	for _, msg := range *w.initial_messages {
		err := w.connection.Write(w.context, websocket.MessageText, []byte(msg))
		if err != nil {
			return err
		}
	}
	w.State = Connected
	return nil
}

func (w *TwitchChatWebsocket) Close() {
	if w == nil || (w.cancel == nil && w.connection == nil) {
		return
	}
	log.Println("closing websocket")
	if w.cancel != nil {
		w.cancel()
	}
	if w.connection != nil {
		_ = w.connection.Close(websocket.StatusNormalClosure, "")
	}
	w.State = Disconnected
}

func (w *TwitchChatWebsocket) Listen(callback func(message string)) error {
	if w.State != Connected {
		return errors.New("Not connected")
	}

	for {
		_, content, err := w.connection.Read(w.context)
		if err != nil {
			log.Println("error: '", err, "', attempting reconnect")
			w.State = Disconnected

			c, ctx, cancel, err := w.dial(w.url)
			w.connection = c
			w.context = *ctx
			w.cancel = *cancel
			if err != nil {
				log.Println("failed to redial:", err)
				time.Sleep(time.Second * 2)
				continue
			}
			err = w.Connect()
			if err != nil {
				log.Println("failed to reconnect:", err)
				time.Sleep(time.Second * 2)
				continue
			}
			keys := make([]string, 0, len(w.channels))
			for k := range w.channels {
				keys = append(keys, k)
			}
			err = w.Join(keys...)
			if err != nil {
				log.Println("failed to rejoin:", err)
				time.Sleep(time.Second * 2)
				continue
			}
		}
		s := string(content)
		fmt.Println("Message: " + s)
		callback(s)
	}
	w.Close()

	return nil
}

func (w *TwitchChatWebsocket) Join(channelStrings ...string) error {
	if w.State != Connected {
		return errors.New("Not connected")
	}
	joinStr := "JOIN "
	for _, channel := range channelStrings {
		joinStr += "#" + channel + ","
	}
	joinStr = joinStr[:len(joinStr)-1]

	err := w.connection.Write(w.context, websocket.MessageText, []byte(joinStr))
	if err != nil {
		return err
	}
	for _, channel := range channelStrings {
		w.channels[channel] = struct{}{}
	}
	return nil
}

func (w *TwitchChatWebsocket) Part(channel string) error {
	if w.State != Connected {
		return errors.New("Not connected")
	}
	_, ok := w.channels[channel]
	if !ok {
		return errors.New("Not in channel")
	}

	err := w.connection.Write(w.context, websocket.MessageText, []byte("PART #"+channel))
	if err != nil {
		return err
	}

	delete(w.channels, channel)
	return nil
}

func (w *TwitchChatWebsocket) Send(channel string, message string) error {
	if w.State != Connected {
		return errors.New("Not connected")
	}
	err := w.connection.Write(w.context, websocket.MessageText, []byte("PRIVMSG #"+channel+" :"+message))
	if err != nil {
		return err
	}
	return nil
}

package hasherino

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"gopkg.in/irc.v4"
	"nhooyr.io/websocket"
)

type WebsocketState int64

const (
	Disconnected WebsocketState = iota
	Connected
)

// ErrNotConnected is returned when a message is sent while the chat
// connection is down. The connection reconnects on its own; the caller can
// retry later.
var ErrNotConnected = errors.New("not connected")

// errWebsocketClosed is returned internally when a connection is shut down on
// purpose, so the listener stops instead of reconnecting.
var errWebsocketClosed = errors.New("websocket closed")

// TwitchChatWebsocket is one IRC-over-websocket connection. All methods are
// safe for concurrent use: the listener goroutine reconnects and answers
// pings while the ui sends messages.
type TwitchChatWebsocket struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	// connectMu serializes dials, so Connect and a reconnect cannot open two
	// connections at once.
	connectMu sync.Mutex

	state           WebsocketState
	url             string
	context         context.Context
	cancel          context.CancelFunc
	connection      *websocket.Conn
	initialMessages []string
	channels        map[string]struct{}

	closed    bool
	listening bool
	done      chan struct{}

	backoff      time.Duration
	reconnectMin time.Duration
	reconnectMax time.Duration
}

// New creates a chat connection. The connection is established by Connect.
func (w *TwitchChatWebsocket) New(token string, user string) (*TwitchChatWebsocket, error) {
	initialMessages := []string{
		"CAP REQ :twitch.tv/commands twitch.tv/tags",
		"PASS " + token,
		"NICK " + user,
	}
	return &TwitchChatWebsocket{
		url:             "wss://irc-ws.chat.twitch.tv",
		state:           Disconnected,
		initialMessages: initialMessages,
		channels:        make(map[string]struct{}),
		done:            make(chan struct{}),
		backoff:         time.Second,
		reconnectMin:    time.Second,
		reconnectMax:    30 * time.Second,
	}, nil
}

// Connect dials the server and sends the login messages. It is safe to call
// on an already connected socket.
func (w *TwitchChatWebsocket) Connect() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errWebsocketClosed
	}
	connected := w.state == Connected
	w.mu.Unlock()
	if connected {
		return nil
	}
	return w.connect()
}

// connect opens a new connection, logs in and rejoins the channels the
// socket is supposed to be in.
func (w *TwitchChatWebsocket) connect() error {
	w.connectMu.Lock()
	defer w.connectMu.Unlock()

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errWebsocketClosed
	}
	if w.state == Connected {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	connection, _, err := websocket.Dial(ctx, w.url, nil)
	if err != nil {
		cancel()
		return err
	}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		cancel()
		connection.Close(websocket.StatusNormalClosure, "")
		return errWebsocketClosed
	}
	w.connection = connection
	w.context = ctx
	w.cancel = cancel
	w.mu.Unlock()

	for _, message := range w.initialMessages {
		if err := connection.Write(ctx, websocket.MessageText, []byte(message)); err != nil {
			w.dropConnection()
			return err
		}
	}

	w.mu.Lock()
	w.state = Connected
	channels := make([]string, 0, len(w.channels))
	for channel := range w.channels {
		channels = append(channels, channel)
	}
	w.mu.Unlock()

	if len(channels) > 0 {
		if err := w.Join(channels...); err != nil {
			w.dropConnection()
			return err
		}
	}
	return nil
}

// dropConnection closes the current connection and marks the socket as
// disconnected, so the listener reconnects. It never returns an error.
func (w *TwitchChatWebsocket) dropConnection() {
	w.mu.Lock()
	connection := w.connection
	cancel := w.cancel
	w.connection = nil
	w.context = nil
	w.cancel = nil
	w.state = Disconnected
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if connection != nil {
		connection.Close(websocket.StatusGoingAway, "")
	}
}

// Close shuts the connection down for good. A listener running on it stops
// instead of reconnecting.
func (w *TwitchChatWebsocket) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	done := w.done
	if done != nil {
		close(done)
	}
	connection := w.connection
	cancel := w.cancel
	w.connection = nil
	w.context = nil
	w.cancel = nil
	w.state = Disconnected
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if connection != nil {
		connection.Close(websocket.StatusNormalClosure, "")
	}
}

// IsConnected reports whether the socket is currently connected.
func (w *TwitchChatWebsocket) IsConnected() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state == Connected
}

// HasChannel reports whether the socket is supposed to be joined to channel.
func (w *TwitchChatWebsocket) HasChannel(channel string) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.channels[channel]
	return ok
}

// StartListening runs the read loop in the background. It answers pings,
// reconnects when the connection drops, and calls onAuthError when Twitch
// rejects the login. Only one listener is started per socket.
func (w *TwitchChatWebsocket) StartListening(callback func(string), onAuthError func()) {
	w.mu.Lock()
	if w.listening || w.closed {
		w.mu.Unlock()
		return
	}
	w.listening = true
	w.mu.Unlock()

	go func() {
		err := w.Listen(callback, onAuthError)
		if err != nil {
			log.Printf("chat websocket listener stopped: %s", err)
		}
		w.mu.Lock()
		w.listening = false
		w.mu.Unlock()
	}()
}

// Listen reads messages until the socket is closed. It answers Twitch pings,
// reconnects after network errors, and returns after an authentication
// failure so the caller can refresh the token.
func (w *TwitchChatWebsocket) Listen(callback func(string), onAuthError func()) error {
	pending := ""
	for {
		w.mu.Lock()
		if w.closed {
			w.mu.Unlock()
			return nil
		}
		connected := w.state == Connected
		connection, ctx := w.connection, w.context
		w.mu.Unlock()

		if !connected || connection == nil || ctx == nil {
			if err := w.connect(); err != nil {
				if errors.Is(err, errWebsocketClosed) {
					return nil
				}
				log.Printf("failed to connect chat websocket: %s; retrying", err)
				if !w.waitBackoff() {
					return nil
				}
			}
			pending = ""
			continue
		}

		_, data, err := connection.Read(ctx)
		if err != nil {
			w.mu.Lock()
			closed := w.closed
			w.mu.Unlock()
			if closed {
				return nil
			}
			log.Printf("chat connection lost: %s; reconnecting", err)
			w.dropConnection()
			pending = ""
			if !w.waitBackoff() {
				return nil
			}
			continue
		}

		// only a connection that actually delivers data resets the backoff,
		// so a server that accepts and immediately closes is not hammered
		w.mu.Lock()
		w.backoff = w.reconnectMin
		w.mu.Unlock()

		pending += string(data)
		reconnect, authFailed := false, false
		for {
			index := strings.IndexByte(pending, '\n')
			if index < 0 {
				break
			}
			line := strings.TrimSuffix(pending[:index], "\r")
			pending = pending[index+1:]
			if line == "" {
				continue
			}
			lineReconnect, lineAuthFailed := w.handleLine(line, callback)
			reconnect = reconnect || lineReconnect
			authFailed = authFailed || lineAuthFailed
			if reconnect || authFailed {
				break
			}
		}

		if authFailed {
			w.dropConnection()
			if onAuthError != nil {
				onAuthError()
				return nil
			}
			if !w.waitBackoff() {
				return nil
			}
			continue
		}
		if reconnect {
			w.dropConnection()
			if !w.wait(time.Second) {
				return nil
			}
			continue
		}
	}
}

// handleLine handles one IRC line. It reports whether the connection has to
// be reconnected or the token was rejected.
func (w *TwitchChatWebsocket) handleLine(line string, callback func(string)) (reconnect bool, authFailed bool) {
	message, err := irc.ParseMessage(line)
	if err != nil {
		return false, false
	}

	switch message.Command {
	case "PING":
		target := "tmi.twitch.tv"
		if len(message.Params) > 0 {
			target = message.Params[len(message.Params)-1]
		}
		if err := w.writeLine("PONG :" + target); err != nil {
			return true, false
		}
		return false, false
	case "PONG":
		return false, false
	case "RECONNECT":
		return true, false
	case "NOTICE":
		if isAuthenticationFailure(line) {
			return false, true
		}
	}
	if callback != nil {
		callback(line)
	}
	return false, false
}

// isAuthenticationFailure reports whether a notice means Twitch rejected the
// login, which only a new token can fix.
func isAuthenticationFailure(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "login authentication failed") ||
		strings.Contains(lower, "improperly formatted auth") ||
		strings.Contains(lower, "authentication failed")
}

// waitBackoff sleeps for the current reconnect delay and increases it for the
// next attempt. It returns false when the socket was closed while waiting.
func (w *TwitchChatWebsocket) waitBackoff() bool {
	w.mu.Lock()
	delay := w.backoff
	minimum := w.reconnectMin
	maximum := w.reconnectMax
	if delay <= 0 {
		delay = minimum
	}
	w.backoff = min(delay*2, maximum)
	w.mu.Unlock()
	return w.wait(delay)
}

// wait sleeps for d or until the socket is closed.
func (w *TwitchChatWebsocket) wait(d time.Duration) bool {
	w.mu.Lock()
	done := w.done
	w.mu.Unlock()
	if done == nil {
		time.Sleep(d)
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-done:
		return false
	case <-timer.C:
		return true
	}
}

// writeLine writes one irc line. It fails fast when the connection is down,
// instead of blocking on a dead socket.
func (w *TwitchChatWebsocket) writeLine(line string) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errWebsocketClosed
	}
	if w.state != Connected || w.connection == nil || w.context == nil {
		w.mu.Unlock()
		return ErrNotConnected
	}
	connection := w.connection
	ctx := w.context
	w.mu.Unlock()

	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return connection.Write(ctx, websocket.MessageText, []byte(line))
}

// send writes a line and drops the connection on failure, so the listener
// reconnects instead of leaving sends silently broken.
func (w *TwitchChatWebsocket) send(line string) error {
	if err := w.writeLine(line); err != nil {
		if !errors.Is(err, ErrNotConnected) {
			w.dropConnection()
		}
		return err
	}
	return nil
}

// Join joins channels. The channels are remembered even when the write fails,
// so an automatic reconnect rejoins them.
func (w *TwitchChatWebsocket) Join(channelStrings ...string) error {
	if len(channelStrings) == 0 {
		return nil
	}

	channels := make([]string, 0, len(channelStrings))
	for _, channel := range channelStrings {
		if channel != "" {
			channels = append(channels, channel)
		}
	}
	if len(channels) == 0 {
		return nil
	}

	w.mu.Lock()
	for _, channel := range channels {
		w.channels[channel] = struct{}{}
	}
	w.mu.Unlock()

	names := make([]string, len(channels))
	for i, channel := range channels {
		names[i] = "#" + channel
	}
	return w.send("JOIN " + strings.Join(names, ","))
}

// Part leaves a channel.
func (w *TwitchChatWebsocket) Part(channel string) error {
	w.mu.Lock()
	_, joined := w.channels[channel]
	delete(w.channels, channel)
	w.mu.Unlock()
	if !joined {
		return errors.New("Not in channel")
	}
	return w.send("PART #" + channel)
}

// Send sends a chat message.
func (w *TwitchChatWebsocket) Send(channel string, message string) error {
	return w.send("PRIVMSG #" + channel + " :" + message)
}

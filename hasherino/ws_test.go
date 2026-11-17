package hasherino

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

// websocketTestServer starts an in-process websocket server. The handler gets
// a connection with a deadline and runs for every accepted connection.
func websocketTestServer(t *testing.T, handler func(ctx context.Context, connection *websocket.Conn)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		handler(ctx, connection)
	}))
	t.Cleanup(server.Close)
	return server
}

func websocketTestURL(server *httptest.Server) string {
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

func newTestWebsocket(t *testing.T, server *httptest.Server) *TwitchChatWebsocket {
	t.Helper()
	ws, err := (&TwitchChatWebsocket{}).New("oauth:token", "user")
	if err != nil {
		t.Fatal(err)
	}
	ws.url = websocketTestURL(server)
	// keep reconnects fast in tests
	ws.reconnectMin = 10 * time.Millisecond
	ws.reconnectMax = 100 * time.Millisecond
	ws.backoff = ws.reconnectMin
	t.Cleanup(ws.Close)
	return ws
}

func readTestLine(t *testing.T, ctx context.Context, connection *websocket.Conn) string {
	t.Helper()
	_, data, err := connection.Read(ctx)
	if err != nil {
		t.Errorf("failed to read websocket message: %s", err)
		return ""
	}
	return strings.TrimSuffix(string(data), "\r\n")
}

func readTestHandshake(t *testing.T, ctx context.Context, connection *websocket.Conn) {
	t.Helper()
	for range 3 {
		if line := readTestLine(t, ctx, connection); line == "" {
			return
		}
	}
}

func TestWebsocketAnswersPing(t *testing.T) {
	pong := make(chan string, 1)
	server := websocketTestServer(t, func(ctx context.Context, connection *websocket.Conn) {
		readTestHandshake(t, ctx, connection)
		if err := connection.Write(ctx, websocket.MessageText, []byte("PING :tmi.twitch.tv\r\n")); err != nil {
			return
		}
		_, data, err := connection.Read(ctx)
		if err != nil {
			return
		}
		pong <- string(data)
	})

	ws := newTestWebsocket(t, server)
	ws.StartListening(nil, nil)

	select {
	case got := <-pong:
		if got != "PONG :tmi.twitch.tv" {
			t.Errorf("unexpected pong: got %q, want %q", got, "PONG :tmi.twitch.tv")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected the websocket to answer the ping")
	}
}

func TestWebsocketReconnectsAfterConnectionDrop(t *testing.T) {
	var connections atomic.Int32
	messages := make(chan string, 1)
	server := websocketTestServer(t, func(ctx context.Context, connection *websocket.Conn) {
		number := connections.Add(1)
		readTestHandshake(t, ctx, connection)
		if number == 1 {
			// answer one ping, then drop the connection
			if err := connection.Write(ctx, websocket.MessageText, []byte("PING :tmi.twitch.tv\r\n")); err != nil {
				return
			}
			readTestLine(t, ctx, connection)
			return
		}
		connection.Write(ctx, websocket.MessageText, []byte("PRIVMSG #channel :hello\r\n"))
	})

	ws := newTestWebsocket(t, server)
	ws.StartListening(func(message string) {
		if strings.Contains(message, "PRIVMSG") {
			messages <- message
		}
	}, nil)

	select {
	case message := <-messages:
		if !strings.Contains(message, "hello") {
			t.Errorf("unexpected message: got %q", message)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("expected the websocket to reconnect and keep reading")
	}
	if connections.Load() < 2 {
		t.Errorf("expected a reconnect, got %d connections", connections.Load())
	}
}

func TestWebsocketReportsAuthenticationFailure(t *testing.T) {
	server := websocketTestServer(t, func(ctx context.Context, connection *websocket.Conn) {
		readTestHandshake(t, ctx, connection)
		connection.Write(ctx, websocket.MessageText, []byte(":tmi.twitch.tv NOTICE * :Login authentication failed\r\n"))
	})

	ws := newTestWebsocket(t, server)
	authFailed := make(chan struct{}, 1)
	ws.StartListening(nil, func() {
		authFailed <- struct{}{}
	})

	select {
	case <-authFailed:
	case <-time.After(3 * time.Second):
		t.Fatal("expected the authentication failure to be reported")
	}
	if ws.IsConnected() {
		t.Error("expected the socket to be disconnected after an authentication failure")
	}
}

func TestWebsocketRejoinsChannelsAfterReconnect(t *testing.T) {
	var connections atomic.Int32
	rejoined := make(chan string, 1)
	server := websocketTestServer(t, func(ctx context.Context, connection *websocket.Conn) {
		number := connections.Add(1)
		readTestHandshake(t, ctx, connection)
		for {
			line := readTestLine(t, ctx, connection)
			if line == "" {
				return
			}
			if !strings.HasPrefix(line, "JOIN #") {
				continue
			}
			if number == 1 {
				// let the first connection drop after the initial join
				return
			}
			rejoined <- line
			return
		}
	})

	ws := newTestWebsocket(t, server)
	if err := ws.Connect(); err != nil {
		t.Fatal(err)
	}
	if err := ws.Join("channel"); err != nil {
		t.Fatalf("failed to join the channel: %s", err)
	}
	ws.StartListening(nil, nil)

	select {
	case line := <-rejoined:
		if line != "JOIN #channel" {
			t.Errorf("unexpected rejoin: got %q", line)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("expected the websocket to rejoin its channel after reconnecting")
	}
}

func TestWebsocketSendFailsWhenDisconnected(t *testing.T) {
	ws, err := (&TwitchChatWebsocket{}).New("oauth:token", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.Send("channel", "hello"); err != ErrNotConnected {
		t.Errorf("expected ErrNotConnected, got %v", err)
	}
}

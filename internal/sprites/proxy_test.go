package sprites

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestDialProxyPerformsSSHHandshakeAndCarriesBinaryFrames(t *testing.T) {
	const (
		token      = "secret-test-token"
		spriteName = "test-sprite"
	)

	serverErr := make(chan error, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sprites/"+spriteName+"/proxy" {
			serverErr <- errors.New("wrong proxy request path: " + r.URL.Path)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			serverErr <- errors.New("wrong authorization header")
			return
		}

		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			serverErr <- err
			return
		}
		defer func() { _ = ws.Close() }()

		messageType, payload, err := ws.ReadMessage()
		if err != nil {
			serverErr <- err
			return
		}
		if messageType != websocket.TextMessage {
			serverErr <- errors.New("proxy init was not a text frame")
			return
		}
		var initMessage struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		}
		if err := json.Unmarshal(payload, &initMessage); err != nil {
			serverErr <- err
			return
		}
		if initMessage.Host != "localhost" || initMessage.Port != 22 {
			serverErr <- errors.New("wrong proxy init target")
			return
		}
		if err := ws.WriteJSON(struct {
			Status string `json:"status"`
			Target string `json:"target"`
		}{Status: "connected", Target: "localhost:22"}); err != nil {
			serverErr <- err
			return
		}

		messageType, payload, err = ws.ReadMessage()
		if err != nil {
			serverErr <- err
			return
		}
		if messageType != websocket.BinaryMessage || string(payload) != "client payload" {
			serverErr <- errors.New("client payload was not one binary frame")
			return
		}
		if err := ws.WriteMessage(websocket.TextMessage, []byte(`{"notice":"ignored"}`)); err != nil {
			serverErr <- err
			return
		}
		if err := ws.WriteMessage(websocket.BinaryMessage, []byte("server payload")); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}))
	defer server.Close()

	conn, err := dialProxy(context.Background(), testWSSDialer(server), "wss"+strings.TrimPrefix(server.URL, "https"), token, spriteName)
	if err != nil {
		t.Fatalf("dialProxy() error = %v", err)
	}
	defer func() { _ = conn.Close() }()

	if got := conn.RemoteAddr().String(); got != "localhost:22" {
		t.Fatalf("RemoteAddr() = %q, want localhost:22", got)
	}
	if n, err := conn.Write([]byte("client payload")); err != nil || n != len("client payload") {
		t.Fatalf("Write() = (%d, %v), want (%d, nil)", n, err, len("client payload"))
	}
	got, err := io.ReadAll(io.LimitReader(conn, int64(len("server payload"))))
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if string(got) != "server payload" {
		t.Fatalf("Read() = %q, want server payload", got)
	}

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not finish proxy exchange")
	}
}

func TestDialProxyRequiresConnectedStatusWithoutExposingToken(t *testing.T) {
	const token = "status-secret"

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.Close() }()

		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
		_ = ws.WriteJSON(struct {
			Status string `json:"status"`
		}{Status: token})
	}))
	defer server.Close()

	conn, err := dialProxy(context.Background(), testWSSDialer(server), "wss"+strings.TrimPrefix(server.URL, "https"), token, "status-test")
	if conn != nil {
		_ = conn.Close()
		t.Fatal("dialProxy() returned a connection for a non-connected status")
	}
	if err == nil {
		t.Fatal("dialProxy() error = nil, want rejected status")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("dialProxy() error exposed token: %v", err)
	}
}

func TestDialProxyCancellationClosesHandshakeConnection(t *testing.T) {
	const token = "cancellation-secret"

	initReceived := make(chan struct{})
	connectionClosed := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.Close() }()

		if _, _, err := ws.ReadMessage(); err != nil {
			return
		}
		close(initReceived)
		if _, _, err := ws.ReadMessage(); err != nil {
			close(connectionClosed)
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := dialProxy(ctx, testWSSDialer(server), "wss"+strings.TrimPrefix(server.URL, "https"), token, "cancel-test")
		result <- err
	}()

	select {
	case <-initReceived:
	case <-time.After(time.Second):
		t.Fatal("server did not receive proxy init")
	}
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("dialProxy() error = %v, want context.Canceled", err)
		}
		if strings.Contains(err.Error(), token) {
			t.Fatalf("dialProxy() error exposed token: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("dialProxy did not return after cancellation")
	}
	select {
	case <-connectionClosed:
	case <-time.After(time.Second):
		t.Fatal("handshake WebSocket remained open after cancellation")
	}
}

func testWSSDialer(server *httptest.Server) *websocket.Dialer {
	transport := server.Client().Transport.(*http.Transport)
	config := transport.TLSClientConfig.Clone()
	config.MinVersion = tls.VersionTLS12
	return &websocket.Dialer{TLSClientConfig: config}
}

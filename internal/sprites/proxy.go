// Package sprites provides the direct transports used to reach Sprites.
package sprites

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	proxyBaseURL     = "wss://api.sprites.dev"
	proxyBufferSize  = 1 << 20
	proxyClosePeriod = time.Second
)

// DialProxy opens a connection to the SSH port of spriteName through the
// Sprites proxy endpoint. The context governs both the WebSocket upgrade and
// the proxy protocol handshake.
func DialProxy(ctx context.Context, token, spriteName string) (net.Conn, error) {
	dialer := &websocket.Dialer{
		ReadBufferSize:  proxyBufferSize,
		WriteBufferSize: proxyBufferSize,
	}
	return dialProxy(ctx, dialer, proxyBaseURL, token, spriteName)
}

func dialProxy(
	ctx context.Context,
	dialer *websocket.Dialer,
	baseURL string,
	token string,
	spriteName string,
) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dialer == nil {
		return nil, errors.New("sprites proxy: nil WebSocket dialer")
	}

	endpoint, err := buildProxyURL(baseURL, spriteName)
	if err != nil {
		return nil, err
	}
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+token)

	ws, response, err := dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, errors.New("sprites proxy: WebSocket upgrade failed")
	}

	conn, err := proxyHandshake(ctx, ws)
	if err != nil {
		_ = ws.Close()
		return nil, err
	}
	return conn, nil
}

func buildProxyURL(baseURL, spriteName string) (string, error) {
	if spriteName == "" || strings.ContainsAny(spriteName, "/?#") {
		return "", errors.New("sprites proxy: invalid sprite name")
	}

	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "wss" || u.Host == "" || u.User != nil {
		return "", errors.New("sprites proxy: invalid WebSocket endpoint")
	}
	u.Path = fmt.Sprintf("/v1/sprites/%s/proxy", spriteName)
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

type proxyInitMessage struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type proxyResponseMessage struct {
	Status string `json:"status"`
	Target string `json:"target"`
}

func proxyHandshake(ctx context.Context, ws *websocket.Conn) (*proxyConn, error) {
	watcherStop := make(chan struct{})
	watcherStopped := make(chan struct{})
	go func() {
		defer close(watcherStopped)
		select {
		case <-ctx.Done():
			_ = ws.Close()
		case <-watcherStop:
		}
	}()

	var response proxyResponseMessage
	handshakeErr := ws.WriteJSON(proxyInitMessage{Host: "localhost", Port: 22})
	if handshakeErr == nil {
		handshakeErr = ws.ReadJSON(&response)
	}

	close(watcherStop)
	<-watcherStopped

	if err := ctx.Err(); err != nil {
		_ = ws.Close()
		return nil, err
	}
	if handshakeErr != nil {
		return nil, errors.New("sprites proxy: protocol handshake failed")
	}
	if response.Status != "connected" {
		return nil, errors.New("sprites proxy: proxy did not connect")
	}

	return &proxyConn{
		ws: ws,
		remote: proxyAddr{
			network: "tcp",
			address: response.Target,
		},
	}, nil
}

// proxyConn maps each Write to one binary WebSocket frame and reads binary
// frame payloads as one byte stream. Text frames after the handshake are
// protocol notices and do not enter the stream.
type proxyConn struct {
	ws     *websocket.Conn
	remote proxyAddr

	readMu     sync.Mutex
	readReader io.Reader
	writeMu    sync.Mutex
	closeOnce  sync.Once
	closeErr   error
}

func (c *proxyConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	c.readMu.Lock()
	defer c.readMu.Unlock()

	for {
		if c.readReader != nil {
			n, err := c.readReader.Read(p)
			if errors.Is(err, io.EOF) {
				c.readReader = nil
				if n > 0 {
					return n, nil
				}
				continue
			}
			return n, err
		}

		messageType, reader, err := c.ws.NextReader()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return 0, io.EOF
			}
			return 0, err
		}
		if messageType == websocket.BinaryMessage {
			c.readReader = reader
		}
	}
}

func (c *proxyConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if err := c.ws.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *proxyConn) Close() error {
	c.closeOnce.Do(func() {
		deadline := time.Now().Add(proxyClosePeriod)
		_ = c.ws.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			deadline,
		)
		c.closeErr = c.ws.Close()
	})
	return c.closeErr
}

func (*proxyConn) LocalAddr() net.Addr { return nil }

func (c *proxyConn) RemoteAddr() net.Addr { return c.remote }

func (c *proxyConn) SetDeadline(deadline time.Time) error {
	if err := c.SetReadDeadline(deadline); err != nil {
		return err
	}
	return c.SetWriteDeadline(deadline)
}

func (c *proxyConn) SetReadDeadline(deadline time.Time) error {
	return c.ws.SetReadDeadline(deadline)
}

func (c *proxyConn) SetWriteDeadline(deadline time.Time) error {
	return c.ws.SetWriteDeadline(deadline)
}

type proxyAddr struct {
	network string
	address string
}

func (a proxyAddr) Network() string { return a.network }

func (a proxyAddr) String() string { return a.address }

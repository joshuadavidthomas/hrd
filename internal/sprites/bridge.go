package sprites

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
)

type bridge struct {
	client      *Client
	name        string
	ctx         context.Context
	cancel      context.CancelFunc
	listener    net.Listener
	once        sync.Once
	mu          sync.Mutex
	closing     bool
	connections map[net.Conn]struct{}
	workers     sync.WaitGroup
}

func (c *Client) openBridge(name, fingerprint string) (Prepared, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return Prepared{}, context.Canceled
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return Prepared{}, fmt.Errorf("listen for Sprite SSH bridge: %w", err)
	}
	ctx, cancel := context.WithCancel(c.root)
	b := &bridge{client: c, name: name, ctx: ctx, cancel: cancel, listener: listener, connections: map[net.Conn]struct{}{}}
	c.bridges[b] = struct{}{}
	b.workers.Add(1)
	go b.accept()
	return Prepared{Port: listener.Addr().(*net.TCPAddr).Port, Fingerprint: fingerprint, close: b.Close}, nil
}
func (c *Client) closeAll() {
	c.mu.Lock()
	c.closing = true
	bridges := make([]*bridge, 0, len(c.bridges))
	for b := range c.bridges {
		bridges = append(bridges, b)
	}
	c.mu.Unlock()
	for _, b := range bridges {
		_ = b.Close()
	}
}
func (b *bridge) Close() error {
	b.once.Do(func() {
		b.cancel()
		_ = b.listener.Close()
		b.mu.Lock()
		b.closing = true
		for conn := range b.connections {
			_ = conn.Close()
		}
		b.mu.Unlock()
		b.workers.Wait()
		b.client.mu.Lock()
		delete(b.client.bridges, b)
		b.client.mu.Unlock()
	})
	return nil
}
func (b *bridge) accept() {
	defer b.workers.Done()
	for {
		local, err := b.listener.Accept()
		if err != nil {
			return
		}
		if !b.track(local) {
			_ = local.Close()
			continue
		}
		b.workers.Add(1)
		go b.relay(local)
	}
}
func (b *bridge) relay(local net.Conn) {
	defer b.workers.Done()
	defer b.untrack(local)
	defer func() { _ = local.Close() }()
	remote, err := DialProxy(b.ctx, b.client.token, b.name)
	if err != nil {
		return
	}
	if !b.track(remote) {
		_ = remote.Close()
		return
	}
	defer b.untrack(remote)
	defer func() { _ = remote.Close() }()
	var copies sync.WaitGroup
	copies.Add(2)
	go func() { defer copies.Done(); _, _ = io.Copy(remote, local); _ = remote.Close() }()
	go func() { defer copies.Done(); _, _ = io.Copy(local, remote); _ = local.Close() }()
	copies.Wait()
}
func (b *bridge) track(conn net.Conn) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing {
		return false
	}
	b.connections[conn] = struct{}{}
	return true
}
func (b *bridge) untrack(conn net.Conn) { b.mu.Lock(); delete(b.connections, conn); b.mu.Unlock() }

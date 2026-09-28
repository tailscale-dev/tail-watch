package main

import (
	"context"
	"net"
	"sync"

	"tailscale.com/client/local"
)

// fakeEnv is the test network. Dial and ListenPacket hit loopback
// listeners unless a test overrides them.
type fakeEnv struct {
	dialer    net.Dialer
	listen    net.ListenConfig
	client    local.Client
	unpriv    bool
	listenErr error
	dial      func(ctx context.Context, network, addr string) (net.Conn, error)

	mu         sync.Mutex
	lastListen string
}

func (f *fakeEnv) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if f.dial != nil {
		return f.dial(ctx, network, addr)
	}
	return f.dialer.DialContext(ctx, network, addr)
}

func (f *fakeEnv) ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error) {
	f.mu.Lock()
	f.lastListen = network
	f.mu.Unlock()
	if f.listenErr != nil {
		return nil, f.listenErr
	}
	return f.listen.ListenPacket(ctx, network, addr)
}

func (f *fakeEnv) LocalClient() *local.Client { return &f.client }

func (f *fakeEnv) UnprivilegedICMP() bool { return f.unpriv }

var _ netEnv = (*fakeEnv)(nil)

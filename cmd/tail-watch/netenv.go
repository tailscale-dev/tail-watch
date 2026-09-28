package main

import (
	"context"
	"net"

	"golang.org/x/net/icmp"
	"tailscale.com/client/local"
)

// netEnv is how the collector reaches the network and the local tailscaled.
type netEnv interface {
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
	ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error)
	LocalClient() *local.Client
	// UnprivilegedICMP reports whether ICMP checks should open udp4/udp6
	// datagram sockets instead of raw ICMP sockets.
	UnprivilegedICMP() bool
}

// kernelEnv uses the host network stack and a LocalAPI client.
type kernelEnv struct {
	dialer     net.Dialer
	listen     net.ListenConfig
	client     local.Client
	unprivICMP bool
}

func newKernelEnv(socket string) *kernelEnv {
	env := new(kernelEnv)
	env.unprivICMP = detectUnprivilegedICMP()
	env.client.Socket = socket
	env.client.UseSocketOnly = true
	return env
}

func (e *kernelEnv) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	return e.dialer.DialContext(ctx, network, addr)
}

func (e *kernelEnv) ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error) {
	return e.listen.ListenPacket(ctx, network, addr)
}

func (e *kernelEnv) LocalClient() *local.Client { return &e.client }

func (e *kernelEnv) UnprivilegedICMP() bool { return e.unprivICMP }

func detectUnprivilegedICMP() bool {
	c, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

var _ netEnv = (*kernelEnv)(nil)

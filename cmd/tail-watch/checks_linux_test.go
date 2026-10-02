//go:build linux

package main

import (
	"context"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestTCPTimeoutNonAcceptingListener(t *testing.T) {
	t.Parallel()
	host, port, cleanup := listenFull(t)
	defer cleanup()
	env := new(fakeEnv)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := tcpCheck{port: port}.Run(ctx, env, &Target{Dynamic: false, Name: "slow", Addr: host,
		Site:             "",
		Severity:         "",
		Parent:           "",
		InsecureTLS:      false,
		Interval:         0,
		Timeout:          0,
		FailThreshold:    0,
		RecoverThreshold: 0,
		FlapLimit:        0,
		FlapWindow:       0,
		Network:          "",
		Checks:           nil,
		Implicit:         false,
		StableID:         "",
		NodeID:           0, Route: "",
	})
	if time.Since(start) > time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if err == nil {
		t.Fatal("expected the dial to fail")
	}
}

func listenFull(t *testing.T) (string, int, func()) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	if err = syscall.Bind(fd, &syscall.SockaddrInet4{Port: 0, Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	if err = syscall.Listen(fd, 0); err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		_ = syscall.Close(fd)
		t.Fatal(err)
	}
	port := sa.(*syscall.SockaddrInet4).Port
	var held []net.Conn
	for range 8 {
		c, dialErr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 20*time.Millisecond)
		if dialErr != nil {
			break
		}
		held = append(held, c)
	}
	return "127.0.0.1", port, func() {
		for _, c := range held {
			_ = c.Close()
		}
		_ = syscall.Close(fd)
	}
}

package runner

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

type forwardingFunc func(context.Context, string, string) (net.Conn, error)

func (f forwardingFunc) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return f(ctx, network, addr)
}

func TestDialWARPConnectsToTargetSSHViaLoopbackSOCKS(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		expected := []byte{5, 1, 0}
		actual := make([]byte, len(expected))
		if _, err := io.ReadFull(server, actual); err != nil {
			done <- err
			return
		}
		if !bytes.Equal(actual, expected) {
			t.Errorf("greeting = %v", actual)
		}
		if _, err := server.Write([]byte{5, 0}); err != nil {
			done <- err
			return
		}
		// SOCKS5 CONNECT to 203.0.113.22:2222.
		expected = []byte{5, 1, 0, 1, 203, 0, 113, 22, 8, 174}
		actual = make([]byte, len(expected))
		if _, err := io.ReadFull(server, actual); err != nil {
			done <- err
			return
		}
		if !bytes.Equal(actual, expected) {
			t.Errorf("CONNECT request = %v", actual)
		}
		_, err := server.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		done <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bridge := forwardingFunc(func(_ context.Context, network, addr string) (net.Conn, error) {
		if network != "tcp" || addr != "127.0.0.1:40000" {
			t.Fatalf("forwarded to %s %s", network, addr)
		}
		return client, nil
	})
	conn, err := dialWARP(ctx, bridge, "203.0.113.22:2222")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

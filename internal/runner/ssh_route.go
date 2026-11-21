package runner

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/sud0-i/KVN-Bridge-Panel/internal/protocol"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

// SSHRoute selects the target SSH port and, optionally, a bridge's local WARP proxy.
type SSHRoute struct {
	Port       int
	OpenBridge func() (*ssh.Client, error)
}

func (r SSHRoute) PortString() string {
	if r.Port == 0 {
		return "22"
	}
	return strconv.Itoa(r.Port)
}

type existingConn struct{ net.Conn }

func (c existingConn) Dial(_, _ string) (net.Conn, error) { return c.Conn, nil }

type contextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

func dialWARP(ctx context.Context, bridge contextDialer, target string) (net.Conn, error) {
	socks := net.JoinHostPort("127.0.0.1", strconv.Itoa(protocol.WarpProxyPort))
	conn, err := bridge.DialContext(ctx, "tcp", socks)
	if err != nil {
		return nil, fmt.Errorf("SOCKS WARP на мосту: %w", err)
	}
	// SSH forwarding channels don't implement deadlines. Closing this channel on
	// timeout also unblocks a SOCKS handshake that received no reply.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	dialer, err := proxy.SOCKS5("tcp", socks, nil, existingConn{conn})
	if err == nil {
		var tunneled net.Conn
		tunneled, err = dialer.Dial("tcp", target)
		if err == nil && ctx.Err() == nil {
			return tunneled, nil
		}
		if err == nil {
			err = ctx.Err()
		}
	}
	conn.Close()
	return nil, fmt.Errorf("SSH %s через WARP: %w", target, err)
}

type bridgeConn struct {
	net.Conn
	bridge *ssh.Client
}

func (c bridgeConn) Close() error {
	_ = c.bridge.Close()
	return c.Conn.Close()
}

// Dial reaches an SSH target by the configured route. The returned connection
// owns the SSH session to the bridge when the route uses WARP.
func (r SSHRoute) Dial(ctx context.Context, target string) (net.Conn, error) {
	if r.OpenBridge == nil {
		return (&net.Dialer{}).DialContext(ctx, "tcp", target)
	}
	bridge, err := r.OpenBridge()
	if err != nil {
		return nil, err
	}
	conn, err := dialWARP(ctx, bridge, target)
	if err != nil {
		bridge.Close()
		return nil, err
	}
	return bridgeConn{conn, bridge}, nil
}

// ansibleTarget exposes a private, temporary relay for OpenSSH in the panel.
func (r SSHRoute) ansibleTarget(ctx context.Context, ip string) (string, string, func(), error) {
	if r.OpenBridge == nil {
		return ip, r.PortString(), func() {}, nil
	}
	bridge, err := r.OpenBridge()
	if err != nil {
		return "", "", nil, err
	}
	target := net.JoinHostPort(ip, r.PortString())
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	probe, err := dialWARP(probeCtx, bridge, target)
	cancel()
	if err != nil {
		bridge.Close()
		return "", "", nil, err
	}
	probe.Close()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		bridge.Close()
		return "", "", nil, err
	}
	closeRelay := func() {
		listener.Close()
		bridge.Close()
	}
	go func() {
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer local.Close()
				dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				remote, err := dialWARP(dialCtx, bridge, target)
				cancel()
				if err != nil {
					return
				}
				defer remote.Close()
				done := make(chan struct{})
				go func() {
					io.Copy(remote, local)
					remote.Close()
					close(done)
				}()
				io.Copy(local, remote)
				local.Close()
				<-done
			}()
		}
	}()
	return "127.0.0.1", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port), closeRelay, nil
}

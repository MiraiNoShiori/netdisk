package nfsgateway

import (
	"context"
	"fmt"
	"github.com/go-git/go-billy/v5"
	nfs "github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs/helpers"
	"net"
	"strings"
)

// allowListener 在连接层检查来源，避免猜到文件句柄绕过 mount 检查。
type allowListener struct {
	net.Listener
	nets []*net.IPNet
}

func (l *allowListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		host, _, err := net.SplitHostPort(c.RemoteAddr().String())
		allowed := false
		if err == nil {
			ip := net.ParseIP(host)
			for _, n := range l.nets {
				if n.Contains(ip) {
					allowed = true
					break
				}
			}
		}
		if allowed {
			return c, nil
		}
		c.Close()
	}
}
func Listen(addr, cidrs string) (net.Listener, error) {
	nets := []*net.IPNet{}
	for _, raw := range strings.Split(cidrs, ",") {
		_, n, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("NFS client CIDR: %w", err)
		}
		nets = append(nets, n)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &allowListener{l, nets}, nil
}
func Serve(ctx context.Context, l net.Listener, fs billy.Filesystem) error {
	go func() { <-ctx.Done(); l.Close() }()
	return nfs.Serve(l, helpers.NewCachingHandler(helpers.NewNullAuthHandler(fs), 4096))
}

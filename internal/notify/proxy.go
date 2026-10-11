package notify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// DevProxy 是开发环境的出口代理：运营商与短信供应商按出口 IP 做白名单，
// 开发机的出口不在名单里，统一从这台代理出去。
const DevProxy = "socks5://10.1.1.4:41282"

const dialTimeout = 10 * time.Second

// egressProxy 为 nil 时直连。只在启动时由 UseProxy 写一次，之后只读。
var egressProxy *url.URL

// UseProxy 让本包发起的全部网络连接（HTTP 接口、SMTP、阿里云 SDK）都走这个 SOCKS5 代理。
// 必须在第一次发送之前调用，不并发安全。
func UseProxy(raw string) error {
	u, err := url.Parse(raw)
	// 只收 socks5：阿里云 SDK 的代理入口只认它，SMTP 也没法走 HTTP 代理。
	if err != nil || u.Scheme != "socks5" || u.Host == "" || u.User != nil {
		return errors.New("notify: 代理地址必须形如 socks5://host:port（不支持带认证）")
	}
	egressProxy = u
	return nil
}

func proxyFunc(req *http.Request) (*url.URL, error) {
	if egressProxy != nil {
		return egressProxy, nil
	}
	return http.ProxyFromEnvironment(req)
}

// dialContext 给不走 net/http 的连接（SMTP）用。
func dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout}
	if egressProxy == nil {
		return d.DialContext(ctx, network, addr)
	}
	conn, err := d.DialContext(ctx, "tcp", egressProxy.Host)
	if err != nil {
		return nil, fmt.Errorf("连接代理: %w", err)
	}
	// 握手期间 ctx 取消就关连接，卡在读写里的握手立刻返回。
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	err = socks5Connect(conn, addr)
	if !stop() && err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// socks5Connect 在 conn 上做无认证的 SOCKS5 CONNECT（RFC 1928）。目标一律按域名
// 发给代理、由代理解析：白名单看的是代理的出口，解析也该在那一侧。
//
// 手写而不是用 golang.org/x/net/proxy：它不在依赖白名单里，而这里只需要这一条命令。
func socks5Connect(conn net.Conn, addr string) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 || len(host) == 0 || len(host) > 255 {
		return errors.New("socks5: 目标地址不合法")
	}
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return fmt.Errorf("socks5: %w", err)
	}
	buf := make([]byte, 262)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return fmt.Errorf("socks5: %w", err)
	}
	if buf[0] != 5 || buf[1] != 0 {
		return errors.New("socks5: 代理不接受无认证方式")
	}
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, host...)
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		return fmt.Errorf("socks5: %w", err)
	}
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return fmt.Errorf("socks5: %w", err)
	}
	if buf[1] != 0 {
		return fmt.Errorf("socks5: 代理拒绝连接 rep=%d", buf[1])
	}
	// 应答里的绑定地址用不上，但必须读完，否则会混进后面的数据流。
	var n int
	switch buf[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			return fmt.Errorf("socks5: %w", err)
		}
		n = int(buf[0])
	default:
		return errors.New("socks5: 应答格式不合法")
	}
	if _, err := io.ReadFull(conn, buf[:n+2]); err != nil {
		return fmt.Errorf("socks5: %w", err)
	}
	return nil
}

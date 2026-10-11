package notify

import (
	"context"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/alibabacloud-go/tea/tea"
)

// socks5Stub 是只会无认证 CONNECT 的最小 SOCKS5 服务器：记下 client 要连的地址，
// 然后一律接到 backend 上。client 要连的主机名根本解析不了，所以请求能通就说明
// 连接确实是从代理出去的，而且域名交给了代理去解析。
func socks5Stub(t *testing.T, backend string) (proxyURL string, requested <-chan string) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lis.Close() })
	got := make(chan string, 4)
	go func() {
		for {
			c, err := lis.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 262)
				if _, err := io.ReadFull(c, buf[:2]); err != nil {
					return
				}
				if _, err := io.ReadFull(c, buf[:buf[1]]); err != nil {
					return
				}
				_, _ = c.Write([]byte{5, 0})
				// VER CMD RSV ATYP=3(域名) LEN
				if _, err := io.ReadFull(c, buf[:5]); err != nil || buf[3] != 3 {
					return
				}
				n := int(buf[4])
				if _, err := io.ReadFull(c, buf[:n+2]); err != nil {
					return
				}
				got <- net.JoinHostPort(string(buf[:n]), strconv.Itoa(int(buf[n])<<8|int(buf[n+1])))
				up, err := net.Dial("tcp", backend)
				if err != nil {
					return
				}
				defer up.Close()
				_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return "socks5://" + lis.Addr().String(), got
}

func useProxyForTest(t *testing.T, raw string) {
	t.Helper()
	if err := UseProxy(raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		egressProxy = nil
		httpClient.CloseIdleConnections()
	})
}

func TestUseProxyOnlyAcceptsSocks5(t *testing.T) {
	t.Cleanup(func() { egressProxy = nil })
	for _, bad := range []string{"", "10.1.1.4:41282", "http://10.1.1.4:41282", "socks5://"} {
		if err := UseProxy(bad); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
	if err := UseProxy(DevProxy); err != nil {
		t.Fatalf("DevProxy: %v", err)
	}
}

func TestHTTPProvidersGoThroughProxy(t *testing.T) {
	srv, got := capturingServer(t, 200, `{"code":200}`)
	proxyURL, requested := socks5Stub(t, strings.TrimPrefix(srv.URL, "http://"))
	useProxyForTest(t, proxyURL)

	p := zhutongAt("http://sms.invalid:8080")
	if err := p.Send(context.Background(), Delivery{To: "13800138000", ProviderTemplateID: "1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if addr := <-requested; addr != "sms.invalid:8080" {
		t.Fatalf("代理收到的目标 = %s", addr)
	}
	if got.path != "/v2/sendSmsTp" {
		t.Fatalf("请求没有到达后端: %+v", got)
	}
}

func TestSMTPDialGoesThroughProxy(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { backend.Close() })
	go func() {
		c, err := backend.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("220 hi\r\n"))
		c.Close()
	}()
	proxyURL, requested := socks5Stub(t, backend.Addr().String())
	useProxyForTest(t, proxyURL)

	conn, err := dialContext(context.Background(), "tcp", "smtp.invalid:465")
	if err != nil {
		t.Fatalf("dialContext: %v", err)
	}
	defer conn.Close()
	if addr := <-requested; addr != "smtp.invalid:465" {
		t.Fatalf("代理收到的目标 = %s", addr)
	}
	if b, _ := io.ReadAll(conn); string(b) != "220 hi\r\n" {
		t.Fatalf("没有读到后端的问候: %q", b)
	}
}

// 走 SOCKS5 时 SDK 按毫秒读 ConnectTimeout：照旧填 5 的话连接超时只有 5 毫秒。
func TestNewAliyunWithProxy(t *testing.T) {
	useProxyForTest(t, DevProxy)
	p, err := aliyunSpec.New(Config{"accessKeyId": "ak", "accessKeySecret": "sk", "signName": "示例签名"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := p.(*aliyunProvider).client
	if u, _ := url.Parse(tea.StringValue(c.Socks5Proxy)); u == nil || u.String() != DevProxy || tea.StringValue(c.Socks5NetWork) != "tcp" {
		t.Fatalf("socks5 = %q / %q", tea.StringValue(c.Socks5Proxy), tea.StringValue(c.Socks5NetWork))
	}
	if ms := tea.IntValue(c.ConnectTimeout); ms != 5000 {
		t.Fatalf("ConnectTimeout = %d, want 5000（毫秒）", ms)
	}
}

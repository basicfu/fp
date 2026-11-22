package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/basicfu/fp/internal/domain"
)

// WechatAvatarHosts 是微信 headimgurl 所在的域名。
var WechatAvatarHosts = []string{"thirdwx.qlogo.cn", "wx.qlogo.cn"}

// avatarFetchTimeout 卡得很短：下载发生在登录 / 绑定请求里，宁可这次不补头像也不能拖慢登录。
const avatarFetchTimeout = 3 * time.Second

// AvatarFetcher 下载三方资料里的头像。
//
// 只访问白名单域名：头像地址来自三方接口的返回，不限制的话就成了让 fp 去请求任意地址（SSRF）的入口。
type AvatarFetcher struct {
	hosts  map[string]bool
	client *http.Client
}

// NewAvatarFetcher 构造只从 hosts 下载的 AvatarFetcher。测试用它放行 httptest 的地址。
func NewAvatarFetcher(hosts ...string) *AvatarFetcher {
	f := &AvatarFetcher{hosts: map[string]bool{}}
	for _, h := range hosts {
		f.hosts[h] = true
	}
	f.client = &http.Client{
		Timeout: avatarFetchTimeout,
		// 跳转目标同样要过白名单，否则白名单域名上的一次 302 就绕过去了。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("跳转次数过多")
			}
			return f.check(req.URL)
		},
	}
	return f
}

func (f *AvatarFetcher) check(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("不支持的协议 %q", u.Scheme)
	}
	if !f.hosts[u.Hostname()] {
		return fmt.Errorf("域名 %q 不在头像白名单内", u.Hostname())
	}
	return nil
}

// Fetch 下载并校验头像，返回的字节可直接存为头像。
func (f *AvatarFetcher) Fetch(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if err := f.check(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); !strings.HasPrefix(mt, "image/") {
		return nil, fmt.Errorf("不是图片：Content-Type=%q", resp.Header.Get("Content-Type"))
	}
	img, err := io.ReadAll(io.LimitReader(resp.Body, domain.AvatarMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if _, err := domain.CheckAvatar(img); err != nil {
		return nil, err
	}
	return img, nil
}

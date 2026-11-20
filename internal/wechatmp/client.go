// Package wechatmp 是微信服务号（公众号）接口的最小客户端：调用凭证、带参二维码、用户信息、模板消息，
// 以及服务器配置里事件推送的验签与加解密（message.go）。
//
// 供通知中心的 wechat_mp 供应商与服务号订阅共用；凭据来自同一个供应商实例。
package wechatmp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// DefaultAPIBase 是微信接口的域名。
const DefaultAPIBase = "https://api.weixin.qq.com"

// 微信的几个要单独认出来的错误码。
const (
	// ErrCodeNotFollowing：用户没关注（或已取关）服务号，模板消息发不出去。
	ErrCodeNotFollowing = 43004
	errCodeTokenInvalid = 40001
	errCodeTokenExpired = 42001
	errCodeTokenMissing = 41001
	errCodeTokenBadForm = 40014
)

// APIError 是微信接口返回的 errcode 非 0。Msg 是微信的原始说明，只适合进日志。
type APIError struct {
	Op   string
	Code int
	Msg  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("wechat_mp %s: errcode=%d %s", e.Op, e.Code, e.Msg)
}

// IsNotFollowing 报告 err 是否是"用户没关注服务号"。
func IsNotFollowing(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == ErrCodeNotFollowing
}

// Config 是一个服务号的接口凭据。
type Config struct {
	AppID     string
	AppSecret string
	// APIBase 为空时取 DefaultAPIBase；测试指向 httptest。
	APIBase string
	// HTTP 为空时用 10 秒超时的默认客户端。fp 里传通知模块的客户端：开发环境要走出口代理，
	// 服务号接口按出口 IP 白名单放行。
	HTTP *http.Client
}

// Client 是一个服务号的接口客户端，并发安全。
type Client struct {
	cfg Config

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// New 构造客户端。
func New(cfg Config) *Client {
	if cfg.APIBase == "" {
		cfg.APIBase = DefaultAPIBase
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{cfg: cfg}
}

// AppID 是服务号的 AppID。
func (c *Client) AppID() string { return c.cfg.AppID }

// tokenRefreshMargin 是凭证到期前提前换新的余量：微信给的 7200 秒是从它签发时算的，
// 网络往返与时钟误差都要留出来。
const tokenRefreshMargin = 5 * time.Minute

// accessToken 取调用凭证。
//
// 用 stable_token 而不是 token 接口：后者每取一次都会让旧凭证 5 分钟后作废，fp 多实例各自取凭证会互相顶掉；
// stable_token 在有效期内反复取拿到的是同一个，多实例、多进程各自缓存也互不影响，不需要共享缓存。
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && time.Now().Before(c.expiresAt) {
		t := c.token
		c.mu.Unlock()
		return t, nil
	}
	c.mu.Unlock()

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	body := map[string]any{"grant_type": "client_credential", "appid": c.cfg.AppID, "secret": c.cfg.AppSecret}
	if err := c.do(ctx, http.MethodPost, "/cgi-bin/stable_token", nil, body, &out); err != nil {
		return "", fmt.Errorf("wechat_mp 取调用凭证: %w", err)
	}
	if out.ErrCode != 0 || out.AccessToken == "" {
		return "", &APIError{Op: "取调用凭证", Code: out.ErrCode, Msg: out.ErrMsg}
	}
	ttl := time.Duration(out.ExpiresIn)*time.Second - tokenRefreshMargin
	if ttl < time.Minute {
		ttl = time.Minute
	}
	c.mu.Lock()
	c.token, c.expiresAt = out.AccessToken, time.Now().Add(ttl)
	c.mu.Unlock()
	return out.AccessToken, nil
}

func (c *Client) dropToken(stale string) {
	c.mu.Lock()
	if c.token == stale {
		c.token = ""
	}
	c.mu.Unlock()
}

// errResp 是各接口共有的错误字段。errcode 用指针：缺字段（被网关拦下的响应之类）不能当成功。
type errResp struct {
	ErrCode *int   `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// call 带凭证调一个接口。凭证被微信判无效时（AppSecret 重置过之类）换新再试一次。
// out 必须内嵌 errResp 的两个字段；requireErrCode 为 true 时响应里必须有 errcode（发消息这类接口成功也会回 0）。
func (c *Client) call(ctx context.Context, op, method, path string, query url.Values, body any, out any, requireErrCode bool) error {
	for attempt := 0; ; attempt++ {
		tok, err := c.accessToken(ctx)
		if err != nil {
			return err
		}
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("access_token", tok)
		var raw json.RawMessage
		if err := c.do(ctx, method, path, q, body, &raw); err != nil {
			return fmt.Errorf("wechat_mp %s: %w", op, err)
		}
		var er errResp
		if err := json.Unmarshal(raw, &er); err != nil {
			return fmt.Errorf("wechat_mp %s: 响应不是合法 JSON", op)
		}
		if er.ErrCode != nil && *er.ErrCode != 0 {
			switch *er.ErrCode {
			case errCodeTokenInvalid, errCodeTokenExpired, errCodeTokenMissing, errCodeTokenBadForm:
				c.dropToken(tok)
				if attempt == 0 {
					continue
				}
			}
			return &APIError{Op: op, Code: *er.ErrCode, Msg: er.ErrMsg}
		}
		if er.ErrCode == nil && requireErrCode {
			return fmt.Errorf("wechat_mp %s: 响应缺少 errcode", op)
		}
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("wechat_mp %s: 解析响应: %w", op, err)
			}
		}
		return nil
	}
}

// do 发请求、解 JSON。返回的错误里不带 URL：access_token 在 query 里。
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	u := c.cfg.APIBase + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return errors.New("构造请求失败")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// QRCode 是一张临时带参二维码。
type QRCode struct {
	// Ticket 换二维码图片用：ImageURL 就是拿它拼的。
	Ticket string
	// ImageURL 是微信托管的二维码图片地址，可以直接放进 <img>。
	ImageURL string
	// Content 是二维码里编码的内容，业务方想自己画二维码（换颜色、加 logo）时用它。
	Content   string
	ExpiresIn time.Duration
}

// CreateSceneQR 生成临时带参二维码（字符串场景值，最长 64 字节）。用户扫码后，
// 没关注的先关注、推 subscribe 事件，已关注的推 SCAN 事件，两者都带着场景值。
func (c *Client) CreateSceneQR(ctx context.Context, scene string, ttl time.Duration) (*QRCode, error) {
	var out struct {
		Ticket        string `json:"ticket"`
		ExpireSeconds int    `json:"expire_seconds"`
		URL           string `json:"url"`
	}
	body := map[string]any{
		"expire_seconds": int(ttl.Seconds()),
		"action_name":    "QR_STR_SCENE",
		"action_info":    map[string]any{"scene": map[string]any{"scene_str": scene}},
	}
	if err := c.call(ctx, "生成带参二维码", http.MethodPost, "/cgi-bin/qrcode/create", nil, body, &out, false); err != nil {
		return nil, err
	}
	if out.Ticket == "" {
		return nil, errors.New("wechat_mp 生成带参二维码: 响应缺少 ticket")
	}
	return &QRCode{
		Ticket:    out.Ticket,
		ImageURL:  "https://mp.weixin.qq.com/cgi-bin/showqrcode?ticket=" + url.QueryEscape(out.Ticket),
		Content:   out.URL,
		ExpiresIn: time.Duration(out.ExpireSeconds) * time.Second,
	}, nil
}

// Follower 是用户信息接口里 fp 用得到的部分。昵称头像这个接口早已不再返回。
type Follower struct {
	Subscribed bool
	UnionID    string
}

// UserInfo 查一个 openid 的关注状态与 unionid（服务号挂在开放平台账号下才有 unionid）。
func (c *Client) UserInfo(ctx context.Context, openid string) (*Follower, error) {
	var out struct {
		Subscribe int    `json:"subscribe"`
		UnionID   string `json:"unionid"`
	}
	q := url.Values{"openid": {openid}, "lang": {"zh_CN"}}
	if err := c.call(ctx, "查询用户信息", http.MethodGet, "/cgi-bin/user/info", q, nil, &out, false); err != nil {
		return nil, err
	}
	return &Follower{Subscribed: out.Subscribe == 1, UnionID: out.UnionID}, nil
}

// TemplateMessage 是一条模板消息。
type TemplateMessage struct {
	ToUser     string
	TemplateID string
	// URL 可选：用户点开消息后跳转的网页。
	URL string
	// Data 是模板字段的取值，键是模板里的字段名（thing1、time2 之类）。
	Data map[string]string
}

// SendTemplate 发一条模板消息。用户没关注时返回的错误满足 IsNotFollowing。
func (c *Client) SendTemplate(ctx context.Context, m TemplateMessage) error {
	data := make(map[string]map[string]string, len(m.Data))
	for k, v := range m.Data {
		data[k] = map[string]string{"value": v}
	}
	body := map[string]any{"touser": m.ToUser, "template_id": m.TemplateID, "data": data}
	if m.URL != "" {
		body["url"] = m.URL
	}
	return c.call(ctx, "发送模板消息", http.MethodPost, "/cgi-bin/message/template/send", nil, body, nil, true)
}

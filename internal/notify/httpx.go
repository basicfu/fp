package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
)

const (
	httpTimeout      = 10 * time.Second
	maxResponseBytes = 64 << 10
)

// httpClient 不跟随重定向：重定向会把签名头、query 里的密钥带去别的主机，
// 而 3xx 本来就不是这些接口的成功响应。
var httpClient = &http.Client{
	Timeout:       httpTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// doHTTP 发请求，返回状态码与响应体（最多 64KB）。
//
// 返回的错误里**不带 URL**：这些接口的 token / key 就在 URL 里，*url.Error 默认会把整条
// URL 打进错误文本，进而进 notify_log 与服务端日志。
func doHTTP(ctx context.Context, req *http.Request) (int, []byte, error) {
	resp, err := httpClient.Do(req.WithContext(ctx))
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// newRequest 同样不让 URL 出现在错误里（http.NewRequest 解析失败时会带上它）。
func newRequest(method, rawURL string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, errors.New("构造请求失败：URL 不合法")
	}
	return req, nil
}

func postJSON(ctx context.Context, rawURL string, payload any) (int, []byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := newRequest(http.MethodPost, rawURL, bytes.NewReader(b))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return doHTTP(ctx, req)
}

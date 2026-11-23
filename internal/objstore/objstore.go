// Package objstore 是对象存储的最小抽象：头像等用户上传的文件只需要上传、删除、拼公开地址。
package objstore

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/basicfu/fp/internal/config"
)

// Store 是对象存储。实现须并发安全。
type Store interface {
	Put(ctx context.Context, key, contentType string, body []byte, cacheControl string) error
	// Delete 对不存在的 key 不报错：删除会被重试，同一个 key 可能已经删过。
	Delete(ctx context.Context, key string) error
	// URL 返回 key 的公开访问地址（不访问网络）。
	URL(key string) string
}

// New 按系统配置建 Store。Bucket 为空返回 nil, nil：没配对象存储不是错误，
// 调用方据此让依赖它的接口返回 unavailable，而不是启动失败。
func New(cfg config.OSS) (Store, error) {
	if cfg.Bucket == "" {
		return nil, nil
	}
	return newAliyun(cfg)
}

// PublicURL 拼 key 的公开地址：file_url 优先（通常是 CDN），否则 https://{bucket}.{endpoint}。
func PublicURL(cfg config.OSS, key string) string {
	if cfg.FileURL != "" {
		return strings.TrimRight(cfg.FileURL, "/") + "/" + key
	}
	return "https://" + cfg.Bucket + "." + bareEndpoint(cfg.Endpoint) + "/" + key
}

// bareEndpoint 去掉协议和末尾斜杠，兼容两种 endpoint 写法。
func bareEndpoint(endpoint string) string {
	endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	return strings.TrimRight(endpoint, "/")
}

var regionRe = regexp.MustCompile(`^oss-([a-z0-9-]+?)(-internal)?\.aliyuncs\.com$`)

// regionOf 从 endpoint 推出 region（oss-cn-hangzhou.aliyuncs.com → cn-hangzhou）。
// SDK v2 签名要 region；推不出来时让用户把 endpoint 写成标准形式，不静默猜。
func regionOf(endpoint string) (string, error) {
	m := regionRe.FindStringSubmatch(bareEndpoint(endpoint))
	if m == nil {
		return "", errors.New("objstore: 无法从 endpoint 推出 region，endpoint 应形如 oss-cn-hangzhou.aliyuncs.com")
	}
	return m[1], nil
}

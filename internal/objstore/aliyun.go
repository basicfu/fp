package objstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"

	"github.com/basicfu/fp/internal/config"
)

type aliyun struct {
	cfg    config.OSS
	client *oss.Client
}

func newAliyun(cfg config.OSS) (*aliyun, error) {
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" || cfg.Endpoint == "" {
		return nil, errors.New("objstore: oss 配了 bucket 就必须同时配 access_key_id、access_key_secret、endpoint")
	}
	region, err := regionOf(cfg.Endpoint)
	if err != nil {
		return nil, err
	}
	c := oss.LoadDefaultConfig().
		WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.AccessKeySecret)).
		WithRegion(region).
		WithEndpoint(bareEndpoint(cfg.Endpoint))
	return &aliyun{cfg: cfg, client: oss.NewClient(c)}, nil
}

func (a *aliyun) Put(ctx context.Context, key, contentType string, body []byte, cacheControl string) error {
	req := &oss.PutObjectRequest{
		Bucket:      oss.Ptr(a.cfg.Bucket),
		Key:         oss.Ptr(key),
		Body:        bytes.NewReader(body),
		ContentType: oss.Ptr(contentType),
	}
	if cacheControl != "" {
		req.CacheControl = oss.Ptr(cacheControl)
	}
	if _, err := a.client.PutObject(ctx, req); err != nil {
		return fmt.Errorf("objstore: 上传 %s: %w", key, err)
	}
	return nil
}

func (a *aliyun) Delete(ctx context.Context, key string) error {
	// OSS 的 DeleteObject 对不存在的对象也返回成功。
	if _, err := a.client.DeleteObject(ctx, &oss.DeleteObjectRequest{
		Bucket: oss.Ptr(a.cfg.Bucket),
		Key:    oss.Ptr(key),
	}); err != nil {
		return fmt.Errorf("objstore: 删除 %s: %w", key, err)
	}
	return nil
}

func (a *aliyun) URL(key string) string { return PublicURL(a.cfg, key) }

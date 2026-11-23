package objstore

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/config"
)

func TestNewWithoutBucketReturnsNil(t *testing.T) {
	s, err := New(config.OSS{})
	if err != nil || s != nil {
		t.Fatalf("New(空配置) = %v, %v, want nil, nil", s, err)
	}
}

func TestNewRejectsIncompleteConfig(t *testing.T) {
	if _, err := New(config.OSS{Bucket: "b"}); err == nil {
		t.Fatal("只配了 bucket 应报错")
	}
}

func TestPublicURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.OSS
		want string
	}{
		{"file_url 带斜杠", config.OSS{Bucket: "b", Endpoint: "e.com", FileURL: "https://cdn.example.com/"}, "https://cdn.example.com/avatar/x.png"},
		{"file_url 无斜杠", config.OSS{Bucket: "b", Endpoint: "e.com", FileURL: "https://cdn.example.com"}, "https://cdn.example.com/avatar/x.png"},
		{"回落到 bucket.endpoint", config.OSS{Bucket: "b", Endpoint: "oss-cn-hangzhou.aliyuncs.com"}, "https://b.oss-cn-hangzhou.aliyuncs.com/avatar/x.png"},
		{"endpoint 带协议", config.OSS{Bucket: "b", Endpoint: "https://oss-cn-hangzhou.aliyuncs.com/"}, "https://b.oss-cn-hangzhou.aliyuncs.com/avatar/x.png"},
	} {
		if got := PublicURL(tc.cfg, "avatar/x.png"); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestMemoryPutDelete(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	if err := m.Put(ctx, "a/1.png", "image/png", []byte("x"), "public"); err != nil {
		t.Fatal(err)
	}
	o, ok := m.Get("a/1.png")
	if !ok || string(o.Body) != "x" || o.ContentType != "image/png" || o.CacheControl != "public" {
		t.Fatalf("Get = %+v, %v", o, ok)
	}
	if got := m.URL("a/1.png"); got != "https://memory.test/a/1.png" {
		t.Fatalf("URL = %q", got)
	}
	if err := m.Delete(ctx, "a/1.png"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("a/1.png"); ok {
		t.Fatal("Delete 后不应还在")
	}
	// 删不存在的 key 不是错误：GC 重试时同一个 key 可能已经删过。
	if err := m.Delete(ctx, "a/1.png"); err != nil {
		t.Fatalf("重复 Delete 应幂等: %v", err)
	}
}

func TestMemoryFailPut(t *testing.T) {
	m := NewMemory()
	m.FailPut = errors.New("boom")
	if err := m.Put(context.Background(), "k", "t", nil, ""); err == nil {
		t.Fatal("应返回注入的错误")
	}
}

func TestRegionOf(t *testing.T) {
	for in, want := range map[string]string{
		"oss-cn-hangzhou.aliyuncs.com":          "cn-hangzhou",
		"https://oss-cn-beijing.aliyuncs.com/":  "cn-beijing",
		"oss-cn-hangzhou-internal.aliyuncs.com": "cn-hangzhou",
	} {
		if got, err := regionOf(in); err != nil || got != want {
			t.Errorf("regionOf(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	if _, err := regionOf("minio.local:9000"); err == nil {
		t.Error("非阿里云 endpoint 应报错")
	}
}

func TestNewBuildsAliyunWithoutNetwork(t *testing.T) {
	s, err := New(config.OSS{AccessKeyID: "a", AccessKeySecret: "s", Bucket: "b", Endpoint: "oss-cn-hangzhou.aliyuncs.com", FileURL: "https://cdn/"})
	if err != nil || s == nil {
		t.Fatalf("New = %v, %v", s, err)
	}
	if s.URL("k") != "https://cdn/k" {
		t.Fatalf("URL = %q", s.URL("k"))
	}
}

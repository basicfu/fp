package fpsdk

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

func TestUsageRecorderKeepsLatestMinute(t *testing.T) {
	u := newUsageRecorder()
	base := time.Date(2026, 9, 13, 10, 30, 45, 0, time.UTC)
	want := base.Truncate(time.Minute).UnixMilli()
	u.record("k", base)
	u.record("k", base.Add(-5*time.Minute))
	batch := u.take()
	if batch["k"] != want {
		t.Fatalf("应保留较新的那一分钟: %v", batch)
	}
	if len(u.take()) != 0 {
		t.Fatal("take 之后应清空")
	}
	u.record("k", base.Add(-time.Hour))
	u.restore(batch)
	if got := u.take()["k"]; got != want {
		t.Fatalf("restore 应保留较新的时间，got %d", got)
	}
}

func reportCollector(ch chan *fpv1.ReportAccessKeyUsageRequest, failFirst bool) func(*stubServer) {
	var failed atomic.Bool
	return func(s *stubServer) {
		s.reportUsage = func(req *fpv1.ReportAccessKeyUsageRequest) (*fpv1.ReportAccessKeyUsageResponse, error) {
			if failFirst && !failed.Swap(true) {
				return nil, status.Error(codes.Unavailable, "down")
			}
			ch <- req
			return &fpv1.ReportAccessKeyUsageResponse{}, nil
		}
	}
}

func TestUsageReportedAfterSuccessfulRequestOnly(t *testing.T) {
	reports := make(chan *fpv1.ReportAccessKeyUsageRequest, 8)
	env, _ := akStubWith(t, reportCollector(reports, false), okAccessKey(), nil,
		func(o *Options) { o.usageFlushInterval = 50 * time.Millisecond })
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	h.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n0", "wrong"))
	select {
	case r := <-reports:
		t.Fatalf("校验失败的请求不应上报: %v", r)
	case <-time.After(200 * time.Millisecond):
	}

	h.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", testSK))
	select {
	case r := <-reports:
		u := r.GetUsages()
		if len(u) != 1 || u[0].GetAccessKeyId() != testAK || u[0].GetLastUsedAtMs()%60_000 != 0 {
			t.Fatalf("上报内容不对: %v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("校验通过的请求应在一个周期内上报")
	}
}

func TestUsageRetriedAfterReportFailure(t *testing.T) {
	reports := make(chan *fpv1.ReportAccessKeyUsageRequest, 8)
	env, _ := akStubWith(t, reportCollector(reports, true), okAccessKey(), nil,
		func(o *Options) { o.usageFlushInterval = 50 * time.Millisecond })
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", testSK))
	select {
	case r := <-reports:
		if len(r.GetUsages()) != 1 {
			t.Fatalf("重试的批次不对: %v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("第一次上报失败后，应并入下一批重试")
	}
}

func TestCloseFlushesPendingUsage(t *testing.T) {
	reports := make(chan *fpv1.ReportAccessKeyUsageRequest, 8)
	env, _ := akStubWith(t, reportCollector(reports, false), okAccessKey(), nil) // 默认 60 秒周期，只有 Close 会触发上报
	h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	h.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "GET", "/x", "", time.Now().Unix(), "n1", testSK))
	_ = env.client.Close()
	select {
	case <-reports:
	case <-time.After(2 * time.Second):
		t.Fatal("Close 应把还没上报的使用时间报掉")
	}
}

// 【辨别力】缓存时长给到 1 小时：第二次回源只能由推送解释。
func TestAccessKeyChangedAndPurgeDropCache(t *testing.T) {
	for _, tc := range []struct {
		name string
		push func(*stubEnv)
	}{
		{"AccessKeyChanged", func(e *stubEnv) {
			e.stub.events <- &fpv1.WatchResponse{Event: &fpv1.WatchResponse_AccessKeyChanged{
				AccessKeyChanged: &fpv1.AccessKeyChanged{AccessKeyId: testAK}}}
		}},
		{"Purge", func(e *stubEnv) { e.pushPurge(t, "测试") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := okAccessKey()
			res.CacheTtlMs = time.Hour.Milliseconds()
			env, calls := akStub(t, res, nil)
			h := env.auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			serve := func() {
				nonce := "n" + strconv.FormatInt(time.Now().UnixNano(), 10)
				h.ServeHTTP(httptest.NewRecorder(), signedRequest(t, "GET", "/x", "", time.Now().Unix(), nonce, testSK))
			}
			serve()
			serve()
			if calls.Load() != 1 {
				t.Fatalf("推送前应命中缓存，GetAccessKey 调了 %d 次", calls.Load())
			}
			tc.push(env)
			env.waitUntil(t, func() bool { serve(); return calls.Load() == 2 }, "收到推送后应重新向 fp 取")
		})
	}
}

func TestPolicyRefreshedPeriodically(t *testing.T) {
	var polls atomic.Int32
	akStubWith(t, func(s *stubServer) {
		s.getPolicy = func(*fpv1.GetPolicyRequest) (*fpv1.GetPolicyResponse, error) {
			polls.Add(1)
			return &fpv1.GetPolicyResponse{Policy: &fpv1.AppPolicy{Version: 1}}, nil
		}
	}, okAccessKey(), nil, func(o *Options) { o.policyRefreshInterval = 50 * time.Millisecond })
	waitUntilTimeout(t, 3*time.Second, func() bool { return polls.Load() >= 3 },
		"除了 ready 触发的那次，策略还应按周期重拉")
}

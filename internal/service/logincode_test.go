package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

// 不限流的验证码服务：这些用例对同一个手机号反复发码，默认限制会把它们卡死。
func newCodes(t *testing.T) *service.LoginCodeService {
	t.Helper()
	return service.NewLoginCodeService(testsupport.NewTestRedis(t), service.WithLoginCodeRateRules())
}

func TestLoginCodeIssueAndVerify(t *testing.T) {
	svc := newCodes(t)
	ctx := context.Background()

	code, err := svc.Issue(ctx, "13800138000")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(code) != 6 {
		t.Fatalf("code = %q, 长度 want 6", code)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			t.Fatalf("code = %q, 应为纯数字", code)
		}
	}

	if err := svc.Verify(ctx, "13800138000", code); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// 验证码必须是一次性的。
func TestLoginCodeVerifyIsOneShot(t *testing.T) {
	svc := newCodes(t)
	ctx := context.Background()

	code, err := svc.Issue(ctx, "13800138000")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := svc.Verify(ctx, "13800138000", code); err != nil {
		t.Fatalf("首次 Verify: %v", err)
	}
	if err := svc.Verify(ctx, "13800138000", code); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("二次 Verify err = %v, want ErrInvalidCredential", err)
	}
}

func TestLoginCodeVerifyWrongCode(t *testing.T) {
	svc := newCodes(t)
	ctx := context.Background()

	if _, err := svc.Issue(ctx, "13800138000"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	err := svc.Verify(ctx, "13800138000", "000000")
	if !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeCodeInvalid {
		t.Fatalf("错误码 = %v, want %s", err, domain.CodeCodeInvalid)
	}
}

func TestLoginCodeVerifyWithoutIssue(t *testing.T) {
	svc := newCodes(t)
	if err := svc.Verify(context.Background(), "13800138000", "123456"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

// 连续猜错达到上限后，验证码立即作废，正确的码也不再有效——防爆破。
func TestLoginCodeVerifyAttemptLimit(t *testing.T) {
	svc := newCodes(t)
	ctx := context.Background()

	code, err := svc.Issue(ctx, "13800138000")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	for i := 0; i < service.MaxVerifyAttempts; i++ {
		err := svc.Verify(ctx, "13800138000", "000000")
		if !errors.Is(err, domain.ErrInvalidCredential) {
			t.Fatalf("第 %d 次猜错 err = %v", i+1, err)
		}
	}
	if err := svc.Verify(ctx, "13800138000", code); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("超限后正确的码也应无效, err = %v", err)
	}
}

// 不同号码之间互不干扰。
func TestLoginCodeScopedByPhone(t *testing.T) {
	svc := newCodes(t)
	ctx := context.Background()

	codeA, err := svc.Issue(ctx, "13800138000")
	if err != nil {
		t.Fatalf("Issue A: %v", err)
	}
	if _, err := svc.Issue(ctx, "13900139000"); err != nil {
		t.Fatalf("Issue B: %v", err)
	}

	if err := svc.Verify(ctx, "13900139000", codeA); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("A 的码不应能验 B, err = %v", err)
	}
	if err := svc.Verify(ctx, "13800138000", codeA); err != nil {
		t.Fatalf("原号码应通过: %v", err)
	}
}

// Issue 对未过期的码直接复用，不再无条件覆盖。
// 窗口内重复调用必须拿到同一个码，否则第二次点"发送"会顶掉用户手里那个
// 已经收到的码，而顶替它的新码又可能被限流挡下、根本送不到。
func TestLoginCodeIssueReturnsExistingUnexpiredCode(t *testing.T) {
	svc := newCodes(t)
	ctx := context.Background()

	code1, err := svc.Issue(ctx, "13800138000")
	if err != nil {
		t.Fatalf("首次 Issue: %v", err)
	}
	code2, err := svc.Issue(ctx, "13800138000")
	if err != nil {
		t.Fatalf("二次 Issue: %v", err)
	}
	if code2 != code1 {
		t.Fatalf("二次 Issue 应返回同一个码, 首次 = %q, 二次 = %q", code1, code2)
	}

	// 原来那个码必须仍然有效——没有被二次 Issue 悄悄顶掉。
	if err := svc.Verify(ctx, "13800138000", code1); err != nil {
		t.Fatalf("首次签发的码应仍然有效: %v", err)
	}
}

// 并发发码也只能产生一个码：GET + SET 两步走的话，两个请求会各写一个，
// 先发出去的那条短信里的码就成了废码。
func TestLoginCodeConcurrentIssueYieldsOneCode(t *testing.T) {
	svc := newCodes(t)
	ctx := context.Background()

	const n = 16
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		codes = map[string]int{}
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			code, err := svc.Issue(ctx, "13800138000")
			if err != nil {
				t.Errorf("Issue: %v", err)
				return
			}
			mu.Lock()
			codes[code]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(codes) != 1 {
		t.Fatalf("并发发码得到 %d 个不同的码: %v", len(codes), codes)
	}
}

// 尝试计数只在 Issue 真的生成新码时才清零——不是每次调用 Issue 都清。
//
// 真实场景下这个时间差来自码的自然 TTL：尝试计数只在第一次猜错时才起 TTL，
// 因此总是比验证码晚过期，"码已经过期但尝试计数还没过期"是会自然发生的
// 状态。测试没法等 5 分钟真实过期，直接删掉 code 键来复现这个状态。
func TestLoginCodeReissueResetsAttemptCounterOnlyWhenGeneratingNewCode(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	svc := service.NewLoginCodeService(rdb, service.WithLoginCodeRateRules())
	ctx := context.Background()

	if _, err := svc.Issue(ctx, "13800138000"); err != nil {
		t.Fatalf("首次 Issue: %v", err)
	}
	// 攒几次错误尝试，但不到上限。
	for i := 0; i < service.MaxVerifyAttempts-1; i++ {
		err := svc.Verify(ctx, "13800138000", "000000")
		if !errors.Is(err, domain.ErrInvalidCredential) {
			t.Fatalf("第 %d 次猜错 err = %v", i+1, err)
		}
	}

	// 模拟"码已自然过期、尝试计数还没过期"：只删 code 键，让下一次 Issue 走生成新码这条分支。
	if err := rdb.Del(ctx, "fp:code:login:13800138000").Err(); err != nil {
		t.Fatalf("模拟码过期: %v", err)
	}

	code2, err := svc.Issue(ctx, "13800138000")
	if err != nil {
		t.Fatalf("重新 Issue: %v", err)
	}

	// 如果尝试计数没有被清零，再猜错 MaxVerifyAttempts-1 次就会让计数越过上限，新码会被过早作废。
	for i := 0; i < service.MaxVerifyAttempts-1; i++ {
		err := svc.Verify(ctx, "13800138000", "000000")
		if !errors.Is(err, domain.ErrInvalidCredential) {
			t.Fatalf("重新签发后第 %d 次猜错 err = %v", i+1, err)
		}
	}
	if err := svc.Verify(ctx, "13800138000", code2); err != nil {
		t.Fatalf("重新签发的码应仍然有效（尝试计数应已被清零）: %v", err)
	}
}

// 复用未过期的码时不能顺手清尝试计数，否则反复点"重发"就能无限重置爆破计数器。
func TestLoginCodeReissueOfLiveCodeKeepsAttemptCounter(t *testing.T) {
	svc := newCodes(t)
	ctx := context.Background()

	code, err := svc.Issue(ctx, "13800138000")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	for i := 0; i < service.MaxVerifyAttempts-1; i++ {
		_ = svc.Verify(ctx, "13800138000", "000000")
	}
	if again, err := svc.Issue(ctx, "13800138000"); err != nil || again != code {
		t.Fatalf("重发应复用同一个码: %q, %v", again, err)
	}
	// 计数没被清零：再错一次就到上限，之后正确的码也作废。
	_ = svc.Verify(ctx, "13800138000", "000000")
	if err := svc.Verify(ctx, "13800138000", code); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("重发把尝试计数清零了，爆破计数器可以被无限重置: err = %v", err)
	}
}

func TestLoginCodeAllowSendDefaultRules(t *testing.T) {
	svc := service.NewLoginCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if err := svc.AllowSend(ctx, "13800138000"); err != nil {
		t.Fatalf("首次: %v", err)
	}
	err := svc.AllowSend(ctx, "13800138000")
	if !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("30 秒内二次 err = %v, want ErrRateLimited", err)
	}
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeRateLimited {
		t.Fatalf("错误码 = %v, want %s", err, domain.CodeRateLimited)
	}
	if ms, _ := de.Detail["retryAfterMs"].(int64); ms <= 0 || ms > 30_000 {
		t.Fatalf("retryAfterMs = %v, want (0, 30000]", de.Detail["retryAfterMs"])
	}

	// 限制按手机号计，换个号码不受影响。
	if err := svc.AllowSend(ctx, "13900139000"); err != nil {
		t.Fatalf("别的号码不该被限: %v", err)
	}
}

// 较长窗口的规则同样生效，且各条规则独立计数。
func TestLoginCodeAllowSendEnforcesEachRule(t *testing.T) {
	svc := service.NewLoginCodeService(testsupport.NewTestRedis(t), service.WithLoginCodeRateRules(
		service.LoginCodeRateRule{Name: "short", Window: time.Minute, Limit: 10},
		service.LoginCodeRateRule{Name: "long", Window: time.Hour, Limit: 2},
	))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if err := svc.AllowSend(ctx, "13800138000"); err != nil {
			t.Fatalf("第 %d 次: %v", i+1, err)
		}
	}
	if err := svc.AllowSend(ctx, "13800138000"); !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("超出长窗口上限 err = %v, want ErrRateLimited", err)
	}
}

// 显式传空规则才是关闭限制；不传选项必须套用默认规则（安全默认）。
func TestLoginCodeAllowSendNoRulesMeansUnlimited(t *testing.T) {
	svc := newCodes(t)
	for i := 0; i < 20; i++ {
		if err := svc.AllowSend(context.Background(), "13800138000"); err != nil {
			t.Fatalf("第 %d 次: %v", i+1, err)
		}
	}
}

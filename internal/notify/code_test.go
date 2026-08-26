package notify_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/notify"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestIssueAndVerify(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	code, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000")
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

	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", code); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

// 验证码必须是一次性的。
func TestVerifyIsOneShot(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	code, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", code); err != nil {
		t.Fatalf("首次 Verify: %v", err)
	}
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", code); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("二次 Verify err = %v, want ErrInvalidCredential", err)
	}
}

func TestVerifyWrongCode(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if _, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", "000000"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

func TestVerifyWithoutIssue(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	if err := svc.Verify(context.Background(), notify.PurposeLogin, "13800138000", "123456"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("err = %v, want ErrInvalidCredential", err)
	}
}

// 连续猜错达到上限后，验证码立即作废，正确的码也不再有效——防爆破。
func TestVerifyAttemptLimit(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	code, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	for i := 0; i < notify.MaxVerifyAttempts; i++ {
		err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", "000000")
		if !errors.Is(err, domain.ErrInvalidCredential) {
			t.Fatalf("第 %d 次猜错 err = %v", i+1, err)
		}
	}
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", code); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("超限后正确的码也应无效, err = %v", err)
	}
}

// 不同用途 / 不同号码之间互不干扰。
func TestCodeScopedByPurposeAndTarget(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	codeA, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000")
	if err != nil {
		t.Fatalf("Issue A: %v", err)
	}
	if _, err := svc.Issue(ctx, notify.PurposeLogin, "13900139000"); err != nil {
		t.Fatalf("Issue B: %v", err)
	}

	if err := svc.Verify(ctx, notify.PurposeLogin, "13900139000", codeA); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("A 的码不应能验 B, err = %v", err)
	}
	if err := svc.Verify(ctx, "other", "13800138000", codeA); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("换用途不应通过, err = %v", err)
	}
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", codeA); err != nil {
		t.Fatalf("原用途原号码应通过: %v", err)
	}
}

// Issue 对未过期的码直接复用，不再无条件覆盖（fix round 1 的 Critical）。
// 窗口内重复调用必须拿到同一个码，否则第二次点"发送"会顶掉用户手里那个
// 已经收到的码，而顶替它的新码又可能被上层的频率限制挡下、根本送不到。
func TestIssueReturnsExistingUnexpiredCode(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	code1, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000")
	if err != nil {
		t.Fatalf("首次 Issue: %v", err)
	}
	code2, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000")
	if err != nil {
		t.Fatalf("二次 Issue: %v", err)
	}
	if code2 != code1 {
		t.Fatalf("二次 Issue 应返回同一个码, 首次 = %q, 二次 = %q", code1, code2)
	}

	// 原来那个码必须仍然有效——没有被二次 Issue 悄悄顶掉。
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", code1); err != nil {
		t.Fatalf("首次签发的码应仍然有效: %v", err)
	}
}

// 尝试计数只在 Issue 真的生成新码时才清零——不是每次调用 Issue 都清。
//
// 这条不变式在上面那个"复用未过期码"的改动之后才需要重新证明：过去 Issue
// 每次调用都会生成新码，"重新签发清计数"和"生成新码时清计数"是同一件事；
// 现在两者分离了，得专门验证"只清对了那一半"没有被连带清没——如果 Issue
// 变成了"从不清"，这条测试要能抓到。
//
// 真实场景下这个时间差来自码的自然 TTL：tryKey 只在第一次猜错时才起 TTL，
// 因此总是比 codeKey 晚过期，"码已经过期但尝试计数还没过期"是会自然发生的
// 状态。测试没法等 5 分钟真实过期，直接删掉 code 键来复现这个状态。
func TestReissueResetsAttemptCounterOnlyWhenGeneratingNewCode(t *testing.T) {
	rdb := testsupport.NewTestRedis(t)
	svc := notify.NewCodeService(rdb)
	ctx := context.Background()

	if _, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000"); err != nil {
		t.Fatalf("首次 Issue: %v", err)
	}
	// 攒几次错误尝试，但不到上限。
	for i := 0; i < notify.MaxVerifyAttempts-1; i++ {
		err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", "000000")
		if !errors.Is(err, domain.ErrInvalidCredential) {
			t.Fatalf("第 %d 次猜错 err = %v", i+1, err)
		}
	}

	// 模拟"码已自然过期、尝试计数还没过期"这个时间差：直接删掉 code 键，
	// 不碰尝试计数键，让下一次 Issue 走生成新码这条分支。
	if err := rdb.Del(ctx, "fp:code:"+notify.PurposeLogin+":13800138000").Err(); err != nil {
		t.Fatalf("模拟码过期: %v", err)
	}

	code2, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000")
	if err != nil {
		t.Fatalf("重新 Issue: %v", err)
	}

	// 如果尝试计数没有被清零，再猜错 MaxVerifyAttempts-1 次就会让计数越过上限，
	// 新码会被过早作废。
	for i := 0; i < notify.MaxVerifyAttempts-1; i++ {
		err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", "000000")
		if !errors.Is(err, domain.ErrInvalidCredential) {
			t.Fatalf("重新签发后第 %d 次猜错 err = %v", i+1, err)
		}
	}
	if err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", code2); err != nil {
		t.Fatalf("重新签发的码应仍然有效（尝试计数应已被清零）: %v", err)
	}
}

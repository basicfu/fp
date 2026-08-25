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

// 补充覆盖：brief 未显式测试的场景——重新签发必须清空上一轮的尝试计数，
// 否则上一轮临近上限的失败次数会直接拖垮新码（Issue 里的 Del(tryKey) 就是为此存在）。
func TestReissueResetsAttemptCounter(t *testing.T) {
	svc := notify.NewCodeService(testsupport.NewTestRedis(t))
	ctx := context.Background()

	if _, err := svc.Issue(ctx, notify.PurposeLogin, "13800138000"); err != nil {
		t.Fatalf("首次 Issue: %v", err)
	}
	// 用掉 MaxVerifyAttempts-1 次错误尝试，只剩最后一次机会但还未作废。
	for i := 0; i < notify.MaxVerifyAttempts-1; i++ {
		err := svc.Verify(ctx, notify.PurposeLogin, "13800138000", "000000")
		if !errors.Is(err, domain.ErrInvalidCredential) {
			t.Fatalf("第 %d 次猜错 err = %v", i+1, err)
		}
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

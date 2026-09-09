package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestIMCredentialRotateAndVerify(t *testing.T) {
	svc := service.NewIMCredentialService(testsupport.NewTestDB(t))
	ctx := context.Background()

	if ok, err := svc.Exists(ctx); err != nil || ok {
		t.Fatalf("初始状态应当是「还没生成过」，got ok=%v err=%v", ok, err)
	}

	secret, err := svc.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) < 32 {
		t.Fatalf("生成的 secret 太短：%d 字符", len(secret))
	}
	if ok, err := svc.Exists(ctx); err != nil || !ok {
		t.Fatalf("生成之后 Exists 应为 true，got ok=%v err=%v", ok, err)
	}
	if err := svc.Verify(ctx, secret); err != nil {
		t.Fatalf("刚生成的 secret 校验失败：%v", err)
	}
	if err := svc.Verify(ctx, secret+"x"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("错误的 secret 必须返回 ErrInvalidCredential，got %v", err)
	}
}

// TestIMCredentialRotateInvalidatesOld 钉住轮换的核心语义：旧 secret 必须
// 立刻在库这一层失效。
//
// gRPC 拦截器那层还有一个 10 秒的成功缓存窗口，是另一回事——那是刻意的
// 取舍（见 grpcapi.DefaultIMSecretCacheTTL），不能拿它给库这层的失效开脱。
func TestIMCredentialRotateInvalidatesOld(t *testing.T) {
	svc := service.NewIMCredentialService(testsupport.NewTestDB(t))
	ctx := context.Background()

	old, err := svc.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.Rotate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if old == fresh {
		t.Fatal("两次轮换生成了相同的 secret")
	}
	if err := svc.Verify(ctx, fresh); err != nil {
		t.Fatalf("新 secret 应当有效：%v", err)
	}
	if err := svc.Verify(ctx, old); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("旧 secret 必须失效，got %v", err)
	}
}

// TestIMCredentialVerifyBeforeRotate 钉住「从未生成过」时的行为：必须是
// 凭据无效，不能是内部错误。
//
// fp-im 配好了 secret 而 fp 这边还没生成，是运维顺序问题不是 bug——报成
// 内部错误会让人去查 fp 的日志和数据库连接，而正确的动作是去控制台点一下
// 「生成」。
func TestIMCredentialVerifyBeforeRotate(t *testing.T) {
	svc := service.NewIMCredentialService(testsupport.NewTestDB(t))
	if err := svc.Verify(context.Background(), "whatever"); !errors.Is(err, domain.ErrInvalidCredential) {
		t.Fatalf("还没生成过 IM 凭据时必须返回 ErrInvalidCredential，got %v", err)
	}
}

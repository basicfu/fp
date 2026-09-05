package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

// newConfigFixture 建一个应用并返回它的 id 与一个 ConfigService。
// pub 传 nil：本任务只测读，推送在 Task 5。
//
// **两个顺序陷阱**：
//  1. testsupport.NewTestDB 每次调用都会 TRUNCATE 全部业务表。所以拿 pool
//     和调 newAppService（它内部又调一次 NewTestDB）都必须发生在建应用
//     **之前**——反过来的话，刚建好的应用会被下一次 TRUNCATE 冲掉，
//     而报错会是一句与真实原因毫无关系的外键失败。
//  2. ApplicationService.Create 返回**三个**值 (*domain.Application, secret, error)，
//     明文密钥只在创建时返回这一次。
//
// newAppService 是 application_test.go 里已有的辅助（同属 package service_test），
// 直接用，不要另建一个 registry。
func newConfigFixture(t *testing.T) (*service.ConfigService, uuid.UUID) {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	apps := newAppService(t)
	app, _, err := apps.Create(context.Background(), "商城", "shop")
	if err != nil {
		t.Fatalf("建应用失败: %v", err)
	}
	return service.NewConfigService(pool, nil), app.ID
}

func field(typ, desc, value string) domain.ConfigField {
	return domain.ConfigField{Type: typ, Desc: desc, Value: json.RawMessage(value)}
}

func TestCurrentReturnsEmptyWhenNoVersion(t *testing.T) {
	svc, appID := newConfigFixture(t)

	got, err := svc.Current(context.Background(), appID, domain.ConfigTypeDefault)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if got.Seq != 0 {
		t.Fatalf("Seq = %d，期望 0", got.Seq)
	}
	// 必须是非 nil 的空 map：调用方会直接 range 它，返回 nil 会让
	// "还没有任何版本"和"这一版是空的"在下游产生不同的分支。
	if got.Fields == nil {
		t.Fatal("Fields 是 nil，期望非 nil 的空 map")
	}
	if len(got.Fields) != 0 {
		t.Fatalf("Fields 有 %d 项，期望 0", len(got.Fields))
	}
}

func TestVersionNotFound(t *testing.T) {
	svc, appID := newConfigFixture(t)

	_, err := svc.Version(context.Background(), appID, domain.ConfigTypeDefault, 7)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，期望包装了 domain.ErrNotFound", err)
	}
}

func TestRejectsUnknownPartition(t *testing.T) {
	svc, appID := newConfigFixture(t)

	_, err := svc.Current(context.Background(), appID, "MOBILE")
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v，期望包装了 domain.ErrInvalidArgument", err)
	}
}

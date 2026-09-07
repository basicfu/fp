package multiauth

import (
	"context"
	"errors"
	"testing"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
)

// fake 记录自己被调用过，并返回一个可辨认的主体。
type fake struct {
	called bool
	sub    model.Subject
}

func (f *fake) Verify(_ context.Context, _ auth.VerifyRequest) (model.Subject, error) {
	f.called = true
	return f.sub, nil
}

func TestDispatchByKind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    string
		wantFP  bool
		wantBiz bool
	}{
		{"空串走 fp", "", true, false},
		{"显式 fp", model.TokenKindFP, true, false},
		{"biz 走业务方", model.TokenKindBiz, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := &fake{sub: model.User("1")}
			biz := &fake{sub: model.Biz("2")}
			a := New(fp, biz)
			if _, err := a.Verify(context.Background(), auth.VerifyRequest{App: "a1", Kind: tc.kind, Token: "t"}); err != nil {
				t.Fatal(err)
			}
			if fp.called != tc.wantFP || biz.called != tc.wantBiz {
				t.Fatalf("分派错了：fp.called=%v biz.called=%v", fp.called, biz.called)
			}
		})
	}
}

func TestUnknownKindIsRejected(t *testing.T) {
	fp := &fake{sub: model.User("1")}
	biz := &fake{sub: model.Biz("2")}
	a := New(fp, biz)
	_, err := a.Verify(context.Background(), auth.VerifyRequest{App: "a1", Kind: "wechat", Token: "t"})
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("未知类型必须拒绝，实际 %v", err)
	}
	// 未知类型不能被兜底送给任何一个下游：一个拼错的类型说明调用方有 bug，
	// 静默降级会让它更难被发现。
	if fp.called || biz.called {
		t.Fatal("未知类型不该被送给任何下游")
	}
}

func TestNilBizRejectsBizKind(t *testing.T) {
	fp := &fake{sub: model.User("1")}
	a := New(fp, nil)
	_, err := a.Verify(context.Background(), auth.VerifyRequest{App: "a1", Kind: model.TokenKindBiz, Token: "t"})
	if !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("没有业务方认证器时 biz 必须拒绝，实际 %v", err)
	}
	if fp.called {
		t.Fatal("不能因为业务方认证器缺席就退回 fp 验：那会让业务方令牌被当成 fp 令牌，报错信息还会误导排查")
	}
}

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// 设备字段要跟着会话走完签发、校验、延期、轮换：丢在任何一步，绑定设备就形同虚设或误伤。
func TestSessionDeviceFieldsSurviveValidateExtendRotate(t *testing.T) {
	svc, clk := newSessionService(t)
	app := testApp(func(p *domain.SessionPolicy) {
		p.IdleTimeoutSeconds = 100
		p.IdleTimeoutMobileSeconds = 100
		p.MaxLifetimeSeconds = 100000
		p.RotateIntervalSeconds = 50
		p.ExtendIntervalSeconds = 10
		p.TokenCacheTTLSeconds = 5
	})
	ctx := context.Background()
	sess, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app, DevicePub: "abcd", ClientRef: "42"})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	check := func(what string, s *domain.Session) {
		t.Helper()
		if s.DevicePub != "abcd" || s.ClientRef != "42" {
			t.Fatalf("%s 后设备字段 = %q/%q", what, s.DevicePub, s.ClientRef)
		}
	}
	check("签发", sess)

	res, err := svc.Validate(ctx, sess.Token, app)
	if err != nil {
		t.Fatal(err)
	}
	check("校验", res.Session)

	clk.Advance(11 * time.Second)
	if res, err = svc.Validate(ctx, sess.Token, app); err != nil {
		t.Fatal(err)
	}
	check("延期", res.Session)

	clk.Advance(45 * time.Second)
	if res, err = svc.Validate(ctx, sess.Token, app); err != nil || !res.Rotated {
		t.Fatalf("期望轮换: rotated=%v err=%v", res != nil && res.Rotated, err)
	}
	check("轮换", res.Session)
	if res, err = svc.Validate(ctx, res.NewToken, app); err != nil {
		t.Fatal(err)
	}
	check("用新 token 校验", res.Session)
}

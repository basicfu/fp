package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
)

func loggedOutOf(t *testing.T, ev *service.AppEvents, appID uuid.UUID, sessionID string) []eventView {
	t.Helper()
	var out []eventView
	for _, v := range pendingEvents(t, ev, appID) {
		if v.Kind == domain.EventLoggedOut && v.SessionID == sessionID {
			out = append(out, v)
		}
	}
	return out
}

// 冻结先递增纪元、再写事件撤销；写事件失败时会话留着。之后校验因纪元不一致删掉它，
// 这时必须补一条 logged_out(revoked)，否则业务方永远不知道这个会话没了。
func TestStaleEpochDeletionEmitsRevoked(t *testing.T) {
	e := newEventEnv(t)
	ctx := context.Background()
	res := e.loginWithRef(t, "13800000001", "dev-1")

	e.appender.set(func() error { return errors.New("事件表挂了") }, nil)
	if _, err := e.accounts.SetStatus(ctx, res.User.ID, domain.UserStatusFrozen); err == nil {
		t.Fatal("写不进事件却冻结成功")
	}
	e.appender.set(nil, nil)
	if got := loggedOutOf(t, e.events, e.app.ID, res.Session.ID); len(got) != 0 {
		t.Fatalf("前提不成立，已经有退出事件: %+v", got)
	}

	_, err := e.auth.ValidateToken(ctx, e.app.AppID, res.Session.Token)
	wantCode(t, err, domain.CodeTokenInvalid, domain.ErrUnauthorized)
	got := loggedOutOf(t, e.events, e.app.ID, res.Session.ID)
	if len(got) != 1 || got[0].Reason != domain.LoggedOutRevoked || got[0].ClientRef != "dev-1" || got[0].UserID != res.User.ID {
		t.Fatalf("logged_out = %+v", got)
	}
	// 会话已删，再校验不会再补。
	_, err = e.auth.ValidateToken(ctx, e.app.AppID, res.Session.Token)
	wantCode(t, err, domain.CodeTokenInvalid, domain.ErrUnauthorized)
	if got := loggedOutOf(t, e.events, e.app.ID, res.Session.ID); len(got) != 1 {
		t.Fatalf("重复补写: %+v", got)
	}
}

// 补写也失败时不删会话：下次校验再补，token 照样不放行。
func TestStaleEpochKeepsSessionWhenRevokedEventFails(t *testing.T) {
	e := newEventEnv(t)
	ctx := context.Background()
	res := e.loginWithRef(t, "13800000001", "dev-1")

	e.appender.set(func() error { return errors.New("事件表挂了") }, nil)
	if _, err := e.accounts.SetStatus(ctx, res.User.ID, domain.UserStatusFrozen); err == nil {
		t.Fatal("写不进事件却冻结成功")
	}
	_, err := e.auth.ValidateToken(ctx, e.app.AppID, res.Session.Token)
	wantCode(t, err, domain.CodeTokenInvalid, domain.ErrUnauthorized)
	if _, err := e.sess.SessionByToken(ctx, res.Session.Token); err != nil {
		t.Fatalf("事件没补上就把会话删了: %v", err)
	}

	e.appender.set(nil, nil)
	_, err = e.auth.ValidateToken(ctx, e.app.AppID, res.Session.Token)
	wantCode(t, err, domain.CodeTokenInvalid, domain.ErrUnauthorized)
	if got := loggedOutOf(t, e.events, e.app.ID, res.Session.ID); len(got) != 1 || got[0].Reason != domain.LoggedOutRevoked {
		t.Fatalf("logged_out = %+v", got)
	}
}

// 正常撤销已经写过事件并删了会话，之后的校验不该再补一条。
func TestNormalRevokeNotFollowedByRevoked(t *testing.T) {
	e := newEventEnv(t)
	ctx := context.Background()
	res := e.loginWithRef(t, "13800000001", "dev-1")
	if _, err := e.accounts.SetStatus(ctx, res.User.ID, domain.UserStatusFrozen); err != nil {
		t.Fatal(err)
	}
	_, err := e.auth.ValidateToken(ctx, e.app.AppID, res.Session.Token)
	wantCode(t, err, domain.CodeTokenInvalid, domain.ErrUnauthorized)
	got := loggedOutOf(t, e.events, e.app.ID, res.Session.ID)
	if len(got) != 1 || got[0].Reason != domain.RevokeReasonFreeze {
		t.Fatalf("logged_out = %+v", got)
	}
}

// 轮换过渡期内同一会话有新旧两个 token：纪元失配时只补一条，不按 token 各补一条。
func TestStaleEpochRotatedPairEmitsOnce(t *testing.T) {
	e := newExpiryEnv(t)
	ctx := context.Background()
	p := expiryPolicy
	p.RotateIntervalSeconds = 600
	app, err := e.apps.UpdateSessionPolicy(ctx, e.app.ID, p)
	if err != nil {
		t.Fatal(err)
	}
	e.app = app
	s := e.issue(t, "dev-1")
	e.advance(601 * time.Second)
	rot, err := e.sess.Validate(ctx, s.Token, e.app)
	if err != nil || !rot.Rotated {
		t.Fatalf("轮换: %+v %v", rot, err)
	}

	if _, err := store.NewEpochStore(e.rdb).Bump(ctx, s.UserID); err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{s.Token, rot.NewToken} {
		_, err := e.sess.Validate(ctx, tok, e.app)
		wantCode(t, err, domain.CodeTokenInvalid, domain.ErrUnauthorized)
	}
	got := loggedOutOf(t, e.events, e.app.ID, s.ID)
	if len(got) != 1 || got[0].Reason != domain.LoggedOutRevoked || got[0].ClientRef != "dev-1" {
		t.Fatalf("logged_out = %+v", got)
	}
	if _, ok, _ := e.st.ExpiryScore(ctx, store.ExpiryMember(s)); ok {
		t.Fatal("到期集合里还留着成员")
	}
}

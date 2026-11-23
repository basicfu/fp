package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

// lastSessionEvents 是该用户各会话的最后一条登录类事件，业务方按它认定设备在不在线。
func lastSessionEvents(t *testing.T, e *eventEnv, userID uuid.UUID) map[string]eventView {
	t.Helper()
	out := map[string]eventView{}
	for _, v := range pendingEvents(t, e.events, e.app.ID) {
		if v.UserID == userID && (v.Kind == domain.EventLoginSucceeded || v.Kind == domain.EventLoggedOut) {
			out[v.SessionID] = v
		}
	}
	return out
}

// wantLoginConverged 断言业务方按事件认定的状态与会话实际状态一致：登录成功则 token 可用且最后一条是
// login_succeeded；登录失败则该用户不留活会话，事件里出现过的会话最后一条都是 logged_out。
func wantLoginConverged(t *testing.T, e *eventEnv, userID uuid.UUID, res *service.LoginResult, loginErr error) {
	t.Helper()
	ctx := context.Background()
	last := lastSessionEvents(t, e, userID)
	if loginErr == nil {
		if v := last[res.Session.ID]; v.Kind != domain.EventLoginSucceeded {
			t.Fatalf("登录成功，但该会话最后一条事件 = %+v（全部 %+v）", v, last)
		}
		if _, err := e.auth.ValidateToken(ctx, e.app.AppID, res.Session.Token); err != nil {
			t.Fatalf("登录成功，但 token 不可用: %v（事件 %+v）", err, last)
		}
		return
	}
	for id, v := range last {
		if v.Kind != domain.EventLoggedOut {
			t.Fatalf("登录失败，会话 %s 最后一条事件仍是 %s（全部 %+v）", id, v.Kind, last)
		}
	}
	if list, err := e.sess.ListByUser(ctx, userID); err != nil || len(list) != 0 {
		t.Fatalf("登录失败却留下活会话: %d, %v", len(list), err)
	}
}

// 踢下线落在「会话已写入、login_succeeded 还没写」之间：老顺序下业务方先收到 logged_out、后收到 login_succeeded，
// 以为设备在线，手里的 token 却早已作废。
func TestLoginRacingKickAfterPutConverges(t *testing.T) {
	e := newEventEnv(t)
	ctx := context.Background()
	in := e.passwordInput(t, "13800000001")
	in.ClientRef = "dev-1"
	uid := mustUserID(t, e.authEnv, "13800000001")
	fired := false
	e.appender.setKind(func(kind string, written bool) error {
		if kind == domain.EventLoginSucceeded && !written && !fired {
			fired = true
			if _, err := e.accounts.RevokeUsersSessions(ctx, []uuid.UUID{uid}); err != nil {
				t.Errorf("RevokeUsersSessions: %v", err)
			}
		}
		return nil
	})
	res, err := e.auth.Login(ctx, in)
	e.appender.setKind(nil)
	wantLoginConverged(t, e, uid, res, err)
}

// 踢下线落在「签发已读纪元、会话还没写入」之间：踢下线扫不到这个会话，它刻着旧纪元却被当成登录成功。
// 业务方最终必须看到它已退出。
func TestLoginRacingKickBeforePutEndsLoggedOut(t *testing.T) {
	e := newEventEnv(t)
	ctx := context.Background()
	in := e.passwordInput(t, "13800000001")
	in.ClientRef = "dev-1"
	uid := mustUserID(t, e.authEnv, "13800000001")
	e.epochs.once(func() {
		if _, err := e.accounts.RevokeUsersSessions(ctx, []uuid.UUID{uid}); err != nil {
			t.Errorf("RevokeUsersSessions: %v", err)
		}
	})
	res, err := e.auth.Login(ctx, in)
	if err == nil {
		t.Fatal("签发途中被踢下线，登录却成功了")
	}
	wantLoginConverged(t, e, uid, res, err)
	if len(lastSessionEvents(t, e, uid)) != 1 {
		t.Fatalf("事件 = %+v", lastSessionEvents(t, e, uid))
	}
}

// login_succeeded 已提交却报错（如提交后连接断开）：登录失败，业务方要收到对应的 logged_out。
func TestLoginSucceededCommittedButErroredIsCompensated(t *testing.T) {
	e := newEventEnv(t)
	ctx := context.Background()
	in := e.passwordInput(t, "13800000001")
	in.ClientRef = "dev-1"
	uid := mustUserID(t, e.authEnv, "13800000001")
	e.appender.setKind(func(kind string, written bool) error {
		if kind == domain.EventLoginSucceeded && written {
			return errors.New("提交后连接断开")
		}
		return nil
	})
	res, err := e.auth.Login(ctx, in)
	e.appender.setKind(nil)
	if err == nil {
		t.Fatal("事件写入报错却登录成功")
	}
	wantLoginConverged(t, e, uid, res, err)
	last := lastSessionEvents(t, e, uid)
	if len(last) != 1 {
		t.Fatalf("事件 = %+v", last)
	}
}

// 连补写的 logged_out 也失败（或进程在两步之间崩掉）：过期扫描到点找不到会话，照样补出 logged_out。
func TestLoginAbandonedWithoutCompensationIsSweptLater(t *testing.T) {
	e := newEventEnv(t)
	ctx := context.Background()
	in := e.passwordInput(t, "13800000001")
	in.ClientRef = "dev-1"
	uid := mustUserID(t, e.authEnv, "13800000001")
	e.appender.setKind(func(kind string, written bool) error {
		if kind == domain.EventLoginSucceeded && written {
			return errors.New("提交后连接断开")
		}
		if kind == domain.EventLoggedOut && !written {
			return errors.New("事件表挂了")
		}
		return nil
	})
	if _, err := e.auth.Login(ctx, in); err == nil {
		t.Fatal("事件写入报错却登录成功")
	}
	e.appender.setKind(nil)
	if last := lastSessionEvents(t, e, uid); len(last) != 1 {
		t.Fatalf("前提不成立: %+v", last)
	}

	later := func() int64 { return time.Now().Add(time.Hour).UnixMilli() }
	scan := service.NewSessionExpiry(e.st, e.epochs, e.events, e.apps, service.SessionExpiryOptions{Now: later})
	if _, err := scan.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	for id, v := range lastSessionEvents(t, e, uid) {
		if v.Kind != domain.EventLoggedOut {
			t.Fatalf("会话 %s 扫描后仍停在 %s", id, v.Kind)
		}
	}
}

// 冻结递增了纪元、写 logged_out 时失败：会话留着，客户端此后不再校验。过期扫描要尽快补出 revoked 并删掉它，
// 不能等到空闲超时才报一个 expired。
func TestStaleEpochSessionSweptAsRevoked(t *testing.T) {
	e := newEventEnv(t)
	ctx := context.Background()
	res := e.loginWithRef(t, "13800000001", "dev-1")
	e.appender.set(func() error { return errors.New("事件表挂了") }, nil)
	if _, err := e.accounts.SetStatus(ctx, res.User.ID, domain.UserStatusFrozen); err == nil {
		t.Fatal("写不进事件却冻结成功")
	}
	e.appender.set(nil, nil)

	soon := func() int64 { return time.Now().Add(time.Second).UnixMilli() }
	scan := service.NewSessionExpiry(e.st, e.epochs, e.events, e.apps, service.SessionExpiryOptions{Now: soon})
	if _, err := scan.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got := loggedOutOf(t, e.events, e.app.ID, res.Session.ID)
	if len(got) != 1 || got[0].Reason != domain.LoggedOutRevoked {
		t.Fatalf("logged_out = %+v", got)
	}
	if _, err := e.sess.SessionByToken(ctx, res.Session.Token); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("纪元失配的会话没删: %v", err)
	}
}

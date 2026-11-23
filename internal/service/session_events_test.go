package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

// hookedAppender 包住真实的事件表，测试在写事件前后插故障。
type hookedAppender struct {
	real *service.AppEvents
	mu   sync.Mutex
	// before 非 nil 时先调它，返回错误就不写。
	before func() error
	// after 在写成功之后调。
	after func()
	// kind 非 nil 时按事件种类插故障：written 为假时在写之前调、返回错误就不写；为真时在写成功之后调、
	// 返回错误就模拟「已提交却报错」。
	kind func(kind string, written bool) error
}

func (h *hookedAppender) Append(ctx context.Context, appID, userID uuid.UUID, kind string, payload any) error {
	h.mu.Lock()
	before, after, hook := h.before, h.after, h.kind
	h.mu.Unlock()
	if before != nil {
		if err := before(); err != nil {
			return err
		}
	}
	if hook != nil {
		if err := hook(kind, false); err != nil {
			return err
		}
	}
	if err := h.real.Append(ctx, appID, userID, kind, payload); err != nil {
		return err
	}
	if after != nil {
		after()
	}
	if hook != nil {
		return hook(kind, true)
	}
	return nil
}

func (h *hookedAppender) setKind(hook func(kind string, written bool) error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.kind = hook
}

func (h *hookedAppender) set(before func() error, after func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.before, h.after = before, after
}

// hookedEpochs 包住真实的纪元，测试在读完纪元之后插一段动作（只触发一次）。
type hookedEpochs struct {
	*store.EpochStore
	mu    sync.Mutex
	after func()
}

func (h *hookedEpochs) Current(ctx context.Context, userID uuid.UUID) (int64, error) {
	v, err := h.EpochStore.Current(ctx, userID)
	h.mu.Lock()
	after := h.after
	h.after = nil
	h.mu.Unlock()
	if after != nil {
		after()
	}
	return v, err
}

func (h *hookedEpochs) once(f func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.after = f
}

// eventEnv 是开了事件的登录环境：会话、登录、注销都接着真实的事件表。
type eventEnv struct {
	*authEnv
	events   *service.AppEvents
	appender *hookedAppender
	epochs   *hookedEpochs
	st       *store.SessionStore
}

func newEventEnv(t *testing.T) *eventEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)

	users := service.NewUserService(pool)
	epochs := &hookedEpochs{EpochStore: store.NewEpochStore(rdb)}
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(users)); err != nil {
		t.Fatal(err)
	}
	apps := service.NewApplicationService(pool, reg)
	events := service.NewAppEvents(pool, apps, nil)
	appender := &hookedAppender{real: events}
	st := store.NewSessionStore(rdb)
	sessions := service.NewSessionService(st, store.NewRevokePublisher(rdb), epochs,
		service.WithSessionEvents(appender), service.WithSessionApps(apps))
	logs := service.NewLoginLogService(pool)

	app := newEventApp(t, apps, "events", true)
	return &eventEnv{
		authEnv: &authEnv{
			auth: service.NewAuthService(service.AuthDeps{
				Apps: apps, Users: users, Sessions: sessions, Logs: logs, Registry: reg, Events: appender,
			}),
			apps: apps, users: users, sess: sessions, logs: logs, app: app, pool: pool,
			accounts: service.NewAccountService(users, sessions, epochs, logs, service.WithAccountEvents(events)),
		},
		events: events, appender: appender, epochs: epochs, st: st,
	}
}

func newEventApp(t *testing.T, apps *service.ApplicationService, code string, events bool) *domain.Application {
	t.Helper()
	ctx := context.Background()
	app, _, err := apps.Create(ctx, code, code+"-"+uuid.NewString()[:6])
	if err != nil {
		t.Fatal(err)
	}
	if err := apps.SetConnector(ctx, app.ID, connector.TypePassword, true, nil); err != nil {
		t.Fatal(err)
	}
	if events {
		if err := apps.EnableAccountEvents(ctx, app.ID); err != nil {
			t.Fatalf("EnableAccountEvents: %v", err)
		}
	}
	if app, err = apps.GetByID(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	return app
}

type eventView struct {
	Kind      string
	UserID    uuid.UUID
	SessionID string `json:"session_id"`
	ClientRef string `json:"client_ref"`
	Reason    string `json:"reason"`
}

func pendingEvents(t *testing.T, ev *service.AppEvents, appID uuid.UUID) []eventView {
	t.Helper()
	list, err := ev.Pending(context.Background(), appID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]eventView, 0, len(list))
	for _, e := range list {
		v := eventView{Kind: e.Kind, UserID: e.UserID}
		if err := json.Unmarshal(e.Payload, &v); err != nil {
			t.Fatalf("payload %s: %v", e.Payload, err)
		}
		out = append(out, v)
	}
	return out
}

func (e *eventEnv) loginWithRef(t *testing.T, phone, ref string) *service.LoginResult {
	t.Helper()
	in := e.passwordInput(t, phone)
	in.ClientRef = ref
	res, err := e.auth.Login(context.Background(), in)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return res
}

func TestEventLoginSucceededCarriesSessionAndClientRef(t *testing.T) {
	e := newEventEnv(t)
	res := e.loginWithRef(t, "13800000001", "dev-7")
	got := pendingEvents(t, e.events, e.app.ID)
	if len(got) != 1 || got[0].Kind != domain.EventLoginSucceeded || got[0].UserID != res.User.ID ||
		got[0].SessionID != res.Session.ID || got[0].ClientRef != "dev-7" {
		t.Fatalf("事件 = %+v", got)
	}
}

func TestEventLoginFailsWhenEventCannotBeWritten(t *testing.T) {
	e := newEventEnv(t)
	in := e.passwordInput(t, "13800000001")
	e.appender.set(func() error { return errors.New("事件表挂了") }, nil)
	if _, err := e.auth.Login(context.Background(), in); err == nil {
		t.Fatal("写不进登录成功事件却登录成功了")
	}
	e.appender.set(nil, nil)
	sessions, err := e.sess.ListByUser(context.Background(), mustUserID(t, e.authEnv, "13800000001"))
	if err != nil || len(sessions) != 0 {
		t.Fatalf("留下了会话: %d, %v", len(sessions), err)
	}
	if got := pendingEvents(t, e.events, e.app.ID); len(got) != 0 {
		t.Fatalf("作废刚签发的会话不该写退出事件: %+v", got)
	}
}

func mustUserID(t *testing.T, e *authEnv, phone string) uuid.UUID {
	t.Helper()
	u, _, err := e.users.FindByIdentity(context.Background(), domain.IdentityTypePhone, phone)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// 每种撤销都写一条 logged_out，reason 对得上。
func TestEventLoggedOutReasonPerRevocation(t *testing.T) {
	cases := []struct {
		reason string
		revoke func(e *eventEnv, res *service.LoginResult) error
	}{
		{domain.RevokeReasonLogout, func(e *eventEnv, res *service.LoginResult) error {
			return e.auth.Logout(context.Background(), e.app.AppID, res.Session.Token)
		}},
		{domain.RevokeReasonKick, func(e *eventEnv, res *service.LoginResult) error {
			_, err := e.auth.RevokeSession(context.Background(), e.app.AppID, res.User.ID, res.Session.ID, domain.RevokeReasonKick)
			return err
		}},
		{domain.RevokeReasonFreeze, func(e *eventEnv, res *service.LoginResult) error {
			_, err := e.accounts.SetStatus(context.Background(), res.User.ID, domain.UserStatusFrozen)
			return err
		}},
		{domain.RevokeReasonPasswordChanged, func(e *eventEnv, res *service.LoginResult) error {
			return e.accounts.ResetPassword(context.Background(), res.User.ID, "another-password-1")
		}},
		{domain.RevokeReasonDeletion, func(e *eventEnv, res *service.LoginResult) error {
			_, err := e.accounts.RequestDeletion(context.Background(), res.User.ID)
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			e := newEventEnv(t)
			res := e.loginWithRef(t, "13800000001", "dev-1")
			if err := c.revoke(e, res); err != nil {
				t.Fatal(err)
			}
			got := pendingEvents(t, e.events, e.app.ID)
			if len(got) != 2 || got[1].Kind != domain.EventLoggedOut || got[1].Reason != c.reason ||
				got[1].SessionID != res.Session.ID || got[1].ClientRef != "dev-1" || got[1].UserID != res.User.ID {
				t.Fatalf("事件 = %+v", got)
			}
		})
	}
}

func TestEventFreezeWritesOnePerSession(t *testing.T) {
	e := newEventEnv(t)
	a := e.loginWithRef(t, "13800000001", "dev-a")
	b := e.loginWithRef(t, "13800000001", "dev-b")
	if _, err := e.accounts.SetStatus(context.Background(), a.User.ID, domain.UserStatusFrozen); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, v := range pendingEvents(t, e.events, e.app.ID) {
		if v.Kind == domain.EventLoggedOut {
			out[v.SessionID] = v.Reason
		}
	}
	if len(out) != 2 || out[a.Session.ID] != domain.RevokeReasonFreeze || out[b.Session.ID] != domain.RevokeReasonFreeze {
		t.Fatalf("logged_out = %v", out)
	}
}

func TestEventAccountDeletedGoesToEveryEventApp(t *testing.T) {
	e := newEventEnv(t)
	other := newEventApp(t, e.apps, "other", true)
	off := newEventApp(t, e.apps, "off", false)
	res := e.loginWithRef(t, "13800000001", "dev-1")
	if _, err := e.accounts.RequestDeletion(context.Background(), res.User.ID); err != nil {
		t.Fatal(err)
	}
	e.backdateDeletion(t, res.User.ID, 30)
	if purged, _ := e.sweep(t); purged != 1 {
		t.Fatalf("purged = %d", purged)
	}
	for _, app := range []*domain.Application{e.app, other} {
		got := pendingEvents(t, e.events, app.ID)
		last := got[len(got)-1]
		if last.Kind != domain.EventAccountDeleted || last.UserID != res.User.ID {
			t.Fatalf("应用 %s 的事件 = %+v", app.Code, got)
		}
	}
	if got := pendingEvents(t, e.events, off.ID); len(got) != 0 {
		t.Fatalf("没开事件的应用也写了: %+v", got)
	}
}

func TestKickedTokenValidatesAsKicked(t *testing.T) {
	e := newEventEnv(t)
	ctx := context.Background()
	kicked := e.loginWithRef(t, "13800000001", "dev-1")
	loggedOut := e.loginWithRef(t, "13800000001", "dev-2")

	n, err := e.auth.RevokeSession(ctx, e.app.AppID, kicked.User.ID, kicked.Session.ID, domain.RevokeReasonKick)
	if err != nil || n != 1 {
		t.Fatalf("RevokeSession = %d, %v", n, err)
	}
	if err := e.auth.Logout(ctx, e.app.AppID, loggedOut.Session.Token); err != nil {
		t.Fatal(err)
	}
	_, err = e.auth.ValidateToken(ctx, e.app.AppID, kicked.Session.Token)
	wantCode(t, err, domain.CodeKicked, domain.ErrUnauthorized)
	_, err = e.auth.ValidateToken(ctx, e.app.AppID, loggedOut.Session.Token)
	wantCode(t, err, domain.CodeTokenInvalid, domain.ErrUnauthorized)

	// 墓碑认应用：别的应用拿到这个 token 不该知道它曾被踢。
	other := newEventApp(t, e.apps, "other", false)
	_, err = e.auth.ValidateToken(ctx, other.AppID, kicked.Session.Token)
	wantCode(t, err, domain.CodeTokenInvalid, domain.ErrUnauthorized)
}

func TestRevokeSessionOnlyTouchesCallerApp(t *testing.T) {
	e := newEventEnv(t)
	ctx := context.Background()
	res := e.loginWithRef(t, "13800000001", "dev-1")
	other := newEventApp(t, e.apps, "other", false)

	n, err := e.auth.RevokeSession(ctx, other.AppID, res.User.ID, res.Session.ID, domain.RevokeReasonKick)
	if err != nil || n != 0 {
		t.Fatalf("别的应用撤了本应用的会话: %d, %v", n, err)
	}
	if _, err := e.auth.ValidateToken(ctx, e.app.AppID, res.Session.Token); err != nil {
		t.Fatalf("会话被误撤: %v", err)
	}
	_, err = e.auth.RevokeSession(ctx, e.app.AppID, res.User.ID, res.Session.ID, domain.RevokeReasonLogout)
	wantCode(t, err, domain.CodeInvalidArgument, domain.ErrInvalidArgument)
}

// Review Focus 2：写完事件、撤销之前出故障，必须是「事件已写、会话还在」。
func TestEventWrittenBeforeRevokeSurvivesCrashInBetween(t *testing.T) {
	e := newEventEnv(t)
	res := e.loginWithRef(t, "13800000001", "dev-1")

	// 事件写成之后掐掉 ctx：随后的墓碑与删除会话都会失败，模拟写完事件就崩。
	ctx, cancel := context.WithCancel(context.Background())
	e.appender.set(nil, cancel)
	_, err := e.auth.RevokeSession(ctx, e.app.AppID, res.User.ID, res.Session.ID, domain.RevokeReasonKick)
	if err == nil {
		t.Fatal("撤销中途失败却返回成功")
	}
	e.appender.set(nil, nil)

	got := pendingEvents(t, e.events, e.app.ID)
	if len(got) != 2 || got[1].Kind != domain.EventLoggedOut || got[1].SessionID != res.Session.ID {
		t.Fatalf("事件 = %+v，期望 logged_out 已写", got)
	}
	if _, err := e.auth.ValidateToken(context.Background(), e.app.AppID, res.Session.Token); err != nil {
		t.Fatalf("会话应当还在: %v", err)
	}
}

// 反过来：事件写不进去就不撤销，绝不出现「会话没了、业务方不知道」。
func TestRevokeAbortsWhenEventCannotBeWritten(t *testing.T) {
	e := newEventEnv(t)
	res := e.loginWithRef(t, "13800000001", "dev-1")
	e.appender.set(func() error { return errors.New("事件表挂了") }, nil)
	if _, err := e.accounts.SetStatus(context.Background(), res.User.ID, domain.UserStatusFrozen); err == nil {
		t.Fatal("写不进事件却返回成功")
	}
	e.appender.set(nil, nil)
	if got := pendingEvents(t, e.events, e.app.ID); len(got) != 1 {
		t.Fatalf("事件 = %+v", got)
	}
	sessions, err := e.sess.ListByUser(context.Background(), res.User.ID)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("会话被撤了: %d, %v", len(sessions), err)
	}
}

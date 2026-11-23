package service_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

// expiryEnv 用同一个可控时钟驱动签发、校验与过期扫描。
type expiryEnv struct {
	pool   *pgxpool.Pool
	rdb    *redis.Client
	st     *store.SessionStore
	sess   *service.SessionService
	events *service.AppEvents
	apps   *service.ApplicationService
	app    *domain.Application
	clock  atomic.Int64
}

// 空闲 1 小时、上限 2 小时、轮换间隔等于上限（不轮换），延期降频 1 分钟。
var expiryPolicy = domain.SessionPolicy{
	IdleTimeoutSeconds: 3600, IdleTimeoutMobileSeconds: 3600, MaxLifetimeSeconds: 7200,
	RotateIntervalSeconds: 7200, ExtendIntervalSeconds: 60, TokenCacheTTLSeconds: 30,
}

func newExpiryEnv(t *testing.T) *expiryEnv {
	t.Helper()
	e := &expiryEnv{pool: testsupport.NewTestDB(t), rdb: testsupport.NewTestRedis(t)}
	e.clock.Store(time.Now().UnixMilli())
	reg := connector.NewRegistry()
	if err := reg.Register(connector.NewPassword(service.NewUserService(e.pool))); err != nil {
		t.Fatal(err)
	}
	e.apps = service.NewApplicationService(e.pool, reg)
	e.events = service.NewAppEvents(e.pool, e.apps, nil)
	e.st = store.NewSessionStore(e.rdb)
	e.sess = service.NewSessionServiceWithClock(e.st, store.NewRevokePublisher(e.rdb), store.NewEpochStore(e.rdb), e.now,
		service.WithSessionEvents(e.events), service.WithSessionApps(e.apps))
	app := newEventApp(t, e.apps, "expiry", true)
	var err error
	if e.app, err = e.apps.UpdateSessionPolicy(context.Background(), app.ID, expiryPolicy); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *expiryEnv) now() int64              { return e.clock.Load() }
func (e *expiryEnv) advance(d time.Duration) { e.clock.Add(d.Milliseconds()) }

func (e *expiryEnv) scanner() *service.SessionExpiry {
	return service.NewSessionExpiry(e.st, store.NewEpochStore(e.rdb), e.events, e.apps, service.SessionExpiryOptions{Now: e.now})
}

func (e *expiryEnv) issue(t *testing.T, ref string) *domain.Session {
	t.Helper()
	s, err := e.sess.Issue(context.Background(), service.IssueInput{UserID: uuid.New(), App: e.app, ClientRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *expiryEnv) sweep(t *testing.T) int {
	t.Helper()
	n, err := e.scanner().Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	return n
}

func (e *expiryEnv) expiredEvents(t *testing.T) []eventView {
	t.Helper()
	var out []eventView
	for _, v := range pendingEvents(t, e.events, e.app.ID) {
		if v.Kind == domain.EventLoggedOut && v.Reason == service.LoggedOutExpired {
			out = append(out, v)
		}
	}
	return out
}

func TestExpiryIdleTimeoutEmitsLoggedOut(t *testing.T) {
	e := newExpiryEnv(t)
	s := e.issue(t, "dev-1")
	e.advance(59 * time.Minute)
	if n := e.sweep(t); n != 0 {
		t.Fatalf("没到期就发了 %d 条", n)
	}
	e.advance(2 * time.Minute)
	if n := e.sweep(t); n != 1 {
		t.Fatalf("空闲到期发了 %d 条", n)
	}
	got := e.expiredEvents(t)
	if len(got) != 1 || got[0].SessionID != s.ID || got[0].ClientRef != "dev-1" || got[0].UserID != s.UserID {
		t.Fatalf("事件 = %+v", got)
	}
	if n := e.sweep(t); n != 0 {
		t.Fatalf("同一会话又发了 %d 条", n)
	}
}

// Review Focus 4：延期后到期集合的分值必须跟着更新，否则会在旧时刻被误报过期。
func TestExpiryExtendedSessionNotReportedEarly(t *testing.T) {
	e := newExpiryEnv(t)
	ctx := context.Background()
	s := e.issue(t, "dev-1")
	member := store.ExpiryMember(s)

	e.advance(50 * time.Minute)
	if _, err := e.sess.Validate(ctx, s.Token, e.app); err != nil {
		t.Fatal(err)
	}
	score, ok, err := e.st.ExpiryScore(ctx, member)
	if err != nil || !ok || score != e.now()+time.Hour.Milliseconds() {
		t.Fatalf("延期后分值 = %d (%v, %v)，want %d", score, ok, err, e.now()+time.Hour.Milliseconds())
	}

	// 旧到期时刻（签发后 1 小时）已过，但会话延期过了。
	e.advance(20 * time.Minute)
	if n := e.sweep(t); n != 0 {
		t.Fatalf("延期过的会话被误报过期 %d 条", n)
	}
	if _, err := e.sess.Validate(ctx, s.Token, e.app); err != nil {
		t.Fatalf("会话应当还有效: %v", err)
	}
}

// 分值不知为何落后（如老数据）时，扫描复核会话后按真实到期放回，而不是误报。
func TestExpiryStaleScoreIsPutBack(t *testing.T) {
	e := newExpiryEnv(t)
	ctx := context.Background()
	s := e.issue(t, "dev-1")
	member := store.ExpiryMember(s)
	if err := e.st.TrackExpiry(ctx, member, e.now()-1); err != nil {
		t.Fatal(err)
	}
	if n := e.sweep(t); n != 0 {
		t.Fatalf("会话还活着却报了过期 %d 条", n)
	}
	score, ok, _ := e.st.ExpiryScore(ctx, member)
	if !ok || score != s.IdleExpiresAt {
		t.Fatalf("放回的分值 = %d (%v)，want %d", score, ok, s.IdleExpiresAt)
	}
}

func TestExpiryMaxLifetimeEmitsLoggedOut(t *testing.T) {
	e := newExpiryEnv(t)
	ctx := context.Background()
	s := e.issue(t, "dev-1")
	// 一直在用：空闲超时不断后延，但绝对上限（签发后 2 小时）挪不动。
	for i := 0; i < 3; i++ {
		e.advance(35 * time.Minute)
		// 延期的去重锁按真实时间 10 秒过期，假时钟跨不过它，手动放掉。
		e.rdb.Del(ctx, "fp:lock:ext:"+s.Token)
		if _, err := e.sess.Validate(ctx, s.Token, e.app); err != nil {
			t.Fatal(err)
		}
		if n := e.sweep(t); n != 0 {
			t.Fatalf("第 %d 次延期后误报过期", i+1)
		}
	}
	score, _, _ := e.st.ExpiryScore(ctx, store.ExpiryMember(s))
	if want := s.FirstAuthAt + 2*time.Hour.Milliseconds(); score != want {
		t.Fatalf("分值 = %d，want 绝对上限 %d", score, want)
	}
	e.advance(16 * time.Minute)
	if n := e.sweep(t); n != 1 {
		t.Fatalf("绝对上限到期发了 %d 条", n)
	}
	if got := e.expiredEvents(t); len(got) != 1 || got[0].SessionID != s.ID {
		t.Fatalf("事件 = %+v", got)
	}
}

func TestExpiryRevokedSessionNotReported(t *testing.T) {
	e := newExpiryEnv(t)
	s := e.issue(t, "dev-1")
	if err := e.sess.Revoke(context.Background(), s.Token, domain.RevokeReasonLogout); err != nil {
		t.Fatal(err)
	}
	e.advance(3 * time.Hour)
	if n := e.sweep(t); n != 0 || len(e.expiredEvents(t)) != 0 {
		t.Fatalf("撤销过的会话报了过期: %d", n)
	}
}

// 会话 key 已被 Redis TTL 清掉（正常过期就是这样），只凭成员串也要发出事件。
func TestExpirySessionKeyGoneStillEmits(t *testing.T) {
	e := newExpiryEnv(t)
	s := e.issue(t, "dev:with:colons")
	if err := e.rdb.Del(context.Background(), "fp:sess:"+s.Token).Err(); err != nil {
		t.Fatal(err)
	}
	e.advance(61 * time.Minute)
	if n := e.sweep(t); n != 1 {
		t.Fatalf("发了 %d 条", n)
	}
	got := e.expiredEvents(t)
	if len(got) != 1 || got[0].SessionID != s.ID || got[0].ClientRef != "dev:with:colons" || got[0].UserID != s.UserID {
		t.Fatalf("事件 = %+v", got)
	}
}

// 90 天前集中登录造成的同时到期：一轮之内取满就接着取，全部发出。
func TestExpiryManyAtOnceAllInOneSweep(t *testing.T) {
	e := newExpiryEnv(t)
	for i := 0; i < 1200; i++ {
		e.issue(t, "")
	}
	e.advance(61 * time.Minute)
	if n := e.sweep(t); n != 1200 {
		t.Fatalf("一轮发了 %d 条，want 1200", n)
	}
	var count int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM app_event WHERE app_id = $1 AND kind = $2`, e.app.ID, domain.EventLoggedOut).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1200 {
		t.Fatalf("事件表里 %d 条", count)
	}
}

func TestExpiryConcurrentScannersEmitOnce(t *testing.T) {
	e := newExpiryEnv(t)
	for i := 0; i < 300; i++ {
		e.issue(t, "")
	}
	e.advance(61 * time.Minute)
	var wg sync.WaitGroup
	var total atomic.Int64
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sc := service.NewSessionExpiry(e.st, store.NewEpochStore(e.rdb), e.events, e.apps, service.SessionExpiryOptions{Now: e.now, Batch: 50})
			n, err := sc.Sweep(context.Background())
			if err != nil {
				t.Error(err)
			}
			total.Add(int64(n))
		}()
	}
	wg.Wait()
	var count, distinct int
	if err := e.pool.QueryRow(context.Background(),
		`SELECT count(*), count(DISTINCT payload->>'session_id') FROM app_event WHERE app_id = $1 AND kind = $2`,
		e.app.ID, domain.EventLoggedOut).Scan(&count, &distinct); err != nil {
		t.Fatal(err)
	}
	if total.Load() != 300 || count != 300 || distinct != 300 {
		t.Fatalf("两个扫描器共发 %d 条，事件表 %d 条（%d 个会话）", total.Load(), count, distinct)
	}
}

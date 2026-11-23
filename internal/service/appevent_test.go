package service_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

// eventApps 在真实 ApplicationService 之上按测试意愿打开事件开关：账号配置的落库不归本任务。
type eventApps struct {
	*service.ApplicationService
	on map[uuid.UUID]bool
}

func (a eventApps) GetByIDCached(ctx context.Context, id uuid.UUID) (*domain.Application, error) {
	app, err := a.ApplicationService.GetByIDCached(ctx, id)
	if err != nil {
		return nil, err
	}
	app.Account.Events = a.on[app.ID]
	return app, nil
}

func (a eventApps) List(ctx context.Context) ([]domain.Application, error) {
	apps, err := a.ApplicationService.List(ctx)
	for i := range apps {
		apps[i].Account.Events = a.on[apps[i].ID]
	}
	return apps, err
}

type appEventEnv struct {
	pool   *pgxpool.Pool
	apps   *service.ApplicationService
	events *service.AppEvents
	on     map[uuid.UUID]bool
}

func newAppEventEnv(t *testing.T) *appEventEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	apps := service.NewApplicationService(pool, connector.NewRegistry())
	on := map[uuid.UUID]bool{}
	return &appEventEnv{pool: pool, apps: apps, on: on,
		events: service.NewAppEvents(pool, eventApps{ApplicationService: apps, on: on}, nil)}
}

func (e *appEventEnv) newApp(t *testing.T, events bool) *domain.Application {
	t.Helper()
	suffix := uuid.NewString()[:8]
	app, _, err := e.apps.Create(context.Background(), "事件-"+suffix, "evt-"+suffix)
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	e.on[app.ID] = events
	return app
}

func TestAppEventAppendSkipsAppsWithoutEvents(t *testing.T) {
	env := newAppEventEnv(t)
	ctx := context.Background()
	app := env.newApp(t, false)

	if err := env.events.Append(ctx, app.ID, uuid.New(), domain.EventLoginSucceeded, map[string]string{"session_id": "s"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := env.events.Pending(ctx, app.ID, 0, 10)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("没开事件的应用写进了 %d 条事件", len(got))
	}
}

func TestAppEventConcurrentAppendSeqIsContiguous(t *testing.T) {
	env := newAppEventEnv(t)
	ctx := context.Background()
	app := env.newApp(t, true)

	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- env.events.Append(ctx, app.ID, uuid.New(), domain.EventLoggedOut, map[string]string{"reason": "logout"})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := env.events.Pending(ctx, app.ID, 0, 100)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(got) != n {
		t.Fatalf("Pending 返回 %d 条，期望 %d", len(got), n)
	}
	for i, ev := range got {
		if ev.Seq != int64(i+1) {
			t.Fatalf("第 %d 条 seq=%d，期望连续的 %d", i, ev.Seq, i+1)
		}
	}
}

func TestAppEventPendingAfterSeqAndAck(t *testing.T) {
	env := newAppEventEnv(t)
	ctx := context.Background()
	app := env.newApp(t, true)
	user := uuid.New()

	for i := 0; i < 3; i++ {
		if err := env.events.Append(ctx, app.ID, user, domain.EventLoginSucceeded, map[string]any{"i": i}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	all, err := env.events.Pending(ctx, app.ID, 0, 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("Pending = %d 条, err=%v", len(all), err)
	}
	if all[0].UserID != user || all[0].Kind != domain.EventLoginSucceeded || all[0].AppID != app.ID {
		t.Fatalf("事件字段不对: %+v", all[0])
	}
	var p map[string]int
	if err := json.Unmarshal(all[2].Payload, &p); err != nil || p["i"] != 2 {
		t.Fatalf("payload = %s, err=%v", all[2].Payload, err)
	}

	after, err := env.events.Pending(ctx, app.ID, all[0].Seq, 10)
	if err != nil || len(after) != 2 || after[0].Seq != all[1].Seq {
		t.Fatalf("Pending(after=%d) = %+v, err=%v", all[0].Seq, after, err)
	}

	if err := env.events.Ack(ctx, app.ID, []uuid.UUID{all[0].ID, all[2].ID}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	left, err := env.events.Pending(ctx, app.ID, 0, 10)
	if err != nil || len(left) != 1 || left[0].ID != all[1].ID {
		t.Fatalf("Ack 之后 Pending = %+v, err=%v", left, err)
	}

	// 别的应用拿着这条事件的 id 也确认不掉。
	other := env.newApp(t, true)
	if err := env.events.Ack(ctx, other.ID, []uuid.UUID{all[1].ID}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if left, _ := env.events.Pending(ctx, app.ID, 0, 10); len(left) != 1 {
		t.Fatal("别的应用确认掉了本应用的事件")
	}
}

func TestAppEventAppendForUserAppsWritesEveryEnabledActiveApp(t *testing.T) {
	env := newAppEventEnv(t)
	ctx := context.Background()
	a := env.newApp(t, true)
	b := env.newApp(t, true)
	off := env.newApp(t, false)
	disabled := env.newApp(t, true)
	if _, err := env.apps.SetStatus(ctx, disabled.ID, domain.ApplicationStatusDisabled); err != nil {
		t.Fatalf("停用应用: %v", err)
	}

	user := uuid.New()
	if err := env.events.AppendForUserApps(ctx, user, domain.EventAccountDeleted, struct{}{}); err != nil {
		t.Fatalf("AppendForUserApps: %v", err)
	}
	for _, c := range []struct {
		app  *domain.Application
		want int
	}{{a, 1}, {b, 1}, {off, 0}, {disabled, 0}} {
		got, err := env.events.Pending(ctx, c.app.ID, 0, 10)
		if err != nil {
			t.Fatalf("Pending: %v", err)
		}
		if len(got) != c.want {
			t.Fatalf("应用 %s 有 %d 条事件，期望 %d", c.app.Name, len(got), c.want)
		}
	}
}

func TestAppEventPruneDeletesOnlyOldAcked(t *testing.T) {
	env := newAppEventEnv(t)
	ctx := context.Background()
	app := env.newApp(t, true)
	for i := 0; i < 3; i++ {
		if err := env.events.Append(ctx, app.ID, uuid.New(), domain.EventLoggedOut, nil); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	evs, _ := env.events.Pending(ctx, app.ID, 0, 10)
	// 0：早已确认；1：刚确认；2：未确认但很旧。
	if err := env.events.Ack(ctx, app.ID, []uuid.UUID{evs[0].ID, evs[1].ID}); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE app_event SET acked_at = now() - interval '8 days' WHERE id = $1`, evs[0].ID); err != nil {
		t.Fatalf("改确认时间: %v", err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE app_event SET created_at = now() - interval '30 days' WHERE id = $1`, evs[2].ID); err != nil {
		t.Fatalf("改创建时间: %v", err)
	}

	n, err := env.events.Prune(ctx, time.Now().Add(-7*24*time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("Prune 删了 %d 条，期望 1", n)
	}
	var left []uuid.UUID
	rows, _ := env.pool.Query(ctx, `SELECT id FROM app_event WHERE app_id = $1 ORDER BY seq`, app.ID)
	for rows.Next() {
		var id uuid.UUID
		_ = rows.Scan(&id)
		left = append(left, id)
	}
	rows.Close()
	if len(left) != 2 || left[0] != evs[1].ID || left[1] != evs[2].ID {
		t.Fatalf("剩下 %v，期望刚确认的与未确认的两条", left)
	}
}

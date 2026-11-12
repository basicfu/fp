package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

const coolingOffMs = int64(15 * 24 * time.Hour / time.Millisecond)

// backdateDeletion 把提交时间往前拨 days 天，模拟冷静期过去。
func (e *authEnv) backdateDeletion(t *testing.T, userID uuid.UUID, days int) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(),
		`UPDATE app_user SET delete_submitted_at = delete_submitted_at - make_interval(days => $2) WHERE id = $1`,
		userID, days); err != nil {
		t.Fatalf("拨提交时间: %v", err)
	}
}

func (e *authEnv) deletionRevokeLogs(t *testing.T, userID uuid.UUID) int {
	t.Helper()
	all, err := e.logs.ListByUser(context.Background(), userID, 100)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	n := 0
	for _, l := range all {
		if l.Event == domain.LoginEventRevoke && l.Reason == domain.RevokeReasonDeletion {
			n++
		}
	}
	return n
}

func (e *authEnv) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func (e *authEnv) sweep(t *testing.T) (int, int) {
	t.Helper()
	purged, expired, err := e.accounts.SweepDeletions(context.Background())
	if err != nil {
		t.Fatalf("SweepDeletions: %v", err)
	}
	return purged, expired
}

// 提交注销：进入冷静期、记提交时间，该用户在每个应用的每个会话都作废，并留一条撤销审计。
func TestRequestDeletionEntersCoolingOffAndRevokesEverySession(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	first := e.passwordLogin(t, "13800138000")
	other, _, err := e.apps.Create(ctx, "另一个应用", "other-app")
	if err != nil {
		t.Fatalf("创建应用: %v", err)
	}
	second, err := e.sess.Issue(ctx, service.IssueInput{UserID: first.User.ID, App: other, Mobile: true})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	purgeAt, err := e.accounts.RequestDeletion(ctx, first.User.ID)
	if err != nil {
		t.Fatalf("RequestDeletion: %v", err)
	}
	u, err := e.users.GetByID(ctx, first.User.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if u.Status != domain.UserStatusPendingDelete || u.DeleteSubmittedAt == 0 {
		t.Fatalf("status = %s, submitted = %d, want PENDING_DELETE 且记下提交时间", u.Status, u.DeleteSubmittedAt)
	}
	if purgeAt != u.DeleteSubmittedAt+coolingOffMs {
		t.Fatalf("purgeAt = %d, want 提交时间 %d + 15 天", purgeAt, u.DeleteSubmittedAt)
	}
	if _, err := e.sess.Validate(ctx, first.Session.Token, e.app); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("登录应用的会话仍有效: %v", err)
	}
	if _, err := e.sess.Validate(ctx, second.Token, other); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("另一个应用的会话仍有效: %v", err)
	}
	if n := e.deletionRevokeLogs(t, u.ID); n != 1 {
		t.Fatalf("注销撤销审计 %d 条, want 1", n)
	}
}

// 冷静期内重复提交：返回原来的删除时间、不重新计时；顺带清掉漏网的会话，好让上一次半途失败的提交靠重试自愈。
func TestRequestDeletionIsIdempotentAndSelfHeals(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	res := e.passwordLogin(t, "13800138000")

	firstAt, err := e.accounts.RequestDeletion(ctx, res.User.ID)
	if err != nil {
		t.Fatalf("第一次提交: %v", err)
	}
	e.backdateDeletion(t, res.User.ID, 3)
	before, _ := e.users.GetByID(ctx, res.User.ID)

	againAt, err := e.accounts.RequestDeletion(ctx, res.User.ID)
	if err != nil {
		t.Fatalf("重复提交: %v", err)
	}
	after, _ := e.users.GetByID(ctx, res.User.ID)
	if after.DeleteSubmittedAt != before.DeleteSubmittedAt || againAt != before.DeleteSubmittedAt+coolingOffMs {
		t.Fatalf("重复提交重新计时了: submitted %d → %d, purgeAt %d", before.DeleteSubmittedAt, after.DeleteSubmittedAt, againAt)
	}
	if againAt != firstAt-3*24*int64(time.Hour/time.Millisecond) {
		t.Fatalf("againAt = %d, want 首次 %d 往前 3 天", againAt, firstAt)
	}
	if n := e.deletionRevokeLogs(t, res.User.ID); n != 1 {
		t.Fatalf("没撤到会话的重复提交不该再记审计, 共 %d 条", n)
	}

	// 模拟上一次提交停在"状态已改、会话未清"：重试必须把它撤掉。
	leftover, err := e.sess.Issue(ctx, service.IssueInput{UserID: res.User.ID, App: e.app})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := e.accounts.RequestDeletion(ctx, res.User.ID); err != nil {
		t.Fatalf("重试: %v", err)
	}
	if _, err := e.sess.Validate(ctx, leftover.Token, e.app); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("重试没有清掉漏网的会话: %v", err)
	}
	if n := e.deletionRevokeLogs(t, res.User.ID); n != 2 {
		t.Fatalf("撤到会话的重试要记审计, 共 %d 条, want 2", n)
	}
}

// 冷静期按配置算。
func TestRequestDeletionUsesConfiguredCoolingOff(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	res := e.passwordLogin(t, "13800138000")

	accounts := service.NewAccountService(e.users, e.sess, noopEpochs{}, e.logs, service.WithDeletionCoolingOff(30*24*time.Hour))
	purgeAt, err := accounts.RequestDeletion(ctx, res.User.ID)
	if err != nil {
		t.Fatalf("RequestDeletion: %v", err)
	}
	u, _ := e.users.GetByID(ctx, res.User.ID)
	if want := u.DeleteSubmittedAt + int64(30*24*time.Hour/time.Millisecond); purgeAt != want {
		t.Fatalf("purgeAt = %d, want %d", purgeAt, want)
	}
}

type noopEpochs struct{}

func (noopEpochs) Bump(context.Context, uuid.UUID) (int64, error) { return 0, nil }

func TestRequestDeletionRejectsFrozenDeletedAndMissing(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	frozen := e.passwordLogin(t, "13800138001")
	if _, err := e.accounts.SetStatus(ctx, frozen.User.ID, domain.UserStatusFrozen); err != nil {
		t.Fatalf("冻结: %v", err)
	}
	_, err := e.accounts.RequestDeletion(ctx, frozen.User.ID)
	wantCode(t, err, domain.CodeAccountFrozen, domain.ErrForbidden)
	if u, _ := e.users.GetByID(ctx, frozen.User.ID); u.Status != domain.UserStatusFrozen {
		t.Fatalf("被拒的提交改了状态: %s", u.Status)
	}

	gone := e.passwordLogin(t, "13800138002")
	if _, err := e.accounts.RequestDeletion(ctx, gone.User.ID); err != nil {
		t.Fatalf("RequestDeletion: %v", err)
	}
	e.backdateDeletion(t, gone.User.ID, 16)
	e.sweep(t)
	_, err = e.accounts.RequestDeletion(ctx, gone.User.ID)
	wantCode(t, err, domain.CodeAccountUnavailable, domain.ErrForbidden)

	_, err = e.accounts.RequestDeletion(ctx, uuid.New())
	wantCode(t, err, domain.CodeUserNotFound, domain.ErrNotFound)
}

// 控制台不能把人直接置成 DELETED：那会跳过归档和删标识，留下一个永远不会被清理的半截账号。
func TestAccountSetStatusRefusesDeleted(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	res := e.passwordLogin(t, "13800138000")
	if _, err := e.accounts.SetStatus(ctx, res.User.ID, domain.UserStatusPendingDelete); err != nil {
		t.Fatalf("置 PENDING_DELETE: %v", err)
	}
	_, err := e.accounts.SetStatus(ctx, res.User.ID, domain.UserStatusDeleted)
	wantCode(t, err, domain.CodeInvalidArgument, domain.ErrInvalidArgument)
}

// 冷静期内登录即撤销注销；期满之后清理任务不再碰这个账号。
func TestLoginDuringCoolingOffSurvivesSweep(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	res := e.passwordLogin(t, "13800138000")
	if _, err := e.accounts.RequestDeletion(ctx, res.User.ID); err != nil {
		t.Fatalf("RequestDeletion: %v", err)
	}
	e.backdateDeletion(t, res.User.ID, 14)

	again := e.passwordLogin(t, "13800138000")
	if again.User.ID != res.User.ID || again.User.Status != domain.UserStatusActive {
		t.Fatalf("登录没有撤销注销: id %s → %s, status %s", res.User.ID, again.User.ID, again.User.Status)
	}
	// 撤销后的账号哪怕"提交时间"早已过了冷静期也不该被清理——复活时提交时间已清空。
	e.backdateDeletion(t, res.User.ID, 30)
	if purged, _ := e.sweep(t); purged != 0 {
		t.Fatalf("撤销了注销的账号被清理了 %d 个", purged)
	}
	p, err := e.users.GetProfile(ctx, res.User.ID)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if p.User.Status != domain.UserStatusActive || len(p.Identities) != 1 {
		t.Fatalf("账号被动过: status %s, identities %d", p.User.Status, len(p.Identities))
	}
}

// 期满生效：归档最少信息，删标识、注册关系、角色、头像，清资料，留墓碑；手机号释放。
func TestSweepPurgesDueDeletionAndArchives(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	const phone = "13800138000"

	res := e.passwordLogin(t, phone)
	uid := res.User.ID
	if _, err := e.users.AttachIdentity(ctx, uid, service.EnsureIdentityInput{
		Type: domain.IdentityTypeWechatWeb, Subject: "openid-web", UnionKey: "union-1", Credential: "wx-token",
	}); err != nil {
		t.Fatalf("挂微信: %v", err)
	}
	nickname, gender := "小明", domain.GenderMale
	if _, err := e.users.UpdateProfile(ctx, uid, service.UserProfileUpdate{Nickname: &nickname, Gender: &gender}); err != nil {
		t.Fatalf("改资料: %v", err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("deletion-avatar")...)
	if _, err := e.users.UpdateAvatar(ctx, uid, png); err != nil {
		t.Fatalf("传头像: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE app_user SET avatar_url = 'https://wx.example/a.png' WHERE id = $1`, uid); err != nil {
		t.Fatalf("写外部头像: %v", err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO user_role (user_id, roles) VALUES ($1, '{ADMIN}')`, uid); err != nil {
		t.Fatalf("授角色: %v", err)
	}
	// 不相干的用户，确认清理只动到期的那一个。
	bystander := e.passwordLogin(t, "13900139000")

	if _, err := e.accounts.RequestDeletion(ctx, uid); err != nil {
		t.Fatalf("RequestDeletion: %v", err)
	}
	if purged, _ := e.sweep(t); purged != 0 {
		t.Fatalf("冷静期没满就清理了 %d 个", purged)
	}
	e.backdateDeletion(t, uid, 14)
	if purged, _ := e.sweep(t); purged != 0 {
		t.Fatalf("第 14 天就清理了 %d 个", purged)
	}
	e.backdateDeletion(t, uid, 1)
	submitted, _ := e.users.GetByID(ctx, uid)

	if purged, _ := e.sweep(t); purged != 1 {
		t.Fatalf("清理了 %d 个, want 1", purged)
	}

	p, err := e.users.GetProfile(ctx, uid)
	if err != nil {
		t.Fatalf("墓碑应当还在: %v", err)
	}
	u := p.User
	if u.Status != domain.UserStatusDeleted || len(p.Identities) != 0 || p.RegisteredAppID != "" {
		t.Fatalf("墓碑 status %s, identities %d, 注册来源 %q", u.Status, len(p.Identities), p.RegisteredAppID)
	}
	if u.Nickname != "" || u.PasswordHash != "" || u.Gender != "" || u.AvatarURL != "" || u.AvatarHash != "" || u.DeleteSubmittedAt != 0 {
		t.Fatalf("墓碑上还留着资料: %+v", u)
	}
	for table, sql := range map[string]string{
		"identity":    `SELECT count(*) FROM identity WHERE user_id = $1`,
		"user_extra":  `SELECT count(*) FROM user_extra WHERE user_id = $1`,
		"user_role":   `SELECT count(*) FROM user_role WHERE user_id = $1`,
		"user_avatar": `SELECT count(*) FROM user_avatar WHERE user_id = $1`,
	} {
		if n := e.count(t, sql, uid); n != 0 {
			t.Errorf("%s 还剩 %d 行", table, n)
		}
	}
	if _, _, err := e.users.GetAvatar(ctx, domain.AvatarHash(png)); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("头像还能按哈希取到: %v", err)
	}
	// 登录日志留到归档期满。
	if n := e.count(t, `SELECT count(*) FROM login_log WHERE user_id = $1`, uid); n == 0 {
		t.Error("登录日志不该随注销生效删除")
	}

	var (
		phones, unions []string
		submittedMs    int64
		withinSecond   bool
		sixMonths      bool
	)
	if err := e.pool.QueryRow(ctx, `
		SELECT phones, wechat_union_ids, (extract(epoch FROM submitted_at) * 1000)::bigint,
		       abs(extract(epoch FROM deleted_at - now())) < 5,
		       expires_at = deleted_at + interval '6 months'
		FROM user_deletion_archive WHERE user_id = $1`, uid).
		Scan(&phones, &unions, &submittedMs, &withinSecond, &sixMonths); err != nil {
		t.Fatalf("读归档: %v", err)
	}
	if len(phones) != 1 || phones[0] != phone || len(unions) != 1 || unions[0] != "union-1" {
		t.Fatalf("归档 phones %v, union ids %v", phones, unions)
	}
	if submittedMs != submitted.DeleteSubmittedAt || !withinSecond || !sixMonths {
		t.Fatalf("归档时间不对: submitted %d (want %d), deleted≈now %v, expires=+6mo %v",
			submittedMs, submitted.DeleteSubmittedAt, withinSecond, sixMonths)
	}

	// 手机号释放：再登录是一个全新的用户。
	fresh, _, created, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{Type: domain.IdentityTypePhone, Subject: phone})
	if err != nil || !created || fresh.ID == uid {
		t.Fatalf("手机号没有释放: created=%v id=%v err=%v", created, fresh, err)
	}
	if again, _, created, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypeWechatWeb, Subject: "openid-web", UnionKey: "union-1",
	}); err != nil || !created || again.ID == uid {
		t.Fatalf("微信没有释放: created=%v err=%v", created, err)
	}

	if b, _ := e.users.GetByID(ctx, bystander.User.ID); b.Status != domain.UserStatusActive {
		t.Fatalf("不相干的用户被动了: %s", b.Status)
	}
	// 已生效的不会被再处理一次。
	if purged, _ := e.sweep(t); purged != 0 {
		t.Fatalf("重复清理了 %d 个", purged)
	}
}

// 归档期满：删归档、该用户的登录日志和墓碑，之后查无此人；没到期的归档不动。
func TestSweepExpiresArchive(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	var ids []uuid.UUID
	for _, phone := range []string{"13800138000", "13900139000"} {
		res := e.passwordLogin(t, phone)
		if _, err := e.accounts.RequestDeletion(ctx, res.User.ID); err != nil {
			t.Fatalf("RequestDeletion: %v", err)
		}
		e.backdateDeletion(t, res.User.ID, 15)
		ids = append(ids, res.User.ID)
	}
	if purged, expired := e.sweep(t); purged != 2 || expired != 0 {
		t.Fatalf("purged %d expired %d, want 2 / 0", purged, expired)
	}
	expiredID, keptID := ids[0], ids[1]
	if _, err := e.pool.Exec(ctx,
		`UPDATE user_deletion_archive SET expires_at = now() - interval '1 second' WHERE user_id = $1`, expiredID); err != nil {
		t.Fatalf("拨到期时间: %v", err)
	}

	if _, expired := e.sweep(t); expired != 1 {
		t.Fatalf("expired %d, want 1", expired)
	}
	_, err := e.users.GetProfile(ctx, expiredID)
	wantCode(t, err, domain.CodeUserNotFound, domain.ErrNotFound)
	if n := e.count(t, `SELECT count(*) FROM user_deletion_archive WHERE user_id = $1`, expiredID); n != 0 {
		t.Fatalf("归档还在")
	}
	if n := e.count(t, `SELECT count(*) FROM login_log WHERE user_id = $1`, expiredID); n != 0 {
		t.Fatalf("登录日志还剩 %d 条", n)
	}

	if u, err := e.users.GetByID(ctx, keptID); err != nil || u.Status != domain.UserStatusDeleted {
		t.Fatalf("没到期的墓碑被动了: %v %v", u, err)
	}
	if n := e.count(t, `SELECT count(*) FROM user_deletion_archive WHERE user_id = $1`, keptID); n != 1 {
		t.Fatalf("没到期的归档被删了")
	}
	if n := e.count(t, `SELECT count(*) FROM login_log WHERE user_id = $1`, keptID); n == 0 {
		t.Fatalf("没到期用户的登录日志被删了")
	}
}

// 多个实例同时跑清理：每个到期的用户恰好被处理一次。
func TestSweepIsSafeAcrossConcurrentInstances(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()

	const n = 30
	for i := 0; i < n; i++ {
		u := newActiveUserWith(t, e.users)
		if _, err := e.accounts.RequestDeletion(ctx, u.ID); err != nil {
			t.Fatalf("RequestDeletion: %v", err)
		}
	}
	if _, err := e.pool.Exec(ctx,
		`UPDATE app_user SET delete_submitted_at = now() - interval '16 days' WHERE status = $1`, domain.UserStatusPendingDelete); err != nil {
		t.Fatalf("拨提交时间: %v", err)
	}

	// 每个"实例"一套独立的 AccountService，只共享数据库，和多进程部署一样。
	const instances = 4
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total int
		errs  []error
	)
	start := make(chan struct{})
	for i := 0; i < instances; i++ {
		acc := service.NewAccountService(service.NewUserService(e.pool), e.sess, noopEpochs{}, e.logs)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			purged, _, err := acc.SweepDeletions(ctx)
			mu.Lock()
			defer mu.Unlock()
			total += purged
			if err != nil {
				errs = append(errs, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(errs) != 0 {
		t.Fatalf("并发清理出错: %v", errs)
	}
	if total != n {
		t.Fatalf("各实例合计处理 %d 个, want %d", total, n)
	}
	if got := e.count(t, `SELECT count(*) FROM user_deletion_archive`); got != n {
		t.Fatalf("归档 %d 行, want %d", got, n)
	}
	if got := e.count(t, `SELECT count(*) FROM app_user WHERE status = $1`, domain.UserStatusDeleted); got != n {
		t.Fatalf("DELETED %d 个, want %d", got, n)
	}
}

// 登录复活与注销生效撞在一起：清理在行锁里置了 DELETED，排在它后面的复活不能把墓碑写回 ACTIVE。
func TestReviveDoesNotOverwriteConcurrentPurge(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	u := newActiveUserWith(t, e.users)
	if _, err := e.accounts.RequestDeletion(ctx, u.ID); err != nil {
		t.Fatalf("RequestDeletion: %v", err)
	}

	// 手动开一个事务替代清理任务：拿住行锁、置 DELETED，先不提交。
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `UPDATE app_user SET status = $2 WHERE id = $1`, u.ID, domain.UserStatusDeleted); err != nil {
		t.Fatalf("置 DELETED: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		// 读到的还是已提交的 PENDING_DELETE，写的时候卡在行锁上。
		_, err := e.users.SetStatus(ctx, u.ID, domain.UserStatusActive)
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	select {
	case err := <-done:
		if !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("复活 err = %v, want 被拒", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("复活一直没返回")
	}
	if fresh, _ := e.users.GetByID(ctx, u.ID); fresh.Status != domain.UserStatusDeleted {
		t.Fatalf("墓碑被复活成了 %s", fresh.Status)
	}
}

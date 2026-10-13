package service_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

// 两张内容不同的合法图片：只有文件头是真的，fp 不解码，后面的字节无所谓。
var (
	testPNG  = append([]byte("\x89PNG\r\n\x1a\n"), []byte("fp-avatar-one")...)
	testJPEG = append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("fp-avatar-two")...)
)

type profileEnv struct {
	users *service.UserService
	pool  *pgxpool.Pool
}

func newProfileEnv(t *testing.T) *profileEnv {
	t.Helper()
	pool := testsupport.NewTestDB(t)
	return &profileEnv{users: service.NewUserService(pool), pool: pool}
}

func (e *profileEnv) newUser(t *testing.T, phone, nickname string) *domain.User {
	t.Helper()
	u, _, _, err := e.users.EnsureUserWithIdentity(context.Background(), service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: phone, Nickname: nickname,
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	return u
}

// storedAvatar 直接读两张表，返回 app_user.avatar_hash 与 user_avatar 那一行（没有时 rows 为 0）。
func (e *profileEnv) storedAvatar(t *testing.T, id uuid.UUID) (userHash string, rows int, rowHash, contentType string, data []byte) {
	t.Helper()
	ctx := context.Background()
	if err := e.pool.QueryRow(ctx, `SELECT avatar_hash FROM app_user WHERE id = $1`, id).Scan(&userHash); err != nil {
		t.Fatalf("读 app_user.avatar_hash: %v", err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM user_avatar WHERE user_id = $1`, id).Scan(&rows); err != nil {
		t.Fatalf("数 user_avatar: %v", err)
	}
	if rows == 1 {
		if err := e.pool.QueryRow(ctx,
			`SELECT hash, content_type, data FROM user_avatar WHERE user_id = $1`, id).Scan(&rowHash, &contentType, &data); err != nil {
			t.Fatalf("读 user_avatar: %v", err)
		}
	}
	return
}

func wantCode(t *testing.T, err error, code string, sentinel error) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v 的哨兵不是 %v", err, sentinel)
	}
}

func TestUpdateAvatarStoresReplacesAndClears(t *testing.T) {
	e := newProfileEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138000", "小明")

	// 存
	p, err := e.users.UpdateAvatar(ctx, u.ID, testPNG)
	if err != nil {
		t.Fatalf("UpdateAvatar: %v", err)
	}
	pngHash := domain.AvatarHash(testPNG)
	if p.User.AvatarHash != pngHash {
		t.Fatalf("返回的 AvatarHash = %q, want %q", p.User.AvatarHash, pngHash)
	}
	userHash, rows, rowHash, ct, data := e.storedAvatar(t, u.ID)
	if userHash != pngHash || rows != 1 || rowHash != pngHash || ct != "image/png" || !bytes.Equal(data, testPNG) {
		t.Fatalf("落库不一致: app_user=%q rows=%d user_avatar=%q type=%q 字节相同=%v",
			userHash, rows, rowHash, ct, bytes.Equal(data, testPNG))
	}
	gotType, gotImage, err := e.users.GetAvatar(ctx, pngHash)
	if err != nil || gotType != "image/png" || !bytes.Equal(gotImage, testPNG) {
		t.Fatalf("GetAvatar = %q, %d 字节, %v——取回的应与存入的逐字节相同", gotType, len(gotImage), err)
	}

	// 替换：仍然只有一行，旧哈希取不到了
	p, err = e.users.UpdateAvatar(ctx, u.ID, testJPEG)
	if err != nil {
		t.Fatalf("替换头像: %v", err)
	}
	jpegHash := domain.AvatarHash(testJPEG)
	userHash, rows, rowHash, ct, _ = e.storedAvatar(t, u.ID)
	if p.User.AvatarHash != jpegHash || userHash != jpegHash || rows != 1 || rowHash != jpegHash || ct != "image/jpeg" {
		t.Fatalf("替换后不一致: 返回=%q app_user=%q rows=%d user_avatar=%q type=%q",
			p.User.AvatarHash, userHash, rows, rowHash, ct)
	}
	_, _, err = e.users.GetAvatar(ctx, pngHash)
	wantCode(t, err, domain.CodeAvatarNotFound, domain.ErrNotFound)

	// 清除：nil 与空切片等价
	for _, empty := range [][]byte{nil, {}} {
		if _, err := e.users.UpdateAvatar(ctx, u.ID, testJPEG); err != nil {
			t.Fatalf("重新设置头像: %v", err)
		}
		p, err = e.users.UpdateAvatar(ctx, u.ID, empty)
		if err != nil {
			t.Fatalf("清除头像: %v", err)
		}
		userHash, rows, _, _, _ = e.storedAvatar(t, u.ID)
		if p.User.AvatarHash != "" || userHash != "" || rows != 0 {
			t.Fatalf("清除后: 返回=%q app_user=%q rows=%d，都应为空", p.User.AvatarHash, userHash, rows)
		}
		_, _, err = e.users.GetAvatar(ctx, jpegHash)
		wantCode(t, err, domain.CodeAvatarNotFound, domain.ErrNotFound)
	}
}

func TestUpdateAvatarSameImageSameHash(t *testing.T) {
	e := newProfileEnv(t)
	ctx := context.Background()
	a := e.newUser(t, "13800138000", "甲")
	b := e.newUser(t, "13800138001", "乙")

	first, err := e.users.UpdateAvatar(ctx, a.ID, testPNG)
	if err != nil {
		t.Fatalf("UpdateAvatar: %v", err)
	}
	again, err := e.users.UpdateAvatar(ctx, a.ID, testPNG)
	if err != nil {
		t.Fatalf("重复上传: %v", err)
	}
	if first.User.AvatarHash == "" || first.User.AvatarHash != again.User.AvatarHash {
		t.Fatalf("同一张图两次上传哈希应相同: %q vs %q", first.User.AvatarHash, again.User.AvatarHash)
	}

	// 另一个用户传同一张图：各存一行；其中一人清除后，另一人的图仍取得到。
	other, err := e.users.UpdateAvatar(ctx, b.ID, testPNG)
	if err != nil {
		t.Fatalf("另一用户上传: %v", err)
	}
	if other.User.AvatarHash != first.User.AvatarHash {
		t.Fatal("不同用户传同一张图，哈希应相同")
	}
	if _, err := e.users.UpdateAvatar(ctx, a.ID, nil); err != nil {
		t.Fatalf("清除: %v", err)
	}
	if _, _, err := e.users.GetAvatar(ctx, other.User.AvatarHash); err != nil {
		t.Fatalf("甲清除头像后，乙的同一张图应仍取得到: %v", err)
	}
}

func TestUpdateAvatarRejectsInvalidImageAndKeepsOld(t *testing.T) {
	e := newProfileEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138000", "小明")
	if _, err := e.users.UpdateAvatar(ctx, u.ID, testPNG); err != nil {
		t.Fatalf("UpdateAvatar: %v", err)
	}

	tooBig := make([]byte, domain.AvatarMaxBytes+1)
	copy(tooBig, testPNG)
	for name, image := range map[string][]byte{
		"GIF":  []byte("GIF89a\x01\x00\x01\x00"),
		"超过上限": tooBig,
		"随机字节": []byte("definitely not an image"),
		"一个空格": []byte(" "),
	} {
		_, err := e.users.UpdateAvatar(ctx, u.ID, image)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != domain.CodeAvatarInvalid {
			t.Fatalf("%s: err = %v, want AVATAR_INVALID", name, err)
		}
	}
	userHash, rows, rowHash, _, _ := e.storedAvatar(t, u.ID)
	if want := domain.AvatarHash(testPNG); userHash != want || rows != 1 || rowHash != want {
		t.Fatalf("被拒的上传不该动到原头像: app_user=%q rows=%d user_avatar=%q", userHash, rows, rowHash)
	}
}

func TestDeletingUserCascadesAvatar(t *testing.T) {
	e := newProfileEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138000", "小明")
	p, err := e.users.UpdateAvatar(ctx, u.ID, testPNG)
	if err != nil {
		t.Fatalf("UpdateAvatar: %v", err)
	}

	if _, err := e.pool.Exec(ctx, `DELETE FROM app_user WHERE id = $1`, u.ID); err != nil {
		t.Fatalf("删用户: %v", err)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM user_avatar`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("删用户后 user_avatar 还剩 %d 行", n)
	}
	_, _, err = e.users.GetAvatar(ctx, p.User.AvatarHash)
	wantCode(t, err, domain.CodeAvatarNotFound, domain.ErrNotFound)
}

func TestProfileOpsOnMissingUser(t *testing.T) {
	e := newProfileEnv(t)
	ctx := context.Background()
	ghost := uuid.New()
	name := "小明"

	_, err := e.users.GetProfile(ctx, ghost)
	wantCode(t, err, domain.CodeUserNotFound, domain.ErrNotFound)
	_, err = e.users.UpdateProfile(ctx, ghost, service.UserProfileUpdate{Nickname: &name})
	wantCode(t, err, domain.CodeUserNotFound, domain.ErrNotFound)
	_, err = e.users.UpdateProfile(ctx, ghost, service.UserProfileUpdate{})
	wantCode(t, err, domain.CodeUserNotFound, domain.ErrNotFound)
	_, err = e.users.UpdateAvatar(ctx, ghost, testPNG)
	wantCode(t, err, domain.CodeUserNotFound, domain.ErrNotFound)
	_, err = e.users.UpdateAvatar(ctx, ghost, nil)
	wantCode(t, err, domain.CodeUserNotFound, domain.ErrNotFound)

	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM user_avatar`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("用户不存在时不该留下头像行，实际 %d 行", n)
	}
}

func TestGetAvatarUnknownOrMalformedHash(t *testing.T) {
	e := newProfileEnv(t)
	for _, hash := range []string{"", "abc", strings.Repeat("0", 32), strings.Repeat("Z", 32), "' OR 1=1 --"} {
		_, _, err := e.users.GetAvatar(context.Background(), hash)
		wantCode(t, err, domain.CodeAvatarNotFound, domain.ErrNotFound)
	}
}

func TestUpdateProfilePartialUpdate(t *testing.T) {
	e := newProfileEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138000", "原昵称")

	male, female, empty := domain.GenderMale, domain.GenderFemale, ""
	p, err := e.users.UpdateProfile(ctx, u.ID, service.UserProfileUpdate{Gender: &male})
	if err != nil {
		t.Fatalf("只改性别: %v", err)
	}
	if p.User.Nickname != "原昵称" || p.User.Gender != domain.GenderMale {
		t.Fatalf("只改性别后 nickname=%q gender=%q——昵称不该变", p.User.Nickname, p.User.Gender)
	}

	name := "  新昵称  "
	p, err = e.users.UpdateProfile(ctx, u.ID, service.UserProfileUpdate{Nickname: &name})
	if err != nil {
		t.Fatalf("只改昵称: %v", err)
	}
	if p.User.Nickname != "新昵称" || p.User.Gender != domain.GenderMale {
		t.Fatalf("只改昵称后 nickname=%q gender=%q——性别不该变，昵称应去掉首尾空白", p.User.Nickname, p.User.Gender)
	}

	both := "两个都改"
	p, err = e.users.UpdateProfile(ctx, u.ID, service.UserProfileUpdate{Nickname: &both, Gender: &female})
	if err != nil {
		t.Fatalf("同时改: %v", err)
	}
	if p.User.Nickname != both || p.User.Gender != domain.GenderFemale {
		t.Fatalf("同时改后 nickname=%q gender=%q", p.User.Nickname, p.User.Gender)
	}

	// 空串是合法的性别取值（未设置），与"没带这个字段"是两回事。
	p, err = e.users.UpdateProfile(ctx, u.ID, service.UserProfileUpdate{Gender: &empty})
	if err != nil {
		t.Fatalf("清空性别: %v", err)
	}
	if p.User.Nickname != both || p.User.Gender != "" {
		t.Fatalf("清空性别后 nickname=%q gender=%q", p.User.Nickname, p.User.Gender)
	}

	// 什么都不带：不改，照样返回完整资料。
	p, err = e.users.UpdateProfile(ctx, u.ID, service.UserProfileUpdate{})
	if err != nil {
		t.Fatalf("空更新: %v", err)
	}
	if p.User.Nickname != both || p.User.Gender != "" {
		t.Fatalf("空更新改动了资料: nickname=%q gender=%q", p.User.Nickname, p.User.Gender)
	}

	// 落库的就是返回的。
	fresh, err := e.users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fresh.Nickname != both || fresh.Gender != "" {
		t.Fatalf("落库值 nickname=%q gender=%q", fresh.Nickname, fresh.Gender)
	}
}

func TestUpdateProfileRejectsInvalidInput(t *testing.T) {
	e := newProfileEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138000", "原昵称")
	male := domain.GenderMale
	if _, err := e.users.UpdateProfile(ctx, u.ID, service.UserProfileUpdate{Gender: &male}); err != nil {
		t.Fatalf("准备性别: %v", err)
	}

	for _, nickname := range []string{"", "   ", strings.Repeat("字", 33), "两\n行", "控制\x00字符", "tab\t键"} {
		_, err := e.users.UpdateProfile(ctx, u.ID, service.UserProfileUpdate{Nickname: &nickname})
		wantCode(t, err, domain.CodeNicknameInvalid, domain.ErrInvalidArgument)
	}
	for _, gender := range []string{"male", "M", "1", "OTHER", " MALE"} {
		_, err := e.users.UpdateProfile(ctx, u.ID, service.UserProfileUpdate{Gender: &gender})
		wantCode(t, err, domain.CodeInvalidArgument, domain.ErrInvalidArgument)
	}

	// 一个字段不合法时整次更新都不生效：不能昵称改了、性别报错。
	good, bad := "新昵称", "OTHER"
	_, err := e.users.UpdateProfile(ctx, u.ID, service.UserProfileUpdate{Nickname: &good, Gender: &bad})
	wantCode(t, err, domain.CodeInvalidArgument, domain.ErrInvalidArgument)

	fresh, err := e.users.GetByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if fresh.Nickname != "原昵称" || fresh.Gender != domain.GenderMale {
		t.Fatalf("被拒的更新改动了资料: nickname=%q gender=%q", fresh.Nickname, fresh.Gender)
	}
}

func TestGetProfileListsEveryIdentityAndPasswordState(t *testing.T) {
	e := newProfileEnv(t)
	ctx := context.Background()
	u := e.newUser(t, "13800138000", "小明")
	if _, err := e.users.AttachIdentity(ctx, u.ID, service.EnsureIdentityInput{
		Type: domain.IdentityTypeEmail, Subject: "Ming@Example.com",
	}); err != nil {
		t.Fatalf("挂邮箱: %v", err)
	}
	if _, err := e.users.AttachIdentity(ctx, u.ID, service.EnsureIdentityInput{
		Type: domain.IdentityTypeWechatMP, Subject: "openid-1", UnionKey: "union-1", Credential: "wx-refresh-token",
	}); err != nil {
		t.Fatalf("挂微信: %v", err)
	}

	p, err := e.users.GetProfile(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	got := map[string]string{}
	for _, id := range p.Identities {
		got[id.Type] = id.Subject
	}
	want := map[string]string{
		domain.IdentityTypePhone: "13800138000", domain.IdentityTypeEmail: "ming@example.com",
		domain.IdentityTypeWechatMP: "openid-1",
	}
	if len(got) != len(want) {
		t.Fatalf("identities = %v, want %v", got, want)
	}
	for typ, subject := range want {
		if got[typ] != subject {
			t.Fatalf("identities[%s] = %q, want %q（手机号等标识返回完整值）", typ, got[typ], subject)
		}
	}
	if p.User.PasswordHash != "" {
		t.Fatal("没设过密码的用户 PasswordHash 应为空")
	}
	if p.RegisteredAppID != "" {
		t.Fatalf("没经登录流程建的号不该有注册来源: %q", p.RegisteredAppID)
	}

	if err := e.users.SetPassword(ctx, u.ID, authTestPassword); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	p, err = e.users.GetProfile(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if p.User.PasswordHash == "" {
		t.Fatal("设过密码后 PasswordHash 应非空（传输层据此给出 has_password）")
	}
}

// ---- 注册来源 ----

// newLoginApp 再建一个开了 password 与 sms_code 的应用。
func (e *authEnv) newLoginApp(t *testing.T, name, code string) *domain.Application {
	t.Helper()
	ctx := context.Background()
	app, _, err := e.apps.Create(ctx, name, code)
	if err != nil {
		t.Fatalf("创建应用 %s: %v", name, err)
	}
	for _, typ := range []string{connector.TypePassword, connector.TypeSMSCode} {
		if err := e.apps.SetConnector(ctx, app.ID, typ, true, nil); err != nil {
			t.Fatalf("启用 %s: %v", typ, err)
		}
	}
	return app
}

func (e *authEnv) smsLoginTo(t *testing.T, app *domain.Application, phone string) *service.LoginResult {
	t.Helper()
	ctx := context.Background()
	if err := e.auth.SendLoginCode(ctx, app.AppID, phone); err != nil {
		t.Fatalf("SendLoginCode: %v", err)
	}
	res, err := e.auth.Login(ctx, service.LoginInput{
		AppID: app.AppID, ConnectorType: connector.TypeSMSCode,
		Credentials: connector.Credentials{"phone": phone, "code": e.sms.LastCode()},
	})
	if err != nil {
		t.Fatalf("Login(sms_code) 到 %s: %v", app.Name, err)
	}
	return res
}

func (e *authEnv) registeredAppID(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	p, err := e.users.GetProfile(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	return p.RegisteredAppID
}

func TestRegistrationSourceIsTheAppThatCreatedTheUser(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	appB := e.newLoginApp(t, "应用B", "app-b")

	first := e.smsLoginTo(t, e.app, "13800138000")
	if got := e.registeredAppID(t, first.User.ID); got != e.app.AppID {
		t.Fatalf("registered_app_id = %q, want 应用 A 的 %q", got, e.app.AppID)
	}

	// 再登录应用 B、再回来登录 A：来源不变。
	second := e.smsLoginTo(t, appB, "13800138000")
	if second.User.ID != first.User.ID {
		t.Fatal("同一手机号登录两个应用应是同一个用户")
	}
	e.smsLoginTo(t, e.app, "13800138000")
	if got := e.registeredAppID(t, first.User.ID); got != e.app.AppID {
		t.Fatalf("登录应用 B 之后 registered_app_id = %q, want 仍是应用 A", got)
	}
	var rows, marked int
	if err := e.pool.QueryRow(ctx,
		`SELECT count(*), count(*) FILTER (WHERE is_registration) FROM user_extra WHERE user_id = $1`,
		first.User.ID).Scan(&rows, &marked); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 || marked != 1 {
		t.Fatalf("user_extra 行数 = %d、标记数 = %d，want 2 行其中 1 行是注册来源", rows, marked)
	}

	// 删除应用 A：来源变空，用户本身不受影响。
	if _, err := e.apps.SetStatus(ctx, e.app.ID, domain.ApplicationStatusDisabled); err != nil {
		t.Fatalf("停用应用 A: %v", err)
	}
	if err := e.apps.Delete(ctx, e.app.ID); err != nil {
		t.Fatalf("删除应用 A: %v", err)
	}
	if got := e.registeredAppID(t, first.User.ID); got != "" {
		t.Fatalf("来源应用删除后 registered_app_id = %q, want 空", got)
	}
	// 来源已空，再登录别的应用也不会被补记成来源。
	e.smsLoginTo(t, appB, "13800138000")
	if got := e.registeredAppID(t, first.User.ID); got != "" {
		t.Fatalf("来源应用删除后再登录应用 B，registered_app_id = %q, want 仍为空", got)
	}
}

// 控制台手动建的号没有注册来源，哪怕他之后登录了某个应用。
func TestManuallyCreatedUserHasNoRegistrationSource(t *testing.T) {
	e := newAuthEnv(t)
	// passwordLogin 先用 EnsureUserWithIdentity 建号——与控制台建号走的是同一个入口。
	res := e.passwordLogin(t, "13800138000")
	if got := e.registeredAppID(t, res.User.ID); got != "" {
		t.Fatalf("手动建的号 registered_app_id = %q, want 空", got)
	}
	e.smsLogin(t, "13800138000")
	if got := e.registeredAppID(t, res.User.ID); got != "" {
		t.Fatalf("手动建的号用短信登录后 registered_app_id = %q, want 仍为空", got)
	}
}

// 同一个新手机号并发首次登录两个应用：只有真正建号的那一次留下注册来源。
func TestConcurrentFirstLoginMarksExactlyOneRegistration(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	apps := []*domain.Application{e.app, e.newLoginApp(t, "应用B", "app-b")}

	// 这是登录流程里"找到或建号 → 写注册关系"那两步。直接并发这两步而不是整个 Login：
	// 验证码是一次性的，同一个号拿不到两个同时有效的码。
	firstLogin := func(app *domain.Application, phone string) error {
		u, _, created, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
			Type: domain.IdentityTypePhone, Subject: phone,
		})
		if err != nil {
			return err
		}
		return e.users.EnsureRegistration(ctx, u.ID, app.ID, created)
	}

	for i := 0; i < 20; i++ {
		phone := fmt.Sprintf("139%08d", i)
		start := make(chan struct{})
		errs := make([]error, len(apps))
		var wg sync.WaitGroup
		for j, app := range apps {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[j] = firstLogin(app, phone)
			}()
		}
		close(start)
		wg.Wait()
		for j, err := range errs {
			if err != nil {
				t.Fatalf("第 %d 轮应用 %d: %v", i, j, err)
			}
		}

		var users, rows, marked int
		if err := e.pool.QueryRow(ctx, `
			SELECT count(DISTINCT i.user_id), count(e.*), count(e.*) FILTER (WHERE e.is_registration)
			FROM identity i LEFT JOIN user_extra e ON e.user_id = i.user_id
			WHERE i.subject = $1`, phone).Scan(&users, &rows, &marked); err != nil {
			t.Fatalf("count: %v", err)
		}
		if users != 1 || rows != 2 || marked != 1 {
			t.Fatalf("第 %d 轮: 用户数 = %d、user_extra 行数 = %d、注册来源数 = %d，want 1 / 2 / 1", i, users, rows, marked)
		}
	}
}

// 「一个用户至多一个注册来源」由部分唯一索引兜底：即使调用方错误地报了两次，也不会标出两行，更不会报错。
func TestEnsureRegistrationNeverMarksTwoSources(t *testing.T) {
	e := newAuthEnv(t)
	ctx := context.Background()
	appB := e.newLoginApp(t, "应用B", "app-b")
	u, _, _, err := e.users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{
		Type: domain.IdentityTypePhone, Subject: "13800138000",
	})
	if err != nil {
		t.Fatalf("建号: %v", err)
	}
	for _, app := range []*domain.Application{e.app, appB, e.app} {
		if err := e.users.EnsureRegistration(ctx, u.ID, app.ID, true); err != nil {
			t.Fatalf("EnsureRegistration: %v", err)
		}
	}
	if got := e.registeredAppID(t, u.ID); got != e.app.AppID {
		t.Fatalf("registered_app_id = %q, want 先写入的应用 A", got)
	}
}

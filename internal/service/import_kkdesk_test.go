package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
)

const importPub = "aa000000000000000000000000000000000000000000000000000000000000bb"

func TestImportDeviceBindings(t *testing.T) {
	svc, clk := newSessionService(t)
	app := testApp(func(p *domain.SessionPolicy) { p.RotateIntervalSeconds = 50 })
	ctx := context.Background()
	uid := uuid.New()
	a, err := svc.Issue(ctx, service.IssueInput{UserID: uid, App: app})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: app})
	if err != nil {
		t.Fatal(err)
	}
	// a 正在轮换过渡期:新旧两个 token 都要补上。
	clk.Advance(51 * time.Second)
	rot, err := svc.Validate(ctx, a.Token, app)
	if err != nil || !rot.Rotated {
		t.Fatalf("轮换: %v %v", rot, err)
	}

	input := strings.Join([]string{
		`{"session_id":"` + a.ID + `","device_pub":"` + importPub + `","client_ref":"42"}`,
		``,
		`not json`,
		`{"session_id":"` + b.ID + `","device_pub":"` + importPub + `","client_ref":"7","user_id":"` + uuid.NewString() + `"}`,
		`{"session_id":"` + b.ID + `","device_pub":"xyz","client_ref":"7"}`,
		`{"session_id":"` + uuid.NewString() + `","device_pub":"` + importPub + `","client_ref":"9"}`,
		`{"session_id":"` + b.ID + `","device_pub":"` + importPub + `","client_ref":"7","user_id":"` + b.UserID.String() + `"}`,
	}, "\n")
	st, err := svc.ImportDeviceBindings(ctx, strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	// 第 4 行给错了 user_id:按那个用户找不到会话,算跳过;第 6 行会话不存在。
	if st.Updated != 2 || st.Skipped != 2 || st.Unchanged != 0 || len(st.Bad) != 2 || st.Bad[0].Line != 3 || st.Bad[1].Line != 5 {
		t.Fatalf("stats = %+v", st)
	}
	for _, tok := range []string{a.Token, rot.NewToken} {
		res, err := svc.Validate(ctx, tok, app)
		if err != nil || res.Session.ClientRef != "42" || res.Session.DevicePub != importPub {
			t.Fatalf("%s: %+v %v", tok, res, err)
		}
	}
	if res, err := svc.Validate(ctx, b.Token, app); err != nil || res.Session.ClientRef != "7" {
		t.Fatalf("b: %+v %v", res, err)
	}

	again, err := svc.ImportDeviceBindings(ctx, strings.NewReader(input))
	if err != nil || again.Updated != 0 || again.Unchanged != 2 {
		t.Fatalf("重复导入应不变: %+v %v", again, err)
	}
}

func TestImportWechatNicknames(t *testing.T) {
	users := newUserService(t)
	ctx := context.Background()
	u, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{Type: domain.IdentityTypeWechatWeb, Subject: "ox1", UnionKey: "un1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.AttachIdentity(ctx, u.ID, service.EnsureIdentityInput{Type: domain.IdentityTypeWechatMP, Subject: "om1", UnionKey: "un1"}); err != nil {
		t.Fatal(err)
	}
	plain, _, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{Type: domain.IdentityTypePhone, Subject: "13800000001"})
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"user_id":"` + u.ID.String() + `","nickname":" 惊呆了小白 "}`,
		`{"user_id":"` + plain.ID.String() + `","nickname":"无微信"}`,
		`{"user_id":"bad","nickname":"x"}`,
		`{"user_id":"` + u.ID.String() + `","nickname":""}`,
	}, "\n")
	st, err := users.ImportWechatNicknames(ctx, strings.NewReader(input))
	if err != nil || st.Updated != 1 || st.Skipped != 1 || len(st.Bad) != 2 || st.Bad[0].Line != 3 || st.Bad[1].Line != 4 {
		t.Fatalf("stats = %+v %v", st, err)
	}
	ids, err := users.ListIdentities(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range ids {
		if i.Nickname != "惊呆了小白" {
			t.Fatalf("%s 的昵称 = %q", i.Type, i.Nickname)
		}
	}
	if again, err := users.ImportWechatNicknames(ctx, strings.NewReader(input)); err != nil || again.Updated != 0 || again.Unchanged != 1 {
		t.Fatalf("重复导入应不变: %+v %v", again, err)
	}
	// 只补空昵称：上线后扫码登录刷新过的昵称比导出时的新，不能被导入盖回去。
	other := `{"user_id":"` + u.ID.String() + `","nickname":"导出时的旧昵称"}`
	if st, err := users.ImportWechatNicknames(ctx, strings.NewReader(other)); err != nil || st.Updated != 0 || st.Unchanged != 1 {
		t.Fatalf("已有昵称应不覆盖: %+v %v", st, err)
	}
	if ids, _ := users.ListIdentities(ctx, u.ID); ids[0].Nickname != "惊呆了小白" {
		t.Fatalf("昵称被覆盖成 %q", ids[0].Nickname)
	}
}

// 扫码登录、绑定时刷新身份上的微信昵称;微信没给昵称时不冲掉记着的。
func TestSetIdentityNickname(t *testing.T) {
	users := newUserService(t)
	ctx := context.Background()
	_, ident, _, err := users.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{Type: domain.IdentityTypeWechatWeb, Subject: "ox2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"小白", "  ", ""} {
		if err := users.SetIdentityNickname(ctx, ident.ID, n); err != nil {
			t.Fatal(err)
		}
	}
	_, got, err := users.FindByIdentity(ctx, domain.IdentityTypeWechatWeb, "ox2")
	if err != nil || got.Nickname != "小白" {
		t.Fatalf("昵称 = %+v %v", got, err)
	}
}

// 导入改了 client_ref 后，到期集合里的成员要换成新串，过期时的 logged_out 才带对 client_ref；
// 上线前签发、不在集合里的会话导入时登记进去，分值按会话真实到期。
func TestImportDeviceBindingsTracksExpiry(t *testing.T) {
	e := newExpiryEnv(t)
	ctx := context.Background()
	tracked := e.issue(t, "old-ref")
	// 上线前签发：当时的 fp 不记到期集合。
	legacySvc := service.NewSessionServiceWithClock(e.st, store.NewRevokePublisher(e.rdb), store.NewEpochStore(e.rdb), e.now)
	legacy, err := legacySvc.Issue(ctx, service.IssueInput{UserID: uuid.New(), App: e.app})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.st.ExpiryScore(ctx, store.ExpiryMember(legacy)); ok {
		t.Fatal("前提不成立：老会话已在到期集合里")
	}

	input := `{"session_id":"` + tracked.ID + `","device_pub":"` + importPub + `","client_ref":"new-a","user_id":"` + tracked.UserID.String() + `"}
{"session_id":"` + legacy.ID + `","device_pub":"` + importPub + `","client_ref":"new-b"}`
	st, err := e.sess.ImportDeviceBindings(ctx, strings.NewReader(input))
	if err != nil || st.Updated != 2 {
		t.Fatalf("stats = %+v, %v", st, err)
	}

	if _, ok, _ := e.st.ExpiryScore(ctx, store.ExpiryMember(tracked)); ok {
		t.Fatal("旧成员还在到期集合里")
	}
	for _, c := range []struct {
		sess *domain.Session
		ref  string
	}{{tracked, "new-a"}, {legacy, "new-b"}} {
		now := *c.sess
		now.ClientRef = c.ref
		score, ok, err := e.st.ExpiryScore(ctx, store.ExpiryMember(&now))
		if want := service.SessionExpiresAt(c.sess, e.app.Session); err != nil || !ok || score != want {
			t.Fatalf("%s: 分值 = %d (%v, %v)，want %d", c.ref, score, ok, err, want)
		}
	}

	again, err := e.sess.ImportDeviceBindings(ctx, strings.NewReader(input))
	if err != nil || again.Unchanged != 2 {
		t.Fatalf("重复导入: %+v %v", again, err)
	}

	e.advance(61 * time.Minute)
	if n := e.sweep(t); n != 2 {
		t.Fatalf("过期发了 %d 条", n)
	}
	refs := map[string]string{}
	for _, v := range e.expiredEvents(t) {
		refs[v.SessionID] = v.ClientRef
	}
	if refs[tracked.ID] != "new-a" || refs[legacy.ID] != "new-b" || len(refs) != 2 {
		t.Fatalf("过期事件的 client_ref = %v", refs)
	}
}

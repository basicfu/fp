package service_test

import (
	"context"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

func wx(openid, union string) service.EnsureIdentityInput {
	return service.EnsureIdentityInput{Type: domain.IdentityTypeWechatWeb, Subject: openid, UnionKey: union,
		Nickname: "微信" + openid, AvatarURL: "https://img/" + openid}
}

func identitySet(t *testing.T, svc *service.UserService, userID uuid.UUID) []string {
	t.Helper()
	list, err := svc.ListIdentities(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(list))
	for _, i := range list {
		out = append(out, i.Type+":"+i.Subject)
	}
	sort.Strings(out)
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestLinkPhoneCreatesUser(t *testing.T) {
	svc := newUserService(t)
	res, err := svc.LinkPhone(context.Background(), service.LinkPhoneInput{Phone: "13800138000", Identity: wx("o1", "u1")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.User.Nickname != "微信o1" || res.User.AvatarURL != "https://img/o1" {
		t.Fatalf("res = %+v user = %+v", res, res.User)
	}
	if got := identitySet(t, svc, res.User.ID); !eq(got, []string{"phone:13800138000", "wechat_web:o1"}) {
		t.Fatalf("identities = %v", got)
	}
}

func TestLinkPhoneAttachesToExistingPhoneUser(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()
	u, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{Type: domain.IdentityTypePhone, Subject: "13800138000", Nickname: "老用户"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.LinkPhone(ctx, service.LinkPhoneInput{Phone: "13800138000", Identity: wx("o1", "u1")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || res.User.ID != u.ID || res.User.Nickname != "老用户" || len(res.Replaced) != 0 {
		t.Fatalf("res = %+v", res)
	}
}

// 账号已有别的微信时自动换绑，旧 unionId 下的其它身份一并删除。
func TestLinkPhoneReplacesOldWechat(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()
	first, err := svc.LinkPhone(ctx, service.LinkPhoneInput{Phone: "13800138000", Identity: wx("old", "u-old")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AttachIdentity(ctx, first.User.ID, service.EnsureIdentityInput{Type: domain.IdentityTypeWechatMP, Subject: "mp-old", UnionKey: "u-old"}); err != nil {
		t.Fatal(err)
	}

	res, err := svc.LinkPhone(ctx, service.LinkPhoneInput{Phone: "13800138000", Identity: wx("new", "u-new")})
	if err != nil {
		t.Fatal(err)
	}
	if res.User.ID != first.User.ID || len(res.Replaced) != 2 {
		t.Fatalf("res = %+v", res)
	}
	if got := identitySet(t, svc, res.User.ID); !eq(got, []string{"phone:13800138000", "wechat_web:new"}) {
		t.Fatalf("identities = %v", got)
	}
	if _, err := svc.FindByUnionKey(ctx, "u-old"); err == nil {
		t.Fatal("旧 unionId 不应再归属任何账号")
	}
}

// 这个微信挂在另一个账号上时，挪到手机号的账号。
func TestLinkPhoneMovesWechatFromOtherUser(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()
	a, err := svc.LinkPhone(ctx, service.LinkPhoneInput{Phone: "13800138001", Identity: wx("o1", "u1")})
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := svc.EnsureUserWithIdentity(ctx, service.EnsureIdentityInput{Type: domain.IdentityTypePhone, Subject: "13800138002"})
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.LinkPhone(ctx, service.LinkPhoneInput{Phone: "13800138002", Identity: wx("o1", "u1")})
	if err != nil {
		t.Fatal(err)
	}
	if res.User.ID != b.ID {
		t.Fatalf("目标账号 = %s, want %s", res.User.ID, b.ID)
	}
	if got := identitySet(t, svc, a.User.ID); !eq(got, []string{"phone:13800138001"}) {
		t.Fatalf("原账号 identities = %v", got)
	}
	if got := identitySet(t, svc, b.ID); !eq(got, []string{"phone:13800138002", "wechat_web:o1"}) {
		t.Fatalf("新账号 identities = %v", got)
	}
}

// 微信已挂在一个没有手机号的账号上，手机号又没注册过：手机号补到那个账号上，不另建。
func TestLinkPhoneAddsPhoneToWechatOnlyUser(t *testing.T) {
	svc := newUserService(t)
	ctx := context.Background()
	u, _, _, err := svc.EnsureUserWithIdentity(ctx, wx("o1", "u1"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.LinkPhone(ctx, service.LinkPhoneInput{Phone: "13800138000", Identity: wx("o1", "u1")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || res.User.ID != u.ID {
		t.Fatalf("res = %+v", res)
	}
	if got := identitySet(t, svc, u.ID); !eq(got, []string{"phone:13800138000", "wechat_web:o1"}) {
		t.Fatalf("identities = %v", got)
	}
}

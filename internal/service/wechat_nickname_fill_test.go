package service_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

func (e *oauthEnv) nicknameOf(t *testing.T, id uuid.UUID) string {
	t.Helper()
	u, err := e.users.GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u.Nickname
}

func (e *oauthEnv) setNickname(t *testing.T, id uuid.UUID, nickname string) {
	t.Helper()
	if _, err := e.users.UpdateProfile(context.Background(), id, service.UserProfileUpdate{Nickname: &nickname}); err != nil {
		t.Fatal(err)
	}
}

func (e *oauthEnv) bindPhoneWith(t *testing.T, code, phone string) *service.LoginResult {
	t.Helper()
	out, err := e.auth.ExchangeOAuth(context.Background(), e.client(), e.webTicket(t, code))
	if err != nil || out.Status != service.OAuthStatusBindPhone {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	return e.bind(t, out.BindTicket, phone)
}

func (e *oauthEnv) bindWechat(t *testing.T, uid uuid.UUID, code string) *service.BindOAuthOutcome {
	t.Helper()
	ticket, _ := e.startBind(t, uid, code)
	out, err := e.auth.ExchangeBindOAuth(context.Background(), e.app, uid, ticket)
	if err != nil || out.Status != service.OAuthStatusSuccess {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	return out
}

func TestWechatNewUserGetsWechatNickname(t *testing.T) {
	e := newOAuthEnv(t, nil)
	res := e.bindPhoneWith(t, "o1:u1:小明", "13800138100")
	if res.User.Nickname != "小明" || e.nicknameOf(t, res.User.ID) != "小明" {
		t.Fatalf("nickname = %q", res.User.Nickname)
	}
	// 头像本次不处理：外链照旧记在 avatar_url，不生成 avatar_hash。
	if res.User.AvatarHash != "" || !strings.HasPrefix(res.User.AvatarURL, "https://img/") {
		t.Fatalf("avatar = %q / %q", res.User.AvatarURL, res.User.AvatarHash)
	}
}

func TestWechatBindPhoneToExistingUserFillsOnlyEmptyNickname(t *testing.T) {
	e := newOAuthEnv(t, nil)

	bare := e.accountUser(t, "13800138101")
	res := e.bindPhoneWith(t, "o2:u2:小刚", "13800138101")
	if res.User.ID != bare || res.User.Nickname != "小刚" {
		t.Fatalf("应挂到已有账号并补昵称：%+v", res.User)
	}

	named := e.accountUser(t, "13800138102")
	e.setNickname(t, named, "自己起的")
	if got := e.bindPhoneWith(t, "o3:u3:小红", "13800138102").User.Nickname; got != "自己起的" {
		t.Fatalf("已有昵称被覆盖：%q", got)
	}
	if got := e.nicknameOf(t, named); got != "自己起的" {
		t.Fatalf("已有昵称被覆盖：%q", got)
	}
}

func TestBindWechatFillsOnlyEmptyNickname(t *testing.T) {
	e := newOAuthEnv(t, nil)

	bare := e.accountUser(t, "13800138110")
	if got := e.bindWechat(t, bare, "ob1:ub1:小红").Profile.User.Nickname; got != "小红" {
		t.Fatalf("nickname = %q", got)
	}
	if got := e.nicknameOf(t, bare); got != "小红" {
		t.Fatalf("落库 nickname = %q", got)
	}

	named := e.accountUser(t, "13800138111")
	e.setNickname(t, named, "自己起的")
	if got := e.bindWechat(t, named, "ob2:ub2:小蓝").Profile.User.Nickname; got != "自己起的" {
		t.Fatalf("已有昵称被覆盖：%q", got)
	}
}

func TestUnionKeyMergeFillsEmptyNickname(t *testing.T) {
	e := newOAuthEnv(t, nil)
	uid := e.accountUser(t, "13800138120")
	e.attach(t, uid, domain.IdentityTypeWechatWeb, "ow", "uz")

	out, err := e.auth.ExchangeAppCode(context.Background(), e.client(), connector.TypeWechatApp, "oa9:uz:小刚")
	if err != nil || out.Status != service.OAuthStatusSuccess || out.Login.User.ID != uid || out.Login.User.Nickname != "小刚" {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
}

func TestWechatNicknameNormalized(t *testing.T) {
	e := newOAuthEnv(t, nil)
	long := strings.Repeat("长", domain.NicknameMaxLen+5)
	if got := e.bindPhoneWith(t, "o7:u7:  "+long, "13800138140").User.Nickname; got != strings.Repeat("长", domain.NicknameMaxLen) {
		t.Fatalf("超长昵称应截断：%q", got)
	}
	// 规整不了的昵称跳过，绑定照常成功。
	res := e.bindPhoneWith(t, "o8:u8:坏\n昵称", "13800138141")
	if res.Session == nil || res.User.Nickname != "" {
		t.Fatalf("res = %+v", res.User)
	}
}

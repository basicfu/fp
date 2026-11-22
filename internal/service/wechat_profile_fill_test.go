package service_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

var (
	wechatAvatarHash = domain.AvatarHash(testsupport.FakeWechatAvatar)
	ownAvatar        = []byte("\x89PNG\r\n\x1a\nuser-uploaded")
)

func (e *oauthEnv) user(t *testing.T, id uuid.UUID) *domain.User {
	t.Helper()
	u, err := e.users.GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// customize 给用户设上自己的昵称和头像。
func (e *oauthEnv) customize(t *testing.T, id uuid.UUID, nickname string) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.users.UpdateProfile(ctx, id, service.UserProfileUpdate{Nickname: &nickname}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.users.UpdateAvatar(ctx, id, ownAvatar); err != nil {
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

func wantFilled(t *testing.T, u *domain.User, nickname, avatarHash string) {
	t.Helper()
	if u.Nickname != nickname || u.AvatarHash != avatarHash {
		t.Fatalf("nickname=%q avatarHash=%q，want %q / %q", u.Nickname, u.AvatarHash, nickname, avatarHash)
	}
}

func TestWechatNewUserGetsWechatNicknameAndAvatar(t *testing.T) {
	e := newOAuthEnv(t, nil)
	res := e.bindPhoneWith(t, "o1:u1:小明", "13800138100")
	wantFilled(t, res.User, "小明", wechatAvatarHash)
	wantFilled(t, e.user(t, res.User.ID), "小明", wechatAvatarHash)
	ct, img, err := e.users.GetAvatar(context.Background(), wechatAvatarHash)
	if err != nil || ct != "image/png" || !bytes.Equal(img, testsupport.FakeWechatAvatar) {
		t.Fatalf("GetAvatar: ct=%q err=%v", ct, err)
	}
}

func TestWechatBindPhoneToExistingUserFillsOnlyEmptyFields(t *testing.T) {
	e := newOAuthEnv(t, nil)
	ctx := context.Background()

	bare := e.accountUser(t, "13800138101")
	res := e.bindPhoneWith(t, "o2:u2:小刚", "13800138101")
	if res.User.ID != bare {
		t.Fatalf("应挂到已有手机号账号：%+v", res)
	}
	wantFilled(t, res.User, "小刚", wechatAvatarHash)

	// 只有昵称、没上传过头像：昵称不动，头像补上。
	named := e.accountUser(t, "13800138102")
	nick := "原昵称"
	if _, err := e.users.UpdateProfile(ctx, named, service.UserProfileUpdate{Nickname: &nick}); err != nil {
		t.Fatal(err)
	}
	wantFilled(t, e.bindPhoneWith(t, "o3:u3:小红", "13800138102").User, "原昵称", wechatAvatarHash)

	custom := e.accountUser(t, "13800138103")
	e.customize(t, custom, "自己起的")
	res = e.bindPhoneWith(t, "o4:u4:小李", "13800138103")
	wantFilled(t, res.User, "自己起的", domain.AvatarHash(ownAvatar))
	wantFilled(t, e.user(t, custom), "自己起的", domain.AvatarHash(ownAvatar))
}

func TestBindWechatFillsOnlyEmptyFields(t *testing.T) {
	e := newOAuthEnv(t, nil)

	bare := e.accountUser(t, "13800138110")
	out := e.bindWechat(t, bare, "ob1:ub1:小红")
	wantFilled(t, &out.Profile.User, "小红", wechatAvatarHash)
	wantFilled(t, e.user(t, bare), "小红", wechatAvatarHash)

	custom := e.accountUser(t, "13800138111")
	e.customize(t, custom, "自己起的")
	out = e.bindWechat(t, custom, "ob2:ub2:小蓝")
	wantFilled(t, &out.Profile.User, "自己起的", domain.AvatarHash(ownAvatar))
}

func TestUnionKeyMergeFillsEmptyProfile(t *testing.T) {
	e := newOAuthEnv(t, nil)
	uid := e.accountUser(t, "13800138120")
	e.attach(t, uid, domain.IdentityTypeWechatWeb, "ow", "uz")

	out, err := e.auth.ExchangeAppCode(context.Background(), e.client(), connector.TypeWechatApp, "oa9:uz:小刚")
	if err != nil || out.Status != service.OAuthStatusSuccess || out.Login.User.ID != uid {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	wantFilled(t, out.Login.User, "小刚", wechatAvatarHash)
}

func TestWechatAvatarDownloadFailureDoesNotBlock(t *testing.T) {
	e := newOAuthEnv(t, nil)
	res := e.bindPhoneWith(t, "noimg1:u5:小李", "13800138130")
	if res.Session == nil {
		t.Fatalf("res = %+v", res)
	}
	wantFilled(t, res.User, "小李", "")

	uid := e.accountUser(t, "13800138131")
	wantFilled(t, &e.bindWechat(t, uid, "noimg2:u6:小王").Profile.User, "小王", "")
}

func TestWechatNicknameNormalized(t *testing.T) {
	e := newOAuthEnv(t, nil)
	long := strings.Repeat("长", domain.NicknameMaxLen+5)
	wantFilled(t, e.bindPhoneWith(t, "o7:u7:  "+long, "13800138140").User,
		strings.Repeat("长", domain.NicknameMaxLen), wechatAvatarHash)
	// 规整不了的昵称跳过，头像照补。
	wantFilled(t, e.bindPhoneWith(t, "o8:u8:坏\n昵称", "13800138141").User, "", wechatAvatarHash)
}

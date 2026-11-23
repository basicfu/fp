package service_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

var wechatAvatarHash = domain.AvatarHash(testsupport.FakeWechatAvatar)

// avatarAfterFill 等后台补头像跑完，返回用户当前的 avatar_hash 与头像公开地址。
func (e *oauthEnv) avatarAfterFill(t *testing.T, id uuid.UUID) (hash, url string) {
	t.Helper()
	service.WaitBackgroundForTest(e.auth)
	p, err := e.users.GetProfile(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.User.AvatarHash, p.AvatarURL
}

// wantWechatAvatar 断言用户的头像是从微信补上的那张，且图片确实在对象存储里。
func (e *oauthEnv) wantWechatAvatar(t *testing.T, id uuid.UUID) {
	t.Helper()
	hash, url := e.avatarAfterFill(t, id)
	key, ok := strings.CutPrefix(url, "https://memory.test/")
	if hash != wechatAvatarHash || !ok || !strings.HasPrefix(key, "avatar/") {
		t.Fatalf("avatarHash=%q url=%q，应补上微信头像", hash, url)
	}
	obj, ok := e.objects.Get(key)
	if !ok || obj.ContentType != "image/png" || !bytes.Equal(obj.Body, testsupport.FakeWechatAvatar) {
		t.Fatalf("对象存储里的头像 = %+v, %v", obj, ok)
	}
}

func (e *oauthEnv) wantNoAvatar(t *testing.T, id uuid.UUID) {
	t.Helper()
	if hash, url := e.avatarAfterFill(t, id); hash != "" || url != "" {
		t.Fatalf("avatarHash=%q url=%q，不应补头像", hash, url)
	}
}

func TestWechatAvatarFilledOnEveryAttachPath(t *testing.T) {
	e := newOAuthEnvWithObjects(t)
	ctx := context.Background()

	t.Run("扫码新建用户", func(t *testing.T) {
		res := e.bindPhoneWith(t, "o1:u1:小明", "13800138200")
		e.wantWechatAvatar(t, res.User.ID)
	})
	t.Run("首次扫码绑手机挂到已有账号", func(t *testing.T) {
		bare := e.accountUser(t, "13800138201")
		res := e.bindPhoneWith(t, "o2:u2:小刚", "13800138201")
		if res.User.ID != bare {
			t.Fatalf("应挂到已有账号：%+v", res.User)
		}
		e.wantWechatAvatar(t, bare)
	})
	t.Run("unionId 归并挂新 openid", func(t *testing.T) {
		uid := e.accountUser(t, "13800138202")
		e.attach(t, uid, domain.IdentityTypeWechatWeb, "ow3", "uz3")
		out, err := e.auth.ExchangeAppCode(ctx, e.client(), connector.TypeWechatApp, "oa3:uz3:小红")
		if err != nil || out.Status != service.OAuthStatusSuccess || out.Login.User.ID != uid {
			t.Fatalf("out = %+v, err = %v", out, err)
		}
		e.wantWechatAvatar(t, uid)
	})
	t.Run("已登录用户扫码绑微信", func(t *testing.T) {
		uid := e.accountUser(t, "13800138203")
		e.bindWechat(t, uid, "ob4:ub4:小蓝")
		e.wantWechatAvatar(t, uid)
	})
	t.Run("App 微信登录首次绑手机", func(t *testing.T) {
		out, err := e.auth.ExchangeAppCode(ctx, e.client(), connector.TypeWechatApp, "oa5:ua5:小李")
		if err != nil || out.Status != service.OAuthStatusBindPhone {
			t.Fatalf("out = %+v, err = %v", out, err)
		}
		res := e.bind(t, out.BindTicket, "13800138205")
		e.wantWechatAvatar(t, res.User.ID)
	})
}

func TestWechatAvatarKeepsExistingAvatar(t *testing.T) {
	e := newOAuthEnvWithObjects(t)
	ctx := context.Background()

	own := e.accountUser(t, "13800138210")
	if _, err := e.users.UpdateAvatar(ctx, own, testJPEG); err != nil {
		t.Fatal(err)
	}
	e.bindPhoneWith(t, "o10:u10:小明", "13800138210")
	if hash, _ := e.avatarAfterFill(t, own); hash != domain.AvatarHash(testJPEG) {
		t.Fatalf("已有头像被覆盖：%q", hash)
	}

	own2 := e.accountUser(t, "13800138211")
	if _, err := e.users.UpdateAvatar(ctx, own2, testJPEG); err != nil {
		t.Fatal(err)
	}
	e.bindWechat(t, own2, "ob11:ub11:小红")
	if hash, _ := e.avatarAfterFill(t, own2); hash != domain.AvatarHash(testJPEG) {
		t.Fatalf("已有头像被覆盖：%q", hash)
	}
	// 已有头像的用户连下载都不该发生，对象存储里只有两张自己传的。
	if keys := e.objects.Keys(); len(keys) != 2 {
		t.Fatalf("对象存储 keys = %v", keys)
	}
}

func TestWechatAvatarBadSourcesSkippedWithoutBlockingLogin(t *testing.T) {
	e := newOAuthEnvWithObjects(t)
	for i, openid := range []string{"noimg1", "text1", "big1", "evil1", "dflt1", "empty1"} {
		t.Run(openid, func(t *testing.T) {
			phone := "1380013822" + string(rune('0'+i))
			res := e.bindPhoneWith(t, openid+":"+openid+"u:小王", phone)
			if res.Session == nil || res.User.Nickname != "小王" {
				t.Fatalf("登录应照常成功：%+v", res)
			}
			e.wantNoAvatar(t, res.User.ID)
		})
	}
	if keys := e.objects.Keys(); len(keys) != 0 {
		t.Fatalf("不该上传任何对象：%v", keys)
	}
}

func TestWechatAvatarSkippedWithoutObjectStore(t *testing.T) {
	e := newOAuthEnv(t, nil)
	res := e.bindPhoneWith(t, "o30:u30:小明", "13800138230")
	e.wantNoAvatar(t, res.User.ID)
}

func TestWechatAvatarDownloadDoesNotDelayLogin(t *testing.T) {
	e := newOAuthEnvWithObjects(t)
	out, err := e.auth.ExchangeOAuth(context.Background(), e.client(), e.webTicket(t, "slow1:us1:小慢"))
	if err != nil || out.Status != service.OAuthStatusBindPhone {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
	start := time.Now()
	res := e.bind(t, out.BindTicket, "13800138240")
	if d := time.Since(start); d >= testsupport.FakeWechatSlowAvatar {
		t.Fatalf("绑手机用了 %v，被头像下载拖慢了", d)
	}
	if res.User.AvatarHash != "" {
		t.Fatalf("头像应在后台补，不在登录结果里：%q", res.User.AvatarHash)
	}
	e.wantWechatAvatar(t, res.User.ID)
}

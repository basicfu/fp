package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAppConfigValidate(t *testing.T) {
	base := AppConfig{AppID: "a1", AppSecret: "s", ConnPolicy: PolicyReplace, ConnLimit: 5, GuestIPRate: 20}
	if err := base.Validate(); err != nil {
		t.Fatalf("合法配置被拒：%v", err)
	}
	bad := base
	bad.ConnPolicy = "whatever"
	if bad.Validate() == nil {
		t.Fatal("未知策略必须报错")
	}
	bad = base
	bad.ConnPolicy = PolicyLimit
	bad.ConnLimit = 0
	if bad.Validate() == nil {
		t.Fatal("limit 策略下 ConnLimit 必须 >= 1")
	}
	bad = base
	bad.ConnPolicy = PolicyNone
	if bad.Validate() == nil {
		t.Fatal("none 只是脚本内部的重登记模式，不能出现在 app 配置里")
	}
	bad = base
	bad.AppID = ""
	if bad.Validate() == nil {
		t.Fatal("缺少 app_id 必须报错")
	}
	bad = base
	bad.AppSecret = ""
	if bad.Validate() == nil {
		t.Fatal("缺少 app_secret 必须报错")
	}
	ok := base
	ok.AllowGuest = true
	ok.GuestIPRate = 20
	if err := ok.Validate(); err != nil {
		t.Fatalf("允许访客且 guest_ip_rate 合法时不应报错：%v", err)
	}
	bad = base
	bad.AllowGuest = true
	bad.GuestIPRate = 0
	if bad.Validate() == nil {
		t.Fatal("允许访客时 guest_ip_rate 必须 >= 1")
	}
}

func TestDurationJSONRoundTrip(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`"2s"`), &d); err != nil {
		t.Fatalf("解析 \"2s\" 失败：%v", err)
	}
	if d.Std() != 2*time.Second {
		t.Fatalf("解析结果 %v，期望 2s", d.Std())
	}
	b, err := json.Marshal(d)
	if err != nil || string(b) != `"2s"` {
		t.Fatalf("序列化得 %s (%v)，期望 \"2s\"", b, err)
	}
	if json.Unmarshal([]byte(`"不是时长"`), &d) == nil {
		t.Fatal("非法时长必须报错，而不是静默变成 0——0 超时意味着每次验证都立刻失败")
	}
	if json.Unmarshal([]byte(`2`), &d) == nil {
		t.Fatal("裸数字必须报错：配置里写 2 是想表达 2 秒还是 2 纳秒，没人说得清")
	}
}

func TestAppConfigValidateBizAuth(t *testing.T) {
	base := AppConfig{AppID: "a1", AppSecret: "s", ConnPolicy: PolicyReplace, ConnLimit: 5, GuestIPRate: 20}

	ok := base
	ok.BizAuth = &BizAuth{VerifyURL: "https://x/verify", Timeout: Duration(2 * time.Second), CacheSize: 10}
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法的 biz_auth 被拒：%v", err)
	}

	for _, tc := range []struct {
		name string
		biz  BizAuth
	}{
		{"缺地址", BizAuth{Timeout: Duration(time.Second), CacheSize: 10}},
		{"明文 HTTP", BizAuth{VerifyURL: "http://x/verify", Timeout: Duration(time.Second), CacheSize: 10}},
		{"超时为零", BizAuth{VerifyURL: "https://x/verify", CacheSize: 10}},
		{"超时为负", BizAuth{VerifyURL: "https://x/verify", Timeout: Duration(-time.Second), CacheSize: 10}},
		{"缓存容量为零", BizAuth{VerifyURL: "https://x/verify", Timeout: Duration(time.Second)}},
	} {
		bad := base
		bad.BizAuth = &tc.biz
		if bad.Validate() == nil {
			t.Errorf("%s 必须被拒绝", tc.name)
		}
	}

	// 不配 biz_auth 是合法的：那表示这个 app 不支持业务方令牌
	none := base
	if err := none.Validate(); err != nil {
		t.Fatalf("不配 biz_auth 应当合法：%v", err)
	}
}

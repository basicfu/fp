package model

import "testing"

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

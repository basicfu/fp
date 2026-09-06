package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/basicfu/fp/internal/httpapi"
)

// 往返：PUT 存进去的东西，GET 能原样拿回来。
func TestConfigRoundTrip(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)

	body := `{"type":"DEFAULT","push":false,"fields":{
		"fee_rate":{"type":"float","desc":"手续费率","value":0.02},
		"api_key":{"type":"string","desc":"上游密钥","value":null}
	}}`
	rec := do(t, h, token, http.MethodPut, "/admin/api/applications/"+appID+"/config", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet,
		"/admin/api/applications/"+appID+"/config?type=DEFAULT", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Seq    int64 `json:"seq"`
		Fields map[string]struct {
			Type  string          `json:"type"`
			Desc  string          `json:"desc"`
			Value json.RawMessage `json:"value"`
		} `json:"fields"`
	}
	decode(t, rec, &got)

	fee := got.Fields["fee_rate"]
	if string(fee.Value) != "0.02" {
		t.Fatalf("fee_rate.value = %s，期望 0.02", fee.Value)
	}
	if fee.Desc != "手续费率" {
		t.Fatalf("desc = %q，期望 手续费率", fee.Desc)
	}
	// 未配置的项必须**保留在响应里**——它就是控制台上待填的那一行。
	api, ok := got.Fields["api_key"]
	if !ok {
		t.Fatal("未配置的 api_key 不该从响应里消失，它是控制台上待填的一行")
	}
	if string(api.Value) != "null" {
		t.Fatalf("api_key.value = %s，期望 null", api.Value)
	}
}

// 弱转换在路由层也成立：字符串 "3" 存进 int 项返回 200，"abc" 返回 400。
func TestConfigSaveCoercionAndRejection(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)
	url := "/admin/api/applications/" + appID + "/config"

	rec := do(t, h, token, http.MethodPut, url,
		`{"type":"DEFAULT","push":false,"fields":{"n":{"type":"int","desc":"","value":"3"}}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf(`"3" 应当能存进 int 项，status = %d, body = %s`, rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodPut, url,
		`{"type":"DEFAULT","push":false,"fields":{"n":{"type":"int","desc":"","value":"abc"}}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(`"abc" 应当被拒，status = %d, body = %s`, rec.Code, rec.Body.String())
	}
}

// PUT 是该分区配置的**全量替换**，不是合并：第二次 PUT 省略掉的 key，
// 必须从当前配置里消失（这正是"删除配置项"的实现方式）。
//
// 【辨别力】必须省略一个**第一次存在过**的 key 再断言它没了。只覆盖同一个
// key 的话，"合并"和"全量替换"产出完全一样的结果，测不出区别——审查时把
// save 改成合并语义，四个既有测试无一变红。
func TestConfigPutIsFullReplacement(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)
	url := "/admin/api/applications/" + appID + "/config"

	rec := do(t, h, token, http.MethodPut, url, `{"type":"DEFAULT","push":false,"fields":{
		"a":{"type":"int","desc":"","value":1},
		"b":{"type":"int","desc":"","value":2}
	}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("首次保存 status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 第二次只带 a，b 被省略 —— 等价于"删掉 b"。
	rec = do(t, h, token, http.MethodPut, url, `{"type":"DEFAULT","push":false,"fields":{
		"a":{"type":"int","desc":"","value":1}
	}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("二次保存 status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet, url+"?type=DEFAULT", "")
	var cur struct {
		Fields map[string]struct {
			Value json.RawMessage `json:"value"`
		} `json:"fields"`
	}
	decode(t, rec, &cur)
	if _, ok := cur.Fields["b"]; ok {
		t.Fatal("b 在第二次 PUT 里被省略了，应当从当前配置消失——PUT 是全量替换不是合并")
	}
	if _, ok := cur.Fields["a"]; !ok {
		t.Fatal("a 应当还在")
	}
}

// 未知分区 400；未知字段（decodeJSON 开了 DisallowUnknownFields）也 400。
func TestConfigRejectsBadRequests(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)
	url := "/admin/api/applications/" + appID + "/config"

	if rec := do(t, h, token, http.MethodPut, url,
		`{"type":"MOBILE","push":false,"fields":{}}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("未知分区 status = %d，期望 400", rec.Code)
	}
	if rec := do(t, h, token, http.MethodPut, url,
		`{"type":"DEFAULT","push":false,"fields":{},"typo":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("未知字段 status = %d，期望 400", rec.Code)
	}
}

// 版本列表与回滚。
func TestConfigVersionsAndRollback(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)
	url := "/admin/api/applications/" + appID + "/config"

	for _, v := range []string{"1", "2"} {
		rec := do(t, h, token, http.MethodPut, url,
			`{"type":"DEFAULT","push":false,"fields":{"n":{"type":"int","desc":"","value":`+v+`}}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("保存 %s status = %d, body = %s", v, rec.Code, rec.Body.String())
		}
	}

	rec := do(t, h, token, http.MethodGet, url+"/versions?type=DEFAULT", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("版本列表 status = %d", rec.Code)
	}
	var versions []struct {
		Seq       int64 `json:"seq"`
		CreatedAt int64 `json:"createdAt"`
	}
	decode(t, rec, &versions)
	if len(versions) != 2 {
		t.Fatalf("版本数 = %d，期望 2", len(versions))
	}
	// 降序：最新的在最前
	if versions[0].Seq != 2 {
		t.Fatalf("第一条 seq = %d，期望 2", versions[0].Seq)
	}

	rec = do(t, h, token, http.MethodPost, url+"/rollback",
		`{"type":"DEFAULT","seq":1,"push":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("回滚 status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet, url+"?type=DEFAULT", "")
	var cur struct {
		Seq    int64 `json:"seq"`
		Fields map[string]struct {
			Value json.RawMessage `json:"value"`
		} `json:"fields"`
	}
	decode(t, rec, &cur)
	if cur.Seq != 3 {
		t.Fatalf("回滚后 seq = %d，期望 3（回滚是往前追加，不是往回删）", cur.Seq)
	}
	if got := string(cur.Fields["n"].Value); got != "1" {
		t.Fatalf("回滚后 n = %s，期望 1", got)
	}
}

// createTestApp 建一个应用并返回它的 uuid 主键字符串。
//
// newAdminEnv 返回的第三个值是 httpapi.Deps，里面有装配好的 Apps 服务，
// 直接用它建应用比走 HTTP 再解析响应短得多。
func createTestApp(t *testing.T, deps httpapi.Deps) string {
	t.Helper()
	app, _, err := deps.Apps.Create(context.Background(), "商城", "shop")
	if err != nil {
		t.Fatalf("建应用失败: %v", err)
	}
	return app.ID.String()
}

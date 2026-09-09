package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/httpapi"
)

// 往返：PUT 存进去的 YAML 原文，GET 能逐字节原样拿回来——包括注释。
func TestConfigRoundTrip(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)

	const yamlText = "fee_rate: 0.02 # 手续费率\napi_key: # 上游密钥，先占个位\n"
	body := `{"type":"DEFAULT","push":false,"value":` + jsonString(yamlText) + `}`
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
		Seq   int64  `json:"seq"`
		Value string `json:"value"`
	}
	decode(t, rec, &got)

	if got.Value != yamlText {
		t.Fatalf("value = %q，期望逐字节等于 %q（包括注释）", got.Value, yamlText)
	}
}

// 保存时只校验"整份文本是不是合法 YAML"：合法的返回 200，语法错误的返回 400。
func TestConfigSaveRejectsInvalidYAML(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)
	url := "/admin/api/applications/" + appID + "/config"

	rec := do(t, h, token, http.MethodPut, url,
		`{"type":"DEFAULT","push":false,"value":"n: 3\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf(`合法 YAML 应当能存，status = %d, body = %s`, rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodPut, url,
		`{"type":"DEFAULT","push":false,"value":"hello"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf(`顶层不是映射应当被拒，status = %d, body = %s`, rec.Code, rec.Body.String())
	}
}

// PUT 是该分区配置的**全量替换**，不是合并：第二次 PUT 省略掉的 key，
// 必须从当前配置里消失（这正是"删除配置项"的实现方式——在 YAML 编辑框
// 里删掉那一行）。
//
// 【辨别力】必须省略一个**第一次存在过**的 key 再断言它没了。只覆盖同一个
// key 的话，"合并"和"全量替换"产出完全一样的结果，测不出区别——审查时把
// save 改成合并语义，四个既有测试无一变红。
func TestConfigPutIsFullReplacement(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)
	url := "/admin/api/applications/" + appID + "/config"

	rec := do(t, h, token, http.MethodPut, url, `{"type":"DEFAULT","push":false,"value":"a: 1\nb: 2\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("首次保存 status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// 第二次只带 a，b 被省略 —— 等价于"删掉 b"。
	rec = do(t, h, token, http.MethodPut, url, `{"type":"DEFAULT","push":false,"value":"a: 1\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("二次保存 status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet, url+"?type=DEFAULT", "")
	var cur struct {
		Value string `json:"value"`
	}
	decode(t, rec, &cur)
	if strings.Contains(cur.Value, "b:") {
		t.Fatalf("b 在第二次 PUT 里被省略了，应当从当前配置消失——PUT 是全量替换不是合并，value = %q", cur.Value)
	}
	if !strings.Contains(cur.Value, "a:") {
		t.Fatalf("a 应当还在，value = %q", cur.Value)
	}
}

// 分区名字符集不合法（数字开头）400；未知字段（decodeJSON 开了
// DisallowUnknownFields）也 400。分区名不再局限于 DEFAULT/WEB——MOBILE
// 这类自定义名字现在是合法的，不能再拿它当"未知分区"的反例。
func TestConfigRejectsBadRequests(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)
	url := "/admin/api/applications/" + appID + "/config"

	if rec := do(t, h, token, http.MethodPut, url,
		`{"type":"1mobile","push":false,"value":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("不合法的分区名 status = %d，期望 400", rec.Code)
	}
	if rec := do(t, h, token, http.MethodPut, url,
		`{"type":"DEFAULT","push":false,"value":"","typo":1}`); rec.Code != http.StatusBadRequest {
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
			`{"type":"DEFAULT","push":false,"value":"n: `+v+`\n"}`)
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
		Seq   int64  `json:"seq"`
		Value string `json:"value"`
	}
	decode(t, rec, &cur)
	if cur.Seq != 3 {
		t.Fatalf("回滚后 seq = %d，期望 3（回滚是往前追加，不是往回删）", cur.Seq)
	}
	if cur.Value != "n: 1\n" {
		t.Fatalf("回滚后 value = %q，期望 %q", cur.Value, "n: 1\n")
	}
}

// 分区列表：一个应用刚建好时是空的；保存过的分区按字母序列出。
func TestConfigListTypes(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)
	url := "/admin/api/applications/" + appID + "/config"

	rec := do(t, h, token, http.MethodGet, url+"/types", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var empty []string
	decode(t, rec, &empty)
	if len(empty) != 0 {
		t.Fatalf("还没存过任何东西，得到 %v", empty)
	}

	for _, typ := range []string{"WEB", "DEFAULT"} {
		rec := do(t, h, token, http.MethodPut, url, `{"type":"`+typ+`","push":false,"value":"a: 1\n"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("保存 %s status = %d", typ, rec.Code)
		}
	}

	rec = do(t, h, token, http.MethodGet, url+"/types", "")
	var got []string
	decode(t, rec, &got)
	if len(got) != 2 || got[0] != "DEFAULT" || got[1] != "WEB" {
		t.Fatalf("得到 %v，期望 [DEFAULT WEB]（字母序）", got)
	}
}

// DELETE 删掉的是整个分区的全部版本，不是清空当前值——删完之后连 v1
// 都查不到了；另一个分区不受影响。
func TestConfigDeleteType(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)
	url := "/admin/api/applications/" + appID + "/config"

	do(t, h, token, http.MethodPut, url, `{"type":"DEFAULT","push":false,"value":"a: 1\n"}`)
	do(t, h, token, http.MethodPut, url, `{"type":"WEB","push":false,"value":"b: 1\n"}`)

	rec := do(t, h, token, http.MethodDelete, url+"?type=DEFAULT", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("删除 status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet, url+"?type=DEFAULT", "")
	var cur struct {
		Seq   int64  `json:"seq"`
		Value string `json:"value"`
	}
	decode(t, rec, &cur)
	if cur.Seq != 0 || cur.Value != "" {
		t.Fatalf("DEFAULT 应当已经没有任何版本，得到 seq=%d value=%q", cur.Seq, cur.Value)
	}

	// WEB 不受影响。
	rec = do(t, h, token, http.MethodGet, url+"?type=WEB", "")
	var web struct {
		Value string `json:"value"`
	}
	decode(t, rec, &web)
	if web.Value != "b: 1\n" {
		t.Fatalf("WEB 不该受 DEFAULT 删除影响，得到 %q", web.Value)
	}
}

// 漏空格的 "port:4379" 要能保存成功，且原样是补完空格之后的样子。
func TestConfigSaveNormalizesMissingColonSpace(t *testing.T) {
	h, token, deps := newAdminEnv(t)
	appID := createTestApp(t, deps)
	url := "/admin/api/applications/" + appID + "/config"

	rec := do(t, h, token, http.MethodPut, url, `{"type":"DEFAULT","push":false,"value":"port:4379\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("漏空格的 YAML 应当能保存，status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet, url+"?type=DEFAULT", "")
	var cur struct {
		Value string `json:"value"`
	}
	decode(t, rec, &cur)
	if cur.Value != "port: 4379\n" {
		t.Fatalf("value = %q，期望补完空格后的 %q", cur.Value, "port: 4379\n")
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

// jsonString 把一个 Go 字符串编码成 JSON 字符串字面量，方便在测试里手写
// 带换行/注释的 YAML 原文作为请求体的一部分，不用手动转义。
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

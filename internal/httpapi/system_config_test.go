package httpapi_test

import (
	"net/http"
	"strings"
	"testing"
)

// 往返：PUT 存进去的 YAML 原文，GET 能逐字节原样拿回来。
func TestSystemConfigRoundTrip(t *testing.T) {
	h, token, _ := newAdminEnv(t)

	const yamlText = "env: dev # 开发环境\nhttp:\n  addr: \":8080\"\n"
	rec := do(t, h, token, http.MethodPut, "/admin/api/system-config", `{"value":`+jsonString(yamlText)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet, "/admin/api/system-config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Seq   int64  `json:"seq"`
		Value string `json:"value"`
	}
	decode(t, rec, &got)
	if got.Value != yamlText {
		t.Fatalf("value = %q，期望逐字节等于 %q", got.Value, yamlText)
	}
}

func TestSystemConfigSaveRejectsInvalidYAML(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	url := "/admin/api/system-config"

	rec := do(t, h, token, http.MethodPut, url, `{"value":"n: 3\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法 YAML 应当能存，status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodPut, url, `{"value":"- a\n- b\n"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("顶层不是映射应当被拒，status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// PUT 是全量替换：第二次省略掉的 key 必须从当前值里消失。
func TestSystemConfigPutIsFullReplacement(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	url := "/admin/api/system-config"

	do(t, h, token, http.MethodPut, url, `{"value":"a: 1\nb: 2\n"}`)
	do(t, h, token, http.MethodPut, url, `{"value":"a: 1\n"}`)

	rec := do(t, h, token, http.MethodGet, url, "")
	var cur struct {
		Value string `json:"value"`
	}
	decode(t, rec, &cur)
	if strings.Contains(cur.Value, "b:") {
		t.Fatalf("b 应当从当前值消失，value = %q", cur.Value)
	}
}

func TestSystemConfigVersionsAndRollback(t *testing.T) {
	h, token, _ := newAdminEnv(t)
	url := "/admin/api/system-config"

	for _, v := range []string{"1", "2"} {
		rec := do(t, h, token, http.MethodPut, url, `{"value":"n: `+v+`\n"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("保存 %s status = %d, body = %s", v, rec.Code, rec.Body.String())
		}
	}

	rec := do(t, h, token, http.MethodGet, url+"/versions", "")
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
	if versions[0].Seq != 2 {
		t.Fatalf("第一条 seq = %d，期望 2（降序）", versions[0].Seq)
	}

	rec = do(t, h, token, http.MethodPost, url+"/rollback", `{"seq":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("回滚 status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, token, http.MethodGet, url, "")
	var cur struct {
		Seq   int64  `json:"seq"`
		Value string `json:"value"`
	}
	decode(t, rec, &cur)
	if cur.Seq != 3 {
		t.Fatalf("回滚后 seq = %d，期望 3", cur.Seq)
	}
	if cur.Value != "n: 1\n" {
		t.Fatalf("回滚后 value = %q，期望 %q", cur.Value, "n: 1\n")
	}
}

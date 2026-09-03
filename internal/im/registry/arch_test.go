package registry

import (
	"regexp"
	"testing"

	"github.com/redis/go-redis/v9"
)

// 每个脚本只能引用 KEYS[1]。引用 KEYS[2] 意味着跨槽访问，单机能跑、Cluster 上会 CROSSSLOT。
func TestScriptsTouchSingleKey(t *testing.T) {
	re := regexp.MustCompile(`KEYS\[(\d+)\]`)
	for name, s := range map[string]*redis.Script{
		"handshake": handshakeScript,
		"kickAll":   kickAllScript,
		"kickOne":   kickOneScript,
	} {
		src := scriptSource(s)
		if src == "" {
			t.Fatalf("脚本 %s 没有经 newScript 登记源码", name)
		}
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			if m[1] != "1" {
				t.Errorf("脚本 %s 引用了 KEYS[%s]：脚本只能碰一个 key，否则 Cluster 下非法", name, m[1])
			}
		}
	}
}

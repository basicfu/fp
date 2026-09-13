package aksign

import (
	"os"
	"strings"
	"testing"
)

// TestDocVectorsMatch 核对 docs/access-key.md 里的测试向量与实现一致：文档和代码不能各说各的。
func TestDocVectorsMatch(t *testing.T) {
	raw, err := os.ReadFile("../../docs/access-key.md")
	if err != nil {
		t.Fatalf("读取对接文档: %v", err)
	}
	doc := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if !strings.Contains(doc, vecSecret) {
		t.Error("文档里缺少测试向量用的 SK")
	}
	for _, v := range vectors {
		if !strings.Contains(doc, v.sts) || !strings.Contains(doc, v.sig) {
			t.Errorf("文档里缺少或写错了向量「%s」的待签名串或签名", v.name)
		}
	}
}

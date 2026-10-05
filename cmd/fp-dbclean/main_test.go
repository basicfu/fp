package main

import (
	"flag"
	"os"
	"strings"
	"testing"
)

// redis.ParseURL 失败时的错误会把 URL 片段（含没做百分号编码的密码）原样带出来，run 的错误会打到
// stderr，不能带出密码。解析发生在确认提示与任何拨号之前，所以这个用例不碰库、也不读 stdin；
// FP_POSTGRES_URL 只需能被解析，指向 127.0.0.1:1：万一将来流程改成先连库，也只会被拒绝连接，清不掉任何库。
func TestRunRedisURLParseErrorDoesNotLeakPassword(t *testing.T) {
	cases := []struct {
		name, url string
		secrets   []string // 错误文本里不许出现的密码片段
	}{
		{"密码以 / 开头", "redis://:/SeCrEt@127.0.0.1:6379/0", []string{"SeCrEt"}},
		{"密码里有坏的百分号转义", "redis://:Pw%zzZq9@127.0.0.1:6379/0", []string{"Pw", "Zq9", "%zz"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// run 直接用全局的 flag 与 os.Args：每个用例换一套全新的并在结束时还原，
			// 既不会重复注册 -y/-no-redis，也不污染测试二进制自己的 flag。
			oldArgs, oldFlags := os.Args, flag.CommandLine
			t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFlags })
			flag.CommandLine = flag.NewFlagSet("fp-dbclean", flag.ContinueOnError)
			os.Args = []string{"fp-dbclean", "truncate"}
			t.Setenv("FP_POSTGRES_URL", "postgres://u:p@127.0.0.1:1/db")
			t.Setenv("FP_REDIS_URL", c.url)

			err := run()
			if err == nil {
				t.Fatal("坏的 FP_REDIS_URL 应当报错")
			}
			for _, secret := range c.secrets {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("错误里带出了密码片段 %q: %v", secret, err)
				}
			}
			if !strings.Contains(err.Error(), "FP_REDIS_URL") || !strings.Contains(err.Error(), "redis://") {
				t.Errorf("错误应点名 FP_REDIS_URL 并给出整体格式 redis://...: %v", err)
			}
		})
	}
}

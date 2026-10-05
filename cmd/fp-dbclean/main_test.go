package main

import (
	"flag"
	"os"
	"strings"
	"testing"
)

// 两个连接串都在解析阶段就被检查，早于确认提示与任何拨号，所以被测的那个之外，另一个只需要"看起来
// 合法"；都指向 127.0.0.1:1：万一将来流程改成先连库，也只会被拒绝连接，清不掉任何库。
const (
	okPostgresURL = "postgres://u:p@127.0.0.1:1/db"
	okRedisURL    = "redis://127.0.0.1:1/0"
)

// callRun 在不碰任何库、不读 stdin 的前提下调用 run，返回它的错误。run 直接用全局的 flag 与
// os.Args：每次换一套全新的并在结束时还原，既不会重复注册 -y/-no-redis，也不污染测试二进制自己的 flag。
func callRun(t *testing.T, postgresURL, redisURL string) error {
	t.Helper()
	oldArgs, oldFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = oldArgs, oldFlags })
	flag.CommandLine = flag.NewFlagSet("fp-dbclean", flag.ContinueOnError)
	os.Args = []string{"fp-dbclean", "truncate"}
	t.Setenv("FP_POSTGRES_URL", postgresURL)
	t.Setenv("FP_REDIS_URL", redisURL)
	return run()
}

func assertScrubbed(t *testing.T, err error, envName, format string, secrets []string) {
	t.Helper()
	if err == nil {
		t.Fatalf("坏的 %s 应当报错", envName)
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("错误里带出了密码片段 %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), envName) || !strings.Contains(err.Error(), format) {
		t.Errorf("错误应点名 %s 并给出整体格式 %s...: %v", envName, format, err)
	}
}

// redis.ParseURL 失败时的错误会把 URL 片段（含没做百分号编码的密码）原样带出来，run 的错误会打到
// stderr，不能带出密码。
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
			assertScrubbed(t, callRun(t, okPostgresURL, c.url), "FP_REDIS_URL", "redis://", c.secrets)
		})
	}
}

// pgx 的解析错误同样会回显连接串：没编码的 % # 让密码片段落进内部错误，写在查询参数里的 password=
// 则原样出现在回显的 URL 里。
func TestRunPostgresURLParseErrorDoesNotLeakPassword(t *testing.T) {
	cases := []struct {
		name, url string
		secrets   []string
	}{
		{"密码里有坏的百分号转义", "postgres://u:Pw%zzZq9@127.0.0.1:5432/db", []string{"Pw", "%zz", "Zq9"}},
		{"密码写在查询参数里，别的参数写错", "postgres://u@127.0.0.1:5432/db?password=Zq9secret&sslmode=bogus", []string{"Zq9secret"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertScrubbed(t, callRun(t, c.url, okRedisURL), "FP_POSTGRES_URL", "postgres://", c.secrets)
		})
	}
}

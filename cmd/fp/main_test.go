package main

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/basicfu/fp/internal/config"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/testsupport"
)

func TestResolveBootstrapAdminDefaultsWhenEmpty(t *testing.T) {
	got := resolveBootstrapAdmin(config.BootstrapAdmin{})
	if got.User != "admin" || got.Password != "admin" {
		t.Fatalf("got = %+v，期望两项都空时落到内置默认 admin/admin", got)
	}
}

func TestResolveBootstrapAdminKeepsExplicitValue(t *testing.T) {
	want := config.BootstrapAdmin{User: "root", Password: "s3cret"}
	got := resolveBootstrapAdmin(want)
	if got != want {
		t.Fatalf("got = %+v，期望原样返回 %+v", got, want)
	}
}

// 只填了一项也不该被当成"两项都空"去覆盖——沿用 EnsureBootstrap 自己
// "任一为空就跳过"的判断，这里只负责"两项都空"这一种情况的默认值。
func TestResolveBootstrapAdminKeepsPartiallyFilledValue(t *testing.T) {
	want := config.BootstrapAdmin{User: "root"}
	got := resolveBootstrapAdmin(want)
	if got != want {
		t.Fatalf("got = %+v，期望原样返回 %+v", got, want)
	}
}

// TestSeedDefaultsYAMLSeedsWhenNoVersion 钉住系统配置表还没有任何版本时
// （Seq=0，全新库必然如此）该把默认值写成第一版——「系统配置」页面第一次
// 打开就有真实内容可以改，不是空白 + placeholder。
func TestSeedDefaultsYAMLSeedsWhenNoVersion(t *testing.T) {
	cfg, err := config.Parse("", "dev")
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}
	cfg.BootstrapAdmin = resolveBootstrapAdmin(cfg.BootstrapAdmin)

	yamlText, ok, err := seedDefaultsYAML(domain.SystemConfig{Seq: 0}, cfg)
	if err != nil {
		t.Fatalf("seedDefaultsYAML() error = %v", err)
	}
	if !ok {
		t.Fatal("Seq=0 时应该要写种子版本，ok = false")
	}
	if !strings.Contains(yamlText, "admin") {
		t.Errorf("种子 YAML 里应当包含兜底后的 bootstrap_admin，得到 %q", yamlText)
	}

	// 种子文本本身必须是能被 Parse 读回来的合法 YAML——写进数据库之前
	// 先在这里验一遍，比等真的存库失败更早暴露问题。
	got, err := config.Parse(yamlText, "dev")
	if err != nil {
		t.Fatalf("种子 YAML 不能被 Parse 读回：%v，yaml = %s", err, yamlText)
	}
	if got.BootstrapAdmin != cfg.BootstrapAdmin {
		t.Errorf("种子 YAML 读回后 BootstrapAdmin = %+v，期望 %+v", got.BootstrapAdmin, cfg.BootstrapAdmin)
	}
}

// TestSeedDefaultsYAMLSkipsWhenVersionExists 钉住已经有人存过版本时不能
// 覆盖——管理员可能已经改过内容，不能被启动流程悄悄冲掉。
func TestSeedDefaultsYAMLSkipsWhenVersionExists(t *testing.T) {
	cfg, err := config.Parse("", "dev")
	if err != nil {
		t.Fatalf("config.Parse() error = %v", err)
	}

	yamlText, ok, err := seedDefaultsYAML(domain.SystemConfig{Seq: 1, Value: "log:\n  level: debug\n"}, cfg)
	if err != nil {
		t.Fatalf("seedDefaultsYAML() error = %v", err)
	}
	if ok {
		t.Fatal("Seq!=0 时不该要求写种子版本，ok = true")
	}
	if yamlText != "" {
		t.Errorf("ok=false 时 yamlText 应当是空字符串，得到 %q", yamlText)
	}
}

// 下面几个 runSubcommand 用例走的都是"拒绝/失败"分支，本不该碰库。清空连接串是兜底：
// scripts/test.sh 会把 .env.local 里的真实连接串带进测试进程，守卫万一回归，用例也只会
// 因缺环境变量而失败，而不是真的去重置某个库里的管理员。
func clearConnectionEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FP_POSTGRES_URL", "")
	t.Setenv("FP_REDIS_URL", "")
}

func TestRunSubcommandRejectsUnknownArgument(t *testing.T) {
	clearConnectionEnv(t)
	// 空串与 -h / --help / help 也算未知：fp 没有帮助子命令，它们和拼错的子命令一样
	// 走"未知参数 + 用法"、退出码 2。
	for _, arg := range []string{"reset-pasword", "", "-h", "--help", "help", "bogus"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runSubcommand([]string{arg}, &stdout, &stderr); code != 2 {
				t.Fatalf("退出码 = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), "用法") || stdout.Len() != 0 {
				t.Fatalf("stderr = %q, stdout = %q", stderr.String(), stdout.String())
			}
		})
	}
}

// reset-password 后面的任何参数都要在碰库之前拒绝：--help / --dry-run 这类探路参数
// 若被静默忽略，会真的重置唯一管理员的密码并作废全部会话。
func TestRunSubcommandRejectsTrailingArguments(t *testing.T) {
	clearConnectionEnv(t)
	for _, args := range [][]string{
		{"reset-password", "--help"},
		{"reset-password", "-h"},
		{"reset-password", "--dry-run"},
		{"reset-password", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runSubcommand(args, &stdout, &stderr); code != 2 {
				t.Fatalf("退出码 = %d, want 2（stderr = %q）", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "用法") || stdout.Len() != 0 {
				t.Fatalf("stderr = %q, stdout = %q", stderr.String(), stdout.String())
			}
		})
	}
}

// 借"缺连接串"这条不碰库的失败路径，钉住失败时给运维的提示。"可安全重跑"不是客套：
// ResetPassword 先提交库、后 INCR 会话纪元，后一步失败时密码已变而新密码没打印出来，
// 这时只有重跑才能拿回账号。
func TestRunSubcommandResetPasswordFailureHintsSafeRerun(t *testing.T) {
	clearConnectionEnv(t)
	var stdout, stderr bytes.Buffer
	if code := runSubcommand([]string{"reset-password"}, &stdout, &stderr); code != 1 {
		t.Fatalf("退出码 = %d, want 1（stderr = %q）", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("失败时 stdout 必须为空，得到 %q", stdout.String())
	}
	for _, want := range []string{"FP_POSTGRES_URL", "可安全重跑", "旧密码已失效"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr 缺少 %q：%q", want, stderr.String())
		}
	}
}

// 登录名被改过的实例：重置必须把用户名一并恢复，打印出来的账号密码必须真的能登录。
func TestResetPasswordPrintsCredentialsThatWork(t *testing.T) {
	pool := testsupport.NewTestDB(t)
	rdb := testsupport.NewTestRedis(t)
	svc := service.NewAdminService(pool, rdb)
	ctx := context.Background()
	if err := svc.EnsureBootstrap(ctx, "root", "some-password"); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}

	var out bytes.Buffer
	if err := resetPassword(ctx, svc, &out); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}
	m := regexp.MustCompile(`用户名：(\S+)\n\s*密码：(\S+)`).FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("输出里找不到用户名与密码: %q", out.String())
	}
	if m[1] != "admin" {
		t.Fatalf("用户名 = %q, want admin", m[1])
	}
	if _, err := svc.Login(ctx, m[1], m[2]); err != nil {
		t.Fatalf("打印出来的账号密码登录不了: %v", err)
	}
}

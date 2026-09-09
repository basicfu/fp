package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/im/fpappcfg"
	"github.com/basicfu/fp/internal/im/fpauth"
	"github.com/basicfu/fp/internal/testsupport"
	fpim "github.com/basicfu/fp/sdk/im"
)

// startIMAgainstFP 起一个**真的向 fp 索取配置**的 fp-im 节点。
//
// 与 im_e2e_test.go 的 startNode 不同：那些场景验的是 ws 协议与跨节点转发，
// 用静态配置就够；这里验的恰恰是 fp-im ↔ fp 之间那条链路，所以 apps 与
// authn 必须都是真的。
func startIMAgainstFP(t *testing.T, e *phase2Env) *imNode {
	t.Helper()
	secret, err := e.imCreds.Rotate(context.Background())
	if err != nil {
		t.Fatalf("生成 IM 凭据: %v", err)
	}

	var node *imNode
	var apps *fpappcfg.Source
	authn, err := fpauth.New(fpauth.Config{
		FPAddr: e.addr, Secret: secret, Insecure: true,
		OnRevoke: func(app string, tokens []string) {
			node.hub.OnRevoked(context.Background(), app, tokens)
		},
		AllApps: func() []string { return apps.Apps() },
	})
	if err != nil {
		t.Fatalf("fpauth.New: %v", err)
	}
	t.Cleanup(func() { _ = authn.Close() })
	apps = fpappcfg.New(authn, nil)

	node = startNode(t, testsupport.NewTestRedis(t), uniqueNodeID("im-prov"), apps, authn)
	return node
}

func enableIMOn(t *testing.T, e *phase2Env, id uuid.UUID) {
	t.Helper()
	cfg := domain.DefaultIMConfig()
	cfg.Enabled = true
	if _, err := e.apps.SetIMConfig(context.Background(), id, cfg); err != nil {
		t.Fatalf("SetIMConfig: %v", err)
	}
}

// TestCrossAppRevokeClosesConnectionsInAllApps 是设计文档第八节那个洞的
// 端到端护栏。
//
// RevokeEvent.AppID 为空串表示**跨全部应用**的撤销（改密、冻结）。早期
// fp-im 每个 app 一个 client，这条事件被 N 个客户端各收一份，扇出靠连接
// 数量天然做到；收敛成一条流之后只收到一份，必须由 fp-im 显式扇开。
//
// fpauth 那条单测钉的是扇出逻辑本身，这条钉的是**整条链路**：fp 侧对
// type=im 订阅者的通配扇出 + SDK 的事件透传 + fp-im 的分派，任何一环断了
// 都在这里红。漏了的话后果是静默的——改密码、冻结用户之后 ws 全都不会被
// 关，零报错零日志。
func TestCrossAppRevokeClosesConnectionsInAllApps(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	enableIMOn(t, e, e.app.ID)

	// 第二个应用，同样打开 IM。
	appB, _, err := e.apps.Create(ctx, "im-prov-b-"+uuid.NewString(), "im-prov-b-"+uuid.NewString())
	if err != nil {
		t.Fatalf("创建第二个应用: %v", err)
	}
	enableIMOn(t, e, appB.ID)

	node := startIMAgainstFP(t, e)

	tokenA, _, _ := e.loginWithPhone(t, "13900000001")

	cliA, err := fpim.Dial(ctx, fpim.ClientConfig{URL: node.wsURL, App: e.appID, Token: tokenA})
	if err != nil {
		t.Fatalf("应用 A 的 client 连不上：%v", err)
	}
	defer cliA.Close()

	closedA := make(chan int, 1)
	cliA.OnClose(func(code int) { closedA <- code })

	// 触发一次跨应用撤销：AppID 为 Nil，覆盖全部应用。
	if err := e.revokePub.Publish(ctx, domain.RevokeEvent{
		Tokens: []string{tokenA}, Reason: "password_changed",
	}); err != nil {
		t.Fatalf("发布跨应用撤销: %v", err)
	}

	select {
	case <-closedA:
	case <-time.After(10 * time.Second):
		t.Fatal("跨应用撤销（AppID 为空）之后，应用 A 的 ws 必须被关闭——" +
			"照抄旧写法拿空串去查连接表一条也查不到，改密码/冻结用户之后 ws 全都不会被关，" +
			"而且零报错零日志")
	}
}

// TestIMDisabledAppRejectsHandshake：控制台关掉 im_enabled 之后，新握手被拒。
//
// 关闭码必须是 4002（策略拒绝、别重连）而不是 4001（去重新登录）——token
// 完全有效，把 client 指去重新登录是错的方向，它登录完还是连不上。
func TestIMDisabledAppRejectsHandshake(t *testing.T) {
	e := newPhase2Env(t)
	ctx := context.Background()
	node := startIMAgainstFP(t, e)

	token, _, _ := e.loginWithPhone(t, "13900000002")

	// e.app 默认 im_enabled=false。
	if _, err := fpim.Dial(ctx, fpim.ClientConfig{URL: node.wsURL, App: e.appID, Token: token}); err == nil {
		t.Fatal("没开 IM 接入的应用必须握手失败")
	}

	// 打开之后同一条握手要能成功——证明拒绝的原因确实是那个开关，
	// 而不是别的什么一直没配好。
	enableIMOn(t, e, e.app.ID)
	cli, err := fpim.Dial(ctx, fpim.ClientConfig{URL: node.wsURL, App: e.appID, Token: token})
	if err != nil {
		t.Fatalf("打开 IM 接入之后应当能握手：%v", err)
	}
	defer cli.Close()
}

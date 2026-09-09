// Package fpauth 用 fpsdk 实现 auth.Authenticator，并把 fp 的 IM 网关接口
// 包给 fp-im 的其余部分用。
//
// **全进程只有一个 fpsdk.Client。** 早期是每个 app 一个——因为那时凭据是
// 各 app 自己的 appSecret，而 fpsdk 的凭据钉在连接上。现在凭据是 IM
// secret，连接级不带 app_id，app 作用域由每次调用附上（fpsdk.WithAppID），
// 所以一条连接服务所有 app。
//
// 这是 internal/im 里唯一允许 import github.com/basicfu/fp/sdk 的包
// （internal/im/arch_test.go 断言）。
package fpauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
	fpsdk "github.com/basicfu/fp/sdk"
)

type Config struct {
	// FPAddr 是 fp 的 gRPC 地址。
	FPAddr string
	// Secret 是 IM 凭据，来自 config-im.yaml 的 fpsdk.secret，在 fp 控制台
	// 生成。全部 fp-im 实例共用同一份。
	Secret   string
	Insecure bool
	// OnRevoke 在 token 被撤销时调用，fp-im 用它关 ws。
	OnRevoke func(app string, tokens []string)
	// AllApps 返回本节点当前持有配置（也就是有过连接）的全部 app。
	//
	// 只为一件事存在：RevokeEvent.AppID 为空串表示**跨全部应用**的撤销
	// （改密、冻结），而 hub 的连接表按 app 分桶，必须逐个投递。
	// 见 dispatchRevoke。
	AllApps func() []string
	// OnAppIMConfigChanged 在 fp 推来某个 app 的 IM 配置变更时调用。
	// app 为空串表示"不知道变了哪个，全部重来"（推送流出现 Gap）。
	OnAppIMConfigChanged func(app string)
	Logger               *slog.Logger
}

type Authenticator struct {
	cfg    Config
	client *fpsdk.Client
}

func New(cfg Config) (*Authenticator, error) {
	// FPAddr 与 Secret 的非空检查留在这里而不是 internal/im/config：
	// "谁用谁校验"——config 包不知道谁会用这两项。而 client 是启动时建的，
	// 所以配错了会在装配阶段就失败，不会推迟到第一个用户握手。
	if cfg.FPAddr == "" {
		return nil, errors.New("fpauth: FPAddr 必填")
	}
	if cfg.Secret == "" {
		return nil, errors.New("fpauth: Secret（IM 凭据）必填")
	}
	a := &Authenticator{cfg: cfg}
	c, err := fpsdk.New(fpsdk.Options{
		Addr:       cfg.FPAddr,
		AppSecret:  cfg.Secret,
		CallerType: fpsdk.CallerTypeIM,
		Insecure:   cfg.Insecure,
		Logger:     cfg.Logger,
		OnRevoke:   a.dispatchRevoke,
	})
	if err != nil {
		return nil, fmt.Errorf("fpauth: 连接 fp: %w", err)
	}
	a.client = c
	return a, nil
}

// dispatchRevoke 把一条撤销事件投给 hub。
//
// **AppID 为空串表示跨全部应用的撤销**（改密、冻结）——proto 的 RevokeEvent
// 注释里写着这个语义。早期 fp-im 每个 app 一个 client，这条事件被 N 个
// 客户端各收一份，扇出是靠连接数量天然做到的；收敛成一条流之后只收到一份，
// 必须在这里显式扇开。
//
// 漏了这一步的后果是**静默的**：拿空串去查 hub 的连接表（键是
// app + "\x00" + token）一条也查不到，改密码 / 冻结用户之后 ws 全都不会被
// 关，没有任何报错或日志。TestCrossAppRevokeFansOutToAllApps 钉住它。
func (a *Authenticator) dispatchRevoke(ev fpsdk.RevokeEvent) {
	if a.cfg.OnRevoke == nil || len(ev.Tokens) == 0 {
		return
	}
	if ev.AppID != "" {
		a.cfg.OnRevoke(ev.AppID, ev.Tokens)
		return
	}
	if a.cfg.AllApps == nil {
		return
	}
	for _, app := range a.cfg.AllApps() {
		a.cfg.OnRevoke(app, ev.Tokens)
	}
}

func (a *Authenticator) Verify(ctx context.Context, req auth.VerifyRequest) (model.Subject, error) {
	// app 作用域逐调用附上：连接级凭据里没有 app_id。
	id, err := a.client.Auth().Validate(fpsdk.WithAppID(ctx, req.App), req.Token)
	if err != nil {
		return model.Subject{}, translate(err)
	}
	return model.User(id.UserID), nil
}

// Fetch 满足 fpappcfg.Fetcher：拉一个 app 的 IM 配置并转成 fp-im 的模型。
func (a *Authenticator) Fetch(ctx context.Context, app string) (model.AppConfig, error) {
	c, err := a.client.IMGateway().GetAppIMConfig(ctx, app)
	if err != nil {
		return model.AppConfig{}, translate(err)
	}
	cfg := model.AppConfig{
		AppID:       app,
		ConnPolicy:  model.Policy(c.ConnPolicy),
		ConnLimit:   int(c.ConnLimit),
		AllowGuest:  c.AllowGuest,
		GuestIPRate: int(c.GuestIPRate),
	}
	if b := c.BizAuth; b != nil {
		cfg.BizAuth = &model.BizAuth{
			VerifyURL: b.VerifyURL,
			Timeout:   model.Duration(time.Duration(b.TimeoutMs) * time.Millisecond),
			CacheSize: int(b.CacheSize),
		}
	}
	// fp 侧保存时已经校验过一遍，这里再校验一次不是不信任它，而是让"线上
	// 传下来一份 fp-im 认为非法的配置"在加载时就暴露，而不是等到某条握手
	// 走到那个字段才诡异地失败。
	if err := cfg.Validate(); err != nil {
		return model.AppConfig{}, err
	}
	return cfg, nil
}

// VerifyAppCredential 核实业务 server 连入 fp-im 时递上来的凭据。
//
// fp-im 没有数据库、也拿不到任何应用的 secret（fp 只存 bcrypt 哈希），
// 只能转给 fp 核实。
func (a *Authenticator) VerifyAppCredential(ctx context.Context, app, secret string) error {
	return translateVerify(a.client.IMGateway().VerifyAppCredential(ctx, app, secret))
}

// translateVerify 是 VerifyAppCredential 专用的映射，**刻意不复用 translate**。
//
// translate 把 ErrIMNotAvailable 压成 ErrUnauthorized，对 token 校验是对的
// （client 拿着 token，凭什么告诉它这个 app 的内部状态）；但对这条路径是错
// 的：调用到这里的业务 server 已经证明自己持有那份 appSecret，告诉它"开关
// 没打开"不泄露任何东西，压成"凭据无效"却会让运维去查一个根本没错的 secret。
//
// fp 侧的 VerifyAppCredential 特意先验 bcrypt 再看 im_enabled 就是为了保住
// 这个区分，在这里压掉等于把那份用心扔了。
func translateVerify(err error) error {
	if errors.Is(err, fpsdk.ErrIMNotAvailable) {
		return auth.ErrIMNotEnabled
	}
	return translate(err)
}

func (a *Authenticator) Close() error { return a.client.Close() }

// translate 把 fpsdk 的哨兵错误映射到 auth 的两个：调用方只需要分"拒绝"和
// "稍后再试"。
//
// ErrIMNotAvailable（应用不存在 / 已停用 / 没开 IM）归到 ErrUnauthorized：
// 它是一个**确定**的拒绝，不是"够不着"——退避重连不会让它变好。
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fpsdk.ErrNoToken), errors.Is(err, fpsdk.ErrUnauthorized),
		errors.Is(err, fpsdk.ErrIMNotAvailable):
		return auth.ErrUnauthorized
	case errors.Is(err, fpsdk.ErrUnavailable):
		return auth.ErrUnavailable
	}
	return fmt.Errorf("%w: %v", auth.ErrUnavailable, err)
}

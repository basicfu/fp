// Package fpauth 用 fpsdk 实现 auth.Authenticator。每个 app 一个 fpsdk.Client：
// fp 的 SDK 按 app 凭据建连，fp-im 服务多个 app 就得有多个。
// 这是 internal/im 里唯一允许 import github.com/basicfu/fp/sdk 的包（arch_test 断言）。
package fpauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
	fpsdk "github.com/basicfu/fp/sdk"
)

type Config struct {
	FPAddr   string
	Insecure bool
	Apps     auth.AppConfigSource
	// OnRevoke 在某 app 的 token 被撤销时调用，fp-im 用它关 ws。
	OnRevoke func(app string, tokens []string)
	Logger   *slog.Logger
}

type Authenticator struct {
	cfg     Config
	mu      sync.Mutex
	clients map[string]*fpsdk.Client
}

func New(cfg Config) (*Authenticator, error) {
	if cfg.FPAddr == "" || cfg.Apps == nil {
		return nil, errors.New("fpauth: FPAddr 与 Apps 必填")
	}
	return &Authenticator{cfg: cfg, clients: map[string]*fpsdk.Client{}}, nil
}

// client 按需为 app 建 fpsdk.Client。懒建而不是启动时全建：apps 文件会热重载。
func (a *Authenticator) client(app string) (*fpsdk.Client, error) {
	cfg, ok := a.cfg.Apps.Get(app)
	if !ok {
		return nil, auth.ErrUnauthorized
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.clients[app]; ok {
		return c, nil
	}
	c, err := fpsdk.New(fpsdk.Options{
		Addr: a.cfg.FPAddr, AppID: cfg.AppID, AppSecret: cfg.AppSecret, Insecure: a.cfg.Insecure, Logger: a.cfg.Logger,
		OnRevoke: func(ev fpsdk.RevokeEvent) {
			if a.cfg.OnRevoke != nil && len(ev.Tokens) > 0 {
				a.cfg.OnRevoke(app, ev.Tokens)
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("fpauth: 为 app %s 建 fp 客户端: %w", app, err)
	}
	a.clients[app] = c
	return c, nil
}

func (a *Authenticator) Verify(ctx context.Context, app, token string) (model.Subject, error) {
	c, err := a.client(app)
	if err != nil {
		return model.Subject{}, err
	}
	id, err := c.Auth().Validate(ctx, token)
	if err != nil {
		return model.Subject{}, translate(err)
	}
	return model.User(id.UserID), nil
}

// translate 把 fpsdk 的哨兵错误映射到 auth 的两个：握手代码只需要分"拒绝"和"稍后再试"。
func translate(err error) error {
	switch {
	case errors.Is(err, fpsdk.ErrNoToken), errors.Is(err, fpsdk.ErrUnauthorized):
		return auth.ErrUnauthorized
	case errors.Is(err, fpsdk.ErrUnavailable):
		return auth.ErrUnavailable
	}
	return fmt.Errorf("%w: %v", auth.ErrUnavailable, err)
}

func (a *Authenticator) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var errs []error
	for app, c := range a.clients {
		if err := c.Close(); err != nil {
			errs = append(errs, fmt.Errorf("app %s: %w", app, err))
		}
	}
	a.clients = map[string]*fpsdk.Client{}
	return errors.Join(errs...)
}

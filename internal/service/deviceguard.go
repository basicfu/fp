package service

import (
	"context"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/basicfu/fp/internal/devicesig"
	"github.com/basicfu/fp/internal/domain"
)

const (
	// DeviceSigSkew 是设备头时间与服务端时间允许的偏差。
	DeviceSigSkew = 300 * time.Second
	// deviceNonceTTL 取时间窗的两倍：窗口两端签出的同一个 nonce 都要认得出来。
	deviceNonceTTL = 2 * DeviceSigSkew
)

// NonceRecorder 是 DeviceGuard 对 nonce 去重的依赖，*store.NonceStore 满足它。
type NonceRecorder interface {
	Use(ctx context.Context, signPubHex, nonce string, ttl time.Duration) (bool, error)
}

// DeviceGuard 在开了会话绑定设备的应用上，校验每个请求的设备头确实来自会话登录时的那台设备。
//
// 全在本地判定、不回调应用：比对公钥、验签与时间窗、nonce 去重。
type DeviceGuard struct {
	nonces NonceRecorder
	// legacyRejected 数因会话没记设备而拒掉的次数。
	legacyRejected atomic.Int64
}

// NewDeviceGuard 构造 DeviceGuard。
func NewDeviceGuard(nonces NonceRecorder) *DeviceGuard {
	return &DeviceGuard{nonces: nonces}
}

// Check 校验 header 与 sess 绑定的设备。设备不符返回 CodeDeviceUnverified；Redis 故障原样返回。
//
// 会话没记设备公钥（开启绑定之前签发、没导入设备的老会话）时按登录过期拒绝，让用户重新登录绑上设备：
// 放行的话这类会话不带设备头也能用，拿走 token 就能冒用，绑定形同虚设。上线流程保证切流前已导入设备，
// 拒绝数只用来观察漏网的会话。
func (g *DeviceGuard) Check(ctx context.Context, app *domain.Application, sess *domain.Session, header string, now time.Time) error {
	if app == nil || !app.Account.DeviceBinding {
		return nil
	}
	if sess.DevicePub == "" {
		g.legacyRejected.Add(1)
		slog.Info("service: 会话没绑设备，按登录过期拒绝", "sessionId", sess.ID, "appId", app.ID)
		return domain.Fail(domain.ErrUnauthorized, domain.CodeTokenInvalid, "登录已过期，请重新登录").
			WithDesc("应用开了设备绑定，会话没记设备公钥")
	}
	tok, err := devicesig.Parse(header)
	if err != nil {
		return deviceUnverified("设备头缺失或格式不正确")
	}
	// 先比公钥再验签：别的设备签得再正确也不是这个会话的设备。
	if !strings.EqualFold(hex.EncodeToString(tok.SignPub), sess.DevicePub) {
		return deviceUnverified("设备与会话不符")
	}
	return g.verify(ctx, tok, now)
}

// VerifyHeader 是登录时的设备校验：还没有会话可比公钥，只验签、时间窗与 nonce，返回设备公钥（hex）供刻进会话。
func (g *DeviceGuard) VerifyHeader(ctx context.Context, header string, now time.Time) (string, error) {
	tok, err := devicesig.Parse(header)
	if err != nil {
		return "", deviceUnverified("设备头缺失或格式不正确")
	}
	if err := g.verify(ctx, tok, now); err != nil {
		return "", err
	}
	return hex.EncodeToString(tok.SignPub), nil
}

func (g *DeviceGuard) verify(ctx context.Context, tok devicesig.Token, now time.Time) error {
	if err := devicesig.Verify(tok, now, DeviceSigSkew); err != nil {
		if errors.Is(err, devicesig.ErrClockSkew) {
			return deviceUnverified("设备时间偏差过大")
		}
		return deviceUnverified("设备签名不正确")
	}
	fresh, err := g.nonces.Use(ctx, hex.EncodeToString(tok.SignPub), tok.Nonce, deviceNonceTTL)
	if err != nil {
		return err
	}
	if !fresh {
		return deviceUnverified("设备头重放")
	}
	return nil
}

// LegacyRejected 是因会话没记设备而拒绝的次数。
func (g *DeviceGuard) LegacyRejected() int64 { return g.legacyRejected.Load() }

// deviceUnverified 对外文案统一，细分原因只进 desc 供排障：细分给客户端没有不同的处理动作。
func deviceUnverified(desc string) error {
	return domain.Fail(domain.ErrUnauthorized, domain.CodeDeviceUnverified, "设备校验未通过，请重试").WithDesc("%s", desc)
}

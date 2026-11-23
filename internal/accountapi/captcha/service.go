package captcha

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/basicfu/fp/internal/accountapi/captcha/gen"
)

const (
	ticketTTL     = 2 * time.Minute
	purgeInterval = time.Minute
	// reloadInterval 限制重读密钥的频率：伪造的票同样会触发重读，不能让它每次都打一趟 Redis。
	reloadInterval = time.Second
)

type UsedStore interface {
	MarkCaptchaUsed(ctx context.Context, idHash []byte, expMs int64) (bool, error)
	PurgeCaptchaUsed(ctx context.Context, nowMs int64) error
}

type Service struct {
	Pool *Pool
	// Sealer 是初始密钥;Secret 非 nil 时校验失败会经它重读共用密钥,变了就换掉 Sealer(此后出题也用新的)。
	// 共用密钥在 Redis 里,被清空后别的实例会生成新的,只在启动时读一次的实例就永远验不过别人出的题。
	Sealer *Sealer
	Secret func(ctx context.Context) (string, error)
	Used   UsedStore
	Now    func() time.Time

	lastPurge  atomic.Int64 // unix 毫秒;每实例最多每分钟清一次过期票,不另开定时任务
	lastReload atomic.Int64 // unix 毫秒
	current    atomic.Pointer[sealerFor]
}

// sealerFor 记下 Sealer 对应的密钥,重读到同一个密钥时不必重建。
type sealerFor struct {
	secret string
	s      *Sealer
}

func (s *Service) sealer() *Sealer {
	if c := s.current.Load(); c != nil {
		return c.s
	}
	return s.Sealer
}

// reload 重读共用密钥,变了就换上新的 Sealer 并返回 true。
func (s *Service) reload(ctx context.Context, now time.Time) bool {
	last := s.lastReload.Load()
	if s.Secret == nil || now.UnixMilli()-last < reloadInterval.Milliseconds() || !s.lastReload.CompareAndSwap(last, now.UnixMilli()) {
		return false
	}
	secret, err := s.Secret(ctx)
	if err != nil {
		slog.Warn("captcha: 重读票据密钥失败", "err", err)
		return false
	}
	if c := s.current.Load(); c != nil && c.secret == secret {
		return false
	}
	ns, err := NewSealer(secret)
	if err != nil {
		return false
	}
	s.current.Store(&sealerFor{secret: secret, s: ns})
	return true
}

func (s *Service) Issue(ctx context.Context) (*Captcha, error) {
	pz, err := s.Pool.Take(ctx)
	if err != nil {
		return nil, err
	}
	return &Captcha{
		ID: s.sealer().Seal(pz.X, pz.Y, s.Now().Add(ticketTTL)), TileY: pz.Y,
		W: gen.W, H: gen.H, Tile: gen.TS, Pad: gen.ShadowPad, Bg: pz.BgJPEG, TilePNG: pz.TilePNG,
	}, nil
}

// Verify 验签成功就先烧票,再判位置与轨迹:每张票只有一次机会,否则同一张票可以反复猜 x 直到撞中。
// 坏票/过期票不写库,免得伪造的 id 给 PG 灌垃圾。返回 ErrFailed / ErrExpired,或存储错误。
func (s *Service) Verify(ctx context.Context, id string, x int, track [][3]int) error {
	now := s.Now()
	ax, _, exp, err := s.sealer().Open(id, now)
	if errors.Is(err, ErrFailed) && s.reload(ctx, now) {
		ax, _, exp, err = s.sealer().Open(id, now)
	}
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(id))
	fresh, err := s.Used.MarkCaptchaUsed(ctx, sum[:16], exp.UnixMilli())
	if err != nil {
		return err
	}
	s.maybePurge(ctx, now)
	if !fresh {
		return ErrFailed
	}
	if d := x - ax; d > Tolerance || d < -Tolerance || !CheckTrack(track, x) {
		return ErrFailed
	}
	return nil
}

func (s *Service) maybePurge(ctx context.Context, now time.Time) {
	last := s.lastPurge.Load()
	if now.UnixMilli()-last < purgeInterval.Milliseconds() || !s.lastPurge.CompareAndSwap(last, now.UnixMilli()) {
		return
	}
	if err := s.Used.PurgeCaptchaUsed(ctx, now.UnixMilli()); err != nil {
		slog.Warn("captcha: 清理过期票据失败", "err", err)
	}
}

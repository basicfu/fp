package captcha

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/basicfu/fp/internal/accountapi/captcha/gen"
)

type memUsed struct {
	seen   map[string]bool
	purged int
}

func (m *memUsed) MarkCaptchaUsed(_ context.Context, h []byte, _ int64) (bool, error) {
	if m.seen[string(h)] {
		return false, nil
	}
	m.seen[string(h)] = true
	return true, nil
}
func (m *memUsed) PurgeCaptchaUsed(context.Context, int64) error { m.purged++; return nil }

func newTestService(t *testing.T) (*Service, *memUsed, *time.Time) {
	t.Helper()
	p := NewPool(1, nil)
	p.items = []*entry{{p: gen.Puzzle{X: 300, Y: 40, BgJPEG: []byte{1}, TilePNG: []byte{2}}}}
	s, _ := NewSealer("sec")
	now := time.Unix(1_700_000_000, 0)
	used := &memUsed{seen: map[string]bool{}}
	return &Service{Pool: p, Sealer: s, Used: used, Now: func() time.Time { return now }}, used, &now
}

func TestIssueThenVerify(t *testing.T) {
	svc, _, _ := newTestService(t)
	c, err := svc.Issue(context.Background())
	if err != nil || c.TileY != 40 || c.W != gen.W || c.H != gen.H || c.Tile != gen.TS || c.Pad != gen.ShadowPad {
		t.Fatalf("%+v %v", c, err)
	}
	if err := svc.Verify(context.Background(), c.ID, 302, human(302)); err != nil {
		t.Fatalf("正确答案被拒: %v", err)
	}
	if err := svc.Verify(context.Background(), c.ID, 302, human(302)); !errors.Is(err, ErrFailed) {
		t.Fatalf("重放: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	svc, _, now := newTestService(t)
	ctx := context.Background()
	c, _ := svc.Issue(ctx)
	if err := svc.Verify(ctx, c.ID, 320, human(320)); !errors.Is(err, ErrFailed) {
		t.Fatalf("位置偏 20: %v", err)
	}
	c2, _ := svc.Issue(ctx)
	*now = now.Add(3 * time.Minute)
	if err := svc.Verify(ctx, c2.ID, 300, human(300)); !errors.Is(err, ErrExpired) {
		t.Fatalf("过期: %v", err)
	}
}

// 答错也烧票:否则同一张票能反复试 x,13 像素容差下几十次就撞中。
func TestVerifyFailBurnsTicket(t *testing.T) {
	svc, used, _ := newTestService(t)
	ctx := context.Background()
	c, _ := svc.Issue(ctx)
	if err := svc.Verify(ctx, c.ID, 100, human(100)); !errors.Is(err, ErrFailed) {
		t.Fatalf("答错: %v", err)
	}
	if len(used.seen) != 1 {
		t.Fatal("答错没烧票")
	}
	if err := svc.Verify(ctx, c.ID, 300, human(300)); !errors.Is(err, ErrFailed) {
		t.Fatalf("烧掉的票用正确答案重试: %v", err)
	}
}

// 坏票/过期票不写库:没验过签的 id 不该落到 PG。
func TestVerifyBadTicketDoesNotTouchStore(t *testing.T) {
	svc, used, now := newTestService(t)
	ctx := context.Background()
	if err := svc.Verify(ctx, "garbage", 300, human(300)); !errors.Is(err, ErrFailed) {
		t.Fatalf("坏票: %v", err)
	}
	c, _ := svc.Issue(ctx)
	*now = now.Add(3 * time.Minute)
	if err := svc.Verify(ctx, c.ID, 300, human(300)); !errors.Is(err, ErrExpired) {
		t.Fatalf("过期: %v", err)
	}
	if len(used.seen) != 0 {
		t.Fatal("坏票/过期票写了库")
	}
}

func TestVerifyToleranceBoundary(t *testing.T) {
	svc, _, _ := newTestService(t) // 缺口 x=300
	ctx := context.Background()
	for _, tc := range []struct {
		x  int
		ok bool
	}{{300 + Tolerance, true}, {300 - Tolerance, true}, {300 + Tolerance + 1, false}, {300 - Tolerance - 1, false}} {
		c, _ := svc.Issue(ctx)
		err := svc.Verify(ctx, c.ID, tc.x, human(tc.x))
		if tc.ok && err != nil || !tc.ok && !errors.Is(err, ErrFailed) {
			t.Fatalf("x=%d: %v", tc.x, err)
		}
	}
}

// Redis 里的共用密钥变了（被清空后别的实例重新生成）：本实例只在启动时读过一次的话，别的实例出的题它永远验不过。
// 校验失败时重读一次密钥再试，之后出题也换用新密钥。
func TestVerifyReloadsSecretOnMismatch(t *testing.T) {
	svc, _, _ := newTestService(t) // 内存里是 "sec"
	svc.Secret = func(context.Context) (string, error) { return "rotated", nil }
	other, _, _ := newTestService(t)
	other.Sealer, _ = NewSealer("rotated")

	c, err := other.Issue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Verify(context.Background(), c.ID, 302, human(302)); err != nil {
		t.Fatalf("密钥变了之后验不过别的实例出的题: %v", err)
	}
	mine, err := svc.Issue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Verify(context.Background(), mine.ID, 302, human(302)); err != nil {
		t.Fatalf("重读密钥后出的题别的实例验不过: %v", err)
	}
	// 伪造的票重读之后照样拒。
	if err := svc.Verify(context.Background(), "garbage", 302, human(302)); !errors.Is(err, ErrFailed) {
		t.Fatalf("坏票: %v", err)
	}
}

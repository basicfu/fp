package service_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/devicesig"
	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
	"github.com/basicfu/fp/internal/store"
	"github.com/basicfu/fp/internal/testsupport"
)

type testDevice struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

func newTestDevice(t *testing.T) testDevice {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return testDevice{pub: pub, priv: priv}
}

func (d testDevice) pubHex() string { return hex.EncodeToString(d.pub) }

// header 按客户端的做法每次重签：新 nonce、当前时间。
func (d testDevice) header(t *testing.T, now time.Time, nonce string) string {
	t.Helper()
	tok := devicesig.Token{SignPub: d.pub, HS: []byte("hs-key"), TS: now.Unix(), Nonce: nonce}
	tok.Sig = ed25519.Sign(d.priv, devicesig.SigningInput(tok))
	raw, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func bindingApp(on bool) *domain.Application {
	return &domain.Application{ID: uuid.New(), Account: domain.AccountConfig{DeviceBinding: on}}
}

func wantUnverified(t *testing.T, err error, what string) {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeDeviceUnverified || !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("%s: err = %v，期望 DEVICE_UNVERIFIED", what, err)
	}
}

func newGuard(t *testing.T) *service.DeviceGuard {
	t.Helper()
	return service.NewDeviceGuard(store.NewNonceStore(testsupport.NewTestRedis(t)))
}

func TestDeviceGuardAcceptsMatchingDevice(t *testing.T) {
	g := newGuard(t)
	dev := newTestDevice(t)
	now := time.Now()
	sess := &domain.Session{DevicePub: dev.pubHex()}
	if err := g.Check(context.Background(), bindingApp(true), sess, dev.header(t, now, "n1"), now); err != nil {
		t.Fatalf("同一设备: %v", err)
	}
	if err := g.Check(context.Background(), bindingApp(true), sess, dev.header(t, now, "n2"), now); err != nil {
		t.Fatalf("同一设备换 nonce: %v", err)
	}
}

func TestDeviceGuardRejectsOtherDeviceWithValidSignature(t *testing.T) {
	g := newGuard(t)
	a, b := newTestDevice(t), newTestDevice(t)
	now := time.Now()
	// A 设备的会话配上 B 设备签得完全正确的头：token 被拷到别的机器上用，必须拒。
	sess := &domain.Session{DevicePub: a.pubHex()}
	wantUnverified(t, g.Check(context.Background(), bindingApp(true), sess, b.header(t, now, "n1"), now), "B 的头配 A 的会话")
}

func TestDeviceGuardRejectsReplayedNonce(t *testing.T) {
	g := newGuard(t)
	dev := newTestDevice(t)
	now := time.Now()
	sess := &domain.Session{DevicePub: dev.pubHex()}
	h := dev.header(t, now, "same")
	if err := g.Check(context.Background(), bindingApp(true), sess, h, now); err != nil {
		t.Fatalf("第一次: %v", err)
	}
	wantUnverified(t, g.Check(context.Background(), bindingApp(true), sess, h, now), "同一 nonce 第二次")
}

func TestDeviceGuardRejectsBadHeaderAndSkew(t *testing.T) {
	g := newGuard(t)
	dev := newTestDevice(t)
	now := time.Now()
	sess := &domain.Session{DevicePub: dev.pubHex()}
	wantUnverified(t, g.Check(context.Background(), bindingApp(true), sess, "", now), "没带设备头")
	wantUnverified(t, g.Check(context.Background(), bindingApp(true), sess, "%%%", now), "设备头不是 base64")
	wantUnverified(t, g.Check(context.Background(), bindingApp(true), sess, dev.header(t, now.Add(-10*time.Minute), "old"), now), "时间窗外")

	tampered := devicesig.Token{SignPub: dev.pub, HS: []byte("hs"), TS: now.Unix(), Nonce: "t", Sig: make([]byte, 64)}
	raw, _ := json.Marshal(tampered)
	wantUnverified(t, g.Check(context.Background(), bindingApp(true), sess, base64.RawURLEncoding.EncodeToString(raw), now), "签名不对")
}

func TestDeviceGuardPassesWhenBindingOff(t *testing.T) {
	g := newGuard(t)
	if err := g.Check(context.Background(), bindingApp(false), &domain.Session{DevicePub: "ab"}, "", time.Now()); err != nil {
		t.Fatalf("应用没开设备绑定: %v", err)
	}
}

// 开了设备绑定却没记设备公钥的会话（开启之前签发、没导入的）一律拒，按登录过期让用户重新登录；
// 放行的话它可以不带设备头被任何人拿去用。计数用来观察上线后还有多少这样的会话。
func TestDeviceGuardRejectsSessionWithoutDevice(t *testing.T) {
	g := newGuard(t)
	dev := newTestDevice(t)
	now := time.Now()
	before := g.LegacyRejected()
	err := g.Check(context.Background(), bindingApp(true), &domain.Session{}, dev.header(t, now, "n1"), now)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeTokenInvalid || !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("没记设备的会话 err = %v", err)
	}
	if g.LegacyRejected() != before+1 {
		t.Fatal("拒绝没有计数")
	}
}

package accountapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// 换绑手机号的票据: /code/verify 验过当前手机号后签一张, 换绑时凭它换新号。5 分钟、一次性、只认签给的那个账号。
// 多实例部署时验旧号与绑新号可能落在不同实例上,所以放 Redis。

const (
	phoneTicketTTL    = 5 * time.Minute
	maxPhoneTicketLen = 128
	phoneTicketPrefix = "fp:acct:phone-ticket:"
)

type phoneTickets interface {
	Issue(ctx context.Context, userID string) (string, error)
	// Take 原子地取走票据; 不存在、过期或不是签给 userID 的返回 nil。
	Take(ctx context.Context, userID, ticket string) (*heldTicket, error)
	// Restore 把取走的票据放回去, 有效期接着原来的算。尽力而为。
	Restore(ctx context.Context, h *heldTicket)
}

type heldTicket struct {
	key, userID string
	ttl         time.Duration
}

type redisTickets struct{ c redis.Cmdable }

// 键只存票据的哈希: Redis 被人读到也拿不出能用的票据。
func ticketKey(t string) string {
	h := sha256.Sum256([]byte(t))
	return phoneTicketPrefix + hex.EncodeToString(h[:])
}

func (t redisTickets) Issue(ctx context.Context, userID string) (string, error) {
	var b [24]byte
	_, _ = rand.Read(b[:])
	tk := base64.RawURLEncoding.EncodeToString(b[:])
	if err := t.c.Set(ctx, ticketKey(tk), userID, phoneTicketTTL).Err(); err != nil {
		return "", err
	}
	return tk, nil
}

// 读值、剩余寿命、删键在一个脚本里: 两个并发的换绑只有一个拿得到票据。
var takeTicket = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if not v then return false end
local ttl = redis.call('PTTL', KEYS[1])
redis.call('DEL', KEYS[1])
return {v, ttl}`)

func (t redisTickets) Take(ctx context.Context, userID, ticket string) (*heldTicket, error) {
	if ticket == "" || len(ticket) > maxPhoneTicketLen {
		return nil, nil
	}
	res, err := takeTicket.Run(ctx, t.c, []string{ticketKey(ticket)}).Slice()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	owner, _ := res[0].(string)
	ttl, _ := res[1].(int64)
	h := &heldTicket{key: ticketKey(ticket), userID: owner, ttl: time.Duration(ttl) * time.Millisecond}
	if owner != userID {
		// 别人的票据: 放回去, 不能让人拿着别人的票据把它作废掉。
		t.Restore(ctx, h)
		return nil, nil
	}
	return h, nil
}

func (t redisTickets) Restore(ctx context.Context, h *heldTicket) {
	if h == nil || h.ttl <= 0 {
		return
	}
	// NX: 万一同名键已被重新写过, 不覆盖。
	if err := t.c.SetNX(ctx, h.key, h.userID, h.ttl).Err(); err != nil {
		slog.Warn("accountapi: 放回换绑票据失败", "err", err)
	}
}

package accountapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// 换绑票据: /code/verify 验过当前手机号或当前邮箱后签一张, 换绑时凭它换新号 / 新邮箱。5 分钟、一次性、只认签给的那个账号。
// 票据记着是验哪种标识换来的(scope = 渠道):验邮箱换来的只能换邮箱,换手机号必须验过手机号——
// 否则拿到邮箱的人就能把手机号这个主登录方式也换走。
// 多实例部署时验旧号与绑新号可能落在不同实例上,所以放 Redis。键名沿用 phone-ticket,上线时在途的票据不失效。

const (
	phoneTicketTTL    = 5 * time.Minute
	maxPhoneTicketLen = 128
	phoneTicketPrefix = "fp:acct:phone-ticket:"
)

type phoneTickets interface {
	Issue(ctx context.Context, userID, scope string) (string, error)
	// Take 原子地取走票据; 不存在、过期或不是签给 userID 的返回 nil。scope 由调用方自己判。
	Take(ctx context.Context, userID, ticket string) (*heldTicket, error)
	// Restore 把取走的票据放回去, 有效期接着原来的算。尽力而为。
	Restore(ctx context.Context, h *heldTicket)
}

type heldTicket struct {
	key, value    string // value 原样放回
	userID, scope string
	ttl           time.Duration
}

// 值是 "用户 id|scope"。上线前签的票据只有用户 id,那时只有验手机号一种。
func splitTicketValue(v string) (userID, scope string) {
	if i := strings.LastIndexByte(v, '|'); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, channelSMS
}

type redisTickets struct{ c redis.Cmdable }

// 键只存票据的哈希: Redis 被人读到也拿不出能用的票据。
func ticketKey(t string) string {
	h := sha256.Sum256([]byte(t))
	return phoneTicketPrefix + hex.EncodeToString(h[:])
}

func (t redisTickets) Issue(ctx context.Context, userID, scope string) (string, error) {
	var b [24]byte
	_, _ = rand.Read(b[:])
	tk := base64.RawURLEncoding.EncodeToString(b[:])
	if err := t.c.Set(ctx, ticketKey(tk), userID+"|"+scope, phoneTicketTTL).Err(); err != nil {
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
	value, _ := res[0].(string)
	ttl, _ := res[1].(int64)
	owner, scope := splitTicketValue(value)
	h := &heldTicket{key: ticketKey(ticket), value: value, userID: owner, scope: scope, ttl: time.Duration(ttl) * time.Millisecond}
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
	if err := t.c.SetNX(ctx, h.key, h.value, h.ttl).Err(); err != nil {
		slog.Warn("accountapi: 放回换绑票据失败", "err", err)
	}
}

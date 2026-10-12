package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/store"
)

const (
	// loginCodeTTL 是验证码有效期。
	loginCodeTTL = 5 * time.Minute
	// MaxVerifyAttempts 是同一个验证码允许的最大校验次数，超过即作废。
	MaxVerifyAttempts = 5
	// loginCodeLength 是验证码位数。
	loginCodeLength = 6
)

// LoginCodeRateRule 是一条发码频率规则，按手机号计数。
type LoginCodeRateRule struct {
	Name   string
	Window time.Duration
	Limit  int
}

// DefaultLoginCodeRateRules 是发码的默认频率限制，取自 3s 的 risk:smssend 策略。
//
// 短信是花钱的外部资源：没有限制，一个手机号就能被无限刷验证码，账单和骚扰都是真的。
func DefaultLoginCodeRateRules() []LoginCodeRateRule {
	return []LoginCodeRateRule{
		{Name: "30s", Window: 30 * time.Second, Limit: 1},
		{Name: "1h", Window: time.Hour, Limit: 5},
		{Name: "1d", Window: 24 * time.Hour, Limit: 10},
	}
}

// issueScript 原子地「还能用的码就复用并续期，否则存新的」。拆成 GET + SET 两步，并发的两次
// 发码会各生成一个码、后写的顶掉先写的，先发出去的那条短信里的码就成了废码。
//
// 校验次数已经用满的码算废了（再校验一次就作废，哪怕码是对的）：这时复用只会让用户收到一条
// 注定用不了的短信，所以换新码并清掉尝试计数。复用时连尝试计数一起续期，两个键的寿命保持一致，
// 否则计数先过期、同一个码会重新获得校验次数。每个码的校验次数上限不变，发码频率另有限流管着。
//
// ARGV：新码、有效期毫秒、校验次数上限。返回当前有效的码。
var issueScript = redis.NewScript(`
local tries = tonumber(redis.call('GET', KEYS[2]) or '0')
local existing = redis.call('GET', KEYS[1])
if existing and tries < tonumber(ARGV[3]) then
  redis.call('PEXPIRE', KEYS[1], ARGV[2])
  if tries > 0 then
    redis.call('PEXPIRE', KEYS[2], ARGV[2])
  end
  return existing
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('DEL', KEYS[2])
return ARGV[1]
`)

// verifyScript 原子地完成「比对 + 计次 + 消费」。
// 拆成多条命令会在并发下产生「同一个码被用两次」的窗口。
//
// 返回值：1 成功；0 码不匹配；-1 码不存在或已过期；-2 尝试次数超限（码已作废）。
var verifyScript = redis.NewScript(`
local stored = redis.call('GET', KEYS[1])
if not stored then
  return -1
end
local tries = redis.call('INCR', KEYS[2])
if tries == 1 then
  redis.call('PEXPIRE', KEYS[2], ARGV[2])
end
if tries > tonumber(ARGV[3]) then
  redis.call('DEL', KEYS[1], KEYS[2])
  return -2
end
if stored == ARGV[1] then
  redis.call('DEL', KEYS[1], KEYS[2])
  return 1
end
return 0
`)

// LoginCodeService 负责短信登录验证码的发码限流、签发与一次性校验。
// 验证码怎么送到手机上不归它管，见 AuthService.SendLoginCode。
type LoginCodeService struct {
	rdb     *redis.Client
	limiter *store.RateLimiter
	rules   []LoginCodeRateRule
}

// LoginCodeOption 调整 LoginCodeService。
type LoginCodeOption func(*LoginCodeService)

// WithLoginCodeRateRules 替换发码频率规则；不带参数调用即关闭限制，测试用。
// 不传这个选项就套用 DefaultLoginCodeRateRules：忘了配和明确不要，后者必须写出来。
func WithLoginCodeRateRules(rules ...LoginCodeRateRule) LoginCodeOption {
	return func(s *LoginCodeService) { s.rules = rules }
}

// NewLoginCodeService 构造 LoginCodeService。
func NewLoginCodeService(rdb *redis.Client, opts ...LoginCodeOption) *LoginCodeService {
	s := &LoginCodeService{rdb: rdb, limiter: store.NewRateLimiter(rdb), rules: DefaultLoginCodeRateRules()}
	for _, o := range opts {
		o(s)
	}
	return s
}

// AllowSend 检查该手机号现在能不能再发一条验证码，超限返回 ErrRateLimited。
//
// 必须先于 Issue 调用：被限流的请求不该生成新码，更不能占用供应商额度。
func (s *LoginCodeService) AllowSend(ctx context.Context, phone string) error {
	for _, rule := range s.rules {
		allowed, retryAfter, err := s.limiter.Allow(ctx, "logincode:"+rule.Name+":"+phone, rule.Window, rule.Limit)
		if err != nil {
			return err
		}
		if !allowed {
			// retryAfterMs 同时进 msg 与 detail：msg 给终端用户看，detail 里的
			// 结构化值让接入方能直接做倒计时，不必从文案里正则抠数字。
			return domain.Failf(domain.ErrRateLimited, domain.CodeRateLimited,
				"发送过于频繁，请 %d 秒后重试", int(retryAfter.Seconds())+1).
				WithField("retryAfterMs", retryAfter.Milliseconds())
		}
	}
	return nil
}

// Issue 返回该手机号当前有效的验证码：还能用的就原样返回（并续期），否则生成新的并存储。
//
// 不能无条件覆盖。覆盖的话，频率限制窗口内的第二次"发送"会用新码顶掉用户手里
// 已经收到的旧码，而这次发送又会被限流拒掉：新码从未送达、旧码已经作废，
// 用户在整个限流窗口内都登录不了。
//
// 只有真的生成新码时才清尝试计数：否则重复点"重发"会无限重置爆破计数器。
func (s *LoginCodeService) Issue(ctx context.Context, phone string) (string, error) {
	if phone == "" {
		return "", domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "手机号不能为空")
	}
	fresh, err := randomDigits(loginCodeLength)
	if err != nil {
		return "", err
	}
	code, err := issueScript.Run(ctx, s.rdb,
		[]string{loginCodeKey(phone), loginCodeTryKey(phone)}, fresh, loginCodeTTL.Milliseconds(), MaxVerifyAttempts).Text()
	if err != nil {
		return "", fmt.Errorf("service: 签发验证码: %w", err)
	}
	return code, nil
}

// Verify 校验验证码。成功后该验证码立即作废。
// 码错误、码不存在、尝试超限一律返回 domain.ErrInvalidCredential，不向调用方区分。
func (s *LoginCodeService) Verify(ctx context.Context, phone, code string) error {
	if code == "" {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeCodeInvalid, "验证码不正确")
	}
	res, err := verifyScript.Run(ctx, s.rdb,
		[]string{loginCodeKey(phone), loginCodeTryKey(phone)},
		code, loginCodeTTL.Milliseconds(), MaxVerifyAttempts).Int64()
	if err != nil {
		return fmt.Errorf("service: 执行验证码校验脚本: %w", err)
	}
	if res == 1 {
		return nil
	}
	return domain.Failf(domain.ErrInvalidCredential, domain.CodeCodeInvalid, "验证码不正确或已过期")
}

func loginCodeKey(phone string) string    { return "fp:code:login:" + phone }
func loginCodeTryKey(phone string) string { return "fp:code:try:login:" + phone }

// randomDigits 生成 n 位密码学随机数字串。
func randomDigits(n int) (string, error) {
	const digits = "0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", fmt.Errorf("service: 生成随机验证码: %w", err)
		}
		b[i] = digits[idx.Int64()]
	}
	return string(b), nil
}

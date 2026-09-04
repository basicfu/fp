package notify

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
)

// 验证码用途。同一号码在不同用途下的验证码互相独立。
const (
	PurposeLogin = "login"
)

const (
	// codeTTL 是验证码有效期。
	codeTTL = 5 * time.Minute
	// MaxVerifyAttempts 是同一个验证码允许的最大校验次数，超过即作废。
	MaxVerifyAttempts = 5
	// codeLength 是验证码位数。
	codeLength = 6
)

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

// CodeService 负责验证码的签发与一次性校验。
type CodeService struct {
	rdb *redis.Client
}

// NewCodeService 构造 CodeService。
func NewCodeService(rdb *redis.Client) *CodeService {
	return &CodeService{rdb: rdb}
}

// Issue 返回该 (purpose, target) 下当前有效的验证码：若已有未过期的验证码，
// 原样返回；否则生成一个新的并存储。
//
// 不能无条件覆盖。覆盖的话，频率限制窗口内的第二次"发送"点击会用新码
// 顶掉用户手里已经收到的旧码，而 Sender.Send 内部的限流又会把这次发送
// 直接拒掉：新码从未送达、旧码已经作废，用户在整个限流窗口内都登录不了。
//
// 只有真的生成新码时才清尝试计数：否则重复点"重发"会无限重置爆破计数器。
func (s *CodeService) Issue(ctx context.Context, purpose, target string) (string, error) {
	if purpose == "" || target == "" {
		return "", domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "purpose 与 target 不能为空")
	}

	if existing, err := s.rdb.Get(ctx, codeKey(purpose, target)).Result(); err == nil {
		return existing, nil
	} else if !errors.Is(err, redis.Nil) {
		return "", fmt.Errorf("notify: 读取现有验证码: %w", err)
	}

	code, err := randomDigits(codeLength)
	if err != nil {
		return "", err
	}
	// 重新签发时一并清掉旧的尝试计数，否则上一轮的失败次数会拖累新码。
	if err := s.rdb.Del(ctx, tryKey(purpose, target)).Err(); err != nil {
		return "", fmt.Errorf("notify: 清理验证码尝试计数: %w", err)
	}
	if err := s.rdb.Set(ctx, codeKey(purpose, target), code, codeTTL).Err(); err != nil {
		return "", fmt.Errorf("notify: 写入验证码: %w", err)
	}
	return code, nil
}

// Verify 校验验证码。成功后该验证码立即作废。
// 码错误、码不存在、尝试超限一律返回 domain.ErrInvalidCredential，不向调用方区分。
func (s *CodeService) Verify(ctx context.Context, purpose, target, code string) error {
	if code == "" {
		return domain.Failf(domain.ErrInvalidCredential, domain.CodeCodeInvalid, "验证码不正确")
	}
	res, err := verifyScript.Run(ctx, s.rdb,
		[]string{codeKey(purpose, target), tryKey(purpose, target)},
		code, codeTTL.Milliseconds(), MaxVerifyAttempts).Int64()
	if err != nil {
		return fmt.Errorf("notify: 执行验证码校验脚本: %w", err)
	}
	if res == 1 {
		return nil
	}
	return domain.Failf(domain.ErrInvalidCredential, domain.CodeCodeInvalid, "验证码不正确或已过期")
}

func codeKey(purpose, target string) string { return "fp:code:" + purpose + ":" + target }
func tryKey(purpose, target string) string  { return "fp:code:try:" + purpose + ":" + target }

// randomDigits 生成 n 位密码学随机数字串。
func randomDigits(n int) (string, error) {
	const digits = "0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(digits))))
		if err != nil {
			return "", fmt.Errorf("notify: 生成随机验证码: %w", err)
		}
		b[i] = digits[idx.Int64()]
	}
	return string(b), nil
}

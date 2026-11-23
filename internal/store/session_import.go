package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/basicfu/fp/internal/domain"
)

// ScanTokens 遍历全部会话的 token。只给一次性的迁移导入用:SCAN 不阻塞 Redis,但要扫全库。
// 同前缀下还有别的键:到期集合 fp:sess:exp 不是字符串,按类型滤掉;墓碑是字符串但后缀带冒号,token 不带。
func (s *SessionStore) ScanTokens(ctx context.Context, fn func(token string) error) error {
	iter := s.rdb.ScanType(ctx, 0, sessionKeyPrefix+"*", 1000, "string").Iterator()
	for iter.Next(ctx) {
		token := strings.TrimPrefix(iter.Val(), sessionKeyPrefix)
		if token == "" || strings.Contains(token, ":") {
			continue
		}
		if err := fn(token); err != nil {
			return err
		}
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("store: 扫描会话: %w", err)
	}
	return nil
}

// SetDevice 给已有会话补上设备字段,TTL 不变。会话不存在 found=false;值本来就一样 changed=false。
// WATCH 包住读改写:导入时会话可能正被延期或轮换,不能把对方刚写的期限覆盖回去。
//
// 到期集合的成员串带 client_ref,改了就得删旧加新,否则过期扫描发出的 logged_out 带的是旧 client_ref。
// expiresAt 给出会话的到期时刻,返回 0 表示不登记;上线前签发、不在集合里的会话借此补登记。
// 分值用 GT:轮换过渡期内的旧 token 到期更早,不能把新 token 记下的到期压回去。
func (s *SessionStore) SetDevice(ctx context.Context, token, devicePub, clientRef string,
	expiresAt func(*domain.Session) (int64, error)) (changed, found bool, err error) {
	key := sessionKeyPrefix + token
	for range 5 {
		err = s.rdb.Watch(ctx, func(tx *redis.Tx) error {
			raw, err := tx.Get(ctx, key).Bytes()
			if errors.Is(err, redis.Nil) {
				found, changed = false, false
				return nil
			}
			if err != nil {
				return err
			}
			found = true
			// 只改两个字段、其余原样:用 map 而不是 domain.Session 往返,免得丢掉本版本不认识的字段。
			var m map[string]json.RawMessage
			var sess domain.Session
			if err := json.Unmarshal(raw, &m); err != nil {
				return fmt.Errorf("store: 解析会话: %w", err)
			}
			if err := json.Unmarshal(raw, &sess); err != nil {
				return fmt.Errorf("store: 解析会话: %w", err)
			}
			oldMember := ExpiryMember(&sess)
			sess.DevicePub, sess.ClientRef = devicePub, clientRef
			exp, err := expiresAt(&sess)
			if err != nil {
				return err
			}
			pub, _ := json.Marshal(devicePub)
			ref, _ := json.Marshal(clientRef)
			changed = string(m["DevicePub"]) != string(pub) || string(m["ClientRef"]) != string(ref)
			m["DevicePub"], m["ClientRef"] = pub, ref
			out, err := json.Marshal(m)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
				if changed {
					p.SetArgs(ctx, key, out, redis.SetArgs{KeepTTL: true, Mode: "XX"})
				}
				if newMember := ExpiryMember(&sess); newMember != oldMember {
					p.ZRem(ctx, SessionExpiryKey, oldMember)
				}
				if exp > 0 {
					p.ZAddArgs(ctx, SessionExpiryKey, redis.ZAddArgs{GT: true, Members: []redis.Z{{Score: float64(exp), Member: ExpiryMember(&sess)}}})
				}
				return nil
			})
			return err
		}, key)
		if !errors.Is(err, redis.TxFailedErr) {
			break
		}
	}
	if err != nil {
		return false, false, fmt.Errorf("store: 补会话设备字段: %w", err)
	}
	return changed, found, nil
}

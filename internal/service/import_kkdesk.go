package service

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"github.com/basicfu/fp/internal/domain"
)

// 账号接口切到 fp 前的一次性数据导入(业务方导出、逐行 JSON)。可重复执行:同样的输入跑第二遍什么都不改。
// 坏行记下行号接着跑,不因一行坏数据整批作废——导出文件几十万行,中途停下比跳过一行更难收拾。

// ImportStats 是一次导入的结果。
type ImportStats struct {
	Updated   int
	Unchanged int
	// Skipped 是对不上的行:会话已不存在、用户没有微信身份。
	Skipped int
	Bad     []ImportLineError
}

// ImportLineError 是一行坏数据。Line 从 1 起。
type ImportLineError struct {
	Line int
	Err  string
}

// eachLine 逐行解析,fn 返回的错误按坏行记下;读文件本身出错才整体返回。
func eachLine(r io.Reader, st *ImportStats, fn func(raw []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		raw := []byte(strings.TrimSpace(sc.Text()))
		if len(raw) == 0 {
			continue
		}
		if err := fn(raw); err != nil {
			var bad badLine
			if !errors.As(err, &bad) {
				return fmt.Errorf("第 %d 行: %w", n, err)
			}
			st.Bad = append(st.Bad, ImportLineError{Line: n, Err: bad.msg})
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("service: 读导入文件: %w", err)
	}
	return nil
}

// badLine 是数据本身的问题(跳过这行);其余错误(Redis、PG 故障)中止整次导入。
type badLine struct{ msg string }

func (b badLine) Error() string { return b.msg }

type sessionBindLine struct {
	SessionID string `json:"session_id"`
	DevicePub string `json:"device_pub"`
	ClientRef string `json:"client_ref"`
	// UserID 可选:有它直接查该用户的会话,没有就按会话 ID 扫一遍全库。
	UserID string `json:"user_id"`
}

// ImportDeviceBindings 给已有会话补上设备公钥与 client_ref:不补的话,切到设备绑定后这些已登录的人会验签失败掉线。
// 会话 TTL 不动;过渡期内同一会话的新旧两个 token 都补。到期集合同步换成新 client_ref 的成员,
// 不在集合里的(上线前签发的)顺带登记,之后过期才有 logged_out(expired)。
func (s *SessionService) ImportDeviceBindings(ctx context.Context, r io.Reader) (ImportStats, error) {
	var (
		st    ImportStats
		bySID map[string][]string
	)
	expiresAt := s.importExpiry(ctx)
	err := eachLine(r, &st, func(raw []byte) error {
		var l sessionBindLine
		if err := json.Unmarshal(raw, &l); err != nil {
			return badLine{"不是合法 JSON"}
		}
		pub, err := hex.DecodeString(l.DevicePub)
		if l.SessionID == "" || l.ClientRef == "" || err != nil || len(pub) != 32 {
			return badLine{"session_id、client_ref 不能为空,device_pub 须是 32 字节公钥的 hex"}
		}
		var tokens []string
		if l.UserID != "" {
			uid, err := uuid.Parse(l.UserID)
			if err != nil {
				return badLine{"user_id 不是合法 UUID"}
			}
			if tokens, err = s.tokensOf(ctx, uid, l.SessionID); err != nil {
				return err
			}
		} else {
			if bySID == nil {
				if bySID, err = s.indexBySessionID(ctx); err != nil {
					return err
				}
			}
			tokens = bySID[l.SessionID]
		}
		changed, found := false, false
		for _, tok := range tokens {
			c, f, err := s.store.SetDevice(ctx, tok, hex.EncodeToString(pub), l.ClientRef, expiresAt)
			if err != nil {
				return err
			}
			changed, found = changed || c, found || f
		}
		switch {
		case !found:
			st.Skipped++
		case changed:
			st.Updated++
		default:
			st.Unchanged++
		}
		return nil
	})
	return st, err
}

// importExpiry 按会话所属应用的策略算到期时刻,一次导入内按应用缓存。应用已删除时不登记:它的会话已经校验不过。
func (s *SessionService) importExpiry(ctx context.Context) func(*domain.Session) (int64, error) {
	policies := map[uuid.UUID]*domain.SessionPolicy{}
	return func(sess *domain.Session) (int64, error) {
		p, ok := policies[sess.AppID]
		if !ok {
			def := domain.DefaultSessionPolicy()
			p = &def
			if s.apps != nil {
				app, err := s.apps.GetByID(ctx, sess.AppID)
				switch {
				case errors.Is(err, domain.ErrNotFound):
					p = nil
				case err != nil:
					return 0, err
				default:
					p = &app.Session
				}
			}
			policies[sess.AppID] = p
		}
		if p == nil {
			return 0, nil
		}
		return SessionExpiresAt(sess, *p), nil
	}
}

func (s *SessionService) tokensOf(ctx context.Context, userID uuid.UUID, sessionID string) ([]string, error) {
	all, err := s.store.ListUserTokens(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, tok := range all {
		sess, err := s.store.Get(ctx, tok)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if sess.ID == sessionID {
			out = append(out, tok)
		}
	}
	return out, nil
}

func (s *SessionService) indexBySessionID(ctx context.Context) (map[string][]string, error) {
	idx := map[string][]string{}
	err := s.store.ScanTokens(ctx, func(tok string) error {
		sess, err := s.store.Get(ctx, tok)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		idx[sess.ID] = append(idx[sess.ID], tok)
		return nil
	})
	return idx, err
}

type wechatNicknameLine struct {
	UserID   string `json:"user_id"`
	Nickname string `json:"nickname"`
}

// ImportWechatNicknames 把业务方记的微信昵称补到用户的微信身份上：只补空的，没有微信身份的跳过。
// 已有昵称的不覆盖——上线后扫码登录刷新过的比导出时的新，重跑导入不能把它盖回去。
func (s *UserService) ImportWechatNicknames(ctx context.Context, r io.Reader) (ImportStats, error) {
	var st ImportStats
	err := eachLine(r, &st, func(raw []byte) error {
		var l wechatNicknameLine
		if err := json.Unmarshal(raw, &l); err != nil {
			return badLine{"不是合法 JSON"}
		}
		uid, err := uuid.Parse(l.UserID)
		if err != nil || thirdPartyNickname(l.Nickname) == "" {
			return badLine{"user_id 须是合法 UUID,nickname 不能为空"}
		}
		n, err := s.fillWechatNickname(ctx, uid, l.Nickname)
		if err != nil {
			return err
		}
		switch n {
		case nicknameNoIdentity:
			st.Skipped++
		case nicknameSame:
			st.Unchanged++
		default:
			st.Updated++
		}
		return nil
	})
	return st, err
}

const (
	nicknameNoIdentity = iota
	nicknameSame
	nicknameUpdated
)

func (s *UserService) fillWechatNickname(ctx context.Context, userID uuid.UUID, raw string) (int, error) {
	nickname := thirdPartyNickname(raw)
	var total, changed int
	err := s.pool.QueryRow(ctx, `
		WITH w AS (SELECT id, nickname FROM identity WHERE user_id = $1 AND type = ANY($3)),
		     u AS (UPDATE identity i SET nickname = $2 FROM w WHERE i.id = w.id AND w.nickname = '' RETURNING 1)
		SELECT (SELECT count(*) FROM w), (SELECT count(*) FROM u)`, userID, nickname, wechatIdentityTypes).Scan(&total, &changed)
	if err != nil {
		return 0, fmt.Errorf("service: 写入微信昵称: %w", err)
	}
	switch {
	case total == 0:
		return nicknameNoIdentity, nil
	case changed == 0:
		return nicknameSame, nil
	}
	return nicknameUpdated, nil
}

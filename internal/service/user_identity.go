package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/basicfu/fp/internal/domain"
)

// UnbindKindWechat 是 UnbindIdentity 的「全部微信身份」：网站应用、移动应用、服务号 openid 一起删。
const UnbindKindWechat = "wechat"

// wechatIdentityTypes 是归到「微信」名下的登录标识类型。
var wechatIdentityTypes = []string{domain.IdentityTypeWechatWeb, domain.IdentityTypeWechatApp, domain.IdentityTypeWechatMP}

func identityTaken() error {
	return domain.Fail(domain.ErrConflict, domain.CodeIdentityTaken, "已绑定其他账号")
}

// ReplaceIdentity 把用户某一类本地标识（phone / email）换成 subject：原来的同类标识删掉，没有则新增。
// subject 已属别的用户返回 IDENTITY_TAKEN；已经是本人的同一个标识时什么都不改。调用方必须已经验过 subject 的归属。
func (s *UserService) ReplaceIdentity(ctx context.Context, userID uuid.UUID, typ, subject string) (*domain.Identity, error) {
	in := EnsureIdentityInput{Type: typ, Subject: subject}.normalize()
	if err := in.validate(); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 与 LinkPhone 同一把锁：换绑与微信首登绑手机并发时，同一个号码只会落到一个账号上。
	if err := lockUnionKeyTx(ctx, tx, in.Type+":"+in.Subject); err != nil {
		return nil, err
	}
	if err := lockUserTx(ctx, tx, userID); err != nil {
		return nil, err
	}
	owner, cur, err := findByIdentityTx(ctx, tx, in.Type, in.Subject)
	switch {
	case err == nil && owner.ID != userID:
		return nil, identityTaken()
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM identity WHERE user_id = $1 AND type = $2 AND subject <> $3`,
		userID, in.Type, in.Subject); err != nil {
		return nil, fmt.Errorf("service: 删除旧标识: %w", err)
	}
	if cur == nil {
		if cur, err = insertIdentityTx(ctx, tx, userID, in); err != nil {
			return nil, takenOnConflict(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("service: 提交事务: %w", err)
	}
	s.profileChanged(ctx, userID)
	return cur, nil
}

// BindThirdParty 把一个三方身份（微信）挂到已存在的用户上。
//
// 这个身份（同 subject 或同 union_key）已挂在别的用户上返回 IDENTITY_TAKEN：与 LinkPhone 的挪绑不同，
// 这里只证明了三方身份的归属，没有证明对方账号也是本人的，不能从别人那里摘。
// 用户已有同类型的另一个身份时换掉它，连同旧身份 union_key 下的其它身份——理由同 LinkPhone 的自动换绑。
func (s *UserService) BindThirdParty(ctx context.Context, userID uuid.UUID, in EnsureIdentityInput) (*domain.Identity, error) {
	in = in.normalize()
	if err := in.validate(); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if in.UnionKey != "" {
		if err := lockUnionKeyTx(ctx, tx, in.UnionKey); err != nil {
			return nil, err
		}
	}
	if err := lockUserTx(ctx, tx, userID); err != nil {
		return nil, err
	}
	owner, err := thirdPartyOwnerTx(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if owner != uuid.Nil && owner != userID {
		return nil, identityTaken()
	}

	replaced, err := deleteIdentitiesTx(ctx, tx,
		`DELETE FROM identity WHERE user_id = $1 AND type = $2 AND subject <> $3 RETURNING `+identityColumns,
		userID, in.Type, in.Subject)
	if err != nil {
		return nil, err
	}
	for _, old := range replaced {
		if old.UnionKey == "" || old.UnionKey == in.UnionKey {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM identity WHERE user_id = $1 AND union_key = $2`, userID, old.UnionKey); err != nil {
			return nil, fmt.Errorf("service: 删除旧三方身份: %w", err)
		}
	}

	_, cur, err := findByIdentityTx(ctx, tx, in.Type, in.Subject)
	if errors.Is(err, domain.ErrNotFound) {
		cur, err = insertIdentityTx(ctx, tx, userID, in)
		err = takenOnConflict(err)
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("service: 提交事务: %w", err)
	}
	s.profileChanged(ctx, userID)
	return cur, nil
}

// RemoveIdentities 删掉用户某一类登录标识：phone / email，或 UnbindKindWechat（全部微信身份及同 union_key 的身份）。
// 一个都没有返回 IDENTITY_NOT_FOUND；删完没有可登录的方式返回 LAST_IDENTITY。
func (s *UserService) RemoveIdentities(ctx context.Context, userID uuid.UUID, kind string) error {
	match := func(i domain.Identity) bool { return i.Type == kind }
	switch kind {
	case domain.IdentityTypePhone, domain.IdentityTypeEmail:
	case UnbindKindWechat:
		match = func(i domain.Identity) bool { return isWechatType(i.Type) }
	default:
		return domain.Failf(domain.ErrInvalidArgument, domain.CodeInvalidArgument, "type 只能是 phone、email 或 wechat，收到 %q", kind)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("service: 开启事务: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 锁住用户行：两个并发的解绑（先解手机、再解微信）各自都觉得还剩一个，合起来就把账号解成了没法登录。
	if err := lockUserTx(ctx, tx, userID); err != nil {
		return err
	}
	u, err := getUserTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	all, err := listIdentitiesTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	unions := map[string]bool{}
	for _, i := range all {
		if match(i) && i.UnionKey != "" {
			unions[i.UnionKey] = true
		}
	}
	var drop []uuid.UUID
	loginable := false
	for _, i := range all {
		if match(i) || unions[i.UnionKey] {
			drop = append(drop, i.ID)
			continue
		}
		if canLoginWith(i.Type, u.PasswordHash != "") {
			loginable = true
		}
	}
	if len(drop) == 0 {
		return domain.Fail(domain.ErrNotFound, domain.CodeIdentityNotFound, "没有绑定这种登录方式")
	}
	if !loginable {
		return domain.Fail(domain.ErrConflict, domain.CodeLastIdentity, "解绑后将无法登录，请先绑定其他登录方式")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM identity WHERE id = ANY($1)`, drop); err != nil {
		return fmt.Errorf("service: 删除登录标识: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("service: 提交事务: %w", err)
	}
	s.profileChanged(ctx, userID)
	return nil
}

// canLoginWith 报告只剩这类标识时还能不能登录。只有微信没有手机号也算：扫码后走绑手机流程，LinkPhone 会落回这个账号。
// 服务号 openid 不是登录方式。
func canLoginWith(typ string, hasPassword bool) bool {
	switch typ {
	case domain.IdentityTypePhone, domain.IdentityTypeWechatWeb, domain.IdentityTypeWechatApp:
		return true
	case domain.IdentityTypeEmail, domain.IdentityTypeUsername:
		return hasPassword
	}
	return false
}

func isWechatType(typ string) bool {
	for _, t := range wechatIdentityTypes {
		if t == typ {
			return true
		}
	}
	return false
}

// lockUserTx 锁住用户行，同时确认用户存在。
func lockUserTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `SELECT id FROM app_user WHERE id = $1 FOR NO KEY UPDATE`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Fail(domain.ErrNotFound, domain.CodeUserNotFound, "用户不存在")
	}
	if err != nil {
		return fmt.Errorf("service: 锁定用户: %w", err)
	}
	return nil
}

func listIdentitiesTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID) ([]domain.Identity, error) {
	rows, err := tx.Query(ctx, `SELECT `+identityColumns+` FROM identity WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("service: 查询 identity 列表: %w", err)
	}
	defer rows.Close()
	var out []domain.Identity
	for rows.Next() {
		i, err := scanIdentity(rows)
		if err != nil {
			return nil, fmt.Errorf("service: 扫描 identity: %w", err)
		}
		out = append(out, *i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("service: 遍历 identity: %w", err)
	}
	return out, nil
}

// takenOnConflict 把唯一约束冲突（并发绑定同一个标识）换成对调用方有意义的 IDENTITY_TAKEN。
func takenOnConflict(err error) error {
	if errors.Is(err, domain.ErrConflict) {
		return identityTaken()
	}
	return err
}

// SetIdentityNickname 记下三方身份自带的昵称（微信昵称），每次扫码登录、绑定时刷新。
// 昵称规整同 thirdPartyNickname，规整后为空就不改：微信偶尔不给昵称，不该把记着的冲掉。
func (s *UserService) SetIdentityNickname(ctx context.Context, identityID uuid.UUID, raw string) error {
	nickname := thirdPartyNickname(raw)
	if nickname == "" {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `UPDATE identity SET nickname = $2 WHERE id = $1 AND nickname <> $2`, identityID, nickname); err != nil {
		return fmt.Errorf("service: 写入身份昵称: %w", err)
	}
	return nil
}

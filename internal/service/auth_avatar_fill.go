package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/domain"
)

// fillProfileFrom 用三方资料补用户空着的昵称和头像（已有的不覆盖），返回补完昵称的用户。
// 三方身份挂到用户上的每条路径都调它；头像在后台补，返回的用户里还看不到。
func (s *AuthService) fillProfileFrom(ctx context.Context, u *domain.User, p *connector.OAuthProfile) *domain.User {
	u = s.fillNicknameFrom(ctx, u, p)
	s.fillAvatarLater(ctx, u, p)
	return u
}

// maxAvatarFills 是同时在跑的后台补头像上限：微信头像域名卡住时，登录高峰不至于堆起成千上万个等超时的 goroutine。
const maxAvatarFills = 32

// avatarFillTimeout 管下载加上传加入库整段；下载自己另有 3 秒超时。
const avatarFillTimeout = 15 * time.Second

// fillAvatarLater 用户还没有头像时，在后台下载三方头像存为头像。
//
// 放到请求之外：下载要访问微信、上传要访问 OSS，任何一步慢都不该拖慢登录与绑定。
// 尽力而为，失败只记 WARN；用户此后照样能自己上传。只补空由 FillEmptyAvatar 在写库时再判一次，
// 这里看 u 只是省掉明知会丢弃的下载。
func (s *AuthService) fillAvatarLater(ctx context.Context, u *domain.User, p *connector.OAuthProfile) {
	if u == nil || p == nil || u.AvatarHash != "" || p.AvatarURL == "" ||
		connector.IsWechatDefaultAvatar(p.AvatarURL) || !s.deps.Users.HasObjectStore() {
		return
	}
	userID, rawURL, typ := u.ID, p.AvatarURL, p.IdentityType
	select {
	case s.avatarSlots <- struct{}{}:
	default:
		slog.Warn("service: 后台补头像已满，本次不补", "userId", userID, "type", typ)
		return
	}
	s.avatarFills.Add(1)
	// 请求结束 ctx 就取消了，后台任务要脱开它、另给超时。
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), avatarFillTimeout)
	go func() {
		defer s.avatarFills.Done()
		defer func() { <-s.avatarSlots }()
		defer cancel()
		img, err := s.deps.Avatars.Fetch(bg, rawURL)
		if err != nil {
			slog.Warn("service: 下载三方头像失败，不补头像", "err", err, "userId", userID, "type", typ)
			return
		}
		if _, err := s.deps.Users.FillEmptyAvatar(bg, userID, img); err != nil {
			slog.Warn("service: 用三方头像补头像失败", "err", err, "userId", userID, "type", typ)
		}
	}()
}

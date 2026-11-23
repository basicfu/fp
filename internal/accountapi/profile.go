package accountapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/basicfu/fp/internal/service"
)

// maxAvatarBytes 是头像规格上限。服务层的 1 MB 只是兜底: 侧栏头像只有几十像素,
// 客户端上传前会压缩, 放这么大进来只会让每台设备多下一份没用的数据。
const maxAvatarBytes = 256 << 10

const fieldNickname = "nickname"

// handleProfile 只接受能改的字段(昵称;头像走 /avatar):出现别的字段整个请求拒掉,而不是静默忽略——
// 客户端以为改成了,下次 /me 又变回去,比明确报错更难排查。
func (h *handler) handleProfile(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	var fields map[string]json.RawMessage
	if !decodeBody(w, r, &fields, maxBody) || len(fields) == 0 {
		h.writeErr(w, "bad_request", "")
		return
	}
	for k := range fields {
		if k != fieldNickname {
			h.writeErr(w, "forbidden", "不允许修改该项资料")
			return
		}
	}
	// 用指针认出 null:解进 string 时 null 是空操作,会被当成改成空昵称。
	var nickname *string
	if json.Unmarshal(fields[fieldNickname], &nickname) != nil || nickname == nil {
		h.writeErr(w, "bad_request", "")
		return
	}
	if p, done := h.updateNickname(w, r, c, *nickname); done {
		h.okUser(w, r, c, p)
	}
}

func (h *handler) updateNickname(w http.ResponseWriter, r *http.Request, c *caller, nickname string) (*service.UserProfile, bool) {
	p, err := h.d.Users.UpdateProfile(r.Context(), c.UserID, service.UserProfileUpdate{Nickname: &nickname})
	if err != nil {
		h.usersErr(w, "profile", err)
		return nil, false
	}
	return p, true
}

func (h *handler) handleAvatar(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	if p, done := h.updateAvatar(w, r, c); done {
		h.okUser(w, r, c, p)
	}
}

// updateAvatar 的请求体是图片原始字节, 空体 = 清除头像。
func (h *handler) updateAvatar(w http.ResponseWriter, r *http.Request, c *caller) (*service.UserProfile, bool) {
	image, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAvatarBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeErr(w, "avatar_too_large", "")
		} else {
			h.writeErr(w, "bad_request", "")
		}
		return nil, false
	}
	p, err := h.d.Users.UpdateAvatar(r.Context(), c.UserID, image)
	if err != nil {
		h.usersErr(w, "avatar", err)
		return nil, false
	}
	return p, true
}

type passwordReq struct {
	Code     string `json:"code"`
	Password string `json:"password"`
}

func (h *handler) handlePassword(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	if !h.setPassword(w, r, c) {
		return
	}
	if p, done := h.profileOf(w, r, c); done {
		h.okUser(w, r, c, p)
	}
}

func (h *handler) setPassword(w http.ResponseWriter, r *http.Request, c *caller) bool {
	var req passwordReq
	if !decodeBody(w, r, &req, maxBody) {
		h.writeErr(w, "bad_request", "")
		return false
	}
	if !digits(req.Code, 6) {
		h.writeErr(w, "bad_request", "请输入 6 位数字验证码")
		return false
	}
	// 先判规则再验码: 码验过即作废, 密码不合规还要用户重新收一条。
	if !validPassword(req.Password) {
		h.writeErr(w, "password_invalid", "")
		return false
	}
	ctx, appID := r.Context(), appOf(r).AppID
	if err := h.d.Auth.VerifyCode(ctx, appID, c.UserID, channelSMS, "", req.Code); err != nil {
		h.manageErr(w, "password", "", err)
		return false
	}
	if err := h.d.Auth.SetPassword(ctx, appID, c.UserID, req.Password); err != nil {
		h.manageErr(w, "password", "", err)
		return false
	}
	return true
}

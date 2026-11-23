package accountapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/basicfu/fp/internal/accountapi/captcha"
	"github.com/basicfu/fp/internal/connector"
	"github.com/basicfu/fp/internal/service"
)

const (
	maxBody = 4 << 10
	// /code 的登录用途带着拖动轨迹(≤200 点),4KB 装不下。
	maxCodeBody = 16 << 10
	// 发码后客户端的倒计时。同一目标 30 秒限 1 条,这里给得更长,让用户少撞限流。
	codeCooldownSec = 60
)

func decodeBody(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v) == nil
}

func (h *handler) handleCaptcha(w http.ResponseWriter, r *http.Request) {
	if h.d.Captcha == nil {
		h.writeErr(w, "unavailable", "验证服务繁忙，请稍后重试")
		return
	}
	c, err := h.d.Captcha.Issue(r.Context())
	if err != nil {
		if !errors.Is(err, context.Canceled) { // 客户端关弹层/断开是常态,不算服务端故障
			slog.Warn("accountapi: 出题失败", "err", err)
		}
		h.writeErr(w, "unavailable", "验证服务繁忙，请稍后重试")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	noStore(w)
	_, _ = w.Write(captcha.EncodeCaptcha(c))
}

// verifyCaptcha 失败时已写好响应。
func (h *handler) verifyCaptcha(w http.ResponseWriter, r *http.Request, id string, x int, track [][3]int) bool {
	if h.d.Captcha == nil {
		h.writeErr(w, "unavailable", "验证服务繁忙，请稍后重试")
		return false
	}
	switch err := h.d.Captcha.Verify(r.Context(), id, x, track); {
	case err == nil:
		return true
	case errors.Is(err, captcha.ErrExpired):
		h.writeErr(w, "captcha_expired", "")
	case errors.Is(err, captcha.ErrFailed):
		h.writeErr(w, "captcha_failed", "")
	default:
		slog.Error("accountapi: 滑块校验出错", "err", err)
		h.writeErr(w, "internal", "")
	}
	return false
}

// 发码的用途与渠道。用途与渠道正交:以后的产品要邮箱登录只是 login + email。
const (
	purposeLogin    = "login"
	purposeVerify   = "verify"
	purposePassword = "password"
	purposeBind     = "bind"
	channelSMS      = service.VerifyChannelSMS
	channelEmail    = service.VerifyChannelEmail
)

type codeReq struct {
	Purpose   string   `json:"purpose"`
	Channel   string   `json:"channel"`
	Target    string   `json:"target"`
	CaptchaID string   `json:"captcha_id"`
	X         *int     `json:"x"`
	Track     [][3]int `json:"track"`
}

type codeResp struct {
	CooldownSec int    `json:"cooldown_sec"`
	Token       string `json:"token,omitempty"`
}

// codeAllowed:用途与渠道的组合必须是现在实现了的。
func codeAllowed(purpose, channel string) bool {
	switch purpose {
	case purposeLogin, purposeVerify, purposePassword:
		return channel == channelSMS
	case purposeBind:
		return channel == channelSMS || channel == channelEmail
	}
	return false
}

// codeOn:登录短信要应用开着短信登录或微信登录(首次扫码要绑手机号,同样发登录短信);设密码的码要开着密码登录。
// 不满足时回 offCode。先于滑块与发码判断:关着的方式不该消耗用户的滑块。失败时已写好响应。
func (h *handler) codeOn(w http.ResponseWriter, r *http.Request, purpose, offCode string) bool {
	var types []string
	switch purpose {
	case purposeLogin:
		types = []string{connector.TypeSMSCode, connector.TypeWechatWeb}
	case purposePassword:
		types = []string{connector.TypePassword}
	default:
		return true
	}
	for _, typ := range types {
		on, ok := h.connectorOn(w, r, typ)
		if !ok {
			return false
		}
		if on {
			return true
		}
	}
	h.writeErr(w, offCode, "")
	return false
}

func (h *handler) handleCode(w http.ResponseWriter, r *http.Request) {
	var req codeReq
	if !decodeBody(w, r, &req, maxCodeBody) {
		h.writeErr(w, "bad_request", "")
		return
	}
	if !codeAllowed(req.Purpose, req.Channel) {
		h.writeErr(w, "bad_request", "")
		return
	}
	if !h.codeOn(w, r, req.Purpose, "bad_request") {
		return
	}
	if req.Purpose == purposeLogin {
		if h.sendLoginCode(w, r, req) {
			ok(w, codeResp{CooldownSec: codeCooldownSec})
		}
		return
	}
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	if h.sendAccountCode(w, r, c, req.Purpose, req.Channel, req.Target) {
		ok(w, codeResp{CooldownSec: codeCooldownSec, Token: c.Rotated})
	}
}

// sendLoginCode 匿名、要滑块、发到 target。格式不对的请求不能走到发码:那边按手机号计限流,也会真的发短信。
func (h *handler) sendLoginCode(w http.ResponseWriter, r *http.Request, req codeReq) bool {
	if req.CaptchaID == "" || req.X == nil || len(req.Track) == 0 {
		h.writeErr(w, "bad_request", "")
		return false
	}
	if !validPhone(req.Target) {
		h.writeErr(w, "bad_request", "请输入正确的手机号")
		return false
	}
	if !h.verifyCaptcha(w, r, req.CaptchaID, *req.X, req.Track) {
		return false
	}
	if err := h.d.Auth.SendLoginCode(r.Context(), appOf(r).AppID, req.Target); err != nil {
		h.loginErr(w, "code-login", err)
		return false
	}
	return true
}

// sendAccountCode:verify、password 只发到当前绑定的号码,不认客户端给的 target(防止拿来往任意号码发码);
// bind 发到 target,已被别的账号占用回 phone_taken / email_taken。
func (h *handler) sendAccountCode(w http.ResponseWriter, r *http.Request, c *caller, purpose, channel, target string) bool {
	taken := ""
	switch purpose {
	case purposeVerify, purposePassword:
		target = ""
	case purposeBind:
		if channel == channelEmail {
			if !validEmail(target) {
				h.writeErr(w, "bad_request", "请输入正确的邮箱")
				return false
			}
			taken = "email_taken"
		} else {
			if !validPhone(target) {
				h.writeErr(w, "bad_request", "请输入正确的手机号")
				return false
			}
			taken = "phone_taken"
		}
	}
	if err := h.d.Auth.SendVerifyCode(r.Context(), appOf(r).AppID, c.UserID, channel, target); err != nil {
		h.manageErr(w, "code-"+purpose, taken, err)
		return false
	}
	return true
}

type codeVerifyReq struct {
	Purpose string `json:"purpose"`
	Channel string `json:"channel"`
	Code    string `json:"code"`
}

type ticketResp struct {
	Ticket string `json:"ticket"`
	Token  string `json:"token,omitempty"`
}

func (h *handler) handleCodeVerify(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	var req codeVerifyReq
	if !decodeBody(w, r, &req, maxBody) || req.Purpose != purposeVerify || req.Channel != channelSMS {
		h.writeErr(w, "bad_request", "")
		return
	}
	if t, done := h.verifyPhone(w, r, c, req.Code); done {
		ok(w, ticketResp{Ticket: t, Token: c.Rotated})
	}
}

// verifyPhone 验当前手机号的码,签一张换绑票据。
func (h *handler) verifyPhone(w http.ResponseWriter, r *http.Request, c *caller, code string) (string, bool) {
	if !digits(code, 6) {
		h.writeErr(w, "bad_request", "请输入 6 位数字验证码")
		return "", false
	}
	ctx := r.Context()
	if err := h.d.Auth.VerifyCode(ctx, appOf(r).AppID, c.UserID, channelSMS, "", code); err != nil {
		h.manageErr(w, "code-verify", "", err)
		return "", false
	}
	t, err := h.tickets.Issue(ctx, c.UserID.String())
	if err != nil {
		slog.Error("accountapi: 签发换绑票据失败", "err", err)
		h.writeErr(w, "internal", "")
		return "", false
	}
	return t, true
}

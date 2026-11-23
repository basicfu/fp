package accountapi

import (
	"log/slog"
	"net/http"
)

type deleteResp struct {
	// PurgeAt 是彻底删除的时刻(毫秒);冷静期内登录即撤销。
	PurgeAt int64 `json:"purge_at"`
}

// handleDelete 提交注销。这个账号的全部会话随之作废,业务方经"退出登录"事件得知。
func (h *handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	c, authed := h.requireUser(w, r)
	if !authed {
		return
	}
	purgeAt, err := h.d.Accounts.RequestDeletion(r.Context(), c.UserID)
	if err != nil {
		h.deleteErr(w, err)
		return
	}
	slog.Info("accountapi: 提交注销", "user", c.UserID, "session", c.Session.ID, "purge_at", purgeAt)
	ok(w, deleteResp{PurgeAt: purgeAt})
}

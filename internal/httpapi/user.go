package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/basicfu/fp/internal/domain"
	"github.com/basicfu/fp/internal/service"
)

type userHandler struct {
	users    *service.UserService
	accounts *service.AccountService
	sessions *service.SessionService
	logs     *service.LoginLogService
}

type identityDTO struct {
	Type        string `json:"type"`
	Subject     string `json:"subject"`
	LastLoginAt int64  `json:"lastLoginAt"`
}

type userDTO struct {
	ID          string        `json:"id"`
	Nickname    string        `json:"nickname"`
	AvatarURL   string        `json:"avatarUrl"`
	Status      string        `json:"status"`
	HasPassword bool          `json:"hasPassword"`
	Identities  []identityDTO `json:"identities"`
	CreatedAt   int64         `json:"createdAt"`
}

// toUserDTO 组装对外的用户视图。
// 注意：password_hash 绝不出现在响应里，只暴露"是否设过密码"。
func toUserDTO(u domain.User, ids []domain.Identity) userDTO {
	out := userDTO{
		ID: u.ID.String(), Nickname: u.Nickname, AvatarURL: u.AvatarURL,
		Status: u.Status, HasPassword: u.PasswordHash != "",
		Identities: make([]identityDTO, 0, len(ids)),
		CreatedAt:  u.CreatedAt,
	}
	for _, i := range ids {
		out.Identities = append(out.Identities, identityDTO{
			Type: i.Type, Subject: i.Subject, LastLoginAt: i.LastLoginAt,
		})
	}
	return out
}

type userListResponse struct {
	Items []userDTO `json:"items"`
	Total int       `json:"total"`
}

func (h *userHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))

	items, total, err := h.users.List(r.Context(), service.UserListQuery{
		Keyword: q.Get("keyword"), Status: q.Get("status"),
		Limit: limit, Offset: offset,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	out := userListResponse{Items: make([]userDTO, 0, len(items)), Total: total}
	for _, it := range items {
		out.Items = append(out.Items, toUserDTO(it.User, it.Identities))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *userHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	u, err := h.users.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	ids, err := h.users.ListIdentities(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(*u, ids))
}

type setStatusRequest struct {
	Status string `json:"status"`
}

// setStatus 改用户状态。状态机校验以及"不可登录状态必须连带撤销会话"这条
// 安全耦合都在 service.AccountService 里，这里只做转发——不含业务逻辑。
func (h *userHandler) setStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req setStatusRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	u, err := h.accounts.SetStatus(r.Context(), id, req.Status)
	if err != nil {
		writeError(w, err)
		return
	}
	ids, err := h.users.ListIdentities(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(*u, ids))
}

type setPasswordRequest struct {
	Password string `json:"password"`
}

// setPassword 管理员重置密码。"改密必须连带撤销全部会话"这条安全耦合在
// service.AccountService 里，这里只做转发——不含业务逻辑。
func (h *userHandler) setPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	var req setPasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := h.accounts.ResetPassword(r.Context(), id, req.Password); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

type sessionDTO struct {
	ID            string `json:"id"`
	AppID         string `json:"appId"`
	IP            string `json:"ip"`
	UA            string `json:"ua"`
	Mobile        bool   `json:"mobile"`
	FirstAuthAt   int64  `json:"firstAuthAt"`
	IdleExpiresAt int64  `json:"idleExpiresAt"`
}

func (h *userHandler) listSessions(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	list, err := h.sessions.ListByUser(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	// 轮换过渡期内同一会话会有新旧两个 token，按会话 ID 去重，
	// 否则管理端会把一台设备显示成两台。
	//
	// 去重时**必须留 IdleExpiresAt 更大的那条**，不能随便留一条：
	// tryRotate 会刻意把旧 token 缩短到 now + max(30s, cache_ttl)，而新 token
	// 拿到完整的空闲窗口。ListUserTokens 走 SMEMBERS，成员数不多时是插入顺序，
	// 也就是**旧的在前**——随便留一条的话，管理端会把一台刚刚轮换过、
	// 完全健康的设备显示成"30 秒后过期"。
	byID := map[string]sessionDTO{}
	order := make([]string, 0, len(list))
	for _, s := range list {
		dto := sessionDTO{
			ID: s.ID, AppID: s.AppID.String(), IP: s.IP, UA: s.UA, Mobile: s.Mobile,
			FirstAuthAt: s.FirstAuthAt, IdleExpiresAt: s.IdleExpiresAt,
		}
		prev, seen := byID[s.ID]
		if !seen {
			order = append(order, s.ID)
			byID[s.ID] = dto
			continue
		}
		if dto.IdleExpiresAt > prev.IdleExpiresAt {
			byID[s.ID] = dto
		}
	}
	out := make([]sessionDTO, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	writeJSON(w, http.StatusOK, out)
}

type revokeResponse struct {
	Revoked int `json:"revoked"`
}

// revokeAllSessions 踢掉该用户的全部设备。
//
// 走 AccountService 而不是直接调 SessionService：撤销要连带写审计，
// 那条耦合和"冻结必须撤销会话"是同一类东西，属于 service 层。
// 放在这里的话，计划二的 gRPC 管理入口会漏掉审计而没有任何测试变红。
func (h *userHandler) revokeAllSessions(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	n, err := h.accounts.RevokeAllSessions(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, revokeResponse{Revoked: n})
}

func (h *userHandler) revokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	n, err := h.accounts.RevokeSession(r.Context(), id, chi.URLParam(r, "sid"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, revokeResponse{Revoked: n})
}

type loginLogDTO struct {
	ID           string `json:"id"`
	IdentityType string `json:"identityType"`
	Subject      string `json:"subject"`
	Event        string `json:"event"`
	Success      bool   `json:"success"`
	Reason       string `json:"reason"`
	IP           string `json:"ip"`
	UA           string `json:"ua"`
	CreatedAt    int64  `json:"createdAt"`
}

func (h *userHandler) listLoginLogs(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	list, err := h.logs.ListByUser(r.Context(), id, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]loginLogDTO, 0, len(list))
	for _, l := range list {
		out = append(out, loginLogDTO{
			ID: l.ID.String(), IdentityType: l.IdentityType, Subject: l.Subject,
			Event: l.Event, Success: l.Success, Reason: l.Reason,
			IP: l.IP, UA: l.UA, CreatedAt: l.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

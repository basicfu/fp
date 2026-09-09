package httpapi

import (
	"net/http"

	"github.com/basicfu/fp/internal/service"
)

// imCredentialHandler 管理 fp-im 网关连 fp 用的那份凭据。
type imCredentialHandler struct {
	svc *service.IMCredentialService
}

// imCredentialStatusDTO 只回答"生成过没有"。
//
// **绝不含 secret 字段。** 库里只有 bcrypt 哈希，本来也回显不出明文；
// DTO 里不留这个字段是为了让以后有人想往里塞的时候，得先改结构体、
// 从而不得不面对"明文只在生成时可见一次"这条纪律。
type imCredentialStatusDTO struct {
	Exists bool `json:"exists"`
}

// imCredentialSecretDTO 是轮换的响应，明文**只在这一次**可见。
type imCredentialSecretDTO struct {
	Secret string `json:"secret"`
}

func (h *imCredentialHandler) status(w http.ResponseWriter, r *http.Request) {
	ok, err := h.svc.Exists(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imCredentialStatusDTO{Exists: ok})
}

// rotate 生成一份新的 IM 凭据，旧的立刻在库这一层失效。
//
// gRPC 拦截器那层还有一个 10 秒的成功缓存窗口（见
// grpcapi.DefaultIMSecretCacheTTL），所以旧凭据最多再活 10 秒。因泄露而
// 轮换时要知道有这个窗口。
func (h *imCredentialHandler) rotate(w http.ResponseWriter, r *http.Request) {
	secret, err := h.svc.Rotate(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, imCredentialSecretDTO{Secret: secret})
}

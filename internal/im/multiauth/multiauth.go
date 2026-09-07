// Package multiauth 按握手帧里的令牌类型把验证请求分派给对应的实现。
// 它自己也实现 auth.Authenticator，所以接入层只持有一个认证器，
// 将来加第三种认证方式时接入层一行都不用动。
package multiauth

import (
	"context"

	"github.com/basicfu/fp/internal/im/auth"
	"github.com/basicfu/fp/internal/im/model"
)

type dispatcher struct {
	fp  auth.Authenticator
	biz auth.Authenticator // 可以为 nil：没有任何 app 配业务方认证时
}

// New 组合两个认证器。biz 为 nil 表示本节点没有业务方认证能力，
// 此时带 biz 类型的握手一律被拒。
func New(fp, biz auth.Authenticator) auth.Authenticator {
	return &dispatcher{fp: fp, biz: biz}
}

func (d *dispatcher) Verify(ctx context.Context, req auth.VerifyRequest) (model.Subject, error) {
	switch req.Kind {
	case "", model.TokenKindFP:
		return d.fp.Verify(ctx, req)
	case model.TokenKindBiz:
		if d.biz == nil {
			return model.Subject{}, auth.ErrUnauthorized
		}
		return d.biz.Verify(ctx, req)
	}
	// 未知类型直接拒，不兜底送给某一个下游：拼错的类型说明调用方有 bug，
	// 静默降级会让它更难被发现，而且报出来的错会指向错误的方向。
	return model.Subject{}, auth.ErrUnauthorized
}

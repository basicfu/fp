package grpcapi

import (
	"context"

	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// notifyServer 实现 fpv1.NotifyServiceServer。
type notifyServer struct {
	fpv1.UnimplementedNotifyServiceServer
	svc  *service.NotifyService
	apps AppLookup
}

func newNotifyServer(svc *service.NotifyService, apps AppLookup) *notifyServer {
	return &notifyServer{svc: svc, apps: apps}
}

// Send 按模板 code 发送一条通知。调用方是用 app_id / secret 认证的应用；
// fp-im 网关的凭据是另一个收窄过的子集，不能发通知。
func (s *notifyServer) Send(ctx context.Context, req *fpv1.SendRequest) (*fpv1.SendResponse, error) {
	if err := requireNotIM(ctx); err != nil {
		return nil, err
	}
	appID, err := callerAppID(ctx)
	if err != nil {
		return nil, err
	}
	// 拦截器只校验 appSecret、不看应用状态；不在这里查一次，停用的应用凭据还能继续发短信/邮件。
	if _, err := s.apps.GetActiveByAppID(ctx, appID); err != nil {
		return nil, StatusFrom(err)
	}
	if err := s.svc.Send(ctx, service.NotifySendInput{
		AppID:          appID,
		Code:           req.GetCode(),
		To:             req.GetTo(),
		Params:         req.GetParams(),
		IdempotencyKey: req.GetIdempotencyKey(),
	}); err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.SendResponse{}, nil
}

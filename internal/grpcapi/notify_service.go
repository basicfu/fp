package grpcapi

import (
	"context"

	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// notifyServer 实现 fpv1.NotifyServiceServer。
type notifyServer struct {
	fpv1.UnimplementedNotifyServiceServer
	svc *service.NotifyService
}

func newNotifyServer(svc *service.NotifyService) *notifyServer {
	return &notifyServer{svc: svc}
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

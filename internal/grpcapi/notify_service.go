package grpcapi

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// notifyServer 实现 fpv1.NotifyServiceServer。
type notifyServer struct {
	fpv1.UnimplementedNotifyServiceServer
	svc    *service.NotifyService
	wechat *service.WechatMPService
	apps   AppLookup
}

func newNotifyServer(svc *service.NotifyService, wechat *service.WechatMPService, apps AppLookup) *notifyServer {
	return &notifyServer{svc: svc, wechat: wechat, apps: apps}
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
	app, err := s.apps.GetActiveByAppID(ctx, appID)
	if err != nil {
		return nil, StatusFrom(err)
	}
	if err := s.svc.Send(ctx, service.NotifySendInput{
		AppID:          app.AppID,
		Code:           req.GetCode(),
		To:             req.GetTo(),
		Params:         req.GetParams(),
		IdempotencyKey: req.GetIdempotencyKey(),
	}); err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.SendResponse{}, nil
}

// activeCaller 与 Send 同一道门：业务应用、且处于启用状态。返回对外 appId。
func (s *notifyServer) activeCaller(ctx context.Context) (string, error) {
	if err := requireNotIM(ctx); err != nil {
		return "", err
	}
	appID, err := callerAppID(ctx)
	if err != nil {
		return "", err
	}
	app, err := s.apps.GetActiveByAppID(ctx, appID)
	if err != nil {
		return "", StatusFrom(err)
	}
	return app.AppID, nil
}

func (s *notifyServer) wechatService() (*service.WechatMPService, error) {
	if s.wechat == nil {
		return nil, status.Error(codes.Unimplemented, "微信通知未启用")
	}
	return s.wechat, nil
}

// CreateWechatSubscribeQR 给用户生成开启微信通知的二维码。用户全局互通，同 UserService 不检查用户是否登录过本应用。
func (s *notifyServer) CreateWechatSubscribeQR(ctx context.Context, req *fpv1.CreateWechatSubscribeQRRequest) (*fpv1.CreateWechatSubscribeQRResponse, error) {
	appID, err := s.activeCaller(ctx)
	if err != nil {
		return nil, err
	}
	w, err := s.wechatService()
	if err != nil {
		return nil, err
	}
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}
	qr, err := w.CreateSubscribeQR(ctx, appID, userID)
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.CreateWechatSubscribeQRResponse{
		ImageUrl: qr.ImageURL, Content: qr.Content, ExpiresInSec: int64(qr.ExpiresIn.Seconds()),
	}, nil
}

func (s *notifyServer) GetWechatSubscription(ctx context.Context, req *fpv1.GetWechatSubscriptionRequest) (*fpv1.GetWechatSubscriptionResponse, error) {
	if _, err := s.activeCaller(ctx); err != nil {
		return nil, err
	}
	w, err := s.wechatService()
	if err != nil {
		return nil, err
	}
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}
	sub, err := w.Subscription(ctx, userID)
	if err != nil {
		return nil, StatusFrom(err)
	}
	out := &fpv1.GetWechatSubscriptionResponse{Bound: sub.Bound, Subscribed: sub.Subscribed}
	if sub.Bound {
		out.BoundAtMs = sub.BoundAt.UnixMilli()
	}
	return out, nil
}

func (s *notifyServer) DeleteWechatSubscription(ctx context.Context, req *fpv1.DeleteWechatSubscriptionRequest) (*fpv1.DeleteWechatSubscriptionResponse, error) {
	if _, err := s.activeCaller(ctx); err != nil {
		return nil, err
	}
	w, err := s.wechatService()
	if err != nil {
		return nil, err
	}
	userID, err := parseUserID(req.GetUserId())
	if err != nil {
		return nil, err
	}
	if err := w.Unbind(ctx, userID); err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.DeleteWechatSubscriptionResponse{}, nil
}

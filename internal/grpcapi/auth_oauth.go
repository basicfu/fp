package grpcapi

import (
	"context"

	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

func (s *authServer) StartOAuthLogin(ctx context.Context, req *fpv1.StartOAuthLoginRequest) (*fpv1.StartOAuthLoginResponse, error) {
	appID, err := businessCaller(ctx)
	if err != nil {
		return nil, err
	}
	st, err := s.auth.StartOAuth(ctx, service.OAuthStartInput{
		AppID: appID, ConnectorType: req.GetConnectorType(), Mode: req.GetMode(), ReturnURL: req.GetReturnUrl(),
	})
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.StartOAuthLoginResponse{
		State: st.State, AuthUrl: st.AuthURL, ProviderAppId: st.ProviderAppID,
		RedirectUri: st.RedirectURI, ExpiresInSec: st.ExpiresInSec,
	}, nil
}

func (s *authServer) ExchangeOAuthTicket(ctx context.Context, req *fpv1.ExchangeOAuthTicketRequest) (*fpv1.ExchangeOAuthTicketResponse, error) {
	appID, err := businessCaller(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.auth.ExchangeOAuth(ctx, service.OAuthClient{
		AppID: appID, IP: req.GetIp(), UA: req.GetUserAgent(), Mobile: req.GetMobile(),
	}, req.GetTicket())
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.ExchangeOAuthTicketResponse{Result: oauthResult(out)}, nil
}

func (s *authServer) PollOAuthLogin(ctx context.Context, req *fpv1.PollOAuthLoginRequest) (*fpv1.PollOAuthLoginResponse, error) {
	appID, err := businessCaller(ctx)
	if err != nil {
		return nil, err
	}
	out, err := s.auth.PollOAuth(ctx, service.OAuthClient{
		AppID: appID, IP: req.GetIp(), UA: req.GetUserAgent(), Mobile: req.GetMobile(),
	}, req.GetState())
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.PollOAuthLoginResponse{Result: oauthResult(out)}, nil
}

func (s *authServer) BindPhoneLogin(ctx context.Context, req *fpv1.BindPhoneLoginRequest) (*fpv1.BindPhoneLoginResponse, error) {
	appID, err := businessCaller(ctx)
	if err != nil {
		return nil, err
	}
	res, err := s.auth.BindPhone(ctx, service.BindPhoneInput{
		OAuthClient: service.OAuthClient{AppID: appID, IP: req.GetIp(), UA: req.GetUserAgent(), Mobile: req.GetMobile()},
		BindTicket:  req.GetBindTicket(), Phone: req.GetPhone(), Code: req.GetCode(),
	})
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.BindPhoneLoginResponse{Login: loginResponse(res)}, nil
}

// businessCaller 与 Login 同一道门：签发会话的接口一律不给 fp-im 网关凭据用。
func businessCaller(ctx context.Context) (string, error) {
	if err := requireNotIM(ctx); err != nil {
		return "", err
	}
	return callerAppID(ctx)
}

func oauthResult(out *service.OAuthOutcome) *fpv1.OAuthLoginResult {
	r := &fpv1.OAuthLoginResult{
		Status: out.Status, BindTicket: out.BindTicket, Nickname: out.Nickname, AvatarUrl: out.AvatarURL,
	}
	if out.Login != nil {
		r.Login = loginResponse(out.Login)
	}
	return r
}

func loginResponse(res *service.LoginResult) *fpv1.LoginResponse {
	return &fpv1.LoginResponse{Token: res.Session.Token, SessionId: res.Session.ID, User: userInfo(res.User)}
}

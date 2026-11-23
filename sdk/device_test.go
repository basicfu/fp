package fpsdk

import (
	"context"
	"sync/atomic"
	"testing"

	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

func TestIdentityCarriesClientRef(t *testing.T) {
	env := newStubEnv(t, func(*fpv1.ValidateTokenRequest) (*fpv1.ValidateTokenResponse, error) {
		return &fpv1.ValidateTokenResponse{UserId: "u1", SessionId: "s1", CacheTtlMs: 60_000, ClientRef: "device-7"}, nil
	})
	id, err := env.auth.Validate(context.Background(), "tok")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if id.ClientRef != "device-7" {
		t.Fatalf("ClientRef = %q", id.ClientRef)
	}
	// 命中缓存时同样带着。
	if id, _ = env.auth.Validate(context.Background(), "tok"); id.ClientRef != "device-7" {
		t.Fatalf("缓存命中时 ClientRef = %q", id.ClientRef)
	}
}

func TestLoginPassesDeviceFields(t *testing.T) {
	var got atomic.Value
	env := newStubEnvFull(t, &stubServer{
		validate: okValidate("u1", 1000),
		login: func(req *fpv1.LoginRequest) (*fpv1.LoginResponse, error) {
			got.Store([2]string{req.GetDevicePub(), req.GetClientRef()})
			return &fpv1.LoginResponse{Token: "t", SessionId: "s"}, nil
		},
	})
	if _, err := env.auth.Login(context.Background(), LoginInput{ConnectorType: "password", DevicePub: "0a0b", ClientRef: "42"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if v, _ := got.Load().([2]string); v != [2]string{"0a0b", "42"} {
		t.Fatalf("服务端收到 %v", v)
	}
}

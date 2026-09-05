package grpcapi

import (
	"context"
	"encoding/json"

	"github.com/basicfu/fp/internal/service"
	fpv1 "github.com/basicfu/fp/sdk/gen/fp/v1"
)

// configServer 实现 fpv1.ConfigServiceServer。
type configServer struct {
	fpv1.UnimplementedConfigServiceServer
	cfgs *service.ConfigService
	apps AppLookup
}

func newConfigServer(cfgs *service.ConfigService, apps AppLookup) *configServer {
	return &configServer{cfgs: cfgs, apps: apps}
}

// GetConfig 返回该应用某分区当前的全部**已配置**的值。
//
// 与 Watch 一样走 GetActiveByAppID：停用的应用不该还能拉到配置，
// 否则 status 又变成一个没人读的死开关（Watch 那里踩过这个）。
func (s *configServer) GetConfig(ctx context.Context, req *fpv1.GetConfigRequest) (*fpv1.GetConfigResponse, error) {
	appIDStr, err := callerAppID(ctx)
	if err != nil {
		return nil, err
	}
	app, err := s.apps.GetActiveByAppID(ctx, appIDStr)
	if err != nil {
		return nil, StatusFrom(err)
	}

	cfg, err := s.cfgs.Current(ctx, app.ID, req.GetType())
	if err != nil {
		return nil, StatusFrom(err)
	}

	// 只装已配置的项。未配置（value 为 JSON null）留在库里是为了让控制台
	// 有那一行待填，但对 SDK 来说它等于不存在——SDK 的 missing 就是
	// "struct 里有、这里没有"。
	values := make(map[string]json.RawMessage, len(cfg.Fields))
	for k, f := range cfg.Fields {
		if f.IsSet() {
			values[k] = f.Value
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.GetConfigResponse{Version: cfg.Seq, Values: string(raw)}, nil
}

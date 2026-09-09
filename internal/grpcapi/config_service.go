package grpcapi

import (
	"context"
	"encoding/json"

	"github.com/basicfu/fp/internal/domain"
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

	// 存的是 YAML 原文，SDK 要的是扁平 {key: JSON值}——这里是唯一的翻译层：
	// 把 YAML 解析成 map[string]any 再序列化成 JSON 字符串。YAML 里写了的
	// key 就是"已配置"，没写就是不存在，不再需要 IsSet 那样的过滤：
	// 旧模型里"建了字段但没填值"这个占位态在自由编辑的 YAML 里没有对应物，
	// 解析出来的 map 天然就是全部已配置的项。
	parsed, err := domain.ParseConfigYAML(cfg.Value)
	if err != nil {
		// Save 已经校验过 YAML 合法性，这里理论上不会失败；万一存量数据
		// 或人工改过库里的内容导致解析出错，当成内部错误处理，不该让 SDK
		// 拿到一份看似成功、实际半份的配置。
		return nil, StatusFrom(err)
	}
	raw, err := json.Marshal(parsed)
	if err != nil {
		return nil, StatusFrom(err)
	}
	return &fpv1.GetConfigResponse{Version: cfg.Seq, Values: string(raw)}, nil
}

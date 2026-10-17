# 微信扫码登录实现计划

设计：`docs/superpowers/specs/2026-10-17-fp-wechat-web-login-design.md`。按下面顺序做，每步测试通过后提交。

1. **配置脱敏 + connector**：`ApplicationService.ListConnectors` 输出掩码、`SetConnector` 先 `MergeSecrets`；`connector.OAuthConnector` 与 `wechat_web`（换 token、取用户信息、降级）；`IdentityTypeWechatWeb`、新错误码；`config.public_url`。
2. **用户层**：`EnsureIdentityInput.AvatarURL`；`UserService.LinkPhone`（换绑、挪绑，单事务加锁）。
3. **登录编排**：`Login` 签发段抽 `completeLogin`；`StartOAuth` / `HandleOAuthCallback` / `ExchangeOAuth` / `PollOAuth` / `BindPhone`；`SendLoginCode` 放宽；Redis 会话存储。
4. **对外接口**：proto 四个 RPC + `scripts/gen.sh`；grpcapi 实现；httpapi `GET /oauth/callback` 与结果页；`cmd/fp` 装配。
5. **SDK 与文档**：`Auth` 四个方法、`OAuthHandler`；控制台中文名；`docs/wechat-login.md`；全量测试（串行，测试库）。

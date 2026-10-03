-- +goose Up
-- 短信验证码登录方式已移除。清掉各应用里残留的启用记录，否则控制台与登录接口
-- 会遇到注册表里不存在的 connector 类型（登录返回 CONNECTOR_UNKNOWN）。用户记录不动。
DELETE FROM application_connector WHERE connector_type = 'sms_code';

-- +goose Down
-- 数据清理不可逆，没有可恢复的内容。
SELECT 1;

-- +goose Up
-- 微信服务号：关注状态与「通知发给谁」的绑定。服务号凭据是通知中心里唯一的 wechat_mp 供应商实例。
--
-- 关注状态按 openid 记，来自服务号的事件推送（关注 / 扫码 / 取关）。event_at 是微信给的事件时间：
-- 推送可能乱序或重试，旧事件不能盖掉新状态。
CREATE TABLE wechat_mp_follower (
    openid     text PRIMARY KEY,
    union_id   text NOT NULL DEFAULT '',
    subscribed boolean NOT NULL,
    event_at   timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 一个 fp 用户的通知发给哪个微信。刻意不挂到 identity 上：通知接收人不是登录凭据，
-- 一个微信可以同时接收几个账号的通知（个人号、工作号），也不该因此和 unionId 归并规则冲突。
-- app_id 只记是哪个应用发起的绑定，供排查用。
CREATE TABLE wechat_mp_binding (
    user_id    uuid PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    openid     text NOT NULL,
    app_id     text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX wechat_mp_binding_openid_idx ON wechat_mp_binding (openid);

-- 一个 fp 部署只接一个服务号：openid 只在本服务号内有意义，两个服务号的 openid 混在一张表里就分不清了。
CREATE UNIQUE INDEX notify_provider_wechat_mp_once ON notify_provider (type) WHERE type = 'wechat_mp';

-- +goose Down
DROP INDEX notify_provider_wechat_mp_once;
DROP TABLE wechat_mp_binding;
DROP TABLE wechat_mp_follower;

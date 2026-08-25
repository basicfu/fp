-- +goose Up
CREATE TABLE app_user (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    -- password_hash 是凭据而非标识：用户名/邮箱/手机号三种标识共用同一个密码。
    -- 未设置密码的用户（仅短信登录）此列为空串。
    password_hash       text NOT NULL DEFAULT '',
    nickname            text NOT NULL DEFAULT '',
    avatar_url          text NOT NULL DEFAULT '',
    gender              text NOT NULL DEFAULT '',
    status              text NOT NULL DEFAULT 'ACTIVE',
    delete_submitted_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

-- identity 是所有登录标识的唯一真相源。
-- type='phone'    subject=手机号
-- type='username' subject=用户名
-- type='email'    subject=邮箱
-- type='wechat_mp' subject=openid  union_key=unionid
CREATE TABLE identity (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id       uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    type          text NOT NULL,
    subject       text NOT NULL,
    -- union_key 用于跨 type 归并（微信 unionId）。本地标识留空串。
    union_key     text NOT NULL DEFAULT '',
    -- credential 存第三方 token 等。密码不存这里，存 app_user.password_hash。
    credential    text NOT NULL DEFAULT '',
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (type, subject)
);
CREATE INDEX identity_user_id_idx ON identity (user_id);
-- 部分索引：只覆盖非空 union_key，本地标识的空串（数量巨大且天然重复）不进索引。
-- 注意：这里不能是 UNIQUE。union_key 的语义就是"同一个 union_key 允许挂多条
-- identity"（规则 2：微信 unionId 下 mp/mini 等多个 openid 归并到同一 user），
-- 唯一约束会直接把归并场景的第二条 INSERT 打成 23505。唯一性在应用层保证的
-- 是"同一 union_key 下的所有 identity 必须指向同一个 user_id"，这不是单列
-- UNIQUE INDEX 能表达的约束。
CREATE INDEX identity_union_key_idx ON identity (union_key) WHERE union_key <> '';

-- 用户在某个应用下的注册关系。
-- 严禁添加 role 列（设计文档 5.5）：角色由 casbin 的 grouping policy 承载，
-- 在授权模块交付前不留任何过渡态。
CREATE TABLE user_application (
    user_id        uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    application_id uuid NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    nickname       text NOT NULL DEFAULT '',
    status         text NOT NULL DEFAULT 'ACTIVE',
    extra          jsonb NOT NULL DEFAULT '{}'::jsonb,
    registered_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, application_id)
);

-- +goose Down
DROP TABLE user_application;
DROP TABLE identity;
DROP TABLE app_user;

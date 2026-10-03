-- +goose Up
-- 供应商实例：一份凭据 = 一个实例。type 对应代码里注册的实现（internal/notify），
-- 同一 type 可以有多个实例（两个阿里云账号、多个 telegram bot）。
-- 不加显式 ON DELETE RESTRICT：PG18 下它报的 SQLSTATE 是 23001 而不是 23503，
-- 而 service 层的 isForeignKeyViolation 只认 23503；默认的 NO ACTION 报 23503。
CREATE TABLE notify_provider (
    id          uuid PRIMARY KEY DEFAULT uuidv7(),
    type        text NOT NULL,
    description text NOT NULL DEFAULT '',
    enabled     boolean NOT NULL DEFAULT true,
    config      jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notify_provider_type_idx ON notify_provider (type);

-- 模板：业务代码按 code 引用的一条通知。code 人工指定、直接做主键。
-- template 是 domain.NotifyContent 的 JSON。
CREATE TABLE notify_template (
    code          text PRIMARY KEY,
    channel       text NOT NULL,
    template_mode text NOT NULL,
    template      jsonb NOT NULL DEFAULT '{}'::jsonb,
    description   text NOT NULL DEFAULT '',
    enabled       boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- 模板 ↔ 供应商实例，所有渠道通用。sms / email 可挂多个（随机 + 降级）；
-- IM / webhook 约定只挂一个（应用层限制，不是库约束，日后放开不用改表）。
CREATE TABLE notify_template_provider (
    code                 text NOT NULL REFERENCES notify_template(code) ON DELETE CASCADE,
    provider_id          uuid NOT NULL REFERENCES notify_provider(id),
    provider_template_id text NOT NULL DEFAULT '',
    enabled              boolean NOT NULL DEFAULT true,
    priority             int NOT NULL DEFAULT 0,
    PRIMARY KEY (code, provider_id)
);
-- 复合主键里 provider_id 在第二位，"供应商被哪些模板引用"要单独的索引。
CREATE INDEX notify_template_provider_provider_idx ON notify_template_provider (provider_id);

-- 发送记录沿用现有表：原 template 列存的就是模板 key，改名为 code；
-- provider 列继续存供应商 type，provider_id 记具体实例，app_id 记调用方应用（控制台测试发送为空）。
ALTER TABLE notify_log RENAME COLUMN template TO code;
ALTER TABLE notify_log ADD COLUMN provider_id uuid;
ALTER TABLE notify_log ADD COLUMN app_id text NOT NULL DEFAULT '';
CREATE INDEX notify_log_code_idx ON notify_log (code, created_at DESC);

-- +goose Down
DROP INDEX notify_log_code_idx;
ALTER TABLE notify_log DROP COLUMN app_id;
ALTER TABLE notify_log DROP COLUMN provider_id;
ALTER TABLE notify_log RENAME COLUMN code TO template;
DROP TABLE notify_template_provider;
DROP TABLE notify_template;
DROP TABLE notify_provider;

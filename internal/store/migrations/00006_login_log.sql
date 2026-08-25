-- +goose Up
-- 登录审计。失败的登录同样要记录，此时 user_id 为空，只留脱敏后的 subject。
CREATE TABLE login_log (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id        uuid REFERENCES app_user(id) ON DELETE SET NULL,
    application_id uuid REFERENCES application(id) ON DELETE SET NULL,
    identity_type  text NOT NULL DEFAULT '',
    -- subject 存脱敏值（138****8000），完整标识通过 user_id 关联查询。
    subject        text NOT NULL DEFAULT '',
    event          text NOT NULL,
    success        boolean NOT NULL,
    reason         text NOT NULL DEFAULT '',
    ip             text NOT NULL DEFAULT '',
    ua             text NOT NULL DEFAULT '',
    session_id     text NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX login_log_user_idx ON login_log (user_id, created_at DESC);
CREATE INDEX login_log_created_idx ON login_log (created_at DESC);

-- +goose Down
DROP TABLE login_log;

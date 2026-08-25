-- +goose Up
-- 发送记录。注意：绝不写入验证码本身，params 只留非敏感的模板变量名。
CREATE TABLE notify_log (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    channel    text NOT NULL,
    target     text NOT NULL,
    template   text NOT NULL,
    provider   text NOT NULL DEFAULT '',
    success    boolean NOT NULL,
    error      text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notify_log_target_idx ON notify_log (target, created_at DESC);

-- +goose Down
DROP TABLE notify_log;

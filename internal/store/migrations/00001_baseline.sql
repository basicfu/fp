-- +goose Up
-- baseline：只放公共扩展与约定，业务表由后续迁移各自添加。
-- PostgreSQL 18 内置 uuidv7()，无需扩展。
CREATE TABLE IF NOT EXISTS schema_note (
    key   text PRIMARY KEY,
    value text NOT NULL
);
INSERT INTO schema_note (key, value)
VALUES ('baseline', 'fp phase 1')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS schema_note;

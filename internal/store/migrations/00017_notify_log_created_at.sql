-- +goose Up
-- 控制台的发送记录默认不按 code 过滤，按 created_at DESC, id DESC 翻页。没有这条索引就是全表排序，
-- 而每次尝试都写一行、从不清理。
CREATE INDEX notify_log_created_at_idx ON notify_log (created_at DESC, id DESC);

-- +goose Down
DROP INDEX notify_log_created_at_idx;

-- +goose Up
-- 注销生效时留下的最少身份信息，供调查安全事件、依法配合有关部门使用，到期删除。
--
-- 只有 fp 进程里的注销清理任务读写这张表，不经任何接口暴露（控制台、gRPC 都没有）。
-- 不挂 app_user 外键：墓碑与归档在同一个事务里删，外键的级联只会让"先删谁"变成隐含约束。
-- 手机号、UnionID 用数组：一个账号挂多个手机号或多个微信主体时一个都不能漏。
CREATE TABLE user_deletion_archive (
    user_id          uuid PRIMARY KEY,
    phones           text[] NOT NULL DEFAULT '{}',
    wechat_union_ids text[] NOT NULL DEFAULT '{}',
    submitted_at     timestamptz NOT NULL,
    deleted_at       timestamptz NOT NULL,
    expires_at       timestamptz NOT NULL
);
CREATE INDEX user_deletion_archive_expires_idx ON user_deletion_archive (expires_at);

-- 到期扫描只看冷静期里的那一小撮用户，部分索引让它不必扫整张用户表。
CREATE INDEX app_user_pending_delete_idx ON app_user (delete_submitted_at) WHERE status = 'PENDING_DELETE';

-- +goose Down
DROP INDEX app_user_pending_delete_idx;
DROP TABLE user_deletion_archive;

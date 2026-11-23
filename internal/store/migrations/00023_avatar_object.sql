-- +goose Up

-- 头像改存对象存储：object_key 非空表示图片在 OSS 里。data 列本迁移不动，
-- 老路径与迁移期间的回滚还要靠它，等全部回填并稳定后再单独删。
ALTER TABLE user_avatar ADD COLUMN object_key text NOT NULL DEFAULT '';

-- 待删除的对象。换头像、清除、账号彻底删除时在同一事务里登记旧 key，
-- 由后台任务重试删除：事务提交与删 OSS 对象无法原子，登记表保证不漏删。
CREATE TABLE object_gc (
    key      text        PRIMARY KEY,
    due_at   timestamptz NOT NULL DEFAULT now(),
    attempts int         NOT NULL DEFAULT 0
);
CREATE INDEX object_gc_due_idx ON object_gc (due_at);

-- +goose Down
DROP TABLE object_gc;
ALTER TABLE user_avatar DROP COLUMN object_key;

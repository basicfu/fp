-- +goose Up
ALTER TABLE app_user ADD COLUMN avatar_hash text NOT NULL DEFAULT '';

-- 注册来源：登录流程自动建号时，把当时那个应用的行标为 true；管理员在控制台手动建的号没有任何一行为 true。
ALTER TABLE user_extra ADD COLUMN is_registration boolean NOT NULL DEFAULT false;
-- 一个用户至多一个注册来源。
CREATE UNIQUE INDEX user_extra_registration_idx ON user_extra (user_id) WHERE is_registration;
-- 存量用户按最早登录过的应用回填。DISTINCT ON 而不是 first_login_at = min(...)：
-- 两行时间并列时后者会标出两行，撞上面的唯一索引，整个迁移失败、fp 起不来。
UPDATE user_extra e SET is_registration = true
FROM (
    SELECT DISTINCT ON (user_id) user_id, application_id
    FROM user_extra
    ORDER BY user_id, first_login_at, application_id
) f
WHERE e.user_id = f.user_id AND e.application_id = f.application_id;

-- 一个用户至多一张头像，随用户删除。
-- 不做按哈希去重的独立内容表：那要引用计数才能清理孤儿，省下的只是偶尔撞图的几 KB。
-- 图片不放进 app_user：登录、校验、列表各处查询共用同一组用户列，不能让它们顺带读出图片。
CREATE TABLE user_avatar (
    user_id      uuid PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    hash         text NOT NULL,
    content_type text NOT NULL,
    data         bytea NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_avatar_hash_idx ON user_avatar (hash);

-- +goose Down
DROP TABLE user_avatar;
DROP INDEX user_extra_registration_idx;
ALTER TABLE user_extra DROP COLUMN is_registration;
ALTER TABLE app_user DROP COLUMN avatar_hash;

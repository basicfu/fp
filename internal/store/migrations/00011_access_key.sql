-- +goose Up

-- 访问密钥：第三方程序调用业务方接口的凭据。全局，不属于应用。
-- secret 暂时明文存储（spec「已接受的风险」第 3 条），以后加密时 SK 的值不变。
CREATE TABLE access_key (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    access_key_id text NOT NULL UNIQUE,
    secret        text NOT NULL,
    remark        text NOT NULL,
    -- 可为空。默认行为（NO ACTION）：被绑定的角色必须先改绑才能删，静默摘掉会
    -- 让合作方突然全部 403——不用 SET NULL，也不用 CASCADE。
    role_key      text REFERENCES role(key),
    allowed_ips   cidr[] NOT NULL DEFAULT '{}',
    status        text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE', 'DISABLED')),
    expires_at    timestamptz,
    last_used_at  timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX access_key_role_idx ON access_key (role_key);

-- 内置访客角色：匿名请求只有它，登录用户判定时并入它。库里已有同名角色就沿用。
INSERT INTO role (key, name) VALUES ('GUEST', '访客') ON CONFLICT (key) DO NOTHING;

-- +goose Down
-- GUEST 行不删：它可能在迁移之前就被人建过。
DROP TABLE access_key;

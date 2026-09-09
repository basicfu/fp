-- +goose Up

-- ---------------------------------------------------------------------------
-- IM 凭据：fp-im 网关用来连 fp 的那一份。
--
-- 单行表，CHECK (id = 1) 把"只能有一条"写进 schema 而不是靠代码自觉。
-- fp-im 是一个服务而不是一群应用，多实例 fp-im 共用同一份凭据。
--
-- 为什么不复用 application 表加一个 kind 列：那会造出一条"不是应用的应用"
-- ——会话策略、登录方式、配置分区、默认角色对它全是死字段，控制台还要
-- 处处判断"这条要不要展示"。IM 凭据与业务应用是两种东西。
--
-- 只存 bcrypt 哈希，与 application.app_secret_hash 同一纪律：明文只在生成
-- 时返回一次，之后无法读回。
-- ---------------------------------------------------------------------------
CREATE TABLE im_credential (
    id          smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    secret_hash text        NOT NULL,
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- 应用的 IM 接入配置。
--
-- im_enabled 默认 false：一个应用在有人明确打开之前连不上 fp-im——业务
-- server 接不进来，client 握手也拒。这条默认值就是"新版 fp 发布后对现网
-- 零影响"的全部依据。
--
-- im_biz_auth 用可空 jsonb 而不是铺平成三列：biz_auth 是"整组有或整组
-- 没有"的东西，铺平之后就得靠"verify_url 是不是空串"这种间接判断，而
-- internal/im/model 的 BizAuth 当初选嵌套正是为了避开这一点。SQL 的 NULL
-- 恰好是同一个语义。
--
-- im_conn_policy 的取值必须与 internal/im/model.Policy 逐字相同。两处常量
-- 分处 internal/domain 与 internal/im/model，任何一边单独看都只是孤立的
-- 字符串字面量，配对关系由 internal/integration 的测试守护。
-- ---------------------------------------------------------------------------
ALTER TABLE application
    ADD COLUMN im_enabled       boolean NOT NULL DEFAULT false,
    ADD COLUMN im_conn_policy   text    NOT NULL DEFAULT 'replace'
        CHECK (im_conn_policy IN ('replace', 'reject', 'limit')),
    ADD COLUMN im_conn_limit    integer NOT NULL DEFAULT 5,
    ADD COLUMN im_allow_guest   boolean NOT NULL DEFAULT false,
    ADD COLUMN im_guest_ip_rate integer NOT NULL DEFAULT 20,
    ADD COLUMN im_biz_auth      jsonb;

-- +goose Down
ALTER TABLE application
    DROP COLUMN im_enabled,
    DROP COLUMN im_conn_policy,
    DROP COLUMN im_conn_limit,
    DROP COLUMN im_allow_guest,
    DROP COLUMN im_guest_ip_rate,
    DROP COLUMN im_biz_auth;
DROP TABLE im_credential;

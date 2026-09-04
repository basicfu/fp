-- +goose Up

-- ---------------------------------------------------------------------------
-- user_application → user_extra
--
-- 改名的理由：原表名读不出它是干什么的。它记录的是"这个人用过这个应用"
-- 这个事实，只在首次登录时写一次。
--
-- 同时删掉 nickname / status / extra 三列——自建表起从未被任何代码读写。
-- 它们对应 per-app 的用户资料与状态两个还没做的功能，而 per-app status
-- 真要启用需要改登录流程同时判全局与应用内状态，不是加列就完事。留着的
-- 唯一后果是让下一个人以为有代码在用（本仓库刚被 ApplicationStatusDisabled
-- 坑过一次：有字段、有常量、有 DTO 输出，就是没人写它）。
-- ---------------------------------------------------------------------------
ALTER TABLE user_application RENAME TO user_extra;
ALTER TABLE user_extra DROP COLUMN nickname;
ALTER TABLE user_extra DROP COLUMN status;
ALTER TABLE user_extra DROP COLUMN extra;
ALTER TABLE user_extra RENAME COLUMN registered_at TO first_login_at;

-- 按应用统计用过的人数、新增曲线、以及控制台按应用筛用户。
CREATE INDEX user_extra_app_idx ON user_extra (application_id, first_login_at DESC);

-- ---------------------------------------------------------------------------
-- 角色。**全局**，不带 application_id。
--
-- 角色的应用归属由它挂了哪些应用的权限点决定（见 role_permission），不由
-- 角色本身声明。这让「普通用户」这类天然跨应用的角色只需建一个、同时挂上
-- 商城与视频的权限点，而不必在每个应用里各建一个同名角色——它们描述的
-- 本来就是同一个身份的两面。
--
-- 应用专属的角色靠命名约定区分：商城管理员 / 视频管理员 / 商城客服。
-- 这是约定不是约束。当前只有一个平台管理员，不构成问题；将来若要做应用级
-- 的角色隔离，给本表加一个可空的 application_id（NULL = 全局）即可，已有
-- 数据与判定逻辑都不用改。
--
-- key 是身份，**不可修改**——user_role.roles 按字符串引用它（没有外键），
-- 且已签发的会话里刻着它，改了那批用户会在会话刷新前丢掉这个角色。
-- 要改显示文字改 name。
-- ---------------------------------------------------------------------------
CREATE TABLE role (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    key        text NOT NULL UNIQUE,
    name       text NOT NULL,
    -- 角色继承。子角色拥有父角色的全部权限，展开在服务端完成。
    parent_id  uuid REFERENCES role(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- 权限点。
--
-- 命名说明：Casdoor 里 "Permission" 指的是授权本身（主体+资源+动作+效果），
-- 而这里 permission 指的是**被授权的那个东西**，role_permission 才是授权。
-- 对着 Casdoor 文档看本表时注意这个差异。
--
-- key 由路由模式推导（"GET:/orders/{id}"），**可以修改**——role_permission
-- 按 id 引用，授权关系自动跟随；会话里也不含权限点 key。改完推一次
-- PolicyChanged 让 SDK 重拉即可。改完之后下次上报会如实反映代码。
-- ---------------------------------------------------------------------------
CREATE TABLE permission (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    application_id uuid NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    key            text NOT NULL,
    -- 显示名。人维护，上报只在首次创建时写入，之后不覆盖——否则每次重启，
    -- 运营填的中文名就没了。
    name           text NOT NULL DEFAULT '',
    -- api / menu / button。本期只有 api，menu 与 button 留着不启用：
    -- 以后加菜单只是多一种 kind 加一个枚举接口，不需要改表、更不需要让
    -- 所有已接入方重新上报一遍。
    kind           text NOT NULL DEFAULT 'api',
    -- 菜单树与权限点分组用，本期恒为空。
    parent_id      uuid REFERENCES permission(id) ON DELETE SET NULL,
    -- app（SDK 上报）| manual（人手动加）。manual 的永远不被上报触碰。
    source         text NOT NULL,
    -- 最后一次出现在上报里的时刻。manual 的为 NULL。
    --
    -- 状态不存库，由它算出来：source='manual' → 手动；
    -- now - last_seen_at > 阈值 → 过渡中；其余 → 正常。
    -- 判据是"多久没有任何实例报过它"而不是"这次上报里有没有"——业务服务
    -- 多半多实例，滚动发布时新旧版本同时在跑，交替上报会让权限点在两个
    -- 状态间反复横跳。
    last_seen_at   timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (application_id, key)
);

CREATE INDEX permission_app_idx ON permission (application_id, kind);

-- ---------------------------------------------------------------------------
-- 角色 → 权限点。
--
-- 两个外键，不再抄一份 application_id——权限点属于哪个应用由 permission
-- 自己说。删权限点连带删授权关系由数据库的级联保证，不靠代码记得清理。
-- ---------------------------------------------------------------------------
CREATE TABLE role_permission (
    role_id       uuid NOT NULL REFERENCES role(id) ON DELETE CASCADE,
    permission_id uuid NOT NULL REFERENCES permission(id) ON DELETE CASCADE,
    -- allow | deny。deny-override：一个角色集合里只要有 deny 就拒。
    effect        text NOT NULL DEFAULT 'allow',
    PRIMARY KEY (role_id, permission_id)
);

CREATE INDEX role_permission_perm_idx ON role_permission (permission_id);

-- ---------------------------------------------------------------------------
-- 用户 → 角色。**每人一行**，不按应用拆——角色是全局的。
--
-- 稀疏：没有行表示"只有各应用的默认角色"。新注册用户一行都不写。
--
-- 用 text[] 而不是关系表，是刻意的取舍：数组人眼可读、登录时零 join
--（会话与 SDK 策略表里用的都是 key 字符串）。代价是没有外键，删角色时
-- 必须连带 array_remove——这条耦合放在 service 层并配测试，与仓库既有的
--「冻结账号必须撤销会话」是同一类处理。
-- ---------------------------------------------------------------------------
CREATE TABLE user_role (
    user_id    uuid PRIMARY KEY REFERENCES app_user(id) ON DELETE CASCADE,
    roles      text[] NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- 控制台查"谁持有某个角色"。
CREATE INDEX user_role_roles_idx ON user_role USING gin (roles);

-- ---------------------------------------------------------------------------
-- 应用的默认角色。
--
-- 有效角色 = 用户的全局角色 ∪ 该应用的默认角色。
--
-- 是并集而不是"没有显式角色才用默认"：角色是全局的，若改成后者，给某人
-- 加一个「商城管理员」会让他在视频里只剩这一个角色，而它在视频没有任何
-- 权限——顺手把他看视频的能力弄没了。
-- ---------------------------------------------------------------------------
ALTER TABLE application ADD COLUMN default_role_key text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE application DROP COLUMN default_role_key;
DROP TABLE user_role;
DROP TABLE role_permission;
DROP TABLE permission;
DROP TABLE role;
DROP INDEX user_extra_app_idx;
ALTER TABLE user_extra RENAME COLUMN first_login_at TO registered_at;
ALTER TABLE user_extra ADD COLUMN extra jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE user_extra ADD COLUMN status text NOT NULL DEFAULT 'ACTIVE';
ALTER TABLE user_extra ADD COLUMN nickname text NOT NULL DEFAULT '';
ALTER TABLE user_extra RENAME TO user_application;

-- +goose Up
-- 配置的版本快照。一次保存一行，自包含：fields 里装该分区**全部**配置项的
-- 类型、备注与值。当前配置 = 该分区 seq 最大的那一行。
--
-- 为什么不是"只存变更点"的时态行：回滚在这里是复制一行，没有可以写错的
-- 地方；时态行的回滚要靠 seq <= N 加 DISTINCT ON 加一层删除过滤全都写对
-- 才对。而且只有整版快照能安全修剪——时态行按 seq 删老行会把"某个字段
-- 最后一次修改恰好落在老版本里"的那行删掉，那个字段就凭空消失了。
--
-- type 是分区不是标记：主键含它，所以同名 key 在 DEFAULT 与 WEB 下是两个
-- 独立的配置项，各有各的值与版本序列。WEB 分区会被下发到浏览器。
CREATE TABLE config (
    application_id uuid        NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    type           text        NOT NULL,
    seq            bigint      NOT NULL,
    fields         jsonb       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id, type, seq)
);

-- +goose Down
DROP TABLE config;

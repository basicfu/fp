-- +goose Up
-- 配置中心从"逐字段的结构化 map"改成"一整份 YAML 原文"：不再需要控制台
-- 逐个维护 key/type/desc/value，一个字段直接就是 YAML 里的一行，注释
-- 就是备注。value 原样存管理端提交的文本，不经过任何"解析成对象再序列化
-- 回来"的往返——那样会丢注释、打乱 key 顺序。
--
-- 这张表此前只在本地开发环境跑过（没有对外发布过的正式数据），所以这里
-- 直接删列重加，不做"把旧 fields jsonb 转成等价 YAML 文本"的数据迁移。
ALTER TABLE config DROP COLUMN fields;
ALTER TABLE config ADD COLUMN value text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE config DROP COLUMN value;
ALTER TABLE config ADD COLUMN fields jsonb NOT NULL DEFAULT '{}'::jsonb;

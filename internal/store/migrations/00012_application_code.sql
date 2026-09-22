-- +goose Up

-- 管理台展示的字段名是"code"，库里跟着改名保持一致，免得以后维护的人
-- 对着 UI 找不到对应的库字段。RENAME COLUMN 不影响数据和原有的
-- UNIQUE 约束（约束仍然生效，只是内部索引名还留着 slug 字样，不影响功能）。
ALTER TABLE application RENAME COLUMN slug TO code;

-- +goose Down
ALTER TABLE application RENAME COLUMN code TO slug;

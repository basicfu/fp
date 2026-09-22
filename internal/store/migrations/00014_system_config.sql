-- +goose Up
-- 系统配置的版本快照：一次保存一行，Value 是 YAML 原文，原样存储——
-- 与 config 表同一条"整版快照"纪律（见
-- docs/superpowers/specs/2026-09-05-fp-config-center-design.md 第三节）。
-- 没有 application_id / type：fp 自己只有一份系统配置，不需要这两个维度。
CREATE TABLE system_config (
    seq        bigint      NOT NULL PRIMARY KEY,
    value      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE system_config;

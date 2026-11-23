-- +goose Up

-- 终端用户账号接口的应用配置。
--
-- domains 是读取用的副本，唯一性由 application_domain 的主键保证：
-- 数组列上做跨行唯一约束要靠触发器或排他约束，不如一张小表直接。
-- 两处由 ApplicationService.SetAccount 在同一事务里一起写。
ALTER TABLE application
    ADD COLUMN domains text[]  NOT NULL DEFAULT '{}',
    ADD COLUMN account jsonb   NOT NULL DEFAULT '{}';

CREATE TABLE application_domain (
    domain text PRIMARY KEY,
    app_id uuid NOT NULL REFERENCES application(id) ON DELETE CASCADE
);
CREATE INDEX application_domain_app_idx ON application_domain (app_id);

-- +goose Down
DROP TABLE application_domain;
ALTER TABLE application
    DROP COLUMN domains,
    DROP COLUMN account;

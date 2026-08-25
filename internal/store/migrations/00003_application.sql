-- +goose Up
CREATE TABLE application (
    id                          uuid PRIMARY KEY DEFAULT uuidv7(),
    name                        text NOT NULL,
    slug                        text NOT NULL UNIQUE,
    app_id                      text NOT NULL UNIQUE,
    app_secret_hash             text NOT NULL,
    status                      text NOT NULL DEFAULT 'ACTIVE',

    -- 会话三参数 + 缓存与降频窗口（设计文档 4.4 / 4.5.2）
    idle_timeout_seconds        int NOT NULL DEFAULT 604800,   -- 7d
    idle_timeout_mobile_seconds int NOT NULL DEFAULT 2592000,  -- 30d
    max_lifetime_seconds        int NOT NULL DEFAULT 7776000,  -- 90d
    rotate_interval_seconds     int NOT NULL DEFAULT 86400,    -- 24h
    extend_interval_seconds     int NOT NULL DEFAULT 600,      -- 10min
    token_cache_ttl_seconds     int NOT NULL DEFAULT 30,

    cookie_domain               text NOT NULL DEFAULT '',

    -- OIDC 预留（设计文档十四章：暂不实现，但留位置）
    redirect_uris               text[] NOT NULL DEFAULT '{}',
    grant_types                 text[] NOT NULL DEFAULT '{}',

    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE application_connector (
    application_id uuid NOT NULL REFERENCES application(id) ON DELETE CASCADE,
    connector_type text NOT NULL,
    enabled        boolean NOT NULL DEFAULT true,
    config         jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id, connector_type)
);

-- +goose Down
DROP TABLE application_connector;
DROP TABLE application;

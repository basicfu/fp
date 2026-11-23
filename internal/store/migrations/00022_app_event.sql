-- +goose Up
-- 业务事件表：登录成功、退出登录、账号删除、微信订阅变化，经 Watch 流可靠投递给开了事件的应用。
--
-- seq 在应用内连续递增，由 app_event_seq 的行锁串行发号：投递按 seq 排序，同一用户的事件先后不能乱。
-- 不用 sequence：sequence 跨应用共享且回滚会留洞，看不出是丢了事件还是洞。
CREATE TABLE app_event (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    app_id     uuid NOT NULL,
    seq        bigint NOT NULL,
    user_id    uuid,
    kind       text NOT NULL,
    payload    jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    acked_at   timestamptz,
    UNIQUE (app_id, seq)
);
-- 投递只取未确认的，部分索引让已确认的历史不拖慢它。
CREATE INDEX app_event_pending_idx ON app_event (app_id, seq) WHERE acked_at IS NULL;
CREATE INDEX app_event_acked_idx ON app_event (acked_at) WHERE acked_at IS NOT NULL;

CREATE TABLE app_event_seq (
    app_id uuid PRIMARY KEY,
    next   bigint NOT NULL
);

-- +goose Down
DROP TABLE app_event_seq;
DROP TABLE app_event;

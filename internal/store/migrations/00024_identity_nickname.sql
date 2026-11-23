-- +goose Up
-- 三方身份自带的昵称（微信昵称）。账号面板显示"绑的是哪个微信"，原先由业务方自己存，搬进来后各产品共用。
ALTER TABLE identity ADD COLUMN nickname text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE identity DROP COLUMN nickname;

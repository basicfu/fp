-- +goose Up
ALTER TABLE role RENAME COLUMN key TO code;

-- +goose Down
ALTER TABLE role RENAME COLUMN code TO key;

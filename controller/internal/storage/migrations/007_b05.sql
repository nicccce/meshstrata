-- +goose Up
-- Node-local checkpoint and execution state use the Agent's private store.
SELECT 1;

-- +goose Down
SELECT 1;

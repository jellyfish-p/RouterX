-- Revert channel routing config (PostgreSQL)

DROP INDEX IF EXISTS idx_channels_status;
DROP INDEX IF EXISTS idx_channels_priority;
DROP INDEX IF EXISTS idx_channels_type;
DROP INDEX IF EXISTS idx_channels_idx;

ALTER TABLE channels DROP COLUMN upstream_options;
ALTER TABLE channels DROP COLUMN channel_group;
ALTER TABLE channels DROP COLUMN model_rewrites;
ALTER TABLE channels DROP COLUMN upstreams;
ALTER TABLE channels DROP COLUMN key_cursor;
ALTER TABLE channels DROP COLUMN key_selection_mode;
ALTER TABLE channels DROP COLUMN api_keys;
ALTER TABLE channels DROP COLUMN base_urls;
ALTER TABLE channels DROP COLUMN idx;

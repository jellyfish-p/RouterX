-- Revert channel routing config (MySQL)

ALTER TABLE channels
    DROP INDEX idx_channels_status,
    DROP INDEX idx_channels_priority,
    DROP INDEX idx_channels_type,
    DROP INDEX idx_channels_idx,
    DROP COLUMN upstream_options,
    DROP COLUMN channel_group,
    DROP COLUMN model_rewrites,
    DROP COLUMN upstreams,
    DROP COLUMN key_cursor,
    DROP COLUMN key_selection_mode,
    DROP COLUMN api_keys,
    DROP COLUMN base_urls,
    DROP COLUMN idx;

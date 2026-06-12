-- Extend channel routing config (MySQL)

ALTER TABLE channels
    ADD COLUMN idx INT NOT NULL DEFAULT 0,
    ADD COLUMN base_urls JSON,
    ADD COLUMN api_keys JSON,
    ADD COLUMN key_selection_mode VARCHAR(16) NOT NULL DEFAULT 'round_robin',
    ADD COLUMN key_cursor INT NOT NULL DEFAULT 0,
    ADD COLUMN upstreams JSON,
    ADD COLUMN model_rewrites JSON,
    ADD COLUMN channel_group VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN upstream_options JSON,
    ADD INDEX idx_channels_idx (idx),
    ADD INDEX idx_channels_type (type),
    ADD INDEX idx_channels_priority (priority),
    ADD INDEX idx_channels_status (status);

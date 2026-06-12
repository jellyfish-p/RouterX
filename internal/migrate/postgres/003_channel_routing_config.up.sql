-- Extend channel routing config (PostgreSQL)

ALTER TABLE channels ADD COLUMN idx INT NOT NULL DEFAULT 0;
ALTER TABLE channels ADD COLUMN base_urls JSON;
ALTER TABLE channels ADD COLUMN api_keys JSON;
ALTER TABLE channels ADD COLUMN key_selection_mode VARCHAR(16) NOT NULL DEFAULT 'round_robin';
ALTER TABLE channels ADD COLUMN key_cursor INT NOT NULL DEFAULT 0;
ALTER TABLE channels ADD COLUMN upstreams JSON;
ALTER TABLE channels ADD COLUMN model_rewrites JSON;
ALTER TABLE channels ADD COLUMN channel_group VARCHAR(64) NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN upstream_options JSON;

CREATE INDEX IF NOT EXISTS idx_channels_idx ON channels(idx);
CREATE INDEX IF NOT EXISTS idx_channels_type ON channels(type);
CREATE INDEX IF NOT EXISTS idx_channels_priority ON channels(priority);
CREATE INDEX IF NOT EXISTS idx_channels_status ON channels(status);

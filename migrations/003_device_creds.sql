-- 003_device_creds.sql — per-device bastion credentials
ALTER TABLE devices ADD COLUMN IF NOT EXISTS ssh_user TEXT DEFAULT '';
ALTER TABLE devices ADD COLUMN IF NOT EXISTS ssh_pass TEXT DEFAULT '';
ALTER TABLE devices ADD COLUMN IF NOT EXISTS ssh_port INTEGER DEFAULT 22;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS api_user TEXT DEFAULT '';
ALTER TABLE devices ADD COLUMN IF NOT EXISTS api_pass TEXT DEFAULT '';
ALTER TABLE devices ADD COLUMN IF NOT EXISTS telnet_port INTEGER DEFAULT 23;
ALTER TABLE devices ADD COLUMN IF NOT EXISTS note TEXT DEFAULT '';
-- ensure existing rows get defaults
UPDATE devices SET ssh_port=22 WHERE ssh_port IS NULL;
UPDATE devices SET telnet_port=23 WHERE telnet_port IS NULL;

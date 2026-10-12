-- 004_alerts.sql — Alert rules, events, channels (P1)
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- Alert rules: threshold-based triggers per device/group/global
CREATE TABLE IF NOT EXISTS alert_rules (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  description TEXT DEFAULT '',
  enabled BOOLEAN DEFAULT true,
  metric TEXT NOT NULL CHECK (metric IN ('device_status','latency','packet_loss','cpu','mem')),
  operator TEXT NOT NULL DEFAULT '>' CHECK (operator IN ('>','>=','<','<=','=','!=','offline','online')),
  threshold DOUBLE PRECISION,
  severity TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('critical','warning','info')),
  group_id UUID REFERENCES device_groups(id) ON DELETE SET NULL,
  device_id TEXT REFERENCES devices(id) ON DELETE CASCADE,
  cooldown_sec INTEGER DEFAULT 300,
  notify_channel_ids UUID[] DEFAULT '{}',
  created_at TIMESTAMPTZ DEFAULT now(),
  updated_at TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_alert_rules_tenant ON alert_rules(tenant_id);
CREATE INDEX IF NOT EXISTS idx_alert_rules_metric ON alert_rules(metric);
CREATE INDEX IF NOT EXISTS idx_alert_rules_enabled ON alert_rules(enabled);
CREATE INDEX IF NOT EXISTS idx_alert_rules_device ON alert_rules(device_id);
CREATE INDEX IF NOT EXISTS idx_alert_rules_group ON alert_rules(group_id);

-- Alert events: firing state machine (firing -> acknowledged -> resolved, auto-resolve when condition clears)
CREATE TABLE IF NOT EXISTS alert_events (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
  rule_id UUID REFERENCES alert_rules(id) ON DELETE SET NULL,
  device_id TEXT REFERENCES devices(id) ON DELETE SET NULL,
  group_id UUID REFERENCES device_groups(id) ON DELETE SET NULL,
  severity TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('critical','warning','info')),
  status TEXT NOT NULL DEFAULT 'firing' CHECK (status IN ('firing','acknowledged','resolved')),
  message TEXT NOT NULL DEFAULT '',
  metric TEXT NOT NULL DEFAULT '',
  value DOUBLE PRECISION,
  threshold DOUBLE PRECISION,
  fired_at TIMESTAMPTZ DEFAULT now(),
  acknowledged_at TIMESTAMPTZ,
  acknowledged_by UUID REFERENCES users(id) ON DELETE SET NULL,
  resolved_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_alert_events_status ON alert_events(status);
CREATE INDEX IF NOT EXISTS idx_alert_events_severity ON alert_events(severity);
CREATE INDEX IF NOT EXISTS idx_alert_events_device ON alert_events(device_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_rule ON alert_events(rule_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_fired ON alert_events(fired_at DESC);
CREATE INDEX IF NOT EXISTS idx_alert_events_tenant ON alert_events(tenant_id);

-- Alert channels: notification sinks (webhook, telegram, email stub, in_app)
CREATE TABLE IF NOT EXISTS alert_channels (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('webhook','telegram','email','in_app')),
  config JSONB NOT NULL DEFAULT '{}'::jsonb,
  enabled BOOLEAN DEFAULT true,
  created_at TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_alert_channels_tenant ON alert_channels(tenant_id);
CREATE INDEX IF NOT EXISTS idx_alert_channels_type ON alert_channels(type);
CREATE UNIQUE INDEX IF NOT EXISTS uq_alert_rules_tenant_name ON alert_rules(tenant_id, name);
CREATE UNIQUE INDEX IF NOT EXISTS uq_alert_channels_tenant_name ON alert_channels(tenant_id, name);

-- Alert delivery log (audit trail per event+channel attempt)
CREATE TABLE IF NOT EXISTS alert_logs (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  event_id UUID REFERENCES alert_events(id) ON DELETE CASCADE,
  channel_id UUID REFERENCES alert_channels(id) ON DELETE SET NULL,
  channel_type TEXT NOT NULL DEFAULT 'in_app',
  payload JSONB DEFAULT '{}'::jsonb,
  status TEXT NOT NULL DEFAULT 'sent' CHECK (status IN ('pending','sent','failed')),
  error TEXT DEFAULT '',
  created_at TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_alert_logs_event ON alert_logs(event_id);
CREATE INDEX IF NOT EXISTS idx_alert_logs_created ON alert_logs(created_at DESC);

-- Backfill menu_permissions to include alerts:true for existing roles (idempotent)
UPDATE roles SET menu_permissions = menu_permissions || '{"alerts": true}'::jsonb
WHERE NOT (menu_permissions ? 'alerts');

-- Seed default channels (in_app + webhook placeholder)
INSERT INTO alert_channels(tenant_id, name, type, config, enabled)
SELECT id, 'In-App', 'in_app', '{"note":"in-app bell + events table"}'::jsonb, true FROM tenants WHERE name='default'
ON CONFLICT (tenant_id, name) DO NOTHING;
INSERT INTO alert_channels(tenant_id, name, type, config, enabled)
SELECT id, 'Webhook (placeholder)', 'webhook', '{"url":""}'::jsonb, false FROM tenants WHERE name='default'
ON CONFLICT (tenant_id, name) DO NOTHING;

-- Seed default rules (enabled, sensible thresholds, global scope)
INSERT INTO alert_rules(tenant_id, name, description, enabled, metric, operator, threshold, severity, cooldown_sec)
SELECT id, 'Device Offline', 'Trigger when ping fails / status offline', true, 'device_status', 'offline', NULL, 'critical', 300 FROM tenants WHERE name='default'
ON CONFLICT (tenant_id, name) DO NOTHING;
INSERT INTO alert_rules(tenant_id, name, description, enabled, metric, operator, threshold, severity, cooldown_sec)
SELECT id, 'High Latency', 'Latency > 200 ms', true, 'latency', '>', 200, 'warning', 600 FROM tenants WHERE name='default'
ON CONFLICT (tenant_id, name) DO NOTHING;
INSERT INTO alert_rules(tenant_id, name, description, enabled, metric, operator, threshold, severity, cooldown_sec)
SELECT id, 'Packet Loss', 'Packet loss > 5 %', true, 'packet_loss', '>', 5, 'warning', 600 FROM tenants WHERE name='default'
ON CONFLICT (tenant_id, name) DO NOTHING;
INSERT INTO alert_rules(tenant_id, name, description, enabled, metric, operator, threshold, severity, cooldown_sec)
SELECT id, 'High CPU', 'CPU > 85 %', true, 'cpu', '>', 85, 'warning', 900 FROM tenants WHERE name='default'
ON CONFLICT (tenant_id, name) DO NOTHING;
INSERT INTO alert_rules(tenant_id, name, description, enabled, metric, operator, threshold, severity, cooldown_sec)
SELECT id, 'High Memory', 'Memory > 90 %', true, 'mem', '>', 90, 'warning', 900 FROM tenants WHERE name='default'
ON CONFLICT (tenant_id, name) DO NOTHING;

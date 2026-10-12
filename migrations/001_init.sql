-- 001_init.sql — NMS v0.4 — fixed (no subquery in DEFAULT)
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "timescaledb" CASCADE;

-- Tenants
CREATE TABLE IF NOT EXISTS tenants (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  name TEXT NOT NULL UNIQUE,
  created_at TIMESTAMPTZ DEFAULT now()
);
INSERT INTO tenants(name) VALUES ('default') ON CONFLICT (name) DO NOTHING;

-- Roles 3 dimensi
CREATE TABLE IF NOT EXISTS roles (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  mikrotik_group TEXT NOT NULL DEFAULT 'read',
  menu_permissions JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ DEFAULT now(),
  UNIQUE(tenant_id, name)
);

-- Users
CREATE TABLE IF NOT EXISTS users (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role_id UUID REFERENCES roles(id) ON DELETE SET NULL,
  must_change_password BOOLEAN DEFAULT true,
  mikrotik_sync_status TEXT DEFAULT 'pending',
  created_at TIMESTAMPTZ DEFAULT now()
);

-- Device groups
CREATE TABLE IF NOT EXISTS device_groups (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  description TEXT DEFAULT '',
  poll_interval INTEGER DEFAULT 30,
  created_at TIMESTAMPTZ DEFAULT now(),
  UNIQUE(tenant_id, name)
);

CREATE TABLE IF NOT EXISTS role_device_groups (
  role_id UUID REFERENCES roles(id) ON DELETE CASCADE,
  group_id UUID REFERENCES device_groups(id) ON DELETE CASCADE,
  PRIMARY KEY(role_id, group_id)
);

-- Racks — dinamis
CREATE TABLE IF NOT EXISTS racks (
  id TEXT PRIMARY KEY,
  tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  location TEXT DEFAULT '',
  x INTEGER DEFAULT 0,
  y INTEGER DEFAULT 0,
  created_at TIMESTAMPTZ DEFAULT now()
);

-- Devices
CREATE TABLE IF NOT EXISTS devices (
  id TEXT PRIMARY KEY,
  tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
  group_id UUID REFERENCES device_groups(id) ON DELETE SET NULL,
  rack_id TEXT REFERENCES racks(id) ON DELETE SET NULL,
  name TEXT NOT NULL,
  ip TEXT NOT NULL,
  type TEXT NOT NULL DEFAULT 'mikrotik',
  poll_interval INTEGER,
  status TEXT DEFAULT 'unknown',
  x INTEGER DEFAULT 100,
  y INTEGER DEFAULT 100,
  snmp_community TEXT DEFAULT 'public',
  snmp_version TEXT DEFAULT '2c',
  created_at TIMESTAMPTZ DEFAULT now()
);

-- Interfaces
CREATE TABLE IF NOT EXISTS interfaces (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  device_id TEXT REFERENCES devices(id) ON DELETE CASCADE,
  if_index INTEGER NOT NULL,
  name TEXT NOT NULL,
  alias TEXT DEFAULT '',
  enabled BOOLEAN DEFAULT true,
  UNIQUE(device_id, if_index)
);

-- Links
CREATE TABLE IF NOT EXISTS links (
  id TEXT PRIMARY KEY,
  tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
  src_id TEXT NOT NULL,
  dst_id TEXT NOT NULL,
  src_type TEXT NOT NULL DEFAULT 'device',
  dst_type TEXT NOT NULL DEFAULT 'device',
  description TEXT DEFAULT '',
  capacity BIGINT DEFAULT 1000000000,
  sensor_device_id TEXT REFERENCES devices(id) ON DELETE SET NULL,
  sensor_if_index INTEGER,
  bps TEXT DEFAULT '',
  off DOUBLE PRECISION DEFAULT 0,
  created_at TIMESTAMPTZ DEFAULT now()
);

-- Link metrics hypertable
CREATE TABLE IF NOT EXISTS link_metrics (
  time TIMESTAMPTZ NOT NULL DEFAULT now(),
  link_id TEXT REFERENCES links(id) ON DELETE CASCADE,
  in_bps BIGINT DEFAULT 0,
  out_bps BIGINT DEFAULT 0
);
SELECT create_hypertable('link_metrics','time', if_not_exists => TRUE, migrate_data => TRUE);

-- Device metrics hypertable
CREATE TABLE IF NOT EXISTS device_metrics (
  time TIMESTAMPTZ NOT NULL DEFAULT now(),
  device_id TEXT REFERENCES devices(id) ON DELETE CASCADE,
  latency_ms DOUBLE PRECISION,
  packet_loss DOUBLE PRECISION,
  cpu DOUBLE PRECISION,
  mem DOUBLE PRECISION,
  uptime BIGINT
);
SELECT create_hypertable('device_metrics','time', if_not_exists => TRUE, migrate_data => TRUE);

-- Auth failures
CREATE TABLE IF NOT EXISTS auth_failures (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  device_id TEXT REFERENCES devices(id) ON DELETE CASCADE,
  username TEXT,
  ip TEXT,
  time TIMESTAMPTZ DEFAULT now()
);

-- Topology layouts
CREATE TABLE IF NOT EXISTS topology_layouts (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID REFERENCES tenants(id) ON DELETE CASCADE,
  user_id UUID REFERENCES users(id) ON DELETE CASCADE,
  scope TEXT NOT NULL DEFAULT 'top',
  layout JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at TIMESTAMPTZ DEFAULT now()
);

-- Seed roles
INSERT INTO roles(tenant_id, name, mikrotik_group, menu_permissions)
SELECT id, 'SuperAdmin','full','{"dashboard":true,"topology":true,"devices":true,"users":true,"settings":true}'::jsonb FROM tenants WHERE name='default'
ON CONFLICT DO NOTHING;
INSERT INTO roles(tenant_id, name, mikrotik_group, menu_permissions)
SELECT id, 'ReadOnly','read','{"dashboard":true,"topology":true,"devices":true}'::jsonb FROM tenants WHERE name='default'
ON CONFLICT DO NOTHING;
INSERT INTO roles(tenant_id, name, mikrotik_group, menu_permissions)
SELECT id, 'Operator','write','{"dashboard":true,"topology":true,"devices":true}'::jsonb FROM tenants WHERE name='default'
ON CONFLICT DO NOTHING;

-- Seed device groups
INSERT INTO device_groups(tenant_id, name, description, poll_interval)
SELECT id, 'Jakarta','Core + Cabang Jakarta',30 FROM tenants WHERE name='default'
ON CONFLICT DO NOTHING;
INSERT INTO device_groups(tenant_id, name, description, poll_interval)
SELECT id, 'Mitra A','Tenant Mitra A',30 FROM tenants WHERE name='default'
ON CONFLICT DO NOTHING;
INSERT INTO device_groups(tenant_id, name, description, poll_interval)
SELECT id, 'Magang','Akses magang terbatas',60 FROM tenants WHERE name='default'
ON CONFLICT DO NOTHING;

-- Seed racks
INSERT INTO racks(id, tenant_id, name, location, x, y)
SELECT 'A', id, 'Rack A','Jakarta DC R1',100,100 FROM tenants WHERE name='default'
ON CONFLICT (id) DO NOTHING;
INSERT INTO racks(id, tenant_id, name, location, x, y)
SELECT 'B', id, 'Rack B','Jakarta DC R2',420,100 FROM tenants WHERE name='default'
ON CONFLICT (id) DO NOTHING;
INSERT INTO racks(id, tenant_id, name, location, x, y)
SELECT 'C', id, 'Rack C','Mitra A',740,100 FROM tenants WHERE name='default'
ON CONFLICT (id) DO NOTHING;

-- Seed devices
INSERT INTO devices(id, tenant_id, name, ip, type, rack_id, group_id, poll_interval, x, y)
SELECT 'RB-01', t.id, 'RB-01 Core','10.0.1.1','mikrotik','A', g.id, 10, 80,80 FROM tenants t, device_groups g WHERE t.name='default' AND g.name='Jakarta'
ON CONFLICT (id) DO NOTHING;
INSERT INTO devices(id, tenant_id, name, ip, type, rack_id, group_id, poll_interval, x, y)
SELECT 'SW-01', t.id, 'SW-01 Dist','10.0.1.2','switch','A', g.id, 30, 280,120 FROM tenants t, device_groups g WHERE t.name='default' AND g.name='Jakarta'
ON CONFLICT (id) DO NOTHING;
INSERT INTO devices(id, tenant_id, name, ip, type, rack_id, group_id, poll_interval, x, y)
SELECT 'SRV-01', t.id, 'SRV-01 NVR','10.0.2.1','server','B', g.id, 60, 80,80 FROM tenants t, device_groups g WHERE t.name='default' AND g.name='Mitra A'
ON CONFLICT (id) DO NOTHING;
INSERT INTO devices(id, tenant_id, name, ip, type, rack_id, group_id, poll_interval, x, y)
SELECT 'CCTV-01', t.id, 'CCTV-01','10.0.3.1','cctv','C', g.id, 300, 80,80 FROM tenants t, device_groups g WHERE t.name='default' AND g.name='Magang'
ON CONFLICT (id) DO NOTHING;

-- Seed links
INSERT INTO links(id, tenant_id, src_id, dst_id, src_type, dst_type, description, capacity, sensor_device_id, sensor_if_index, bps, off)
SELECT 'L-RACK-A-B', t.id, 'A','B','rack','rack','Rack A → Rack B — 2x10G fiber',10000000000,'RB-01',2,'8.2 Gbps',0.0 FROM tenants t WHERE t.name='default'
ON CONFLICT (id) DO NOTHING;
INSERT INTO links(id, tenant_id, src_id, dst_id, src_type, dst_type, description, capacity, sensor_device_id, sensor_if_index, bps, off)
SELECT 'L-RACK-A-B-2', t.id, 'A','B','rack','rack','Rack A → Rack B — backup 1G',1000000000,'SW-01',1,'120 Mbps',0.15 FROM tenants t WHERE t.name='default'
ON CONFLICT (id) DO NOTHING;
INSERT INTO links(id, tenant_id, src_id, dst_id, src_type, dst_type, description, capacity, sensor_device_id, sensor_if_index, bps, off)
SELECT 'L-RACK-A-C', t.id, 'A','C','rack','rack','Rack A → Rack C — VLAN 10 uplink',1000000000,NULL,NULL,'540 Mbps',0.0 FROM tenants t WHERE t.name='default'
ON CONFLICT (id) DO NOTHING;
INSERT INTO links(id, tenant_id, src_id, dst_id, src_type, dst_type, description, capacity, sensor_device_id, sensor_if_index, bps, off)
SELECT 'L-RB-SW', t.id, 'RB-01','SW-01','device','device','RB↔SW — trunk',1000000000,'RB-01',1,'1.2 Gbps',0.0 FROM tenants t WHERE t.name='default'
ON CONFLICT (id) DO NOTHING;
INSERT INTO links(id, tenant_id, src_id, dst_id, src_type, dst_type, description, capacity, sensor_device_id, sensor_if_index, bps, off)
SELECT 'L-RB-SW-2', t.id, 'RB-01','SW-01','device','device','RB↔SW — backup',1000000000,'SW-01',2,'340 Mbps',0.15 FROM tenants t WHERE t.name='default'
ON CONFLICT (id) DO NOTHING;

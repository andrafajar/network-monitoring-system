# Changelog

## v0.5.0 — 2026-10-12
- RBAC 3 dimensi enforcement: `roles.menu_permissions` + `role_device_groups` — `requireMenu` di semua endpoint CRUD (dashboard/topology/devices/users/settings), filter `devices` & `device-groups` by allowedGroupIDs, SuperAdmin bypass
- Poller interval custom per-device > per-group > global (1s ticker, `effectiveInterval`), sudah verified 10s/30s/60s/300s
- Poller SNMP: walk ifDescr/ifInOctets/ifOutOctets via gosnmp, upsert `interfaces`, hitung bps delta 32/64-bit wrap, publish `link:*` + `device:*` ke Redis, Timescale link_metrics/device_metrics
- Bastion routes: SSH/Telnet/MikroTik API/WebFig/Web Console proxy (bastion server-side, kredensial tidak ke browser) — `GET /api/bastion/*` require menu devices
- Frontend Devices Manager: `devices.html` CRUD Groups/Racks/Devices/Links (fetch Bearer, filter group/rack, sensor attach)
- Dashboard Device→Interface picker real: Traffic card → `/api/devices` + `/api/interfaces?device_id` + `/api/metrics/device/{id}` live chart, MutationObserver re-wire
- Topology sensor picker real: `/api/devices` + `/api/interfaces` → sensorPick/mSensor, live bps poll `/api/links` tiap 8s + offset paralel `off*28px`
- Nav sync: dashboard/topology/devices link silang, versi v0.5, dedup script, `wireTrafficPickers` + `wireTopoSensors`
- Fix API: `handleListDevices` require dashboard, `handleListRacks/Topology/Links/Interfaces/Metrics` guards, `menuRaw sql.NullString` fix


## v0.4.0 — 2026-10-12
- Backend Go API: JWT auth, setup-status banner (superadmin/Admin@123), device-groups/racks/devices/links/interfaces CRUD, topology API (top/rack), users/roles 3 dimensi, metrics
- Poller Go: ping ICMP (go-ping) + SNMP-ready, metrics to TimescaleDB, link bps live + Redis pub, 5s ticker
- Docker Compose production: db TimescaleDB + Redis + API (migrations mount) + poller + frontend nginx proxy /api → api:8080
- Frontend wiring: index.html login -> /api/auth/login|change-password (token Bearer), dashboard/topology fetch /api/devices|racks|topology with fallback preview
- UI: sidebar logo minimalis putih 36px (match minimized 32px), Group & Rack panel redesign (progress bar, badge, hover), topology map pan/drag (geser area kosong, drag node, wheel zoom, persist nms_topo)
- Migrations 001_init (tenants/roles/users/device_groups/racks/devices/interfaces/links Timescale hypertables) + 002 seed superadmin

## v0.2.0 — 2026-10-11
- Spec v0.2: multi-device management + custom dashboard (device→interface picker) + polling interval custom + AAA User Manager (custom role 3 dimensi) + split-screen remote (SSH/Telnet/MikroTik API/WebFig) + tenant grouping
- First-setup superadmin banner: login page tampilkan `superadmin / Admin@123` jika `must_change_password=1` (GET /api/setup-status), hilang permanen setelah ganti password — sesuai request
- Skeleton frontend Login.jsx dengan first-setup banner (fetch backend + fallback localStorage preview)
- Docker Compose VPS Ubuntu 24.04 ready

## v0.1.0 — 2026-10-11
- Init repo, SSH GitHub connected (andrafajar), git config, SSH key ed25519
- Skeleton docker-compose (db/timescale, redis, api, poller, frontend)
- Workflow Laptop → VPS → GitHub disiapkan, versioning via git tag
- Spec: multi-device management + custom dashboard + AAA User Manager

# Network Monitoring System — Spec v0.2
Status: draft — VPS Ubuntu 24.04 container
Repo: git@github.com:andrafajar/network-monitoring-system.git — workflow Laptop → VPS → GitHub

## 1. Tujuan
Multi-Device Management & Monitoring System — 1 panel untuk monitoring + remote multi-device, multi-user, multi-tenant (sewa/magang), AAA via MikroTik User Manager (RADIUS).

## 2. Deploy
- VPS Ubuntu 24.04 container, Docker Compose
- GitHub private: push dari laptop hanya kalau OK di VPS (`git tag v0.x.y` + `git push origin main --tags`)
- Versioning: `VERSION` + `CHANGELOG.md`

## 3. Stack
- Poller: Go (gosnmp + pro-bing) — fan-out goroutine + Redis Stream sharding, 1000 device @30s ~200MB RAM
- API: Go (or Fastify Node) — REST + WebSocket (bastion proxy SSH/Telnet/MikroTik API)
- DB: PostgreSQL + TimescaleDB (metrics), Redis (queue, session, poller sharding)
- Frontend: React/Vite + Tailwind + xterm.js (terminal) + ECharts — dashboard builder custom
- Alert: Telegram / WhatsApp (Baileys) / Email (SMTP)

## 4. Fitur Utama
### 4.1 Monitoring
- Ping ICMP (primary online/offline), interval custom (global 30s, override per-group/per-device, ex: core 10s, cabang 60s)
- SNMP v2c/v3: ifIn/OutOctets→bps, errors, CPU/mem/temp/uptime; auto-discover interfaces via walk
- HTTP check opsional, syslog → auth_failures (failed login) → widget counter
- Metrics timeseries ke TimescaleDB, retensi 7d/30d configurable

### 4.2 Device & Grouping
- `device_groups(tenant_id, name)` — ex: Jakarta, Mitra A, Magang; `devices(group_id, ip, type, poll_interval, credentials_ref)`
- `interfaces(device_id, ifIndex, name)` — user pilih mana yang monitor/tampil di dashboard
- Import bulk CSV/API

### 4.3 Dashboard Custom (Builder)
- Picker: Device → Interface multi-select (1 device banyak interface, user pilih ether1,ether2 dst)
- Widget: Online/Offline by ping, Failed Login (24h), Traffic graph per-interface (1h/24h/7d), CPU/Mem, Tabel last seen
- Layout drag-drop 12-kolom, save per user (`dashboard_layouts` JSON), filter by group/tag

### 4.4 Remote Multi-Device
- Bastion: frontend WebSocket → backend → SSH/Telnet/API ke device (credential tidak ke browser), split-screen 2x2
- Generic: SSH/Telnet via xterm.js
- MikroTik: API REST/8728 → UI Winbox-like (Interfaces/Firewall/DHCP/Queue/Routes), Terminal (xterm via /terminal), WebFig proxy + Web Console popup tab baru (proxy http://router-ip via backend, tab per device)

### 4.5 Auth & AAA User Manager — Custom Role 3 Dimensi
- Role custom, bukan hardcode: (A) Menu app yang boleh dibuka, (B) Device Group yang boleh diakses, (C) Mikrotik-Group (read/write/full/custom) yang sync ke User Manager
- Schema: `roles(tenant_id, name, mikrotik_group, menu_permissions JSONB)`, `role_device_groups`, `users(tenant_id, username, password_hash bcrypt, role_id, mikrotik_sync_status)`
- 1 User = 1 Role → clean map ke 1 Mikrotik-Group; jika multi-role ambil tertinggi
- Sync: buat user di app → `POST /rest/user-manager/user` ke router pusat User Manager via API TLS; semua router anggota `/radius add service=login + /user aaa set use-radius=yes` → RADIUS `Mikrotik-Group`
- App jadi Source of Truth: ganti password/role di app → auto PATCH User Manager; fallback auth DB lokal jika UM down
- Enforce ganda: Menu check di app + privilege MikroTik; App block meski MikroTik full jika menu tidak allowed
- Audit log semua remote session

### 4.6 Alert
- Rule: ping down >2 interval, loss>20%, interface down, failed login >5/10m
- Sender queue → Telegram bot, WA, Email harian

### 4.7 First Setup — Default Superadmin di Login Page (BARU v0.2)
- First-time (belum ganti password superadmin) → **banner di login page menampilkan kredensial default** agar admin tahu, auto-hilang setelah diganti
- Default: `superadmin / Admin@123` (configurable via env `DEFAULT_SUPERADMIN_USER` / `DEFAULT_SUPERADMIN_PASS`), seed sekali saat migrasi, simpan `bcrypt` + flag `must_change_password=1`
- Backend: `GET /api/setup-status` → `{firstSetup:true, defaultUser, defaultPass}` jika `must_change_password=1`; setelah `POST /api/change-password` sukses → `firstSetup=false`, banner hilang permanen
- Frontend: fetch status saat load login; jika `firstSetup` tampilkan `.first-setup-banner` kuning mencolok: "Login awal — Username: superadmin Password: Admin@123 — Segera ganti setelah login pertama"
- Tidak di-commit ke Git, tidak tampil lagi setelah diganti; superadmin tetap hidden dari list user biasa, log terpisah
- Preview lokal: `index.html` login juga baca `localStorage nms_setup_done` fallback untuk mode tanpa backend

## 5. Data Model (ringkas)
- devices, device_groups, interfaces, metrics(time, device_id, ifIndex, in_bps, out_bps, ping_ms, loss), auth_failures(time, device_id, username, ip_source)
- roles, users, dashboard_layouts

## 6. MVP Phases
- P1: Poller ping+SNMP interval custom, device+group CRUD, dashboard builder v1, SSH terminal, RBAC + first-setup banner, alert Telegram
- P2: MikroTik API UI + WebFig proxy + terminal MT + failed-login widget
- P3: Tenant billing, audit log, bulk import, Web Console popup split-screen polish

## 7. Workflow
1. Develop di laptop (`C:/Users/andra/Desktop/network-monitoring-system`)
2. Deploy ke VPS via ssh + docker compose up -d → cek http://IP_VPS:3000
3. OK → `echo 0.2.0 > VERSION`, update CHANGELOG.md, `git tag v0.2.0 -m "..."` + `git push origin main --tags`

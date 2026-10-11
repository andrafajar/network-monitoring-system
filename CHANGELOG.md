# Changelog

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

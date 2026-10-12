# Changelog

## v0.7.1 — 2026-10-13
- **Fix dashboard traffic CPU loop:** `MutationObserver` di `dashboard.html` sebelumnya memicu `wireTrafficPickers()` setiap mutasi `grid` (childList+subtree) tanpa debounce sehingga setiap `innerHTML` rewrite → mutasi baru → fetch `/api/devices` + `/api/interfaces` → rewrite lagi = tight loop; `chart.update()` juga dipanggil tiap perubahan picker. Perbaikan: (1) guard `wireTrafficPickers._running` + `try/finally` anti re-entrance, (2) ganti observer langsung ke `scheduleWireTraffic(350)` debounce via `_wireTimer`, (3) `tryObs` tunggu `#grid` ada sebelum observe, (4) cache `selDev.dataset._lastHTML` agar `innerHTML` device selector hanya ditulis jika HTML benar-benar berubah, mengurangi thrash DOM. `node --check` OK, `refreshKPI` tetap 10s, `tick` 1s tidak tersentuh.


## v0.7.0 — 2026-10-13
- **Sidebar minimal + floating toggle:** ganti emoji → SVG stroke 1.7 minimalis (18px) untuk semua menu; sidebar `position:fixed` + `transform:translateX(-100%)` saat minimize (beneran hilang seperti versi HP), tombol floating `#sbToggle` ☰/✕ di pojok kiri atas (14px saat minimize, 274px saat expanded desktop, tetap 14px di mobile ≤1000px), overlay `.sidebar-overlay.show`; `#sideToggle`/`#topMinBtn` di-hide; persist `nms_sidebar_mini`/`nms_sidebar_theme`/`nms_dark`
- **Devices → submenu expand/collapse:** `Devices` jadi parent `.has-sub` dengan chevron rotate, submenu `Device List / Racks / Groups / Links` (`devices.html#devices|#racks|#groups|#links`), toggle `toggleDevSub(event)` persist `nms_dev_sub`, auto-expand saat klik dari minimized, sinkron dengan hash & `localStorage nms_dev_view`
- **Devices page pecah 4 view + tabs:** tab bar `.tabs` sticky (`Device List | Racks | Groups | Links`), 4 container `#view-devices|#view-racks|#view-groups|#view-links`, router `switchView(name)` + `syncViewFromHash()` + `hashchange` listener, judul topbar dinamis, validasi `node --check` 4 halaman OK
- **Groups: kelola anggota via checklist panel (sesuai request):** layout 2 kolom `#groupsLayout` (320px + 1fr, collapse ke 1 kolom ≤900px); kiri: list groups dengan `groupSearch` + badge `N devices` + select click; kanan: panel `groupDevChecklist` dengan `groupDevSearch` + `Pilih semua / Kosongkan` + `Simpan Anggota` batch `PUT /api/devices/{id}` (diff group_id), hint `Belum disimpan`/`Tersimpan`, `selectedGroupId` state, auto-refresh ke `renderGroupsList/renderDevices`
- **Device List & Racks search:** `devSearch` (name/IP/ID), `rackSearch` (name/ID/lokasi), `groupSearch`, filter `filterGroup/filterRack` tetap, `devCount` sinkron, escaping via `esc()`
- **4 file sinkron:** `dashboard.html / devices.html / topology.html / users-roles.html` — sidebar HTML, CSS minimal, JS IIFE `#sbToggle` konsisten; `devices.html` len 73174, `dashboard` 62934, `topology` 63467, `users-roles` 42568; `div` balance -2 konsisten (pre-existing)

## v0.6.0 — 2026-10-13
- Frontend Terminal bastion split-screen: `xterm.js@5.3.0` + FitAddon 2-pane (`cols1/cols2`) popup `#termModal` + WebFig iframe `#webfigModal` konsisten di `devices.html` & `topology.html` (WS `/api/bastion/ssh|telnet|mikrotik-api`, creds server-side, `termWSUrl` token query, `createTermPane`/`termClosePane`/`termReconnect`/`termSplitToggle`, `openTerminal`/`openWebFig`/`openWebConsole` aliases)
- Devices: `renderDevices` action SSH/Telnet/API/WebFig per-row; Topology: `showSelection` + `dblclick` → SSH/WebFig via `openTermTopology`/`openWebFigTopology` (mock→real dev map), `#webfigFrame` onerror fallback ke `/api/bastion/webconsole`
- Users & Roles Manager `users-roles.html`: CRUD `roles` (name, mikrotik_group read/write/full, menu_permissions JSON, group_ids via `role_device_groups`) + CRUD `users` (username, password bcrypt, role 1:1), modal `+ Role`/`+ User`, `PUT/DELETE /api/users/{id}` & `/api/roles/{id}`, `GET /api/roles/{id}/groups` & `GET /api/role-groups`, RADIUS sync stub log di `handleUpdateUser/handleCreateUser`
- API: `handleUpdateRole/handleDeleteRole/handleUpdateUser/handleDeleteUser/handleGetRoleGroups/handleListRoleGroupsDetail`, `handleListRoles` `COALESCE(json_agg...)`, `gorilla/websocket` dep, bastion handlers server-side WS proxy, `requireMenu` 3-dimensi (menu+device_group+mikrotik_group)
- Poller: HC 64-bit counters `.1.3.6.1.2.1.31.1.1.1.6/.10` prefer, `fetchCpuMem` hrProcessorLoad avg, 32/64-bit wrap `max32/isHC`, `!didSNMP && online` pseudo-bps fix
- Nav sync 4 pages (dashboard/devices/topology/users-roles): `class="on" href` konsisten, theme/dark/minimize `localStorage nms_sidebar_theme/nms_dark/nms_sidebar_mini` + single `#sideToggle` ‹/› + `#sbOverlay` mobile, dashboard head duplicate `backendSync` inside `<script src>` dibersihkan, `hdrSync` rename di `topoSync` IIFE
- Migrations: `003_device_creds.sql` (ssh_user/pass/port, api_user/pass, telnet_port, note per-device bastion)


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

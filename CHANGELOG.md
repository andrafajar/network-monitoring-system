## [0.9.3] - 2026-10-13
### Fixed
- Sub-menu sidebar `Devices > Rack / Group / Links` sekarang bisa diklik langsung dari side menu tanpa harus masuk `Device List` dulu — `devices.html#devSubmenu` diganti `href="#racks|#groups|#links"` + `onclick="event.preventDefault();switchView('xxx')"` (in-page hash router). Halaman lain (`dashboard/topology/users-roles/alerts`) tetap `href="devices.html#racks"` untuk deep-link cross-page, `switchView` update `history.replaceState` + `hashchange` + `localStorage nms_dev_view` persist, `toggleDevSub` auto-expand saat sidebar minimized.
- Rapikan panel `Device List` & `Links`: atribut/parameter di modal `+ Device / Edit` dikelompokkan dengan header seksi `Identitas / Penempatan / Polling & SNMP / Kredensial Remote / Catatan` (CSS `.sec-head` uppercase, border-top), urutan SSH `user → pass → port` (sebelumnya `user → port → pass` acak), `X/Y` dipindah ke `Penempatan`. Modal `+ Link` juga dikelompokkan `Endpoint / Detail Link / Sensor (opsional)`. `view-links` header disamakan dengan `view-devices`: tambah search `🔍 linkSearch` + pill `linkCount` + refresh `↻` + `border-bottom` + `padding-bottom:12px`, pill count live update `a.length + ' links'`.
- Links sekarang punya state `LINKS[]` + `LINKS_FILTER_Q` + `renderLinks()` filter `id/src_id/dst_id/description/src_type/dst_type` via `linkSearch`, empty state bedakan `Belum ada link` vs `Tidak ada hasil untuk "..."`, escape `esc()` untuk semua kolom, `Number(capacity)` fallback, `thead` konsisten 8 kolom. `loadLinks()` kini `LINKS=await jget('/api/links'); renderLinks();` bukan innerHTML langsung.
- CSS `.form-grid .sec-head` tambah `grid-column:1 / -1`, `font-size:.68rem`, `letter-spacing:.05em`, `text-transform:uppercase`, `border-top:1px solid var(--line)`.

## [0.9.2] - 2026-10-13
### Fixed
- Poller `doPing` refaktor jadi `privileged(true)→false` retry + `execPing` (`ping -c2 -W2` parse `rtt/round-trip min/avg/max` + `0% packet loss` hint) + `tcpReach` per-port timing (22/80/443/161/8291/8728, 1200ms each) — sebelumnya hanya `SetPrivileged(false)` single shot sehingga selalu jatuh ke TCP 1200ms untuk 1.1.1.1/8.8.8.8, sekarang ICMP real 0.5ms via NET_RAW + `busybox ping` fallback.
- API `handleTestDeviceConnection` loop `for priv in [true,false]` + `execPingViaBinary` + `tcpReachFallback` (6 port), hilangkan single privileged-false path yang bikin `Test koneksi` return 2ms go-ping vs 1200ms poller.
- Tambah import `os/exec` di `app/api/go.mod` already + `app/poller/main.go` (tidy), `strconv` sudah ada, brace `poller 0 / api 1` (expected 1 dari template).
- `docker-compose.yml` `cap_add NET_RAW` tetap untuk `api`+`poller` agar `ping.NewPinger(privileged:true)` diizinkan.

## [0.9.1] - 2026-10-13
### Added
- Device CRUD hardening: modal `+ Device / Edit` diperluas (SNMP version 2c/1/3, SSH user/pass/port 22, API user/pass, Telnet port 23, Note) + `handleGetDevice` kini return `ssh_pass/api_pass` untuk prefill edit, `handleCreateDevice`/`handleUpdateDevice` sudah handle semua cred fields (`snmp_version`, `ssh_*`, `api_*`, `telnet_port`, `note`), `handleUpdateDevice` `poll_interval` null-safe (clear to global). Frontend `openDeviceModal` reset 14 fields, `editDevice` fetch `GET /api/devices/{id}` untuk cred lengkap dengan fallback `DEVICES`, `saveDevice` kirim 17 fields lengkap (`snmp_version`, `ssh_*`, `api_*`, `telnet_port`, `note`) + `poll_interval` null jika kosong.

### Added — API
- Endpoint `POST /api/devices/test-connection` + `POST /api/devices/{id}/test` — `handleTestDeviceConnection` (menu `devices`) : terima `ip` + optional `snmp_*`/`ssh_*`/`api_*`/`telnet_*`, fallback ke DB jika `id` ada, jalankan **ping** (go-ping unprivileged + TCP fallback `execPingFallback` 22/80), **SNMP** (gosnmp walk `ifDescr` + `sysDescr`, `BulkWalk 1.3.6.1.2.1.2.2.1.2`, `snmp_version` 2c/1, 3 = not supported), **SSH dial** (golang.org/x/crypto/ssh Dial 5s), return `{ip, overall, ping{ok,latency_ms,loss,error}, snmp{ok,interfaces,sysDescr,error}, ssh{ok,error}}`. Modal tombol `Test koneksi` (`testDeviceConnection()`) call endpoint dengan/without `id`, render result `Overall / Ping / SNMP / SSH` di `#devTestRes` dengan border hijau/kuning/merah.
- Deps `app/api/go.mod` tambah `github.com/go-ping/ping v1.2.0` + `github.com/gosnmp/gosnmp v1.42.1`.

### Changed
- Poller `doPing` sekarang fallback TCP (`tcpReach` dial 22/80/443/161/8291/8728, latency dari durasi dial) jika `ping.Run` permission/sock error atau `PacketsRecv==0` (firewall drop ICMP) → `online` dengan `loss 0`, sebelumnya hanya `offline`.
- Poller import sort + tambah `net` (sebelumnya hanya `net/http`).
- `docker-compose.yml` `api` + `poller` tambah `cap_add: [NET_RAW]` agar raw ICMP socket diizinkan (tanpa privileged).

### Fixed
- Frontend `devices.html` `renderDevices` sekarang filter `devSearch` (q di `id/name/ip/type`), sebelumnya search input ada tapi tidak dipakai.
- Frontend injected `.form-grid{display:grid;grid-template-columns:1fr 1fr;gap:10px; label flex col}` + responsive 1col ≤640px (sebelumnya style `.form-grid` hilang).
- API `execPingFallback` mark `ctx` unused (`_ = ctx`) untuk `go vet` clean, hapus var `cmd` leftover.
- Poller & API build `node --check` OK, `div` 110/110, `cap_add` ×2 verified.


## [0.9.0] - 2026-10-13
### Added
- Alerts & Notifications P1: DB schema `004_alerts.sql` — `alert_rules` (metric/operator/threshold/severity, scope global/group/device, cooldown, notify_channel_ids), `alert_events` (state machine firing → acknowledged → resolved + auto-resolve), `alert_channels` (webhook/telegram/email/in_app), `alert_logs` (audit delivery). Unique index `uq_alert_*` idempotent. Seed 5 rules (Device Offline critical offline, High Latency latency>200ms warning, Packet Loss >5% warning, High CPU >85% warning, High Memory >90% warning) + 2 channels (In-App enabled, Webhook placeholder disabled) + backfill `roles.menu_permissions alerts:true`.
- Poller alert engine: `evaluateAlerts()` every tick — loads enabled rules, filters by device/group scope, eval numeric `evalNumeric()` + status `offline/online`, checks existing firing/acknowledged to respect cooldown, INSERT firing event + Redis pub `alerts` + per-`alert:{device}` + channel log + async `deliverWebhookChannels()` POST; else auto-resolves when condition clears + pub resolved. `Device.GroupID` added, `tick()` query now selects `d.group_id::text`, imports `bytes/database/sql/encoding/json/net/http/strings`. Panic-guarded `tryEvaluateAlerts()`.

### Added — API
- Routes: `GET/POST /api/alert-rules`, `PUT/DELETE /api/alert-rules/:id`, `GET /api/alert-events` + alias `GET /api/alerts` (filters status/severity/device_id/limit), `POST /api/alert-events/:id/ack` + alias `POST /api/alerts/:id/ack`, `POST /api/alert-events/:id/resolve` + alias, `GET/POST /api/alert-channels`, `PUT/DELETE /api/alert-channels/:id`, `GET /api/alerts/summary` (counts firing/acknowledged/resolved/critical/warning). Menu guard `alerts` (events/summary also allow `dashboard` fallback). Imports patched `strconv` + `database/sql` for NullTime/NullFloat.

### Added — Frontend
- Page `app/frontend/alerts.html` (58331 chars, 718 lines) — canonical sidebar sync (navy/dark/sbToggle 14px/274px, overlay, devSub), bell badge `#alertBell` + top firing badge, 4 summary cards (Firing/Acknowledged/Resolved/Critical-Warning), tabs Events/Rules/Channels (`#events/#rules/#channels` + localStorage `nms_alert_view`), Events filter status/severity/device + search debounced + ack/resolve actions, Rules table (condition/scope/enabled/cooldown) + modal (metric/operator/threshold dynamic sync, group/device scope, cooldown, notify channels checklist), Channels table + modal (type webhook/telegram/email/in_app, config JSON, enabled). Poll 15s summary+events, `node --check OK`.

### Changed
- Sidebar nav in `dashboard/devices/topology/users-roles` — placeholder `alert('Alerts — P1 next')` → real `<a href="alerts.html">Alerts</a>` (non-on; Alerts page marks `class="on"`). Verified `href="alerts.html"` ×1 per file, `div` balanced, `sbToggle` 15 refs intact.


## [0.8.0] - 2026-10-13
### Changed
- Device-list (view-devices) rapikan: header 2-baris → 1 baris rapi dengan border-bottom, pill `devCount`, search dengan icon 🔍 (`padding-left 32px`), select `filterGroup`/`filterRack` sizing konsisten, tombol refresh & +Device aligned.
### Improved
- Actions per-row: 4 tombol remote (SSH/Tel/WebFig) + Edit/Hapus disatukan `inline-flex nowrap` dengan style `.act-btn` minimalis (border tipis, hover biru) + divider — tetap 4 tombol sesuai request, tidak lagi menumpuk `flex-wrap` berantakan.
- Table overflow tetap `auto`, row actions `white-space:nowrap` agar tidak meluber di layar kecil.

## [0.7.4] - 2026-10-13
### Fixed
- Topbar left-padding adjusted (`padding-left: 56px`) across all pages to reserve clear space for `#sbToggle` floating button, preventing it from overlapping page titles / tabs.

## [0.7.3] - 2026-10-13
### Fixed
- Sidebar hide button (`#sbToggle`): positioning logic mobile (≤1000px) vs desktop, smooth transition `14px` when minimized / mobile, `274px` when desktop expanded.
- Cleaned up obsolete `#topMinBtn` topbar duplication and stale media queries across all 4 frontend pages.

# Changelog

## v0.7.2 — 2026-10-12
- **Fix side panel tumpang tindih & content geser:** orphan block duplikat (`<button id="sideToggle">` + `<nav class="menu">` emoji + `<div class="theme-panel">` duplikat) tertinggal di 4 file setelah migrasi ke sidebar minimal floating — menyebabkan 2× `nav` + 2× `theme-panel`, DOM body `sidebar → sideToggle → nav(orphan) → theme(orphan) → overlay → sbToggle → main` sehingga `.sidebar.minimized ~ .main` dan `.sidebar:not(.minimized) ~ #sbToggle` (sibling `~`) salah hit, content `main` kegeser jauh (`mainRect.x 851` bukan `260`, `margin-left:260px` dobel-hit) dan panel bawah masih kelihatan di belakang. Perbaikan: hapus exakt segmen `sideToggle..orphan theme` (1686 char) di `dashboard/devices/topology/users-roles`, `div` balance `108/108` (was `-2`), `nav` 2→1, `theme` 2→1, `sideToggle` 1→0, `sbToggle`/`sbOverlay` tetap 1, `node --check` OK, `margin-left:260px` + `transform:translateX(-100%)` + overlay sibling kembali akurat.


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

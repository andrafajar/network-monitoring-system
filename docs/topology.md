# Topology Builder — Spec Detail (v0.3 draft)

## Konsep
- **Node** = device (router, switch, server) — punya `group_id`, `rack_id` (nullable), `x,y` di layout
- **Rack** = container node — adalah device group fisik (Rack A, Rack B) atau `rack` table sendiri. Di layer atas, rack tampil sebagai 1 node besar. Klik rack → drill-down ke layer isi rack tersebut.
- **Link** = kabel/edge antar device (atau antar rack di layer atas)

## Layer & Navigasi
- **Top Layer (Rack view):** tampilkan semua `racks` sebagai node + link `rack-to-rack`. Setiap link rack-to-rack punya deskripsi (mis: "Rack A → Rack B — 2x10G fiber") dan bisa attach ke sensor agregat (opsional).
- **Rack Layer:** klik Rack A → masuk view isi Rack A (devices di `rack_id=A` + link `device-to-device`). Breadcrumb: `Top > Rack A` dengan tombol Back/Collapse.
- **Collapse/Expand** — sama kayak folder, bisa expand inline atau drill-down penuh. State simpan per user.
- Navigasi tidak reload: frontend router push `?layer=rackA`, WebSocket tetap subscribe metrics layer aktif.

## Link — Sensor Binding & Traffic
- Tiap link bisa di-attach ke **1 SNMP sensor/interface**:
  - Picker: `Device → Interface` (mis: MikroTik ether1, ifIndex 2, OID ifInOctets.2). Poller sudah walk `interfaces` jadi dropdown ready.
  - Binding: `link.sensor = {device_id, ifIndex, oid}` → poller hitung `bps` dari delta octet, tulis ke `link_metrics(time, link_id, in_bps, out_bps)`
  - Label link live: `1.2 Gbps / 800 Mbps` + warna threshold (hijau <50%, kuning 50-80%, merah >80% dari `link.capacity`)
- Link juga punya `description` free text (mis: "VLAN 10 — uplink core"), `capacity` (bps), `status` (up/down dari carrier sensor atau ping).

## Multi-Link Paralel (2 kabel device sama)
- Jika ada 2+ link dengan `src==dst` sama (A↔B dua kabel), render **sejajar berjarak**, tidak tumpang tindih.
- Implementasi: hitung `parallelIndex` untuk group link yang sama → offset kurva Bezier `offset = (index - (n-1)/2) * 28px`. Library: React Flow `getBezierPath` dengan `offset`, atau AntV G6 `quadratic`.
- Label tiap link tetap terpisah, hover highlight link terkait.

## Data Model
```sql
racks(id, tenant_id, name, location, x, y)
devices(id, rack_id nullable, group_id, ip, type, poll_interval)
topology_layouts(id, tenant_id, scope ENUM('top','rack'), scope_id nullable, layout JSONB) -- {nodes:[{id,x,y}], zoom}
links(id, tenant_id, src_device_id, dst_device_id, src_rack_id nullable, dst_rack_id nullable,
      sensor_device_id nullable, sensor_ifIndex nullable,
      description text, capacity bigint, color text, parallel_group text)
link_metrics(time, link_id, in_bps, out_bps) -- hypertable
```

## Rendering & Library
- Pilihan: **React Flow** (paling cocok drill-down + custom node rack besar + minimap) atau **AntV G6** (auto parallel layout). Rekomendasi React Flow + `elk` untuk auto layout awal.
- Node rack = custom node dengan header + badge jumlah device + status agregat (online/total).
- Edge = `Edge` dengan label `description + live bps`, animasi dash jika traffic >0.

## Interaksi User
- Drag node untuk atur posisi → auto-save ke `topology_layouts` (debounce 1s)
- Add link: drag handle dari node A ke B → modal pilih sensor + deskripsi
- Edit link: klik label → modal ganti sensor/deskripsi/capacity
- Right-click device → Remote (SSH/WebFig) langsung dari topologi
- Permission: lihat link hanya jika user punya akses ke `device_group` dari kedua ujung; rack visible jika ada minimal 1 device allowed.

## Realtime
- Poller publish ke Redis channel `link:{id}` tiap interval sensor → API WebSocket broadcast ke frontend layer aktif → edge label & warna update tanpa refresh.

## Halaman Utama (Landing Dashboard)
- Referensi: https://share.google/uVhmgbWf9dx5svGGk (Jaida Personal Dashboard — card Rounded, KPI di atas, soft shadow)
- Dipadukan dengan CRM ref sebelumnya (pin.it/6eKpOzaAl): KPI bar (Online/Offline/Failed Login), chart pipeline → traffic per interface, tabel device, quick action remote.

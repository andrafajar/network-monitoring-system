# Network Monitoring System — Spec Awal
Status: draft — siap diisi di sesi baru
Tujuan: monitor jaringan/server/perangkat, alert real-time
Scope kandidat:
- Ping/HTTP uptime (ICMP + HTTP check interval)
- Bandwidth / SNMP (MikroTik, switch)
- Server (CPU/RAM/disk, systemd/docker)
- Alert: Telegram / WhatsApp / Email
- Dashboard: Grafana / web ringan custom
- Log & history, retention
Stack opsi:
- Ringan VPS: Prometheus + Grafana + Alertmanager + Blackbox Exporter
- Tanpa Docker: Node/Python + SQLite + cron ping + web dashboard
- cPanel-friendly: PHP + MySQL + cron + Chart.js

# Network Monitoring System — Multi-Device Management & Monitoring

> 1 panel untuk monitoring + remote (SSH/Telnet/MikroTik API/WebFig) + multi-user + AAA User Manager + grouping tenant.
> Deploy: VPS Ubuntu 24.04 container (Docker Compose)

## Stack
- **Poller:** Go (gosnmp + pro-bing) — ringan, scale 1000+ device @30s ~200MB RAM
- **API:** Go/Fastify — REST + WebSocket (remote console proxy)
- **DB:** PostgreSQL + TimescaleDB + Redis
- **Frontend:** React/Vite + Tailwind + xterm.js + ECharts — dashboard builder custom
- **Auth:** App RBAC custom (menu + device group) + sync Mikrotik-Group (read/write/full) ke User Manager via REST API

## Quick Start (Lokal)
```bash
docker compose up -d
# API: http://localhost:8080 | Frontend: http://localhost:3000
```

## Quick Start (VPS)
```bash
ssh user@IP_VPS
git clone git@github.com:andrafajar/network-monitoring-system.git
cd network-monitoring-system
docker compose up -d
# buka http://IP_VPS:3000
```

## Versioning
- `CHANGELOG.md` + git tag `v0.x.y`
- Setiap update ada note versi

## Workflow (Laptop ↔ VPS ↔ GitHub)
1. Develop di laptop (`C:/Users/andra/Desktop/network-monitoring-system`)
2. Test deploy ke VPS via `ssh` + `docker compose` — cek live di `http://IP_VPS:3000`
3. Jika OK → `git tag v0.x.y` + push ke GitHub (`git push origin main --tags`)

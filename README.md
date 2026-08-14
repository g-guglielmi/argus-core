# Argus — Monitoring Cockpit over Zabbix

A self-hosted, **PRTG-style monitoring web app** layered on [Zabbix 7.0](https://www.zabbix.com/).
Zabbix handles collection, transport, and buffering; Argus owns the **UI, authentication,
and notifications** — a single distroless container (Go backend + embedded React SPA,
SQLite state, no external dependencies beyond a running Zabbix).

**What it does today (v0.2.8):**

- **Monitoring tree** — site → host → sensor, grouped by Zabbix host group, with curated
  "key sensor" views, live values, inline sparklines, and per-sensor uPlot charts
  (2h/2d/1M/3M/6M/1Y with drag-to-zoom).
- **States** — acknowledge (with expiry + undo), pause (stops Zabbix collection), hide
  (Argus-side suppression), each with duration picker and auto-expiry. Host-level states
  inherit to sensors.
- **Overview** — cross-site active-problem list, deep-linked to the tree, with a six-chip
  status summary (OK / Warning / Error / Acknowledged / Paused / Hidden).
- **Notifications** — Argus-native alerting engine (Discord, Telegram, email) with
  60-second debounce, recovery notices, rich messages (status icons, value + threshold,
  2-hour trend graph, deep-links, one-click acknowledge).
- **Auth** — three roles (admin / helpdesk / viewer), argon2id passwords, TOTP two-factor
  with recovery codes, WebAuthn passkeys, admin user management, login rate-limiting.
- **Admin Settings** — an in-app page to change the Zabbix connection, public URL, timezone,
  and login limits at runtime (no redeploy); env vars, when set, take precedence and lock the field.
- **Security** — AES-256-GCM encryption at rest for stored secrets, brute-force protection
  by IP + account.
- **Mobile-responsive** — off-canvas drawer sidebar, scrollable status chips, stacked card
  layout with labeled fields on small screens.
- **Live probes** — real Zabbix proxy status (online/offline, last seen, mode).

Argus is not tied to a specific network vendor or topology. Any setup that runs Zabbix —
from a homelab to a multi-site enterprise — can layer Argus on top.

## Repository layout

| Path | What |
|---|---|
| [`argus/`](argus/README.md) | The app — Go backend + React frontend, packaged as a Docker image to GHCR |
| [`deploy/`](deploy/README.md) | Deploy kit — Zabbix core install, probe scripts, PKI, unRAID templates, checklist |
| [`docs/DESIGN.md`](docs/DESIGN.md) | Full design document (architecture, device classes, thresholds, roadmap) |
| [`.github/workflows/`](.github/workflows/build.yml) | CI — builds the Argus image, pushes to `ghcr.io/<owner>/argus`, auto-publishes GitHub Releases on tags |
| [`CHANGELOG.md`](CHANGELOG.md) | Release-by-release history |

---

## Quick start

Argus needs a running **Zabbix 7.0 server** with at least one host producing data. If you
already have Zabbix, skip to [Deploy Argus](#2-deploy-argus). If not, the full guide below
walks through the entire stack.

---

## Full deployment guide

### Prerequisites

| Component | Where | Notes |
|---|---|---|
| **Core VM** | A Linux VM (Debian 12+ / Ubuntu 24.04+) | Runs Zabbix server + PostgreSQL/TimescaleDB + Argus |
| **Docker Engine** | On the core VM | For the Argus container (and optionally probes) |
| **Probes** (optional) | One per remote site | Zabbix active proxy in a container (unRAID, Docker VM, etc.) |
| **Reverse proxy** (optional) | HAProxy / nginx / Caddy | For HTTPS + custom FQDN; not required for LAN-only use |

### 1. Deploy the Zabbix core

> The [`deploy/`](deploy/README.md) directory contains automated scripts and a
> [step-by-step checklist](deploy/PHASE0-CHECKLIST.md). Below is the condensed version.

#### 1a. Generate the PKI (mutual TLS for probes)

```bash
cd deploy/pki && chmod +x gen-certs.sh && ./gen-certs.sh
```

This creates a private CA and per-site client certificates in `pki/out/`. To add a site
later: `./gen-certs.sh mynewsite` (reuses the existing CA).

**Key rule:** `ca.key` stays offline. Each probe gets only `ca.crt` + its own cert + key.

#### 1b. Install Zabbix + PostgreSQL + TimescaleDB

```bash
cd deploy/core && chmod +x setup-core.sh
sudo DBPASS='<choose-a-strong-password>' ./setup-core.sh
```

The script installs Zabbix 7.0 LTS, PostgreSQL, and TimescaleDB (pinned to a
Zabbix-compatible 2.28.x). Then append the TLS + tuning config and deploy the certs:

```bash
cat zabbix_server.conf.snippet | sudo tee -a /etc/zabbix/zabbix_server.conf

sudo mkdir -p /etc/zabbix/certs
sudo cp ../pki/out/{ca.crt,zabbix-core.crt,zabbix-core.key} /etc/zabbix/certs/
sudo chown -R zabbix:zabbix /etc/zabbix/certs
sudo chmod 600 /etc/zabbix/certs/zabbix-core.key
```

Start the services:

```bash
sudo systemctl enable --now zabbix-server zabbix-agent2 nginx php8.4-fpm
```

Two steps are manual:
1. **Nginx** — uncomment `listen` / `server_name` in `/etc/zabbix/nginx.conf`, then
   restart nginx + php-fpm.
2. **Frontend wizard** — open `http://<core-ip>:8080` in a browser and complete the
   setup (DB connection, admin password, timezone). Set **Housekeeping** retention:
   history 30d, trends 730d, compression after 7d.

#### 1c. Register a probe (per remote site)

In the Zabbix UI → **Administration → Proxies → Create proxy**:
- Name: `proxy-<site>` (must match the probe's `ZBX_HOSTNAME`)
- Mode: **Active**
- Encryption → Connections from proxy: **Certificate**;
  Issuer `CN=Monitoring Core CA`, Subject `CN=proxy-<site>`

#### 1d. Deploy the probe container

Copy `ca.crt` + `proxy-<site>.crt` + `proxy-<site>.key` to the probe host, then run:

```bash
deploy/probe/run-probe.sh <site> <core-ip> [appdata-dir]
```

Or import the **unRAID template** (`deploy/unraid/zabbix-proxy-site1.xml`) via the Docker
tab → Add Container → Template dropdown.

The probe dials out to the core on TCP 10051 — no inbound ports needed at the remote site.
It buffers up to 7 days of data if the core is unreachable.

#### 1e. Verify

- Zabbix UI → Proxies → the proxy shows **Last seen** ticking (green).
- Add a test host (e.g. a gateway, ICMP Ping template, monitored by the proxy).
- Monitoring → Latest data should show ping values within 1–2 minutes.

### 2. Deploy Argus

> Full env-var reference: [`argus/README.md`](argus/README.md#configuration-env-vars)
>
> unRAID template: [`deploy/unraid/argus.xml`](deploy/unraid/argus.xml)

#### 2a. Create a Zabbix API token

In the Zabbix UI → **Users → API tokens → Create API token**. The token needs **write**
scope (for acknowledge and pause). Copy the token value.

#### 2b. Run the container

```bash
docker run -d \
  --name argus \
  --restart unless-stopped \
  -p 8081:8080 \
  -v /docker/argus:/data \
  -e ARGUS_ZABBIX_API_URL=http://10.0.0.10:8080/api_jsonrpc.php \
  -e ARGUS_ZABBIX_API_TOKEN=<zabbix-api-token> \
  -e ARGUS_ADMIN_EMAIL=admin@example.com \
  -e ARGUS_ADMIN_PASSWORD=<strong-password> \
  -e ARGUS_COOKIE_SECURE=true \
  -e ARGUS_PUBLIC_URL=https://monitoring.example.com \
  -e ARGUS_TZ=Europe/Rome \
  -e ARGUS_SECRET_KEY=<run: openssl rand -hex 32 — set once, keep stable> \
  -e ARGUS_TRUST_PROXY=true \
  -e ARGUS_RP_ID=monitoring.example.com \
  -e ARGUS_RP_DISPLAY_NAME=Argus \
  -e ARGUS_RP_ORIGINS=https://monitoring.example.com \
  ghcr.io/<your-account>/argus:latest
```

> **First run:** `ARGUS_ADMIN_EMAIL` / `ARGUS_ADMIN_PASSWORD` seed the initial admin only
> when the database is empty; they're ignored afterwards, so you can drop them on later runs.
>
> **Port note:** the Zabbix web UI usually holds host 8080, so Argus is published on host
> 8081 (→ container 8080).
>
> **Passkeys** (`ARGUS_RP_*`) need HTTPS + a real domain — omit them for a plain-HTTP/IP
> setup and Argus falls back to password + TOTP.
>
> **`ARGUS_SECRET_KEY`** encrypts stored secrets at rest; keep it off the data volume and
> **stable** — changing it makes existing encrypted values unreadable.
>
> **`ARGUS_TRUST_PROXY=true`** is required behind a reverse proxy for correct client-IP
> rate limiting (ensure the proxy sends `X-Forwarded-For`).

#### 2c. Verify

Open `http://<core-ip>:8081` — you should see the Argus login page. Sign in with the
admin credentials from step 2b. The Monitoring tab should show hosts and live data from
Zabbix.

### 3. Updating Argus

Argus uses a rolling `:latest` tag — pushing to `main` builds a new image. To update:

```bash
docker pull ghcr.io/<your-account>/argus:latest
docker rm -f argus
docker run -d --name argus ... # same run command as above
```

State (SQLite database + encryption keyfile) lives on the `/data` volume and persists
across container recreations.

---

## Configuration reference

All Argus configuration is via environment variables. Full table with defaults:
[`argus/README.md` — Configuration (env vars)](argus/README.md#configuration-env-vars). Several
(Zabbix URL + token, Public URL, timezone, login limits) can also be changed at runtime from the
admin **Settings** page — a set env var takes precedence and locks the field.

| Group | Vars |
|---|---|
| **Core** | `ARGUS_ZABBIX_API_URL`, `ARGUS_ZABBIX_API_TOKEN`, `ARGUS_DATA_DIR`, `ARGUS_LISTEN` |
| **First-run admin** | `ARGUS_ADMIN_EMAIL`, `ARGUS_ADMIN_PASSWORD` |
| **Security** | `ARGUS_COOKIE_SECURE`, `ARGUS_SECRET_KEY`, `ARGUS_TRUST_PROXY`, `ARGUS_LOGIN_MAX_ATTEMPTS`, `ARGUS_LOGIN_WINDOW_MINUTES` |
| **Notifications** | `ARGUS_PUBLIC_URL`, `ARGUS_TZ` |
| **Passkeys** | `ARGUS_RP_ID`, `ARGUS_RP_DISPLAY_NAME`, `ARGUS_RP_ORIGINS` |

---

## Architecture

```
Remote sites
  └─ PROBE  [Docker container per site]
        └─ Zabbix active proxy + SQLite spool (7-day offline buffer)
           │
           │  pushes (probe INITIATES) ─── mutual TLS ──▶ core :10051
           ▼
CORE  [VM]
  ├─ zabbix-server                 collection engine, triggers, thresholds
  ├─ zabbix-web                    JSON-RPC API (admin "engine room")
  ├─ PostgreSQL + TimescaleDB      history + trends (time-series store)
  └─ ARGUS  [Docker container]     UI + auth + notifications
        ▲
     reverse proxy (optional)      HTTPS + custom FQDN
```

- **Probes** are Zabbix active proxies — they dial out to the core, so remote sites need
  only **outbound** TCP 10051. No inbound ports required.
- **Mutual TLS**: one shared CA, one unique client cert per site. A compromised probe key
  is contained to that site.
- **Argus** reads from the Zabbix API (JSON-RPC) and stores its own state (users, channels,
  suppressions) in an embedded SQLite database on a mounted volume.

---

## CI/CD

GitHub Actions (`.github/workflows/build.yml`):
- **Push to `main`** → builds the multi-stage Docker image → pushes `:latest` + `:sha-xxxxx` to GHCR.
- **Push a tag `vX.Y.Z`** → builds the versioned image → auto-publishes a GitHub Release
  from the matching `CHANGELOG.md` section.

---

## Status & roadmap

**Shipped:**
- v0.1.0 — Feature-complete UI (monitoring tree, states, overview, auth, passkeys, users)
- v0.2.0–v0.2.3 — Notifications (alerting engine, channels, rich messages, trend graphs)
- v0.2.4–v0.2.6 — Mobile-responsive layout
- v0.2.7–v0.2.8 — Security hardening (rate limiting, at-rest encryption)

**Planned:**
- Probe enrollment — token-based PKI enrollment service (replaces manual `gen-certs.sh`)
- Auto-discovery — UniFi API sweep + SNMP fingerprinting → automatic host/sensor provisioning
- Per-channel severity filter, labeled graph axes
- Scaling pass for large deployments (~6000 sensors)

See [`docs/DESIGN.md`](docs/DESIGN.md) for the full design and device-class definitions.

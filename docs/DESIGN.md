# Monitoring System — Design Document

Status: **Design locked (v1)** · Last updated: 2026-08-09

A self-hosted, PRTG-style monitoring system built as a **hybrid**: Zabbix as the
collection/transport/buffering engine, plus a custom web application ("the cockpit")
that owns the UI, authentication, dashboards, and per-site notifications. Not tied to
any specific network vendor — any Zabbix deployment can layer this on top.

---

## 1. Goals & context

- Replace 5× free PRTG instances + 1× Uptime Kuma with one system.
- 5 sites — `site1` (Site 1), `site2` (Site 2), `site3` (Site 3), `site4` (Site 4),
  `site5` (Site 5) — connected via UniFi Site Magic.
- Each site: UniFi Cloud Gateway, ≥1 UniFi switch, ≥1 UniFi AP, 1 XCP-NG host.
  `site1` and `site2` also have an unRAID server.
- Keep the **PRTG architecture**: a core server that displays data, with remote
  probes that collect it and survive site/internet/VPN outages.
- Prefer **SNMP** where possible; use device APIs (UniFi / unRAID / XCP-NG) where SNMP
  falls short.
- Deployment via **`docker run`** (no compose); **unRAID template XML** for the probes.
- **Scale target (future):** may be deployed at work to replace a **~6000-sensor PRTG** install
  → probe deployment must be fast/repeatable; triggers a sizing pass (proxies, DB, caches)
  before that rollout. Homelab is the derisking ground first.

---

## 2. High-level architecture

```
Remote sites (site1, site2, site3, site4, site5)
  └─ PROBE  [1 Docker container]
        ├─ Zabbix proxy (active mode) + local SQLite spool (7-day offline buffer)
        └─ Discovery/collector sidecar (queries site-local UniFi/unRAID/XCP-NG APIs)
        │
        │  pushes (proxy INITIATES) ── mutual TLS ──▶ core :10051 (published, secured)
        ▼
CORE  [dedicated VM]
  ├─ zabbix-server            (engine: triggers, thresholds, discovery orchestration)
  ├─ zabbix-web               (serves JSON-RPC API + admin "engine room")
  └─ PostgreSQL + TimescaleDB (history + trends = the time-series store; also app data)

CUSTOM APP  [Docker, on/next to core VM]  ← "the cockpit"
  ├─ Backend API + notifier (talks to Zabbix API + Timescale)
  └─ Frontend (responsive web UI)
        ▲
     HAProxy (custom FQDN)   ← human access
```

### Two independent exposure paths (do not conflate)
- **Probe ingestion:** proxies push to the Zabbix **server** on `:10051`, secured with
  **per-probe mutual TLS**. This is the port published for remote sites without a VPN.
- **Human access:** the custom app via **HAProxy** on the FQDN. Zabbix's own web
  frontend stays private / admin-only.

### Why VM for core, Docker for probes
- Core is a multi-component stateful stack (server + web/API + DB) deployed **once** →
  a VM is cleaner than 3 linked `docker run`s and sidesteps the "no compose" pain.
- **Core VM host: XCP-NG** (IP **10.0.0.10**). Thin-provisioned vDisk on SSD storage for
  Postgres; snapshots/backups via XCP-NG / Xen Orchestra; nightly `pg_dump` recommended regardless.
- Probe is a **single container** (proxy uses embedded SQLite) → perfect `docker run` +
  unRAID template, deployed 5×.

### Probe placement
| Site | Probe runs on |
|---|---|
| site1 | Docker on unRAID |
| site2 | Docker on unRAID |
| site3 | Docker on the existing Docker VM |
| site4 | Docker on the existing Docker VM |
| site5 | Docker on the existing Docker VM |

---

## 3. Data flow & offline buffering

- Proxies run in **active mode** — they initiate the connection to the core. This
  satisfies "probe sends to core, not the other way around" and supports future remote
  sites with no VPN / no static IP (just publish the core port).
- Each proxy keeps an **always-on local SQLite spool** (not created-on-demand — simpler
  and more reliable). Configured buffer: **7 days** (`ProxyOfflineBuffer`).
- On outage, data accumulates in the spool; on reconnect it flushes to the core
  automatically. No data loss up to the buffer window.
- **Site-local APIs** (UniFi controller per gateway, unRAID API, XCP-NG XAPI) are only
  reachable from inside the site, so the **discovery/collector sidecar runs on the
  probe** and reports findings to the core over the same secured channel. The core then
  provisions hosts/items via the Zabbix API and assigns them to that proxy.

---

## 4. Security & addressing

- **Public FQDN (human access):** `monitoring.example.com` (custom app via HAProxy, :443).
  This is also the **WebAuthn RP ID**.
- **Core OS:** Debian 13 (trixie) — PostgreSQL 17, PHP 8.4.
- **Probe → core addressing:** the Zabbix server listens on **:10051** (core = **10.0.0.10**).
  Current sites reach it over **UniFi Site Magic**; only **TCP 10051 outbound** (probe→core)
  is required — active proxies dial out, so nothing inbound is needed at the remote site. Future no-VPN sites reach
  `monitoring.example.com:10051` (published). The mTLS server cert uses `CN=zabbix-core`
  and Zabbix validates by **issuer/subject, not hostname/SAN** — so the FQDN choice does
  not affect the proxy certs.
- **Probe ↔ core:** mutual TLS. **One shared CA** signs a **unique per-site client cert**
  (never shared across sites); the core trusts the CA but pins each proxy to `CN=proxy-<site>`.
  A leak is contained to one site; adding a site = sign one new leaf with the existing CA
  (`gen-certs.sh <site>`). `ca.key` stays offline, never on a probe. (Token-over-TLS as a
  fallback where mTLS is impractical.)
- **Probe enrollment (token-based, preferred):** the core runs a small enrollment/PKI service.
  Admin creates a short-TTL token in the UI → probe boots with the token → probe generates its
  own keypair **locally** and sends a CSR → core validates the token, signs the cert, registers
  the proxy via the Zabbix API, returns cert + `ca.crt`. The **private key never leaves the
  probe**. This is the fast/scaled path; `gen-certs.sh` is the manual Phase-0 stand-in.
  (Enrollment service = Phase 1 backend; "Add probe" wizard UI = Phase 4/6.)
- **CSRF / allowed hosts:** allowed-hosts/origins list = `monitoring.example.com` **+ the
  private IP**, with SameSite cookies + CSRF tokens.
- **Passkey caveat (accepted):** WebAuthn RP IDs must be a domain, not a bare IP.
  → Passkey login works via `monitoring.example.com`; direct **private-IP** access
  (troubleshooting) falls back to **password + MFA**.

---

## 5. Device classes & templates

Every host gets the **Base** template (Ping — latency + loss, always). Then one or more
class templates attach automatically by fingerprint. 11 classes:

| Class | Detected by | Metrics beyond Ping | Source | LLD-discovered |
|---|---|---|---|---|
| **UniFi Gateway** (any UniFi-managed gateway) | UniFi API type `ugw/udm` | Uptime, WAN up/down, per-port traffic, PoE, clients, CPU/mem | UniFi API (SNMP fallback) | ports |
| **UniFi Switch** (USW / Flex) | UniFi API type `usw` | Uptime, per-port traffic, PoE per-port, CPU/mem | UniFi API (SNMP fallback) | ports |
| **UniFi AP** (U6-PRO) | UniFi API type `uap` | Uptime, per-radio traffic, clients, channel util | UniFi API (SNMP fallback) | radios |
| **MikroTik** (CRS305) | sysObjectID `.1.3.6.1.4.1.14988` | Uptime, CPU load, board/CPU temp, per-port traffic | SNMP (MIKROTIK-MIB `…1.1.3` + IF-MIB) | interfaces |
| **unRAID** | sysDescr `Unraid` / tag | CPU load, RAM %, uptime, per-share disk-free, per-disk temp, per-disk I/O, NIC traffic | SNMP host-resources + **unRAID API (disk temp/SMART)** | disks, shares, NICs |
| **XCP-NG host** | XAPI reachable / sysDescr `XCP-ng` | Host CPU, RAM, per-VM state, pool, CPU temp | **XAPI (RRD)** + SNMP/IPMI (temp) | VMs, PBDs |
| **Generic Linux/VM** (Home Assistant) | answers host-resources SNMP, unmatched | CPU, RAM, filesystem free, uptime | SNMP (host-resources + UCD) | filesystems, NICs |
| **Web service / app** (Plex, PiKVM) | answers HTTP(S), thin SNMP | HTTP/HTTPS up + response time + **TLS cert expiry**, custom port | HTTP checks | — |
| **DNS server** (AdGuard ×2) | :53 + HTTP admin | HTTP admin up + **DNS resolve check** (query known record, verify answer + time) | HTTP + `net.dns` | — |
| **UPS** (NUT / SNMP) | NUT :3493 or SNMP `.1.3.6.1.2.1.33` | Battery %, on-battery status, runtime, load, input voltage | NUT or UPS-MIB | — |
| **Printer / pingable** (printers, etc.) | fallback | Ping only | ICMP | — |

### SNMP gaps (API required)
- **unRAID disk temps & SMART** → smartctl via Net-SNMP `extend`, or the unRAID API (chosen).
- **XCP-NG per-VM + host temp** → XAPI/RRD; host CPU temp via IPMI/lm-sensors.
- **UniFi per-port / PoE / WAN throughput** → controller API (SNMP is thin).

---

## 6. Thresholds (typed defaults, all overridable per device/sensor)

| Metric | Warning | Error |
|---|---|---|
| Disk free | ≤ 10% free | ≤ 5% free |
| CPU load | ≥ 80% | ≥ 95% |
| RAM used | ≥ 85% | ≥ 95% |
| CPU temp | ≥ 75 °C | ≥ 85 °C |
| Disk temp — **HDD** | ≥ 40 °C | ≥ 45 °C |
| Disk temp — **SSD** | ≥ 50 °C | ≥ 60 °C |
| Ping | loss ≥ 20% or latency > 150 ms | 100% loss (down) |
| HTTP/HTTPS | resp > 1 s | non-2xx/3xx or timeout |
| TLS cert expiry | ≤ 14 days | ≤ 3 days |
| DNS | resolve > 500 ms | no/incorrect answer |
| UPS | — | **on battery** / runtime < 5 min / replace battery |
| Printer supply | (not monitored) | (not monitored) |

- Disk-temp thresholds are **type-aware** (HDD vs SSD) via the SMART `rotational` flag
  (from the unRAID API). Pure-SNMP disks with unknown type fall back to the SSD numbers.

---

## 7. Sensor state model & dashboards

States (native Zabbix problem events, acknowledgement, maintenance):

| State | Meaning |
|---|---|
| OK | within thresholds |
| Warning | past warn threshold |
| Error | past error threshold |
| Acknowledged | a Warning/Error a human marked "seen / handling" |
| Paused | maintenance — not evaluated |

Dashboards (list views, same event stream):
- **Errors-only** → shows Error; **hides Acknowledged and Paused**.
- **Errors + Warnings** → shows Error + Warning, **including acknowledged (dimmed/tagged)**.

---

## 8. Auto-provisioning pipeline (replaces PRTG's "Add Sensor")

Runs per-site on the probe, reports to core for provisioning:
1. **UniFi API sweep** → managed inventory (gateway/switches/APs + known clients) with
   model/MAC/IP/uptime/port stats → become Zabbix hosts, tagged by site, bound to that proxy.
2. **Capability fingerprint** per host → SNMP (`sysObjectID`), HTTP(S), DNS :53, NUT :3493.
3. **Template attach** by fingerprint.
4. **LLD** creates only instances that exist (disks, filesystems, NICs, temps, PSUs, ports)
   → satisfies "only show fields the device reports."
5. **Default thresholds** applied; overridable in the UI.
6. New devices surface in the UI as **"Discovered — review"** (confirm / adjust / ignore).

---

## 9. Notifications

Abstraction separates **credentials** from **targets** so shared-vs-dedicated is a
per-channel choice:
- **Credential** = reusable secret (SMTP account, Telegram bot token, Discord webhook URL).
- **Target** = credential + destination (Telegram topic, Discord webhook, email address).
- **Instance** = one per site (+ core) = a bundle of targets firing on state changes.

Owned by the **custom notifier** (Zabbix emits site-tagged events; the notifier routes).

| Site | Telegram | Discord | Email |
|---|---|---|---|
| site1 | shared bot → topic | dedicated webhook | alerts@example.com |
| site2 | shared bot → topic | dedicated webhook | alerts@example.com |
| site3 | shared bot → topic | dedicated webhook | alerts@example.com |
| site4 | shared bot → topic | dedicated webhook | alerts@example.com |
| site5 | shared bot → topic | dedicated webhook | alerts@example.com |
| **core/global** | shared bot → topic | dedicated webhook | alerts@example.com |

- **Routing:** Warning **and** Error → Telegram + Discord + email (same for all sites).
- **Recovery (OK) notifications:** enabled.
- **Flap debounce:** a sensor must hold a state for N consecutive polls before notifying.
- Telegram = one shared bot, per-site topic. Discord = dedicated webhook per site.
  Model supports flipping either to shared/dedicated with no code change.
- Secrets entered in the UI later (placeholders for now).

---

## 10. Users, roles & authentication

- **Fields:** name, surname, email (self-service reset), password. MFA optional (TOTP).
  Passkey (WebAuthn) login optional.
- **Roles:**
  - **Admin** — everything, incl. user management + core system settings; can reset other
    users' password / MFA / passkey.
  - **Helpdesk** — all device/sensor/threshold/notification/discovery ops + ack + pause;
    **no** user management, **no** core system settings.
  - **Viewer** — view + **acknowledge** only (no pause, no edits).
- **Auth lives in the custom app** (Zabbix frontend locked down; app uses a service
  account to the Zabbix API).
- **Per-user landing page** preference — default Overview; user can switch to Errors page.

---

## 11. Custom app screens

**Viewing:** 1) Overview (all sites, health rollup — default landing) · 2) Errors-only ·
3) Errors + Warnings · 4) Site view (device list) · 5) Device view (sensor tiles) ·
6) Sensor detail (graphs).

**Managing:** 7) Discovery review · 8) Device management (add/edit, assign site+proxy,
class, threshold overrides, pause, acknowledge) · 9) Thresholds (global + overrides) ·
10) Notifications (instances, credentials, targets, test-send) · 11) Users & security ·
12) Settings (FQDN/allowed-hosts, retention, proxy status).

---

## 12. Graphs, time tabs & retention

- Tabs: **2h · 2d · 1M · 3M · 6M · 1Y**, with zoom-to-timeframe.
- Maps onto Zabbix's data split (no custom downsampling needed):
  - **history** (raw, retain ~7–30 d) → powers **2h / 2d** + zoom.
  - **trends** (hourly min/avg/max, retain 1–2 y) → powers **1M / 3M / 6M / 1Y**.
- Storage: **PostgreSQL + TimescaleDB** as Zabbix's DB (native integration, partitioning +
  compression). Single source of truth; app data lives in the same instance (separate schema).

---

## 13. Responsive UI targets
- Phone (S25 Ultra), tablet (Galaxy Tab S9), desktop 16:9 / 16:10 / 32:9.
- Look: PRTG-style density, Uptime-Kuma-grade polish.

---

## 14. Deployment
- Core: dedicated VM (Zabbix server + web + Timescale). Custom app: Docker container(s).
- Probes: single `docker run` container per site + **unRAID template XML**.
- No docker-compose.
- **Probe delivery — one artifact, two vehicles:** (a) Docker image (unRAID / any docker host);
  (b) a golden **Debian 13 VM template** built with **Packer** that runs the same probe
  container, seeded per-site via **cloud-init** (2 vars: site name + enrollment token). On
  XCP-NG use cloud-init config-drive or an **XVA** template clone → spin up a site in minutes.

---

## 14b. Resource sizing

**Core VM (homelab, ~5 sites / few hundred items):** recommended **4 vCPU / 8 GB RAM /
60 GB disk** (min 2 / 4 / 40). One VM runs Zabbix server + PostgreSQL/TimescaleDB + frontend
+ the custom app container. Timescale compression (~10×) keeps the DB to a few GB; 60 GB is
OS + DB + logs + app + headroom. On XCP-NG: thin-provisioned vDisk on
SSD storage; grow later if needed.

**Probe (each):** container ≈ 1 vCPU / 0.5–1 GB RAM / 4–8 GB disk (7-day SQLite spool is small).
As a VM ≈ 1–2 vCPU / 2 GB / ~15 GB.

**Future ~6000-sensor work deployment (rough; sizing pass TBD):** ~8 vCPU / 16–32 GB RAM /
DB on fast SSD, likely with PostgreSQL/Timescale split onto its own VM. ~100–200 NVPS =
moderate Zabbix load; architecture unchanged, resources scaled.

## 15. Tech stack (confirmed)
- **App name:** **Argus.** Monorepo: `docs/`, `deploy/`, `argus/` (the app), `.github/` (CI).
- **Backend / notifier:** **Go** (single static binary, distroless image).
- **Frontend:** **React + Vite** (uPlot for the dense/zoomable time-series graphs). The Go
  binary **serves the built SPA** via `go:embed` — one container, one origin (simplifies
  cookies / CSRF / passkeys).
- **App data:** **embedded SQLite** in a mounted volume (users, roles, config, CA, enrollment
  tokens). Metrics stay in Zabbix/TimescaleDB, read via the **Zabbix JSON-RPC API** (direct
  Timescale reads are a later performance optimization).
- **Delivery:** GitHub Actions builds a multi-stage image → **`ghcr.io/<owner>/argus`**;
  deployed on the core VM via `docker run` (dev PC has no VM access, so build/test happens
  through the CI→GHCR pipeline — "walking skeleton" first to validate the pipeline).

---

## 16. Global search (host & sensor) — future phase

**Motivation (scale-driven).** At homelab scale (a few hundred items) the site→host→sensor
tree plus the status chips are enough to find anything. At the target **~6000-sensor** work
deployment, expanding sites/hosts to locate one device does not scale — you need to jump
straight to a host or sensor by name. This phase is therefore parked until the production
rollout; it is low priority for the homelab but important before the large deployment.

**Scope.**
- A persistent **search box in the top bar** with a keyboard shortcut (e.g. `/` or `Ctrl/⌘-K`)
  opening a quick-switcher palette.
- **Hosts** searchable by visible name, technical name, interface IP/DNS, host group (site),
  and Zabbix tags.
- **Sensors/items** searchable by name and key — globally or scoped to a host.
- Results are grouped (Hosts / Sensors); each row **deep-links into the existing tree**
  (reusing the current `goHost` / `goSensor` navigation) and/or opens the sensor's chart.

**Implementation — must be server-side at scale.**
- Back it with a new endpoint `GET /api/search?q=…` that calls Zabbix `host.get` / `item.get`
  with `search` / `searchByAny` filters and a **result cap** (e.g. top ~50), **debounced** on
  the client. Do **not** filter a full client-side census — the current `/api/sensors` census
  is fine for the homelab but would mean shipping thousands of items to the browser at
  production scale.
- Honour the same **role and suppression** model as the rest of the UI.

**Nice-to-haves.** Recent/pinned hosts; filter tokens (`site:`, `tag:`, `down:`) for power
users; fuzzy matching. Pairs naturally with the **sizing pass** (§14b) as part of readying the
6000-sensor deployment.

---

## 17. Deep-link URLs & reload persistence — ✅ implemented (v0.3.1)

**Problem.** The SPA tracked the active view in React state only; it never reflected navigation
in the address bar, and it deliberately strips `?host=&item=` after consuming a notification
deep-link. So the URL stays at the base FQDN, a **reload resets to the Overview** landing page,
notification "Open in Argus" links don't survive a refresh, and a specific sensor view can't be
bookmarked or shared.

**Scope.**
- Encode the current view (and `host`/`item` for the tree, `filter` for the status lists) in the
  URL — query params or a hash route — and `pushState` on navigation.
- Parse the URL on load to restore the exact view (extends the existing `?host=&item=` handler;
  stop stripping it).
- Handle browser **back/forward** (`popstate`).

**Effort.** Small, **frontend-only** (`web/src/App.tsx`), **no backend change and no new
dependency** — the native History API is enough (a tiny router could be added but isn't needed).
Bonus: makes notification deep-links reload-safe and shareable.

**Delivered.** The active view is encoded as `?view=…` (list adds `&filter=…`; monitoring adds
`&host=…&item=…` when a host/sensor is open), pushed on tab switches / deep-link jumps and
refined in place (`replaceState`) on in-tree drilldown; Back/Forward restore the view; admin-only
views are clamped for non-admins on a shared/stale URL.

---

## 18. Parking lot / future
- Public status page (Uptime-Kuma-style shareable page).
- Native mobile apps (only if the responsive web UI proves insufficient).
- Escalation policies / repeat notifications beyond flap debounce.
- Token-based enrollment service (Phase 1 backend) + "Add probe" wizard (Phase 4/6 UI).
- Golden probe **VM template** (Packer) + cloud-init for scaled/work rollout (Phase 6).
- Sizing pass before the ~6000-sensor work deployment.
```

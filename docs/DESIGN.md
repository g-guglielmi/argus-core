# Monitoring System - Design Document

Status: **Design locked (v1)** · Last updated: 2026-08-09

A self-hosted, PRTG-style monitoring system built as a **hybrid**: Zabbix as the
collection/transport/buffering engine, plus a custom web application ("the cockpit")
that owns the UI, authentication, dashboards, and per-site notifications. Not tied to
any specific network vendor - any Zabbix deployment can layer this on top.

---

## 1. Goals & context

- Replace 5× free PRTG instances + 1× Uptime Kuma with one system.
- 5 sites - `site1` (Site 1), `site2` (Site 2), `site3` (Site 3), `site4` (Site 4),
  `site5` (Site 5) - connected via UniFi Site Magic.
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

- Proxies run in **active mode** - they initiate the connection to the core. This
  satisfies "probe sends to core, not the other way around" and supports future remote
  sites with no VPN / no static IP (just publish the core port).
- Each proxy keeps an **always-on local SQLite spool** (not created-on-demand - simpler
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
- **Core OS:** Debian 13 (trixie) - PostgreSQL 17, PHP 8.4.
- **Probe → core addressing:** the Zabbix server listens on **:10051** (core = **10.0.0.10**).
  Current sites reach it over **UniFi Site Magic**; only **TCP 10051 outbound** (probe→core)
  is required - active proxies dial out, so nothing inbound is needed at the remote site. Future no-VPN sites reach
  `monitoring.example.com:10051` (published). The mTLS server cert uses `CN=zabbix-core`
  and Zabbix validates by **issuer/subject, not hostname/SAN** - so the FQDN choice does
  not affect the proxy certs.
- **Probe ↔ core:** mutual TLS. **One shared CA** signs a **unique per-site client cert**
  (never shared across sites); the core trusts the CA but pins each proxy to `CN=proxy-<site>`.
  A leak is contained to one site; adding a site = sign one new leaf with the existing CA
  (`gen-certs.sh <site>`). `ca.key` stays offline, never on a probe. (Token-over-TLS as a
  fallback where mTLS is impractical.)
- **Probe enrollment (token-based, preferred): ✅ implemented (v0.4.0).** The core runs a small
  enrollment/PKI service. Admin creates a short-TTL token in the UI → probe boots with the token →
  probe generates its own keypair **locally** and sends a CSR → core validates the token, signs the
  cert, registers the proxy via the Zabbix API, returns cert + `ca.crt`. The **private key never
  leaves the probe**. Argus signs with the mounted CA (`ARGUS_CA_*`); the self-enrolling
  `argus-probe` image runs the probe side. `gen-certs.sh` remains the manual fallback.
- **CSRF / allowed hosts:** allowed-hosts/origins list = `monitoring.example.com` **+ the
  private IP**, with SameSite cookies + CSRF tokens.
- **Passkey caveat (accepted):** WebAuthn RP IDs must be a domain, not a bare IP.
  → Passkey login works via `monitoring.example.com`; direct **private-IP** access
  (troubleshooting) falls back to **password + MFA**.

---

## 5. Device classes & templates

Every host gets the **Base** template (Ping - latency + loss, always). Then one or more
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
| **Web service / app** (Plex, PiKVM) | answers HTTP(S), thin SNMP | HTTP/HTTPS up + response time + **TLS cert expiry**, custom port | HTTP checks | - |
| **DNS server** (AdGuard ×2) | :53 + HTTP admin | HTTP admin up + **DNS resolve check** (query known record, verify answer + time) | HTTP + `net.dns` | - |
| **UPS** (NUT / SNMP) | NUT :3493 or SNMP `.1.3.6.1.2.1.33` | Battery %, on-battery status, runtime, load, input voltage | NUT or UPS-MIB | - |
| **Printer / pingable** (printers, etc.) | fallback | Ping only | ICMP | - |

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
| Disk temp - **HDD** | ≥ 40 °C | ≥ 45 °C |
| Disk temp - **SSD** | ≥ 50 °C | ≥ 60 °C |
| Ping | loss ≥ 20% or latency > 150 ms | 100% loss (down) |
| HTTP/HTTPS | resp > 1 s | non-2xx/3xx or timeout |
| TLS cert expiry | ≤ 14 days | ≤ 3 days |
| DNS | resolve > 500 ms | no/incorrect answer |
| UPS | - | **on battery** / runtime < 5 min / replace battery |
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
| Paused | maintenance - not evaluated |

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
6. New devices surface in the UI as **"Discovered - review"** (confirm / adjust / ignore).

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
  - **Admin** - everything, incl. user management + core system settings; can reset other
    users' password / MFA / passkey.
  - **Helpdesk** - all device/sensor/threshold/notification/discovery ops + ack + pause;
    **no** user management, **no** core system settings.
  - **Viewer** - view + **acknowledge** only (no pause, no edits).
- **Auth lives in the custom app** (Zabbix frontend locked down; app uses a service
  account to the Zabbix API).
- **Sessions:** admin-configurable **max lifetime** (default **12h**, `ARGUS_SESSION_MAX_HOURS`)
  plus an optional **idle timeout** (default off, `ARGUS_SESSION_IDLE_MINUTES`; a per-session
  `last_seen` is bumped by the auth middleware, throttled to ≤1 write/min). Both live in
  **Settings → Sessions** and honour env-wins precedence.
- **Per-user landing page** preference - default Overview; user can switch to the Errors list in
  **Account → Landing page** (stored server-side, `POST /api/me/preferences`).

---

## 11. Custom app screens

**Viewing:** 1) Overview (all sites, health rollup - default landing) · 2) Errors-only ·
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
  - **history** (raw, retain ~7-30 d) → powers **2h / 2d** + zoom.
  - **trends** (hourly min/avg/max, retain 1-2 y) → powers **1M / 3M / 6M / 1Y**.
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
- **Probe delivery - one artifact, two vehicles:** (a) Docker image (unRAID / any docker host);
  (b) a golden **Debian 13 VM template** built with **Packer** that runs the same probe
  container, seeded per-site via **cloud-init** (2 vars: site name + enrollment token). On
  XCP-NG use cloud-init config-drive or an **XVA** template clone → spin up a site in minutes.
  Full delivery/enrollment model (cloud-init primary + first-boot fallback, and an optional
  bare-metal Clonezilla wrapper) in **§14a**.

---

## 14a. Self-configuring probe VM - delivery vs. enrollment

Two **independent** concerns, deliberately decoupled so one golden image serves every target:

1. **Image delivery** - how the bits land on the VM's disk.
2. **Enrollment** - how the per-instance secret (the site name + one-time enroll token) gets in.

A single golden image (Packer: Debian 13 + the `argus-probe` container, no baked-in token) is built
once and carries the **first-boot enrollment service** described below, so the *same* image works
whether it's seeded automatically, configured by hand, or restored to bare metal.

### Golden image build - unattended install (preseed)
Packer drives `debian-installer` fully unattended via a **preseed** file (`preseed.cfg`, served over
HTTP or on the boot media) so the base OS is built with zero prompts. Target answers (site-invariant -
these are baked into the image; the per-site secret is injected later at enrollment):

| Installer step | Value | Preseed key (approx.) |
| --- | --- | --- |
| Language | English | `debian-installer/language = en` |
| Country / location | Italy (region: Europe) | `debian-installer/country = IT` |
| Locale | `en_US.UTF-8` | `debian-installer/locale = en_US.UTF-8` |
| Keyboard | Italian | `keyboard-configuration/xkb-keymap = it` |
| Timezone | `Europe/Rome` | `time/zone = Europe/Rome` |
| Hostname | placeholder (set per-site at enrollment) | `netcfg/get_hostname` |
| Domain | placeholder (set per-site at enrollment) | `netcfg/get_domain` |
| Root account | enabled, password set at build | `passwd/root-login = true`, `passwd/root-password[-crypted]` |
| Partitioning | guided, entire disk, all files in one partition | `partman-auto/method = regular`, `partman-auto/choose_recipe = atomic` |
| Extra install media | none (don't scan another CD/DVD) | `apt-setup/cdrom/set-first = false` |
| Mirror country | Italy | `mirror/country = manual` + `mirror/http/hostname` |
| Mirror host | `deb.debian.org` | `mirror/http/hostname = deb.debian.org`, `mirror/http/directory = /debian` |
| Proxy | none | `mirror/http/proxy =` (empty) |
| Package usage survey (popcon) | disabled | `popularity-contest/participate = false` |
| Software selection | **standard system utilities + SSH server only** (no desktop) | `tasksel/first = standard, ssh-server` |

Notes:
- Hostname/domain are placeholders in the image; the **enrollment** step (cloud-init or the first-boot
  service) sets the real per-site values, so one image serves every site.
- Timezone is `Europe/Rome` (the "Italy" location from the installer); locale stays `en_US.UTF-8` per
  the request (English UI, US formatting) even though the country is Italy.
- Credentials follow the two-phase model below - the preseed's account is a **build-time throwaway**,
  scrubbed before the image ships; no shared credential ever ships in the golden image.

### Credential lifecycle - throwaway at build, per-VM secret at enrollment
The preseed password and the real access credential happen at two different times, so they are two
different things:

- **Build time (Packer / preseed) - throwaway account.** The preseed's account exists only so Packer
  can log in and provision the image. Packer generates a **random password for that one build**, and a
  final provisioner **scrubs it** before capture: `passwd -l` (or delete the throwaway user), wipe its
  SSH keys, and clean `cloud-init` state + `/etc/machine-id`. The shipped golden image therefore has
  **no usable, known credential** - nothing shared across the fleet.
- **Instance time (enrollment) - generated by core, stored encrypted, retrievable.** When the Add-probe
  wizard runs, core generates a **random per-VM password**, embeds it in the cloud-init user-data
  (alongside hostname/domain/token), and **stores it encrypted at rest** (reuse the existing
  AES-256-GCM encryption) keyed to that probe. The probe detail page reveals it (admin-only) as the
  **break-glass console credential** - for hypervisor-console access when SSH/network is down.
- **Normal access is by SSH key, not password.** cloud-init injects an SSH key (core-held or the
  operator's); the stored password is the rare-emergency path. For the **first-boot fallback** (no
  cloud-init datasource), core still generates and displays the password and the operator sets it once
  via the setup page.

### Enrollment - cloud-init primary, first-boot wizard fallback
- **Primary: cloud-init (zero-touch).** The Add-probe flow mints a token and emits **cloud-init
  user-data** (site name + enroll token), delivered as a **NoCloud seed ISO** or pasted into the
  hypervisor's cloud-init field. Native on every target here - VMware (guestinfo/OVF datasource),
  Nutanix, XCP-NG (config-drive / NoCloud), libvirt/KVM. First boot self-enrolls with no interaction.
- **Fallback: first-boot enrollment service (no datasource needed).** A tiny service that runs on
  first boot and, **only when no token was supplied by cloud-init**, serves a one-field setup page
  (paste the enroll command / claim code) on the VM's IP. This removes the hard dependency on a
  working cloud-init datasource (XCP-NG can be fiddly) and gives a graceful manual path. Idea borrowed
  from the [adsb-feeder](https://github.com/dirkhh/adsb-feeder-image) first-boot web wizard. Once a
  token is present (either way), the service is inert on subsequent boots.
- **Never** bake a token into the image (one-token-per-image, non-reusable, leaks the secret) - the
  image stays generic; the secret is always external.

### Image delivery - hypervisor import primary, Clonezilla for bare metal only
- **Primary (VMs): native disk-image import.** Distribute the golden image as **OVA** (VMware/Nutanix)
  + **qcow2/XVA** (KVM/XCP-NG). Hypervisors import these directly - thin, fast, standard.
- **Bare-metal only: Clonezilla-wrapped restore ISO.** For appliance-style installs with *no*
  hypervisor (a mini-PC / SBC at a site), optionally ship the same image inside a Clonezilla live ISO
  that asks only which disk to restore onto, à la adsb-feeder. This is a **later, optional SKU** - it
  buys nothing inside a hypervisor (where you'd be booting a live ISO to write a disk you could just
  import), so it's reserved for bare metal. Because delivery and enrollment are decoupled, the
  bare-metal image reuses the *same* first-boot enrollment service - no per-image token, no extra
  wizard to build.

**Net:** cloud-init + OVA/qcow2/XVA is the backbone for the hypervisor fleet; the first-boot service is
the everywhere-fallback that also unlocks the bare-metal Clonezilla path for free.

---

## 14b. Resource sizing

**Core VM (homelab, ~5 sites / few hundred items):** recommended **4 vCPU / 8 GB RAM /
60 GB disk** (min 2 / 4 / 40). One VM runs Zabbix server + PostgreSQL/TimescaleDB + frontend
+ the custom app container. Timescale compression (~10×) keeps the DB to a few GB; 60 GB is
OS + DB + logs + app + headroom. On XCP-NG: thin-provisioned vDisk on
SSD storage; grow later if needed.

**Probe (each):** container ≈ 1 vCPU / 0.5-1 GB RAM / 4-8 GB disk (7-day SQLite spool is small).
As a VM ≈ 1-2 vCPU / 2 GB / ~15 GB.

**Future ~6000-sensor work deployment (rough; sizing pass TBD):** ~8 vCPU / 16-32 GB RAM /
DB on fast SSD, likely with PostgreSQL/Timescale split onto its own VM. ~100-200 NVPS =
moderate Zabbix load; architecture unchanged, resources scaled.

---

## 14c. OS patching & lifecycle (core + probe VMs)

The container images already self-update with rollback; the **underlying Debian OS** of the core VM and
every probe VM needs its own patch story so it doesn't accumulate CVEs over time. Design splits along
"pet vs cattle".

**Baseline (both roles): `unattended-upgrades`, security suite only.** Baked into the golden image and
enabled on the core, configured to auto-apply `${distro_codename}-security` **only**. It **respects apt
pins/holds**, so the core's `timescaledb-2-*` hold (Zabbix 7.0 needs Timescale <= 2.28) is safe - it
will not drag Timescale forward. Ship `needrestart` too, so services restart after a libc/openssl bump
without needing a full reboot.

**Reboot policy differs by role:**
- **Probes (cattle) - automatic.** Auto-reboot in a **weekly maintenance window, ~03:00** local. Probes
  buffer 7 days offline, so a ~60s reboot is invisible. Fully hands-off.
- **Core (pet) - operator-scheduled.** Security patches auto-apply, but the **reboot is never
  unattended**. Argus core gets a small **Settings mask to pick a day + time** for the core's reboot
  window (or "notify only, never auto-reboot"), because it hosts the DB + Zabbix data plane and must not
  bounce unannounced.

**Visibility in core (patching stays local).** Extend the existing probe check-in, and add a core
self-report, to include the **pending-security-update count** and the **`/var/run/reboot-required`
flag**; surface per-probe + core in the UI ("N sites need a reboot"). Reuses the fleet/version-reporting
plumbing.

**Deliberately NOT remote-triggered.** Unlike container images (clean rollback), `apt upgrade` has no
clean rollback, and a remote OS upgrade bricking a probe at a hard-to-reach site is high-stakes. So the
OS patches itself locally (reliable; hypervisor snapshots are the safety net) and core only *reports* -
the remote-trigger-with-rollback pattern stays reserved for container images.

**Golden-image refresh cadence.** Re-run Packer periodically (e.g. quarterly or on each Debian point
release) so newly deployed probes ship already-patched instead of installing months of updates on first
boot. **Major-version upgrades (Debian 13 -> 14) are a deliberate manual / re-image event** - never
unattended.

## 15. Tech stack (confirmed)
- **App name:** **Argus.** Split across three repos: **argus-core** (this repo — the app in `argus/`, docs, core deploy kit), **argus-probe** (the probe Docker image + self-configuring golden VM), and **argus-updater** (the core self-update sidecar). Image names stay `argus` / `argus-probe` / `argus-updater` regardless of repo names.
- **Backend / notifier:** **Go** (single static binary, distroless image).
- **Frontend:** **React + Vite** (uPlot for the dense/zoomable time-series graphs). The Go
  binary **serves the built SPA** via `go:embed` - one container, one origin (simplifies
  cookies / CSRF / passkeys).
- **App data:** **embedded SQLite** in a mounted volume (users, roles, config, CA, enrollment
  tokens). Metrics stay in Zabbix/TimescaleDB, read via the **Zabbix JSON-RPC API** (direct
  Timescale reads are a later performance optimization).
- **Delivery:** GitHub Actions builds a multi-stage image → **`ghcr.io/<owner>/argus`**;
  deployed on the core VM via `docker run` (dev PC has no VM access, so build/test happens
  through the CI→GHCR pipeline - "walking skeleton" first to validate the pipeline).

---

## 16. Global search (host & sensor) - future phase

**Motivation (scale-driven).** At homelab scale (a few hundred items) the site→host→sensor
tree plus the status chips are enough to find anything. At the target **~6000-sensor** work
deployment, expanding sites/hosts to locate one device does not scale - you need to jump
straight to a host or sensor by name. This phase is therefore parked until the production
rollout; it is low priority for the homelab but important before the large deployment.

**Scope.**
- A persistent **search box in the top bar** with a keyboard shortcut (e.g. `/` or `Ctrl/⌘-K`)
  opening a quick-switcher palette.
- **Hosts** searchable by visible name, technical name, interface IP/DNS, host group (site),
  and Zabbix tags.
- **Sensors/items** searchable by name and key - globally or scoped to a host.
- Results are grouped (Hosts / Sensors); each row **deep-links into the existing tree**
  (reusing the current `goHost` / `goSensor` navigation) and/or opens the sensor's chart.

**Implementation - must be server-side at scale.**
- Back it with a new endpoint `GET /api/search?q=…` that calls Zabbix `host.get` / `item.get`
  with `search` / `searchByAny` filters and a **result cap** (e.g. top ~50), **debounced** on
  the client. Do **not** filter a full client-side census - the current `/api/sensors` census
  is fine for the homelab but would mean shipping thousands of items to the browser at
  production scale.
- Honour the same **role and suppression** model as the rest of the UI.

**Nice-to-haves.** Recent/pinned hosts; filter tokens (`site:`, `tag:`, `down:`) for power
users; fuzzy matching. Pairs naturally with the **sizing pass** (§14b) as part of readying the
6000-sensor deployment.

---

## 17. Deep-link URLs & reload persistence - ✅ implemented (v0.3.1)

**Problem.** The SPA tracked the active view in React state only; it never reflected navigation
in the address bar, and it deliberately strips `?host=&item=` after consuming a notification
deep-link. So the URL stays at the base FQDN, a **reload resets to the Overview** landing page,
notification "Open in Argus" links don't survive a refresh, and a specific sensor view can't be
bookmarked or shared.

**Scope.**
- Encode the current view (and `host`/`item` for the tree, `filter` for the status lists) in the
  URL - query params or a hash route - and `pushState` on navigation.
- Parse the URL on load to restore the exact view (extends the existing `?host=&item=` handler;
  stop stripping it).
- Handle browser **back/forward** (`popstate`).

**Effort.** Small, **frontend-only** (`web/src/App.tsx`), **no backend change and no new
dependency** - the native History API is enough (a tiny router could be added but isn't needed).
Bonus: makes notification deep-links reload-safe and shareable.

**Delivered.** The active view is encoded as `?view=…` (list adds `&filter=…`; monitoring adds
`&host=…&item=…` when a host/sensor is open), pushed on tab switches / deep-link jumps and
refined in place (`replaceState`) on in-tree drilldown; Back/Forward restore the view; admin-only
views are clamped for non-admins on a shared/stale URL.

---

## 18. Probe fleet updates - control plane (implemented, v0.4.8)

**Constraint.** Sites are outbound-only (probes dial out, nothing inbound), so Argus can't *push*
into a probe. Updates are therefore **pull-based but Argus-coordinated**: Argus is the control
plane; the probe checks in and converges.

**Model: control plane + opt-in self-update.**
- **Check-in credential.** Enrollment issues each probe a long-lived token (tied to its proxy
  name), stored hashed in `probe_agents` and returned alongside a `checkin_url`.
- **Check-in.** The probe posts `POST /api/probes/checkin` (Bearer probe token) every 5 min with
  its running image version + self-updater flag, and receives the fleet **target** to converge on.
  The version is baked into the image at build (`/etc/argus-probe.version`).
- **Target.** Argus holds a dashboard-settable target in `app_meta` (`GET`/`PUT /api/probes/target`,
  admin): `latest`, or an exact pin in the **decoupled probe scheme** - `7.0.29-r1`, *not* app
  semver (see the probe-image versioning in `deploy/README.md`). `/api/proxies` reports each
  probe's version / target / `update_status` (`unknown | tracking | current | outdated`).
- **Manual path (always available).** Drifted probes surface in the Probes view with a one-click
  `docker pull … && docker restart …` command - no Docker socket involved.
- **Dashboard-triggered self-update (v0.4.9; `docker run`, no compose).** A probe given the Docker
  socket + `ARGUS_PROBE_SELFUPDATE=1` reports itself self-update-capable; the Probes view shows an
  **Update now** button that queues the target (`POST /api/probes/{name}/update`), handed to the
  probe once at its next check-in as `{"update":"<tag>"}`. The probe can't `rm -f` itself, so it
  spawns a short-lived **argus-updater** sister in `probe-recreate` mode that clones the proxy's
  config onto the new image via the Docker Engine API (Dockhand-style), **rolling back to the old
  container on any failure**. Socket lives on the proxy in this mode (the tradeoff for no sidecar);
  opt-in only.
- **Socket-holding sidecar, no compose (v0.4.30; recommended for `docker run` / Dockhand).** The
  shared **argus-updater** image in `probe-watch` mode runs as a tiny per-proxy sidecar that holds
  the socket and recreates the proxy via the Engine API on a dashboard "Update now" or a fleet-target
  change - so the **proxy container never gets the socket** (the same principle as the core). It
  reports self-update capability on the proxy's behalf and reads the proxy's check-in credential from
  the proxy's data volume (`-v <data>:/probe:ro`); the proxy keeps reporting its own version.
  Deploy: `-e ARGUS_UPDATER_MODE=probe-watch -e ARGUS_PROXY_CONTAINER=<name>` + the socket. **Off
  unless you deploy the sidecar.**
- **Self-updater (opt-in compose sidecar).** For compose deployments, the shared **argus-updater**
  image runs in `probe-poll` mode (`ARGUS_UPDATER_MODE=probe-poll`), converging the proxy via
  `docker compose up -d proxy` (keeps the compose `.env` authoritative). Shipped as
  `deploy/probe-image/docker-compose.yml`; **off unless you deploy the sidecar**. The "Compose +
  auto-update" tab of the Add-probe wizard generates it.
- **One updater for all of them (v0.4.30).** The core self-updater and every probe path above are the
  same image, `ghcr.io/g-guglielmi/argus-updater` (its own version line), sharing one recreate engine
  (`lib/recreate.sh`) selected by `ARGUS_UPDATER_MODE` (`core` | `probe-recreate` | `probe-watch` |
  `probe-poll`) - so pull → config-clone → verify → rollback can never drift. The probe image no
  longer bundles its own `updater.sh`/`recreate.sh`; it just spawns the updater image. **Two-reporter
  model:** a socket-less proxy reports its version but omits self-update capability, while the sidecar
  advertises capability but reports no version - the check-in fields are sticky (an omitted field
  keeps the stored value), and the one-shot "Update now" is handed only to a capability-advertising
  caller, so the two never clobber each other or race for the update. See the
  [argus-updater](https://github.com/g-guglielmi/argus-updater) repo.

**Tradeoff acknowledged.** Any automatic in-place container update needs Docker socket access at
the site (the same mechanism Watchtower uses). The win over Watchtower is **central version control
+ fleet visibility + no third-party container + you decide when**. The socket is isolated to the
minimal **updater sidecar**, never the proxy. Unraid/`docker run` deployments use the manual
one-click path (or their own auto-update); the compose sidecar is the hands-off route.

---

## 19. Parking lot / future
- Public status page (Uptime-Kuma-style shareable page).
- **Android native app** with push notifications (device registers with Argus → notifier delivers
  via a "push"/FCM channel) - the planned last step (ROADMAP §I). iOS undecided (would need APNs).
- Escalation policies / repeat notifications beyond flap debounce.
- Token-based enrollment service (Phase 1 backend) + "Add probe" wizard (Phase 4/6 UI).
- Golden probe **VM template** (Packer) + cloud-init for scaled/work rollout (Phase 6).
- Sizing pass before the ~6000-sensor work deployment.
```

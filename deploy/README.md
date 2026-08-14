# Phase 0 — Deployment Kit (Foundations)

Goal of Phase 0: stand up the **core** (Zabbix server + web + PostgreSQL/TimescaleDB) and
**one probe** (site1) that connects to the core over **mutual TLS**, proving one device
flows end-to-end. Everything else builds on this.

> ⚠️ These scripts run on your Linux VM / unRAID, not on the machine Claude runs on.
> They are **reviewed starting points**, not tested-in-your-env artifacts. Read each one,
> adjust OS/version/paths, and we iterate as you run them.

## Architecture recap (see ../docs/DESIGN.md)

```
Probe (site)  --active mTLS-->  core:10051   (proxy INITIATES; no inbound at the site)
Core VM: zabbix-server + zabbix-web (API) + PostgreSQL+TimescaleDB
```

- **Active proxy** = the probe dials out to the core. Remote sites need only **outbound**
  to `core:10051`. The **core** is the only side that publishes an inbound port.
- **mTLS**: a small private CA signs one server cert (core) and one client cert per probe.

## Runbook (do in this order)

### 1. Generate the PKI  → `pki/gen-certs.sh`
Run once, anywhere with `openssl` (ideally on the core VM). Produces:
- `ca.crt` / `ca.key` — your monitoring CA (keep `ca.key` safe/offline).
- `zabbix-core.crt` / `.key` — server cert for the core.
- `proxy-<site>.crt` / `.key` — one client cert per site (site1, site2, site3, site4, site5).

Copy `ca.crt` + `zabbix-core.*` to the core; copy `ca.crt` + `proxy-<site>.*` to each probe.
**Never copy `ca.key` or other sites' keys to a probe.**

### 2. Stand up the core  → `core/setup-core.sh`
Debian 12 / Ubuntu 24.04 assumed (apt). Installs Zabbix 7.0 LTS, PostgreSQL 16 +
TimescaleDB, creates the DB, imports schema, enables Timescale compression/partitioning.
Then apply the TLS + tuning snippet: `core/zabbix_server.conf.snippet`.

After it's up:
- Zabbix web UI (admin engine room) on the VM — lock it to the private network / admin only.
- Publish **TCP 10051** to wherever probes will reach it (LAN via Site Magic today; a NAT/
  HAProxy TCP-passthrough rule for future no-VPN sites).

### 3. Register the site1 proxy in Zabbix
In the Zabbix web UI → **Data collection → Proxies → Create proxy**:
- Proxy name: `proxy-site1` (must match the probe's `ZBX_HOSTNAME`).
- Mode: **Active**.
- Encryption → **Connections from proxy: Certificate**; Issuer `CN=Monitoring Core CA`,
  Subject `CN=proxy-site1` (pins this exact probe).

### 4. Deploy the site1 probe  → `probe/run-probe.sh` (or unRAID XML)
- On the unRAID box: import `unraid/zabbix-proxy-site1.xml` via Community Applications
  → **Add Container**, fill in the core host + cert paths.
- Elsewhere (the Docker VM sites): `probe/run-probe.sh site1 core.example.lan`.

### 5. Prove the pipeline
Add one test host in Zabbix (e.g. the UniFi gateway IP) assigned to `proxy-site1`
with a single ICMP ping item. Confirm data arrives through the proxy. Then pull the
core's network briefly and confirm the proxy buffers + flushes on reconnect.

## Official docs vs. this script

They do the **same base steps** — the Zabbix installer page (repo → packages → DB → schema)
is now confirmed exact for Debian 13 / Zabbix 7.0. `setup-core.sh` automates those *and* adds
three things the basic doc flow does **not** cover: **TimescaleDB**, the **TLS config** for
proxies, and **retention** tuning.

Recommended: run `setup-core.sh` for the whole thing, but keep the official page open as the
reference. Two steps stay **manual either way** (neither the docs nor the script can finish them
headless):
- **Nginx**: uncomment `listen`/`server_name` in `/etc/zabbix/nginx.conf`, then restart nginx + php-fpm.
- **Frontend setup wizard** in the browser (DB connection, admin password, timezone).

If you'd rather follow the docs by hand for the base install, do that, then apply only the
TimescaleDB block from `setup-core.sh` + the `zabbix_server.conf.snippet`. Same result.

## Adding a new remote site — token enrollment from the Argus GUI (preferred)

Once Argus is running with the CA mounted (`ARGUS_CA_CERT_FILE` / `ARGUS_CA_KEY_FILE`), adding a
probe no longer needs `gen-certs.sh` or manual proxy registration:

**Prerequisites (one-time):**
- Mount the CA (`ca.crt` + `ca.key`) read-only into the Argus container and set the two
  `ARGUS_CA_*` paths; set the **probe core host** (the address probes dial for `:10051`) — either
  via `ARGUS_PROBE_CORE_HOST` or in **Settings → Probe enrollment** (no redeploy). Tip: a
  split-horizon DNS name that resolves to the core's LAN/mesh IP internally and the public IP
  externally lets every probe use one address. A probe can also override its baked-in core host
  with `-e ZBX_SERVER_HOST=…` on its `docker run` (handy to re-point one probe without re-enrolling).
- The Zabbix API token Argus uses must have **super-admin** rights (to run `proxy.create`).
- For remote sites with no VPN, publish **TCP 10051** on the core to the internet (HAProxy TCP
  passthrough / NAT), and make the `ghcr.io/<owner>/argus-probe` package public (or `docker login`).

**Per probe:**
1. In Argus → **Probes → Add probe**: enter the site name + token TTL. Argus mints a single-use
   token and shows a ready-to-deploy **`docker run`** *or* **unRAID XML template** for
   `argus-probe`, with the enroll URL + token filled in (the token is shown once).
2. On the site's Docker host, run that command (or import the XML on unRAID). On first boot the probe generates its own key +
   CSR, redeems the token (`/api/enroll`), receives its signed cert + `ca.crt`, and starts the
   proxy. **The private key never leaves the probe.** Certs persist on the mounted volume, so the
   single-use token isn't re-redeemed on restart.
3. The proxy appears **online** in the Probes list within a minute.

The manual flow below stays available as a fallback (e.g. before the CA is mounted).

### Updating a probe (automatic)

The probe runs `ghcr.io/<owner>/argus-probe:latest`. An update is a plain image pull + container
recreate — **no re-enrollment**: the signed cert + `ca.crt` live on the persistent
`/var/lib/zabbix` volume, and the entrypoint skips enrollment whenever those certs already exist
(so the single-use token is never needed again). Options:

- **Watchtower** (recommended, per probe host): run one alongside the probe and it pulls new
  `:latest` images and recreates the container with the same volume + env automatically:
  ```bash
  docker run -d --name watchtower --restart unless-stopped \
    -v /var/run/docker.sock:/var/run/docker.sock \
    containrrr/watchtower --cleanup --interval 3600 argus-probe
  ```
- **unRAID**: the built-in *CA Auto Update* plugin updates the container on a schedule.
- **Manual / cron**: `docker pull …/argus-probe:latest && docker rm -f argus-probe && docker run …`
  (same command you deployed with — the token env is harmless once certs exist).

Because the probe pins `:latest`, pushing a new `argus-probe` image to GHCR is enough for these to
pick it up. (Pin a specific `:vX.Y.Z` instead if you'd rather gate probe updates.)

### Security notes (before publishing Argus to the internet)

Enrollment was designed to be safe as a public endpoint, but mind these:

- **The enrollment token is a bootstrap secret.** It's 256-bit, single-use, time-limited, and
  revocable, and it only yields one proxy certificate for the site it's scoped to — but treat the
  generated `docker run` (which embeds it) like a password. Short TTLs are your friend.
- **`ARGUS_ENROLL_URL` must be HTTPS with a valid certificate.** The probe posts the token over it;
  `curl` verifies the cert (don't use `-k`). Terminate TLS at HAProxy with a real cert.
- **The CA private key is online** (mounted into Argus so it can sign). That's inherent to
  automated enrollment, but it means a compromise of the Argus host exposes the CA. To limit the
  blast radius, consider signing with an **intermediate CA** (root stays offline; Zabbix trusts the
  root; Argus holds only the intermediate — revoke/replace it without touching the root). The
  single-CA setup is fine to start; the intermediate is the hardening step for a public deployment.
- **Scope the Zabbix API token.** It needs super-admin for `proxy.create`; keep it Argus-only and
  rotate it if leaked (now easy from Settings).
- **Keep 10051 pinned.** The Zabbix server accepts proxies only by certificate issuer+subject, so
  exposing 10051 doesn't accept anonymous connections — but only publish it as far as remote sites
  need.

## Adding a new remote site later — manual (fallback)

The CA never changes — you only mint one new leaf:
1. `cd pki && ./gen-certs.sh <newsite>` — reuses the existing CA, leaves other certs untouched.
2. Copy `out/ca.crt` + `out/proxy-<newsite>.crt` + `out/proxy-<newsite>.key` to that probe (its key ONLY).
3. Deploy the probe: `probe/run-probe.sh <newsite> <core-host>` (or duplicate the unRAID XML, swapping the site name).
4. In the Zabbix UI: register active proxy `proxy-<newsite>`, certificate encryption,
   issuer `CN=Monitoring Core CA`, subject `CN=proxy-<newsite>`.

Never copy `ca.key` or another site's key to the probe.

## What Phase 0 deliberately does NOT include
- Auto-discovery / UniFi sweep (Phase 4) — here we hand-add one host to prove the path.
- The custom app / UI / notifier (Phases 1–5).
- The other 4 probes — clone steps 3–4 once site1 works.

## Files
| File | Purpose |
|---|---|
| `pki/gen-certs.sh` | Create CA + core cert + per-site probe certs |
| `core/setup-core.sh` | Install & configure Zabbix + PostgreSQL + TimescaleDB |
| `core/zabbix_server.conf.snippet` | TLS + DB + tuning settings for the server |
| `probe/run-probe.sh` | Parametrized `docker run` for a probe (active proxy, mTLS, 7-day buffer) |
| `unraid/zabbix-proxy-site1.xml` | unRAID Community Applications template for the site1 probe |
| `unraid/argus.xml` | unRAID Community Applications template for the Argus app (all env vars as fields) |
| `probe-image/Dockerfile` | Self-enrolling probe image (stock Zabbix proxy + first-boot enrollment) |
| `probe-image/entrypoint.sh` | Enrollment entrypoint: generate key/CSR, redeem token, write certs, start proxy |

## Troubleshooting / known issues

### TimescaleDB too new for Zabbix 7.0 (version regression)

**Symptoms**
- `zabbix-server` refuses to start; log shows:
  `Unsupported DB! timescaledb version 22901 is newer than 22899` /
  `TimescaleDB version is too new. Recommended version is up to TimescaleDB Community Edition 2.28.`
- Administration → Housekeeping shows: *"Unsupported TimescaleDB ... Should not be higher than 2.28."*
  and **compression cannot be enabled/managed** by Zabbix.

**Cause**
The TimescaleDB apt repo (packagecloud) ships ahead of what each Zabbix LTS certifies. On
this build the repo installed **2.29.1** while Zabbix **7.0.29** supports only up to **2.28.x**.
Zabbix gates on this: it won't manage native compression, and by default won't even start.

**Prevention (fresh installs)**
`setup-core.sh` now auto-selects the newest **2.28.x** TimescaleDB at install time, `apt-mark
hold`s it, **and** writes a priority-1001 APT pin (`/etc/apt/preferences.d/timescaledb-pin.pref`,
`Pin: version 2.28.*`), so new cores never hit this. `AllowUnsupportedDBVersions=1` remains in
`zabbix_server.conf.snippet` as a safety net (lets the server *run* on an unsupported version,
but Zabbix still won't manage compression until you're on 2.28).

**Why the pin as well as the hold.** A `hold` only stops `apt upgrade` / `full-upgrade`; an
explicit install or a GUI update manager (e.g. Linux Update Dashboard, PackageKit) can still
pull 2.29. The priority-1001 pin removes 2.29 as a candidate entirely, so *nothing* upgrades
past 2.28.* — even "Upgrade All". To add it to a box that predates this change:
```bash
sudo tee /etc/apt/preferences.d/timescaledb-pin.pref >/dev/null <<'PIN'
Package: timescaledb-2-*
Pin: version 2.28.*
Pin-Priority: 1001
PIN
apt-cache policy timescaledb-2-postgresql-17   # Candidate: should read 2.28.x, not 2.29
```

**Fix on a live box (what was done here — safe because the DB was empty).**
Downgrade TimescaleDB to the newest 2.28.x, pin it, then recreate the empty `zabbix` DB so the
extension is created at the supported version:
```bash
# 1. find newest supported 2.28.x (prints e.g. 2.28.3~debian13-1710)
TS_VER=$(apt-cache madison timescaledb-2-postgresql-17 | awk '{print $3}' | grep -E '^2\.28' | head -1); echo "$TS_VER"

# 2. stop server, downgrade + hold TimescaleDB, restart postgres
sudo systemctl stop zabbix-server && sudo apt-get install -y --allow-downgrades \
  timescaledb-2-postgresql-17=$TS_VER timescaledb-2-loader-postgresql-17=$TS_VER \
  && sudo apt-mark hold timescaledb-2-postgresql-17 timescaledb-2-loader-postgresql-17 \
  && sudo systemctl restart postgresql

# 3. recreate the empty DB + extension at 2.28
sudo -u postgres psql -c "DROP DATABASE zabbix WITH (FORCE);" \
  && sudo -u postgres createdb -O zabbix zabbix \
  && sudo -u postgres psql -d zabbix -c "CREATE EXTENSION IF NOT EXISTS timescaledb CASCADE;"

# 4. re-import Zabbix schema + Timescale hypertable conversion
zcat /usr/share/zabbix-sql-scripts/postgresql/server.sql.gz | sudo -u zabbix psql zabbix \
  && sudo -u zabbix psql zabbix -f "$(ls /usr/share/zabbix-sql-scripts/postgresql/timescaledb/schema.sql 2>/dev/null || ls /usr/share/zabbix-sql-scripts/postgresql/timescaledb.sql)"

# 5. start + verify (no "too new" line)
sudo systemctl start zabbix-server && sleep 3 && sudo tail -n 20 /var/log/zabbix/zabbix_server.log
```

**Aftermath — recreating the DB resets state stored in the DB:**
- Admin login goes back to `Admin` / `zabbix` → log in and change the password again.
- Per-user + system **timezone and theme** reset → User profile (theme/timezone) and
  Administration → General → GUI (system defaults).
- The frontend config file (`/etc/zabbix/web/zabbix.conf.php`) is untouched, so the DB
  connection and the `Monitoring` instance name survive.

Harmless output to ignore during the fix: `character varying ... does not follow best practices`
WARNINGs (Timescale hints), and any old-kernel `autoremove` note.

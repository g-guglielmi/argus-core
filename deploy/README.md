# Phase 0 - Deployment Kit (Foundations)

> **Fastest path:** the self-installing **core appliance VM** ([`core-vm/`](core-vm/README.md))
> does everything in this runbook - Zabbix + database + PKI + Argus - through one first-boot
> form. This kit is the **manual alternative** for a distro of your choice, an existing Zabbix,
> or a split Zabbix/Argus layout.

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
- `ca.crt` / `ca.key` - your monitoring CA (keep `ca.key` safe/offline).
- `zabbix-core.crt` / `.key` - server cert for the core.
- `proxy-<site>.crt` / `.key` - one client cert per site (site1, site2, site3, site4, site5).

Copy `ca.crt` + `zabbix-core.*` to the core; copy `ca.crt` + `proxy-<site>.*` to each probe.
**Never copy `ca.key` or other sites' keys to a probe.**

### 2. Stand up the core  → `core/setup-core.sh`
Debian 12 / Ubuntu 24.04 assumed (apt). Installs Zabbix 7.0 LTS, PostgreSQL 16 +
TimescaleDB, creates the DB, imports schema, enables Timescale compression/partitioning.
Then apply the TLS + tuning snippet: `core/zabbix_server.conf.snippet`.

It also sets up **OS patching** (DESIGN §14c): `unattended-upgrades` (security suite only — it respects
the TimescaleDB 2.28 hold) + `needrestart`, with the core's **reboot left to you** (Argus schedules it
in **Settings → OS updates**; default is notify-only). A host reporter writes the core's patch status
into `ARGUS_STATE_DIR` (default `/opt/argus/update`) — set that to the **same host path** you map as the
Argus container's `ARGUS_UPDATE_DIR`, so the core shows its own status and the chosen reboot window is
honoured locally. Probe VMs patch + reboot themselves (weekly ~03:00) and report status the same way.

> On an **already-running core**, don't re-run the whole installer just for this — run the standalone
> `core/setup-core-patching.sh` instead: `sudo ARGUS_STATE_DIR=/docker/argus-update ./setup-core-patching.sh`
> (the host path bound to the core container's `/update`). It installs only the patching + reporter bits.

After it's up:
- Zabbix web UI (admin engine room) on the VM - lock it to the private network / admin only.
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

They do the **same base steps** - the Zabbix installer page (repo → packages → DB → schema)
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

## Adding a new remote site - token enrollment from the Argus GUI (preferred)

Once Argus is running with the CA mounted (`ARGUS_CA_CERT_FILE` / `ARGUS_CA_KEY_FILE`), adding a
probe no longer needs `gen-certs.sh` or manual proxy registration:

**Prerequisites (one-time):**
- Mount the CA (`ca.crt` + `ca.key`) read-only into the Argus container and set the two
  `ARGUS_CA_*` paths; set the **probe core host** (the address probes dial for `:10051`) - either
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

### Migrating a manually-enrolled proxy to `argus-probe`

A proxy you registered by hand (Phase-0 `gen-certs.sh` + the stock `zabbix-proxy` container) keeps
working - migration is **optional**, only to standardize on the self-enrolling image. It's safe
because enrolling an **existing proxy name** does a `proxy.update`, not a create, so the Zabbix
proxy record, its assigned hosts, and history are **preserved** (same `proxyid`). Steps, per site:

1. In Argus → **Probes → Add probe**, use the **same site name** so the proxy name matches exactly
   (e.g. site `mybz` → `proxy-mybz`). Copy the `docker run` / unRAID XML.
2. On the site host, **stop and remove the old** manual proxy container
   (`docker rm -f zbx-proxy-<site>`). Use a **fresh volume** for the new one (don't reuse the old
   `/var/lib/zabbix`), so it enrolls cleanly.
3. Run the new `argus-probe` container. It generates a new key + CSR, gets a fresh cert signed for
   `proxy-<site>`, and Argus **updates** the existing proxy (same issuer/subject pin) - the probe
   reconnects as the same proxy.
4. Confirm it's **online** in Probes. Then delete the old cert files (`ca.crt`, `proxy-<site>.*`)
   from the host - the new probe made its own.

There's a brief collection gap while you swap containers (two containers can't share one proxy
name at once). The old cert stays valid but unused; you can leave it or clean it up.

> **Storage note — two binds, no anonymous volume.** The stock Zabbix proxy image declares
> `/var/lib/zabbix/snmptraps` as a `VOLUME`, which Docker fills with an **anonymous volume** unless
> that exact subpath is mounted. The generated commands/templates therefore bind **both**
> `…/<probe>:/var/lib/zabbix` **and** `…/<probe>/snmptraps:/var/lib/zabbix/snmptraps`, so everything
> lands in your appdata folder and no stray volume is created. (A child image can't un-declare a
> parent's `VOLUME`, so binding the subpath is the fix.)

### Updating a probe (automatic)

The probe runs `ghcr.io/<owner>/argus-probe:latest`. An update is a plain image pull + container
recreate - **no re-enrollment**: the signed cert + `ca.crt` live on the persistent
`/var/lib/zabbix` volume, and the entrypoint skips enrollment whenever those certs already exist
(so the single-use token is never needed again). Options:

- **Watchtower** (recommended, per probe host): run one alongside the probe and it pulls new
  `:latest` images and recreates the container with the same volume + env automatically:
  ```bash
  docker run -d --name watchtower --restart unless-stopped \
    -v /var/run/docker.sock:/var/run/docker.sock \
    containrrr/watchtower --cleanup --interval 3600 argus-proxy-<site>
  ```
- **unRAID**: the built-in *CA Auto Update* plugin updates the container on a schedule.
- **Manual / cron**: `docker pull …/argus-probe:latest && docker rm -f argus-proxy-<site> && docker run …`
  (same command you deployed with - the token env is harmless once certs exist).

Because the probe pins `:latest`, pushing a new `argus-probe` image to GHCR is enough for these to
pick it up. (Pin a specific tag instead if you'd rather gate probe updates - see below.)

### Fleet updates - Argus-coordinated (v0.4.8)

Argus is a **control plane** for probe versions: it holds a **fleet target** (Probes → *Fleet
target version*: `latest` or an exact pin like `7.0.29-r1`) and shows each probe's running version
against it. Every probe self-reports its baked-in version every ~5 min over an outbound-only
check-in (a long-lived token issued at enrollment) - nothing inbound is opened.

- **Visibility + manual update (any deployment).** The Probes view flags drift and offers a
  one-click `docker pull … && docker restart argus-<proxy>` for each outdated probe. No Docker
  socket involved. Version is shown even for probes that never check in (read from Zabbix).
- **Two containers: proxy + updater sidecar (the one self-update model).** Every Argus-driven probe
  is the proxy plus the shared **[argus-updater](https://github.com/g-guglielmi/argus-updater)** image
  in `probe-watch` mode as a sidecar. The sidecar holds the socket and recreates the proxy via the
  Docker Engine API on an **Update now** or a fleet-target change (rolling back if the new one fails),
  so **the proxy container never gets the socket** — the same principle as the core's updater. The
  Add-probe wizard's **Docker run** and **Compose** tabs both emit the two containers. Deploy the
  sidecar by hand next to a `docker run` proxy:
  ```
  docker run -d --name <proxy>-updater --restart unless-stopped \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v <proxy-data-dir>:/probe:ro \
    -e ARGUS_UPDATER_MODE=probe-watch -e ARGUS_PROXY_CONTAINER=<proxy-container-name> \
    ghcr.io/g-guglielmi/argus-updater:latest
  ```
  `<proxy-data-dir>` is the proxy's `/var/lib/zabbix` host path (holds the enrollment + check-in
  credential). Sidecar env: `ARGUS_UPDATE_INTERVAL` (poll seconds, default 300).
- **Updating the updater itself.** The sidecar can recreate itself too (via an ephemeral
  `probe-recreate` copy): the **⟳** control next to a probe's **auto** tag queues it, or your platform
  (Dockhand / `docker compose pull` / the VM's systemd unit) updates the small image. Its version
  shows on the **auto** tag's tooltip.
- **VM probes** run the same two containers as two systemd units (`argus-probe` + `argus-updater`),
  installed by the golden image and enabled together at enrollment - so a VM probe is Argus-managed
  like any other. See the argus-probe `deploy/probe-vm` README.
- **unRAID:** keep unRAID's native auto-update as the updater there (no sidecar app); Argus shows the
  installed version (from Zabbix) and the fleet target so you can see drift.

**Turning on exact version reporting for an older probe.** A probe enrolled before fleet updates has
no check-in token (enrollment mints it, and enrollment is skipped once the certs exist), so it won't
report its exact `-rN` version even after you update the image - it only shows the Zabbix version.
Fix it without re-enrolling: in **Probes**, click **Enable reporting** on that probe, then add the
one env var it gives you to the container (unRAID: *Edit → Add another variable*) and restart:
```
ARGUS_PROBE_TOKEN=<the token shown once>
```
The probe derives the check-in URL from its existing `ARGUS_ENROLL_URL`, so that single variable is
all it needs. On first boot it **saves the token to the data volume**, so you can **remove the env
var on a later run** and reporting keeps working (a fresh `ARGUS_PROBE_TOKEN` always wins, in case
you re-mint it).

The win over plain Watchtower/unRAID auto-update is **central pinning + fleet visibility + you decide
when a change rolls out**, with no third-party updater container.

**Versioning & tracking upstream Zabbix automatically.** The `argus-probe` image is a thin wrapper
over `zabbix/zabbix-proxy-sqlite3:alpine-7.0-latest`, and that base is baked in at *our* build time -
so probes only see new Zabbix once we rebuild + publish. It is versioned by the **Zabbix version it
ships plus a revision for our own wrapper edits** - `probe/v<zabbix>-r<n>` (e.g. `probe/v7.0.29-r1`,
then `-r2` for a wrapper fix, then `7.0.30-r1` when Zabbix bumps). This is **decoupled from the app's
`vX.Y.Z` semver**: an app-only release never rebuilds or re-tags a probe.

A single CI job ([`probe-image.yml`](https://github.com/g-guglielmi/argus-probe/blob/main/.github/workflows/probe-image.yml),
in the **argus-probe** repo) owns that lifecycle from two triggers, with no manual step:

- **Upstream watch (daily schedule):** checks the base image; when its **digest** moves it rebuilds
  and pushes the rolling tags `:latest` + `:zabbix-<ver>` (absorbing even same-version Alpine/security
  rebuilds). It cuts a Release only when the **Zabbix version number** actually changes (→ `-r1`).
- **Wrapper change (push to `deploy/probe-image/**`):** rebuilds and cuts a revision Release
  (`-r<prev+1>`) against the same Zabbix version.

Your probe hosts pick any of these up through the same Watchtower / unRAID / manual path above.

- **Gate updates by version:** pin `ghcr.io/<owner>/argus-probe:zabbix-7.0.29` (newest wrapper for
  that Zabbix line) or `:7.0.29-r1` (fully immutable) instead of `:latest`, and bump deliberately.
- **Major upgrades stay manual:** moving to a new Zabbix major/minor (7.2, the next LTS 8.0, …) is a
  deliberate `FROM` bump in `deploy/probe-image/Dockerfile`, done in lockstep with upgrading the
  Zabbix **server** (the proxy must match the server's major.minor). The watcher never does that.

### Security notes (before publishing Argus to the internet)

Enrollment was designed to be safe as a public endpoint, but mind these:

- **The enrollment token is a bootstrap secret.** It's 256-bit, single-use, time-limited, and
  revocable, and it only yields one proxy certificate for the site it's scoped to - but treat the
  generated `docker run` (which embeds it) like a password. Short TTLs are your friend.
- **`ARGUS_ENROLL_URL` must be HTTPS with a valid certificate.** The probe posts the token over it;
  `curl` verifies the cert (don't use `-k`). Terminate TLS at HAProxy with a real cert.
- **The CA private key is online** (mounted into Argus so it can sign). That's inherent to
  automated enrollment, but it means a compromise of the Argus host exposes the CA. To limit the
  blast radius, consider signing with an **intermediate CA** (root stays offline; Zabbix trusts the
  root; Argus holds only the intermediate - revoke/replace it without touching the root). The
  single-CA setup is fine to start; the intermediate is the hardening step for a public deployment.
- **Scope the Zabbix API token.** It needs super-admin for `proxy.create`; keep it Argus-only and
  rotate it if leaked (now easy from Settings).
- **Keep 10051 pinned.** The Zabbix server accepts proxies only by certificate issuer+subject, so
  exposing 10051 doesn't accept anonymous connections - but only publish it as far as remote sites
  need.

## Adding a new remote site later - manual (fallback)

The CA never changes - you only mint one new leaf:
1. `cd pki && ./gen-certs.sh <newsite>` - reuses the existing CA, leaves other certs untouched.
2. Copy `out/ca.crt` + `out/proxy-<newsite>.crt` + `out/proxy-<newsite>.key` to that probe (its key ONLY).
3. Deploy the probe: `probe/run-probe.sh <newsite> <core-host>` (or duplicate the unRAID XML, swapping the site name).
4. In the Zabbix UI: register active proxy `proxy-<newsite>`, certificate encryption,
   issuer `CN=Monitoring Core CA`, subject `CN=proxy-<newsite>`.

Never copy `ca.key` or another site's key to the probe.

## What Phase 0 deliberately does NOT include
- Auto-discovery / UniFi sweep (Phase 4) - here we hand-add one host to prove the path.
- The custom app / UI / notifier (Phases 1-5).
- The other 4 probes - clone steps 3-4 once site1 works.

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
past 2.28.* - even "Upgrade All". To add it to a box that predates this change:
```bash
sudo tee /etc/apt/preferences.d/timescaledb-pin.pref >/dev/null <<'PIN'
Package: timescaledb-2-*
Pin: version 2.28.*
Pin-Priority: 1001
PIN
apt-cache policy timescaledb-2-postgresql-17   # Candidate: should read 2.28.x, not 2.29
```

**Fix on a live box (what was done here - safe because the DB was empty).**
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

**Aftermath - recreating the DB resets state stored in the DB:**
- Admin login goes back to `Admin` / `zabbix` → log in and change the password again.
- Per-user + system **timezone and theme** reset → User profile (theme/timezone) and
  Administration → General → GUI (system defaults).
- The frontend config file (`/etc/zabbix/web/zabbix.conf.php`) is untouched, so the DB
  connection and the `Monitoring` instance name survive.

Harmless output to ignore during the fix: `character varying ... does not follow best practices`
WARNINGs (Timescale hints), and any old-kernel `autoremove` note.

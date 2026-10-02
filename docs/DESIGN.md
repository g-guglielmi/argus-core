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
- **CSRF:** the session cookie is `SameSite=Lax` (other sites can't send signed-in requests), and on
  top of that every state-changing `/api/*` request from a browser is checked, list or no list: a
  `Sec-Fetch-Site: cross-site` is refused, an `Origin` must be one of Argus's own (the allow-list, the
  Public URL, or the host the request was addressed to), and a body must be `application/json` (so a
  cross-site `text/plain` form can't post a JSON-shaped login). The probes' machine endpoints, the push
  sensor endpoints (`/api/push/...`: a job's report, authenticated by its URL's token, and a host's
  template read, by the host's key) and the signed acknowledge form are exempt (they carry no session). **Allowed FQDNs and IPs** (Settings,
  `ARGUS_TRUSTED_ORIGINS`) adds the Host allow-list on top, e.g. `monitoring.example.com` **+ the
  private IP**: with a list set, `/api/*` requests for any other `Host` are refused (DNS rebinding).
  Off until configured, so an upgrade can't lock anyone out; the Public URL host and loopback are
  always allowed; a save that would lock out the admin making it is refused;
  `ARGUS_TRUSTED_ORIGINS=*` is the recovery switch. Behind a trusted proxy the host comes from
  `X-Forwarded-Host`.
- **Response headers:** every answer carries `X-Content-Type-Options: nosniff`, `X-Frame-Options:
  DENY`, `Referrer-Policy: same-origin` and a `Permissions-Policy`; the app pages a
  Content-Security-Policy (`script-src 'self'`, no inline scripts, hence `theme.js` as a file;
  `style-src` allows inline for React's style props; `img-src` allows `data:`/`blob:` for chart PNGs,
  the TOTP QR and downloads; `frame-ancestors 'none'`); `/api/*` answers `Cache-Control: no-store`.
  HSTS is the TLS proxy's job (Argus never knows it's behind TLS).
- **Cookies:** `argus_session` is HttpOnly, `SameSite=Lax`, `Path=/`, `Secure` when
  `ARGUS_COOKIE_SECURE` says so or, unset, when the Public URL is https; secure, it is named
  `__Host-argus_session` (a browser only accepts that name from a Secure, `Path=/`, domain-less
  setter, so plain-HTTP pages and sibling subdomains can't plant one). Sessions end at once when the
  user is disabled, when an admin resets their password, and (all but the current one) when they
  change their own.
- **Second factor:** the password step returns a 10-minute challenge, `/api/login/totp` completes it;
  a TOTP code is accepted once (the last accepted time step is stored per user). A passkey logs in
  without the TOTP prompt, so user verification (PIN, biometric) is **required** at registration and
  at every login, and an assertion whose signature counter didn't advance (a cloned credential) is
  refused. Adding a passkey takes the password again; turning TOTP off takes the password and a
  current code.
- **Links that leave Argus** (reset emails, the enroll command shown to admins) are built from the
  Public URL, else from the request's host only when that host is allow-listed or loopback; a probe's
  check-in URL comes from the Public URL alone (probes write it into their own configuration, and
  derive it from their enroll URL when there is none). "Probe core host" is validated as `host[:port]`.
- **Secrets at rest** are AES-256-GCM under `ARGUS_SECRET_KEY` (stretched into the key with argon2id,
  so a passphrase can't be guessed from a database dump at hashing speed; a database written under
  the earlier SHA-256 derivation is re-encrypted once at start) or a generated `secret.key` on the
  data volume. A canary (`app_meta.cipher_canary`) is checked at start: a key
  that doesn't open it stops Argus with a clear message, unless `ARGUS_SECRET_KEY_RESET=true`, which
  drops every unreadable secret once (channel/SNMP/UniFi credentials, break-glass passwords,
  status-page link copies, the alert signing key, encrypted settings; TOTP is switched off for the
  users affected). A corrupt keyfile is reported, never replaced. An unreadable value decrypts to
  nothing, never to its ciphertext. Channel credentials (SMTP password, bot token, webhook) are
  write-only through the API (`<key>_set` flags; blank keeps); the SNMP community is blank for the
  viewer role.
- **Outbound:** Discord webhooks are accepted only on Discord's hosts under `/api/webhooks/`, Slack
  webhooks only on `hooks.slack.com` under `/services/`, Teams webhooks only on the Workflows hosts
  (`*.logic.azure.com`, `*.api.powerplatform.com`, the older `*.webhook.office.com`), all over https,
  and the sender never follows redirects. Anyone signed in can save a personal channel, so its address
  must not be able to point inside the network: a personal channel is one of the public services
  (Telegram, Discord, Teams, Slack, ntfy over https, Pushover) and connects only to public internet
  addresses, checked when the connection is made (after DNS, so a name that resolves to a private,
  loopback, link-local or CGNAT address is refused too), directly rather than through a proxy. A
  generic webhook, Gotify or a self-hosted ntfy on the LAN is a global channel, which only admins set
  up. Bot tokens, webhook tokens and Teams signatures are redacted from transport errors, and a
  webhook's or server's path is cut from them, before they are logged, stored as a channel's health
  line or sent as a system notice.
- **Trusted proxies** (`ARGUS_TRUST_PROXY`, Settings -> Reverse proxy): empty = none (socket address
  is the client, forwarded headers ignored); `true` = one proxy on a private or loopback address (the
  client is the LAST `X-Forwarded-For` entry, the one that proxy appended; a public peer is a direct
  client whatever it sends); or a list of proxy addresses / networks:
  forwarded headers count only on a connection from one of them, and the client is found by walking
  `X-Forwarded-For` (every copy of the header) from the right past listed proxies, so a chain like
  NetScaler -> HAProxy -> Argus resolves to the real client. Entries left of the first untrusted
  address are the client's to forge and never believed, and a forwarded value that isn't an address
  falls back to the peer. Feeds the login rate limit, status pages' allowed networks, Allowed FQDNs
  and IPs (`X-Forwarded-Host`) and reset links (`X-Forwarded-Proto`).
- **Sign-in throttling** counts failures per address and per account; the account counter never locks
  a user out (over the limit a wrong password gets 429 instead of 401, the right one still signs in).
  Anonymous passkey-login begins are capped per address, argon2 runs are capped at eight at a time,
  and the limiter bounds what it keeps (long keys hashed, a hard ceiling on tracked keys).
  Second-factor challenges, reset links and passkey ceremonies are redeemed in the statement that
  deletes them, so a race yields one session. Zabbix RPC errors reach non-admin users without their
  `data` part. A check-in token is issued only for a proxy Zabbix knows. CI runs `govulncheck` and
  `npm audit` on the production dependencies before any image is built, and the Go toolchain is the
  pinned image's own.
- **Passkey caveat (accepted):** WebAuthn RP IDs must be a domain, not a bare IP.
  → Passkey login works via `monitoring.example.com`; direct **private-IP** access
  (troubleshooting) falls back to **password + MFA**.

---

## 5. Device classes & templates

Every host gets the **Base** template (Ping - latency + loss, always) and, optionally, the
**HTTP/HTTPS** add-on (`Argus HTTP Endpoint`: `argus_http.py` on the probe fetches every URL in
`{$HTTP.URLS}` at once - full URLs, hosts without a scheme (given `{$HTTP.SCHEME}`), or paths on the
host's address with `{$HTTP.SCHEME}` / `{$HTTP.PORT}`, a blank list being the host itself, each with
options after `#` (`tls=`, `text=`, `notext=`; host settings edit the list as rows, each naming its
`tls=`, show the add-on's scheme, port and certificate fields only while something uses them, and a URL's id
is its URL alone, so changing its options keeps its history) - one sensor group per URL under **Web**: up when it
answers with an accepted status code after redirects (`{$HTTP.EXPECT}`), has the text a `#text` /
`#!text` suffix asks for, and presents an accepted certificate (`{$HTTP.TLS.VERIFY}`: `verify` a known
CA's, `self-signed` also one no CA vouches for - the collector reads the certificate itself (validity,
subject alternative names, else the common name) instead of asking the TLS library, which refuses a
device chain whose own "CA" isn't marked as one, so its dates still count, and its name for a URL by
name (not by IP: device certificates rarely list their address) - `ignore` any, and doesn't read it);
the response time (a URL whose certificate is refused is down for it, but its page is still asked
for over the unchecked connection, so its response time and status keep coming); for https the
certificate's days left, read even when untrusted, in a second LLD
rule so plain http URLs and `ignore` ones have none (alerts: expires soon, very soon, has expired); why it isn't up as the reason, and a list the collector refuses
makes the URL sensors unsupported with why - attachable to any host, not a class), the **DNS
resolution** add-on and the **TCP ports** add-on
(`Argus TCP ports`: `argus_tcp.py` on the probe connects to every port in `{$TCP.PORTS}` at once,
one sensor group per port under **TCP** with its connect time, reachability as the Downtime band and
why it doesn't answer as the reason; add-on values are checked against a pattern before they reach
the collector's command line). **Push sensors** (`Argus Push`, docs/push-sensors.md) are a job's
own reports: the job calls its push sensor's secret URL (`/api/push/{token}`, ok or fail and a
message), Argus keeps the last run (`push_sensors`), and the template, which Argus links with a host's
first push sensor and unlinks with its last, reads the host's push sensors back once a minute through
the host's own proxy (a SCRIPT item on `{$PUSH.URL}` = `/api/push/host/{id}` with the host's secret
`{$PUSH.KEY}`, stored hashed; dependent LLD carries each sensor's late and missed times as
`{#PUSH.LATE}` / `{#PUSH.MISSED}`). A trapper item can't do it: Zabbix accepts a pushed value for a
host behind a proxy only through that proxy, which the core can't reach. When Argus can't be read,
every push sensor shows why. On top, one or more **class templates** attach - manually
in the first cut (§C), automatically by fingerprint once discovery lands (§8, §B). Templates are
hand-authored Zabbix YAML, version-controlled under `argus/internal/provision/templates/` (in-module
so they can be embedded) and imported into zabbix-server via `configuration.import` (Argus reconciles
the set on startup, so templates track the app version). Thresholds are Zabbix **user-macros** carrying the §6 defaults, overridable
per-host/sensor (§D).

**Fleet reality:** 400+ servers with **SNMP already configured**; deploying an agent that widely
is impractical, so classes are **SNMP-first** wherever the device supports it. Agents/vendor APIs
are used only where SNMP genuinely can't reach the data (per-VM state, PoE, app-level metrics).

Each class is built by one of a few **patterns** (build the pattern once, replicate):
- **SNMP** - SNMP template + `sysObjectID`/`sysDescr` fingerprint + IF-MIB interface LLD. The bulk.
- **HTTP-API** - Zabbix HTTP-agent items (proxy-executed, no custom code): a master item pulls the
  vendor REST JSON, dependent items via JSONPath, LLD over the JSON. UniFi, Nutanix, Citrix farm.
- **Native VMware** - Zabbix's built-in VMware collector + stock templates. vSphere only.
- **Collector/script** - the true SNMP-gaps Zabbix can't HTTP-agent cleanly: XCP-NG (XAPI),
  NUT (upsd :3493). A probe-side sidecar or Zabbix script item.
- **Agentless** - server/proxy-run checks with no agent on the device: DNS (`net.dns`), Linux-over-SSH
  (a one-login-per-poll collector, `argus_linux_ssh.py`, that reads /proc + df in a single session).
- **Agent** - a Zabbix agent runs *on* the device (in a container) and the proxy polls it passively.
  The carve-out for a Docker-capable host that speaks no SNMP: Ugreen UGOS.

**Two host models.** Most classes are **per-host** (one pingable device = one Zabbix host). A few
are **API-endpoint sources** (✦): you register one endpoint (vCenter, Prism, Citrix Monitor, a
UniFi controller) and host-prototype **LLD spawns** the child hosts it manages.

**Tree glyph.** Each class carries an `Icon` (server, switch, shield, router, wifi, nas, cloud,
globe, battery, device) that drives its leading icon in the sites tree; groups render as folders,
hosts as their class icon with a small health-coloured status badge. A host with no class (the
pre-§C fleet) falls back to a best-effort guess from its name, else a generic device.

| Class | Pattern | Detected by | Metrics beyond Ping | LLD |
|---|---|---|---|---|
| **Linux (SNMP)** | SNMP | host-resources / UCD | CPU, RAM, disk, net, uptime | fs, NICs |
| **HPE Aruba CX** | SNMP | sysObjectID (Aruba/HPE) | CPU, mem, temp, PSU/fan, per-port, PoE | ifaces, sensors |
| **Aruba InstantOn 1960** | SNMP | sysObjectID | CPU, mem, per-port traffic, PoE | ports |
| **Sophos XGS** | SNMP | sysObjectID `.2604` | CPU, mem, disk, ifaces, HA, live users, VPN | ifaces |
| **Citrix NetScaler** | SNMP (+Nitro opt.) | sysObjectID `.5951` | CPU, mem, throughput, vserver state/health, SSL, HA | vservers |
| **QNAP** | SNMP | sysObjectID `.24681` | CPU, mem, volume/disk, temp, fan, RAID, SMART | disks, volumes |
| **Ugreen UGOS** ✓ | Agent † | agent2 in Docker on the NAS | CPU, RAM, disk, net, uptime + CPU temp + no-wake per-disk SMART temps (20 °C parked sentinel) - **v0.4.50** | fs, NICs, disks |
| **unRAID** | SNMP | sysDescr `Unraid` | CPU load, RAM %, uptime, per-share free, NIC; disk + CPU temp via optional NET-SNMP extends (setup: [docs/hosts/unraid.md](hosts/unraid.md)) | shares, disks, NICs |
| **Libraesva ESG** | SNMP (+HTTPS) | sysObjectID/sysDescr | host CPU/RAM/disk + mail-queue + admin-cert | fs |
| **Windows server** | SNMP | sysObjectID (Windows) | CPU, RAM, disk, net, uptime + **selected services** (LANMGR `svSvcTable`; opt-in via `{$WIN.SERVICE.MATCHES}`, set at add time or in host settings) | disks, NICs, services |
| **Linux (SSH)** | Agentless | SSH login (no-SNMP fallback) | CPU (+ iowait and steal), load, RAM, uptime, disk, net via `argus_linux_ssh.py` (one login/poll; key or password auth); failed systemd units (any unit, `systemctl list-units --state=failed`, the names as the reason; only where systemd runs); opt-in systemd units (`{$SSH.UNITS}`) and Docker containers (`{$SSH.CONTAINERS}`, needs `docker ps` rights), each Running / Down with its state as the reason | fs, NICs, units, containers, systemd |
| **DNS server** (incl. **AdGuard**) | Collector (+HTTP-API) | :53 + admin | per-name resolve (success/time/IP) via `dns-resolver.py`; AdGuard stats via its API | names |
| **UPS (NUT via PeaNUT)** | HTTP-API | PeaNUT :8080 → upsd | battery %, on-battery/low-battery, runtime, load, input V, power draw | - |
| **Home Assistant** | HTTP-API | :8123 REST + token | API up, version, integrations, entity count, unavailable entities | - |
| **UniFi Switch** ✦src | HTTP-API | UniFi controller | uptime, CPU/mem, per-port traffic + PoE, clients | ports |
| **UniFi Gateway** ✦src | HTTP-API | UniFi controller | + WAN up/down + throughput | ports, WANs |
| **UniFi AP** ✦src | HTTP-API | UniFi controller | + per-radio traffic, clients, channel util | radios |
| **UniFi OS Console** ✦ | HTTP-API | the controller host | console CPU/mem/temp/disk, adoption count, version | - |
| **Nutanix AHV** ✦ | HTTP-API | Prism v3/v4 REST | cluster/host/VM CPU/mem/storage, VM state | hosts, VMs |
| **Hyper-V** | SNMP | sysObjectID (Windows) | host CPU/RAM/disk/net/uptime; **per-VM state = gap (WMI/agent)** | disks, NICs |
| **XCP-NG** | Collector | XAPI on the pool master | pool (HA, members live, VM counts), per-hypervisor CPU/mem/uptime/version (+ temp via optional argus-temp dom0 plugin); opt-in per-VM state / CPU / mem / disk+net I/O ({$XCP.VM.MODE}) | hypervisors, VMs |
| **vSphere ESXi + vCenter** ✦ | Native VMware | register vCenter | hypervisor CPU/mem, datastore, per-VM state/CPU/mem | hypervisors, VMs, datastores |
| **Citrix farm** ✦ | HTTP-API | Monitor OData | registered-machine count/state, **failed logons**, sessions, load | delivery groups |

✦src = per-host today, but its metrics come *through* the registered UniFi controller (an API
source; **both** self-hosted Network controllers **and** cloud gateways are in the fleet, so the
UniFi template carries a controller-access mode). ✦ = API-endpoint source (register once → LLD
spawns children). † Ugreen UGOS exposes **no SNMP**, so it's monitored with **Zabbix agent 2 run
in a container on the NAS** (host network, `/proc` + `/sys` + `/` mounts, smartmontools for SMART);
the site proxy polls it passively on `:10050` and the agent's `Server=` allow-lists that proxy. This
is the catalog's first **agent** pattern - a carve-out for a Docker-capable device with no SNMP; the
SNMP-first rule still governs the 400-server fleet. CPU/mem/fs/NIC reuse the native agent item keys,
so curation is shared with the SNMP classes.

### SNMP gaps (need more than SNMP)
- **UniFi** per-port/PoE/WAN/clients → controller API (self-hosted Network app **or** cloud gateway;
  SNMP is thin). **Nutanix / Citrix farm / vSphere** → API/VMware collector (app-level, no SNMP).
- **XCP-NG** per-VM + host CPU → XAPI + the hosts' rrd_updates feed; host temp → the argus-temp
  XAPI plugin on dom0 (hwmon; see docs/hosts/xcpng.md). **NUT** → upsd :3493.
- **unRAID** disk temp & SMART → smartctl via Net-SNMP `extend`, or the unRAID API. **Hyper-V**
  per-VM → WMI/agent (host stays SNMP). **AdGuard** block/query stats → AdGuard HTTP API.

### Every template says why (template rule)
A sensor that can't read must say why, in words an admin can act on, and Argus shows that reason next
to the reading (hover, or tap on a phone, for a line under the sensor) and puts it in the alert. Every
Argus template follows one of two shapes, and a new one must too:
- **Script items that throw** (the UniFi, AdGuard, Home Assistant and PeaNUT masters): the thrown
  message names the cause and, where there is a usual fix, the setting to check. A refused HTTP call
  passes on the service's own reason (`UniFi API HTTP 400 (api.err.NoSiteContext) - no site named
  "x": the Site name must be the internal name ...`), never a bare status code. Zabbix keeps it as
  the item's error; the dependents inherit it. Hints name the setting the way Argus labels it ("the
  API key", "the Site name").
- **Collectors that report "down" on purpose** (SSH, XCP-NG, NUT, and the HTTP masters' own down
  paths; DNS per name): a connection or login failure is data, not an error, so the down trigger
  fires instead of every sensor going unsupported. The JSON then carries `"error": "<one line>"`
  (what ssh printed, upsd's `ERR` answer with its meaning, the XAPI failure, the rcode), and the
  template keeps it in a **Collection error** item next to the down flag (`linux.ssh.error`,
  `xcp.error`, `nut.error`, `adguard.error`, `hass.error`, `dns.resolve.error[<name>]`, and per
  systemd unit / Docker container `linux.ssh.unit.state[<unit>]` / `linux.ssh.container.status[<name>]`: text, not a
  curated sensor, empty while it works, `JSONPATH $.error` with an empty value when an older collector
  doesn't print it). Argus maps each flag to its reason item (`reasonKeys` in
  `internal/server/reasons.go`; a test fails when a collector flag has none).

A collector that can't run at all (an external script the probe doesn't have yet, a timeout) leaves
its master item unsupported, and since its sensors are discovered from its answer, nothing else would
show it. So the probe's external collectors (`collectors` in `internal/server/collectors.go`:
`argus_http.py`, `argus_tcp.py`, `argus_linux_ssh.py`, `argus_nut.py`, `argus_xcpng.py`,
`dns-resolver.py`) show as a sensor of their own while unsupported ("HTTP checks", in their sensors'
category), and alert even when they never collected; a missing script reads as which probe release
brings it.

Reasons never carry a secret: no password, token or key, only what the other side said and which
setting to look at. Argus trims them to one line of at most 200 characters.

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
| Ping | loss ≥ 20% or RTT ≥ 0.15 s | loss ≥ 60%, RTT ≥ 0.5 s, or 100% loss (down) |
| Probe reporting (§18a) | no data for 3 min | no data for 5 min |
| Probe unsent values | ≥ 1000 for 5 min | ≥ 10000 for 5 min |
| Probe items delayed over 10 min | ≥ 50 for 15 min | ≥ 200 for 15 min |
| Probe cache used (history / configuration) | ≥ 75% | ≥ 90% |
| Probe process busy (per process type) | ≥ 75% (5 min avg) | ≥ 90% (5 min avg) |
| HTTP/HTTPS | resp ≥ 1 s | resp ≥ 3 s, or a status code outside `{$HTTP.EXPECT}` (200-299) / missing text / untrusted certificate / timeout (down) |
| TLS cert expiry | < 21 days | < 7 days |
| DNS | resolve ≥ 0.5 s | resolve ≥ 1 s, or no/incorrect answer |
| UPS | - | **on battery** / runtime < 5 min / replace battery |
| Printer supply | (not monitored) | (not monitored) |

- Every graded metric carries **both** a warning and an error (high) band - `{$X.WARN}` and
  `{$X.HIGH}` - so an alert can escalate; the hard-failure cases (host down, DNS not resolving,
  endpoint down) stay as their own triggers on top.
- Disk-temp thresholds are **type-aware** (HDD vs SSD) via the SMART `rotational` flag
  (from the unRAID API). Pure-SNMP disks with unknown type fall back to the SSD numbers.
- **Edited from the UI (§D).** Each threshold is a Zabbix user macro defined on the class template
  (`{$CPU.UTIL.WARN}`, `{$DISK.TEMP.HIGH}`, `{$PING.LOSS.WARN}`, ...). The admin **Thresholds** screen
  edits the fleet-wide default per template; per-device (per-type) overrides live in each host's
  settings dialog. A host macro of the same name beats the template default deterministically, so an
  override is a host macro and a "reset" deletes it. Fleet-wide defaults are stored in Argus and
  re-applied onto the templates after each startup reconcile, so a template re-import can't clobber
  them. True per-individual-instance overrides (one disk vs another on the same host) would need
  instance-context macros in the trigger expressions and are a later option; per-type (HDD/SSD/NVMe)
  is covered today by context macros.

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

Acknowledged has its own colour (`--acked`), and everything about an acknowledged problem takes it:
the sensor row and its graph, the "acked" tag, the host's problems box (titled **Acknowledged
problems** once all are), and, once every problem on a host is acknowledged (the host's `acked` flag),
its dot, problem count and ping graph, and a group's dot when that's all the group has left.

## 7b. Uptime and incident history

**Uptime** (`internal/server/uptime.go`). An up/down sensor reads 1 while up and 0 while down (ping,
a TCP/HTTP(S) service check, a collector's reachable flag, XCP-NG's login flag, a DNS name
resolving), so its average over a period is the share of checks that found it up. 24 h comes from
the raw history; 7 and 30 days are calendar days in the Argus timezone (today and the days before
it), summed from Zabbix's hourly trends weighted by their sample counts, plus the raw values of the
hour the trends don't cover yet. A host's uptime is measured on its master sensor when that is an
up/down one, else on its ping, else on a collector's reachable flag.
- **Host card:** a band at the top with the 24 h / 7 day / 30 day figures and a strip of the last 60
  checks (`GET /api/hosts/{id}/availability`); an up/down sensor's chart reveal shows the same plus
  one bar per day for 30 days (`GET /api/items/{id}/availability`), above the group's chart for a
  group (ping loss and latency, HTTP response time), in place of the flat 0/1 chart for a lone flag.
  Cached a minute per sensor.
- **Status pages** read many hosts at once, so complete days (a day counts as complete two hours
  after it ends, once its last trends are in) are kept in `uptime_days` (per sensor and day: the
  weighted up count and the samples; 120 days kept) and read from Zabbix once; only the days still
  open are read each time, and a new host reads its window without making the others read theirs.
- Figures never round up to 100%: one missed check in a month reads 99.99%. Colour: green from
  99.9%, amber from 99%, red below.

**Incident history** (`internal/server/incidents.go`). What went wrong and when: every problem at
Warning and above, with its start, its end (or "ongoing"), the sensor it was on, who acknowledged it
(Argus's own acknowledgements, while on record) and, for a collector flag, the reason the collector
gave when it started (read from the reason item's history, section 5 "Every template says why").
Zabbix keeps every problem event with the recovery that closed it; the problems Argus raises itself
(a sensor that stopped collecting, an interface that stopped answering) exist only while they are
open, so the notifier logs them in `argus_incidents` as they open and close (only from a complete
lookup, so a failed read never closes them; the reason is kept from the moment they opened; closed
ones kept 120 days). A host card ends with its last 30 days, folded (`GET
/api/hosts/{id}/incidents?days=30`), and a drilled-down sensor with its own history, open
(`&items=<its id, or every channel of its group>`: Zabbix is asked for those sensors' triggers only,
so a busy host's other incidents can't crowd them out); the **History** page lists the fleet's, 24 h / 7 / 30 / 90 days,
Errors + Warnings or Errors only, filterable by host, sensor or reason (`GET
/api/incidents?days=N`). Zabbix's events follow its housekeeping retention (Settings, Data retention).

---

## 8. Auto-provisioning pipeline (replaces PRTG's "Add Sensor")

> Sequencing: **§C** built the class templates (§5) plus a **manual** attach path; **§B** automates
> the pipeline below on top of them. The **universal subnet scan** (steps 2-6) and the **UniFi
> controller sweep** (step 1) are both SHIPPED - §B is complete.

Runs per-site on the probe, reports to core for provisioning:
1. **UniFi controller sweep** ✅ → a saved controller (name + URL + encrypted API key) is asked for
   its adopted devices across all sites (model/type/MAC/IP/firmware/site, devices only - clients
   are the subnet scan's job) → candidates into the same review pipeline as the subnet scan.
2. **Capability fingerprint** per host ✅ → ICMP + a TCP port set (22/53/80/443/445/3493/8080/8443/10050),
   SNMP `sysDescr`/`sysObjectID`/`sysName` (hand-rolled v1/v2c GET), an HTTP(S) banner grab
   (status/Server/`<title>`), a real DNS query on :53, reverse DNS and the ARP cache.
3. **Template attach** by fingerprint ✅ → the core maps raw facts to a suggested device class
   (`provision.SuggestClass`); the admin can override per row before adopting.
4. **LLD** creates only instances that exist ✅ (adoption reuses `POST /api/hosts`, incl. the
   post-create LLD auto-fire below).
5. **Default thresholds** applied ✅ (classes carry them; overridable per host).
6. New devices surface in the UI for review ✅ → the admin-only **Discovery** tab: pick a probe +
   subnet, multi-select results, adjust name/class/macros per row, adopt or ignore (ignored devices
   stay ignored across re-scans; already-monitored IPs are flagged).

**Mechanics (universal subnet scan, shipped).** The scan piggybacks on the probe check-in channel,
so the probe stays a pure reporter with no listening port: the check-in loop (60 s tick) advertises
`"scans":true`, the core hands a queued job out exactly once in the check-in response
(`scan: {id, cidr, snmp}` - SNMP creds default to the probe's SNMP default, v1/v2c only), the probe
backgrounds `argus_netscan.py` (stdlib-only, one process per scan, lock-file serialised, 8-minute
budget, ≤1024 addresses) and POSTs the raw fingerprints to `POST /api/probes/scan-results`
(probe-token auth). Jobs/results persist in `discovery_jobs`/`discovery_results`; stale jobs expire
(pending 5 min, dispatched 15 min). Classification lives on the core so the mapping improves without
fleet releases. Adopted hosts are tagged `argus.source=discovered`. **Core as a scan source:** a
scan can also run from the core server itself - no check-in to ride, so the job dispatches straight
into an in-process Go scanner (`internal/netscan`, the twin of argus_netscan.py) covering the
networks the core monitors directly. Container-imposed limits: ICMP uses an unprivileged datagram
socket (works under Docker's default `ping_group_range`, silently skipped elsewhere) and no MAC/ARP.
The core has its own SNMP default (stored under proxy id "0", set in Settings → Core SNMP default): it backs
core-run scans and gives core-monitored hosts the same SNMP-credential inheritance as proxy hosts.

**Controller scope and certificates.** A saved controller carries a **site scope** (`sites`; only
the probes of those sites receive it and its API key at scan/sweep hand-out; empty = every site) and
a **certificate policy** (`tls_mode` verify | pin | ignore, `fingerprint` for pin). Saving with
*verify* makes the core inspect the certificate: trusted by the system roots -> saved; self-signed ->
the admin is shown the certificate (subject, issuer, expiry, SHA-256) and asked to pin it; unreachable
from the core -> "Ask a probe": a `cert` discovery job (`discovery_jobs.kind = cert`, the URL in
`cidr`) rides the probe's check-in as a `cert_only` sweep payload without a key; the sweep script
reports `{certificate: {fingerprint, subject, issuer, not_after}}` through scan-results, stored in
`discovery_jobs.certificate` and offered for pinning (a refused sweep reports it the same way);
cert jobs are hidden from the scan history and the notices; or paste a fingerprint, or ignore. The Go client (`internal/unifi`, `Options`) and the
probe scripts (`tls_opener` in `argus_netscan.py` / `argus_unifi_sweep.py`, the pinned connection
compares the leaf SHA-256 right after the handshake) apply the same policy. XCP-NG (`{$XCP.TLS}`,
default pin) pins on the collector itself: first contact stores the SHA-256 under
`/var/lib/zabbix/argus-pins/`, a change is refused and reported as `tls_error` (and as the poll's
`error`, which Argus shows). The UniFi class
templates' own HTTP items still don't verify (Zabbix can't pin).

**Mechanics (UniFi controller sweep, shipped).** The second candidate source rides the exact same
rails: a `discovery_jobs` row with `kind=unifi` referencing a saved controller (`unifi_controllers`,
API key encrypted at rest, write-only from the browser). A probe advertising `"sweeps":true`
receives it as `sweep: {id, url, key}` in the check-in response and backgrounds
`argus_unifi_sweep.py` (stdlib-only, mirrored like the scanner); a core-sourced job runs the
in-process `internal/unifi` client. Both speak the API the UniFi class templates already poll
(`X-API-KEY` against `/proxy/network/api/self/sites` + `/api/s/{site}/stat/device`, bare-path
fallback for plain self-hosted controllers, TLS unverified) and post into the same
`/api/probes/scan-results` shape with a per-host `unifi` facts object. Classification is
deterministic (`provision.SuggestUniFiClass` from the controller's own device type). The adopt
payoff: for a sweep-adopted UniFi-class host the server injects `{$UNIFI.URL}/{$UNIFI.KEY}/
{$UNIFI.MAC}/{$UNIFI.SITE}` from the saved controller + sweep facts before validation, so the API
key never travels through the browser and nobody types per-device macros. Sweeps share the scan
queue, history, retention and ignore carry-over. Saved controllers also **enrich subnet scans**,
primarily PROBE-SIDE (image r16+): the scan job payload carries the controllers (keys decrypted
at handout, like the sweep), the scanner queries them locally after the scan - the only vantage
point that reliably reaches a remote site's controller - and posts rows with `unifi` facts +
`unifi_ctl`, or a `unifi_client` naming hint when the host matched the controller's client
table instead (name suggestion only, never a class, never an import). The core runs the same
merge at ingest (`enrichScanResults`, MAC-then-IP, best-effort bounded) for core-run scans and
as the fallback for older probe images; the stored per-result `controller_id` is what the
adopt-time macro injection resolves through - so a scan row for known UniFi gear behaves
exactly like a sweep row. The facts carry the controller's own device MAC, and `{$UNIFI.MAC}` is
filled from it rather than from the scanned MAC: a gateway answers ARP on its LAN side with a
derived address, so a gateway matched by IP would otherwise get a MAC the controller doesn't know
(`api.err.UnknownDevice`).

**Discovery trigger (shipped with §C).** LLD rules run on a long interval (1h on the SNMP classes),
so a freshly added host would sit without its per-instance sensors. Two seams close that gap:
- **Add-device auto-fire:** after `POST /api/hosts` creates the host, the server fires every enabled
  LLD rule via Zabbix `task.create` ("execute now") in the background - delayed ~15 s (the new
  config must reach the host's proxy on its next config sync first) with one retry at 60 s.
- **"Discover now"** (`POST /api/hosts/{id}/discover`, admin/helpdesk): the host kebab in the tree
  runs the same trigger on demand, for any host with LLD rules - classed or not.

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
- **Flap debounce (alert delay):** a problem must stay open for the **alert delay** (Settings ->
  Alerting, default 60 s, `ARGUS_ALERT_DELAY_SECONDS`) before anyone is notified.
- **Escalation + reminders, per channel:** every channel (global or personal) has a **Notify after**
  delay (0 = with the first alert) and a **Remind every** interval (0 = off). A channel hears of a
  problem only once the incident has been open and unacknowledged for its delay (counted from when
  the incident began, never before the alert delay has passed), so "team at once, managers after 30
  min" is two channels. Reminders (`[HIGH REMINDER]`, "Still open after 1h 5m (reminder 2)") repeat
  while the problem stays open and unacknowledged, for problems at or above the channel's own
  **Remind for** severity (independent of its alert floor, so "alert on warnings, remind only about
  errors" is one channel); acknowledging, pausing or hiding stops them. A
  channel added after a problem went live isn't sent that problem.
- **Heartbeat (who watches Argus):** nothing inside Argus can report that Argus itself stopped, so
  with a **Heartbeat URL** set (Settings -> Heartbeat, `ARGUS_HEARTBEAT_URL`) it pings an outside
  monitor (a healthchecks.io check, an Uptime Kuma push monitor) once a minute, but only while it is
  healthy end to end: the Zabbix API answers with the token, some probe delivered data in the last 5
  minutes (so `zabbix_server` is taking it), the alert loop read the problem list in the last 90 s,
  the database takes a write, and not every enabled alert channel failed its last send. Anything else
  holds the ping and the outside monitor raises the alarm after its own grace period. Settings shows
  the last check (pinging / held and why / failing and the HTTP status) and a **Send now** button;
  the URL is never logged, since it usually carries the check's secret.
- **Maintenance windows (planned work):** **Configure -> Maintenance** lists windows (admin and
  helpdesk edit, a viewer reads), each covering sites (a root covers its subgroups) and/or single
  hosts, once or every day, week (chosen weekdays) or month (a day, a short month runs it on its last
  day, or the last day), from a start time for a duration of 5 minutes to 7 days, in the Argus
  timezone (a window may run past midnight; local start times hold across DST). While a window is on,
  its hosts keep collecting and their problems stay on screen, tagged (the tree says
  `· maintenance`, the host card has a band with the window and its end, an Overview row says
  "In maintenance (name) until ..."), the tree dot stops pulsing, and the notifier treats their
  problems as not alertable, like a paused host's: no first alert, no escalation, no reminder.
  When the window ends, whatever is still open alerts at once (its alert delay is long over).
  Acknowledgements and recoveries of alerts sent before the window still go out. A scoped user
  (section 10) sees the windows that touch their sites and edits only those that cover nothing
  else. `internal/server/maintenance.go`.
- **Quiet hours (per user):** **Account -> Quiet hours** sets a daily stretch (may wrap past
  midnight, Argus timezone) during which the user's **personal** channels only get problems at or
  above a floor (Average, High or Disaster; High by default). A quieter problem waits: it is sent
  when the quiet hours end if still open (the delivery was never recorded, so the plan picks it up).
  Reminders, acknowledgements and recoveries below the floor are skipped meanwhile. Shared channels
  are not affected.
- **Master sensors (dependencies):** every host has a master sensor - its ICMP ping by default
  (`icmpping`), another sensor or none per host (`host_masters`), and the Probe host's reporting
  sensor (`zabbix[uptime]`). A collector-based host also has its collector's reachability sensor
  (`nut.reachable`, `xcp.reachable`, `linux.ssh.reachable`, `adguard.running`, `hass.running`) as a
  second master, so a stopped service alerts once as "unreachable" while the machine still pings;
  "none" drops both. Masters are ranked (the ping, or the chosen sensor, above the collector) and
  hold only what ranks below them, so when the whole machine is down the ping's own alert goes out
  and the collector's is held; two masters down together never hold each other, which would leave
  the device with no alert at all. A collector holds only what it feeds, never the ping's own
  sensors (`icmpping*`: loss and response time measure the network). While the master has an open "down" problem (error level, or any "no
  data" one), the host's other alerts are **held**: they stay pending, and firing ones get no
  escalation or reminders. A probe's reporting sensor is also the master of **every host that proxy
  monitors** and of Zabbix's own per-proxy checks (`zabbix.proxy.*[<proxy>]` on the Zabbix server
  host), so a probe outage sends only the probe's alerts. To win the race with a slow master (ping
  needs 3 failed checks), a new problem also waits up to 5 minutes while the master hasn't reported
  since the problem began, or its last ping failed. A problem held by a down master waits out the
  alert delay again (at least 90 s) after the master recovers, so readings that settle while a device
  or probe reconnects don't alert; a held problem that is still open after that alerts normally.
  The census applies the same rule (`markHeld`): an unacknowledged error or warning a **down** master
  holds (not one merely yet to report, so rows don't flicker) carries `held_by` (the master's host,
  item and label), the master's row `holds` (how many), and `/api/census` counts them as `held`
  instead of their state. The lists show the master and fold the held rows behind a **Show held**
  toggle. A status page folds them into the master's row (`foldHeld`, below).
- **Argus-raised problems:** Zabbix raises no problem when monitoring itself stops, so Argus adds its
  own, shaped like Zabbix problems (event ids `argus-unsupported-<item>` / `argus-interface-<iface>`)
  and merged into the notifier's list, the Overview and the host page: a sensor **not supported** on
  its third failed check in a row, if it has collected before (one with no last value never applied
  to the device, or has been dead for over a day: silent, and an alert already sent for it is
  dropped without a recovery), i.e. 2 of its own update intervals after Argus first saw it fail
  (a dependent sensor uses its master's interval; an unreadable one counts as 1 min;
  `item_unsupported` records since when, as Zabbix doesn't say). A dependent sensor whose own steps
  can't fail (each discards, or sets, its value on error) is left out while its master collects:
  only its master's failure made it unsupported, and Zabbix keeps that state until it stores a value,
  which a URL that doesn't answer never gives its response time (`leftOverUnsupported`). And an **agent / SNMP / IPMI / JMX
  interface** Zabbix marks unavailable (timed from its `errors_from`). Both are High and skip the
  alert delay, since failed checks or Zabbix's retries already are the wait. The reading is Zabbix's
  error. Acks stay in Argus (no Zabbix event to mirror). Those present when the
  feature arrived were baselined. Supported sensors with no new values are deliberately not alerted:
  many store only changes, so an old last value is normal; a silent device or probe is caught by its
  master instead.
- **System notices:** Argus's own news as `[INFO]` messages (neutral colour, no reminders, no
  recovery), for channels with **System notices** on (`system_notices`, off by default; a channel's
  alert level can be **None** = `alerts` off, for a notices-only channel; one with neither is
  refused). A loop in the server checks once a minute, since most sources are only read on request:
  the release cache (`appUpdateStatus`), the update-dir status files (core self-update outcome), the
  probes' check-in rows (versions, updater versions, OS patch status), the core OS report (security
  updates, reboot, Zabbix candidate), discovery jobs past a watermark, and channels whose last
  attempt failed. **Conditions** are tracked in `notice_conditions` (since when) and told once they
  have held their minimum time (probe/updater behind 6 h, security updates 48 h, reboot and Zabbix
  update 24 h, failing channel 30 min, a new release at once); **events** are told once. The
  `notices_sent` ledger dedupes (a condition's row goes when it ends, so it can be told again;
  events are kept a year). Probe self-update outcomes are inferred: a newly reported version is a
  success, and an update handed out at check-in that isn't running 20 minutes later failed (the
  updater rolled it back). The Updates and Probes pages show the same records per row (`update_job` /
  `updater_job`: queued while `update_to` waits, updating while the hand-out record stands, failed for
  a day or until retried), so a reload never hides an update in hand. **Check for updates** on the
  Updates page runs the core's lookup (`POST /api/version/check`) and the three 3-hourly probe
  lookups (`POST /api/probes/check-updates`, admin: probe image, argus-updater, probe-vm) at once.
  Update notices open the Updates page. Probe notices route by the probe's site; a failing shared channel isn't
  told about itself, and a failing personal channel only reaches its owner's other channels.
- **Deliveries decide the follow-ups:** `notify_deliveries` records which channels an alert reached.
  Reminders, the **acknowledged notice** (`[ACKNOWLEDGED]` with who took it and their note, sent once
  per ack; an un-ack re-arms it and resumes reminders) and the **recovery** go to exactly those
  channels, so a delayed channel that was never told gets no RESOLVED either, and a High-only channel
  that got the error still hears when an incident that eased to a warning finally clears. On a
  severity change the deliveries move to the problem that took over, and a channel that already had
  the incident gets the new severity straight away.
- **Severity changes aren't recoveries:** warning and error thresholds are separate band triggers
  ("at or above warning and below error" / "at or above error"), so escalating closes the warning
  trigger. RESOLVED is sent only when the sensor has no open problem left; a problem that closes
  while another one on the same host + sensor at a different severity is open is a severity change,
  and sends nothing (the new severity alerts on its own, and the channels that had the old one keep
  following the incident).
- **"No data" alerts** (nodata() triggers, e.g. probe unreachable) read "No data for 4m (since
  HH:MM)" instead of the stale last value, and skip the flap debounce: the nodata period already is
  one. The notifier reads trigger expressions expanded (`expandExpression`), which is also where the
  alert's threshold comes from.
- Telegram = one shared bot, per-site topic. Discord = dedicated webhook per site.
  Model supports flipping either to shared/dedicated with no code change.
- Secrets entered in the UI later (placeholders for now).

**Status (2026-09).** Channels are managed in the **Notifications** tab (Discord webhook / Telegram
bot + chat (+ forum topic) / SMTP, and from 2026-10 Microsoft Teams, Slack, ntfy, Gotify, Pushover and a
generic JSON webhook), each scoped to one or more sites (or all) with an alert level:
**warnings and errors** (Zabbix Warning and up) or **errors only** (Average and up, i.e. everything the
UI paints red; the older High / Disaster floors are folded into it at startup). Every send attempt -
alert, recovery, or the **Send test** button - is recorded on the channel (`last_sent_at` /
`sent_count` on success, `last_error` / `last_error_at` on failure) and shown on its card, so a broken
webhook or SMTP password is visible in the UI rather than only in the core log. The message format is
shared across channels: `[SEVERITY] host - trigger` with the status emoji (the Zabbix severity, e.g.
`[HIGH]`; `[RESOLVED]` for recoveries), the reading + threshold, the site, the time, and deep links -
**Open in Argus** and, for problems, a signed one-click **Acknowledge**. Email is a single-card HTML
message with a plain-text alternative, the inline 2-hour chart, a dark-mode override for clients that
honour it, and a footer linking back to the channel page; Telegram is a compact card whose links are
inline-keyboard buttons (a photo message when a chart is attached); Discord is an embed with
Severity / Host / Site / Reading fields. Microsoft Teams gets an Adaptive Card (a header in the status
style, the details as plain text runs so nothing typed is read as Markdown, Open in Argus /
Acknowledge buttons; a Workflow message is capped near 28 KB, so no chart); Slack a classic attachment
with the status colour, Severity / Host / Site / Reading fields and link buttons; ntfy, Gotify and
Pushover a phone push whose priority follows the severity (an acknowledgement is quiet; Pushover also
carries the chart); the generic webhook POSTs the event as JSON (`notify.WebhookPayload`: kind, state,
severity, host, site, name, value, threshold, time, links) with the whole message as plain text in
`text`, and an optional `Authorization` header.

**Personal channels + email-to-users (2026-09).** Two per-recipient additions sit alongside the global
channels above. (1) **Personal channels** let any signed-in user (any role) register their own Telegram
(their own @BotFather bot: token + chat id) or Discord (webhook URL), and from 2026-10 Teams, Slack, ntfy
or Pushover, in **Account → Personal
notifications** and receive alerts there, scoped by one or more sites (a multi-select of host-groups; selecting a
probe's root group covers its subgroups) and a severity floor, exactly like a global channel.
They are self-service and self-owned (`user_notify_channels`, config encrypted at rest, managed under
`/api/me/notify/*`); a user only ever sees and edits their own. The notifier routes a problem to global
**and** matching personal channels, and now fires as soon as *either* matches - so a personal-only setup
alerts. This is the foundation for future mobile (Android/iOS) push, which becomes just another personal
channel type. (2) An **email channel** can deliver to **each active user's registered email** instead of
a fixed `to` (a `recipients` mode on the channel, admin-controlled): the notifier fans it out to one
private per-user message. Per-user email opt-out and a shared one-tap Telegram-link bot are left for
later.

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
  **Settings → Sessions** and honour env-wins precedence. When a session ends while the app is
  open, the SPA sees the auth middleware's `401 {"error":"unauthorized"}` on its next API call and
  drops straight back to the login screen ("Your session has ended"), keeping the URL so signing in
  again returns to the same view. Other 401s (a wrong current password) are left to their form.
- **Per-user landing page** preference - default Overview; user can switch to the Errors list in
  **Account → Landing page** (stored server-side, `POST /api/me/preferences`).
- **Per-site visibility:** a helpdesk or viewer account can be limited to some **sites** (Users ->
  Sites, the same hierarchical picker as a channel's: a root covers its subgroups; empty = every
  site; an admin always sees everything). The server filters every read to the hosts in those groups
  (`internal/server/scope.go`): the tree, groups and search, the census behind the pills and the
  Overview, problems, triggers (a trigger over hosts in two sites names only the user's), sparklines
  and daily bars (by each sensor's host), incidents (the fleet feed asks Zabbix for the user's hosts
  only, so other sites can't crowd them out of the row limit) and probes (by the probe's site). A
  host, sensor, problem, probe or group outside the sites answers **404**, as if it didn't exist,
  for reads and for every action on it (acknowledge, pause, hide, mute, priority, host settings,
  moving a host between groups or probes, group create / rename / delete, tree order); a problem
  over hosts in two sites needs both. Alerts follow the same line: a scoped user's **personal
  channels** serve only their sites (a channel set to a wider site is narrowed to theirs; "all
  sites" means all of theirs), an email channel that goes to **every registered user** sends each
  scoped user only their sites' alerts and their probes' notices, and news about the whole install
  (updates, failing channels) is not sent to them. Host-to-group lookups are cached for 30 s, so a
  host moved between groups follows within that; a change to a user's sites applies at their next
  request.

---

## 11. Custom app screens

**Viewing:** 1) Overview (all sites, health rollup - default landing) · 2) Errors-only ·
3) Errors + Warnings · 4) Site view (device list) · 5) Device view (sensor tiles) ·
6) Sensor detail (graphs).

**Managing:** 7) Discovery review · 8) Device management (add/edit, assign site+proxy,
class, per-host threshold + sensor-order overrides, pause, acknowledge) · 9) Thresholds
(fleet-wide defaults per template + per-class sensor-category order) · 10) Notifications
(instances, credentials, targets, test-send) · 11) Users & security ·
12) Updates (the core, its sidecar, the probes and the VMs' operating systems) ·
13) Settings (FQDN/allowed-hosts, retention, proxy status).

---

## 12. Graphs, time tabs & retention

- Tabs: **2h · 2d · 1M · 3M · 6M · 1Y**, with zoom-to-timeframe.
- Maps onto Zabbix's data split (no custom downsampling needed):
  - **history** (raw, retain ~7-30 d) → powers **2h / 2d** + zoom.
  - **trends** (hourly min/avg/max, retain 1-2 y) → powers **7d / 1M / 3M / 6M / 1Y**.
  - The periods are Zabbix's global housekeeping settings (installers set 30d / 730d / compress
    after 7d), editable in **Settings → Data retention** (`/api/settings/retention`, needs a Super
    admin token). The tabs set the floors: history ≥ 2 days (2d tab), trends ≥ 7 days (7d tab);
    under a year only warns that the 1Y tab won't be full. Saving always applies the periods as a
    global override, and a save that shortens a period confirms first (Zabbix deletes the older
    data at its next hourly housekeeping run).
- **Daily bars for "today so far" counters** (AdGuard's queries/blocked, `.today` keys): the
  source resets its count at ITS OWN day boundary (AdGuard: hard-coded UTC midnights), and Argus
  reconstructs **true local calendar days** from it - within a source day the counter only grows,
  so consecutive-reading deltas are exact and credit to the local day they happened in
  (`dailySplitDeltas`). The **`/api/daily`** endpoint is the single source: the row headline
  ("N queries · M blocked today"), the mini daily bars, and the big stacked-bar chart (blocked
  overlays total; tabs **7d (default) · 1M · 3M · 6M · 1Y**, no 2h/2d) all render its buckets.
  **Block rate** derives per day from the sibling counters (blocked ÷ total) and bar-charts the
  same way.
- **Threshold bands:** a single-sensor chart is coloured **by value**, not by the sensor's current
  state - the normal accent colour inside the normal range, the warning colour past the warning
  value, the error colour past high (mirrored for lower-is-worse sensors), with dashed reference
  lines at both values. Only the stretch of line beyond a threshold changes colour, and a past
  excursion stays marked after the alert clears. The values come from the sensor's **own triggers**
  (`trigger.get` with `expandExpression`, so host overrides, fleet defaults and per-disk-type macro
  contexts are already resolved - the chart shows exactly what alerts); `/api/hosts/{id}/items`
  carries them per item as `thr`. On multi-channel group charts only the **main channel** is banded -
  the primary, when it is the only channel on its unit (ICMP response time, a disk's Used %); every
  other channel, and peer groups (drive temps, CPU cores, In/Out) where a gold channel would read as
  "warning", keeps its identity colour and gets just the reference lines (one pair per distinct
  threshold, e.g. HDD and NVMe pairs on a mixed drive group). Every reference line is labelled by a
  filled **axis tag** (the trading-chart pattern) at its height in the gutter of the axis showing its
  scale - never over the data, and the side says which channel's scale it is on (ICMP: Loss tags on
  the % axis, response time on the ms axis). A tag's text is anchored exactly where uPlot anchors the
  axis numbers (tick length + gap out from the plot, same alignment and font), so it lines up with
  them. Tick labels within a tag's height are dropped - a tagged axis gets denser ticks (min spacing
  18px instead of 30) so a readable scale survives - the gutter grows if a tag is wider than the
  widest tick, and tags on one side stack instead of overlapping. A line whose scale has no axis (a third unit on a two-axis chart) falls back to a
  small in-plot label with the channel name.
  Sensors without a numeric threshold (up/down, state checks) are unchanged. A reference line outside
  the data's range is not drawn - the y-scale is never stretched to fit it. The **alert-notification
  PNG** (`chart.go`) applies the same banding server-side: its fill is tinted per pixel row by the
  band of that height (matching the app's vertical gradient), with dashed reference lines and the
  same filled value tags in its Y-axis gutter (an axis label a tag would cover is dropped). An up/down
  sensor's PNG (`isUpDownKey`: ping, collector reachability, HTTP and TCP checks, units and
  containers) is drawn as states instead (`renderStateChart`): the time it was down shaded red over
  the full height, like the app's Downtime band, and a step line on labelled "up" and "down" levels.
- Storage: **PostgreSQL + TimescaleDB** as Zabbix's DB (native integration, partitioning +
  compression). Single source of truth; app data lives in the same instance (separate schema).

---

## 13. Responsive UI targets
- Phone (S25 Ultra), tablet (Galaxy Tab S9), desktop 16:9 / 16:10 / 32:9.
- Look: PRTG-style density, Uptime-Kuma-grade polish.

---

## 14. Deployment
- Core: dedicated VM (Zabbix server + web + Timescale). Custom app: Docker container(s).
  For NEW deployments the whole core ships as a **self-installing appliance VM** - see **§14d**.
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
  token is present (either way), the service is inert on subsequent boots. **The page asks for a
  setup code** printed on the VM's console (and its login banner): anyone on the network can reach
  the page, only someone at the console can submit it (with no console, the seed ISO is the path: it
  needs no code). Every value is shape-checked (https enroll URL unless a "lab" switch is ticked, a token, a host) before it reaches
  the container's env file.
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

**Status (shipped).** The golden image (`argus-probe`'s `deploy/probe-vm/`) is built with Packer from
the Debian 13 **`generic`** cloud image (full driver set - needed so the OVA boots on non-virtio
hypervisors and the seed CD's isofs/CD-ROM works). **Delivery:** CI publishes **OVA** (stream-optimized
VMDK + a hand-written OVF; imports on VMware/Nutanix/VirtualBox and, via *Import → OVA*, Xen Orchestra),
**qcow2** (KVM/libvirt), and **VHD** (Hyper-V; VDI-import on XCP-NG). **Enrollment**, three ways, all
from *Add probe → VM (cloud-init)*: pasted cloud-init user-data; a **downloadable seed ISO**; or the
first-boot setup page. The seed ISO is deliberately **not** a cloud-init NoCloud seed - NoCloud needs
the `user-data`/`meta-data` names, which plain ISO9660 mangles and only Joliet/Rock-Ridge preserve.
Instead it's an **Argus-owned** image (label `ARGUSSEED`, one 8.3-safe `ARGUS.ENV`) read by our own
first-boot service, which sidesteps cloud-init's NoCloud datasource detection (fiddly on XCP-NG)
entirely.

**cloud-init is dropped from the deployed image** (it does its build-time job - build user + root-FS
grow - then `provision.sh` purges it), so the appliance self-configures through systemd-networkd +
the first-boot service alone; the enrollment matrix is now seed ISO / first-boot page (the cloud-init
paste path is retired). **Break-glass (§14a credential lifecycle) is implemented**: the first-boot
service creates a per-VM `argus` sudo user with a generated password, reports it over the probe
check-in channel to `POST /api/probes/break-glass`, and Argus stores it encrypted and reveals it to
admins on the Probes page (**Console** button); the user is in `sudo` only (no docker group), and
SSH password login is allowed for that one account only (root and everything else: keys). SSH host keys regenerate on first boot
(`argus-hostkeys.service`); the **console keyboard layout** is configurable per-VM (Add-probe → VM, or
the setup page → `/etc/vconsole.conf`). **Static networking** for no-DHCP sites rides the seed too
(`ARGUS_IP`/`ARGUS_GATEWAY`/`ARGUS_DNS` from Add-probe → VM → a static systemd-networkd file applied
before enrollment); it's seed-only, since the first-boot page needs an IP to be reachable, and a stuck
VM re-reads a corrected seed on reboot. Still open: the bare-metal Clonezilla SKU.

**Container health (all three images).** Each image declares a Docker `HEALTHCHECK`, which
`docker ps`, the Unraid GUI and Dockhand show. The **core** is distroless, so the binary checks
itself (`/argus healthcheck`: its own `/healthz` over the loopback at `ARGUS_LISTEN`). The **probe**
runs `/app/healthcheck.py`: the Zabbix proxy process is up and accepts connections on its listen port.
The **updater** runs `/app/healthcheck.sh`: the Engine answers `/_ping` on the socket and, in the
long-running modes, the watch loop's heartbeat is fresh (each round allows the next one its time,
an update included). Faults outside the container (Zabbix, the core, the network) stay out of
every check: a restart can't fix them, and Argus alerts on them already.

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

**Sensor census (the pills, the Overview, the status pages).** The census reads every host item,
every open problem and their triggers, so its cost grows with the fleet. It is built on the core in
the background and served from memory (`internal/server/census.go`): every 20 s while someone has
looked at it in the last 10 minutes, every minute otherwise, one build shared by every browser and
status page. A change made through Argus (acknowledge, pause, hide, a threshold, any other write by a
signed-in user or through an alert's signed link) marks it stale once the request completes, and the
next read waits for a fresh build, so whoever acted sees the result. The app asks
`GET /api/census?rows=<states>` for every state's count plus the rows of the states on screen (the
Overview's error / warning / acknowledged; the OK, paused or hidden list only while it is open), so
the browser no longer downloads the whole census every 30 s. The answer carries `age_ms` (how old
the data is), `next_ms` (when the next build should be ready: the app fetches again just after it,
so every answer is fresh; the header counts down to that fetch, "Refresh in 18s", with the
data's age in its tooltip) and
`build_ms`, the last build's duration, as a sizing input. Template items are excluded at Zabbix (`templated: false`).
`GET /api/sensors` still returns the full list from the same cache.

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

**Core Zabbix minor updates (same machinery).** The probe fleet auto-tracks Zabbix minors through
base-image rebuilds, but the core's `zabbix-*` packages come from the per-major-pinned Zabbix apt repo
and are outside unattended-upgrades' security-only origins - so without help the core slowly drifts
behind its own fleet. The §14c file channel closes that gap: the host reporter adds the installed +
candidate `zabbix-server-pgsql` version to `os-status.json`, the Updates page shows "core x.y.z /
candidate / fleet" with a second operator mask (notify-only default), Argus mirrors it to
`zbx-update-window.json`, and a host timer (`argus-zbx-update`, every 5 min) applies
`apt-get install --only-upgrade zabbix-*` in the window - **same major.minor line only** (the repo
pinning makes majors unreachable anyway; the script double-guards) - then restarts `zabbix-server`
(a seconds-long blip the proxies buffer through) and re-reports. **Major Zabbix upgrades stay a
planned manual event**: DB migration on first start + Timescale compatibility, snapshot first.

**Core time (same machinery).** Every schedule above runs on the VM's clock, so the reporter also
carries the VM timezone and `timedatectl`'s NTPSynchronized flag (Debian syncs via
systemd-timesyncd out of the box): Settings shows both, with a warning pill when the clock is NOT
synchronized - a monitoring box with a drifting clock corrupts every timestamp it collects. The
timezone has ONE source of truth: the existing Settings → General → Timezone setting (ARGUS_TZ),
which already drives the app's notification timestamps. When it is explicitly configured (env or
stored - never the built-in UTC default, which must not override a first-boot choice), Argus
mirrors the IANA name to `timezone.json` and a host timer (`argus-tz-check`) applies it via
`timedatectl set-timezone` after validating the name against the local zoneinfo database, then
restarts zabbix-server (long-running daemons cache the zone) and re-reports.

**Golden-image refresh cadence.** Re-run Packer periodically (e.g. quarterly or on each Debian point
release) so newly deployed probes ship already-patched instead of installing months of updates on first
boot. **Major-version upgrades (Debian 13 -> 14) are a deliberate manual / re-image event** - never
unattended.

**Status: implemented (v0.4.32 / probe-vm v0.3.1).** The probe golden image bakes `unattended-upgrades`
(security only) + `needrestart` with a weekly ~03:00 auto-reboot, and an hourly `argus-os-report.timer`
posts its security-update count + reboot-required flag to `POST /api/probes/os-status` (probe-token
auth). `setup-core.sh` installs the same on the core with **auto-reboot off** (it respects the
TimescaleDB 2.28 hold), a host reporter that writes `os-status.json` into the shared self-update dir,
and a reboot watcher that honours the operator window. Argus surfaces per-probe status on the **Probes**
page (the **OS** column + a "N need a reboot" rollup) and on the **Updates** page (Operating systems:
every probe VM, the core's own status and the reboot-window mask; `GET /api/os/status`,
`PUT /api/os/reboot-window`, default **notify only**). The window is mirrored to `reboot-window.json` for the core's host watcher; patching stays
strictly local (Argus never runs `apt` remotely).

## 14d. Self-installing core appliance VM (`deploy/core-vm/`)

The probe golden-image pattern (§14a) applied to the **core**: one Packer-built Debian 13 image with
the entire stack baked - Zabbix 7.0 (server + nginx frontend + agent2), PostgreSQL + TimescaleDB
(pinned 2.28), Docker with the `argus` + `argus-updater` images pre-pulled, and §14c patching in the
**core flavor** (security-only, reboot operator-scheduled). Same base (`generic` cloud qcow2, full
driver set), same delivery (OVA / qcow2 / VHD from a `core-vm/v*` tag Release), same identity strip,
same no-cloud-init model, same systemd-networkd DHCP. Container folders follow
`/docker/<container name>` on both VMs and in the manual install (`docs/folder-layout.md`):
`/docker/argus` (`/data`), `/docker/argus/pki` (`/ca`), `/docker/argus-update` (`/update`), and
`/docker/argus-probe` on a probe VM.

**Nothing instance-specific is baked** - no passwords, no database, no certs. First boot serves a
one-form setup page on `http://<vm>/` (hostname · console keymap · timezone · admin email +
password, with per-role overrides under Advanced) and then configures everything behind a live,
ground-truth progress page (idempotent steps; retry/edit on failure; reboot-safe resume):

1. **system** - hostname/tz/keymap + the local Debian sudo user (console + SSH access; password
   login over SSH for this account only, no docker group).
2. **database** - `timescaledb-tune` for the deployed RAM, `zabbix` role + DB with a **generated**
   password, schema import, TimescaleDB conversion.
3. **pki** - CA (`CN=Monitoring Core CA`) + core server cert; CA mounted RO into Argus so **probe
   enrollment works out of the box**. It lives in `/docker/argus/pki` (root's, readable by the
   container's group), mounted as `/ca` and over `/data/pki`, both read-only, so the container can't
   replace it through its writable `/data`.
4. **zabbix** - `zabbix_server.conf` (DB + TLS/tuning snippet), frontend `zabbix.conf.php` written
   directly (**the browser setup wizard never runs**), nginx `:8080`, php-fpm tz, agent2
   self-monitoring, services enabled.
5. **https** - a server certificate for the VM (hostname + address) signed by the monitoring CA;
   nginx on `:443` proxies to Argus on `127.0.0.1:8081` with forwarded headers; Argus trusts
   `127.0.0.1` as its proxy (seeded in the argus step) and the Public URL defaults to `https://<ip>`.
6. **accounts** - rotate the stock `Admin` password; create the **`argus-svc`** super-admin machine
   user and mint its **API token** (never shown to a human; rotating `Admin` never breaks Argus);
   housekeeping retention (30d/730d/compress 7d) via the API.
7. **argus** - write `/etc/argus-core/argus.env` (token, first-admin seed via the existing
   `ARGUS_ADMIN_*` mechanism, generated `ARGUS_SECRET_KEY`, `/ca` + `/update` mounts), start both
   containers (systemd oneshot + docker-restart pattern from the probe VM), then seed Public URL +
   timezone through the settings API so they stay UI-editable (only the Zabbix URL/token are
   env-locked - the appliance owns its Zabbix).
8. **finish** - scrub the one-time admin seed, park a permanent `:80 -> https` nginx redirect (to the
   Public URL when https, else the VM's own address, never the request's Host), disable the
   first-boot service.

**The setup page asks for a setup code** printed on the VM's console and login banner (the page is
open to the network until setup completes and creates every credential), or read from an attached
`ARGUSSEED` disk carrying `ARGUS_SETUP_CODE=` when there is no console; forms carry a per-boot CSRF
field and answers are `no-store`.

**Credential model:** one administrator password fans out to the Debian user, Zabbix `Admin`, and the
Argus admin (individually overridable); the DB password, the API token, and the encryption key are
machine-generated and never displayed. **The manual path stays first-class**: `setup-core.sh` gained
`SETUP_MODE=image` (repos/packages/patching only) so the appliance and the manual install share one
installer; Option B in the README covers non-Debian distros and split Zabbix/Argus layouts.

**Limits:** first boot needs DHCP (static afterwards = swap the networkd file or use a reservation);
the https certificate is signed by the appliance's own CA (install `ca.crt` or front it with your
own); Zabbix/PG package upgrades remain deliberate `apt` operations on the VM.

**Status: shipped (`core-vm/v0.1.0`, 2026-09-11).** Lab-validated on XCP-NG end to end: one-form
setup completes the whole bring-up, sign-in works, the Zabbix connection is live, and a probe enrolled
and came online over mutual TLS. (Two first-boot bugs found and fixed during lab: Zabbix 7.0 requires
`current_passwd` when rotating the Admin's own password, and the `zabbix_server.conf` TLS snippet must
be appended by matching a self-authored marker, not the substring `TLSCAFile=` which the stock conf
already carries in a commented example.)

## 14e. Backups and restore (core host)

The core backs itself up, and one command restores it onto a new core (the user guide is
`docs/backup-and-restore.md`). The work runs **on the host as root**, since it needs `pg_dump`, the
env files, the CA and the Zabbix config, none of which the distroless, non-root Argus container can
reach; **Argus holds the plan** (Settings, Backups, `internal/server/backup.go`) and shows the outcome.

- **Tools** (`deploy/core/host`, installed by `install-backup.sh`, which `setup-core.sh` runs in both
  modes and `setup-core-patching.sh` runs on an existing core; the core VM image bakes them in):
  `argus-backup` and `argus-restore` (Python 3, stdlib only: python3 is already on every core),
  `argus-backup.timer` (every 15 minutes), `argus-backup.path` (on a request from Argus) and
  `argus-backup.service` (oneshot, idle IO class, `Nice=10`).
- **The plan**: `argus-backup` runs `docker exec argus /argus backup-plan` (a subcommand in the
  image, `backupcmd.go`: it opens the database with the at-rest key, prints the schedule, keep,
  history flag, passphrase and remote target with its credentials as JSON). `docker exec` takes root
  on the host, so the secrets never cross the network or sit in a shared file. The last plan is kept
  in `/etc/argus-core/backup-plan.json` (root, 0600) so backups carry on while Argus is down.
- **An archive**: `argus backup-db` writes a consistent copy of the SQLite database inside the
  container (`VACUUM INTO`; a plain file copy when Argus is stopped); `pg_dump -Fc` of the Zabbix
  database (metric history optional: without it the data of `history*`, `trends*` and the
  TimescaleDB chunks is left out) plus `pg_dumpall --globals-only`; `tar` of the core's files
  (`/etc/argus-core`, `/docker/argus/pki`, `/etc/zabbix`, the nginx TLS front, `/etc/postgresql`, the
  collectors' keys and pins, external scripts, Argus units and scripts, the setup marker, and every
  other folder the Argus container mounts besides `/data` and its update dir, which on a core
  installed by hand is where its CA lives; owners kept by name); `containers.json`, the Argus and
  updater containers' `docker inspect` (on a core installed by hand the secret key and the Zabbix API
  token exist only in their environment; `argus-restore containers` prints them back as `docker run`
  commands); `network.json`, the core's network identity (the addresses on the interface with the
  default route, gateway, DNS, static routes, hostname, DHCP or fixed, and a copy of the network
  settings files for reference), which `argus-restore network` sets on a new core (systemd-networkd:
  a `05-argus-restored.network` matched by the new card's MAC; other managers get the settings
  printed); a `manifest.json` with versions, the Argus container's mounts and a SHA-256 per part.
  The shared update dir is resolved, not assumed: `ARGUS_STATE_DIR` when it exists, else the folder
  the Argus container mounts as its `ARGUS_UPDATE_DIR` (`install-backup.sh` writes that into the units). All in one `tar`, encrypted with
  `gpg --symmetric` (AES-256, the passphrase through a pipe) when a passphrase is set. Local copies in
  `/var/backups/argus` (0700), newest N kept.
- **Export** (only encrypted archives; a passphrase is required to set a target): SMB and NFS are
  mounted for the run (the SMB password through a root-only credentials file); rsync over SSH uses an
  ed25519 key Argus generates (the public half shown in Settings, `accept-new` host keys) and deletes
  with an rsync filter, so an `rrsync`-restricted account works; S3 goes through `rclone` configured
  from the environment only. Each run uploads what the target lacks and keeps **this host's** newest N
  there, never touching other files, so a rebuilt core can't wipe the history. Tools missing on an
  older core are installed on first use (the NFS client's `rpcbind` is switched off again for NFSv4).
- **Status** comes back in `backup-status.json` in the shared update dir (0644): the last run, the last
  good one, the local archives and free space, the last export and target test, the next due time.
  Argus shows it and raises system notices when a run or an export fails, or when the last good backup
  is over 36 hours old. "Back up now" and "Check the target" drop `backup-request.json`, which the
  path unit picks up at once.
- **Restore** (`argus-restore restore ARCHIVE`, or `inspect` to check one): decrypt, unpack, verify
  the checksums, compare Zabbix, PostgreSQL and TimescaleDB versions with the manifest (a TimescaleDB
  dump restores only onto the same extension version; `--force` overrides), confirm, stop
  argus-updater, argus-core and zabbix-server, put back the files (except `/etc/postgresql`: the new
  VM keeps its own tuning), fix owners, recreate the `zabbix` database (globals, `CREATE EXTENSION`,
  `timescaledb_pre_restore()`, `pg_restore -j`, `timescaledb_post_restore()`, analyze), put back the
  Argus database, start everything. `--only files|zabbix|argus` restores one part. Probes reconnect
  unchanged: the CA and the Zabbix server certificates come back with the files.
- **Tests**: `deploy/core/host/tests/test_host_scripts.py` builds, encrypts, exports, decrypts and
  restores real archives with the core's own commands faked; CI runs it on Linux.

## 15. Tech stack (confirmed)
- **App name:** **Argus.** Split across three repos: **argus-core** (this repo - the app in `argus/`, docs, core deploy kit), **argus-probe** (the probe Docker image + self-configuring golden VM), and **argus-updater** (the core self-update sidecar). Image names stay `argus` / `argus-probe` / `argus-updater` regardless of repo names.
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
  the client. Do **not** filter a full client-side census - shipping thousands of items to the
  browser does not scale (the status pills fetch counts only, see section 14b).
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
- **Central core-host re-point.** The check-in response also carries the current **`core_host`**
  (the `ARGUS_PROBE_CORE_HOST` setting, same source as enrollment). The probe re-fetches it once at
  startup and applies it as its Zabbix `Server=`, so changing the setting re-points the whole fleet
  at each probe's next restart - no re-enrollment. Fail-safe: an unreachable core or an older Argus
  leaves the last-known host in place; an explicit `ZBX_SERVER_HOST` on the probe always wins.
  Prefer an **IP** for this value - the proxy re-resolves it on every data send, so an FQDN floods
  DNS.
- **Target.** Argus holds a dashboard-settable target in `app_meta` (`GET`/`PUT /api/probes/target`,
  admin): `latest`, or an exact pin in the **decoupled probe scheme** - `7.0.29-r1`, *not* app
  semver (see the probe-image versioning in `deploy/README.md`). `/api/proxies` reports each
  probe's version / target / `update_status` (`unknown | tracking | current | outdated`).
- **Manual path (always available).** A drifted probe without a sidecar shows a
  `docker pull … && docker restart …` command on the Updates page - no Docker socket involved.
- **One self-update model: proxy + updater sidecar (v0.4.30).** Every Argus-driven probe is **two
  containers** - the proxy (a pure reporter; never gets the socket, no `docker-cli` in its image) and
  the shared **argus-updater** image in `probe-watch` mode. The sidecar holds the socket and recreates
  the proxy via the Docker Engine API on an **Update** (`POST /api/probes/{name}/update`, handed
  to the sidecar once at its next check-in as `{"update":"<tag>"}`) or a fleet-target change, cloning
  the proxy's config onto the new image and **rolling back on any failure**. This is the same
  principle as the core's updater - the socket is isolated to the minimal sidecar, never on the
  public/service container. The wizard's **Docker run**, **Compose**, and **VM** tabs all emit the two
  containers; on the VM they're two systemd units (`argus-probe` + `argus-updater`). Deploy a sidecar
  by hand with `-e ARGUS_UPDATER_MODE=probe-watch -e ARGUS_PROXY_CONTAINER=<name>` + the socket +
  `-v <proxy-data>:/probe:ro`. (The socket-on-proxy `ARGUS_PROBE_SELFUPDATE` path and the compose
  `probe-poll` mode were retired in favour of this one model.)
- **Every hand-out carries a digest.** Tags stay tags (`latest`, `testing`, a version), but a tag is
  a pointer the registry can move, so the core resolves what the tag points to when it hands it out
  (`target_digest` / `update_digest` / `updater_update_digest` at check-in; `digests` per tag in the
  core's `request.json`; `digest` in `updater-request.json`; GHCR `Docker-Content-Digest`, cached
  5 minutes) and the updater compares the pulled image's repository digest against it before
  recreating anything (`pull_verified` in `lib/recreate.sh`, also for the self-update helper image).
  A mismatch is refused and logged, the container left untouched. No digest (an older core, the
  registry unreachable at hand-out) means the tag is applied unverified, with a log line.
- **The updater updates itself.** A long-running updater can't `rm -f` itself, so on request it spawns
  an ephemeral `argus-updater --rm` copy in `probe-recreate` mode targeting its own container (the
  self-update **primitive**). Argus drives it: the sidecar reports its own version at check-in
  (stored as `probe_agents.updater_version`), and its **Update** on the Updates page queues a
  one-shot (`POST /api/probes/{name}/updater-update`) handed back as `{"updater_update":"<tag>"}`.
  A check-in hands out one update at most, the proxy's first: the sidecar's own update replaces it,
  and could do so while it is still recreating the proxy, so the other waits a check-in.
- **One image, one engine.** The core self-updater and both probe roles are the same image,
  `ghcr.io/g-guglielmi/argus-updater` (its own version line), sharing one recreate engine
  (`lib/recreate.sh`) selected by `ARGUS_UPDATER_MODE` (`core` | `probe-watch` | `probe-recreate`) -
  so pull → config-clone → verify → rollback can never drift. **Two-reporter model:** the proxy
  reports its version but omits self-update capability, while the sidecar advertises capability but
  reports no proxy version - the check-in fields are sticky (an omitted field keeps the stored value),
  and one-shots are handed only to a capability-advertising caller, so the two never clobber each
  other or race. See the [argus-updater](https://github.com/g-guglielmi/argus-updater) repo.
- **Updates show their steps.** The Updates page follows a core update and a sidecar self-update step by
  step until it ends. The core update's `status.json` keeps every message in `steps`; a sidecar
  self-update is remembered by the core in `updater-job.json` (the request file is consumed when the
  sidecar picks it up) and reported by the sidecar and its swap helper in `updater-status.json`
  (`lib/job.sh`: picked up, pulling, the swap, verify, done or why not; a sidecar already on the
  newest image says so instead of swapping). A sidecar older than 0.2.12 reports nothing: its new
  version is the outcome, and three minutes without one read "no word back". A finished update stays
  shown for 5 minutes, a failed one until it is closed (`POST /api/update/updater/dismiss`). The
  sidecar row reads like the core's: the newest published argus-updater (the 3-hourly lookup, or
  now with Check for updates) against its version, and **Update** only when a newer one is out.
- **One page for every update.** The admin **Updates** page has a single **Check for updates** and
  three sections that read the same way: each component is a row (or a table cell) with its version,
  one status pill (up to date, available, queued, updating, failed) and its **Update** button, plus
  the step log where the updater reports steps. **Argus core**: the core (channel switch, release
  notes), its sidecar, the collectors. **Probes**: the fleet target and every probe's proxy and
  sidecar, with **Update all** for every one that is behind. **Operating systems**: the core VM (patch
  state, reboot window), the core's Zabbix (minor-update window), each probe VM, and the newest probe
  VM image. The Probes page keeps the versions as read-only status in the same words; a probe that
  is behind links to Updates (admins only: helpdesk sees Probes, not Updates).
- **The core host's collectors ride the image.** The core's Zabbix server is a host package, so the
  collectors (external checks) it runs for the hosts it monitors live in the host's
  `/usr/lib/zabbix/externalscripts`, out of reach of a container update. The Argus image carries them
  (`/collectors`, from `deploy/core/externalscripts` as a named build context, labelled
  `io.argus.collectors`) and `/argus install-collectors <dir>` copies the ones that changed
  (atomically, 0755, nothing else in the folder touched). The `core`-mode updater runs that same image
  once - as root, `--network none`, read-only, only that folder bound in - whenever the core's image
  changes, a day after a success (puts back a deleted or edited collector) and ten minutes after a
  failure, and reports in `collectors.json` (`ok` | `failed` | `skipped`, with the version and what
  it wrote); the Updates page shows it. The long-running core never gets write access to a folder
  the Zabbix server executes from. `setup-core.sh` still installs them on a fresh core.

**Tradeoff acknowledged.** Any automatic in-place container update needs Docker socket access at
the site (the same mechanism Watchtower uses). The win over Watchtower is **central version control
+ fleet visibility + no third-party container + you decide when**. The socket is isolated to the
minimal **updater sidecar**, never the proxy. Unraid probes use their own native auto-update (Argus
shows drift + the manual one-click command).

---

## 18a. Probe health (implemented)

**What.** Each probe gets one Argus-managed host, **Probe <site>** (technical name
`argus-probe-<site>`), in its site group, **monitored by that proxy**, with **no interface** and the
**Argus Probe Health** template. Zabbix runs internal checks on the proxy that monitors the host, so
its items measure the proxy itself:

- `zabbix[uptime]` - with a **strict** `nodata()` pair (warning / high after
  `{$PROBE.NODATA.WARN}` / `{$PROBE.NODATA.HIGH}` seconds). Strict mode keeps nodata() from being
  held back while the proxy is away, so "probe unreachable" fires while it is offline. This is the
  alert that was missing: the Probes page showed offline, but nothing notified.
- `zabbix[proxy_history]` (values waiting to be sent), `zabbix[queue,10m]` (items running late),
  `zabbix[wcache,history,pused]` / `zabbix[rcache,buffer,pused]` (cache use) and
  `zabbix[process,<type>,avg,busy]` per process type (grouped as one **Process load** sensor that
  headlines the busiest).

Every sensor has a warning and an error threshold, editable on the Thresholds screen and per host.

**Lifecycle.** `EnsureProbeHosts` runs after the startup template import and after each
enrollment; it is idempotent. It never creates the site group: a proxy whose group is missing (the
operator deleted it, or the proxy isn't named `proxy-<site>`) is skipped with a log line. Deleting a
probe deletes its Probe host first (Zabbix refuses to delete a proxy that still monitors hosts).

**Class.** `probe` is an **internal** class: never offered in the Add-device, discovery or
change-class pickers, refused by the create and change-class APIs, no Base Ping (no address) and no
add-ons. The Probes page's Health cell shows the host's worst open problem (ok / warning / error)
and links to it.

## 18b. Status pages

A read-only dashboard for a wall screen, opened with a secret link instead of a login (admin
**Status pages** screen; `status_pages` table).

- **Link:** `/status/<token>`, a 256-bit random token, looked up by its SHA-256 and kept encrypted at
  rest (the same cipher as channel secrets), so an admin can copy it again (**Show link**, `GET
  /api/status-pages/{id}/link`); pages made before that only have the hash and need a new link. Opening it sets an `argus_status` cookie (HttpOnly, SameSite=Strict, Path=/status, Secure as the
  session cookie is, lasting until the page expires or ~400 days) and redirects to a clean
  `/status`, so the token doesn't sit in the address bar, history suggestions or screenshots.
  **New link** (rotate) or deleting the page kills it, cookie included.
- **Limits:** optional allowed networks (CIDRs, checked against the client IP as resolved through the
  trusted proxies, see section 4) and an optional expiry; a page shows only its sites (host groups, a root
  covering its subgroups).
- **Content:** `/status/data`, built from the sensor census (the rows behind the pills and Overview;
  hidden and paused hosts left out), cached 20 s per page: every sensor in error, in warning or
  acknowledged, ordered like the Overview (priority, severity, host, sensor), with site group, the
  reason (worst problem + severity), the reading, a 2 h trend series (the /api/spark data, up to 200
  rows), the priority and since, plus the counts. Problems with no sensor (an unreachable agent or
  SNMP endpoint) have a census row of their own ("Zabbix agent" / "SNMP", flagged synthetic: no chart,
  can't be paused or hidden), so the pills, the Overview and the status pages all count them. A
  sensor a down master holds (`held_by`) folds into its master's row (`foldHeld`): the row says
  "holding N other sensors" (`holds`), and the page's counts leave it out (`held` counts them), so a
  dead device is one row and one error. One whose master isn't on the page keeps its own row. No addresses, credentials or settings. The page is a standalone HTML
  file (not the SPA), dark and sized for a TV: a slim bar with Errors / Warnings / Acknowledged pills
  that switch the list (the address decides: `#acknowledged` / `#warnings`, which survives the link's
  redirect, else errors, so a plain `/status` always opens on errors; the pills stay grey until the
  first data), the list itself ("All systems operational" when there
  are no errors), paged every 15 s when it doesn't fit, refreshing every 30 s, flagging a lost
  connection, and reloading itself every 6 h.
- **Note:** an admin can pin a note on a page (**Add note** on its card; `PUT` / `DELETE
  /api/status-pages/{id}/note`): up to 500 characters of plain text (line breaks kept, control
  characters dropped), a style (info, warning, problem) and an end (an hour to 90 days, or until
  removed). It shows in a bar under the header in its style's colour, with when it was posted and
  until when; who posted it is on the admin card only. Kept on the `status_pages` row (`note_*`).
- **Maintenance:** a strip under the note lists the windows touching the page's hosts (section 9): the
  ones in progress with their end, then up to three starting within a week, each with how many of the
  page's hosts it covers (named when five or fewer); a problem row whose host is in a window says so
  (`maintenance` on the issue). From `maintenance_windows`, computed with the page's data.
- **Uptime:** the header shows the average 30-day uptime of the page's hosts, and the calm screen
  ("All systems operational") lists the ones under 100% over 30 days, worst first, at most ten, with
  their 7 and 30 day figures (section 7b; from `uptime_days`, so a page of hundreds of hosts stays cheap).
- **Headers:** `Referrer-Policy: no-referrer`, `X-Robots-Tag: noindex`, `X-Frame-Options: DENY` + CSP
  `frame-ancestors 'none'`, `Cache-Control: no-store`. The routes sit outside `/api`, so the cookie
  opens nothing else.

## 18c. Probe process autoscaling (implemented)

**Why.** A Zabbix proxy starts a fixed number of each process kind (ICMP pingers, pollers, trappers,
history syncers, preprocessing workers, the async agent / SNMP / HTTP agent pollers) and reads the
numbers only at start. A site that outgrows them queues its checks and the "processes busy" warning
fires; one sized by hand stays wrong as the site changes. The Probe health host (18a) already
records `zabbix[process,<type>,avg,busy]` for every kind, so Argus sizes them.

**Rule** (`internal/server/autoscale.go`, every 15 minutes, per probe):
- Only counts that have run for 6 hours are judged (the clock restarts whenever they change), and
  only while no earlier change is still waiting to be applied.
- The load is the busiest hourly average (trends) since the counts started, at most a day back, and
  at least three hours of it.
- Busiest hour at or above 60%: raise to `ceil(count x busy / 50)` (at least one more), so the
  busiest hour lands near 50%. At or below 20%: lower the same way. Never below the image's default
  (5 pingers, 5 pollers, 5 trappers, 4 history syncers, 16 preprocessing workers, 1 of the rest),
  never above a ceiling per kind (50 pingers, 100 pollers, 8 history syncers, 64 workers, 10 to 20
  for the rest): past it the site needs a second probe, not more forks. History syncers stop at 8
  because they write the proxy's SQLite buffer.
- A count set on the container (`ZBX_START*`) wins and is left alone; the probe reports which.
- **Every raise is judged.** At the first evaluation after a raise applied, its busiest hour must
  have come down by at least a third of what the raise predicted (5 to 8 pingers at 72% predicts
  45%). If it didn't, the limit was somewhere else: the count goes back and the kind is **held**
  (not raised again) until the probe's usable CPU count changes or an admin presses **Try again**
  on the Processes panel.
- **Short on CPU.** The probe reports its CPU count, a container CPU limit (cgroup v2 `cpu.max` or
  v1 CFS quota) and its load average at every check-in; Argus keeps a day of them (`probe_load`).
  When the busiest hour's load average reached the CPUs the probe can use (at least three hours of
  reports), nothing is raised: more processes would only queue for the CPU. Lowering still works. A
  change in the usable CPU count restarts the settle clock. Inside a container the load average is
  the whole Docker host's, so the advice for a container probe names the host.
- Short on CPU or holding a kind, the probe gets one system notice while it lasts ("Probe <site>
  may be short on CPU"): what Argus saw, and for a VM probe "give the VM more vCPUs, or split the
  site", for a container "the Docker host may be short on CPU, or the site needs a second probe".

**Exchange.** The proxy reports the counts it started with (`procs`, `procs_pinned`) at every
check-in; the first report seeds Argus's target with them, so nothing changes until the load has
been watched. Every check-in answer carries the target (`procs`); the probe's start-time check-in
saves it to `procs.env` on its data volume (root-owned, read as data, each value checked) and starts
Zabbix with it, so a start without Argus keeps the last counts. The updater sidecar advertises
`restarts` and, while a change waits, gets a one-shot `restart_proxy` (at most once per 6 hours per
probe) and restarts the proxy container through the Engine API: a few seconds down, unsent data
kept in the proxy's buffer, well under the 3-minute "not reporting" alert.

**Mode** (`ARGUS_PROBE_AUTOSCALE`, Settings -> Probes): `restart` (default: the sidecar restarts the
probe to apply a change), `next-restart` (a change applies whenever the probe next starts: an
update, a reboot), `off` (no evaluation, no counts handed out; probes keep what they have). Each
change is logged, shown on the Probes page (Processes: running count, busiest hour, target) and
told once as a system notice.

## 19. Parking lot / future
- **Android native app** with push notifications (device registers with Argus → notifier delivers
  via a "push"/FCM channel) - the planned last step (ROADMAP §I). iOS undecided (would need APNs).
- Token-based enrollment service (Phase 1 backend) + "Add probe" wizard (Phase 4/6 UI).
- Golden probe **VM template** (Packer) + cloud-init for scaled/work rollout (Phase 6).
- Sizing pass before the ~6000-sensor work deployment.
```

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
  setter, so plain-HTTP pages and sibling subdomains can't plant one), and only that name is read:
  a bare-name cookie seen while secure is ignored and expired. Sessions end at once when the user
  is disabled, when an admin resets their password, and (all but the current one) when they change
  their own; a replaced password takes the user's API tokens and outstanding reset links with it
  (`RevokeUserCredentials`), so a token minted from a stolen session doesn't outlive the change.
- **Sign-in attempts:** limited per client address (IPv6 by its /64), per socket peer (so a LAN
  client that names its own address in `X-Forwarded-For` is still counted by its connection) and
  per account. There is no lockout: over the account's limit, every attempt on it waits its turn
  and three seconds before the password is checked, and a wrong one is answered 429; the right one
  still signs in. The re-authentication prompts, turning two-factor off and changing the own
  password count wrong attempts per user the same way.
- **Second factor:** the password step returns a 10-minute challenge, `/api/login/totp` completes it;
  a TOTP code is accepted once: the time step the code was made for (not the server's clock step,
  which would let it pass again one step later) is claimed per user in one statement
  (`ClaimTOTPStep`), so a racing second use loses. A passkey logs in without the TOTP prompt, so
  user verification (PIN, biometric) is **required** at registration and at every login, an
  assertion whose signature counter didn't advance (a cloned credential) is refused, and a ceremony
  is consumed in one statement. Anything that adds a way into the account or takes one away takes
  both factors (`reauth`): adding a passkey, making new recovery codes, creating an API token and
  turning TOTP off ask for the password and, with two-factor on, a current code.
- **Links that leave Argus** (reset emails, the enroll command shown to admins) are built from the
  Public URL, else from the request's host only when that host is allow-listed or loopback; a probe's
  check-in URL comes from the Public URL alone (probes write it into their own configuration, and
  derive it from their enroll URL when there is none). "Probe core host" is validated as `host[:port]`.
- **Images** are built from base images pinned by digest and signed keyless with Sigstore by the
  build workflow (the signature names the workflow; `cosign verify` with the repository's identity
  proves an image came from it). The update hand-out pins digests, and the updater (0.2.13 and
  later) verifies both before a container is recreated: the pulled image must carry the digest the
  core handed out, and a signature from the one workflow allowed to build it (argus-core's
  `build.yml`, argus-probe's `probe-image.yml`, argus-updater's `updater-image.yml`). An image
  pushed with a registry token, or an unsigned one, is refused with the reason on the Updates page.
  The three repositories' images carry signatures from core v0.8.13, probe r31 and updater 0.2.13.
- **The status page** allows only its one inline script, by hash; the acknowledgement page from an
  email link allows none; a disabled account answers a sign-in like a wrong password.
- **The Zabbix API token** is a super-admin credential sent as a Bearer header on every poll, so from
  another host the API must be https: the core serves the frontend and API over TLS on `:8443`
  with the VM's certificate (signed by the monitoring CA; the appliance from first boot, a core
  installed by hand through `argus-web-guard`, 14c), the client trusts that CA
  through `ARGUS_ZABBIX_CA_FILE` (default: the enrollment CA's `ca.crt` it already mounts), and a
  plain-http URL to a host that isn't the container's own is a warning at start.
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
  pinned image's own. A weekly run repeats them and adds Trivy over the three published images (what
  they carry) and over the three repositories (misconfigurations, committed secrets); CodeQL and
  Dependabot watch the repositories through GitHub's own alerts.
- **Passkey caveat (accepted):** WebAuthn RP IDs must be a domain, not a bare IP.
  → Passkey login works via `monitoring.example.com`; direct **private-IP** access
  (troubleshooting) falls back to **password + MFA**.
- **Passkeys behind a password manager extension.** Bitwarden, 1Password and the like take over
  `navigator.credentials` on every page and answer passkey calls through a script of their own in
  the page. A tab left open across an extension update or restart keeps that script but loses its
  link to the extension: the next passkey call gets no answer and ends in a plain `Error`
  ("Something went wrong."), where the browser itself always throws a `DOMException`. `webauthn.ts`
  turns that into `PasskeyBridgeError`; the login screen reloads the page once by itself on it (a
  flag in `sessionStorage`, this tab only) and starts the sign-in again on the fresh page, and shows
  the reason instead when the fresh page fails the same way. Adding a passkey in Account shows the
  same reason with the advice to reload. Password sign-in never goes through the extension.
  A login page left open across a core update runs the bundle it loaded, fixes included, so before
  a passkey sign-in the page compares its script's hashed name with the one `index.html` (served
  no-cache) names now, and reloads itself first when they differ; the fresh page then starts the
  sign-in by itself.

---

## 5. Device classes & templates

Every host gets the **Base** template (Ping - latency + loss, always) and, optionally, the
**HTTP/HTTPS** add-on (`Argus HTTP Endpoint`: `argus_http.py` on the probe fetches every URL in
`{$HTTP.URLS}` at once - full URLs, hosts without a scheme (given `{$HTTP.SCHEME}`), or paths on the
host's address with `{$HTTP.SCHEME}` / `{$HTTP.PORT}` (a blank port is the scheme's own, 443 or 80),
a blank list being the host itself, each with
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
the collector's command line). Add-ons can name the classes they belong to: the **Speedtest** and
**Common SaaS** add-ons are offered only on a site's Probe host (the internal probe class, which gets
no others). **Speedtest** (`Argus Speedtest`: `argus_speedtest.py` on the probe, every
`{$SPEEDTEST.INTERVAL}`, against Cloudflare's speed test, stdlib only: download and upload over
8 streams after a 1 s warm-up, the idle round trip less Cloudflare's own time from its
Server-Timing header, jitter, the round trip while each direction is busy, the public address and
Cloudflare site from its `/cdn-cgi/trace` and the network announcing the address from RIPEstat;
speeds in Mbps so their thresholds read as such, round trips in seconds; a run it couldn't measure is
a reading with its reason, `speedtest.ok` 0 and `speedtest.error`). It asks only for what Cloudflare
serves any client, with no headers posing as its page. Cloudflare limits how much one address
downloads from it in an hour (HTTP 429 with a Retry-After of about an hour; its zero-byte round trips
still answer), so a run moves at most what one test on its own page moves on a fast line (its
library's steps add up to about 970 MB down and 300 MB up): 720 MB down and 300 MB up shared among the
streams, downloads of at most 90 MB a request (it refuses 100 MB), one upload a stream (it refuses
1 GB), or less when `{$SPEEDTEST.SECONDS}` runs out first on a slower line. The rate is read from the
warm-up (1 s, or a quarter of the data moved, whichever comes first) to the first stream done, while
all of them fill the line; the round trips under load are timed every 0.25 s meanwhile. A direction
any of whose streams was refused reports no speed, since the others would read low, and the 429 is
said in the reason. `{$SPEEDTEST.ENGINE}` = `ookla` runs Ookla's Speedtest
CLI instead (Speedtest Tracker's engine): Ookla's own program under Ookla's terms (personal,
non-commercial use), so Argus doesn't ship it. A field option can carry a third party's terms
(`MacroSpec.Terms`): the form asks to accept them when the option is chosen, the save carries
`accepted`, the server refuses the option without it unless the host already has it (`termsRefusal`)
and the change log records "Ookla's terms: accepted" with who. Once set, the probe's first run
downloads the CLI from install.speedtest.net (version 1.2.0, its SHA-256 pinned per processor as
nixpkgs pins it; the probe image build checks the pins against Ookla's download before a release)
into `/var/lib/zabbix/ookla` on the probe's data volume, and runs it with `--accept-license
--accept-gdpr --format=json` (`{$SPEEDTEST.SERVER}` an optional server ID). Its result maps onto the
same sensors (bandwidth bytes/s x 8, `ping.latency`/`jitter`, each direction's `latency.iqm`, the
server's sponsor, city and ID as the test site) plus **Packet loss** (`speedtest.loss`, hidden until it
has a reading: only Ookla measures it, and only against servers that do; `hideUntilRead`). Fields can
show only for one engine (`MacroSpec.ShowIf`): seconds and connections for Cloudflare, the server ID for
Ookla. A probe whose collector predates the engine (it prints no `engine`) is caught by the template:
`speedtest.ok` reads 0 and the reason says the probe updates itself.
**Common SaaS** (`Argus Common SaaS`: the HTTP collector `argus_http.py` with the probe's
`{$SAAS.URLS}`, each entry named with `#name=`, any answer short of a server error counting
(`{$SAAS.EXPECT}` 200-499), one group per service under **Cloud services** like the Web checks; the
catalog of services is `provision/saas.go`, the template's default list is generated from it and a test
keeps them equal). **The vendor's own word** (`vendorstatus.go`, OneUptime's external status page
monitor, as context only): a catalog service whose vendor publishes a machine-readable status carries
its source (an Atlassian Statuspage's `/api/v2/incidents/unresolved.json`: Cloudflare, Zoom, Dropbox,
GitHub; a page with only `/api/v2/status.json`'s indicator: ChatGPT; Slack's own API). Every 5
minutes the core reads the sources of the services some site checks (the `saas.up[<id>]` and
`saas.time[<id>]` items, the id being the first 10 hex of the URL's SHA-1 as `argus_http.py` gives it,
so a renamed service still finds its vendor; no add-on, no request), keeping a word 20 minutes when
the page stops answering. Only open incidents count, never the headline: Cloudflare's reads "Partial
System Outage" most days from regional rerouting with no incident open, Zoom's "maintenance" for weeks
of no-impact work; the worst open incident (impact, name, since when, how many more) is the word.
It never alerts by itself. It shows under a Cloud services problem in the lists ("GitHub's status
page: no incident open", or the incident in its tone, with a link to the page), under the service's
row on the host page while the vendor has an incident open or the site's own check fails, and as a
line in the service's alerts, reminders and RESOLVED (the webhook's `vendor`). Microsoft 365, Google,
AWS and Apple publish none (Microsoft 365's service health needs Graph and an app registration: a
later step). Zabbix runs a new item within 60 seconds of its creation (since 6.0.2), so a
speed test every 6 hours shows its first reading a minute after it is turned on; Argus doesn't run it
as well (it once did, and that made two tests in a row). A sensor's or group's **Check now**
(`POST /api/items/check`, admin and helpdesk) runs the items that do the reading (a dependent item's
master, up the chain, `ReadingItems`), each once. A collector that isn't running alerts for itself;
its readings whose steps only discard stay quiet meanwhile (one alert, not one per reading). A
discovered SNMP sensor whose index stops answering ("No Such Instance"/"No Such Object":
`renumberedInstance`) was renumbered or removed: a VPN tunnel (NetBird, WireGuard) recreated by a
plugin update comes back as the same interface at a new ifIndex. The sensors are keyed by interface
name, so the notifier runs the host's discovery rules at once (`rediscoverRenumbered`, at most every
5 minutes a host) instead of waiting for the hourly run; discovery repoints the same sensors (history
kept) or, for an interface really gone, disables them (Zabbix's default for lost resources). The
alert waits `rediscoverGrace` (10 minutes) meanwhile; one still failing past it alerts as usual.
A speed test reading a run didn't produce is discarded by its script returning `null` (Zabbix 7.0
discards that without an error; a throw left the sensor "not supported" in practice despite its
"discard value" handler), and the speed test's readings (`measuredByRuns`, dependents by their own
fields) never alert as stopped collecting: a failed run is "Speedtest could not run" with why, and a
collector that can't run alerts for itself. Round trips are stored in seconds to the microsecond. On the Probe host the unlisted categories read Probe, Internet, Cloud services.
The speed test's charts are run charts (`buildMultiPlot`'s and `buildPlot`'s runs mode, for the
Internet category): its readings are dependent items of one master, so a run stores them all at the
master value's timestamp and nothing is bucketed; each run is a dot, lines join only runs next to each
other, a failed run (`speedtest.ok` 0) is a red ✕ drawn by a draw hook rather than an interpolated band,
and the axes start at 0. Ranges are 2d to 1Y, 7d by default; `GET /api/items/{id}/history?runs=1`
reads raw history up to 90 days (`runsRawMax`, the template's history) instead of hourly trends.
`GET /api/hosts/{id}/speedtest/runs?range=` joins a host's speed test readings on that timestamp into
runs, newest first, with the reason of a failed one and the test site in effect (stored only when it
changes), for the Runs list under the Speed chart and the reasons on hover; past 90 days it reads
trends (hourly averages, no reason or site). A downtime or failed band never gets an axis of its own,
and a push job's Failed band is stepped (failed until the next run). Sparklines of a sensor measured
by runs read a week (`measuredByRuns`, `runsSpark`) instead of two hours; the Speed row's is a
two-line `PairSpark` (download and upload on one scale, one step per run from the runs endpoint, a gap
where a run didn't measure one of them, no fill). A host page sparkline with thresholds is banded by
value like its chart (`Spark`'s thr: an SVG gradient with hard stops at the threshold heights, the
accent colour in range), on single rows and on groups by the thresholds of the one reading their
sparkline draws (a summed In + Out has none); the Overview lists keep the state colour (their rows
carry no thresholds).
**Push sensors** (`Argus Push`, docs/push-sensors.md) are a job's
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
the set on startup, so templates track the app version; each file is imported on its own, so one
Zabbix refuses doesn't hold the others back or stop a host being added; the failure is shown on the
Updates page with its reason and tried again every 15 minutes until it goes through, and a test
checks that no template puts an item's fields in a discovery rule or the reverse). Thresholds are Zabbix **user-macros** carrying the §6 defaults, overridable
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

**Sensor notes.** Admins and helpdesk can leave a note on a sensor with an open problem (the ⋯ menu:
**Add note**, **Edit note**, **Remove note**; `PUT` / `DELETE /api/sensors/{key}/note`, up to 500
characters). It belongs to the sensor, not to a Zabbix event: `sensor_notes` is keyed by the item id
(or, for an Argus-raised row with no sensor, an unreachable interface, by its problem id), so it
survives the warning-to-error handover. It doesn't acknowledge anything: the colour and the reminders
stay. It shows under the sensor's reasons in the lists, on its own line on the host page (once for a
group whose channels share it) and on the status pages (without the author), and goes out with the
sensor's alerts, reminders and RESOLVED in every channel (`note` / `note_by` in the webhook). The
notifier clears it once the sensor has had no open problem for the alert delay (at least the hold
grace), so a blip keeps it, and only when it could read every open problem's trigger. Cleared notes
stay for the incident history, which shows the note an incident's sensor had while it was open;
they're pruned with the Argus incidents (a year).

## 7b. Uptime and incident history

**Uptime** (`internal/server/uptime.go`). An up/down sensor reads 1 while up and 0 while down (ping,
a TCP/HTTP(S) service check, a collector's reachable flag, XCP-NG's login flag, a DNS name
resolving), so its average over a period is the share of checks that found it up. 24 h comes from
the raw history; 7 and 30 days are calendar days in the Argus timezone (today and the days before
it), summed from Zabbix's hourly trends weighted by their sample counts, plus the raw values of the
hour the trends don't cover yet. A host's uptime is measured on its master sensor when that is an
up/down one, else on its ping, else on a collector's reachable flag.
**Exact hours** (`exactUptime`): Zabbix keeps the hourly average of a sensor stored as whole numbers
(value type 3, the ping among them) rounded down, so an hour with one missed check in sixty reads 0,
as if down all of it. Every hour of such a sensor that reads under 1 is read again from the raw checks
(an hour few apart in one read, a month of flapping in one), while the raw history still has them, as
the share of checks that found it up; older hours stay as read. The host card, the status pages and
the reports all go through it; days stored before it were dropped once to be read again.
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
ones kept a year, for the reports of section 7k). A host card ends with its last 30 days, folded (`GET
/api/hosts/{id}/incidents?days=30`), and a drilled-down sensor with its own history, open
(`&items=<its id, or every channel of its group>`: Zabbix is asked for those sensors' triggers only,
so a busy host's other incidents can't crowd them out); the **History** page lists the fleet's, 24 h / 7 / 30 / 90 days,
Errors + Warnings or Errors only, filterable by host, sensor or reason, and by probe and group
(`GET /api/incidents?days=N&probe=<proxy id, 0 = the server>&group=<name>`, each repeatable; a group
covers its subgroups, and the filter narrows within the user's sites). **Export CSV** saves the list
as shown. Zabbix's events follow its housekeeping retention (Settings, Data retention).
**Uptime targets** (`uptimetarget.go`, `uptime_targets`): a guaranteed uptime per calendar month (99.9
%), set on a site (site info, for its hosts) or on a host (host settings, **Uptime target**: its site's,
none, or its own; saved at once, logged in Changes). The month's downtime is measured like the host's
uptime (its master sensor, else its ping): from the hourly trends (`exactUptime` where an hour wasn't
all up), an hour partly down spread evenly, the raw checks for the hours the trends don't cover yet;
time in a maintenance window covering the host counts as up (planned work isn't held against the
target). The allowance is the downtime the target leaves in the whole month (99.9 % of October: 44
min). The host's uptime band shows the downtime still allowed and the share spent; the push channel
(section 5) carries the share to the host's probe, where the Argus Push template's **Downtime allowance
used** (%) warns from `{$UPTIME.ALLOWANCE.WARN}` 75 and errors from `{$UPTIME.ALLOWANCE.HIGH}` 100 (both
in Thresholds), so a host about to miss its target alerts before it does. A report without a target of
its own holds each host to its monthly target ("Under target", the day tones); a status page's hosts
under 100 % say their target this month (missed, or the share of the allowance used). Kept 5 minutes
per host.

**One timeline per incident** (`timeline.go`, `incident_log`, kept a year; OneUptime's incident
timeline): an incident row on History (and a host's History tab) opens onto what happened to it in
order (`GET /api/incidents/{id}/timeline`, anyone who sees its host). The notifier records when it
opened (severity and name, at the incident's start), each channel it alerted at once, escalated to
(its Notify after) or reminded, and a send that failed with the channel's error, a hold behind a down
master (once per hold), a wait for a maintenance window (once per wait) and a problem heard of late
(section 7, masters: "Reported late", with how long after it began), the channels told of an
acknowledgement, and the end with how many channels heard (or, for a problem heard of late that
closed while it waited, that nothing was sent); the handlers record each acknowledgement
(who, from the alert link too, and the note), its removal, and the notes left on the sensor. Channels
read as "IT team (Telegram)", a personal one as its owner's. Incidents from before 0.9.9 have none.
Hiding a sensor says its problems aren't news: a problem whose sensors are all hidden (`sensorsHidden`)
is left out of the Overview, the status pages, the pills and the alerts, and of the host's badge,
count and colour in the tree too (`handleHosts`); the host's own page still lists it, dimmed under
the problems to handle and marked "hidden sensor" (`hidden` on the problem), so it isn't lost. A
trigger over several sensors stays news while any of them is not hidden. The history leaves out a hidden sensor's incidents,
and on the History page a hidden host's too, and says how many (`hidden` in the response; at most
twice the row limit is read, so a noisy hidden sensor can't crowd the rest out). **Show N from hidden
sensors** asks again with `&hidden=1`, and they come back dimmed and tagged. A drilled-down sensor
always shows its own incidents, and a hidden host's own page shows its incidents.

## 7c. Change log

Who changed what and when (NetBox's change log, `changes.go`). Every signed-in write passes the
`changeLog` middleware, which logs it once it succeeded: in its route's words from `changeRoutes`
("Paused a host", "Changed a channel"), naming the object the path points at (a host, a sensor as
"host · sensor", a user, a channel, a window; read before the handler runs when it deletes or renames
it), or in the handler's own words (`noteChange`). Handlers add the values before and after
(`changeDiff`): host settings (names, probe, interfaces, class options, per-host thresholds, add-ons,
master sensor, sensor order), default thresholds, class, probe and groups of a host, settings, shared
channels and users. An entry also keeps the sensors it was done to (`items`), for the chart markers
(section 7l). A secret only reads "changed". A route the table doesn't list isn't logged:
sign-ins, test sends, checks, reads; revealing a probe VM's console password is the one read that is.
Nothing of a request body is kept but the fields a route chose to show. One action is one entry,
however many hosts it touched (`change_hosts` lists them, for a host's own list and the filters).

A **reason** typed beside a dialog's Save (host settings, the thresholds dialog; the bulk actions)
rides along as the `X-Argus-Reason` header (URL-encoded, up to 200 characters); an acknowledgement's
or a hide's own note stands in for one. Argus logs what it changes by itself as "Argus" (an upstream
device read from the UniFi controller, an import).

**Changes** (Admin) lists the log newest first, 300 at a time with **Show older changes**: search
(action, object, who, detail, reason, values), kind, probe and group (the hosts it touched), period
(24 h / 7 / 30 days / a year), **Export CSV** (`GET /api/changes?from=&q=&cat=&probe=&group=&before=`).
A host's own list folds under its history (`GET /api/hosts/{id}/changes`, anyone who sees the host).
Entries are kept for **Keep changes for** (Settings, `ARGUS_CHANGES_KEEP_DAYS`: 90, 365 (default) or
730 days) and pruned once a day.

## 7d. Tags and bulk actions

**Tags** (`tags.go`) are Argus's own labels across sites: `tags` (name, colour from an eight-colour
palette, description), `host_tags` and `probe_tags`; a rename or delete carries through to the hosts,
probes and channels (`ON UPDATE / DELETE CASCADE`, and the channels' JSON lists rewritten in the same
transaction). A host carries its own tags and its probe's (`hostTagIndex`; the probe's read "from
proxy-x", dashed, and can't be removed on the host; a host moved to another probe takes that probe's).
Admins manage the list in Settings, Tags (`GET/POST /api/tags`, `PATCH/DELETE /api/tags/{name}`) and a
probe's tags on the Probes page (`PUT /api/proxies/{id}/tags`); a host's own ride its settings save
(`tags` in `PATCH /api/hosts/{id}/config`). The tree shows them on each row (not on a phone, where the
name wins) and filters by them (a group left with no host drops out). A channel, shared or personal,
can be limited to hosts with one of some tags (`tags` on the channel; empty = every host): the notifier
reads every host's tags once a tick (`effectiveTagNames`) and `serves` checks them with the sites and
the severity floor (`hostRoute`).

**Bulk actions** (`bulk.go`) act on a selection in one request, each item on its own, and answer
`{done, failed: [{id, name, error}]}`: one refusal (out of the user's sites, a host Zabbix refuses, a
sensor of no problem) doesn't undo the rest, the failed stay selected and the toast names the first
with its reason. `POST /api/bulk/hosts` (admin, helpdesk): `ack` (every open problem of the hosts),
`pause`/`resume`, `hide`/`show` (with a length), `groups` (replace their groups), `probe`, `tags` (add
and remove), `thresholds` (per-host thresholds on hosts of one class, the class's add-ons' included:
a value sets it, "" puts the default back, absent leaves it). `POST /api/bulk/sensors`: `ack` (anyone, like a single ack), `note`,
`pause`, `hide` (admin, helpdesk). Maintenance for the selected hosts opens the window editor with them
picked (a one-off window by default). The change log gets one entry per action naming the hosts.
The tree's **Select** turns on a checkbox per host (a group's box picks every host under it); the
problem lists always have one per row. The action bar sticks to the bottom of the panel while
anything is selected, and scrolls sideways on a phone.

**Export CSV** saves the list as shown (filters applied) from the browser: the tree's hosts, a problem
list, History, Changes; UTF-8 with a BOM and quoted cells, so spreadsheets open it as is.

## 7e. Host tabs, device facts, links, journal, Inventory

A host's body (expanded in the tree or open on its own) has tabs, each with its count: **Sensors**
(the default: problems and the sensor table as before), **Device**, **History** (the last 30 days,
with the hidden-sensor toggle), **Journal** and **Changes** (section 7c). A drilled-down sensor keeps
its own history open below its chart, without tabs.

**Device facts** (`device.go`) come from the device's own items, read across all hosts in a few calls
(`collectFacts`: `item.get` by the fact keys, a key-prefix search for discovered ones, the hosts'
addresses): `unifi.model`, `unifi.serial`, `unifi.mac` and `unifi.firmware` (the UniFi templates keep
them from the controller record they already fetch), `system.descr[snmp]` (SNMPv2-MIB sysDescr, read
hourly by the Linux, Windows and unRAID SNMP classes, shortened to "Linux 6.1.0-21-amd64 (Debian)" or
"Windows (build 22631)"), Home Assistant's and AdGuard's versions, an XCP-ng pool's hypervisor
versions. The MAC falls back to the one discovery saw for a host adopted from it. The asset tag and
location are Argus's own (`host_facts`, host settings, Device facts). `GET /api/hosts/{id}/device`
also gives the host's links, tags and **Used by**: its groups and probe, the status pages and
maintenance windows that cover it, and the shared channels its alerts reach (sites, tags, alerts on).

**Links** are buttons on the Device tab: templates every host of some classes gets (Settings, Device
links; `link_templates`, `/api/links`; a new install starts with "Web UI" `https://{ip}` for the
classes with a web page) and a host's own (host settings, Links). `{ip}`, `{name}`, `{host}`,
`{mac}`, `{group}` and `{macro:NAME}` (a non-secret host macro, like `{macro:UNIFI.URL}`) are filled in
from the host; a host missing one doesn't get that button. Only `http(s)`, `ssh`, `rdp`, `vnc` and
`telnet` addresses become links.

The **Journal** (`host_journal`, `GET/POST /api/hosts/{id}/journal`, `DELETE /api/journal/{id}`) keeps
lasting notes on a host, newest first, each with a kind (info, warning, problem: the status-page note
kinds), author and date. Admins and helpdesk write; an admin removes any entry, helpdesk their own;
everyone who sees the host reads it. Unlike a sensor note it doesn't clear itself.

The **Inventory** (Watch; `GET /api/inventory?probe=&group=`) lists every device the user sees (not
Argus's own probe hosts), grouped by class, with its facts and own fields; search, probe and group
filters, **Older firmware**, Export CSV. A UniFi device has its controller's word for it: the version
the controller offers as an upgrade (`unifi.firmware.upgrade`, from `upgradable` and
`upgrade_to_firmware` in its record) flags it "update available", and one with nothing offered is
current, since models run different firmware lines (the 2.5G switches 2.x, the others 7.x). Any other
device whose firmware (or, without one, OS) is older than the newest seen on exactly the same model is
flagged with that version; a device whose model isn't known, and a pool on mixed versions, aren't
compared.

## 7f. Upstream devices

The device a host is plugged into (`upstream.go`). By default it is the **UniFi controller's
answer**: a UniFi switch or access point reports the device it hangs off (`unifi.uplink.mac` and
`unifi.uplink.port`, kept from the controller record its template already reads, matched to the host
whose `unifi.mac` it is), and a UniFi switch or gateway lists the wired clients on its ports
(`unifi.wired.clients`: `stat/sta` filtered to `sw_mac` = its MAC, every 10 minutes, as `[{mac, ip,
port}]`), which Argus matches to hosts by IP (hosts that share an address are one machine, a NAS and
the services on it, so all of them are behind that port), else by the MAC discovery saw. A UniFi device's own uplink wins over a client entry.
**The ports' side wins:** a switch, gateway or console also lists the UniFi devices on its ports
(`unifi.downlinks`: the record's `downlink_table` as `[{mac, port}]`, a plumbing item), and that
beats a device's own uplink field. On a site with Flex Minis, whose LLDP frames reach devices they
aren't cabled to, the uplink field named the wrong switch (an access point on a Flex's port 2 read
as on a Flex Mini's port 2, and that Flex Mini as on another Flex Mini's unused port), and the
controller's own topology view flipped with it, while every switch's downlink list followed the
cables. The uplink field still answers for a device no switch lists.
**One network at a time:** private addresses repeat from site to site and a cloned VM keeps its MAC,
so every address and MAC is matched only among the hosts on the reporting device's network
(`upstreamNetworks`: the probe that monitors a host, or for a host the server monitors the probe of
its site, the way the site's map takes it): a VM on one site's hypervisor was once placed over a
host on another site that shared its MAC.
**Checked against the ports:** the controller guesses uplinks from what the switches have learned, and
a guess can be plainly wrong (two USW Flex Minis on one switch were each placed on a port of the other).
An answer that names a port with no link (`unifi.port.state[N]` = 0) or the named switch's own uplink
port (`unifi.uplink.local`, which would put the device above it) is ignored: the one Argus has stays,
and the Device tab says what the controller said and why it was ignored. The recorded answer is checked
the same way on every read: one that fails is dropped (the host has no upstream meanwhile) and the
first live answer that passes is taken at once, without the 15-minute wait. A host can be set (host settings,
Upstream device; `upstream` in `PATCH /api/hosts/{id}/config`) to a host **chosen by hand** (refused
when that host is behind this one) or to **None** (`host_upstream`). `unifi.wired.clients` is plumbing: it
is never a sensor, and one that fails never raises "stopped collecting". A host the controller gives
no upstream says why on its Device tab and in its settings: no client lists yet (the templates not
updated), a read that failed (the controller's reason), lists not read yet, or this address not among
them.

**VMs: the hypervisor they run on.** An XCP-NG pool's collector lists, every poll and whatever VM
monitoring is set to, each VM's network cards (MACs from its VIFs), its IPv4 addresses when guest
tools report them, and the member it runs on (resident, else its affinity, else the pool's only
member; templates, snapshots and control domains left out, ignored VMs kept: this places hosts, it
monitors nothing). The template carries it as `xcp.vm.nics` (plumbing, like `unifi.wired.clients`), and a
poll that can't log in keeps the last list, so the VMs stay behind their hypervisor while it is down.
`hypervisorUpstreams` matches each VM to hosts by its guest addresses and by its MACs (through the
UniFi wired-client lists, MAC to address, or the MACs discovery saw), on the pool's own network only
(above), and hangs them off the host at
the member's address (the pool's own host when the pool has one member; a VM on a member Argus
doesn't monitor is left to the other answers). That answer wins over the controller's for the same
host: the controller only sees a VM's MAC on its hypervisor's switch port. It goes through the same
settle (a live migration moves it once the new answer holds), and the stored answer keeps its source
(`auto_source`: Changes say "Upstream (from XCP-NG)", the Device tab "from XCP-NG, the hypervisor it
runs on"; the host settings' mode is **Automatic**). **A Probe host never has an upstream device**,
whoever says so (`dropProbeHosts`): it is its site's master, and a probe VM behind a hypervisor whose
ping went down first would hold the hypervisor's alert while the hypervisor held the probe's.

**Holds.** The notifier's master set walks a host's chain of upstream devices, nearest first, and
judges each one's main master (its ping) like the host's own (section 9): while one is down, or yet to
report on a new problem, the host's alerts are held (`by: upstream`), its ping included. The top of an
outage still alerts, since its own chain is up; a chain that loops back (two devices reporting each
other) holds nothing, so a down device always gets its alert out. The census marks those rows
`held_by` with `via: upstream`, the lists say "Held: behind sw-core, which is down", the tree row says
"held: behind sw-core" and the host's body carries a band saying so. An alert on a device's ping
names the hosts behind it ("Behind it: ap-lobby, ap-office and 2 more (their alerts wait while it is
down)"; its RESOLVED says their own alerts go out now if they're still in trouble), in every channel
and as `behind` in the webhook.

**Shown** on the Device tab as **Path** (top first, each hop with the port the next one is plugged
into, a down hop red, and where it comes from) with the hosts plugged straight into it, and in host
settings. Every 5 minutes Argus reads the controller's answers and stores them (`auto_*` in
`host_upstream`): a host's first answer at once, a new one only once the controller has kept it for 15
minutes (logged in Changes, as Argus). Until then the stored answer stays in effect, holds included:
the controller can flip between two answers every few minutes (two USW Flex Minis on one switch report
no neighbours, so it guesses each hangs off the other as the switch's MAC table ages out). A host whose
answer flips 3 times in an hour is logged once a day as "Upstream device keeps changing", with a
pointer to setting it by hand.

## 7g. Site info and who to call

A site (a top-level group) keeps its **address** (and a note: opening hours, access), its
**contacts** (role, name, phone, email) and its **internet lines** (name, provider, circuit or
contract, support phone, a note such as "LTE backup", and the speed as contracted: download and
upload in Mbps, 0 to 1000000, a blank upload being the download's; the network map shows how full a
gateway's WAN is from it, section 7j), as one JSON document per site in `site_info`
(`siteinfo.go`, `store/site.go`; up to 20 contacts and 10 lines). It is keyed by the site's name and
moves along when the group is renamed (a rename onto a site that has its own info leaves both alone).
`GET /api/sites/{site}/info` (with `?choices=1`, what a line can be tied to), `GET /api/sites/info`,
`PUT /api/sites/{site}/info` (admin and helpdesk, within their sites; logged in Changes with what
moved).

**A line is tied to the sensor that measures it**: a UniFi gateway's WAN (`unifi.wan.avail[N]`; any
sensor of the same WAN counts, so its latency or traffic too) or a whole host, such as the provider's
modem (any of its sensors). Its state on the site page and the Device tab (up / down) is that
sensor's, from the census. The problem lists give a row on such a sensor a who-to-call line
("Example Fiber · circuit EXF-000123 · support +1 555 0100").

**Who to call** is a switch per channel (`who_to_call`, global and personal, off by default): its
problem alerts and reminders gain "Call Example Fiber +1 555 0100, circuit EXF-000123 (WAN 1)" for
each line the alert is about, every line when the site's probe stopped reporting (`zabbix[uptime]`
on its Probe host: the internet is the usual reason), and the site's first contact with a name or a
phone ("On-site IT: Bob Example +1 555 0101"). A RESOLVED and an acknowledgement never carry them.
The webhook sends them as `call` (a list). A host's site is the top of its first group, the one its
alerts' "Site" names.

**Shown** at the top of the site's page in Monitoring (Address, Contacts, Internet; **Edit site
info** in the toolbar for those who may edit, and in the site's row menu in the tree, which opens the
editor without opening the site), and on each of its hosts' Device tab, with copy buttons on the
numbers. A site's map whose gateway WAN has no speed on its line says so under the map, with **Set
the speed** opening the same editor (for those who may edit).

## 7h. Import from a spreadsheet or PRTG

Discovery's **Import** (admin) creates hosts from rows: name, address (an IP or a DNS name),
class (its id or label), site (the group, made when new), probe (a probe's name, `server`, or
empty for the site's own `proxy-<site>`, else the core), tags, asset tag, location, a UniFi device's
MAC, and any class input as a column named after its macro (`NUT.UPS`). The browser reads a CSV
(comma, semicolon or tab, UTF-8 or Excel's Windows encoding; other columns are listed as ignored);
for PRTG, Argus reads the tree itself (`importprtg.go`).

**Checked before anything is made** (`POST /api/import/check`, `importhosts.go`): each row is
ready, skipped (already monitored: its name, or its address as the same class) or to fix (a class
it doesn't know, with "did you mean", a probe it doesn't know, no site, a bad address or tag, a name
an earlier row has, or a class input missing). Inputs derived from the address (an AdGuard or Home
Assistant URL) are filled in; a UniFi device gets its controller URL, key, MAC and site from a saved
controller that lists it (by MAC, else address) or the one serving its site, so the key never goes
through the browser. The table lets a bad cell be fixed in place (a class or probe picked, a missing
input typed) and re-checks after every edit.

**Run** (`POST /api/import/run`) re-checks and creates the ready rows in the background, one at a
time, through the same path as Add device (`provisionHost`, source `imported`), then their tags
(made when new), asset tag and location; `GET /api/import/{id}` follows it (one import at a time,
kept a day). The change log gets one entry when it ends: "Imported 12 hosts", from the file's name
or PRTG, "12 created, 1 skipped, 2 failed", naming the hosts. Nothing sets an upstream device: the
UniFi controller's answer comes afterwards (section 7f).

**PRTG** (`POST /api/import/prtg`, the address, an API key with read access, PRTG 22.4 or later,
and optionally "accept a self-signed certificate"): Argus reads `table.json` for devices (name,
host, probe, parent, tags), groups and probe nodes (the group path below the probe) and sensors
(their types). The key is used for these reads only and never kept, and it is left out of any error.
Each device's class is guessed from its sensors (Windows from WMI, Linux from SSH or SNMP Linux, DNS,
UniFi by name or, better, from a saved controller that lists its address; only Ping, or nothing
Argus has a class for, is Ping only) with the reason shown; a UPS or a VMware host is left to pick.
PRTG's sensors aren't copied: each host gets its class's sensors and thresholds. In the dialog each
PRTG probe maps to an Argus probe and a site (paired by name: "Site 1 probe" and proxy-site1; the
Local Probe to the core), groups are kept under that site or flattened, and the device tags come
along or not.

## 7i. Personal API tokens

For scripts and other tools (Account, API tokens; `/api/me/tokens`, `apitokens.go`,
`auth/token.go`). A token is `argus_pat_` and 40 hex characters, shown once when it is made; only its
SHA-256 is kept (`api_tokens`), with its name, the last 4 characters (to tell tokens apart), its
scope, when it expires (30, 90, 182 or 365 days, or never) and when and from where it was last used
(recorded at most once a minute). A user has up to 25. Making one takes the password and, with
two-factor on, a current code (a token is a way in, so a session alone can't mint one), and every
token of a user is revoked when their password is replaced (an admin's reset, the self-service
reset, their own change).

**Signing in.** `Authorization: Bearer argus_pat_...` makes the request its user's, as a session
would (`TokenMiddleware`, after the session middleware: a session cookie wins). An unknown or expired
token, or one whose user is disabled or removed, is refused at once with a 401. Other bearer tokens
(the probes' check-ins) pass untouched. A token acts with its user's current role and sites, never
more, and its **scope** narrows it further (`tokenScope`): **read only** (GET), **acknowledge, add
notes** (acknowledge and un-acknowledge, sensor notes, journal entries, the bulk sensor actions ack
and note), **open and close maintenance windows**, or **everything its user can do**. No token may
manage tokens, change a password, two-factor or passkeys, sign out, reveal a probe's break-glass
password, or (an admin's) administer accounts, change the settings or the notification channels:
who can sign in, how, and where the reset mail goes need a person in the app, so a leaked admin
token can't make itself a new admin or point the reset mail elsewhere.

**Logged.** Every change a token makes is in the change log as "Alice Example (token ticketing)";
making and revoking a token are logged too (its scope, the user's role and when it expires, never the
token). An admin can revoke all of a user's tokens from the Users page (`DELETE
/api/users/{id}/tokens`), which also shows how many each user has.

## 7j. Network maps

A probe's site drawn as a tree (Maps in the sidebar, under Watch; `maps.go`, `store/sitemaps.go`),
built on request from what Argus already holds: no polling of its own. **The links** are the upstream
devices in effect (section 7f: the controller's answer as Argus holds it, or one set by hand), between
hosts both on the site (the hosts the probe monitors, and the server's own in the site's group; never
Argus's Probe host). **Their traffic** comes from the census: the upstream's port sensors when the link
names a port it reports (`unifi.port.out[N]` is the traffic down toward the device, `unifi.port.in[N]`
up, `unifi.port.speed[N]` the speed, `unifi.port.state[N]` = 0 no link), and the device's own uplink
sensors (`unifi.uplink.in` / `out`) when it has them: both count the same cable, and a gateway can read
0 on a busy port while the switch on it counts its own uplink right, so the device's count wins (the
port still gives the speed and the state). How full a link is, is the busier way over its speed. A
reading older than 10 minutes is unknown. A UniFi gateway's WANs lead up to the internet: one node per
WAN, named after the site's line on it (section 7g), its link carrying `unifi.wan.in[N]` (the download),
`unifi.wan.out[N]` and the latency, coloured by the WAN sensors' own state. A WAN whose availability
reads 0 has no link when its alert is open (it worked and died); with no alert it is a **spare**, a
failover set up but never connected (its alert is armed only once it has worked): drawn dashed grey as
"not connected", never counted as a link down. The internet boxes sit a level above their gateway, one
per WAN side by side, each with its own link and label, and move with the gateway when it is moved. With the line's speed, the WAN also shows how full it is, each way against its
own speed (an upload is often slower). The line is the one tied to that WAN, else one tied to the
whole gateway when it has only that WAN. **The devices**: UniFi gateways, switches and access points are always on the
map; any other host only when it is linked. A device's state is its sensors': down while its ping is in
error (and for how long), else its worst sensor, paused with its ping. A host with no known upstream is
listed under the map as not on it.

**Drawn** top-down in the browser: each device centred over the ones below it, in port order. The hosts
under a device stack in a column at its left, so only network devices spread sideways and a big site
grows downward; hosts on one port share one box, a row each (one cable, one link, one label; each row
opens its host). A VM hangs off its hypervisor on a link labelled "VM", with no traffic of its own;
the VMs on one hypervisor share one box the same way ("2 VMs"). Five or more network devices with nothing below them, under one device, fold into
columns of six too. A link is labelled
above the device it feeds ("Port 9 ↓950 ↑12 Mbps · 95%"), coloured by how full it is (past 70% amber,
past 90% red, dashed red with no link, grey with no reading) and thicker the more it carries (the square
root of its traffic, the thickest at 1 Gbps). Links share their upstream's trunk, the worst-off drawn on
top. A device opens its host, a link its traffic chart. **Hosts** (a switch, off when a map opens: the
network devices first) adds the hosts with nothing below them; a map with only hosts says so and offers
to show them. **Fit** draws the map to the page's width but never below 75% (past that it scrolls
sideways, so the text stays readable), **Actual size** at 100%; the page refreshes every 30 seconds. **List** draws the
same tree as a table (device, port, down, up, a fill bar), one line per link on a phone, where it is
the default. **Arrange** (admins and helpdesk) lets devices be dragged: each move is kept as an offset
from where the layout puts the device, so a moved switch takes the devices below it along and a
changed site still lays out; **Reset layout** puts them all back. One layout per map, for everyone.

**Off until turned on**, per probe (`site_maps`: on, by whom, when, and the offsets, which turning a
map off keeps). An off map is never built: `GET /api/maps` lists the probes the user sees, with a
summary only for the maps that are on (devices and hosts on it, devices down, busy links (70% or more),
links with no link, the busiest one: a port, or a gateway's WAN), and `GET /api/maps/{probe}` answers 409 for an off one. `PUT
/api/maps/{probe}` (`{"enabled": true}`, admins; logged in Changes as "Switched a site's map") turns one
on or off; `PUT /api/maps/{probe}/layout` (`{"pins": {"<id>": {"dx", "dy"}}}`, admins and helpdesk, up
to 1000) keeps the moved devices. A removed probe takes its map along. Per-site visibility applies: a
user sees only their sites' probes and, on a map, only their hosts. The models and addresses on the
devices are read every 10 minutes at most.

## 7k. PDF reports

A report is a PDF of the sites, hosts and sensors someone picked, over a period (`reports.go` works
the figures out, `reportsapi.go` serves, saves, emails and schedules, `internal/report` draws).
**Watch, Reports** has two tabs: **New report**, a tree of sites, hosts and sensors to tick (a site
covers every host in it, now and later, but not Argus's own probe host unless it is picked; a host
its uptime and problems; a sensor its chart, a group's channels together) beside the report's name,
period, uptime target, whether maintenance is left out, an outline of what it holds and about how
many pages; and **Saved reports**. The tree ticks like any checkbox tree: a site takes over the hosts
picked in it one by one, unticking a site clears everything under it (its hosts' sensors too) and
unticking a host clears its sensors; **Clear all** resets the whole report (the picks, its name,
period, target and switches; a saved one being edited stays saved, untouched). The draft stays in the browser
between visits, and **Add to a report** in a site's, host's or sensor's row menu adds to it where the
user is.

- **Period** (`reportPeriod`, Argus timezone): the 7 or 30 whole days before the day it is made, last
  calendar month, this month up to now, or from one day to another (both included; a year at most).
- **Uptime**: each covered host's availability sensor (the same pick as the host page and the status
  pages, `hostUptimeItems`), read hour by hour from Zabbix's trends over the period, each hour
  weighted by its checks; downtime is each hour's share of checks that found it down. With
  maintenance left out, every hour a window covering the host overlaps (`occurrencesBetween`, from
  the windows enabled now) counts as neither up nor down. The strip has a cell per day (per week past
  two months), green with no downtime, amber at or over the target, red under it (99 % without one).
- **Problems**: every problem at Warning and above that overlaps the period, on any host in the
  report (`collectIncidentsTill`: Zabbix's events since a month before the period up to its end, and
  Argus's own incidents, kept a year for this), with how long it lasted and who acknowledged it with
  their note (else the note on its sensor); 40 per host, then a count.
- **Host charts**: every covered host gets a chart of its availability sensor under its problems,
  "Ping": its response time (`icmppingsec`) with the time `icmpping` read down shaded red and marked
  along the chart's foot (a collector's flag alone, drawn as up and down, for a host without a ping),
  its down time the same as in its uptime (maintenance left out the same way); 100 hosts at most. A
  switch on New report leaves them out (`skip_host_charts`), and a host whose own ping sensor is
  picked isn't charted twice.
- **Close-ups of outages**: the hours a covered host's availability sensor read down (from the same
  trends as its uptime, maintenance left out alike) are grouped into stretches, the most downtime
  first; up to 3 per host (60 in a report) are read check by check from Zabbix's raw history, an
  hour either side, and drawn as a lower chart from half the outage's length before it (30 minutes
  at least, 6 hours at most) to as long after: the minutes down shaded and marked red, the ping's
  response time around them, the problems open then ("no problem was raised for it" otherwise), a
  count of the shorter ones not drawn. An outage older than the raw history Zabbix keeps has no
  close-up, and the footnote says how many. A switch leaves them out (`skip_close_ups`).
- **Sensors**: up to 3 days, raw history; longer, hourly trends (each hour's average drawn, min and
  max from the hours' extremes, the 95th percentile of the averages). A group's channels share a
  chart when they share units, one chart per kind of units otherwise, and its up/down channels shade
  the time they read down; a lone up/down sensor is drawn as up with the time down in red. The chart
  is banded by the first channel's own thresholds (`itemThresholdsFrom`: the line past a threshold
  takes its colour, dashed lines with a tag on the axis); a threshold more than twice the highest
  value away is left out so the line isn't flattened; the axis ends at a round number a little past
  the highest value, and a percentage's never past 100 %. Time in warning and in error is how long the
  sensor's own alerts were open within the period. 60 sensors are charted at most.
- **Name**: one left empty is named after what it covers and its period as picked (`reportAutoName`:
  "Firewall-BZ, last month", "mybz and myng, last 7 days", "web1, 3 sensors, 1 to 15 October 2026"),
  so a saved report's name stays right each time it is made; the name field shows it as its
  placeholder and the save dialog fills it in.
- **Page 1**: the logo (Settings, Reports: a PNG or JPEG of 512 KB at most, kept in `app_meta`, drawn
  up to 170 by 54 points), what it covers, when it was made and for whom; the title and period; the
  tiles (uptime, under target, downtime, problems); the hosts table (more than 12 hosts: those at
  100 % with no problems fold into one line) and the sensors table; a footnote on how the figures are
  read. Every page has the title, the period and "page N of M" at its foot.
- **Drawing**: A4 in points with gopdf (MIT), vectors only, set in Open Sans (Apache 2.0, embedded as
  the subset a report uses), so the distroless image needs no browser and the text can be searched
  and copied. Two reports are made at a time at most, 2 minutes each.
- **Saving and email**: `reports` holds each saved report's spec and schedule (JSON), who saved it
  last (its owner) and how its last run went. Anyone signed in makes the PDF of a report, saved or
  not, with what they can see (`GET /api/reports/{id}/pdf`, `POST /api/reports/pdf`); admins and
  helpdesk save, change, delete and email them (`POST`, `PUT`, `DELETE /api/reports[/{id}]`, `POST
  /api/reports/{id}/email`, `POST /api/reports/email`), logged in Changes under Reports.
- **Per site** (one Argus serving several customers): an account limited to some sites lists, makes,
  emails, changes and deletes only the saved reports whose every site, host and sensor is inside its
  sites (`reportVisible`); any other report, one that also covers another site or a host Argus can't
  place any more, answers 404 as if it didn't exist. What it saves or makes can't reach outside its
  sites, and every PDF is drawn with only the hosts the one making it can see. An account with no
  sites set sees every site, here as everywhere. The New report draft is kept per account in the
  browser, so a shared browser never starts a customer from someone else's picks.
- **Email** goes through the first enabled email channel, the PDF attached. A schedule sends every
  day, every week on a weekday or every month on a day (1 to 28, or the last), at a time: checked
  every minute, made with what its owner can see now (a disabled or deleted owner stops it, saying so), a failed send tried again every
  half hour until six hours past its time, and the error shown on the list. Saving a report starts its
  schedule from then, so a time already past today waits for the next one.

---

## 7l. Chart markers

What happened to a sensor's host in the range its chart shows, drawn on the chart, so "why did it
jump at 03:00?" is answered there (OneUptime's change events; `marks.go`). `GET
/api/marks?items=<id,id>&range=<2h..1Y>` (anyone who sees the sensors; up to 12, a group chart's
channels) gathers, oldest first, at most 300 (the newest):
- **The change log** (section 7c) of the sensors' hosts: the host's own entries (settings, class,
  probe, thresholds, pause...) and the sensors' own (an acknowledgement, a pause, a note), never
  another sensor's. Each entry keeps the sensors it was done to (`changes.items`, `,id,id,`; empty =
  the whole host): an item route names its item, an event or a sensor key every census row that
  matches (a problem over several sensors is on each), a bulk sensor action each sensor; a row Argus
  raised itself has no item and stays the host's. Entries from before the column (NULL) are matched
  by the name they gave ("host · sensor"). An acknowledgement and a note get their own kinds; a
  journal entry's log line is left out (the journal has its own marker). The text is the action, its
  detail and up to three values before and after; who and the reason ride along.
- **The journal** (section 7e), each entry in its kind's colour.
- **Maintenance windows** (section 9) covering the hosts, each occurrence in the range as a stretch
  (the reports' `occurrencesBetween`); a disabled window shows none, and a window edited or deleted
  shows as it is now.
- **Restarts**, from the host's uptime sensor (`system.uptime`, `system.uptime[snmp]`,
  `system.uptime[ssh]`, `unifi.uptime`; `zabbix[uptime]` on a probe's host reads "Zabbix
  restarted"). From raw readings (2h, 2d) the boot is the reading's time less its uptime, never before
  the reading taken before it; from hourly trends (7d and longer) an hour whose lowest reading is far
  below its highest (the restart was inside it) or below the hour before's lowest (at the turn of the
  hour), placed mid-hour and shown as "around". SNMP's 32-bit counter wrapping after 497 days is not
  a restart.

A source that fails is left out; the markers never fail a chart, and they arrive with the chart's
history so it doesn't jump. **In the UI** (single and group charts, not the daily bars): a 24px lane
above the plot holds a pin per event (an icon in its kind's colour: a change neutral, an
acknowledgement in the acknowledged colour, a note and an info journal entry in the accent, a journal
warning or problem in theirs, a restart in the paused blue), pins closer than 14px merged into one
with their count, and a window as a bar; the plot stays clean but for a faint band per window.
Hovering, focusing or tapping a pin shows a card (when, what, who and the reason) and draws its dashed
line through the plot; zooming re-places the pins. Under the legend a folded "N events in this
range" lists them newest first, the way in on a phone; hovering or tapping a row draws its line.

## 7m. Email heartbeat (push sensors by email)

OneUptime's incoming email monitor, as push sensors (`mailboxes.go`, `internal/mailread`): for gear that
only reports by email. **Mailboxes** (Settings, admin; `mailboxes`, up to 20): IMAP (server, port,
TLS or STARTTLS, user, password, folder; `emersion/go-imap`, the folder opened with EXAMINE so
nothing is marked seen) or Microsoft 365 through Graph (tenant, client ID, client secret, mailbox,
folder; client credentials, the token kept until a minute before it expires; the app may read only
that mailbox, through RBAC for Applications). The secret is encrypted like the channels'; the list
never returns it, a blank one on edit keeps it, and a new kind needs its own. **Test** (`POST
/api/mailboxes/test`) signs in and counts the folder's mails. Argus never marks, moves or deletes a
mail: it keeps a **watermark** per mailbox (IMAP `<UIDVALIDITY>:<last UID>`, a renumbered folder
starting over; Graph `<last received time>|<ids read at that time>`), reads every 2 minutes (and at
once when a mailbox is saved) what arrived after it, oldest first, at most 200 a read, and a new
mailbox, or one whose place changed, reads back 48 hours. A mail is its sender, its decoded subject,
the start of its text (the first text/plain part, else the HTML without tags; Graph's 255-character
preview) and when it arrived.

**A push sensor's rule** (`push_sensors.mail_*`): the mailbox, **From (contains)** and **Subject
contains** (one or both), success and failure words. A failure word, then a success word, is looked
for in the subject, then in the text (case-insensitive, at the start of a word); a matching mail with
neither is a failed run that says so. The run counts at the mail's arrival time (`RecordPushByID`; an
older one than the run on record counts without replacing it), so the late and missed times work as
for URL pushes. The last 48 hours of mails (at most 300 a mailbox) are kept in memory, so a rule saved
after its job's mail arrived takes the newest match at once. A mailbox that can't be read shows why in
Settings (`last_error`, since when), and its late push sensors say "No run read: its mailbox ... can't
be read since ..." instead of their last run. Deleting a mailbox takes the rule off its push sensors.

## 7n. Domain expiry

When a host's domains stop being registered, beside its certificates (OneUptime's domain monitor;
`domains.go`, `internal/domainexp`). **A host's domains** are the registrable domains its web checks
name (`{$HTTP.URLS}`: full URLs and bare host names, `shop.example.com` -> `example.com` by the public
suffix list; paths, addresses and internal names name none) and the ones typed in its settings
(**Domains**: a domain used only for mail; `host_domains`, up to 20, each saved as the registrable
domain; logged in Changes). **Argus reads each domain** once a day (`domain_checks`; an hourly loop
reads the ones due, a failed read again after 6 hours, 2 seconds between lookups): the registry's RDAP
(found in IANA's bootstrap file, kept a day; the `expiration` event), else its WHOIS (the server IANA's
WHOIS names for the TLD, kept a day; the expiry line, in the date formats registries use), so `.it`
(no RDAP) works. A registry that publishes no expiry (`.de`, `.eu`) says so; a domain the registry
doesn't know says that. A failed read keeps the expiry the last good one found.

**To Zabbix** through the push channel (section 5): `/api/push/host/{id}` carries `domains` (name, whole
days left, expiry date; only the ones whose expiry is known), and the **Argus Push** template's
**Domain discovery** makes `argus.domain.days[<domain>]` (days, below 0 once expired) with **expires
soon** (Warning, under `{$DOMAIN.EXPIRY.WARN}` 30), **expires very soon** (High, under
`{$DOMAIN.EXPIRY.HIGH}` 7) and **has expired** (High), both thresholds in the Thresholds screen. The
template goes on a host with its first push sensor or domain and off when it has neither (the loop links
it, at the Public URL). A domain whose expiry can't be read gets no sensor (it would only ever read "not
supported" and alert as stopped collecting); its row in host settings says why. Under **Domains** in
the host's sensors, one row each.

## 7o. Runbook notes

What someone should do when a sensor is in trouble (OneUptime's runbooks, kept to text; `runbooks.go`,
`runbooks` table). Written from a sensor's menu (**Runbook…**, admin and helpdesk; `GET`/`PUT
/api/runbooks/item/{id}`, an empty text removing it; logged in Changes) for **this sensor only** or for
**every sensor of its kind on hosts of its class**: the class is the host's device class, the kind its
item key with the instance as `*` (`vfs.fs.size[/data,pused]` -> `vfs.fs.size[*,pused]`, so every
mount's used space is one kind and its free space another). A sensor's own runbook wins over its
kind's; a host without a class can have only its sensors' own. Plain text, up to 1000 characters. A
group's menu writes the runbook of its channel in trouble (when one is), else of its main reading.

It shows under a sensor in trouble: its first line ("What to do: ...") with **more** for the rest, in
the problem lists and on the host page (a group shows its channels' first). In alerts, a channel's
**Runbook notes** switch (global and personal, off by default, like Who to call) adds the whole note
under "What to do:" to its alerts and reminders, never to a RESOLVED or an acknowledgement; the webhook
sends it as `runbook`. The views and the notifier read the runbooks with the hosts' classes, kept 15
seconds.

## 7p. Multi-step web checks

"Can I log in to the portal", checked end to end with Zabbix's own web scenarios (OneUptime's synthetic
monitors without a browser; `webscenarios.go`, `zabbix/httptest.go`). A host's settings get **Web
scenarios** (admin and helpdesk, saved at once, logged in Changes; up to 20 a host): a name, how often
(1 minute to 1 hour) and up to 10 steps, each a full http(s) URL, form fields (name=value a line, a POST
when there are any), the text the page must contain and the status codes it may answer. The host's
proxy runs the steps in order, keeping cookies, following redirects, 15 seconds a step, one retry. A
password goes in a secret host macro (`{$WEB.PASSWORD}`, any `{$NAME}` a step names gets a password field
beside the steps; a blank one keeps it), never in the scenario. Names keep to letters, digits, spaces and
`. _ : / ( ) -`, so the item keys Zabbix makes need no quoting.

Zabbix makes the items (`web.test.fail[<scenario>]`, the step that failed or 0; `web.test.error`, why;
`web.test.time[<scenario>,<step>,resp]` per step); Argus makes the host's triggers, tagged
`argus.web=<scenario id>` and remade on every save: the scenario failed (High), and each step slow (a
warning past `{$WEB.TIME.WARN}` 5 s, an error past `{$WEB.TIME.HIGH}` 15 s), the thresholds being the
**Argus Web Scenarios** template's macros (linked with a host's first scenario, unlinked with its last)
so Thresholds edits them. On the host, each scenario is a group under **Web scenarios**: its result ("OK"
or "Step 2 failed", the error as the reason, a count key like the failed units) and each step's time.
Deleting a scenario takes its items and triggers with it.

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
  delay (0 = with the first alert) and a **First reminder** gap (0 = off). A channel hears of a
  problem only once the incident has been open and unacknowledged for its delay (counted from when
  the incident began, never before the alert delay has passed), so "team at once, managers after 30
  min" is two channels. Reminders (`[HIGH REMINDER]`, "Still open after 1h 5m (reminder 2)") repeat
  while the problem stays open and unacknowledged, backing off: the first after the channel's gap,
  each next one twice as long after the last, at most a day apart (a 1 h gap reminds after 1, 2, 4, 8
  and 16 hours, then daily; a new severity starts over), for problems at or above the channel's own
  **Remind for** severity (independent of its alert floor, so "alert on warnings, remind only about
  errors" is one channel); acknowledging, pausing or hiding stops them. A
  channel added after a problem went live isn't sent that problem.
- **Maintenance calendar (OneUptime's maintenance calendar; `calendar.go`):** the Maintenance page
  shows the windows as a list or as a **Calendar**, a month (Monday first, days in the Argus timezone)
  with every run of the enabled windows the user sees (`GET /api/maintenance/occurrences?from=&to=`, up
  to 62 days, from the reports' `occurrencesBetween`), a run opening its window for those who may edit
  it. **Calendar feed…** gives each user their own secret link (`/cal/m/<token>.ics`; the token's
  SHA-256 in `users.cal_hash` for the lookup, encrypted in `cal_enc` to show it again; **New link**
  replaces it, logged in Changes) to subscribe to in Outlook or Google Calendar: an iCalendar of the runs
  of the windows that user sees (their sites), 30 days back and 180 ahead, one event per run with UTC
  times and a UID stable per run, refreshed hourly. A disabled user's link answers 404. A calendar that
  fetches from its own servers needs Argus reachable from the internet.
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
  (`nut.reachable`, `xcp.reachable`, `linux.ssh.reachable`, `adguard.running`, `hass.running`, and a
  UniFi device's `unifi.device.state`, what its controller reports about it) as a second master, so a
  stopped service alerts once as "unreachable" while the machine still pings;
  "none" drops both. Masters are ranked (the ping, or the chosen sensor, above the collector) and
  hold only what ranks below them, so when the whole machine is down the ping's own alert goes out
  and the collector's is held; two masters down together never hold each other, which would leave
  the device with no alert at all. A collector holds only what it feeds, never the ping's own
  sensors (`icmpping*`: loss and response time measure the network). While the master has an open "down" problem (error level, or any "no
  data" one), the host's other alerts are **held**: they stay pending, and firing ones get no
  escalation or reminders. A probe's reporting sensor is also the master of **every host that proxy
  monitors** and of Zabbix's own per-proxy checks (`zabbix.proxy.*[<proxy>]` on the Zabbix server
  host), so a probe outage sends only the probe's alerts. The device a host is plugged into (its
  **upstream device**, section 7f) is a master too: while it, or one above it, is down, the host's
  alerts are held behind it, so a dead switch sends one alert, not one per access point and camera.
  A **UniFi controller** is a master of the devices read through it, too (`controllerfold.go`): the
  hosts sharing a `{$UNIFI.URL}` are its devices, and when enough of them show the controller's
  signature at once (reported offline on it while still answering ping, or their controller call
  failing: at least two, and at least half), Argus raises one problem on the controller's own host
  (the console with that URL, else the gateway, else the first device): "UniFi controller
  unifi.example.lan: 4 of 5 devices offline on it", with the devices as its reading. The devices'
  own alerts are held behind it (`held: behind the UniFi controller`), the console's included, but
  never a device's ping: the controller feeds the device's sensors, not its network path, so a device
  that stopped answering still alerts. The outage keeps its start across ticks, and when it ends the
  devices wait out the settle window, after which one still offline alerts on its own. Fewer devices
  in trouble than the quorum are their own problems. The UniFi sensors read from the controller's
  answer discard a sample they can't read instead of failing (a device re-adopting reports null
  statistics), so a controller restart never turns into one "stopped collecting" per sensor. To
  win the race with a slow master (ping
  needs 3 failed checks), a new problem also waits up to 5 minutes while the master hasn't reported
  since the problem began, or its last ping failed. After a master recovers, what it holds waits out
  the **settle window** (Settings -> Alerting, "Settle after an outage", default 5 min,
  `ARGUS_SETTLE_SECONDS`, 0 = off): a site or a device coming back up has services still starting
  and agents not yet listening, which would otherwise alert on the way (the maintenance rule applied
  to recoveries: whatever is still open when the window ends alerts then). The recovery time is the
  later of the master's "down" trigger's `lastchange` in Zabbix (`masterRecoveries`, read with the
  masters' triggers each tick), which keeps the window across a core restart, and the moment the
  notifier saw the master come back (`noteMastersDown`, in memory): Zabbix dates a recovery by the
  reading that closed it, and a probe reconnecting closes its "no data" trigger with a stored reading
  taken up to the trigger's period before it arrived, which alone would end the window minutes
  early. A settling hold isn't shown on the rows (`markHeld` reads no trigger states). A problem
  held by a down master also waits out the alert delay again (at least 90 s) after the master
  recovers; a held problem that is still open after that alerts normally.
  **Problems heard of late.** A probe cut off from the core keeps checking and stores the readings;
  as it reconnects it sends them, and Zabbix opens the problems they show dated when they began
  ("Since 00:34" at 00:58), which the next live check closes again: every one would send its alert
  and its RESOLVED a minute apart. A problem that began more than 2 minutes before the notifier's
  previous read of the problem list yet wasn't in it is heard of late (`heardLate`; the last read is
  `notifier_last_read` in meta, so one that began while Argus was down isn't late, and Argus's own
  problems never are). It is flagged (`notify_events.late`) and its alert waits the settle window (at
  least the alert delay, 90 s) from when Argus first saw it: one that closes meanwhile sends nothing,
  neither the problem nor its recovery, and one still open then alerts as usual, with its real
  start. Its timeline says "Reported late" and, if it closed, that nothing was sent. The site's own
  messages are the probe's "not reporting" / "unreachable" alerts and their recovery, which Zabbix
  raises in real time.
  The census applies the same rule (`markHeld`): an unacknowledged error or warning a **down** master
  holds (not one merely yet to report, so rows don't flicker) carries `held_by` (the master's host,
  item and label), the master's row `holds` (how many), and `/api/census` counts them as `held`
  instead of their state. The lists show the master and fold the held rows behind a **Show held**
  toggle. A status page folds them into the master's row (`foldHeld`, below). A held row is its
  master's, not one more open problem: the tree's host dot (`unackedHosts` / `ackedHosts`) leaves it
  out, so a device whose ping is down and acknowledged reads as acknowledged (washed red, no pulse)
  instead of red because its packet loss and SNMP are down too; and the host page's problems list
  (`held_by` on `/api/hosts/{id}/problems`, from the census rows, where a problem on no sensor has a
  row keyed by its event) shows a held one muted, "held: Reachable (ICMP) is down", outside the open
  count.
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
  which a URL that doesn't answer never gives its response time (`leftOverUnsupported`). A reading
  of a target that doesn't answer is left out too, whatever made it unsupported: a response time,
  status code, certificate or resolved address whose own up/down flag (same key parameters,
  `downFlags`: `saas.time` and `saas.up`, `http.url.*` and `http.url.up`, `tcp.port.time` and
  `tcp.port.up`, `dns.resolve.*` and `dns.resolve.success`) reads down: the flag's alert ("does not
  answer") says what is wrong, where GitHub's response time used to alert "stopped collecting" a
  minute before it (`targetDown`). And an **agent / SNMP / IPMI / JMX
  interface** Zabbix marks unavailable (timed from its `errors_from`). Both are High and skip the
  alert delay, since failed checks or Zabbix's retries already are the wait. The reading is Zabbix's
  error; for a failed preprocessing step, the step's own line ("1. Failed: ..."), not the
  "Preprocessing failed for: <value>" line above it, which only echoes the value (`itemError`). Acks stay in Argus (no Zabbix event to mirror). Those present when the
  feature arrived were baselined. Supported sensors with no new values are deliberately not alerted:
  many store only changes, so an old last value is normal; a silent device or probe is caught by its
  master instead.
- **Who to call:** a channel can add the site's internet line and contact to its alerts (section 7g).
- **System notices:** Argus's own news as `[INFO]` messages (neutral colour, no reminders; an
  all-clear only for the conditions whose ending is news, below), for channels with **System
  notices** on (`system_notices`, off by default; a channel's
  alert level can be **None** = `alerts` off, for a notices-only channel; one with neither is
  refused). A loop in the server checks once a minute, since most sources are only read on request:
  the release cache (`appUpdateStatus`), the update-dir status files (core self-update outcome), the
  probes' check-in rows (versions, updater versions, OS patch status), the core OS report (security
  updates, reboot, Zabbix candidate), discovery jobs past a watermark, and channels whose last
  attempt failed. **Conditions** are tracked in `notice_conditions` (since when) and told once they
  have held their minimum time (probe/updater behind 6 h, security updates 48 h, reboot and Zabbix
  update 24 h, failing channel 30 min, a probe not reporting to Argus 15 min, a new release at once);
  **events** are told once, except that the "updated to" events of one fleet update (probes, or
  their updaters, moving to the same version) wait five minutes after the first in `notice_pool`
  (app_meta, so a restart loses nothing) and go out as one message listing the probes; a single
  one keeps its own words and a failure is never pooled. The
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
- Secrets entered in the UI later (placeholders for now). A told condition whose ending is news carries an all-clear (`cleared` on the notice, kept in `notices_sent` with since when it held and where it belonged): when it ends, the same destinations get "Probe <site> is reporting to Argus again", "Notification channel delivering again", "Core backup succeeded again", "Security updates applied", "rebooted", "no longer short on CPU", with how long it held. An update available or a probe behind ends quietly: someone acted, and the outcome is told as its own event.

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
a RESOLVED is titled with what came back, not the problem that ended ("GitHub answers again from
this site", "UPS back on utility power"): every trigger in an Argus template carries that sentence
as its `argus.recovery` tag (`TestTriggersSayHowTheyRecover` holds every template to it), the
notifier keeps it from the problem's tags while the alert is open (`notify_events.recovery`), Argus's
own problems word theirs ("... is collecting again", "SNMP is responding again"), and a trigger from
elsewhere reads "Back to normal: <problem>"; it leads with "Back after 3m" and shows the reading
now, without the threshold -
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
13) Settings (FQDN/allowed-hosts, retention, proxy status) · 14) Changes (the change log, section 7c).

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
  carries them per item as `thr`. A trigger over several items counts only the comparisons on this
  one, and there a `>0` is a "has data" guard (Zabbix's own templates add `and last(total)>0` to
  "Lack of available memory"), not a threshold. On multi-channel group charts only the **main channel** is banded -
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
  widest tick, and a tag always sits exactly at its line's value: where two would overlap, only one is
  drawn (the first channel's before the others', an error before a warning) and the other line stays,
  untagged; a tag is never moved off its value (`thrTagged`). A line whose scale has no axis (a third unit on a two-axis chart) falls back to a
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

## 13. UI surface and responsive targets
- **The surface (v0.9):** the content area is one surface (`--surface`: white in light, the deep
  page tone in dark; `.main` and `.content` carry it). A view's own panels (`.content > .panel`)
  lie flat on it, edge to edge, their rows divided by hairlines and inset by `--gutter` (22px, 14px
  on phones); every panel is flat (`.panel + .panel` divide with a hairline, the Reports columns
  with a vertical one), and so are the sections and cards inside: `.acct .card`, `.set-card`, `.chan`,
  `.thr-list-row` are stretches of the surface under a hairline, inset by the gutter. Nothing in a
  page is boxed; floating things (menus, dialogs, toasts, the sign-in card and the full-page error,
  which float on the page itself), the inline editors a row opens and the sidebar keep `--panel`.
  The sticky, folding head and the table gutters are desktop rules (min-width 769px): a phone stacks
  rows into cards with insets of their own, and its head scrolls with the page. A row under the pointer takes
  `--hover`. A view's list closes with a hairline under its last row (`.content > .panel`, the Reports
  grid; the rows draw only the lines between them) and the content ends a gutter below it; a view's
  description (`.panel-intro`) is a row of its own, with room under the text. `.main` turns scroll
  anchoring off: a folding head shrinks, and the browser would otherwise move the scroll position to
  hold the rows still, pulling the head back across the fold (it flickered on a slow wheel). A list's header (`.content > .panel > .phead`) sticks under the topbar (`--topbar-h`,
  kept by `useStickyHeads`, which measures the heads on each scroll frame) and folds to one line (`.stuck`) while scrolled past. Settings is
  five groups (`SETTINGS_GROUPS`, a caption over each, the header's jump links scroll to them):
  This Argus, Monitoring, Hosts, Access and security, Data; Account is three (`ACCOUNT_GROUPS`:
  Preferences, Notifications, Sign-in and security), the per-user Interface preference (Advanced
  mode) among its Preferences. `JumpLinks` draws the links for both.
- **Tree order (Reorder mode):** the Monitoring tree's order is Argus's own, per sibling set (a
  parent's direct hosts and subgroups as one list, `PUT /api/tree/order`), set in Reorder mode by
  admins and helpdesk. Every row has up/down arrows; with a mouse or trackpad (`(hover: hover) and
  (pointer: fine)`) each row also takes a grip and drags (HTML5 drag and drop on the rows). Dropped
  above or below a sibling, a row takes that place (`reorderSiblings`); a host dropped on the middle
  of a group row moves into that group (`setHostGroups`, the source group swapped for the target),
  and dropped between that group's rows it lands there (the target's order is saved first, then the
  move, so the host takes its place when the tree reloads). A group only moves among its siblings
  (its hierarchy is its Zabbix path, which Rename changes), a host never lands between sites, and
  nothing drops onto itself or into the group it is already in. The dragged row dims, the target
  shows a line above or below, a group taking a host lights up (`.dragging`, `.drop-before`,
  `.drop-after`, `.drop-into`); the arrows stay for keyboards and phones.
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

**Sizing pass (2026-10-19), from measurements.** One Argus sensor costs 0.022 values/s of
Zabbix "required server performance" (lab: 2,312 sensors, 50.97 values/s), about one polled value
every 45 s; most sensors are dependent items carved out of one poll, so history writes run up to
twice the required-performance figure. A device brings, by class: Windows over SNMP ~23 sensors
(plus one per watched service), Linux over SNMP ~25, XCP-NG ~18, UniFi switch ~41, UniFi gateway
~38, access point 16, unRAID ~105, ping only 5, a probe's own health 40; an HTTP endpoint add-on 4,
a TCP port 1, DNS resolution 3. An SNMP network device (the family still to build) is ~10 fixed
plus 2 per discovered interface, so the interface filter is the biggest lever on the total: filter
to ports that are up or carry a description, as the Linux and Windows classes filter to physical
interfaces.

**The work deployment (19 sites, ~650 devices, ~5,000 PRTG sensors)** comes out at ~12.5-13k
Argus sensors, ~280 values/s required performance, up to ~570 values/s written. Recommended:
history 31 days and trends 2 years in Settings > Retention (~45 GB with Timescale compression;
90 days of history would be ~78 GB). Core VM **8 vCPU / 16 GB / 200 GB SSD**, one VM as today
(no database split needed at this load). Zabbix server, above its defaults: CacheSize 256M,
HistoryCacheSize 256M (30 min of proxy backlog), HistoryIndexCacheSize 64M, TrendCacheSize 64M,
ValueCacheSize 512M, StartDBSyncers 8, StartTrappers 16 (19 active proxies), StartPreprocessors 8,
StartPollers 16, StartPingers 4, StartSNMPPollers 2, Timeout 10 (SNMP over VPN); PostgreSQL from
timescaledb-tune (the first boot runs it) with max_connections 200. Probes size their own
pollers from their hosts (14c); the image's ceilings for the biggest site (~1,800 sensors,
~40 values/s): CacheSize 64M, ProxyMemoryBufferSize 128M, so a 30 minute WAN outage stays in
memory before the SQLite spool; 1 vCPU / 1 GB as a container, 2 vCPU / 2 GB as a VM. Applied by
`argus-size-zabbix` (deploy/core/host): its unit runs it at every boot, with timescaledb-tune,
before PostgreSQL and the server start (a postgresql@.service drop-in orders the instances), so a
VM given more RAM or cores re-sizes itself on the reboot the resize needs anyway; the appliance
first boot runs it too (PostgreSQL's limit and tune in the database step, the server block after
the TLS snippet, so its values win), and a hand-installed core runs it once after appending the
snippet. The sizes scale with RAM and cores (about 7 % of RAM in Zabbix shared memory) and never
go below the snippet's.
The probe image carries its ceilings in its next revision.

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
**Update now (same channel).** The Updates page offers a pending minor as a button: Argus drops
`zbx-update-request.json` and a path unit (`argus-zbx-update.path`) starts the same script within
seconds, outside the window; it removes the request as it starts and leaves
`zbx-update-result.json` (done or failed, the versions, why it ran, one sentence) for the page,
which polls until the host reports the new version and shows a failure with its reason. The
reporter's `zbx_update_now` flag says the host honours requests; a core whose scripts predate it
keeps the window and the hint to re-run the patching installer, and the API refuses the request
(from the host-tools-carrying release on, the updater brings the scripts and the hint goes away by
itself).
**Reboot now and Patch now** ride the same channel: `reboot-request.json` makes the reboot watcher
reboot right away if the OS still asks for it (`reboot-result.json` says rebooting, and the page
waits for the first report after boot), `patch-request.json` makes `argus-patch` run
`unattended-upgrade` now, the same security-only policy that runs nightly, and report
(`patch-result.json`); the reporter's `reboot_now` / `patch_now` flags gate the buttons and the
API the same way. **Without the host reporter** (Argus as a container on Unraid with Zabbix
elsewhere, or a core on a distro the installer doesn't cover) the card says the core's OS is managed
outside Argus and shows neither windows nor buttons; the probe VMs still list.

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
strictly local (Argus never runs `apt` remotely). Both reporters (and the backup manifest) name the
OS the same way: Debian by its point release, "Debian 13.7" (`DEBIAN_VERSION_FULL` in os-release,
or `/etc/debian_version` before Debian 13 added it), any other OS by its `PRETTY_NAME`; a VM whose
reporter predates that shows "Debian 13 (trixie)" until its host tools update.

**The Zabbix frontend, kept narrow (`argus-web-guard`).** A host tool of the core (script, a
oneshot service enabled at boot and run when an update installs it, an hourly timer that puts it
back). Three things, each changing only what it owns: (1) a `conf.d` file in every installed
php-fpm turns session upload progress off (Zabbix never uses it; it was the confirmed trigger of
PHP2Shell, CVE-2026-109619), php-fpm reloaded only when the file changed; (2) a core installed by
hand gets the appliance's https frontend: when no nginx server with `ssl` proxies to the zabbix
vhost's plain `listen` port, it writes `conf.d/argus-zabbix-https.conf` (`:8443`, the same server
block as the appliance's) with a certificate in `/etc/nginx/argus` signed by the CA in
`/docker/argus/pki` (the machine's name and address, two years, a random serial so the CA's folder
is never written), checked with `nginx -t` and taken back when nginx refuses it; skipped, said once
per boot, when `:8443` is held by another service or the CA isn't there; and removed again if
another https server for the frontend appears (the appliance's, or the admin's own). Only where the
zabbix vhost lives: an Argus on its own VM runs the tool too and finds nothing to do, and a Zabbix on
its own VM never runs it (deploy/README.md shows the same server by hand); (3) when nginx serves the
frontend over https on this machine, an iptables and ip6tables chain `ARGUS-ZBX-WEB`, jumped to from
INPUT for the plain port, lets in only `lo` and the Docker bridges (`docker+`, `br-+`) and resets
the rest. Interfaces, not addresses: a container reaching the VM's LAN address still arrives on its
bridge, and a DHCP lease change changes nothing. Without an https frontend the plain port stays open
(the Zabbix UI lives there), and so it does on a core with `/etc/argus-core/zabbix-http-open`; either
way an earlier chain is removed. A run that finds everything in place changes and logs nothing.

**The core's certificates renew themselves (`argus-cert-renew`).** A host tool of the core (script, a
oneshot service with the manifest's `run` flag so an update runs it, a daily timer). Two
certificates the monitoring CA in `/docker/argus/pki` signed live on the core: the https front's
(`/etc/nginx/argus/argus-web.crt`: Argus on `:443` on the appliance, the Zabbix frontend on
`:8443`), and the Zabbix server's TLS certificate (`TLSCertFile` in `zabbix_server.conf`), which
every probe checks on `:10051` by issuer and subject. Thirty days before its end each is signed again
with the same CN and subject alternative names and a new key, for two years but never past the CA's
own end, the new pair taking the old one's owner and mode by a rename in the same folder; then nginx
is tested and reloaded, or zabbix-server restarted and checked active, and a refusal puts the old pair
back and the service on it again. Because the subject and issuer stay the same, a probe needs nothing.
Left alone, said once per boot (by key, not by text, since the text counts days): a certificate the
monitoring CA did not sign (your own). Without the CA on the machine (Argus elsewhere, or a CA kept
offline) it does nothing. A Zabbix on its own VM never runs it.

**The monitoring CA renews itself.** It lasts ten years and every probe and the Zabbix server trust
it, so it is renewed in place rather than replaced: two years before its end `argus-cert-renew` signs
the CA certificate again with its own key (`openssl x509 -signkey`: the same subject, key and key
identifier, a new serial, ten more years). A certificate checks against an issuer by name and key, so
every certificate the old one signed verifies with the new one and nothing re-enrolls. It is taken
only when it is the same CA, signed by itself, ending later, and every certificate of the machine the
old one trusts verifies with it; then it replaces `ca.crt` (owner and mode kept), the copy Zabbix
reads (`TLSCAFile`, when it holds just this CA) too, and zabbix-server restarts, a refusal putting both
back. Argus reads the CA file again every five minutes (pki.Refresh: the same name and key, else the
one loaded at start stays), for enrollment and for the Zabbix API's TLS trust. The probe reports the
SHA-256 of its `ca.crt` in its minute check-in (`ca`), and Argus answers a different one with its CA
(PEM). The probe takes it only as a renewal of the CA it has (one certificate, the same name and key,
valid now, ending later; anything else could let another server pass for the core) and saves it for
the proxy's next start, which reads the CA only then; a proxy still running on a CA within 30 days of
its end stops itself so Docker's restart policy brings it back on the renewed one. System notices: the
renewal itself (with the old end, until when PCs that trust the old certificate keep working), a probe
on another CA a day after (`probe-ca:`: one that refused it, or an image that doesn't report its CA),
and a CA within a year of its end that nobody renewed (Argus on its own machine; `RENEW_CA=1
./gen-certs.sh` signs it again the same way).

**Probes renew their own certificates.** A probe's key never leaves it, so the core can't renew its
certificate; the probe does, from its minute check-in loop (not the updater sidecar, which is
optional and holds the Docker socket, not the key). Thirty days before the end it makes a new key and
request and posts `{csr, cert, sig}` to `/api/probes/renew-cert` with its check-in token: `cert` is the
current certificate, `sig` the request's text signed by the current key (`openssl dgst -sha256
-sign`, base64). Argus signs (five years, CN forced to the token's proxy, capped at the CA's end) only
when the current certificate is the monitoring CA's (`pki.Signed`, issuer and signature, time left
out so an ended one still counts) for that proxy and its key signed the request: a token alone gets
nothing. The probe takes the answer only for its new key, its name and the CA it trusts, ending later,
keeps `proxy.crt.old` / `proxy.key.old`, and stops its proxy so Docker restarts it on the new pair
(the proxy reads it only at start); a failure is said and tried again hourly. Zabbix pins issuer and
subject, which don't change, so nothing changes on the server. The probe reports its certificate's
end at check-in (`cert_end`), kept per probe with the last renewal; a probe within 14 days of the end
is a system notice (`probe-cert:`). Settings > Certificates lists the core's certificates from
`core-certs.json` (written by `argus-cert-renew` at every run into the update dir) and every probe's.

**Guest agents on the appliance VMs (`argus-guest-agent`).** One image runs on every hypervisor, so it
carries every guest agent and each starts only on its own: xe-guest-utilities for Xen (XenServer's
GitHub release, downloaded and taken only when its SHA-256 matches the one pinned in the script; the
.deb starts it with a SysV script, which systemd runs only through a compatibility layer it is
dropping, so the script installs XenServer's own native unit from their repository under the same
name, with Debian's paths and `ConditionVirtualization=xen`, and moves the running agent to it),
open-vm-tools
(its unit has `ConditionVirtualization=vmware`), qemu-guest-agent (bound to the guest agent channel's
device) and hyperv-daemons (bound to Hyper-V's devices), the last three from Debian. The image build
runs `argus-guest-agent --all` with service starts held (`policy-rc.d`), since the build VM is KVM and
xe-guest-utilities' start fails there. On a running VM, the same script is a host tool in both
repositories (one file, diffed weekly by scan.yml) with a oneshot unit enabled by the host-tools
channel: at boot it installs the agent of the hypervisor `systemd-detect-virt` names when the VM has
none, and an install that failed (no network yet at the site) is tried again an hour later
(`Restart=on-failure`). Only on the appliances (`host-tools.json` on a probe VM, the first-boot state
folder on the core VM): a core installed by hand picks its own agent. A Xen agent already there
(xe-guest-utilities or xen-guest-agent) is kept. xen-guest-agent (Rust) is the likely successor;
it replaces xe-guest-utilities here once it has a current release published somewhere stable.

**A quiet console on the appliance VMs (`argus-console-quiet.service`).** The Debian cloud image
boots without `quiet`, so the kernel's console log level let every informational line (veth ports
of each container restart, generator notes at each `daemon-reload`) print over the console menu on
the hypervisor console. A oneshot unit early in boot (after systemd-sysctl, before sysinit.target)
sets the console level to 4: errors and worse still reach the screen, everything stays in the
journal. One file in both repositories, delivered by the host-tools channel; its conditions keep it
to the appliances (`host-tools.json` on a probe VM, the first-boot state folder on the core VM).

### The probe host-tools channel

The core's pattern (above) applied to the probe VM, so a probe VM's host side follows the probe
release instead of waiting for a new probe-vm image. `deploy/probe-vm/host/` in argus-probe holds
the VM's host files (the OS reporter, the console menu and its login wrapper, the apply script,
their units) with a `manifest` of the same grammar, and `install-host-tools.py` (Python, standard
library, the Go installer's logic: write only what changed, atomically, with the manifest's mode,
`@STATE_DIR@` rendered, the reload marker dropped when any file changed, so the VM reloads and
re-reports at once, as the core does through its `run` entries). The probe image
carries that folder as `/host-tools` (a named build context, `LABEL io.argus.host-tools`), and the
image build of the VM runs the same installer against `/` and writes `host-tools.json` into the
probe's data folder: that file is the sign of a probe VM. The argus-updater's probe-watch mode
(0.2.17) runs the probe image once after each probe update, and daily, as root and offline, with
`/usr/local/sbin`, `/etc/systemd/system` and the data folder bound; a probe as a container
elsewhere (no sign, or no systemd to bind) is skipped once per image. The sidecar's mount of the
data folder is read-only, so the probe image writes the outcome file for it, and the sidecar's
own check-in carries the report as `host_tools` (state, reason, version, when, files written,
reload); Argus keeps it on the probe (`probe_agents.host_tools`) and shows it on the Updates page
as a **Host tools** row under the probe's sidecar (the probe version they came from: up to date,
updating while the sidecar writes a newer probe's, failed with the reason), and on the Probes page
as a pill in the VM OS column only while they're updating or failed. `argus-host-tools.path` on the VM does the
reload and re-runs the reporter. The reporter now also keeps `os-status.json` in the data folder,
for the console. A change to `deploy/probe-vm/host/**` is a probe release like any image change.

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
2. **database** - `argus-size-zabbix` (PostgreSQL's connection limit), `timescaledb-tune` for the deployed RAM, `zabbix` role + DB with a **generated**
   password, schema import, TimescaleDB conversion.
3. **pki** - CA (`CN=Monitoring Core CA`) + core server cert; CA mounted RO into Argus so **probe
   enrollment works out of the box**. It lives in `/docker/argus/pki` (root's, readable by the
   container's group), mounted as `/ca` and over `/data/pki`, both read-only, so the container can't
   replace it through its writable `/data`.
4. **zabbix** - `zabbix_server.conf` (DB + TLS/tuning snippet + the sizes for this VM), frontend `zabbix.conf.php` written
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

- **Tools** (`deploy/core/host`, in the host tools manifest that `install-host-tools.sh` installs
  and the argus-updater keeps current from the Argus image; `install-backup.sh`, which `setup-core.sh`
  runs in both modes and `setup-core-patching.sh` on an existing core, adds the packages and the
  archive folder; the core VM image bakes them in):
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

## 14f. Appliance console (`argus-console`)

What an operator at the VM's console (or on an SSH login of the console account) sees, instead of a
bare shell prompt. Modelled on the console menus of mail and firewall appliances: a status block,
then numbered actions, with a shell as one of them.

- **Where it comes from.** `deploy/core/host/argus-console` (Python, standard library) and its
  login-shell wrapper `argus-console-login` ride the host tools (14c), so every core host that has
  them gets the menu as a command: `sudo argus-console`. It runs as root because everything it
  touches is root's: the Docker socket, the shared update folder, networkd's files, the journal,
  apt, the partition table. `sudo argus-console --make-login-shell [user]` (the account that ran
  sudo when none is named) wires an account so that its logins open the menu instead: the
  wrapper becomes the login shell (added to `/etc/shells`) and a sudoers drop-in
  (`/etc/sudoers.d/argus-console`, kept only if `visudo -cf` accepts it) lets that account run the
  menu, and only the menu, without a second password, so the one typed at login is the
  authentication. The appliance's first boot runs exactly that for the Debian user;
  `--remove-login-shell` puts bash back. An ssh command, scp or sftp with the account is handed
  to bash untouched. "Open a shell" drops privileges back to the account and runs bash; sudo
  there asks the password and logs as always. Every action logs to the journal under
  `argus-console`.
- **Versions in the status block come from the image, not the container.** A container's labels
  are a copy taken when it was first created, and the updater's recreate clones that config, so
  on a long-lived core they still say what the first image said (a lab core showed v0.4.11 and
  0.2.1 under v0.9.4 and 0.2.16). The menu reads the container's image id and that image's
  labels: the updater's `io.argus.updater.version` first, then the OCI version label the core's
  build writes from `git describe`.
- **Status block.** An ASCII "ARGUS" banner (plain characters, so a serial console and any SSH
  client render it), then hostname and uptime; the primary link's addresses, gateway, DHCP or
  static (static = the console's own `00-argus-static.network` exists), the interface; DNS servers
  from resolved; the https address that always works (the VM's IP) plus the name on the
  appliance's own certificate; NTP sync and the time zone; `/` usage, with `/docker` on its own
  line only when it is a file system of its own; the OS line from
  `os-status.json` (security updates, reboot pending, a newer Zabbix); the units (PostgreSQL,
  Zabbix server, nginx, Docker) and the containers (`argus`, `argus-updater`) with their health and
  the image version label. The status is a courtesy: if any of it fails, the menu still comes up.
- **Networking.** DHCP or a static address. Static writes `00-argus-static.network` for the primary
  interface (address with prefix, gateway inside that network, one to three DNS servers, optional
  search domain); it sorts before the image's `10-argus-dhcp.network`, and networkd applies the
  first matching file, so deleting it is the way back to DHCP (the menu's DHCP choice does that).
  Applied with `networkctl reload` + `reconfigure`. **Lock-out protection:** over SSH (a
  pseudo-terminal) the change arms a `systemd-run` timer that reverts the files after 180 s; the
  timer is cancelled by Enter in a session that survived the change (a DNS-only change, say) or by
  any new login of the menu within the window, since reaching the VM on the new address proves it.
  On the VM's own console nothing is armed. The files before the change sit in
  `/run/argus-console/` until kept or reverted.
- **The rest.** Hostname (`hostnamectl` plus the `127.0.1.1` line of `/etc/hosts`; nothing else is
  tied to it). Connectivity test: gateway ping, DNS, TCP to ghcr.io, Docker Hub and deb.debian.org,
  NTP. Restarts: `docker restart` for Argus and the updater (the same on the appliance and on a core
  installed by hand), `systemctl restart zabbix-server`. Logs: the last 150 lines of a chosen
  service, paged. OS updates: `unattended-upgrade -v` on the terminal, then the host reporter so
  Argus sees the result. Grow the disk: `growpart` on the root partition then `resize2fs` or
  `xfs_growfs`, after the virtual disk was enlarged in the hypervisor (the image carries
  `cloud-guest-utils` for it). Keyboard layout (`/etc/vconsole.conf`, as first boot does) and the
  time zone, which is only offered when Argus doesn't manage it (`timezone.json` absent; otherwise
  the `argus-tz-check` timer would put Argus's setting back within the hour, so the menu says to
  change it in Settings). Console password (`passwd` for the login account). Reboot and power off
  ask for the word. Log out ends the session.
- **Probe VM.** The same file (`deploy/probe-vm/host/argus-console` in argus-probe, byte for byte
  the core's; the weekly scan fails when the copies drift) tells the host apart at start: a probe
  VM has the proxy's `enroll/proxy.env` in its data folder and no Zabbix server. The probe profile
  shows the site, the core it reports to (the Zabbix server, pinned on the container or handed out
  by Argus, and the check-in address), the proxy and updater containers and the OS line from the
  reporter's local copy; its items are the probe restart, a check-in on demand with the probe's
  own credential (proves the path, moves the probe's last check-in in Argus), the logs, and the
  core it talks to (for the day the core moved and Argus can no longer tell the probe itself:
  `proxy.env`, and `probe.env` when the server is pinned there, then a restart). No password item:
  the break-glass password is Argus's to hold and reveal. First boot makes the wrapper the
  break-glass account's login shell. It reaches probe VMs through the probe host-tools channel
  (14c).
- **Tests**: `deploy/core/host/tests/test_host_scripts.py` drives the actions with faked commands
  and a rooted file tree (static and DHCP files, the revert timer, hostname, status, restarts, disk
  growth, connectivity, the login wrapper).

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
- **A probe whose report stops is flagged.** The proxy container's minute report (version,
  capabilities, process counts, CPU) is what hands it discovery jobs and what the process sizing works
  from, yet nothing showed when it stopped: Zabbix's "online" is the proxy's data channel, and the
  sidecar keeps `last_checkin` fresh with the same token. Probe images r8 to r28 sent a report Argus
  couldn't read (an empty body, refused with 400) for two weeks unnoticed. So the check-in that
  carries `scans` (only the proxy container's minute report does; not its start-time sync, not the
  sidecar) stamps `probe_agents.report_at`, and `reportStale` (`probereport.go`) flags a probe Zabbix
  hears from with no report for 5 minutes, counting from the newest of its last report, its check-in
  credential (`created_at`) and the core's own start, so a new probe or a restarted core gets the full
  grace. The Probes page shows **not reporting to Argus** beside online (the why on hover), the probe
  pickers of the discovery dialogs say "(not reporting to Argus)" and refuse it instead of "(needs
  probe update)", its Processes panel says why it has no counts, and a system notice goes out after
  15 more minutes (`probe-report:<proxy>`). A re-enrollment resets `report_at`.
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
  three sections laid out the same way, in rows. A group is one machine or one setting: its label on
  the left, then one line per part with its version, one status pill (up to date, available, queued,
  updating, failed) and its **Update** button, and under a part its step log or a hint. Settings
  (the core's channel, the fleet target, the core's reboot and Zabbix windows) are groups of their
  own whose value reads like a version and is changed in place. **Argus core**: the channel (the tag
  the core runs under, `channel` in `GET /api/version`), then the core, its sidecar and the
  collectors. **Probes**: the fleet target, then every probe's proxy, sidecar and (on a probe VM) the host
  tools the sidecar installs from the probe image, with **Update all**
  for every one that is behind. **Operating systems**: the two core windows, the core VM (OS, Zabbix),
  each probe VM, and the newest probe VM image. The Probes page keeps the versions as read-only status in the same words; a probe that
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
- **The core host's scripts and units ride the image too.** Everything the core VM runs outside
  the container (the OS reporter, the reboot, patch and Zabbix minor-update watchers, the timezone
  check, the sizing tool, the backup tools, their systemd units) is a file in `deploy/core/host`,
  listed in its `manifest` (destination, mode, whether `@STATE_DIR@` is rendered, whether the unit
  is enabled, whether a service runs again after an update: `run`). `install-host-tools.sh` installs them from that list the first time (`setup-core.sh`
  in both modes, `setup-core-patching.sh` on an existing core, the appliance image at build). The
  Argus image carries the same folder (`/host-tools`, the `hosttools` build context, labelled
  `io.argus.host-tools`) and `/argus install-host-tools --root /host --state-dir DIR` writes the
  files that changed (atomically, with their modes) and, when a systemd file changed or any file
  changed while the manifest lists a service to `run`, drops `host-tools-apply.json` in the shared
  dir. The `core`-mode updater runs that image once, as root,
  `--network none`, read-only, with the host's `/usr/local/sbin` and `/etc/systemd/system` bound
  under `/host` and the shared dir at `/update`, on the same rhythm as the collectors, and only
  where the reporter's `os-status.json` shows the host was set up as a core (Argus on Unraid or a
  plain Docker host is skipped); it reports in `host-tools.json`, shown on the Updates page. A
  container cannot reload systemd: `argus-host-tools.path` on the host starts
  `argus-host-tools-apply`, which reloads, enables every timer and path unit the marker lists,
  starts again the services it lists to run (`argus-web-guard`, so a new guard acts at the update and
  not at its timer's next tick), and re-reports. From a core's first install on, nothing on its host side is ever re-run by hand.

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
change is logged, shown on the Probes page (Processes: running count, busiest hour, decision) and
told once as a system notice.

## 18d. Core health (implemented)

**What.** The Zabbix server's own host, **Zabbix server** (created by Zabbix itself), is adopted by
Argus at startup (`server.EnsureCoreHost`): it takes the **Argus core** class (internal, never
offered in the pickers, the core glyph in the tree) and the **Argus Core Health** template, and
Zabbix's stock *Zabbix server health* template makes way for it (unlinked and cleared: the two read
the same internal keys, and Argus's carries its thresholds and recovery sentences). Whatever agent
template the host has stays (Linux by Zabbix agent on the appliance: the VM's CPU, memory and
disks). Zabbix runs the internal checks on the server itself, so the sensors are the figures the
sizing pass (14b) is built on:

- `zabbix[requiredperformance]`, the "required server performance" the pass sized the core for, and
  `zabbix[wcache,values,all]` / `zabbix[vps,written]` as rates (values processed and written per
  second), with thresholds at the pass's design point (`{$CORE.VPS.*}` 300/500, `{$CORE.WRITTEN.*}`
  600/1000): "the fleet outgrew the sizing" is an alert, not a surprise.
- `zabbix[hosts]`, `zabbix[items]`, `zabbix[items_unsupported]` and `zabbix[queue,10m]`, each with
  a warning and a high threshold.
- the five caches' use (`wcache` history, index and trend, `rcache`, `vcache`) at
  `{$CORE.CACHE.WARN/HIGH}` 75/90 %: the answer is a bigger VM, which re-sizes itself at boot.
- `zabbix[process,<type>,avg,busy]` for eleven process types at `{$CORE.PROC.WARN/HIGH}` 75/90 %,
  one **Process load** sensor with a channel per type: the answer is more cores, same story.

- **Headroom**, four calculated sensors (hourly): days until items, hosts, required performance
  and the database disk reach their high thresholds, from a linear fit of the last 30 days
  (`timeleft`), warning under `{$CORE.HEADROOM.WARN}` 90 days and high under 30. Each row reads as
  a sentence, label then value: "Database disk full in 121 days", "Hosts at the limit in over a
  year"; beyond a year the value is the words (that far out a linear fit says nothing more
  precise), and the number, capped at two years, stays for the chart and the triggers. The chart
  says what was, these say when it runs out. The disk one reads
  the Linux agent template's root filesystem (`vfs.fs.dependent.size[/,pused]`, the appliance keeps
  the database there) against `{$CORE.DISK.HIGH}` 90 %; a core set up by hand without that item
  sees the sensor unsupported, with Zabbix's reason. The caches and process loads get no forecast
  on purpose: they jump with configuration rather than grow, so a fit on them is noise.

The items are named "Core ..." so the sensor curation files them under **Core** (the same keys file
under **Probe** on a Probe host), in a pinned reading order (`itemLabelOrder`): load first, each
gauge followed by its forecast, then the fleet it carries, then processes and caches (the answers:
cores, RAM), the database disk last. Thresholds sit on the Thresholds screen like any class. A core
installed by hand whose server host was renamed is left alone and logged; renaming it back adopts it
at the next start. A sensor Zabbix has never read shows no reading ("-") rather than Zabbix's
placeholder zero, on every host.

## 19. Parking lot / future
- **Android native app** with push notifications (device registers with Argus → notifier delivers
  via a "push"/FCM channel) - the planned last step (ROADMAP §I). iOS undecided (would need APNs).
- Token-based enrollment service (Phase 1 backend) + "Add probe" wizard (Phase 4/6 UI).
- Golden probe **VM template** (Packer) + cloud-init for scaled/work rollout (Phase 6).
- Sizing pass before the ~6000-sensor work deployment.
```

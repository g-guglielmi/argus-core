<p align="center">
  <img src="web/public/argus-logo.png" alt="Argus" width="150" />
</p>

# Argus

A self-hosted, PRTG-style monitoring cockpit layered on Zabbix - a Go backend that serves
an embedded React SPA and talks to Zabbix via its JSON-RPC API. App data (users, sessions,
notification channels, suppressions) lives in embedded SQLite; metrics stay in
Zabbix/TimescaleDB and are read through the API. Single distroless container, no external
dependencies beyond a running Zabbix.

Current state: **v0.2.8** - monitoring tree with live data and charts, full auth (roles,
TOTP, passkeys), cross-site overview, Discord/Telegram/email notifications with trend
graphs, mobile-responsive layout, login rate-limiting, and at-rest encryption.

## Layout
```
argus/
  main.go                 entrypoint (HTTP server, graceful shutdown)
  internal/config/        env-based configuration
  internal/zabbix/        Zabbix JSON-RPC client (APIVersion for now)
  internal/server/        HTTP routes + embedded SPA serving
  web/                    React + Vite frontend (built into the binary via go:embed)
  Dockerfile              multi-stage: build frontend -> build Go -> distroless
```

## Endpoints
- `GET /healthz` - liveness (no dependencies)
- `GET /api/health` - readiness; includes whether the Zabbix API is reachable + its version
- `GET /*` - the React SPA

## Build & deploy (via GitHub -> GHCR -> docker run)

> **Prerequisite:** Docker Engine must be installed on the core VM. The Zabbix `setup-core.sh`
> installs only the Zabbix stack (Zabbix + PostgreSQL/TimescaleDB + nginx), **not** Docker -
> install it separately (`apt-get install docker.io`, or Docker CE from the official repo).

1. Push this repo to GitHub. The `build` workflow builds the image and pushes it to
   `ghcr.io/<your-account>/argus` (`:latest` on the default branch, `:vX.Y.Z` on tags).
2. The package starts **private**. Either make it public (GitHub → Packages → argus →
   Package settings → visibility), or on the core VM run
   `docker login ghcr.io -u <user>` with a PAT that has `read:packages`.
3. On the core VM (10.0.0.10):

```bash
docker run -d \
  --name argus \
  --restart unless-stopped \
  -p 8081:8080 \
  -v /docker/argus:/data \
  -v /docker/pki:/ca:ro \
  -e ARGUS_ZABBIX_API_URL=http://10.0.0.10:8080/api_jsonrpc.php \
  -e ARGUS_ZABBIX_API_TOKEN=<zabbix-api-token> \
  -e ARGUS_ADMIN_EMAIL=admin@example.com \
  -e ARGUS_ADMIN_PASSWORD=<strong-password> \
  -e ARGUS_COOKIE_SECURE=true \
  -e ARGUS_PUBLIC_URL=https://monitoring.example.com \
  -e ARGUS_TZ=Europe/Rome \
  -e ARGUS_SECRET_KEY=<run: openssl rand -hex 32 - set once, keep stable> \
  -e ARGUS_TRUST_PROXY=true \
  -e ARGUS_CA_CERT_FILE=/ca/ca.crt \
  -e ARGUS_CA_KEY_FILE=/ca/ca.key \
  -e ARGUS_PROBE_CORE_HOST=monitoring.example.com \
  -e ARGUS_RP_ID=monitoring.example.com \
  -e ARGUS_RP_DISPLAY_NAME=Argus \
  -e ARGUS_RP_ORIGINS=https://monitoring.example.com \
  ghcr.io/<your-account>/argus:latest
```

> **First run:** `ARGUS_ADMIN_EMAIL` / `ARGUS_ADMIN_PASSWORD` seed the initial admin **only when
> the database is empty**; they're ignored afterwards, so you can drop them on later runs.
>
> **Port note:** the Zabbix web UI already uses host **8080**, so Argus is published on host
> **8081** (→ container 8080). Behind a reverse proxy it's served at `monitoring.example.com`.
>
> **Passkeys** (`ARGUS_RP_*`) need HTTPS + a real domain - omit them for a plain-HTTP/IP setup and
> Argus falls back to password + TOTP. **`ARGUS_SECRET_KEY`** encrypts stored secrets at rest;
> keep it off the volume and **stable** (changing it makes existing encrypted values unreadable).
> **`ARGUS_TRUST_PROXY=true`** is required behind a reverse proxy for correct client-IP rate
> limiting (ensure the proxy sends `X-Forwarded-For`).
>
> **Probe enrollment** (optional): mount the monitoring CA (`ca.crt` + `ca.key` from
> `gen-certs.sh`) read-only and set `ARGUS_CA_CERT_FILE` / `ARGUS_CA_KEY_FILE` so Argus can sign
> probe certificates from the **Probes → Add probe** wizard. Set `ARGUS_PROBE_CORE_HOST` to the
> address probes dial for `:10051` (the FQDN, which must publish 10051 for remote sites, or the
> LAN IP). The Zabbix API token must have **super-admin** rights to register proxies. Omit the CA
> mount to leave enrollment off (probe status still works).

4. Open `http://10.0.0.10:8081` - you should see the Argus page reporting the backend as `ok`
   and the Zabbix API as **reachable** with its version. That confirms the whole
   GitHub → GHCR → docker run → Zabbix chain works end to end.

## Local development (optional, needs Go 1.22+ and Node 20+)
```bash
# terminal 1 - backend
cd argus && ARGUS_ZABBIX_API_URL=http://<reachable-zabbix>:8080/api_jsonrpc.php go run .
# terminal 2 - frontend with hot reload (proxies /api to :8080)
cd argus/web && npm install && npm run dev
```

## Configuration (env vars)

All configuration is via environment variables (`docker run -e …` / `--env-file`).

> **Runtime settings (admin UI).** Several of these can also be set from the admin **Settings**
> page with no redeploy: the **Zabbix API URL + token**, **`ARGUS_PUBLIC_URL`**, **`ARGUS_TZ`**,
> the **login rate-limit** vars, and the **session timeout** vars. Precedence is **env-wins**: when the variable is set it takes
> effect and the field is read-only in the UI; unset the variable to manage that setting in the
> GUI (stored in the database, token encrypted at rest). The rows below are marked _(UI)_.

**Core**
| Var | Default | Purpose |
|---|---|---|
| `ARGUS_ZABBIX_API_URL` | *(empty)* | _(UI)_ Zabbix JSON-RPC endpoint, e.g. `http://10.0.0.10:8080/api_jsonrpc.php` |
| `ARGUS_ZABBIX_API_TOKEN` | *(empty)* | _(UI)_ Zabbix API token (Bearer). Needs **write** scope for acknowledge/pause |
| `ARGUS_DATA_DIR` | `/data` | SQLite DB + encryption keyfile location (mount a volume) |
| `ARGUS_LISTEN` | `:8080` | address the server listens on inside the container |

**First-run admin seed** (used only while the user table is empty)
| Var | Default | Purpose |
|---|---|---|
| `ARGUS_ADMIN_EMAIL` | *(empty)* | email of the initial admin to create |
| `ARGUS_ADMIN_PASSWORD` | *(empty)* | password of the initial admin |

**Security**
| Var | Default | Purpose |
|---|---|---|
| `ARGUS_COOKIE_SECURE` | `false` | set `true` when served over HTTPS (Secure session cookie) |
| `ARGUS_SECRET_KEY` | *(empty)* | key for at-rest encryption of stored secrets. Empty ⇒ auto keyfile on the volume; set a long random value (e.g. `openssl rand -hex 32`) to keep the key off the volume. **Keep it stable.** |
| `ARGUS_TRUST_PROXY` | `false` | read the client IP from `X-Forwarded-For` (set `true` behind a reverse proxy) |
| `ARGUS_LOGIN_MAX_ATTEMPTS` | `7` | _(UI)_ failed logins per window before throttling |
| `ARGUS_LOGIN_WINDOW_MINUTES` | `15` | _(UI)_ rate-limit sliding window |
| `ARGUS_SESSION_MAX_HOURS` | `12` | _(UI)_ absolute session lifetime before re-authentication is required |
| `ARGUS_SESSION_IDLE_MINUTES` | `0` | _(UI)_ sign out after this long with no activity (`0` disables the idle timeout) |

**Notifications**
| Var | Default | Purpose |
|---|---|---|
| `ARGUS_PUBLIC_URL` | *(empty)* | _(UI)_ external base URL, for "Open in Argus" / acknowledge links in alerts |
| `ARGUS_TZ` | `UTC` | _(UI)_ IANA timezone for timestamps in notifications, e.g. `Europe/Rome` |

**Probe enrollment** (optional; enables the Probes → Add probe wizard when the CA is mounted)
| Var | Default | Purpose |
|---|---|---|
| `ARGUS_CA_CERT_FILE` | *(empty)* | path to the monitoring CA cert (`ca.crt`) mounted into the container |
| `ARGUS_CA_KEY_FILE` | *(empty)* | path to the CA private key (`ca.key`) - mount **read-only**; both must be set to enable enrollment |
| `ARGUS_PROBE_CORE_HOST` | *(Public URL host)* | _(UI)_ address probes dial for `:10051` (FQDN or LAN IP). Falls back to the Public URL's hostname |

> Enrollment also needs the Zabbix API token to have **super-admin** rights (to run `proxy.create`).

**Passkeys / WebAuthn** (optional; needs HTTPS + a real domain - omit for plain-HTTP/IP)
| Var | Default | Purpose |
|---|---|---|
| `ARGUS_RP_ID` | *(empty)* | relying-party ID = the FQDN, no scheme, e.g. `monitoring.example.com` |
| `ARGUS_RP_DISPLAY_NAME` | `Argus` | name shown by the authenticator |
| `ARGUS_RP_ORIGINS` | *(empty)* | comma-separated origins, e.g. `https://monitoring.example.com` |

**One-click self-update** (optional; enables the "Update now" button - see [Self-update](#one-click-self-update-optional))
| Var | Default | Purpose |
|---|---|---|
| `ARGUS_UPDATE_DIR` | *(empty)* | path of a volume shared with the `argus-updater` sidecar. Set (e.g. `/update`) to enable admin-triggered self-update; empty leaves it off (Settings then shows a manual update command instead). The core never gets the Docker socket - the sidecar does |

## One-click self-update (optional)

Argus **Settings → About** shows the running build and flags when a newer release is published. You
can wire up an in-app **"Update now"** button that upgrades the core to the latest release.

The Argus core is a public-facing, distroless, non-root container with **no Docker socket**, so it
cannot recreate itself. Instead, a small companion container - **`argus-updater`** - holds the socket
and does the work on the core's behalf. The two share a tiny volume: the core drops an update request
there, the updater pulls the newest release, recreates the core cloning its config, verifies it comes
up healthy, and **rolls back on failure** - reporting the result back so Argus shows a success or
failure banner. The public-facing core never touches Docker.

**Recommended (Docker Compose):** use [`deploy/updater/docker-compose.yml`](../deploy/updater/docker-compose.yml)
(fill in `.env` from the table above), then `docker compose up -d`. It runs both containers and the
shared volume.

**Manual (docker run):** add a shared volume + `ARGUS_UPDATE_DIR` to the core, and run the sidecar
alongside it:

```bash
# core: add these to your `docker run` above
  -v argus-update:/update \
  -e ARGUS_UPDATE_DIR=/update \

# the sidecar (holds the socket; not web-facing)
docker run -d --name argus-updater --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v argus-update:/update \
  -e ARGUS_CORE_CONTAINER=argus \
  ghcr.io/<your-account>/argus-updater:latest
```

**Unraid:** map the core template's *Self-update channel* to a host folder and set *Self-update dir* =
`/update`, then add the **Argus-Updater** template ([`deploy/unraid/argus-updater.xml`](../deploy/unraid/argus-updater.xml))
with the **same** host folder and your Argus container's name.

Beyond the plain "Update now" button (which updates in place, preserving the core's `latest` /
`testing` channel), Settings → About has a **Change channel or version** control to deliberately switch
the core between `latest`, `testing`, and recent pinned releases. A version pin sticks until you switch
back to a channel.

The sidecar also reports which tag the core container runs under (into the shared folder), so a box on
`:testing` is correctly offered newer testing builds even right after a release - when `:testing` and
`:latest` momentarily point to the same clean `vX.Y.Z` image and the core otherwise couldn't tell which
channel it's on. Without the sidecar, a build carrying a `git describe` suffix is treated as testing and
a clean release as latest.

> **Keep the sidecar current.** `argus-updater` recreates the core but not itself, so after upgrading
> Argus pull the newest `argus-updater:latest` and redeploy the sidecar too - otherwise it keeps
> running an older update script (e.g. one without channel/version switching).

> **Security note.** `argus-updater` has the Docker socket (host-level control), so keep it on the
> trusted core host. It runs no listening service - it only watches the shared folder - which is why
> the socket lives here and not on the internet-facing core. Leave `ARGUS_UPDATE_DIR` unset to disable
> the feature entirely; Settings then shows the newest version + changelog with a manual update command.

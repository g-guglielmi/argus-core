# Argus

A self-hosted, PRTG-style monitoring cockpit layered on Zabbix — a Go backend that serves
an embedded React SPA and talks to Zabbix via its JSON-RPC API. App data (users, sessions,
notification channels, suppressions) lives in embedded SQLite; metrics stay in
Zabbix/TimescaleDB and are read through the API. Single distroless container, no external
dependencies beyond a running Zabbix.

Current state: **v0.2.8** — monitoring tree with live data and charts, full auth (roles,
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
- `GET /healthz` — liveness (no dependencies)
- `GET /api/health` — readiness; includes whether the Zabbix API is reachable + its version
- `GET /*` — the React SPA

## Build & deploy (via GitHub -> GHCR -> docker run)

> **Prerequisite:** Docker Engine must be installed on the core VM. The Zabbix `setup-core.sh`
> installs only the Zabbix stack (Zabbix + PostgreSQL/TimescaleDB + nginx), **not** Docker —
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

> **First run:** `ARGUS_ADMIN_EMAIL` / `ARGUS_ADMIN_PASSWORD` seed the initial admin **only when
> the database is empty**; they're ignored afterwards, so you can drop them on later runs.
>
> **Port note:** the Zabbix web UI already uses host **8080**, so Argus is published on host
> **8081** (→ container 8080). Behind a reverse proxy it's served at `monitoring.example.com`.
>
> **Passkeys** (`ARGUS_RP_*`) need HTTPS + a real domain — omit them for a plain-HTTP/IP setup and
> Argus falls back to password + TOTP. **`ARGUS_SECRET_KEY`** encrypts stored secrets at rest;
> keep it off the volume and **stable** (changing it makes existing encrypted values unreadable).
> **`ARGUS_TRUST_PROXY=true`** is required behind a reverse proxy for correct client-IP rate
> limiting (ensure the proxy sends `X-Forwarded-For`).

4. Open `http://10.0.0.10:8081` — you should see the Argus page reporting the backend as `ok`
   and the Zabbix API as **reachable** with its version. That confirms the whole
   GitHub → GHCR → docker run → Zabbix chain works end to end.

## Local development (optional, needs Go 1.22+ and Node 20+)
```bash
# terminal 1 — backend
cd argus && ARGUS_ZABBIX_API_URL=http://<reachable-zabbix>:8080/api_jsonrpc.php go run .
# terminal 2 — frontend with hot reload (proxies /api to :8080)
cd argus/web && npm install && npm run dev
```

## Configuration (env vars)

All configuration is via environment variables (`docker run -e …` / `--env-file`).

> **Runtime settings (admin UI).** Several of these can also be set from the admin **Settings**
> page with no redeploy: the **Zabbix API URL + token**, **`ARGUS_PUBLIC_URL`**, **`ARGUS_TZ`**,
> and the **login rate-limit** vars. Precedence is **env-wins**: when the variable is set it takes
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

**Notifications**
| Var | Default | Purpose |
|---|---|---|
| `ARGUS_PUBLIC_URL` | *(empty)* | _(UI)_ external base URL, for "Open in Argus" / acknowledge links in alerts |
| `ARGUS_TZ` | `UTC` | _(UI)_ IANA timezone for timestamps in notifications, e.g. `Europe/Rome` |

**Passkeys / WebAuthn** (optional; needs HTTPS + a real domain — omit for plain-HTTP/IP)
| Var | Default | Purpose |
|---|---|---|
| `ARGUS_RP_ID` | *(empty)* | relying-party ID = the FQDN, no scheme, e.g. `monitoring.example.com` |
| `ARGUS_RP_DISPLAY_NAME` | `Argus` | name shown by the authenticator |
| `ARGUS_RP_ORIGINS` | *(empty)* | comma-separated origins, e.g. `https://monitoring.example.com` |

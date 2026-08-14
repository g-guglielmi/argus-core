# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/), and the project
follows [Semantic Versioning](https://semver.org/) (`MAJOR.MINOR.PATCH`).

**Versioning policy:** increment the PATCH (`0.0.x`) for each change during development —
`0.0.1`, `0.0.2`, `0.0.3`, … — reserving **`1.0.0`** for the first production-ready release.
Each release is a git tag `vX.Y.Z` that triggers CI to build the versioned image and publish a
GitHub Release from the matching section below.

---

## [0.4.0] - 2026-08-14

**Probe enrollment — one-click, from the GUI.** Adding a site probe no longer needs
`gen-certs.sh` or manual Zabbix proxy registration.

- **Probes → Add probe** (admin): pick a site name + token TTL → Argus mints a **single-use,
  time-limited token** and shows a ready-to-deploy **`docker run`** *or* **unRAID XML template**
  for the new **`argus-probe`** image, with the enroll URL + token filled in (the token is shown
  once). Pending/recent tokens are listed with status (pending / enrolled / expired) and can be
  revoked. `ARGUS_PROBE_CORE_HOST` (the address probes dial for `:10051`) is editable in **Settings**.
- **Self-enrolling probe image** (`ghcr.io/<owner>/argus-probe`): on first boot it generates its
  own key + CSR **locally**, redeems the token against `POST /api/enroll`, receives its signed
  certificate + `ca.crt`, and starts the stock Zabbix proxy. **The private key never leaves the
  probe.** Certs persist on the volume, so a restart doesn't re-redeem the single-use token.
- Argus signs the CSR with the **mounted monitoring CA** (same CA Zabbix already trusts; the leaf
  subject is forced to the token's proxy name) and registers the proxy in Zabbix via `proxy.create`
  (active, certificate-pinned by issuer + subject).
- Enrollment is **off unless the CA is mounted** — new config `ARGUS_CA_CERT_FILE`,
  `ARGUS_CA_KEY_FILE` (mount read-only), and `ARGUS_PROBE_CORE_HOST` (address probes dial for
  `:10051`, defaults to the Public URL host). The Zabbix API token needs **super-admin** rights to
  register proxies. The `/api/enroll` endpoint is IP rate-limited.

New: `internal/pki` (CA load + CSR signing), `enroll_tokens` table, `POST /api/enroll` +
admin `GET/POST/DELETE /api/probes/tokens`, `zabbix.EnsureActiveProxyCert`, and
`deploy/probe-image/` (Dockerfile + enrollment entrypoint) built by CI.

---

## [0.3.3] - 2026-08-14

**Self-service password reset.** Users who forget their password can now recover it themselves
via an emailed link — no admin intervention.

- A **"Forgot password?"** link on the sign-in screen takes an email and sends a **single-use,
  1-hour reset link**; the reset page sets a new password. The link (`/?reset=…`) carries a
  256-bit token whose **SHA-256 is stored** (never the token itself), consumed on use.
- **Anti-enumeration**: the request endpoint always responds the same way and does its work in
  the background, so it never reveals whether an account exists. Requests are **rate-limited** by
  IP and by email; the reset submission is throttled by IP.
- On success, **all of that user's sessions are revoked** (sign-out everywhere). **MFA is
  untouched** — a reset changes only the password, so two-factor is still required at sign-in.
- **Delivery reuses your existing email notification channel** as the SMTP sender — no separate
  mail config. The "Forgot password?" link only appears when an email channel is configured
  (advertised via `/api/features`, like passkeys). The link uses the Public URL when set, else
  the request's own origin.

New: `password_resets` table + token lifecycle, `POST /api/password-reset/{request,confirm}`,
a reusable `notify.SMTP` transport (factored out of the alert email sender).

---

## [0.3.2] - 2026-08-14

**Sidebar polish.**
- The desktop sidebar **remembers whether it's collapsed or expanded** across reloads (stored
  per-device, like the theme).
- Removed the **theme toggle from the sidebar** now that it lives on the Settings page. To keep
  it reachable for every role (Settings is admin-only), the theme switch is also on the
  **Account** page — available to all signed-in users.

---

## [0.3.1] - 2026-08-14

**Deep-link URLs — the view now lives in the address bar.** Navigation was tracked in React
state only, so the URL stayed at the base FQDN and a **reload always dropped you back to
Overview**; notification "Open in Argus" links also didn't survive a refresh.

- The active view is encoded in the URL — `?view=…`, with `&filter=…` for the status lists and
  `&host=…&item=…` for an open host/sensor in the Monitoring tree. **Reload, bookmark, and share
  now restore the exact screen**, and notification deep-links are reload-safe.
- **Back/Forward** work: tab switches and deep-link jumps push history; expanding a host or
  opening a chart refines the URL in place (so Back steps between screens, not accordion toggles).
- Admin-only views (Users, Settings) are clamped to Overview if a non-admin opens a shared or
  stale link.
- Frontend-only; no backend change and no new dependency (native History API).

---

## [0.3.0] - 2026-08-14

**In-app Settings (admin).** A new admin-only **Settings** page moves the most frequently
changed configuration off the `docker run` command and into the UI — no redeploy to change it.

- **Editable in the UI**: the **Zabbix API URL + token**, the **Public URL** (notification
  links), the **timezone**, and the **login rate-limit** thresholds. Changes apply live — the
  Zabbix client, notifier, and rate limiter are reconfigured in place, no restart.
- **Env-wins precedence**: if the backing `ARGUS_*` variable is set, that value is used and the
  field is shown **read-only** ("via env"). Your existing `docker run … -e …` keeps working
  unchanged; drop a variable to manage that setting in the GUI instead.
- The Zabbix token is stored **encrypted at rest** (same AES-256-GCM as channel credentials);
  it's never sent back to the browser. The connection card shows live reachability after a save.
- **Not** movable (stay in env, by design): `ARGUS_SECRET_KEY` (it *is* the encryption key),
  `ARGUS_COOKIE_SECURE` / `ARGUS_TRUST_PROXY` (they govern the session you'd edit them with),
  the passkey `ARGUS_RP_*` (changing them invalidates passkeys), and `ARGUS_LISTEN` /
  `ARGUS_DATA_DIR` (needed before the app starts).
- Theme selection now also appears on the Settings page (still a per-device preference; the
  sidebar toggle stays for everyone).

New: `internal/settings` (env/DB resolver + live apply), `GET`/`PATCH /api/settings`,
`zabbix.Client.Configure` and `ratelimit.Limiter.Configure` for runtime reconfiguration.

---

## [0.2.8] - 2026-08-14

**Encrypt secrets at rest.** Sensitive values in the SQLite database are now stored encrypted with
AES-256-GCM instead of plaintext: notification channel credentials (Discord webhook, Telegram bot
token, SMTP password), users' TOTP seeds, and the alert-link signing key.

- **Zero-config by default**: a key is generated once and kept in `<data>/secret.key` (mode 0600).
  For real protection of database backups, set **`ARGUS_SECRET_KEY`** to a long random string —
  it's hashed to the key and kept off the data volume. Set it on the first v0.2.8 deploy and keep
  it stable (changing the key, or losing the keyfile, makes existing encrypted values unreadable —
  channels would need re-entering and 2FA re-enrolling).
- Existing plaintext values are migrated to encrypted form automatically on startup. Encryption is
  transparent — values are decrypted only in memory when needed.
- Password hashes (argon2id), recovery codes (hashed), and passkeys (public keys) were already not
  reversible and are unchanged.

New: `internal/secret` (AES-256-GCM, marker-prefixed ciphertext, passthrough when disabled).

---

## [0.2.7] - 2026-08-14

**Login rate limiting** (brute-force protection). Repeated failed sign-ins are now throttled by
both **client IP** and **account**, with a `429 Too Many Requests` (and `Retry-After`) once the
limit is hit; a successful sign-in clears the counters.

- Applies to the password step and the TOTP-code step (per-account limiting means IP rotation
  can't grind a single account, and it works even behind a shared proxy IP).
- Tunable via `ARGUS_LOGIN_MAX_ATTEMPTS` (default 7) and `ARGUS_LOGIN_WINDOW_MINUTES` (default 15).
- **Behind a reverse proxy** (HAProxy), set **`ARGUS_TRUST_PROXY=true`** so the real client IP is
  read from `X-Forwarded-For` (ensure the proxy sends it, e.g. HAProxy `option forwardfor`).
  Without it, all requests share the proxy's IP; account-level limiting still protects each login.

---

## [0.2.6] - 2026-08-14

Mobile card polish:
- **Status-list kebab** no longer opens off-screen — the action cell kept its desktop 44px width,
  which pinned the kebab to the left of the stacked card so its menu opened past the screen edge.
  It now spans full width with the kebab on the right.
- **Users cards**: the name/surname values are right-aligned to match the role, 2FA, and passkeys
  rows (the email title stays left-aligned).
- **Trend sparkline restored on mobile**: v0.2.4 dropped the trend column to save width; it's back
  as a labelled "Trend" row in the stacked Overview/status-list cards (hidden only when a
  problem/sensor has no graphable series).

---

## [0.2.5] - 2026-08-13

- **Mobile card labels**: the stacked lists from v0.2.4 dropped their column headers, so on a phone
  the Users page and status lists read as unlabeled values. Each stacked cell now shows its label
  (Name / Role / 2FA / Passkeys, Value / Last check / Age), with the email/host as the card title
  and the kebab in the corner. Empty name/surname show a clearer placeholder.

---

## [0.2.4] - 2026-08-13

**Mobile-responsive layout.** The dashboard was desktop-only; on a phone the sidebar squeezed the
content, the status chips stacked, and tables ran off-screen. Now (≤768px wide):

- The sidebar becomes an **off-canvas drawer** — hidden by default, slid in by the ☰ button over a
  dimmed backdrop, and closed by tapping the backdrop or a nav item. On desktop ☰ still collapses
  the rail as before.
- The top bar wraps: title + ☰ on the first row, the **status chips on their own horizontally
  scrollable row**.
- Problem/status lists and the users table **stack each row into a card** instead of scrolling
  sideways; the monitoring tree drops its trend column and tightens indentation.

---

## [0.2.3] - 2026-08-13

- **Configurable timezone** for notification timestamps: set `ARGUS_TZ` to an IANA name
  (e.g. `Europe/Rome`); defaults to `UTC`. The binary embeds the tz database (`time/tzdata`) so
  it works on the distroless image without a system zoneinfo.
- **Email graph fix**: the inline chart now uses a fully-qualified `Content-ID` (`<chart@argus>`)
  for broader client compatibility (some clients, Gmail included, are picky about bare cids).

Note: pulling a new image doesn't replace a *running* container — recreate it (or update the
pinned tag) to pick up a release.

---

## [0.2.2] - 2026-08-13

**2-hour trend graph in alerts.** Every problem and recovery notification now carries a compact
chart of the offending sensor's last two hours, rendered server-side to PNG (pure stdlib, no new
deps) in the status color.

- The image is **uploaded directly** to each channel — Discord webhook attachment
  (`attachment://`), Telegram `sendPhoto`, and an inline `cid:` image in the HTML email — so it
  works whether the instance is internal-only or public, with no image URL to host or expose.
- Graphs are best-effort: non-numeric sensors or sensors without history simply omit the chart.
- The **Test** button now renders a demo graph too, so the whole message format previews at once.

New: `internal/server/chart.go` (history fetch + PNG renderer) and `internal/notify/multipart.go`
(shared multipart uploader).

---

## [0.2.1] - 2026-08-13

**Richer alert messages.** Notifications now carry status, context, and one-click actions.

- **Status icon + color** on every channel: a 🔴/🟡/🟢 indicator in the title, Discord's colored
  embed with structured Host / Site / Reading fields, and a colored HTML email (with a plain-text
  fallback) instead of plain text.
- **The reading that fired it**: current value plus a best-effort threshold parsed from the trigger
  expression, e.g. "Value: 96 % (threshold >90)".
- **Recovery duration**: resolved notices say how long the problem lasted ("recovered after 14m").
- **Open in Argus** deep-link straight to the offending sensor's chart (the SPA now honours
  `?host=…&item=…` on load). Requires the new `ARGUS_PUBLIC_URL` env (your external base URL);
  links are omitted when it's unset.
- **One-click acknowledge**: a signed, HMAC-verified link that acknowledges the problem. A GET
  shows a confirmation page (so link previewers can't silently ack); the POST performs it. The
  signing secret is generated once and stored in the data volume.
- Removed the stale "Soon" badge from the Notifications sidebar item (Probes keeps its badge —
  enrollment is still pending).

Next: the 2-hour trend graph in the message body (email inline / link-out, since the instance is
internal-only).

---

## [0.2.0] - 2026-08-12

**Notifications — alerting engine + channel management.** Argus now watches active problems
itself and delivers alerts, respecting the same suppression rules as the Overview: acknowledged,
paused, and hidden items stay quiet.

- **Channels** (admin, Notifications tab): add/edit/enable/delete **Discord** (webhook),
  **Telegram** (bot + optional forum topic), and **Email** (SMTP: STARTTLS / implicit TLS / none)
  targets, each scoped to **all sites** or one **host group**. A **Test** button sends a sample
  notification so credentials can be verified end-to-end. Config is stored in the SQLite data
  volume (plaintext — single-tenant, private-VM assumption; env-key encryption may come later).
- **Engine** (background goroutine, 30s poll): Warning and Error problems alert; a **60-second
  debounce** suppresses flapping; a **recovery** notice follows when a fired problem clears.
  Problems already active at first-ever startup are **baselined** (never retro-alerted), and a
  problem stays pending (does not fire) while it is acknowledged / on a paused or hidden host, or
  until a channel serving its site exists.
- **Routing**: a problem routes to every enabled channel whose site matches one of its host's
  Zabbix host groups (or is "all sites").
- New: `internal/notify` package (Discord/Telegram/email dispatchers), notifier state machine,
  `notify_channels` / `notify_events` / `app_meta` tables, and admin `/api/notify/*` endpoints.

---

## [0.1.0] - 2026-08-12

**First minor release — feature-complete UI.** No code changes since v0.0.32; this marks the
milestone where the redesigned interface and the core monitoring feature set are complete. From
here, `0.1.x` continues for fixes and the next features (notifications), with `1.0.0` reserved
for the production-ready release.

What 0.1.0 delivers:
- **Auth & users**: roles (admin / helpdesk / viewer), argon2id + server-side sessions, TOTP
  two-factor with recovery codes, WebAuthn passkeys, and admin user management including
  enable/disable.
- **Monitoring** (Zabbix-backed): a site → host → sensor tree (grouped by host group), curated
  "key" sensors with live values, per-sensor charts (uPlot, history + trends) and inline
  sparklines.
- **States**: acknowledge, pause (actually stop collecting), and hide (suppress) at host and
  sensor level — with durations, auto-expiry, inheritance, and honest graph gaps.
- **Overview & summary**: a cross-site active-problem list with deep-links into the tree, and a
  six-state status summary (OK / Warning / Error / Acknowledged / Paused / Hidden) whose chips
  open filtered, cross-site sensor lists.
- **Live Probes** view (real Zabbix proxy status) and a polished, theme-aware (dark/light),
  collapsible shell that updates on its own.
- Placeholders for the upcoming **Notifications** and **Probe enrollment** work.

---

## [0.0.32] - 2026-08-12

**New UI, stage 4 of 4 — the Users page (+ real "disable user"), and Overview spacing.**
This completes the UI port from the approved mockup.

### Added
- **Redesigned Users page**: inline-editable **email / name / surname** (save on blur), a role
  dropdown, and a per-user **⋮ menu** — Reset password · Remove 2FA · Remove passkeys ·
  Disable/Enable user · Remove user. Add-user is a toggle form in the header.
- **Disable user** is now a real feature: a disabled account **cannot sign in** (blocked on every
  login path — password, 2FA, passkey), shown faded with a "disabled" badge, and re-enablable.
  Guarded so you can't disable yourself or the last remaining admin. New `disabled` column
  (additive migration), `POST /api/users/{id}/disabled`, and email is now editable via PATCH.

### Changed
- **Overview** is now a properly spaced table (Host · Problem · Trend · Age · action), matching
  the status lists — no more large gap between the description and the controls on wide screens.

---

## [0.0.31] - 2026-08-12

**Mini-graphs (for real) & collapsed-sidebar fix.**

### Added
- **Inline sparklines** are back — and now real, drawn from live history — in the Monitoring
  tree (new Trend column), the Overview problem rows, and the status-chip lists. Backed by a new
  batched **`GET /api/spark?items=…`** endpoint (one `item.get` + up to two `history.get`,
  server-downsampled to ~24 points) so a whole host/list loads its sparks in a single request.

### Fixed
- **Collapsed sidebar**: the user menu (Account · Log out) no longer gets clipped by the
  sidebar — it now overflows correctly and sits above the content.

---

## [0.0.30] - 2026-08-12

**Fixes & Account polish.**

### Fixed
- **Sensor rows keep their left status stripe when acknowledged / paused / hidden**, recoloured
  to that state instead of vanishing. State colours are now unified on the design tokens
  (acknowledged = its own washed-red everywhere, matching the chip).
- **Unacknowledge (and acknowledge) from the status-chip lists**: the sensor census now carries
  each sensor's problem event ids, so the Acknowledged list's ⋮ menu offers **Unacknowledge**,
  and the Errors/Warnings lists offer **Acknowledge**.
- **Account page**: added the **Confirm new password** field (with a match check), and the cards
  now share one width so their edges line up.

---

## [0.0.29] - 2026-08-12

**New UI, stage 3b of 4 — full status summary & filtered lists.**

### Added
- **Six-chip status summary** in the top bar — **OK · Warnings · Errors · Acknowledged ·
  Paused · Hidden** — counted from a new cross-host sensor census, updating live (and instantly
  on any ack/pause/hide).
- **Clickable chips → filtered lists**: click any chip to see just those sensors across all
  sites (host · sensor · value · last check · actions), with deep-links to each sensor's host or
  chart and a per-row kebab (pause / hide / resume / show).
- Backend **`GET /api/sensors`** — a census of the curated "key" sensors, each tagged with one
  state (hidden > paused > error > warning > acknowledged > ok). New `item.get`-based
  `AllItems` client method.

### Notes
- The census covers curated key sensors (the same set as Monitoring's "Key sensors"); unsupported
  sensors are treated as unknown, not counted as OK. On very large deployments this census will
  move to server-side counts.

---

## [0.0.28] - 2026-08-12

**New UI, stage 3a of 4 — Overview redesign, deep-links & instant refresh.**

### Added
- **Overview redesigned** onto the design tokens: severity-striped problem rows, faded
  acknowledged state, inline acknowledge / unacknowledge.
- **Deep-links from the Overview into the tree**: click a problem's **host name** to jump to it
  in Monitoring; click the **problem** to open that sensor's chart. `/api/problems` now returns
  each problem's `item_ids` for the sensor link.

### Fixed
- **The top-bar status summary now updates instantly** after an acknowledge / pause / hide,
  instead of lagging up to 30s until the next poll. A lightweight refresh signal fans a mutation
  out to the summary and any open view.

---

## [0.0.27] - 2026-08-12

**New UI, stage 2 of 4 — the Monitoring tree & live Probes.**

### Added
- **Site → host → sensor tree** in Monitoring, grouped by **Zabbix host group** (site = host
  group; a host in several groups appears under each; hosts with none fall under "Ungrouped").
  Collapsible sites and hosts, a worst-state dot per site, and a panel-level Key sensors / All
  sensors toggle. Backend: `host.get` now returns each host's groups (`selectHostGroups`), and
  `hostView` carries `groups`.
- **Kebab (⋮) action menus** on hosts and sensors, replacing the inline Pause/Hide buttons —
  Pause / Hide (with the duration picker), Resume / Show, and **Acknowledge** on a sensor that
  has an unacknowledged problem. Inherited (host-controlled) pause/hide is cleared at the host.
- **Live Probes page**: shows the real Zabbix proxies (the per-site collectors) with online /
  offline status (seen within 5 min), last check-in, and mode — replacing the placeholder table.
  New `GET /api/proxies` (proxy.get) and client method. Token enrollment is still "coming soon".

### Changed
- The sensor table, charts (range tabs), and problem panel now use the design-token styling.
  Full-size charts keep uPlot, so they retain axis ticks and the hover legend.

---

## [0.0.26] - 2026-08-12

**New UI, stage 1 of 4 — foundation & shell.** Start of the port from the approved design
mockup. This release lands the visual foundation and app shell; Monitoring, Overview, and Users
keep working inside it and get their full redesign in the next stages.

### Added
- **Design-token system** (`theme.css`): a cohesive set of CSS variables for colour, surfaces,
  borders, and shadow, driving every component. Cerulean accent kept distinct from the status
  palette (ok/warn/err/paused/hidden/acknowledged).
- **Light & dark themes** with a toggle (in the sidebar). The choice persists and is applied
  before first paint to avoid a flash; a forced repaint on toggle keeps text readable.
- **Left-sidebar shell**: collapsible sidebar (Overview, Monitoring, Notifications, Probes,
  Users) plus a top bar with the page title and a live status summary (errors / warnings /
  acknowledged) that links to the Overview.
- **Account moved into the user chip** menu (Account settings · Log out) — no longer a nav tab.
- **Placeholders** for the upcoming **Notifications** and **Probes** sections, marked "Soon".

### Changed
- Shared surfaces (cards, inputs, buttons, dropdowns) now read from the design tokens, so the
  existing views are theme-aware. Full per-view redesigns follow in stages 2–4.

---

## [0.0.25] - 2026-08-12

### Added
- **"Suppressed until" labels**: paused, hidden, and acknowledged items now show when they'll
  clear (e.g. "paused · until Aug 12, 14:30", or "no expiry" when indefinite) in the host list,
  sensor rows, the host problem panel, and the Overview.
- **Auto-refresh for the Monitoring view and graphs**: the host list, the expanded sensor
  values/last-check/problems (30s), and open charts (60s) now update on their own — matching
  the Overview, which already refreshed. Background refreshes don't flash a loading state.

### Internal
- Suppression reads return the expiry (`ActiveSuppressionMap`); views carry `*_until` fields.

---

## [0.0.24] - 2026-08-12

### Changed
- **Custom duration now uses a date/time picker** instead of a "how many hours" prompt. Pick a
  calendar date and time and the state (ack/pause/hide) holds from now until that moment.

---

## [0.0.23] - 2026-08-12

**Durations, un-acknowledge, and faded acknowledged state.**

### Added
- **Every suppression takes a duration** — Acknowledge, Pause, and Hide now offer
  **1 hour / 8 hours / 1 day / 1 week / Indefinitely / Custom…** When the timer expires the
  state clears automatically: hide/ack lazily, and a background **sweeper** re-enables timed
  **Pause**s in Zabbix.
- **Un-acknowledge** brings a problem back into the error state. Acknowledge is now tracked in
  Argus (with expiry) and mirrored to Zabbix, so it can be undone and can expire.
- **Acknowledged problems fade** (PRTG-style): red/amber become a muted tone in the Overview,
  the host problem panel, and the sensor-row highlight — still visible, clearly de-emphasized.

### Changed
- Suppression storage generalized to a single `suppressions` table (kind hide/pause/ack, scope
  host/item/event, optional `until`). Endpoints `POST /api/.../{pause,hide}` and
  `POST /api/events/{id}/ack` accept `duration_seconds` (0 = indefinite); `DELETE
  /api/events/{id}/ack` un-acknowledges.

---

## [0.0.22] - 2026-08-12

**Overview dashboard.** The cross-host "what's wrong right now" view — now the default landing.

### Added
- **Overview** tab: a single list of active problems across all hosts, with an
  **Errors / Errors + Warnings** toggle. Errors-only hides acknowledged problems; both views
  exclude problems on hidden or paused hosts (and whose sensors are all hidden).
- Acknowledge directly from the list; rows sort worst-first, then unacknowledged, then newest,
  and the view auto-refreshes every 30s. Shows "✓ All clear" when there's nothing to report.
- Endpoint `GET /api/problems` (`problem.get` across all hosts, joined to host/items via
  `trigger.get`).

---

## [0.0.21] - 2026-08-12

### Fixed
- **Graphs now show a gap when data is missing** (e.g. a paused sensor) instead of drawing a
  straight line across the empty period. Where the interval between two points exceeds ~1.75x
  the typical sampling interval, the line breaks.

---

## [0.0.20] - 2026-08-12

### Changed
- **Sensors inherit their host's Pause/Hide state.** When a host is paused or hidden, all its
  sensors now show as paused/hidden too (marked "· host"), and their individual Pause/Hide
  toggles are disabled — you can't resume a single sensor while its whole host is paused. This
  matches how disabling a host in Zabbix actually stops all of its sensors collecting.

---

## [0.0.19] - 2026-08-12

**Two distinct suppression actions: Pause and Hide.** Hosts and sensors each get both.

### Added / Changed
- **Pause** (blue) = PRTG-style stop: disables the host/item **in Zabbix**, so collection
  actually stops (a gap in the graph while paused). Resuming re-enables it. Requires the API
  token to have **write** permission — a read-only token returns a clear error.
- **Hide** (grey) = Argus-side suppression: keeps collecting but mutes alerting/surfacing.
  Instant, reversible, no extra Zabbix permissions.
- Both available for **hosts and individual sensors** (Helpdesk + Admin). Paused/hidden rows
  are dimmed and marked, with a blue or grey status dot.
- Endpoints: `POST`/`DELETE /api/{hosts,items}/{id}/pause` (Zabbix enable/disable) and
  `.../hide` (Argus). A host/item disabled directly in Zabbix now shows as **Paused** in Argus.

### Internal
- The Argus suppression store/table was renamed from `pauses` to `hidden` to match the new
  naming; the old `pauses` rows (test data) are not migrated.

---

## [0.0.18] - 2026-08-12

### Changed
- **Pause/Resume buttons are aligned** to the right edge for both hosts and sensors, so they
  sit in a consistent vertical column and are easy to find. The per-sensor button moved from
  the sensor-name cell to the right of its "last check" time.

---

## [0.0.17] - 2026-08-12

### Added
- **Per-sensor pause** — each sensor row now has its own Pause/Resume control (Helpdesk +
  Admin); paused sensors are dimmed and marked "(paused)". Complements host-level pause.
- Endpoints: `POST`/`DELETE /api/items/{id}/pause`; items carry a `paused` flag.

### Notes
- Acknowledge was already per-problem (each active problem has its own Acknowledge button);
  Zabbix acknowledges at the event level, tied to the specific failing trigger/sensor.

---

## [0.0.16] - 2026-08-12

**States model — Acknowledge & Pause.** The first of the state controls the dashboards will
build on.

### Added
- **Acknowledge** a problem from the Active problems panel — uses Zabbix's native
  `event.acknowledge`, so it's reflected in Zabbix too; acked problems show a "✓ acknowledged"
  marker. Available to any signed-in user.
- **Pause / Resume** a host (Helpdesk + Admin) — an Argus-side flag: paused hosts go grey,
  hide their problem count, and are marked "(paused)". Zabbix keeps collecting; pausing just
  suppresses Argus-side surfacing (and, later, alerting). Instant and reversible.
- Endpoints: `POST /api/events/{id}/ack`, `POST`/`DELETE /api/hosts/{id}/pause`.
- Problems now carry their Zabbix `event_id` and `acknowledged` state; hosts carry `paused`.

### Notes
- Acknowledge runs with the **API token's** Zabbix permissions — the token's user needs rights
  to acknowledge events (a read-only token will get a permission error).
- Pause is host-level for now; per-sensor pause and the acknowledged/paused dashboards come
  with the error/warning list views.

---

## [0.0.15] - 2026-08-12

### Changed
- **Trimmed CPU-state noise** in the curated view: "Key sensors" now shows only the meaningful
  CPU utilization states (overall + user/system/iowait/idle/steal); the near-zero states
  (nice/interrupt/softirq/guest/…) remain available under "All sensors".

---

## [0.0.14] - 2026-08-12

### Changed
- **Network traffic scales** to Kbps/Mbps/Gbps (1000-based bits), and **uptime** renders as a
  duration (e.g. `1d 4h 14m`) instead of raw seconds — in the table and on the graphs.
- **Network sensors are de-duplicated**: the per-interface error/dropped/packet counters that
  share the `net.if.in`/`net.if.out` key are now labeled distinctly (e.g. "Traffic in dropped
  (enX0)"), so the byte-rate row is no longer repeated.

---

## [0.0.13] - 2026-08-12

### Changed
- **Byte values are now human-readable** — sizes/throughput auto-scale to KB/MB/GB/TB
  (1024-based) in both the sensor table and the graphs, instead of raw bytes.
- **CPU utilization rows are labeled by state** (idle/user/system/iowait/…) instead of a dozen
  identical "CPU utilization" rows sharing the `system.cpu.util` base key.

### Fixed
- **Graph legend no longer shows "--" when idle** — it now displays the latest point's time and
  value when the cursor isn't over the chart, and formats them (scaled units, readable time).

---

## [0.0.12] - 2026-08-11

**Curated sensor views.** The Monitoring view no longer dumps every raw template item.

### Added
- Items are classified by their Zabbix key into a small set of **categories** (Ping, CPU,
  Memory, Disk, Network, Temperature, Uptime) with friendly labels, and the default view shows
  only those — grouped by category — so you see the sensors that matter and only when the host
  actually reports them.
- A **Key sensors / All sensors** toggle per host; "All sensors" still shows the complete raw
  list (via `GET /api/hosts/{id}/items?all=1`).

### Notes
- Classification is key-pattern based (`internal/server/curate.go`), covering the common
  Zabbix agent2 Linux + ICMP keys; multi-instance sensors (per-mount disk, per-interface
  network) stay distinct. Unrecognized items fall under "All sensors" and the mapping is easy
  to extend as new device classes come online.

---

## [0.0.11] - 2026-08-11

**Per-sensor graphs.** Click a numeric sensor to chart its history.

### Added
- Numeric sensor rows are now **clickable** and expand into a time-series chart (uPlot) with
  **2h / 2d / 1M / 3M / 6M / 1Y** range tabs and drag-to-zoom.
- Short ranges (2h/2d) read raw **history**; long ranges read **trends** and draw the avg line
  with a shaded min/max band — matching Zabbix's 30-day history / 730-day trend retention.
- Endpoint `GET /api/items/{id}/history?range=…` (history.get / trend.get). Non-numeric
  sensors aren't clickable and the endpoint rejects them.

### Notes
- New frontend dependency: `uplot` (tiny, dependency-free charting).

### Not yet (upcoming slices)
- Curated per-device-class sensor views (hide the raw template noise), self-service email
  reset, login rate-limiting, and the probe enrollment/PKI backend.

---

## [0.0.10] - 2026-08-11

**Show what's actually wrong.** A host's problem count now has detail behind it.

### Added
- Expanding a host shows an **Active problems** panel listing each firing trigger (name +
  severity color), and the **sensor row(s)** a problem references are highlighted and
  left-barred in the trigger's severity color.
- Endpoint `GET /api/hosts/{id}/problems` (via `trigger.get` with `selectItems`).

### Notes
- Some triggers reference an item that isn't in the visible list (or a computed expression),
  so the problem still appears in the panel even when no specific row highlights.

---

## [0.0.9] - 2026-08-11

### Fixed
- **Sensor values are now rounded** (2 decimals for values ≥ 1, 4 for sub-1 so small timings
  don't collapse to zero), with trailing zeros stripped. Text values and checksums are left
  as-is. No more 16-digit readings.
- **Sensor table alignment**: switched to a fixed table layout so long values (e.g. a 64-char
  checksum) wrap within the Value column instead of overflowing and pushing the right border
  out of alignment. "Last check" no longer wraps.

---

## [0.0.8] - 2026-08-11

**Read path — hosts & sensors.** The first monitoring-facing feature: Argus now reads live
data from Zabbix and shows it. A flat host list with per-host sensor values; graphs land next.

### Added
- **Monitoring** tab: lists Zabbix hosts with a status dot (OK / Warning / Error, derived from
  active trigger severity) and a problem count. Click a host to expand its **sensors** with the
  latest value + units and how long ago each was checked.
- **Authenticated Zabbix client**: read calls use an API token via a Bearer header
  (Zabbix 7.0 style). New methods `host.get`, `item.get`, `trigger.get`.
- Endpoints: `GET /api/hosts`, `GET /api/hosts/{id}/items` (any signed-in user).
- New config: `ARGUS_ZABBIX_API_TOKEN` (create it in Zabbix under Users → API tokens).

### Notes
- Severity mapping: Zabbix info/unclassified → OK, warning → Warning, average/high/disaster →
  Error. Without a token the health probe still works but Monitoring returns a clear error.

### Not yet (upcoming slices)
- Per-sensor history/trend **graphs** with the 2h/2d/1M/3M/6M/1Y time tabs, self-service email
  reset, login rate-limiting, and the probe enrollment/PKI backend.

---

## [0.0.7] - 2026-08-11

### Changed
- Widened the app's content area (max width 820 → 1200px) so the Dashboard and the Users
  table use more of the screen on desktop/wide displays. Individual forms keep their own
  narrower max-widths for readability, and the layout still scales down on phones/tablets.

---

## [0.0.6] - 2026-08-11

**WebAuthn passkeys.** Passwordless, phishing-resistant sign-in that completes the
auth-hardening track. Uses discoverable (resident) credentials, so login needs no username —
the authenticator lists available passkeys. Works with Bitwarden, platform authenticators
(Windows Hello, Face ID/Touch ID), and hardware security keys.

### Added
- **Register passkeys** in Account: add multiple, name each, see when they were added and
  last used, and remove them individually.
- **Passwordless login**: a "Sign in with a passkey" button on the sign-in screen runs a
  discoverable-credential ceremony and starts a session on success.
- **Admin "Reset passkeys"** on the Users table (with a per-user passkey count) to recover a
  locked-out account.
- Endpoints: `GET /api/features`, `POST /api/login/passkey/{begin,finish}`,
  `GET /api/me/passkeys`, `POST /api/me/passkeys/register/{begin,finish}`,
  `DELETE /api/me/passkeys/{id}`, `POST /api/users/{id}/passkeys/reset`.
- New config: `ARGUS_RP_ID`, `ARGUS_RP_DISPLAY_NAME`, `ARGUS_RP_ORIGINS`.

### How it degrades
- Passkeys are **feature-gated**: they appear only when the server is configured
  (`ARGUS_RP_ID` + `ARGUS_RP_ORIGINS`) **and** the page is a secure context (HTTPS or
  localhost). Reaching Argus by private IP over plain HTTP simply hides the passkey UI and
  falls back to password + TOTP — WebAuthn RP IDs can't be a bare IP.

### Changed
- The admin user list now reports a passkey count per user.
- Additive SQLite migration adds a `webauthn_handle` column and the `passkeys` and
  `webauthn_sessions` tables (no manual steps).

### Not yet (upcoming slices)
- Self-service email password reset, login rate-limiting, and the probe enrollment/PKI backend.

---

## [0.0.5] - 2026-08-11

**MFA usability fixes** from first-run validation.

### Fixed
- **Copy recovery codes** now works over plain HTTP on a private IP. `navigator.clipboard`
  only exists in a secure context, so the button silently did nothing when Argus was reached
  by IP over HTTP; it now falls back to a `textarea` + `execCommand('copy')` and shows a
  brief "Copied!" confirmation.

### Changed
- The two-factor login step now includes a visually-hidden `autocomplete="username"` field
  (the account email) so password managers such as **Bitwarden** recognize it as a login form
  and offer to autofill the one-time code; the code input also carries a stable `id`.

---

## [0.0.4] - 2026-08-11

**Two-factor authentication (TOTP).** Optional, self-service, and standards-based so it
works with authenticator apps and password managers (Bitwarden, 1Password, Google/Microsoft
Authenticator) — both scanning the QR and pasting the setup key.

### Added
- **TOTP enrollment** in Account: shows a QR code and the base32 setup key, then confirms
  with a live 6-digit code before turning MFA on (RFC 6238 defaults: SHA1 / 6 digits / 30s).
- **Two-step login**: when a user has MFA on, a correct password yields a short-lived
  challenge (no session yet); the second step verifies the code and then signs in. The code
  field is marked `autocomplete="one-time-code"` so Bitwarden can autofill it.
- **One-time recovery codes** (10) generated when MFA is enabled, shown once with copy and
  download; usable in place of a code at login. Regenerate at any time (re-auth required).
- **Disable MFA** yourself (re-auth with your password), and an admin **"Reset 2FA"** action
  on the Users table to recover a locked-out account.
- Endpoints: `POST /api/login/totp`, `GET`/`POST /api/me/mfa`,
  `POST /api/me/mfa/{setup,enable,disable,recovery-codes}`, `POST /api/users/{id}/mfa/reset`.

### Changed
- `GET /api/me` and the user list now report `mfa_enabled`.
- Additive SQLite migration adds `totp_secret` / `totp_enabled` to existing databases and
  creates the `recovery_codes` and `mfa_challenges` tables (no manual steps).

### Not yet (upcoming slices)
- WebAuthn passkeys, self-service email password reset, login rate-limiting, and the probe
  enrollment/PKI backend.

---

## [0.0.3] - 2026-08-11

**User management.** Makes the role model usable — admins can now manage the other accounts.

### Added
- Admin-only **user management**: list users, create, change role, reset password, delete.
- **Role enforcement** on the user endpoints (helpdesk/viewer receive 403).
- **Change-my-password** for any signed-in user (verifies the current password).
- Guardrails: you can't delete your own account, can't delete or demote the **last admin**,
  passwords must be ≥ 8 characters, and a duplicate email returns a clear conflict.
- Endpoints: `GET`/`POST /api/users`, `PATCH`/`DELETE /api/users/{id}`,
  `POST /api/users/{id}/password`, `POST /api/me/password`.
- UI: top navigation (Dashboard / Users / Account), a Users table with an add-user form and
  per-row role/reset/delete controls, and an Account password-change form.

### Not yet (upcoming slices)
- TOTP MFA, WebAuthn passkeys, self-service email reset, and the probe enrollment/PKI backend.

---

## [0.0.2] - 2026-08-11

**Authentication.** Adds persistent users, password login with sessions, and the role model —
the first real feature on top of the skeleton.

### Added
- **Embedded SQLite** persistence in the data volume (`argus.db`) for users and sessions.
- **Password login** using argon2id hashing and server-side sessions (HttpOnly cookie; session
  ids are stored hashed, so a DB leak can't yield usable tokens).
- **Three roles** recorded on each user: `admin`, `helpdesk`, `viewer`.
- **First-run admin bootstrap** from `ARGUS_ADMIN_EMAIL` / `ARGUS_ADMIN_PASSWORD` (only when the
  database has no users yet).
- **Endpoints:** `POST /api/login`, `POST /api/logout`, `GET /api/me` (auth-protected).
- **UI:** a sign-in screen and an authenticated dashboard showing the current user, role, and a
  log-out button.

### Changed
- New configuration: `ARGUS_ADMIN_EMAIL`, `ARGUS_ADMIN_PASSWORD`, `ARGUS_COOKIE_SECURE`.
- Image now builds with Go 1.24 and resolves modules via `go mod tidy` at build time.

### Not yet (upcoming slices)
- Role enforcement on write actions, user-management UI, TOTP MFA, WebAuthn passkeys,
  self-service password reset, and the probe enrollment/PKI backend.

---

## [0.0.1] - 2026-08-11

**Initial release.** Establishes the two foundations of the system: a validated Zabbix-based
collection layer, and the first "walking skeleton" of **Argus** (the custom web app), packaged
for automated delivery to GitHub Container Registry.

### Added — Argus application (`argus/`)
- **Go backend** that serves an embedded React single-page app from a single binary/container.
- **Health endpoints:** `GET /healthz` (liveness) and `GET /api/health`, which reports backend
  status and whether the Zabbix JSON-RPC API is reachable (with its version).
- **Zabbix API client** (JSON-RPC) with an unauthenticated `apiinfo.version` connectivity check.
- **Environment-based configuration** (`ARGUS_LISTEN`, `ARGUS_ZABBIX_API_URL`, `ARGUS_DATA_DIR`)
  so the container is configured entirely via `docker run`.
- **React + Vite frontend** showing live backend and Zabbix status, with a dark theme.

### Added — delivery & CI
- **Multi-stage Dockerfile:** build frontend → build Go binary (frontend embedded) → minimal
  distroless runtime image.
- **GitHub Actions** workflow that builds and pushes `ghcr.io/<owner>/argus` (`:latest` on the
  default branch, `:vX.Y.Z` on tags) and auto-publishes a GitHub Release for each tag.

### Added — deployment kit (`deploy/`)
- `setup-core.sh` — installs Zabbix 7.0 + PostgreSQL 17 + TimescaleDB on Debian 13, including
  auto-pinning TimescaleDB to a Zabbix-supported 2.28.x.
- `gen-certs.sh` — one shared CA + unique per-site mutual-TLS client certs; supports adding a
  new site with a single command.
- `zabbix_server.conf.snippet` — mutual-TLS config, tuning, and the TimescaleDB compatibility flag.
- `run-probe.sh` and an **unRAID template** for deploying an active proxy (single container,
  mTLS, 7-day offline buffer).
- `PHASE0-CHECKLIST.md` (command-by-command runbook) and `README.md` (including a documented
  TimescaleDB version-regression fix).

### Added — documentation
- `docs/DESIGN.md` — the full system design (architecture, device classes, thresholds, state
  model, notifications, auth, roadmap).
- Top-level `README.md` describing the repository layout and status.

### Project milestone (infrastructure validated outside the repo)
- Zabbix 7.0 core live on Debian 13 (`10.0.0.10`) with PostgreSQL 17 + TimescaleDB 2.28.3.
- The **site1** active proxy is online over mutual TLS; live ICMP data flows through it; the
  7-day offline buffer was verified by backfill after a simulated outage.

### Notes
- TimescaleDB is pinned to 2.28.x because Zabbix 7.0 rejects 2.29+.
- This is a **walking skeleton**: authentication, the PKI/enrollment backend, dashboards,
  auto-discovery, and notifications are not yet implemented — they arrive in later releases.

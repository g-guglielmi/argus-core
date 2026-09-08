# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/), and the project
follows [Semantic Versioning](https://semver.org/) (`MAJOR.MINOR.PATCH`).

**Versioning policy:** increment the PATCH (`0.0.x`) for each change during development -
`0.0.1`, `0.0.2`, `0.0.3`, … - reserving **`1.0.0`** for the first production-ready release.
Each release is a git tag `vX.Y.Z` that triggers CI to build the versioned image and publish a
GitHub Release from the matching section below.

---

## [0.4.39] - 2026-09-09

**The UniFi Switch class — and a discovery trigger.** §C phase C1 is complete: switches join the
device catalog, polled from their UniFi controller with an API key — ports, PoE, traffic and all —
and a freshly added host gets its discovered per-instance sensors in seconds instead of an hour.

**Added:**
- **UniFi Switch device class.** One controller call per poll, addressed by the switch's MAC —
  works against a UniFi OS console (cloud gateway on :443) or a self-hosted console on a custom
  port via a per-host `{$UNIFI.URL}`. Sensors: controller state (offline trigger), CPU and memory
  utilization (threshold triggers), uptime, firmware, temperature (models that report one), uplink
  traffic, total **PoE power draw**, and port discovery — per-port link, negotiated speed, traffic
  in/out (controller-computed rates) and **PoE watts** on PoE-capable ports only. Ports carry the
  controller's names ("Port 1 · Office-AP") and sort naturally (Port 2 before Port 10).
- **Class-declared per-host inputs.** A device class can declare the macros it needs (controller
  URL, API key, MAC, site) and the Add-device form renders them generically — secrets are masked in
  the form and stored as Zabbix **secret macros**, write-only after creation. The seam every future
  API-source class reuses.
- **Discovery trigger.** After Add-device creates a host, its LLD rules fire automatically
  ("execute now", delayed until the proxy has synced the new config, with a retry) — and a
  **Discover now** action in the host menu runs the same trigger on demand for any host.

**Changed:**
- **Sensor categories order by host shape:** network gear (anything with switch ports) reads
  network-first, servers keep the classic compute-first order, and Power sorts before CPU in both.
  A GUI-editable order is on the roadmap.
- **Port rows read like network interfaces:** live ↓/↑ traffic in the value column (a down port
  reads "down"), a traffic sparkline — and every traffic-style group's sparkline (NICs, uplinks,
  ports) now shows the **sum of in + out**: total throughput at a glance.
- **Port charts start with the constant Speed/Link lines hidden** (their live values stay in the
  legend; one click reveals the line) — so the traffic axis ranges to the actual in/out rates
  instead of being pinned at the negotiated gigabits.

**Fixed:**
- Capability placeholders no longer occupy sensor rows: a switch without a temperature probe or
  without PoE reports a constant 0 for those — the template stops collecting them and the curated
  view hides the stale rows. The controller-state item feeds its offline trigger without taking up
  a row of its own.

## [0.4.38] - 2026-09-08

**Device classes — Argus provisions hosts now — plus a PRTG-grade monitoring view.** The first slice
of the device-class catalog: hand-authored Zabbix templates ship inside Argus and reconcile at
startup, **"+ Add device"** creates and binds a host end-to-end, and a **Generic Linux SNMP** class
arrives with full discovery. On the viewing side: channel groups with multi-series charts, a tree
that separates groups from hosts at a glance, and a deep chart/sparkline rework.

**Added:**
- **Device-class framework (C0).** Class templates live inside the binary and are versioned and
  re-imported at startup via `configuration.import` (needs a super-admin API token; soft-skips
  without one). A class registry plus a per-host `device_class` overlay; **"+ Add device"** (admin)
  in Sites & hosts creates the host in Zabbix, binds it to the site's proxy and attaches the class's
  templates, macros and interface. First classes: **Ping only** with an optional **HTTP/HTTPS
  endpoint** add-on (custom port).
- **Generic Linux SNMP class (C1).** Core metrics (CPU, memory, uptime) plus discovery: filesystem
  LLD → per-mount Disk total / used / used % (tmpfs filtered; OIDs matched in numeric *and*
  MIB-symbolic form), interface LLD → per-NIC traffic in/out. Thresholds ride as user macros.
  Add-device **inherits the proxy's SNMP defaults** — credentials are asked for only to override.
- **PRTG-style channel groups.** Per-instance sensors (a disk mount, a NIC, the ICMP trio) collapse
  into one group row; expanding it opens a single chart overlaying every channel — mixed units on two
  y-axes, click-to-toggle legend — and pause/hide/priority act on the whole instance. ICMP reads
  response time as the main field, loss beside it, and reachability inverted into a red **downtime
  band**.
- **Tree readability.** Compact accent-tinted group bands vs. taller host rows with device-type
  icons, indent guides, and a per-host ICMP latency + sparkline; sparkline, latency and menu align in
  shared columns at any nesting depth. Groups and hosts start collapsed and remember their open state
  for the login session.
- **Charts.** Min/max envelope band on trend ranges (1M+); the ping chart's % scale is pinned 0-100
  and owns a fixed 0/20/…/100 grid, so "100 % loss" and "down" peak at the same height; the second
  axis labels ride the shared gridlines; axis gutters size themselves to their longest label so
  nothing clips; ticks are unit-aware for every unit; the primary channel is shaded like the
  single-metric charts; drag-zoom pauses that chart's auto-refresh.

**Changed:**
- The host sensor table and the overview/status lists now share the tree's column order (… Trend,
  Priority, Last check) and stable proportions, every sparkline in the app is one size, single-scale
  charts label their axis on the right, and the Loss channel is amber-gold — clearly apart from the
  red downtime.

**Fixed:**
- **The stray blue dot** in every chart's top-left corner: uPlot parks its drag-zoom selection box
  collapsed at 0×0 there, and the dark theme's 1px accent border on it painted a 2-px speck. The
  selection edge is now an inset shadow — identical while dragging, invisible when idle.
- The 30-second refresh could steal the open chart, snapping the view back to the sensor originally
  drilled into from the Overview.
- Flat sparklines rendered dim and blurry (a stroke centered on a pixel boundary splits across two
  rows — half-pixel centers now); axis labels could clip (`7d 4h 46m`, `953.67 MB`); a plain % axis
  lost its unit and followed the browser locale; the overview list's column widths never applied
  (Chrome ignores `calc(% - px)` on fixed-layout table columns).
- Zabbix 7.0 rejects non-v4 UUIDs in template imports; proxy/load-balancer names no longer
  icon-guess as a NAS.

## [0.4.37] - 2026-09-06

**Human-readable time units.** Second-based readings now auto-scale to a sensible magnitude.

**Fixed:**
- A seconds metric like ICMP response time showed as `0.0138 s`, and its chart's y-axis labels all
  collapsed to `0.014`/`0.015`. Seconds now scale to `ms` / `µs` / `ns` (staying `s` at ≥ 1 s) in the
  sensor value, the chart axis, and the chart legend, with the duplicate `(s)` legend suffix dropped.
- Alert messages scale to match: the "Value:" line reads e.g. `13.8 ms` instead of `0.0138 s` (bytes,
  bits and uptime format the same way the UI does).

## [0.4.36] - 2026-09-06

**Monitoring tree** fixes for hosts that belong directly to a parent group (alongside its subgroups).

**Fixed:**
- A host that belongs directly to a group (e.g. a host in `myng` while `myng` also has subgroups) was
  drawn indented under — and after — its sibling subgroups, so it looked like a member of one of them.
  It now sits at the group's own level, above the subgroups.
- Row dividers were inconsistent once hosts and subgroups interleaved: some boundaries drew a double
  line, others none. Every tree row now draws exactly one divider, in any order.

**Added:**
- **Reorder hosts and subgroups together.** In the tree's Reorder mode a group's direct hosts and its
  subgroups are now one list, so a host can be moved above or below the subgroups (saved per parent).

## [0.4.35] - 2026-09-06

**Per-user notifications.** Everyone can now get alerts on their *own* Telegram or Discord, and email
can fan out to every registered user — additively, without changing the existing shared channels.

**Added:**
- **Personal notifications (Account tab).** Any signed-in user (any role) can register their own
  Telegram (their own @BotFather bot token + chat ID) or Discord (webhook URL) and receive alerts there,
  scoped by site and severity like a global channel — with the same per-channel delivery-health line and
  a **Send test** button. Self-service and private: everything is under `/api/me/notify/*`, a user only
  ever sees their own channels (`user_notify_channels`, config encrypted at rest). Groundwork for the
  future mobile apps, where a phone is just another personal channel.
- **Email → registered users.** An email channel can now deliver to **each active user's registered
  email** instead of a fixed address — a "Send to" choice on the channel (admin-controlled), alongside
  the existing fixed-address mode. Each recipient gets a private, individual message.
- **Multi-site channels.** Any channel — global or personal — can target **several sites at once**
  (a multi-select of host-groups) rather than only one site or all. The picker is hierarchical:
  selecting a probe's root group (e.g. `mybz`) covers all its subgroups (`mybz/Network`, …), and a
  channel fires when any of its sites matches — or is an ancestor of — one of the host's groups.

**Changed:**
- The notifier fires a problem as soon as a **global or personal** channel matches its site + severity
  (previously a problem stayed pending until a *global* channel existed), so personal-only setups alert.

## [0.4.34] - 2026-09-05

A whole-app **UI and notifications polish pass**, driven by a review of every screen at desktop and
phone widths: a handful of real defects, a denser and more legible phone layout, proper feedback
primitives (toasts, skeletons, empty states), a reworked Notifications page with per-channel delivery
health, one consistent set of controls, and clearer alert messages on every channel. Companion:
`argus-probe` **probe-vm/v0.3.2** restyles the VM's first-boot setup page to match.

**Fixed:**
- **Overview / status lists:** the per-row ⋯ button spilled outside the card once the panel was narrower
  than ~1100px (the fixed-layout table gave the actions column a 4% share). It is now a fixed 48px
  column; narrow desktops also trim cell padding and show the compact `★4` priority.
- **Phone drill-down:** the expanded host's sensor table overflowed its card (Last check and the ⋯
  clipped, values wrapping mid-number). Compact priority, nowrap values, tighter cells, and the tree
  indent no longer eats the card width.
- **Future times read "in 22h"** (pending-enrollment expiries, the wizard's token expiry) instead of the
  misleading "0s ago".
- **Sensor chart colours** come from the theme tokens (the hard-coded white-alpha grid was invisible on
  the light theme) and the series takes the sensor's state colour, matching its sparkline; the chart
  repaints when the theme flips.
- **Phone touch targets:** ⋯ buttons 36px, buttons/chips/segments/nav rows enlarged, 16px inputs so
  iOS stops zooming the page on focus. Probe cards align every value - text, ✓ states, pill boxes and
  the ⋯ - on one right edge.

**Changed:**
- **Phone header** drops the subtitle (it repeated the panel header underneath) and keeps the tools
  beside the panel title; **Overview cards** are a compact 4-row grid - host + ⋯, sensor + reason,
  sparkline beside the value, "last check 1m ago" beside the priority - about 30% shorter. The smallest
  phone text moved up to 11px labels / 12.5px sub-lines.
- **Notifications page:** each channel card has an enable switch, a single **Send test** button,
  Edit/Delete in a ⋯, and a **delivery-health line** - "Last sent 2h ago · 42 delivered", "Last delivery
  failed 5m ago · HTTP 400 …", or "Nothing sent yet". Every send attempt (alert, recovery, test) is now
  recorded on the channel, so a broken webhook or SMTP password is visible in the UI, not only in the
  core log. (New `notify_channels` columns, added by the idempotent migration.)
- **Alert messages carry the Zabbix severity** - subjects/titles read `[HIGH]`, `[DISASTER]`,
  `[WARNING]` (what the UI shows) instead of the coarse `[ERROR]`/`[WARNING]` state; recoveries stay
  `[RESOLVED]`. Discord gains a Severity field; the plain-text and email bodies name it too.
- **Email** is a single HTML card with a plain-text alternative: severity-coloured header, no duplicated
  trigger name, buttons that wrap on narrow screens, a footer linking back to the Notifications page,
  and a dark-mode override for clients that honour `prefers-color-scheme`.
- **Telegram** is a compact card (title, site · host, reading, since) with **Open in Argus** and
  **Acknowledge** as inline-keyboard buttons; non-http links fall back to inline anchors.
- **One set of controls:** the channel editor, user roles and the Add-probe wizard use the shared
  Select / Switch / input skin (three select styles, bare checkboxes and an inline style object are
  gone); native checkboxes take the accent colour. The Monitoring toolbar on phones folds Reorder / Show
  hidden / Key-All sensors into a ⋯. Read-only priority stars are slightly muted.

**Added:**
- **Toasts** for action outcomes (Saved, Test sent, Could not delete …) that auto-dismiss, replacing
  persistent inline text lines; contextual form errors stay inline.
- **Loading skeletons** on every list while it fetches, and the Overview no longer flashes "All clear"
  before the first data arrives.
- **Designed empty states** - a green all-clear on the Overview and Triggers, muted "nothing here"
  blocks with an action where one makes sense (Probes, Notifications).
- **Sidebar version tag** (amber "update available" when Settings has an update) and tooltips on the
  collapsed rail.

## [0.4.33] - 2026-09-05

A readability pass over the **Probes** and **Users** tables (both the same labelled-card pattern —
a data table on desktop, stacked cards on mobile). No functional changes; UI only.

**Changed:**
- **Probes table restructured for scannability.** Dropped the `Argus-` prefix from the version headers
  (`Proxy Version` / `Updater Version` / `VM OS Version`). The all-good states — `up to date` and
  `patched` — are now a quiet green ✓ + muted text instead of a full pill, so colored pills are reserved
  for things that need attention (drift → `Update`, `reboot`, `offline`) and the eye lands on the probes
  that need action.
- **Grid dividers** on both tables: faint vertical rules between columns and stronger horizontal rules
  between rows on desktop; on mobile, faint dividers between each field and a stronger boundary between
  cards, so rows/cards read apart from the fainter within-row/field dividers.
- **Mobile cards tidied:** values stack (value over status pill), every value lines up down the right
  edge (bare text inset to match pill text), labels vertically centered, roomier/centered card padding,
  and the kebab tucked under the last field.
- The per-row **⋯ menu now flips upward** when there isn't room below the button, so the last row's menu
  stays on-screen (both the Probes and Users menus).
- Applied the **same treatment to the Users table** for consistency.

## [0.4.32] - 2026-09-05

**OS patching & lifecycle** (roadmap §A, DESIGN §14c): keep the Debian OS under the core and probe VMs
patched without accumulating CVEs, while leaving the reboot policy appropriate to each role. The OS
patches itself locally — Argus reports status and schedules the core's reboot, but never runs `apt`
remotely (there's no clean rollback; hypervisor snapshots are the safety net).

**Added:**
- **Automatic security patching on both roles.** The probe golden image (`argus-probe`
  `probe-vm/v0.3.1`) and the core (`deploy/core/setup-core.sh`) install `unattended-upgrades`
  (**security suite only**, so the core's TimescaleDB 2.28 hold is safe) + `needrestart` (auto-restart
  services after a libc/openssl bump, so most updates need no reboot).
- **Role-appropriate reboots.** **Probe VMs** (cattle) auto-reboot in a weekly ~03:00 window — they
  buffer 7 days offline, so a ~60 s reboot is invisible. The **core** (a pet hosting the DB + Zabbix)
  never reboots unattended by default: a new **Settings → OS updates** mask picks a day + time, or
  "notify only" (the default). A host-side watcher honours the window locally.
- **Fleet patch visibility.** Probe VMs report their pending **security-update count** + **reboot-
  required** flag hourly (`POST /api/probes/os-status`, probe-token auth); the core reports its own via
  a host timer into the shared self-update dir. The **Probes** page gains an **OS** column and a
  "N need a reboot" header rollup; **Settings → OS updates** shows the core's status.
- Endpoints: `GET /api/os/status` (core status + reboot window), `PUT /api/os/reboot-window` (admin),
  `POST /api/probes/os-status` (probe report). New `probe_agents` columns `sec_updates` /
  `reboot_required` / `os_reported_at` (additive migration).

**Changed:**
- **Probes table reworked.** Adding the OS column pushed it to 11 columns; regrouped into a clearer
  layout — **Probe** (name + `mode · enrolled` subline), **Health** (online/offline + last check-in),
  **Argus-Proxy Version** (Zabbix proxy version + up-to-date/update), **Argus-Updater Version** (the
  argus-updater sidecar's version + drift + an Update button when behind), **Argus-VM OS Version** (the
  OS name + patch chip), and a per-row **⋯ menu** (SNMP defaults, Console, Delete). The menu is portaled
  to `<body>` so the table's scroll container can't clip it.
- **Updater drift.** Argus now resolves the newest `argus-updater` version from GHCR (like it does for
  the probe image) so each probe's sidecar shows up-to-date / behind, not just its version.
- **Probes report their OS name** (`os` in `POST /api/probes/os-status`, new `os_version` column) so the
  VM's Debian version shows alongside the patch status.
- The proxy SNMP-default band is a centered card with balanced fields; fixed its prose spilling past the
  card — the probes table forces `white-space: nowrap` on cells and the inline panel inherited it, so its
  note couldn't wrap (reset to `normal`; also guarded the field grid against `min-width: auto` overflow).

**Deploy:**
- `setup-core.sh` gains an OS-patching step: `unattended-upgrades` (auto-reboot **off**), a host
  reporter writing `os-status.json`, and a reboot watcher reading `reboot-window.json` — both under
  `ARGUS_STATE_DIR` (the host path you map as the core container's `ARGUS_UPDATE_DIR`).

## [0.4.31] - 2026-09-03

Finish the **self-configuring probe VM** (roadmap §A): full-fleet OVA delivery, a downloadable seed
ISO, **break-glass** console access, a configurable keyboard layout, and dropping cloud-init entirely.
Completes DESIGN §14a's delivery-vs-enrollment matrix and the break-glass item.

**Added:**
- **Downloadable seed ISO.** Add probe → **VM** gains a **Download seed ISO** button for hypervisors
  with no cloud-init field: `POST /api/probes/seed-iso` (admin) streams a small ISO the probe VM's
  first-boot service reads to self-enroll. It's an Argus-owned image (volume label `ARGUSSEED`, one
  8.3-safe `ARGUS.ENV`) - deliberately **not** a cloud-init NoCloud seed (which would need
  Joliet/Rock-Ridge to keep the `user-data`/`meta-data` names), so it sidesteps cloud-init's NoCloud
  datasource detection (fiddly on XCP-NG). The token is never persisted - the ISO is built on demand.
- **OVA delivery** (in `argus-probe`'s golden-image CI): the VM ships as an **OVA** (stream-optimized
  VMDK + OVF) for VMware/Nutanix/VirtualBox and Xen Orchestra (*Import → OVA*), alongside qcow2 and VHD.
  The image base moved to Debian 13 **`generic`** (full drivers) so it boots on non-virtio hypervisors
  and can read the seed CD.
- **Break-glass console access.** The probe VM generates a per-VM `argus` sudo user with a random
  password on first boot and reports it to Argus (`POST /api/probes/break-glass`, authenticated by the
  probe check-in token); Argus stores it **encrypted at rest** and reveals it to admins on the Probes
  page (a **Console** button;  `GET /api/probes/{name}/break-glass`). For hypervisor-console or
  VPN-SSH access when something's wrong.
- **Configurable console keyboard layout** for the probe VM - a picker in Add probe → VM and on the
  first-boot page; applied to `/etc/vconsole.conf` on first boot (default `us`).
- **Static networking for no-DHCP sites** - an optional Static IP section in Add probe → VM (address, a
  **subnet-mask dropdown** showing both the /prefix and dotted mask, gateway, and **two DNS** fields for
  redundancy, all validated) baked into the seed ISO (`ARGUS_IP` etc.); the first-boot service writes a
  static `systemd-networkd` file before enrollment, so the VM enrolls on a fixed address with no
  interaction. Seed-only (the first-boot page needs an IP to be reachable); a stuck VM is recoverable by
  re-attaching a corrected seed.
- **The probe VM takes the hostname `argus-probe-<site>`** (e.g. `argus-probe-office`) on enrollment — the
  VM is the probe appliance; the container it runs is the Zabbix proxy (`proxy-<site>`). The break-glass
  `argus` user is also added to the `docker` group so it can run `docker` without sudo.
- **Terminology tidy-up:** the operator-facing copy consistently calls the deployable a **probe**;
  **proxy** is reserved for the Zabbix entity (the `proxy-<site>` name, the "Monitored by" assignment).

**Changed:**
- **Reworked "Add probe" into a guided modal wizard** - name → deploy method (+ only that method's
  settings) → deploy instructions → an optional live "waiting for enrolment ✓". Replaces the crowded
  inline panel that showed every method and setting at once. Back navigation between steps doesn't
  waste a token (it's minted once, on leaving the method step, and only re-minted if the name changes);
  the wait step polls the token status and is purely a visual aid (closing it changes nothing).
- **Dropped cloud-init from the probe VM.** The golden image purges cloud-init after the build; the
  appliance self-configures via systemd-networkd + the first-boot service. The wizard's VM option is now
  seed ISO + first-boot page (the cloud-init paste path is retired). VM defaults bumped to 30 GB disk /
  4 GB RAM.

**Fixed:**
- **A failed update check no longer masquerades as "up to date."** When the core couldn't reach GHCR,
  the check bailed out and left the previous verdict in place, so "Check for updates" showed a
  falsely-reassuring "You're on the latest available build" — indistinguishable from an actual up-to-date
  result (as a GHCR outage made obvious). `/api/version` now reports the last successful-check time and
  whether the most recent attempt reached the registry; the About panel shows a **check failed** tag +
  "Couldn't reach the registry — showing the result from N ago" instead of the green "latest" verdict.
- **Core "Check for updates" missed newer `:testing` builds.** Channel resolution let the argus-updater
  sidecar's reported image tag override the version stamp, so if the sidecar reported anything but
  `testing` a development build (which can only come from `:testing`) was treated as the `latest`
  channel and never offered newer `:testing` images - the in-app check found nothing while Dockhand
  correctly saw the newer digest. A dev-stamped build is now authoritatively the testing channel
  regardless of the reported tag (the tag still disambiguates a clean build). Pure `channelFor` helper,
  unit-tested.

**Dependencies:** adds `github.com/kdomanski/iso9660` (pure-Go, builds the seed image).

---

## [0.4.30] - 2026-09-02

Unify self-update on one model across the core and the whole probe fleet: **two containers**
everywhere - the main container (a pure reporter, never holding the Docker socket) plus the shared
**argus-updater** sidecar that recreates it via the Docker Engine API and rolls back on failure.

**Added:**
- **One updater for everything.** The core self-updater and every probe now use the same
  `ghcr.io/g-guglielmi/argus-updater` image (its own version line), sharing one recreate engine, with
  modes `core` (file-channel), `probe-watch` (socket-holding probe sidecar, no compose), and
  `probe-recreate` (the one-shot self-update primitive). The probe image dropped `docker-cli` and is a
  pure reporter; the socket is only ever on the sidecar.
- **Update the updater.** The sidecar can update itself (via an ephemeral `probe-recreate` copy). A
  new **Updater** column on the Probes page (per-probe sidecar version + an admin Update button) and
  an **Updater sidecar** section in Settings → About (version + Update sidecar) drive it. New
  `POST /api/probes/{name}/updater-update` and `POST /api/update/updater`.
- **Two-container deploys from the wizard.** The Add-probe wizard's Docker-run and Compose tabs now
  emit both containers (proxy + `probe-watch` sidecar); the probe VM installs them as two systemd
  units (`argus-probe` + `argus-updater`); unRAID keeps its native auto-update.

**Changed:**
- **Retired the socket-on-proxy self-update path** (`ARGUS_PROBE_SELFUPDATE`) and the compose-specific
  `probe-poll` updater mode in favour of the single sidecar model.
- Two-reporter check-in: a socket-less proxy reports its version but omits self-update capability
  while the sidecar advertises capability but omits a version; the fields are sticky (an omitted field
  keeps the stored value) and one-shots are handed only to a capability-advertising caller, so the two
  never clobber each other or race. `RecordProbeCheckin` takes `selfupdate *bool`.
- Settings → About standardized: parallel **Core** / **Updater sidecar** rows with prominent labels
  and real (bordered) buttons.

## [0.4.29] - 2026-09-01

**Added:**

- **Delete a proxy from the Probes page.** Admins can remove a decommissioned probe: it's deleted from
  Zabbix (`proxy.delete`) and its Argus-side records (enrollment tokens, check-in/version state, SNMP
  default) are cleaned up. Zabbix's "still monitored by hosts" error is surfaced so you can reassign
  them first; the `proxy-<site>` host group is left in place.
- **"Clean up" on the Probes page** prunes Argus records orphaned by proxies deleted directly in the
  Zabbix UI (out of band), keeping pending enrollment tokens whose proxy doesn't exist yet.

**Changed:**

- **The update-available state on the Probes page is now highlighted in amber** (the button + the
  "→ version" chip), so an outdated probe stands out at a glance instead of rendering muted like the
  other states.
- **Project split into three repositories** — `argus-core` (this app), `argus-probe` (the probe Docker
  image + self-configuring golden VM), and `argus-updater` (the self-update sidecar). Image names are
  unchanged, so deployments are unaffected; the Add-probe **Compose** command now fetches its compose
  file from the argus-probe repo.

---

## [0.4.28] - 2026-09-01

**Improved:**

- **Alert trend graphs now scale the Y axis by the sensor's units**, matching the app. Byte counters
  read as KB/MB/GB (1024-based), bit rates as Kbps/Mbps/Gbps, and uptime as a duration (e.g. `817.1d`,
  `4.1h`, `45s`) — instead of raw or scientific-notation values like `7.06e+05`. Unitless values keep a
  compact SI form (`70.6M`).

**Added:**

- **Add probe → "VM (cloud-init)"** deploy output: emits cloud-init user-data for the self-configuring
  probe VM, so a VM enrolls with zero touch (paste it into the hypervisor's cloud-init field). The
  golden image itself is built and released separately under `probe-vm/vX.Y.Z`.

---

## [0.4.27] - 2026-09-01

**Added:**

- **Global quick-switcher (Ctrl-K).** A new search box in the top bar — and the **Ctrl/Cmd-K**
  shortcut from anywhere — opens a palette that searches **hosts** (by name or IP), **sensors** (by
  name), and **host groups** (by name). Arrow keys move, Enter jumps: a host opens in the tree, a
  sensor opens its chart, a group focuses the tree on it. Matches rank prefix and word-boundary hits
  above mid-word ones.
- **Per-channel notification severity floor.** Each notification channel can now set its own minimum
  severity — **Warning & up**, **Average & up**, **High & up**, or **Disaster only** — in the channel
  editor (next to Site), with the floor shown on the channel card when it's above the default. A
  problem below a channel's floor no longer routes to it, and its recovery notice follows the same
  rule. Defaults to Warning, matching the previous behavior, so existing channels are unchanged.
- **Add-probe self-update toggle.** The "Add probe" deploy panel gained an **Enable self-update**
  checkbox that adds the Docker-socket mount and `ARGUS_PROBE_SELFUPDATE=1` to the generated
  Docker-run command (and the equivalent socket volume + variable to the unRAID XML), so a
  socket-enabled probe deploys straight from the wizard instead of hand-editing the command. The
  Compose format already bundles the updater sidecar, so there it's always on.
- **Labeled axes on alert trend graphs.** The 2-hour trend PNG in every problem and recovery alert now
  carries axis labels — min/mid/max value gridlines on the Y axis and relative time (2h ago → now) on
  the X axis — for at-a-glance scale without opening the app.

---

## [0.4.26] - 2026-08-31

**Fixes:**

- **Group drill-down is now reflected in the URL.** Focusing a group in the monitoring tree (clicking a
  group name or a breadcrumb) now updates the address bar to `?view=monitoring&group=<path>`, so a
  reload, bookmark, shared link, or Back/Forward restores that group view instead of dropping back to
  the tree root. Host and sensor focus were already URL-persisted; group focus was the gap.
- **Browser Back/Forward now step through the tree's drill levels.** Explicit drills (clicking a
  group/host/sensor name or a breadcrumb) push history entries, so Back walks back up root ← group ←
  host ← sensor and Forward re-drills — instead of Back jumping straight out of the Monitoring tab.
  Inline accordion toggles (expanding a host card or sensor row) still don't add history entries.

---

## [0.4.25] - 2026-08-31

**Changes:**

- **Advanced mode declutters the monitoring toolbar.** The Sites & hosts toolbar had grown crowded, so
  the two rarely-used controls — the **All sensors** view toggle and **hidden-group management** (Show
  hidden / per-group hide) — now appear only when **Advanced mode** is on. It's a per-user preference
  (like the landing page), toggled from the admin-only **Settings → Interface** tab, so only an admin
  can enable it and only for their own view — no one else is affected. Off by default; with it off the
  toolbar is just **+ New group** and **Reorder**.
- **"+ New group" is now the primary (blue) button**, matching "+ Add channel" and "+ Add probe" on the
  other tabs.
- **Sidebar icon rework and polish.** Reworked two nav icons to match their tab: **Monitoring** is now a
  hierarchy/tree (sites → hosts → sensors) instead of stacked bars, and **Triggers** is a pulse/threshold
  spike instead of a generic list. Also fixed the Settings gear, whose hand-rounded path left a misshapen
  tooth in the lower-right, by swapping in the clean canonical gear; and vertically centered the Overview
  gauge (it was top-weighted, which made the space below it in the collapsed rail look like an uneven gap).
- **Group separators in the collapsed sidebar.** When the rail is collapsed to icons, the
  Watch/Configure/Admin section labels can't show their text, so they now render as thin divider lines —
  keeping the same grouping the expanded sidebar shows.
- **Hiding a group is now admin-only and asks for confirmation.** Unhiding a group is only reachable in
  advanced mode (an admin-only preference), so the **Hide from tree** / **Show in tree** actions now
  appear only there too — closing a gap where a helpdesk user could hide a group and then have no way to
  bring it back. Hiding also now shows an in-app confirm (it can tuck a group out of everyone's view),
  and the write endpoint (`PUT /api/tree/hidden`) is restricted to admins.

---

## [0.4.24] - 2026-08-31

**Features:**

- **Manually reorder the monitoring tree.** The tree defaulted to alphabetical; a **Reorder** button in
  the Sites & hosts toolbar now reveals inline up/down arrows on every group and host so their order can
  be set by hand (e.g. put Network above Infrastructure). The order is stored in Argus (Zabbix has no
  group/host ordering) and applies per sibling set; groups or hosts added later fall to the end,
  alphabetically, until moved. Admin/helpdesk only. New endpoints `GET`/`PUT /api/tree/order`.
- **Hide groups from the monitoring tree.** A group's kebab now has **Hide from tree** (and **Show in
  tree**), tucking a group and its subtree out of view without touching Zabbix - handy for the stock
  Zabbix groups (Applications, Databases, …) that can't be deleted because a host prototype references
  them. Hidden groups are hidden for everyone; admin/helpdesk can reveal and manage them with the
  **Show hidden** toolbar toggle. Argus-local, new endpoints `GET`/`PUT /api/tree/hidden`.

---

## [0.4.23] - 2026-08-31

**Fixes:**

- **Self-update no longer loops on a just-released build.** Cutting a release rebuilds the release
  commit twice (the main-push build and the tag build), producing two images with the same code but
  different digests; whichever `:testing` push landed last could differ from the `vX.Y.Z` tag, and the
  digest-only update check then offered a "newer `:testing`" update to the box's own commit forever.
  The check is now **commit-aware**: if `:testing` and the running image share a git revision
  (`org.opencontainers.image.revision`), it's the same code and no update is offered. CI also
  **serializes image builds** (a `concurrency` group) so the tag build is the last writer of the
  rolling tags and leaves `:testing` cleanly stamped `vX.Y.Z`.

---

## [0.4.22] - 2026-08-31

**Host settings editor (phase 2b) — manage a host's identity + connection from Argus:**

- A **"Settings…"** action on each host (admin/helpdesk) edits the **visible + technical name**,
  **Monitored by** (Server / Proxy with a proxy picker), and the host's **Agent and SNMP interfaces**
  — add, edit and remove, with connect-via IP/DNS, port, and SNMP version/community (v1/v2c/v3).
  One Save reconciles the whole desired state.
- **Removing an interface no longer strands its checks.** If items still use the interface, Argus
  moves them to a surviving interface first (so you can swap an Agent interface for SNMP without
  losing ICMP ping). A check that needs an interface of the same type still surfaces a clear refusal.
- **Group moves can keep the collector in sync.** Moving a host into a site group that matches a proxy
  (`proxy-<site>` ↔ group `<site>`) offers to switch its **Monitored by** to that proxy too.

**PRTG-style SNMP inheritance:**

- Each proxy holds a **default** SNMP credential set — edit it in the Probes tab (the proxy's
  **Defaults** button). Community and v3 passphrases are encrypted at rest.
- A host's SNMP interface can **Inherit** its proxy's default or **Override** per host. New SNMP
  interfaces default to Inherit when a default exists.
- Changing a proxy default **propagates live** to every inheriting host, and setting a default
  **offers to adopt** existing per-host overrides. Both report how many hosts were updated.

**Fixes:**

- Clicking the **Monitoring** tab now returns the tree to the root even when it's already the active
  view and you're drilled into a group/host/sensor.

## [0.4.21] - 2026-08-31

**Manage tree groups from Argus (first Zabbix *config* writes):**

- **Create, rename, delete groups and move hosts between them** from the Monitoring tree
  (admin/helpdesk). New group CRUD (`/api/groups`) + per-host membership (`/api/hosts/{id}/groups`),
  backed by new Zabbix host-group client methods. Deleting a non-empty group is refused; a host always
  keeps at least one group. Requires the Zabbix API token to have **super-admin** rights.
- **Nested groups render as a real hierarchy.** Zabbix nests host groups by name with `/`
  (`mybz/Network`); the tree now draws that as `mybz` › `Network`, with rolled-up host counts and
  status per node. There are **no virtual parents** - only real groups are nodes; a group with no real
  ancestor sits at the top level under its full name.
- **A host group per probe.** Enrolling a probe now also creates its site's top-level group (named
  after the site). Probes enrolled before this are seeded once, at startup.

**Everything in the web UI - no browser popups:**

- Replaced every native `window.confirm` / `window.prompt` / `window.alert` with an in-app,
  theme-aware dialog (`useConfirm` / `usePrompt` / `useAlert`): version/channel switch, delete channel,
  revoke probe token, user reset-password / reset-MFA / reset-passkeys / disable / delete, MFA disable +
  recovery-code regen, passkey add/remove, and probe update/check-in errors. Group create/rename/delete
  use inline editor bands.

**Fixes:**

- **Self-update on a clean-tag testing box.** A box running a clean `vX.Y.Z` image that tracks
  `:testing` showed the "Update" button but the click was refused ("no newer release available") because
  the action gated on `status=="development"` while the button used a broader rule. The button and the
  click now share one predicate, so the in-place testing update applies.

## [0.4.20] - 2026-08-30

**Monitoring drill-down (PRTG-style):**

- **Click through the tree.** The group, host and sensor names in the Monitoring tree are now links
  that narrow the view to just that level, with a breadcrumb (`Sites & hosts / Group / Host / Sensor`)
  to step back up. The chevrons still expand inline for a quick peek - name = drill in, chevron = peek.
- **Deep-links land focused.** Opening a host or sensor from the Overview, a status-chip list or the
  Triggers tab now drops you straight into that focused view; the address bar, the Back button and a
  reload all restore it.
- **Key sensors / All sensors** toggle is hidden at the single-sensor level (where it does nothing) and
  kept at the group and host levels, so you can still switch modes while focused on one host.

## [0.4.19] - 2026-08-30

**Sensor priority (PRTG-style):**

- **Per-sensor priority (1-5).** Set a priority on each sensor from the Monitoring tree (a star column,
  editable by admin/helpdesk). It's stored in Argus and never touches Zabbix. Higher priority sorts
  first in the Overview and the status-chip lists, and is shown read-only in those lists.

**The Overview is now sensor-centric (one unified list):**

- The Overview and the status-chip lists are the **same** sensor list with different filters: the
  Overview is the "needs attention" (not-OK) filter with an Errors / Errors+Warnings toggle, and each
  top-bar chip is a single-state filter. They share the same fixed-width columns, so every list lines
  up (a 1-row list matches a busy one) instead of the browser re-sizing columns per row count.
- Each unhappy sensor shows a coloured **"why" line** under its name - the worst trigger's severity and
  name, and how long it's been firing, e.g. "High · Unavailable by ICMP ping · 17d". Severity is
  therefore visible in every list, without an empty column on the OK list.
- The old problem-centric overview is gone; its actions moved into a per-row **kebab** (acknowledge for
  any user; pause/hide the host for admin+helpdesk), matching the sensor lists.

**New Triggers tab:**

- A trigger-centric view with a **Firing / All** toggle. *Firing* is a flat cross-host table of the
  triggers currently in problem; *All* groups every monitored trigger by host with OK / firing state.
  Both list the sensor(s) each trigger's expression watches, so **multi-sensor triggers are visible**.
  Backed by a new `GET /api/triggers`.

**Update system:**

- **Testing-channel updates are detected on a clean "aligned" build.** Right after a release, `:testing`
  and `:latest` are the same clean `vX.Y.Z` image, so the core couldn't tell which channel it tracked
  and stopped offering newer `:testing` builds. The `argus-updater` sidecar now reports the tag the core
  container actually runs under (into the shared dir), so a box on `:testing` keeps getting testing
  updates - no configuration needed.

**Polish:**

- Priority and severity columns in the Overview/status lists, evened-out column spacing, and a taller,
  wider trend sparkline for readability. Sensor graph height 260 -> 320.

## [0.4.18] - 2026-08-26

**Probes table:**

- **Update column no longer wraps or splits a version.** In the probes fleet table the "Update" cell
  could break mid-version (e.g. `7.0.30-` on one line, `r1` on the next). The button, the `-> version`
  chip and the `auto` tag now sit on one rigid line.
- **Columns size to their content.** The 7-column probes table had been inheriting the 4-column
  enrollment table's fixed widths (column 1 = 40%), which ballooned the Probe column and starved the
  Update column (forcing the wrap above). The table now uses content-based auto-layout - every column
  sizes to its text, the table fills the card width, and a horizontal-scroll wrapper covers very narrow
  windows instead of wrapping a cell.

## [0.4.17] - 2026-08-20

**Update checks:**

- **Name the exact target of a `:testing` update.** A testing update previously read "new testing
  build" / "Update to the latest testing build" without saying *which* build. Images are now stamped
  with their `git describe` version as an OCI label (`org.opencontainers.image.version`), and the core
  reads the `:testing` image's label so the About card names the target precisely (e.g. "↑
  v0.4.16-3-gabcdef1" and "Update to v0.4.16-3-gabcdef1"). Falls back to the generic wording if the
  label isn't present (an image built before this change).

**Fixes:**

- **`:testing` no longer looks "rolled back" right after a release.** A release tags the commit that is
  also tip-of-main, but the pre-tag main build had already published `:testing` stamped `vPREV-N-g<sha>`
  (git describe couldn't see the not-yet-created tag). So switching `latest -> testing` straight after a
  release swapped to an identically-coded image that merely *read* as an older version. The v* release
  build now also refreshes `:testing` (cleanly stamped, same commit), so `:testing` is never older than
  the release it contains; it advances again on the next main push.
- **Testing build no longer shows a phantom "update to release".** A `:testing` build is named by
  `git describe` from the last tag (e.g. `v0.4.15-9-g0517cd4` is the v0.4.16 commit). The update check
  compared that numeric base (`0.4.15`) against the newest release (`0.4.16`) and reported "outdated -
  update to v0.4.16" - but an in-place update channel-preserves `:testing` (the same commit), so it
  could never reach the clean release tag, leaving a permanent update prompt that did nothing.
  `appUpdateStatus` now classifies any development build as "development" up front and never compares
  its base against releases; whether a *newer testing image* exists is decided solely by the
  digest-based dev-channel check. To move onto a clean release, use "Change channel or version".

## [0.4.16] - 2026-08-20

**Fixes:**

- **Detect updates on the `:testing` channel.** A development build (e.g. `v0.4.15-4-g1098b9e`) reads
  as "ahead of the newest release", so the update check - which only compared against `v*` releases -
  never flagged an update for it, even when a newer `:testing` image had been published (a container
  manager would show the drift; Argus wouldn't). The core now also compares, via GHCR image digests,
  the running commit's image (`sha-<short>`) against the current `:testing` digest; when they differ it
  surfaces "new testing build" with an "Update to the latest testing build" button. The updater's
  existing channel-preserve re-pulls `:testing` in place. Release-channel behaviour is unchanged.

**Update checks:**

- **Switch channel / version from the GUI.** The About card now has a "Change channel or version"
  control to deliberately move the core between `latest`, `testing`, and the last 5 published releases
  (pinning). A plain "Update" still preserves the current channel; a switch sends the exact target and
  the updater converges on it (a new `exact` flag bypasses channel-preserve). Picking a specific
  version pins the core until you switch back to a channel; the UI confirms first (and warns that a
  version pin stops channel tracking). **Requires redeploying the `argus-updater` sidecar** to pick up
  the new script (the updater doesn't self-update). New `GET /api/version/tags`; `POST
  /api/update/start` accepts an optional `{target}`.
- **Nightly check + manual "Check for updates".** The automatic GHCR update check now runs once daily
  at 04:00 (in the configured timezone) instead of every 3 hours - releases are rare, so nightly is
  plenty and lighter on the registry. For on-demand checks (e.g. right after publishing a build), the
  About card gained a **Check for updates** button that forces an immediate re-check and reflects the
  fresh verdict.

**UI:**

- **Subtle motion pass.** Added short, tasteful transitions so the UI no longer hard-cuts: the app
  fades in right after sign-in (the biggest, most noticeable cut), switching
  sections fades + rises the content (~180ms), opening a sensor chart and expanding a host reveal with
  the same fade-rise, banners fade in, and the sensor caret rotates smoothly. Backed by shared
  `--dur-*` / `--ease-out` motion tokens, and every animation is disabled under
  `prefers-reduced-motion`.
- **Green Reload button after an update.** The post-update "Reload to finish updating" button now uses
  a green (success) variant instead of the default accent colour, so it visually matches the success
  banner above it. New `success` Button variant backed by the `--ok` token.

## [0.4.15] - 2026-08-20

**Fixes:**

- **Long-range charts (1M/3M/6M/1Y) rendered as a shattered "comb".** These ranges read Zabbix
  *trends*, and `trend.get` - unlike `history.get`, which we sort ascending - has no reliable sort
  order, so points could arrive out of chronological order. uPlot requires strictly-increasing
  timestamps, so an unsorted series drew lines jumping back and forth in time (the spiky/gappy look);
  short ranges using raw history were unaffected. The history endpoint now sorts every series by
  timestamp before returning it, so trend charts render as a clean, monotonic line.
- **Core/updater version stamped with the probe's tag.** CI computed the build version with
  `git describe --tags`, which matches *any* tag - so a `:testing` build cut before a `v*` release tag
  existed fell back to the nearest tag of any kind, e.g. the automated `probe/v7.0.29-r7` bump
  (`probe/v7.0.29-r7-5-g0cc2d40` shown as the core's version). The core and updater builds now use
  `git describe --match 'v*'`, so only app release tags name the build (probe tags start with
  `probe/` and are excluded). Cosmetic - it never affected behaviour, only the displayed version.
- **Clearer post-update action.** After a successful self-update the About card kept showing the
  "Update to vX.Y.Z" button (the still-running old bundle thinks it's outdated) alongside a small
  inline "Reload" link - confusing. It now replaces that button with a single primary **"Reload to
  finish updating"** button, so the only offered action is the one that actually completes the update.

## [0.4.14] - 2026-08-20

**Fixes:**

- **Max session length is now enforced live.** The absolute session lifetime was written into the
  session at login and only ever checked against that frozen value, so changing "Max session length"
  in Settings only affected sessions created *after* the change - an existing sign-in kept running for
  its original lifetime (e.g. a session issued when the max was higher could stay valid for days).
  The middleware now also enforces `created_at + current max` on every request (effective expiry =
  `min(stored expiry, created_at + current max)`), matching the idle timeout's live behaviour:
  lowering the max signs affected users out on their next request; raising it never retroactively
  extends an existing session. Settings note updated accordingly.

## [0.4.13] - 2026-08-19

**Self-update fixes** (from first real-world testing):

- **Self-update channel permissions.** A fresh Docker named volume mounts root-owned (`0755`), so the
  non-root, distroless core could not write `request.json` ("could not queue the update") nor clear a
  finished banner ("Dismiss" did nothing) - it could only read the sidecar's status. The
  `argus-updater` sidecar (root, holds the socket) now makes the shared channel dir writable on
  startup, self-healing both new and existing volumes. Ships in `argus-updater:latest`.
- **Preserve the release channel instead of pinning.** A `:latest` / `:testing` core self-updated to a
  fixed `:vX.Y.Z` tag, dropping it off its channel. The updater now re-pulls the core's current tag in
  place when it's a rolling channel (`:latest` / `:testing`), and only bumps a genuinely pinned
  (`:vX.Y.Z`) deployment. Ships in `argus-updater:latest`.
- **"Reload" after a successful update.** The success banner offered only "Dismiss", but the running
  SPA is still the old bundle (and shows the old version) until reloaded - so it now offers a
  **Reload** action (which clears the job, then reloads to the new frontend + version).
- **Dismiss/Reload no longer trip "no settings to update".** Those raw buttons sit inside the Settings
  `<form>` and defaulted to `type="submit"`, so clicking them submitted the (empty) settings form.
  Marked them `type="button"`.

**Other fixes:**

- **Login-screen flash on refresh.** The initial-load state reused the branded login `Frame`, so an
  authenticated page refresh briefly flashed the login chrome before the app mounted. Replaced with a
  neutral loader.

## [0.4.12] - 2026-08-19

**One-click core self-update.** (Roadmap §F/§G)

Argus **Settings → About** can now update the core to the newest release with a single click. Because
the public-facing core is a distroless, non-root container with **no Docker socket**, it cannot
recreate itself - so a small companion container, **`argus-updater`**, holds the socket and does the
work on the core's behalf. The core never touches Docker.

- **`argus-updater` sidecar** (new image `ghcr.io/<owner>/argus-updater`): watches a small volume
  shared with the core. When an admin clicks "Update now", the core drops a request there; the updater
  pulls the target release, recreates the core cloning its config (binds/mounts, env, restart policy,
  network, ports), **verifies** the new container stays healthy (crash-loop guard + best-effort
  `/healthz`), and **rolls back** to the previous container on any failure. It runs no listening
  service. Reuses the proven `argus-recreate.sh` recreate/rollback approach.
- **Core endpoints** (`coreupdate.go`): `POST /api/update/start` (admin), `GET /api/update/state`,
  `POST /api/update/dismiss` (admin). A file-drop channel (`request.json` / `status.json`, atomic
  writes) - no socket, no network endpoint, no tokens; least-privilege (the sidecar shares only a
  dedicated `argus-update` volume, not the core's `/data`). New env `ARGUS_UPDATE_DIR` enables it;
  unset leaves the feature off and Settings shows a manual update note instead.
- **UI**: an "Update now" button plus a running / success / **failure** banner (with the reason and a
  "rolled back" note), and the update state is polled so it reconnects across the brief restart.
- **Deploy**: `deploy/updater/` (Dockerfile + `core-update.sh` + `docker-compose.yml`), a new
  `argus-updater` Unraid template, `ARGUS_UPDATE_DIR` + shared-volume rows added to the core's Unraid
  template and the README env table, a README "One-click self-update" section, and a
  `updater-image.yml` CI workflow.

## [0.4.11] - 2026-08-19

**Release channels - `:testing` for main, `:latest` for releases.** (Roadmap §G)

Pushes to `main` previously published `:latest`, so `:latest` meant "tip of main" (unreleased code).
Now `main` builds publish **`:testing`** and `:latest` is reserved for actual `v*` releases (alongside
`:vX.Y.Z`), so production can safely pin `:latest` and a test box can track `:testing` - no manual
tagging. Every build still gets a `:sha` tag.

**Version indicator - correct "development build" verdict + changelog.** (Roadmap §F)

- The verdict no longer shows a green **LATEST** for a build that is merely *ahead* of the newest
  release. `appUpdateStatus` now distinguishes a clean release tag equal to the newest release
  (`current` -> green tick) from a `git describe` build past its tag or a base newer than anything
  published (`development` -> neutral "development build" tag). Fixes a `:testing`/local build reading
  as "latest", and drops the redundant "newest release" line next to the badge.
- **"What's new"**: when an update is available, Settings shows the release notes, fetched from the
  GitHub Releases API (the CHANGELOG section published as the release body) via `GET
  /api/version/notes`, cached alongside the GHCR latest-version poll.

## [0.4.10] - 2026-08-19

**UI standardization / design system.** (Roadmap §F)

The SPA had grown two competing styling systems: the token-based CSS classes
(`.btn`/`.panel`/`.field`/`.badge`) used by most views, and a legacy inline-style-object system
(`card`/`btn`/`ghost`/`input`) with **hardcoded, non-token colors** (`crimson`, `seagreen`, `#aaa`,
`#888`, `#e59`, …) used by the auth flows, the Account cards, Dashboard and a few others. The
hardcoded colors ignored the theme tokens, so error/success text and muted labels rendered wrong
across light/dark, and the same widgets were rebuilt ad-hoc per view.

- **New `web/src/ui.tsx`** with shared primitives backed by the existing CSS classes: `Button`
  (variant/block), `Card` (title/note), `Field` (labeled input/select), `Banner` (theme-aware
  error/success/info/warn), `Badge` (on/off/err), and `CopyButton` (wraps the shared
  `copyToClipboard`).
- **Migrated** Login / ForgotPassword / ResetPassword, the whole Account family
  (Appearance / Landing / Password / 2FA / Recovery codes / Passkeys), Dashboard, the Users disabled
  badge, `DurationButton`, `SensorChart`, and the three Probes copy buttons onto the primitives.
- **Removed** the legacy `card`/`btn`/`ghost`/`input` objects and every hardcoded color, so the app
  themes correctly and pages look and behave the same. Additive CSS only: `.card`, `.banner`,
  `.callout-warn`, `.btn.block`, `.badge.err`, and `.field select` styling.
- No behavior changes - pure markup/style consolidation.

**Text readability - higher contrast in both themes.** (Roadmap §F)

The secondary-text tokens were too weak, especially on a dim display: dark `--faint` sat at ~3.5:1
on the panel and light `--faint` at ~3.1:1, both below the WCAG AA 4.5:1 target. Lifted
`--text`/`--muted`/`--faint` in all four token blocks (light `:root`, the dark `@media`, and both
`[data-theme]` overrides). Measured on the rendered page: dark faint 3.5 -> 5.9:1 and muted
6.3 -> 9.3:1; light faint 3.1 -> 5.0:1 and muted ~5 -> 6.9:1.

**Version indicator - "are we on the latest release?"**

- The running version is stamped into the binary at build time (`git describe --tags` via
  `-ldflags`), and a new authenticated `GET /api/version` reports
  `{version, latest, update_available, status}`.
- The core polls public GHCR for the newest published release (`vX.Y.Z`) - like the probe fleet
  already does - and compares only the `X.Y.Z` base, so a `:latest`/dev build ahead of the last
  release tag reads as **current** and a genuinely newer release reads as **outdated**.
- The sidebar footer now shows the running build with a green **latest** tick or an amber
  **↑ vX.Y.Z** update pill, so you can tell at a glance without checking the registry.
- Fixed a latent GHCR bug along the way: `tags/list` pages at 100, so once a repo passed 100 tags a
  single fetch missed the newest (the app repo has 167). Tag resolution now follows Link-header
  pagination for both the app and probe images.

**Dev: local build toolchain + CI now typechecks.**

- The CI Docker build ran only `vite build` (esbuild strips types without checking them), so
  TypeScript errors never failed the build. The web `build` script is now `tsc --noEmit && vite
  build`, making type-checking an enforced gate; fixed two pre-existing latent `tsc` errors it
  surfaced (dead `DashboardView`, an over-wide icon index).
- **Reproducible builds.** `go.mod` (now with its full `require` set), `go.sum`, and
  `package-lock.json` are committed, and the Docker build uses the pinned installers
  (`go mod download` + `npm ci`) instead of resolving/tidying at build time - so an image builds
  from locked dependency versions rather than whatever floats to the top on build day.

## [0.4.9] - 2026-08-19

**Dashboard-triggered probe self-update (`docker run`, no compose needed).** (Roadmap §A; DESIGN §18)

Builds on the exact-version check-in: a probe reports its full version *including our wrapper
revision* (`7.0.29-r2`) over the Argus channel, so the core sees subrelease drift - not just the
Zabbix release the API exposes.

- **Update now.** Give a probe the Docker socket + `ARGUS_PROBE_SELFUPDATE=1` and the Probes view
  shows an **Update now** button. It queues the fleet target for that probe
  (`POST /api/probes/{name}/update`), handed to the probe once at its next check-in.
- **Sister-container recreate.** Since a container can't `rm -f` itself mid-update, the probe spawns
  a short-lived `ARGUS_PROBE_ROLE=recreate` helper that clones the proxy's config onto the new image
  via the Docker Engine API (env, binds/mounts, restart policy, network, labels preserved) and
  **rolls back to the previous container if the new one fails to create or start** - a bad update
  never leaves a site without a probe.
- Only offered when the probe reports it's socket-capable; everything else keeps the read-only
  visibility + one-click manual command. unRAID probes stay on native auto-update.
- **Redeploy-aware wizard.** When the Add-probe wizard targets a name that already exists, the
  Docker-run command prepends `docker rm -f <name>` so a redeploy is a single paste (the data
  volume is a host bind mount, so it's kept and the probe stays enrolled). Compose and unRAID
  already recreate in place, so they're unchanged.
- **Enable reporting for pre-existing probes.** A probe enrolled before fleet updates has no
  check-in token (enrollment, which mints it, is skipped once certs exist), so it never reported its
  version even after a plain image update. The Probes view now offers **Enable reporting** on such
  probes (`POST /api/probes/{name}/checkin-token`): it mints a token to drop in as a single env var
  (`ARGUS_PROBE_TOKEN`) via the container's GUI - the check-in URL is derived from the enroll URL, so
  no re-enrollment is needed. The probe **saves the token to its data volume on first boot**, so the
  env var can be removed on later runs (a fresh env token always wins, for rotation).
- **No stray anonymous volume on probes.** The stock Zabbix image marks `/var/lib/zabbix/snmptraps`
  as a `VOLUME`, so Docker created an anonymous volume for it alongside the probe's bind mount. The
  generated Docker-run command, the compose file, and the unRAID templates now bind that subpath
  into the probe's data folder too, so everything stays in one place and no anonymous volume is
  created. The compose file also switches from a named volume to a `./data` bind.
- **Real drift against `latest`.** Argus core now polls the public GHCR tags list anonymously
  (every 3h, no auth) to learn the newest published `X.Y.Z-rN` probe revision, and compares it to
  each probe's reported version. When the fleet target is `latest`, probes behind the newest now
  show **outdated → 7.0.29-rN** instead of just "tracking latest"; the Fleet target control shows
  the newest published version too. Falls back to "tracking" until the first resolve succeeds.

---

## [0.4.8] - 2026-08-18

**Probe fleet updates - control plane + opt-in self-update.** (Roadmap §A; DESIGN §18)

Argus now centrally controls and sees probe versions, over the same outbound-only channel probes
already use (nothing inbound is opened).

- **Version check-in.** Enrollment issues each probe a long-lived check-in token; the probe reports
  its running image version (baked in at build, `/etc/argus-probe.version`) every ~5 min and reads
  the fleet target to converge on (`POST /api/probes/checkin`).
- **Fleet target.** Admins set the target in **Probes → Fleet target version**: `latest` or an exact
  pin in the decoupled probe scheme (e.g. `7.0.29-r1`). Stored server-side
  (`GET`/`PUT /api/probes/target`).
- **Fleet visibility.** The Probes view gains **Version** and **Update** columns showing each probe
  as up-to-date / outdated / tracking-latest / unknown, with an **auto** pill when a probe's
  self-updater is on. A **Last check-in** time that turns amber once a proxy's data is >1 min stale.
- **Version always visible.** Probes that don't check in (older images, or ones updated outside
  Argus like unRAID) still show their installed version, read from the version Zabbix already
  tracks for every connected 7.0 proxy (without the `-rN` wrapper revision, marked as externally
  managed). The precise self-reported version wins when a probe checks in.
- **Guided manual update (any deployment).** Drifted probes offer a one-click
  `docker pull … && docker restart …` - no Docker socket needed.
- **Opt-in self-updater (compose sidecar).** New `deploy/probe-image/docker-compose.yml` runs an
  `ARGUS_PROBE_ROLE=updater` sidecar that, with the Docker socket mounted, recreates the proxy to
  match the target automatically. Off unless deployed; the socket is isolated to the sidecar, never
  the proxy. The Add-probe wizard gains a **Compose + auto-update** output that generates it.

---

## [0.4.7] - 2026-08-18

**Configurable session timeouts + per-user landing page.** (Roadmap §E)

- **Max session length** is now configurable and defaults to **12 hours** (previously a fixed
  7-day absolute expiry). Set in **Settings → Sessions** or via `ARGUS_SESSION_MAX_HOURS`. Applies
  to sessions created after the change.
- **Idle timeout** (new, **off by default**): sign out after a period of inactivity. Each request
  refreshes the session's `last_seen` (throttled to at most once a minute); crossing the window
  drops the session. Set in **Settings → Sessions** or via `ARGUS_SESSION_IDLE_MINUTES` (`0`
  disables it). Changes take effect immediately for existing sessions.
- **Per-user landing page**: choose whether Argus opens on **Overview** or the **Errors** list on
  sign-in / a fresh visit, from **Account → Landing page**. A deep link or reload still restores
  the exact screen in the URL. Stored per user (`POST /api/me/preferences`), so it follows you
  across devices.
- Both timeout settings honour the usual **env-wins** precedence and are marked _(UI)_ in the
  README; the Unraid template gains the two `ARGUS_SESSION_*` variables (advanced).

---

## [0.4.6] - 2026-08-18

- **Removed the duplicate theme toggle.** The dark/light switch existed in both **Account** and the
  admin-only **Settings** page (with different markup). Theme is a per-device preference, so it now
  lives only in **Account**, where every role can reach it - Settings is server-wide config and no
  longer carries it. (Roadmap: a broader UI-standardization pass is now tracked under §F.)

---

## [0.4.5] - 2026-08-17

**Probes view tidy-up.**
- The token list now shows only **actionable** entries (pending / expired) and drops rows once a
  probe is enrolled - a redeemed token is no longer useful information. Heading is now
  "Pending enrollments".
- The live probe row gains an **Enrolled** column showing when each probe self-enrolled via Argus
  (a `-` for proxies registered by hand in Zabbix). Backed by a new `enrolled_at` field on
  `/api/proxies`, derived from the enrollment token's redemption time.
- The generated **unRAID probe template** now includes the Argus `<Icon>` (matching the server
  template), so the probe container shows the logo in unRAID's Docker tab.

---

## [0.4.4] - 2026-08-17

- **Probe container naming.** The generated **Docker run** and **unRAID XML** now name the probe
  container `argus-<proxy_name>` (e.g. `argus-proxy-office`) with a matching data volume, instead
  of a fixed `argus-probe`. This makes each site's container self-identifying in the Docker/unRAID
  UI when several probes run on one host. Updated the Dockerfile example and the probe update/
  migration runbook to match.

---

## [0.4.3] - 2026-08-15

- **Login screen** is centered horizontally and anchored in the upper-middle (instead of top-left),
  and now shows the **Argus logo** beside the title.
- **Sidebar** uses the logo in place of the old eye glyph.

---

## [0.4.2] - 2026-08-15

**Branding - the Argus logo.** Added the eagle/radar logo across the project:
- **Browser tab favicon** (+ apple-touch icon) - served from the app (`web/public/`).
- **READMEs** (root + `argus/`) show the logo at the top.
- **unRAID template** `<Icon>` points at the logo (this is also the container's icon in unRAID's
  Docker tab).

Assets live in `argus/web/public/`: `argus-logo.png` (512², used by the READMEs + unRAID icon),
`favicon.png` (48²), `apple-touch-icon.png` (180²).

---

## [0.4.1] - 2026-08-15

Mobile polish:
- **Probes tables are now responsive** - the tokens and live-proxy tables stack into labelled
  cards on a phone (like the Users and status lists), instead of a cramped fixed 4-column table
  with truncated text and wrapped status pills.
- **Users: the passkeys count lines up** with the other values - it's inset to match the internal
  padding of the boxed values (2FA badge / role select / inputs) instead of sitting flush past them.
- Status pills (`.tag`) no longer wrap.

---

## [0.4.0] - 2026-08-14

**Probe enrollment - one-click, from the GUI.** Adding a site probe no longer needs
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
- Enrollment is **off unless the CA is mounted** - new config `ARGUS_CA_CERT_FILE`,
  `ARGUS_CA_KEY_FILE` (mount read-only), and `ARGUS_PROBE_CORE_HOST` (address probes dial for
  `:10051`, defaults to the Public URL host). The Zabbix API token needs **super-admin** rights to
  register proxies. The `/api/enroll` endpoint is IP rate-limited.

New: `internal/pki` (CA load + CSR signing), `enroll_tokens` table, `POST /api/enroll` +
admin `GET/POST/DELETE /api/probes/tokens`, `zabbix.EnsureActiveProxyCert`, and
`deploy/probe-image/` (Dockerfile + enrollment entrypoint) built by CI.

---

## [0.3.3] - 2026-08-14

**Self-service password reset.** Users who forget their password can now recover it themselves
via an emailed link - no admin intervention.

- A **"Forgot password?"** link on the sign-in screen takes an email and sends a **single-use,
  1-hour reset link**; the reset page sets a new password. The link (`/?reset=…`) carries a
  256-bit token whose **SHA-256 is stored** (never the token itself), consumed on use.
- **Anti-enumeration**: the request endpoint always responds the same way and does its work in
  the background, so it never reveals whether an account exists. Requests are **rate-limited** by
  IP and by email; the reset submission is throttled by IP.
- On success, **all of that user's sessions are revoked** (sign-out everywhere). **MFA is
  untouched** - a reset changes only the password, so two-factor is still required at sign-in.
- **Delivery reuses your existing email notification channel** as the SMTP sender - no separate
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
  **Account** page - available to all signed-in users.

---

## [0.3.1] - 2026-08-14

**Deep-link URLs - the view now lives in the address bar.** Navigation was tracked in React
state only, so the URL stayed at the base FQDN and a **reload always dropped you back to
Overview**; notification "Open in Argus" links also didn't survive a refresh.

- The active view is encoded in the URL - `?view=…`, with `&filter=…` for the status lists and
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
changed configuration off the `docker run` command and into the UI - no redeploy to change it.

- **Editable in the UI**: the **Zabbix API URL + token**, the **Public URL** (notification
  links), the **timezone**, and the **login rate-limit** thresholds. Changes apply live - the
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
  For real protection of database backups, set **`ARGUS_SECRET_KEY`** to a long random string -
  it's hashed to the key and kept off the data volume. Set it on the first v0.2.8 deploy and keep
  it stable (changing the key, or losing the keyfile, makes existing encrypted values unreadable -
  channels would need re-entering and 2FA re-enrolling).
- Existing plaintext values are migrated to encrypted form automatically on startup. Encryption is
  transparent - values are decrypted only in memory when needed.
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
- **Status-list kebab** no longer opens off-screen - the action cell kept its desktop 44px width,
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

- The sidebar becomes an **off-canvas drawer** - hidden by default, slid in by the ☰ button over a
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

Note: pulling a new image doesn't replace a *running* container - recreate it (or update the
pinned tag) to pick up a release.

---

## [0.2.2] - 2026-08-13

**2-hour trend graph in alerts.** Every problem and recovery notification now carries a compact
chart of the offending sensor's last two hours, rendered server-side to PNG (pure stdlib, no new
deps) in the status color.

- The image is **uploaded directly** to each channel - Discord webhook attachment
  (`attachment://`), Telegram `sendPhoto`, and an inline `cid:` image in the HTML email - so it
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
- Removed the stale "Soon" badge from the Notifications sidebar item (Probes keeps its badge -
  enrollment is still pending).

Next: the 2-hour trend graph in the message body (email inline / link-out, since the instance is
internal-only).

---

## [0.2.0] - 2026-08-12

**Notifications - alerting engine + channel management.** Argus now watches active problems
itself and delivers alerts, respecting the same suppression rules as the Overview: acknowledged,
paused, and hidden items stay quiet.

- **Channels** (admin, Notifications tab): add/edit/enable/delete **Discord** (webhook),
  **Telegram** (bot + optional forum topic), and **Email** (SMTP: STARTTLS / implicit TLS / none)
  targets, each scoped to **all sites** or one **host group**. A **Test** button sends a sample
  notification so credentials can be verified end-to-end. Config is stored in the SQLite data
  volume (plaintext - single-tenant, private-VM assumption; env-key encryption may come later).
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

**First minor release - feature-complete UI.** No code changes since v0.0.32; this marks the
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
  sensor level - with durations, auto-expiry, inheritance, and honest graph gaps.
- **Overview & summary**: a cross-site active-problem list with deep-links into the tree, and a
  six-state status summary (OK / Warning / Error / Acknowledged / Paused / Hidden) whose chips
  open filtered, cross-site sensor lists.
- **Live Probes** view (real Zabbix proxy status) and a polished, theme-aware (dark/light),
  collapsible shell that updates on its own.
- Placeholders for the upcoming **Notifications** and **Probe enrollment** work.

---

## [0.0.32] - 2026-08-12

**New UI, stage 4 of 4 - the Users page (+ real "disable user"), and Overview spacing.**
This completes the UI port from the approved mockup.

### Added
- **Redesigned Users page**: inline-editable **email / name / surname** (save on blur), a role
  dropdown, and a per-user **⋮ menu** - Reset password · Remove 2FA · Remove passkeys ·
  Disable/Enable user · Remove user. Add-user is a toggle form in the header.
- **Disable user** is now a real feature: a disabled account **cannot sign in** (blocked on every
  login path - password, 2FA, passkey), shown faded with a "disabled" badge, and re-enablable.
  Guarded so you can't disable yourself or the last remaining admin. New `disabled` column
  (additive migration), `POST /api/users/{id}/disabled`, and email is now editable via PATCH.

### Changed
- **Overview** is now a properly spaced table (Host · Problem · Trend · Age · action), matching
  the status lists - no more large gap between the description and the controls on wide screens.

---

## [0.0.31] - 2026-08-12

**Mini-graphs (for real) & collapsed-sidebar fix.**

### Added
- **Inline sparklines** are back - and now real, drawn from live history - in the Monitoring
  tree (new Trend column), the Overview problem rows, and the status-chip lists. Backed by a new
  batched **`GET /api/spark?items=…`** endpoint (one `item.get` + up to two `history.get`,
  server-downsampled to ~24 points) so a whole host/list loads its sparks in a single request.

### Fixed
- **Collapsed sidebar**: the user menu (Account · Log out) no longer gets clipped by the
  sidebar - it now overflows correctly and sits above the content.

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

**New UI, stage 3b of 4 - full status summary & filtered lists.**

### Added
- **Six-chip status summary** in the top bar - **OK · Warnings · Errors · Acknowledged ·
  Paused · Hidden** - counted from a new cross-host sensor census, updating live (and instantly
  on any ack/pause/hide).
- **Clickable chips → filtered lists**: click any chip to see just those sensors across all
  sites (host · sensor · value · last check · actions), with deep-links to each sensor's host or
  chart and a per-row kebab (pause / hide / resume / show).
- Backend **`GET /api/sensors`** - a census of the curated "key" sensors, each tagged with one
  state (hidden > paused > error > warning > acknowledged > ok). New `item.get`-based
  `AllItems` client method.

### Notes
- The census covers curated key sensors (the same set as Monitoring's "Key sensors"); unsupported
  sensors are treated as unknown, not counted as OK. On very large deployments this census will
  move to server-side counts.

---

## [0.0.28] - 2026-08-12

**New UI, stage 3a of 4 - Overview redesign, deep-links & instant refresh.**

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

**New UI, stage 2 of 4 - the Monitoring tree & live Probes.**

### Added
- **Site → host → sensor tree** in Monitoring, grouped by **Zabbix host group** (site = host
  group; a host in several groups appears under each; hosts with none fall under "Ungrouped").
  Collapsible sites and hosts, a worst-state dot per site, and a panel-level Key sensors / All
  sensors toggle. Backend: `host.get` now returns each host's groups (`selectHostGroups`), and
  `hostView` carries `groups`.
- **Kebab (⋮) action menus** on hosts and sensors, replacing the inline Pause/Hide buttons -
  Pause / Hide (with the duration picker), Resume / Show, and **Acknowledge** on a sensor that
  has an unacknowledged problem. Inherited (host-controlled) pause/hide is cleared at the host.
- **Live Probes page**: shows the real Zabbix proxies (the per-site collectors) with online /
  offline status (seen within 5 min), last check-in, and mode - replacing the placeholder table.
  New `GET /api/proxies` (proxy.get) and client method. Token enrollment is still "coming soon".

### Changed
- The sensor table, charts (range tabs), and problem panel now use the design-token styling.
  Full-size charts keep uPlot, so they retain axis ticks and the hover legend.

---

## [0.0.26] - 2026-08-12

**New UI, stage 1 of 4 - foundation & shell.** Start of the port from the approved design
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
- **Account moved into the user chip** menu (Account settings · Log out) - no longer a nav tab.
- **Placeholders** for the upcoming **Notifications** and **Probes** sections, marked "Soon".

### Changed
- Shared surfaces (cards, inputs, buttons, dropdowns) now read from the design tokens, so the
  existing views are theme-aware. Full per-view redesigns follow in stages 2-4.

---

## [0.0.25] - 2026-08-12

### Added
- **"Suppressed until" labels**: paused, hidden, and acknowledged items now show when they'll
  clear (e.g. "paused · until Aug 12, 14:30", or "no expiry" when indefinite) in the host list,
  sensor rows, the host problem panel, and the Overview.
- **Auto-refresh for the Monitoring view and graphs**: the host list, the expanded sensor
  values/last-check/problems (30s), and open charts (60s) now update on their own - matching
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
- **Every suppression takes a duration** - Acknowledge, Pause, and Hide now offer
  **1 hour / 8 hours / 1 day / 1 week / Indefinitely / Custom…** When the timer expires the
  state clears automatically: hide/ack lazily, and a background **sweeper** re-enables timed
  **Pause**s in Zabbix.
- **Un-acknowledge** brings a problem back into the error state. Acknowledge is now tracked in
  Argus (with expiry) and mirrored to Zabbix, so it can be undone and can expire.
- **Acknowledged problems fade** (PRTG-style): red/amber become a muted tone in the Overview,
  the host problem panel, and the sensor-row highlight - still visible, clearly de-emphasized.

### Changed
- Suppression storage generalized to a single `suppressions` table (kind hide/pause/ack, scope
  host/item/event, optional `until`). Endpoints `POST /api/.../{pause,hide}` and
  `POST /api/events/{id}/ack` accept `duration_seconds` (0 = indefinite); `DELETE
  /api/events/{id}/ack` un-acknowledges.

---

## [0.0.22] - 2026-08-12

**Overview dashboard.** The cross-host "what's wrong right now" view - now the default landing.

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
  toggles are disabled - you can't resume a single sensor while its whole host is paused. This
  matches how disabling a host in Zabbix actually stops all of its sensors collecting.

---

## [0.0.19] - 2026-08-12

**Two distinct suppression actions: Pause and Hide.** Hosts and sensors each get both.

### Added / Changed
- **Pause** (blue) = PRTG-style stop: disables the host/item **in Zabbix**, so collection
  actually stops (a gap in the graph while paused). Resuming re-enables it. Requires the API
  token to have **write** permission - a read-only token returns a clear error.
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
- **Per-sensor pause** - each sensor row now has its own Pause/Resume control (Helpdesk +
  Admin); paused sensors are dimmed and marked "(paused)". Complements host-level pause.
- Endpoints: `POST`/`DELETE /api/items/{id}/pause`; items carry a `paused` flag.

### Notes
- Acknowledge was already per-problem (each active problem has its own Acknowledge button);
  Zabbix acknowledges at the event level, tied to the specific failing trigger/sensor.

---

## [0.0.16] - 2026-08-12

**States model - Acknowledge & Pause.** The first of the state controls the dashboards will
build on.

### Added
- **Acknowledge** a problem from the Active problems panel - uses Zabbix's native
  `event.acknowledge`, so it's reflected in Zabbix too; acked problems show a "✓ acknowledged"
  marker. Available to any signed-in user.
- **Pause / Resume** a host (Helpdesk + Admin) - an Argus-side flag: paused hosts go grey,
  hide their problem count, and are marked "(paused)". Zabbix keeps collecting; pausing just
  suppresses Argus-side surfacing (and, later, alerting). Instant and reversible.
- Endpoints: `POST /api/events/{id}/ack`, `POST`/`DELETE /api/hosts/{id}/pause`.
- Problems now carry their Zabbix `event_id` and `acknowledged` state; hosts carry `paused`.

### Notes
- Acknowledge runs with the **API token's** Zabbix permissions - the token's user needs rights
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
  duration (e.g. `1d 4h 14m`) instead of raw seconds - in the table and on the graphs.
- **Network sensors are de-duplicated**: the per-interface error/dropped/packet counters that
  share the `net.if.in`/`net.if.out` key are now labeled distinctly (e.g. "Traffic in dropped
  (enX0)"), so the byte-rate row is no longer repeated.

---

## [0.0.13] - 2026-08-12

### Changed
- **Byte values are now human-readable** - sizes/throughput auto-scale to KB/MB/GB/TB
  (1024-based) in both the sensor table and the graphs, instead of raw bytes.
- **CPU utilization rows are labeled by state** (idle/user/system/iowait/…) instead of a dozen
  identical "CPU utilization" rows sharing the `system.cpu.util` base key.

### Fixed
- **Graph legend no longer shows "--" when idle** - it now displays the latest point's time and
  value when the cursor isn't over the chart, and formats them (scaled units, readable time).

---

## [0.0.12] - 2026-08-11

**Curated sensor views.** The Monitoring view no longer dumps every raw template item.

### Added
- Items are classified by their Zabbix key into a small set of **categories** (Ping, CPU,
  Memory, Disk, Network, Temperature, Uptime) with friendly labels, and the default view shows
  only those - grouped by category - so you see the sensors that matter and only when the host
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
  with a shaded min/max band - matching Zabbix's 30-day history / 730-day trend retention.
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

**Read path - hosts & sensors.** The first monitoring-facing feature: Argus now reads live
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
auth-hardening track. Uses discoverable (resident) credentials, so login needs no username -
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
  falls back to password + TOTP - WebAuthn RP IDs can't be a bare IP.

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
Authenticator) - both scanning the QR and pasting the setup key.

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

**User management.** Makes the role model usable - admins can now manage the other accounts.

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

**Authentication.** Adds persistent users, password login with sessions, and the role model -
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

### Added - Argus application (`argus/`)
- **Go backend** that serves an embedded React single-page app from a single binary/container.
- **Health endpoints:** `GET /healthz` (liveness) and `GET /api/health`, which reports backend
  status and whether the Zabbix JSON-RPC API is reachable (with its version).
- **Zabbix API client** (JSON-RPC) with an unauthenticated `apiinfo.version` connectivity check.
- **Environment-based configuration** (`ARGUS_LISTEN`, `ARGUS_ZABBIX_API_URL`, `ARGUS_DATA_DIR`)
  so the container is configured entirely via `docker run`.
- **React + Vite frontend** showing live backend and Zabbix status, with a dark theme.

### Added - delivery & CI
- **Multi-stage Dockerfile:** build frontend → build Go binary (frontend embedded) → minimal
  distroless runtime image.
- **GitHub Actions** workflow that builds and pushes `ghcr.io/<owner>/argus` (`:latest` on the
  default branch, `:vX.Y.Z` on tags) and auto-publishes a GitHub Release for each tag.

### Added - deployment kit (`deploy/`)
- `setup-core.sh` - installs Zabbix 7.0 + PostgreSQL 17 + TimescaleDB on Debian 13, including
  auto-pinning TimescaleDB to a Zabbix-supported 2.28.x.
- `gen-certs.sh` - one shared CA + unique per-site mutual-TLS client certs; supports adding a
  new site with a single command.
- `zabbix_server.conf.snippet` - mutual-TLS config, tuning, and the TimescaleDB compatibility flag.
- `run-probe.sh` and an **unRAID template** for deploying an active proxy (single container,
  mTLS, 7-day offline buffer).
- `PHASE0-CHECKLIST.md` (command-by-command runbook) and `README.md` (including a documented
  TimescaleDB version-regression fix).

### Added - documentation
- `docs/DESIGN.md` - the full system design (architecture, device classes, thresholds, state
  model, notifications, auth, roadmap).
- Top-level `README.md` describing the repository layout and status.

### Project milestone (infrastructure validated outside the repo)
- Zabbix 7.0 core live on Debian 13 (`10.0.0.10`) with PostgreSQL 17 + TimescaleDB 2.28.3.
- The **site1** active proxy is online over mutual TLS; live ICMP data flows through it; the
  7-day offline buffer was verified by backfill after a simulated outage.

### Notes
- TimescaleDB is pinned to 2.28.x because Zabbix 7.0 rejects 2.29+.
- This is a **walking skeleton**: authentication, the PKI/enrollment backend, dashboards,
  auto-discovery, and notifications are not yet implemented - they arrive in later releases.

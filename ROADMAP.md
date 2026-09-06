# Argus - Roadmap

A living checklist of what's built and what's left. The **design** and rationale live in
[`docs/DESIGN.md`](docs/DESIGN.md); this file is the tracking view. Sizes are rough:
**S** ≈ hours, **M** ≈ a day or few, **L** ≈ a week+ / multi-part.

Legend: `[x]` done · `[~]` partly done · `[ ]` planned · _(FE)_ frontend-only · _(BE)_ backend · _(ops)_ infra/deploy.

---

## ✅ Shipped (through v0.4.12)

**v0.4.12 - one-click core self-update**
- [x] **One-click core self-update** via an `argus-updater` sidecar (holds the Docker socket so the public-facing core never does): pull -> recreate cloning config -> health-verify -> rollback on failure, with a result banner in Settings. New `ARGUS_UPDATE_DIR` channel, `argus-updater` image + Unraid template + compose

**v0.4.11 - release hygiene**
- [x] **`:testing` channel / release-gated `:latest`** - `main` builds publish `:testing`; `:latest` is reserved for `v*` releases
- [x] **Version verdict fix + changelog** - a build ahead of the newest release reads as "development build" (not green LATEST); Settings shows the release notes when an update is available


**Foundations (through v0.3.0)**
- [x] **Phase 0 - foundations**: Zabbix 7.0 core + site1 probe over mutual TLS, 7-day offline buffer
- [x] **Auth**: roles (admin/helpdesk/viewer), argon2id + sessions, TOTP + recovery codes, WebAuthn passkeys, admin user management, login rate-limiting
- [x] **Monitoring**: site→host→sensor tree, curated key sensors, per-sensor charts (2h-1Y, zoom), sparklines
- [x] **States**: acknowledge / pause / hide with durations, auto-expiry, host→sensor inheritance
- [x] **Overview**: cross-site problem list + six-state status chips
- [x] **Notifications**: engine + Discord/Telegram/email channels, rich messages, 2h trend graphs, one-click ack
- [x] **Mobile-responsive** layout
- [x] **Security**: AES-256-GCM at-rest encryption, brute-force protection
- [x] **Admin Settings** (v0.3.0): runtime Zabbix conn / public URL / timezone / login limits

**v0.4.x - probe fleet & account hardening**
- [x] **Probe enrollment** (v0.4.0): token-based PKI, Add-probe wizard, self-enrolling `argus-probe` image
- [x] **Session timeouts + per-user landing page** (v0.4.7); **self-service password reset** (v0.3.3)
- [x] **Probe fleet updates** (v0.4.8-0.4.9): fleet version visibility, GHCR-resolved drift, dashboard-triggered self-update (sister-container recreate + rollback) + opt-in compose sidecar, "enable reporting" for older probes, single-folder storage
- [x] **4 of 5 site probes online** and self-reporting (mybz, myng, myrn, office)
- [x] **UI standardization / design system** (v0.4.10): shared `ui.tsx` primitives (Button, Card, Field, Banner, Badge, CopyButton) backed by the CSS tokens; legacy inline-style objects and all hardcoded colors removed so the SPA themes correctly and pages are consistent
- [x] **Text readability** (v0.4.10): raised `--text`/`--muted`/`--faint` contrast in both themes to clear WCAG AA (faint 3.x -> 5-6:1)
- [x] **Version indicator** (v0.4.10): build-stamped running version + `GET /api/version`; core resolves the newest published release from GHCR and the sidebar footer shows a "latest" tick or an "update available" pill
- [x] **Local build toolchain + CI typecheck** (v0.4.10): Node/Go installed for local `tsc`/`vite`/`go build`; the web build now runs `tsc --noEmit` so type errors fail CI (previously `vite build` skipped type-checking)

---

## 🚧 Remaining

### A. Probe fleet & enrollment
- [x] **Token-based enrollment / PKI service** - mint token → probe self-generates key + CSR → core signs & registers proxy via Zabbix API; private key never leaves the probe - v0.4.0
- [x] **"Add probe" wizard** UI + self-enrolling `argus-probe` image - v0.4.0
- [x] **Delete / deregister a proxy from Argus** - remove a decommissioned probe from the Probes page: `proxy.delete` via the Zabbix API plus cleanup of the Argus-side records (enroll tokens, check-in/version state in `probe_agents`, per-proxy SNMP defaults in `snmp_defaults`). A **"Clean up"** reconcile prunes orphan rows left when a proxy is deleted directly in Zabbix (out-of-band), keeping pending enrollment tokens. The empty `proxy-<site>` host group is left in place (delete/hide it from the tree). - v0.4.29
- [~] Bring **site2-site5** probes online (Probes → Add probe) - **4/5 done** (mybz, myng, myrn, office); **mygrz** blocked - its building is under renovation, so it won't come online in the near term - _(ops)_ S
- [x] **Probe fleet updates - control plane + opt-in self-update** - Argus holds a fleet target (`latest` or a `7.0.29-r1` pin) + shows each probe's version vs target; probes check in outbound; drift gets a one-click manual update; opt-in compose sidecar (`ARGUS_PROBE_ROLE=updater`) self-updates via the Docker socket. See DESIGN §18 - v0.4.8
- [x] **Dashboard-triggered self-update + exact-version reporting** - probes report their exact `X.Y.Z-rN` version over the check-in; a socket-enabled probe self-updates on demand via a short-lived `recreate` sister container (config-cloning, rollback on failure); "Enable reporting" mints a check-in token for older probes (persisted to the volume, one env var); redeploy-aware wizard command; snmptraps bound so no anonymous volume - v0.4.9
- [x] **Resolve "latest" from the registry for accurate drift** - Argus core polls GHCR anonymously (`ghcr.io/token` → `/v2/<owner>/argus-probe/tags/list`) every 3h, picks the newest `X.Y.Z-rN` tag, and compares to each probe's reported version, so a `latest` target flags "outdated → rN" instead of just "tracking". - v0.4.9
- [x] **Add-probe wizard: self-update toggle** - an "Enable self-update" switch in the deploy panel adds `-v /var/run/docker.sock` + `ARGUS_PROBE_SELFUPDATE=1` to the generated Docker-run command (and the socket volume + variable to the unRAID XML), so socket-enabled probes deploy straight from the wizard; Compose already bundles the updater sidecar, so it reads as always-on there - v0.4.27
- [x] **Unify the updater across core + probe** - **v0.4.30**. One `argus-updater` image, one recreate engine (config-clone + verify + rollback), modes `core` / `probe-watch` / `probe-recreate`. Every unit is now **two containers** (main + sidecar; socket only on the sidecar): the probe image dropped `docker-cli` and became a pure reporter; the socket-on-proxy and compose `probe-poll` paths were retired. The updater updates **itself** (ephemeral `probe-recreate`), driven from the Probes **Updater** column and Settings. Wizard (run/compose) + the VM emit both containers.
- [x] **Self-configuring probe VM** (VMware / Nutanix / XCP-NG / KVM) - one Packer golden image (Debian 13 `generic` + the `argus-probe` proxy **and** `argus-updater` sidecar, no baked-in token) with **delivery and enrollment decoupled** so it serves every target. **Enrollment** (three ways, all from Add-probe → VM): pasted cloud-init user-data · a **downloadable seed ISO** (an Argus-owned image, label `ARGUSSEED` / `ARGUS.ENV`, read by our first-boot service - not a cloud-init NoCloud seed, so no Joliet needed and no dependency on cloud-init's fiddly datasource detection) · or the **first-boot setup page** fallback. **Delivery:** CI publishes **OVA** (stream-optimized VMDK + OVF; VMware/Nutanix/VirtualBox + Xen Orchestra *Import → OVA*), **qcow2** (KVM/libvirt), and **VHD** (Hyper-V; VDI-import on XCP-NG). Live-tested end-to-end (first-boot enrollment on XCP-NG). See DESIGN §14a. **Cut v0.4.31 (core) + probe-vm/v0.3.0.** - _(ops+image+FE)_ L
- [x] **Break-glass VM access (per-VM credential in Argus)** - the golden image ships with **no login**, so this adds a cloud-init-independent path: on first boot the VM creates a per-VM **`argus`** sudo user with a generated password, reports it over the probe check-in channel (`POST /api/probes/break-glass`), and Argus stores it **encrypted** (existing cipher) and reveals it to admins on the Probes page (**Console** button). SSH host keys regenerate on first boot; the **console keyboard layout** is configurable per-VM (Add-probe → VM / setup page → `/etc/vconsole.conf`). This pass also **dropped cloud-init entirely** (purged in `provision.sh`; enrollment is now seed ISO / first-boot page) and bumped the VM defaults (30 GB disk, 4 GB RAM). Primary access = the **hypervisor console** (XO); remote sites are outbound-only so inbound SSH needs the VPN. SSH-key auth deferred (password covers console + VPN SSH). **Cut v0.4.31.** - _(image+BE+FE)_ M
- [ ] **Bare-metal probe SKU** (optional, later) - the *same* golden image wrapped in a **Clonezilla restore ISO** (boot → pick disk → restore, à la adsb-feeder) for appliance-style installs with no hypervisor. Reuses the first-boot enrollment service, so no per-image token. Reserved for bare metal - buys nothing inside a hypervisor. See DESIGN §14a. - _(ops+image)_ M
- [x] **OS patching & lifecycle** (core + probe VMs) - `unattended-upgrades` (security-suite only, respects the core's Timescale hold) + `needrestart` baked into the golden image and installed on the core by `setup-core.sh`. **Probes** auto-reboot in a weekly ~03:00 window (they buffer offline); **core** reboot is operator-scheduled via a **Settings → OS updates** mask (pick day+time, or notify-only; default notify-only). Probes report pending-security-update count + `reboot-required` hourly (`POST /api/probes/os-status`) and the core reports its own via a host timer into the shared update dir; surfaced on the **Probes** page (**OS** column + "N need a reboot") and Settings. Patching stays **local, never remote-triggered** (no clean apt rollback); hypervisor snapshots are the safety net. See DESIGN §14c. **v0.4.32 (core) + probe-vm/v0.3.1.** - _(ops+BE+FE)_ M-L

### B. Auto-provisioning / discovery (Phase 4 - "replaces PRTG Add Sensor")
- [ ] Per-site **UniFi API sweep** → inventory → Zabbix hosts, tagged, bound to proxy - _(BE)_ **L**
- [ ] **Capability fingerprint** (SNMP sysObjectID, HTTP(S), DNS :53, NUT :3493) - _(BE)_ M
- [ ] **Template attach** by fingerprint + **LLD** per-instance items (disks, NICs, ports, radios, VMs) - _(BE)_ M
- [ ] Default thresholds applied on discovery - _(BE)_ S
- [ ] **"Discovered - review"** screen (confirm / adjust / ignore) - _(FE)_ M

### C. Device classes & templates
Zabbix templates (hand-authored YAML under `argus/internal/provision/templates/`, imported via
`configuration.import` on Argus startup) + an Argus class registry/overlay. Catalog = DESIGN §5
(**SNMP-first**; ~23 classes across 5 patterns). Built in three phases:

**C0 - Framework (unblocks all):**
- [ ] Zabbix client: add `configuration.import`, `template.get`, `host.create`, template/group/macro attach, `usermacro.*` - _(BE)_ M
- [ ] Startup template-reconcile (version the set, import if changed) + `argus/internal/provision/templates/` home - _(BE)_ S-M
- [ ] Class **registry** (generalize `classifyItem` → class catalog) + `device_class` overlay table keyed by `host_id` - _(BE)_ M
- [ ] Minimal **manual attach** path (`POST /api/hosts` → create host, attach class templates/macros/interface) - _(BE)_ M
- [ ] **Base/Ping** template + **HTTP/HTTPS-custom-port** add-on (the universal slice, end-to-end) - _(BE)_ S

**C1 - Vertical slice (prove both dominant patterns):**
- [ ] **Generic Linux SNMP** template (host-resources+UCD+IF-MIB, fs/NIC LLD, §6 macros) - _(BE)_ M
- [ ] **UniFi Switch** HTTP-API template (self-hosted controller; master+dependent items, port LLD) - _(BE)_ M
- [ ] Done = host created via Argus → template attached → sensors show with right labels/units → §6 triggers fire → LLD only-real instances → a per-host threshold override works

**C2 - Breadth by pattern (ROI order):**
- [ ] Finish **SNMP family**: Aruba CX, InstantOn 1960, QNAP, Ugreen UGOS, unRAID, Sophos XGS, NetScaler, Libraesva, Windows (SNMP + LANMGR services), Hyper-V (host) - _(BE)_ **L**
- [ ] Finish **UniFi family**: Gateway, AP, OS Console (+ cloud-gateway access mode) - _(BE)_ M
- [ ] **Agentless**: DNS/AdGuard, NUT UPS (upsd :3493), Linux SSH (no-SNMP fallback) - _(BE)_ M
- [ ] **API-source heavies** (register-endpoint + host-prototype LLD): Nutanix Prism, XCP-NG (XAPI collector), Citrix farm (OData) - _(BE)_ **L**
- [ ] **vSphere** (native VMware collector) - **low priority, production-only** (not the lab) - _(BE)_ M

**SNMP gaps** (DESIGN §5) - unRAID disk-temp/SMART, Hyper-V per-VM (WMI), XCP-NG XAPI, UniFi PoE/WAN, AdGuard stats, NUT protocol - are handled inside the per-class work above, not a separate track.

### D. Management UI screens
- [ ] **Device management** - add/edit, assign site+proxy, class, per-device threshold overrides - _(FE+BE)_ M
- [ ] **Thresholds** - global defaults + per-device/sensor overrides - _(FE+BE)_ M
- [ ] **Settings expansion** - retention controls, proxy health, allowed-hosts - _(FE+BE)_ S-M

### E. Auth / account gaps
- [x] **Self-service email password reset** (single-use emailed link; reuses the email channel) - v0.3.3
- [x] **Configurable session timeouts** - **max session lifetime** (default **12h**, replacing the old fixed 7-day absolute expiry) + optional **idle timeout** (sliding; **disabled by default**). Both admin-editable in **Settings → Sessions** (env-overridable). Idle uses a per-session `last_seen` bumped by the auth middleware (throttled ≤1 write/min); max caps absolute lifetime - v0.4.7
- [x] **Per-user landing page** preference (Overview vs Errors), in **Account → Landing page** - v0.4.7

### F. UX / quality-of-life
- [x] **Deep-link URLs / reload persistence** - reflect the view in the address bar (DESIGN §17) - _(FE)_
- [x] **UI standardization / design system** - the SPA carried two competing styling systems: the
  token-based CSS classes and a legacy inline-style-object system (`card`/`btn`/`ghost`/`input`)
  with hardcoded, non-token colors (crimson/seagreen/#aaa...) that ignored the theme. Added
  `web/src/ui.tsx` with shared primitives (Button, Card, Field, Banner, Badge, CopyButton) backed
  by the CSS classes, migrated the auth flows / Account family / Dashboard / Users / DurationButton /
  SensorChart / Probes copy buttons onto them, and removed the legacy objects and all hardcoded
  colors so pages look and behave the same and theme correctly. - _(FE)_ v0.4.10
- [x] **Global search** - top-bar quick-switcher (and Ctrl/Cmd-K) searching hosts by name/IP, sensors by name, and groups by name; a hit opens the tree host, its chart, or the group focus. `GET /api/search` with prefix/word-boundary/substring ranking (DESIGN §16) - v0.4.27
- [x] **Per-channel severity filter** - each notification channel sets its own floor (Warning / Average / High / Disaster, default Warning); a problem below the floor - and its recovery - skips that channel - v0.4.27
- [x] **Labeled graph axes** in alert PNGs - min/mid/max Y gridlines + relative-time X labels, rendered with the built-in basicfont face (adds golang.org/x/image, no TTF shipped) - v0.4.27
- [x] **Human-readable time units** - second-based readings (latency, response time) auto-scale to ms/µs/ns in the sensor value, chart axis + legend, and alert messages, matching the existing byte/bit scaling. - v0.4.37
- [x] **Monitoring tree: direct-parent hosts** - a host that belongs directly to a parent group now renders at the group's own level (above its subgroups) rather than looking nested in one; a group's hosts and subgroups reorder as one interleaved list; and row dividers stay consistent in any order. - v0.4.36
- [x] **Per-user + multi-site notifications** - personal Telegram/Discord channels each user self-manages in **Account** (their own @BotFather bot / webhook, `/api/me/notify/*`, encrypted at rest); an email channel can deliver to **every registered user's** address; and channels (global + personal) can target **multiple sites** via a hierarchical picker where selecting a probe's root group covers its subgroups. Lays the groundwork for the mobile push channel (§I). - v0.4.35

### G. Scale & production readiness
- [ ] **Sizing pass** before the ~6000-sensor deployment (proxies, DB, caches, NVPS) - analysis
- [ ] **Server-side census/counts** - move the `/api/sensors` full census server-side at scale - _(BE)_ M
- [x] **`testing` channel / release-gated `latest`** - `main` pushes now publish `:testing` (+ `:sha`)
  and only `v*` tag pushes move `:latest` (alongside `:vX.Y.Z`), so production can pin `:latest` and a
  test box tracks `:testing` without manual tagging. Pairs with the version indicator (a `:testing`
  build reads as "development build"). - v0.4.11
- [x] **Repo split** (2026-09-01) - the monorepo became three repos so each deployable owns its own
  release list + versioning: **argus-core** (app), **[argus-probe](https://github.com/g-guglielmi/argus-probe)**
  (probe image + golden VM), **[argus-updater](https://github.com/g-guglielmi/argus-updater)** (self-update
  sidecar). Image names unchanged. History preserved via `git filter-repo`.

### H. Parking lot (maybe)
- [ ] Public status page (Uptime-Kuma-style shareable)
- [ ] Escalation policies / repeat notifications beyond flap debounce

### I. Mobile app (last step)
- [ ] **Android native app with push notifications** - the app registers a device with Argus; the notifier delivers alerts as **push** (e.g. FCM) via a new "push" notification channel type, alongside Discord/Telegram/email. A PWA + web push is a cheaper fallback if a full native app isn't warranted. - _(app + BE)_ **L**
- [ ] **iOS app** - _undecided_; would need APNs + an Apple developer account. Decide once the Android app exists.

---

## Suggested near-term order

Done: ~~deep-link URLs~~ ✅ · ~~password reset~~ ✅ (v0.3.3) · ~~probe enrollment~~ ✅ (v0.4.0) ·
~~probe fleet updates + self-update~~ ✅ (v0.4.8-0.4.9) · ~~session timeouts + landing page~~ ✅ (v0.4.7) ·
~~UI standardization / design system~~ ✅ (v0.4.10) ·
~~smaller-wins pass: global search + per-channel severity + self-update toggle + labeled axes~~ ✅ (v0.4.27).

Re-evaluated from here:

1. **The 1.0 lift - "replaces PRTG Add Sensor" (§C → §B → §D)** _(next)_ **:** build/verify the **device-class templates** (§C, the foundation), then **auto-discovery** (§B: UniFi sweep → fingerprint → LLD → "Discovered - review"), then the **device/threshold management UI** (§D). This is the core work that gets Argus to a production **1.0**. Start with §C phase **C1**: **Generic Linux SNMP + UniFi Switch** end-to-end (SNMP-first fleet; templates are hand-authored Zabbix YAML imported at bootstrap) so discovery and the threshold UI have a concrete shape to build against. See §C for the C0/C1/C2 breakdown.
2. **Scale & production readiness (§G)** - sizing pass + server-side census before the ~6000-sensor
   deployment.
3. **(last)** **Android native app** with push notifications (§I) - iOS TBD.

Blocked / deferred: **mygrz** probe (§A) - its building is under renovation, so it won't come online in the near term; bring it online once that's done.

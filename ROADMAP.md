# Argus - Roadmap

A living checklist of what's built and what's left. The **design** and rationale live in
[`docs/DESIGN.md`](docs/DESIGN.md); this file is the tracking view. Sizes are rough:
**S** ≈ hours, **M** ≈ a day or few, **L** ≈ a week+ / multi-part.

Legend: `[x]` done · `[~]` partly done · `[ ]` planned · _(FE)_ frontend-only · _(BE)_ backend · _(ops)_ infra/deploy.

---

## ✅ Shipped (through v0.4.10)

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
- [x] **4 of 5 site probes online** and self-reporting (site1, site2, site3, site5)
- [x] **UI standardization / design system** (v0.4.10): shared `ui.tsx` primitives (Button, Card, Field, Banner, Badge, CopyButton) backed by the CSS tokens; legacy inline-style objects and all hardcoded colors removed so the SPA themes correctly and pages are consistent
- [x] **Text readability** (v0.4.10): raised `--text`/`--muted`/`--faint` contrast in both themes to clear WCAG AA (faint 3.x -> 5-6:1)
- [x] **Version indicator** (v0.4.10): build-stamped running version + `GET /api/version`; core resolves the newest published release from GHCR and the sidebar footer shows a "latest" tick or an "update available" pill
- [x] **Local build toolchain + CI typecheck** (v0.4.10): Node/Go installed for local `tsc`/`vite`/`go build`; the web build now runs `tsc --noEmit` so type errors fail CI (previously `vite build` skipped type-checking)

---

## 🚧 Remaining

### A. Probe fleet & enrollment
- [x] **Token-based enrollment / PKI service** - mint token → probe self-generates key + CSR → core signs & registers proxy via Zabbix API; private key never leaves the probe - v0.4.0
- [x] **"Add probe" wizard** UI + self-enrolling `argus-probe` image - v0.4.0
- [~] Bring **site2-site5** probes online (Probes → Add probe) - **4/5 done** (site1, site2, site3, site5); **site4** blocked - its building is under renovation, so it won't come online in the near term - _(ops)_ S
- [x] **Probe fleet updates - control plane + opt-in self-update** - Argus holds a fleet target (`latest` or a `7.0.29-r1` pin) + shows each probe's version vs target; probes check in outbound; drift gets a one-click manual update; opt-in compose sidecar (`ARGUS_PROBE_ROLE=updater`) self-updates via the Docker socket. See DESIGN §18 - v0.4.8
- [x] **Dashboard-triggered self-update + exact-version reporting** - probes report their exact `X.Y.Z-rN` version over the check-in; a socket-enabled probe self-updates on demand via a short-lived `recreate` sister container (config-cloning, rollback on failure); "Enable reporting" mints a check-in token for older probes (persisted to the volume, one env var); redeploy-aware wizard command; snmptraps bound so no anonymous volume - v0.4.9
- [x] **Resolve "latest" from the registry for accurate drift** - Argus core polls GHCR anonymously (`ghcr.io/token` → `/v2/<owner>/argus-probe/tags/list`) every 3h, picks the newest `X.Y.Z-rN` tag, and compares to each probe's reported version, so a `latest` target flags "outdated → rN" instead of just "tracking". - v0.4.9
- [ ] **Add-probe wizard: self-update toggle** - an optional switch that adds `-v /var/run/docker.sock:…` + `ARGUS_PROBE_SELFUPDATE=1` to the generated command/XML, so socket-enabled probes deploy straight from the wizard (today you add the two flags by hand) - _(FE)_ S
- [ ] **Self-configuring probe VM** (VMware / Nutanix / XCP-NG / KVM) - Packer golden image (Debian cloud image + the `argus-probe` container) that self-enrolls via **cloud-init**; the Add-probe flow gains a third output generating the cloud-init user-data / a tiny **NoCloud seed ISO** carrying the enroll token. Distribute as **OVA** (VMware/Nutanix) + **qcow2/XVA** (XCP-NG/KVM). See DESIGN §14. - _(ops+image+FE)_ L

### B. Auto-provisioning / discovery (Phase 4 - "replaces PRTG Add Sensor")
- [ ] Per-site **UniFi API sweep** → inventory → Zabbix hosts, tagged, bound to proxy - _(BE)_ **L**
- [ ] **Capability fingerprint** (SNMP sysObjectID, HTTP(S), DNS :53, NUT :3493) - _(BE)_ M
- [ ] **Template attach** by fingerprint + **LLD** per-instance items (disks, NICs, ports, radios, VMs) - _(BE)_ M
- [ ] Default thresholds applied on discovery - _(BE)_ S
- [ ] **"Discovered - review"** screen (confirm / adjust / ignore) - _(FE)_ M

### C. Device classes & templates
- [ ] Build/verify the **11 class templates** + Base ping (UniFi gw/switch/AP, MikroTik, unRAID, XCP-NG, generic Linux, web/app, DNS, UPS, printer) - _(BE)_ **L**
- [ ] **SNMP-gap API integrations**: unRAID API (disk temp/SMART), XCP-NG XAPI (per-VM/host + temp), UniFi API (per-port/PoE/WAN) - _(BE)_ **L**

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
- [ ] **Global search** - top-bar quick-switcher by name/IP/tag, server-side (DESIGN §16) - _(FE+BE)_ M
- [ ] **Per-channel severity filter** - _(FE+BE)_ S
- [ ] **Labeled graph axes** in alert PNGs (needs a font dep) - _(BE)_ S

### G. Scale & production readiness
- [ ] **Sizing pass** before the ~6000-sensor deployment (proxies, DB, caches, NVPS) - analysis
- [ ] **Server-side census/counts** - move the `/api/sensors` full census server-side at scale - _(BE)_ M

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
~~UI standardization / design system~~ ✅ (v0.4.10).

Re-evaluated from here:

1. **Smaller-wins pass (§F / §A)** _(next)_ - now that the shared primitives exist, knock out the
   quick, self-contained items on top of them: **global search** (top-bar quick-switcher, §F),
   **per-channel severity filter** (§F), the **Add-probe self-update toggle** (§A), and
   **labeled graph axes** in alert PNGs (§F). Mostly **S**, high polish-per-effort.
2. **The 1.0 lift - "replaces PRTG Add Sensor" (§C → §B → §D):** build/verify the **device-class templates** (§C, the foundation), then **auto-discovery** (§B: UniFi sweep → fingerprint → LLD → "Discovered - review"), then the **device/threshold management UI** (§D). This is the core work that gets Argus to a production **1.0**.
3. **Scale & production readiness (§G)** - sizing pass + server-side census before the ~6000-sensor deployment.
4. **(last)** **Android native app** with push notifications (§I) - iOS TBD.

Blocked / deferred: **site4** probe (§A) - its building is under renovation, so it won't come online in the near term; bring it online once that's done.

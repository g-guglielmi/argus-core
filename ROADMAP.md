# Argus — Roadmap

A living checklist of what's built and what's left. The **design** and rationale live in
[`docs/DESIGN.md`](docs/DESIGN.md); this file is the tracking view. Sizes are rough:
**S** ≈ hours, **M** ≈ a day or few, **L** ≈ a week+ / multi-part.

Legend: `[x]` done · `[ ]` planned · _(FE)_ frontend-only · _(BE)_ backend · _(ops)_ infra/deploy.

---

## ✅ Shipped (through v0.3.0)

- [x] **Phase 0 — foundations**: Zabbix 7.0 core + site1 probe over mutual TLS, 7-day offline buffer
- [x] **Auth**: roles (admin/helpdesk/viewer), argon2id + sessions, TOTP + recovery codes, WebAuthn passkeys, admin user management, login rate-limiting
- [x] **Monitoring**: site→host→sensor tree, curated key sensors, per-sensor charts (2h–1Y, zoom), sparklines
- [x] **States**: acknowledge / pause / hide with durations, auto-expiry, host→sensor inheritance
- [x] **Overview**: cross-site problem list + six-state status chips
- [x] **Notifications**: engine + Discord/Telegram/email channels, rich messages, 2h trend graphs, one-click ack
- [x] **Mobile-responsive** layout
- [x] **Security**: AES-256-GCM at-rest encryption, brute-force protection
- [x] **Admin Settings** (v0.3.0): runtime Zabbix conn / public URL / timezone / login limits

---

## 🚧 Remaining

### A. Probe fleet & enrollment
- [x] **Token-based enrollment / PKI service** — mint token → probe self-generates key + CSR → core signs & registers proxy via Zabbix API; private key never leaves the probe — v0.4.0
- [x] **"Add probe" wizard** UI + self-enrolling `argus-probe` image — v0.4.0
- [ ] Bring **site2–site5** probes online (now: Probes → Add probe) — _(ops)_ S each
- [ ] **Probe fleet updates — control plane + opt-in self-update** (Argus sets target version + shows fleet; probe self-updates via mounted socket when enabled, else one-click command). Outbound-only means pull-based, not push. Build **after v0.4.0 is validated**. See DESIGN §18 — _(BE+FE+image)_ M
- [ ] **Self-configuring probe VM** (VMware / Nutanix / XCP-NG / KVM) — Packer golden image (Debian cloud image + the `argus-probe` container) that self-enrolls via **cloud-init**; the Add-probe flow gains a third output generating the cloud-init user-data / a tiny **NoCloud seed ISO** carrying the enroll token. Distribute as **OVA** (VMware/Nutanix) + **qcow2/XVA** (XCP-NG/KVM). See DESIGN §14. — _(ops+image+FE)_ L

### B. Auto-provisioning / discovery (Phase 4 — "replaces PRTG Add Sensor")
- [ ] Per-site **UniFi API sweep** → inventory → Zabbix hosts, tagged, bound to proxy — _(BE)_ **L**
- [ ] **Capability fingerprint** (SNMP sysObjectID, HTTP(S), DNS :53, NUT :3493) — _(BE)_ M
- [ ] **Template attach** by fingerprint + **LLD** per-instance items (disks, NICs, ports, radios, VMs) — _(BE)_ M
- [ ] Default thresholds applied on discovery — _(BE)_ S
- [ ] **"Discovered — review"** screen (confirm / adjust / ignore) — _(FE)_ M

### C. Device classes & templates
- [ ] Build/verify the **11 class templates** + Base ping (UniFi gw/switch/AP, MikroTik, unRAID, XCP-NG, generic Linux, web/app, DNS, UPS, printer) — _(BE)_ **L**
- [ ] **SNMP-gap API integrations**: unRAID API (disk temp/SMART), XCP-NG XAPI (per-VM/host + temp), UniFi API (per-port/PoE/WAN) — _(BE)_ **L**

### D. Management UI screens
- [ ] **Device management** — add/edit, assign site+proxy, class, per-device threshold overrides — _(FE+BE)_ M
- [ ] **Thresholds** — global defaults + per-device/sensor overrides — _(FE+BE)_ M
- [ ] **Settings expansion** — retention controls, proxy health, allowed-hosts — _(FE+BE)_ S–M

### E. Auth / account gaps
- [x] **Self-service email password reset** (single-use emailed link; reuses the email channel) — v0.3.3
- [ ] **Configurable session timeouts** _(next)_ — **max session lifetime** (default **12h**, replacing today's fixed 7-day absolute expiry) + optional **idle timeout** (sliding; **disabled by default**). Both admin-editable in **Settings** (env-overridable). Idle needs a per-session `last_seen` bumped by the auth middleware and re-checked on each request; max caps absolute lifetime. — _(BE+FE)_ M
- [ ] **Per-user landing page** preference (Overview vs Errors) — _(FE)_ S

### F. UX / quality-of-life
- [x] **Deep-link URLs / reload persistence** — reflect the view in the address bar (DESIGN §17) — _(FE)_
- [ ] **UI standardization / design system** — the SPA grew organically and the same widgets are
  built ad-hoc in different places (e.g. the theme toggle once existed in both Account and admin
  Settings with different markup; buttons/cards/form-rows vary between views). Pull shared,
  consistently-styled primitives (button, card, form-row, table, status pill) into one set and
  reuse them across Overview / Account / Settings / Probes so pages look and behave the same. — _(FE)_ M
- [ ] **Global search** — top-bar quick-switcher by name/IP/tag, server-side (DESIGN §16) — _(FE+BE)_ M
- [ ] **Per-channel severity filter** — _(FE+BE)_ S
- [ ] **Labeled graph axes** in alert PNGs (needs a font dep) — _(BE)_ S

### G. Scale & production readiness
- [ ] **Sizing pass** before the ~6000-sensor deployment (proxies, DB, caches, NVPS) — analysis
- [ ] **Server-side census/counts** — move the `/api/sensors` full census server-side at scale — _(BE)_ M

### H. Parking lot (maybe)
- [ ] Public status page (Uptime-Kuma-style shareable)
- [ ] Escalation policies / repeat notifications beyond flap debounce

### I. Mobile app (last step)
- [ ] **Android native app with push notifications** — the app registers a device with Argus; the notifier delivers alerts as **push** (e.g. FCM) via a new "push" notification channel type, alongside Discord/Telegram/email. A PWA + web push is a cheaper fallback if a full native app isn't warranted. — _(app + BE)_ **L**
- [ ] **iOS app** — _undecided_; would need APNs + an Apple developer account. Decide once the Android app exists.

---

## Suggested near-term order

1. ~~Deep-link URLs~~ ✅ (done) — small, felt every day
2. ~~Self-service password reset~~ ✅ (done, v0.3.3)
3. ~~Probe enrollment~~ ✅ (done, v0.4.0) — now bring site2–5 online from the GUI
4. **Configurable session timeouts** (max 12h + optional idle) — hardening ahead of internet exposure (§E)
5. Then the big lift: **discovery + device templates** (B/C) → the real path to a production **1.0**.
6. **(last)** **Android native app** with push notifications (§I) — iOS TBD.

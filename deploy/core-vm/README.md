# Argus core appliance golden image (`deploy/core-vm/`)

A self-installing **Debian 13 VM** that carries the entire monitoring core - **Zabbix 7.0** (server +
nginx frontend + agent2), **PostgreSQL + TimescaleDB**, and the **`argus`** + **`argus-updater`**
containers - and configures all of it on first boot through **one setup form**. Implements DESIGN
**§14d**; it is the core-side sibling of the probe golden image (argus-probe repo, `deploy/probe-vm/`)
and reuses its build pattern wholesale.

Import the disk, boot it on a DHCP network, browse to `http://<vm-ip>/`, fill in one page
(hostname · keyboard · timezone · admin email + password), and watch the live progress until
**"Your monitoring core is ready"**. No Zabbix frontend wizard, no token copy-pasting, no SQL.

The **manual install path is still fully supported** (any apt distro, or Argus on a separate VM from
Zabbix) - see the repo README's *Option B* and [`deploy/README.md`](../README.md). Both paths install
the identical stack: the appliance image is built with the same
[`setup-core.sh`](../core/setup-core.sh) (in `SETUP_MODE=image`).

## What's here

| File | Purpose |
|------|---------|
| `argus-core-vm.pkr.hcl` | Packer template - builds the golden image (qcow2). |
| `build-seed/` | Build-only cloud-init seed so Packer can SSH into the base cloud image. |
| `scripts/provision.sh` | Bakes the stack into the image: `setup-core.sh SETUP_MODE=image` (Zabbix + PG/Timescale + OS patching), Docker, pre-pulled `argus`/`argus-updater` images, the units below. |
| `scripts/make-ova.sh` | Packages the built qcow2 into an OVA (stream-optimized VMDK + OVF + manifest). |
| `files/argus-core.service` | Runs the `argus` container from `/etc/argus-core/argus.env` (detached, Docker-restarted, updater-recreatable). |
| `files/argus-updater.service` | The socket-holding update sidecar in its core (file-channel) mode - one-click "Update now" from Argus Settings. |
| `files/argus-firstboot.service` + `argus-core-firstboot.py` | The first-boot setup: one-form page on `http://<vm>/`, then a live progress page while it configures the whole core. Disables itself when done. |
| `files/argus-hostkeys.service` | Regenerates SSH host keys on first boot (they're stripped from the golden image). |
| `files/10-argus-dhcp.network` | systemd-networkd DHCP for the primary NIC (cloud-init is purged from the image). |
| `files/argus.env.example` | Reference for the `/etc/argus-core/argus.env` + `image.env` contract the setup writes. |

## What first boot actually does

The setup page collects: **hostname**, **console keyboard layout**, **timezone**, and one
**administrator email + password** (under *Advanced*: a separate password per role, the Linux
username, and the Public URL). Then, step by step, with live progress and ground-truth checks:

1. **System** - hostname, timezone, console keymap, and the **local Debian sudo user** (default
   `argus`, in `sudo` + `docker` groups) with the password you chose - your console/SSH access.
2. **Database** - `timescaledb-tune` against *this* VM's RAM, then the `zabbix` role + database with a
   **generated password** (root-only in the configs, never shown), the Zabbix schema import (the long
   step), and the TimescaleDB conversion.
3. **PKI** - a private CA (`CN=Monitoring Core CA`, same convention as `deploy/pki/gen-certs.sh`) +
   the core server cert. The CA is mounted read-only into Argus, so **probe enrollment works out of
   the box**; Zabbix gets its mTLS server cert from day one.
4. **Zabbix** - `zabbix_server.conf` (DB + the TLS/tuning snippet), the frontend `zabbix.conf.php`
   (what the browser wizard would have written - so the wizard never runs), nginx on **:8080**,
   php-fpm timezone, agent2 self-monitoring, services enabled.
5. **Accounts** - signs in with the stock `Admin`/`zabbix`, **rotates the Admin password** to yours,
   creates the **`argus-svc`** super-admin machine account and mints its **API token** (Argus talks
   through this token; a human never sees or handles it - and rotating `Admin` later never breaks
   Argus), and sets housekeeping retention (history 30d, trends 2y, compression after 7d).
6. **Argus** - writes `/etc/argus-core/argus.env` (token, first-admin seed, generated
   `ARGUS_SECRET_KEY`, CA + update-dir mounts), starts the **`argus`** + **`argus-updater`**
   containers, waits for health, signs in as your admin and seeds **Public URL + timezone** through
   the settings API (so they stay editable in the UI - not env-locked).
7. **Finish** - scrubs the one-time `ARGUS_ADMIN_PASSWORD` from `argus.env`, parks a
   **`:80 → :8081` redirect** on nginx (the setup page retires and `http://<vm>/` lands on Argus
   from then on), and disables the first-boot service.

Every step is **idempotent**: a failure shows red with the real error and offers **Retry** (same
answers, resumes at the failed step) or **Change the answers**; a reboot mid-setup resumes
automatically. The submitted answers are kept root-only under `/var/lib/argus-core-setup/` and
scrubbed of secrets once setup completes.

### The three credentials, and where they live

| Account | Created by | Password |
|---|---|---|
| Debian user (default `argus`) | step 1 | yours (Advanced: separate) - console + SSH |
| Zabbix `Admin` | step 5 (rotated from the default) | yours (Advanced: separate) - the Zabbix UI on `:8080` |
| Argus admin (your email) | step 6 (first-run seed) | yours (Advanced: separate) - Argus on `:8081` |

Machine-generated and never displayed: the **database password** (in `zabbix_server.conf` /
`zabbix.conf.php`, root-only), the **`argus-svc` API token** (in `argus.env`, root-only), and the
**at-rest encryption key**. Rotate any human password later without touching the others.

## Design notes

- **Same base and build as the probe image**: official Debian 13 **`generic`** cloud qcow2 (full
  driver set - the OVA must boot non-virtio hypervisors), Packer under TCG (runner-friendly),
  identity + build user stripped in the shutdown step, **cloud-init purged** from the deployed image
  (systemd-networkd DHCPs; the first-boot service does all configuration).
- **Native Zabbix, containerized Argus** - the exact layout the manual path installs and the one the
  reference deployment runs. Zabbix/PostgreSQL stay apt packages (patched-but-pinned, TimescaleDB
  held at 2.28 for Zabbix 7.0); Argus + updater stay containers with the existing one-click
  self-update flow.
- **OS patching, core flavor** (DESIGN §14c): security-only unattended-upgrades, **no auto-reboot** -
  the reboot is operator-scheduled from Argus **Settings → OS updates**; the host reporter and
  reboot-window watcher are baked in via `setup-core.sh`.
- **Ports**: Argus on **:8081**, Zabbix UI/API on **:8080**, proxies inbound on **:10051**, and
  **:80** serves the setup page first, then a permanent redirect to Argus.
- The Argus container reaches the host's Zabbix frontend as `host.docker.internal` (mapped to the
  Docker bridge gateway) - stable across DHCP address changes.

## Building

CI (`.github/workflows/core-vm.yml`) builds it - `packer validate` on every push, and a full build on
**workflow_dispatch** or a **`core-vm/v*`** tag (which also publishes a GitHub Release with the image
assets). Outputs: `argus-core-vm.qcow2`, `argus-core-vm.vhd` (gzipped in the Release), and
`argus-core-vm.ova`. The disk is **100 GB thin-provisioned** (the shipped files stay small; history
grows into it).

Locally (needs Packer + QEMU):

```bash
cd deploy/core-vm
packer init argus-core-vm.pkr.hcl
packer build argus-core-vm.pkr.hcl        # -> output/argus-core-vm.qcow2
```

## Deploying

1. **Import the disk** - `argus-core-vm.ova` (VMware / Nutanix / VirtualBox / Xen Orchestra
   **Import → OVA**), `argus-core-vm.qcow2` (KVM/libvirt), or `argus-core-vm.vhd` (XCP-NG VDI import /
   Hyper-V; gunzip first). Give it **2+ vCPU and 4+ GB RAM** (more for bigger fleets, §14b).
2. **Boot it on a network with DHCP** and find its address (your hypervisor console shows it, or your
   DHCP leases). First boot needs DHCP - see *Scope* below for static addressing.
3. **Browse to `http://<vm-ip>/`**, fill in the form, and wait for *"Your monitoring core is ready"*
   (a few minutes; the schema import is the long step).
4. Sign in to **Argus at `http://<vm-ip>:8081/`**, add your first probe (**Probes → Add probe** - the
   enrollment PKI already works), and take a **hypervisor snapshot**.

Afterwards, `http://<vm-ip>/` redirects to Argus. The Zabbix UI stays available on `:8080`
(user `Admin`) for engine-room work.

**Fronting it with HTTPS** (recommended before any internet exposure): point your reverse proxy
(HAProxy/nginx/Caddy) at `:8081`, then update **Settings → Public URL** and add
`ARGUS_COOKIE_SECURE=true` / `ARGUS_TRUST_PROXY=true` (and `ARGUS_RP_*` for passkeys) to
`/etc/argus-core/argus.env` + `systemctl restart argus-core`. Remote probes additionally need
**:10051** published/forwarded to the VM.

## Scope / notes

- **First boot needs DHCP** (the chicken-and-egg is real: you reach the setup page by IP). To move to
  a static address afterwards, replace `/etc/systemd/network/10-argus-dhcp.network` with a static
  `.network` file (template in the DHCP file's comments) - or just give the VM a DHCP reservation.
- **Zabbix / PostgreSQL package upgrades stay deliberate** - `apt upgrade` on the VM when *you*
  choose (security patches are automatic; TimescaleDB is pinned to 2.28 for Zabbix 7.0). A major
  Debian upgrade is a re-image event, as with the probe VM.
- **Argus + updater track `:latest`** by default; pin tags in `/etc/argus-core/image.env` if you want
  a reboot never to move versions. In-app updates (Settings → About) work from day one via the baked
  updater sidecar.
- The setup form travels over plain HTTP on your LAN, once, like the probe's first-boot page - do the
  setup from the network you trust.
- Refresh the golden image periodically (quarterly / on a Debian point release) so new deployments
  ship already-patched.

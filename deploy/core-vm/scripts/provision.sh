#!/usr/bin/env bash
# Provision the Argus CORE appliance golden image on top of the stock Debian cloud image: install the
# full core stack (Zabbix 7.0 server + nginx frontend + PostgreSQL/TimescaleDB via setup-core.sh in
# image mode), Docker + the argus / argus-updater images, and the appliance systemd units + first-boot
# setup service - then leave the VM UNCONFIGURED. Everything instance-specific (users, passwords,
# database, certificates) happens on first boot through the setup page (argus-core-firstboot.py).
# Identity is stripped in the Packer shutdown_command, not here, so the live build SSH session isn't
# cut off.
set -euo pipefail

echo "==> waiting for cloud-init to finish its build-seed run"
cloud-init status --wait || true

export DEBIAN_FRONTEND=noninteractive
echo "==> installing base packages"
apt-get update
# systemd-resolved: DHCP DNS under networkd. kbd: console keymaps + loadkeys (configurable keyboard
# layout). sudo + openssh-server: the local admin user created by the setup page. openssl: the
# first-boot PKI (CA + core server cert). python3 runs the first-boot service itself.
apt-get install -y --no-install-recommends ca-certificates curl python3 systemd-resolved kbd sudo openssh-server openssl

echo "==> installing the Zabbix + PostgreSQL/TimescaleDB stack (setup-core.sh, image mode)"
# The SAME script the manual install path runs - image mode does repos + packages + the TimescaleDB
# 2.28 pin + OS patching (DESIGN §14c, core flavor: security-only, NO auto-reboot, os-report/reboot
# watcher into /opt/argus/update), and skips the per-instance DB/config phases (first boot does those).
chmod +x /tmp/setup-core.sh
SETUP_MODE=image bash /tmp/setup-core.sh

echo "==> disabling the Zabbix stack until first boot configures it"
# zabbix-server has no database yet (it would crash-loop), and nginx's Debian default site binds :80,
# which the first-boot setup page needs. The first-boot service enables everything once configured.
systemctl disable --now zabbix-server zabbix-agent2 nginx 2>/dev/null || true
rm -f /etc/nginx/sites-enabled/default

echo "==> installing Docker Engine"
# Official convenience script: adds Docker's apt repo and installs docker-ce. Pinned enough for an
# appliance; the containers carry the application logic.
curl -fsSL https://get.docker.com | sh
systemctl enable docker

echo "==> installing argus-core appliance units and files"
FILES=/tmp/files
install -D -m 0644 "$FILES/argus-core.service"          /etc/systemd/system/argus-core.service
install -D -m 0644 "$FILES/argus-updater.service"       /etc/systemd/system/argus-updater.service
install -D -m 0644 "$FILES/argus-firstboot.service"     /etc/systemd/system/argus-firstboot.service
install -D -m 0644 "$FILES/argus-hostkeys.service"      /etc/systemd/system/argus-hostkeys.service
install -D -m 0755 "$FILES/argus-core-firstboot.py"     /usr/local/bin/argus-core-firstboot.py
install -D -m 0600 "$FILES/argus.env.example"           /etc/argus-core/argus.env.example
# The TLS + tuning lines first boot appends to zabbix_server.conf once the PKI exists.
install -D -m 0644 /tmp/zabbix_server.conf.snippet      /etc/argus-core/zabbix_server.conf.snippet
# First-boot setup state (config + step markers; scrubbed of secrets once setup completes).
install -d -m 0700 /var/lib/argus-core-setup
# The Argus data volume (/data in the container) and the CA dir (/ca, read-only in the container).
# The core runs as uid 65532 (distroless nonroot), so it must own what it writes.
install -d -m 0755 /var/lib/argus-core
chown 65532:65532 /var/lib/argus-core
install -d -m 0700 /etc/argus/pki
# setup-core.sh created the shared self-update dir (/opt/argus/update); hand it to the core's uid at
# IMAGE BUILD time only. Never chown/chmod an EXISTING deployment's dir - create-only rule (the core
# container writes its update request.json here and owns the dir from first boot on).
chown 65532:65532 /opt/argus/update

# Networking: systemd-networkd DHCPs the primary NIC. cloud-init is purged below, so networkd is the
# sole network manager - no datasource dependency, no fight over the interface.
install -D -m 0644 "$FILES/10-argus-dhcp.network"       /etc/systemd/network/10-argus-dhcp.network
systemctl enable systemd-networkd.service systemd-resolved.service

# The core + updater units are installed but NOT enabled - the first-boot setup enables them once
# argus.env carries the Zabbix token, so an unconfigured VM never crash-loops. The first-boot service
# IS enabled (it serves the setup page), as is the SSH host-key regen oneshot.
systemctl enable argus-firstboot.service
systemctl enable argus-hostkeys.service

echo "==> pre-pulling the core images (best-effort, so first boot doesn't wait on big pulls)"
docker pull ghcr.io/g-guglielmi/argus:latest || true
docker pull ghcr.io/g-guglielmi/argus-updater:latest || true

# Point resolv.conf at resolved's stub (done last: before this, the build's own DNS must keep working
# for apt/docker; on the deployed VM systemd-resolved runs and populates the stub from DHCP).
ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf

# Neutral hostname for the shipped image (the build seed named the VM argus-core-build, which leaked
# into the setup page's footer before setup ran). First boot sets the operator's chosen name.
echo argus-core > /etc/hostname

# Drop cloud-init entirely. It has finished its build-time job (it created the packer user and grew
# the root filesystem to fill the disk on the build's first boot); the deployed appliance uses
# systemd-networkd for networking and the first-boot setup service for configuration, so cloud-init
# is only a flaky, confusing extra on the no-datasource path. Purge it and its state so no clone ever
# runs it. (Because it's gone, the Packer shutdown step no longer runs `cloud-init clean`.)
echo "==> removing cloud-init (the appliance self-configures without it)"
apt-get purge -y cloud-init || true
rm -rf /etc/cloud /var/lib/cloud

echo "==> trimming build artifacts"
apt-get autoremove -y || true
apt-get clean
rm -rf "$FILES" /tmp/setup-core.sh /tmp/zabbix_server.conf.snippet /tmp/externalscripts /var/lib/apt/lists/*
echo "==> provision complete"

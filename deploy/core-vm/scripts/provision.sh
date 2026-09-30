#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

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
apt-get install -y --no-install-recommends ca-certificates curl gnupg python3 systemd-resolved kbd sudo openssh-server openssl

echo "==> installing the Zabbix + PostgreSQL/TimescaleDB stack (setup-core.sh, image mode)"
# The SAME script the manual install path runs - image mode does repos + packages + the TimescaleDB
# 2.28 pin + OS patching (DESIGN §14c, core flavor: security-only, NO auto-reboot, os-report/reboot
# watcher into /docker/argus-update), and skips the per-instance DB/config phases (first boot does those).
chmod +x /tmp/setup-core.sh
SETUP_MODE=image bash /tmp/setup-core.sh

echo "==> disabling the Zabbix stack until first boot configures it"
# zabbix-server has no database yet (it would crash-loop), and nginx's Debian default site binds :80,
# which the first-boot setup page needs. The first-boot service enables everything once configured.
systemctl disable --now zabbix-server zabbix-agent2 nginx 2>/dev/null || true
rm -f /etc/nginx/sites-enabled/default

echo "==> installing Docker Engine (Docker's apt repository, signing key pinned by fingerprint)"
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
DOCKER_FPR=$(gpg --show-keys --with-colons /etc/apt/keyrings/docker.asc | awk -F: '/^fpr/{print $10; exit}')
if [ "$DOCKER_FPR" != "9DC858229FC7DD38854AE2D88D81803C0EBFCD88" ]; then
  echo "Docker apt signing key fingerprint mismatch: $DOCKER_FPR" >&2
  exit 1
fi
chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  > /etc/apt/sources.list.d/docker.list
apt-get update
apt-get install -y --no-install-recommends docker-ce docker-ce-cli containerd.io
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
# The VM's own https certificate for Argus (written by the first-boot "https" step).
install -d -m 0700 /etc/nginx/argus

echo "==> hardening SSH (no root login; password login only for the account first boot creates)"
# Password login is off for every account (keys still work); first boot adds a Match block for the
# administrator it creates, so that one password works over SSH when the console isn't at hand.
install -d -m 0755 /etc/ssh/sshd_config.d
printf 'PasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin no\n' > /etc/ssh/sshd_config.d/10-argus.conf
# The container folders follow /docker/<container name> (docs/folder-layout.md). The Argus folder is
# /data in the container, and the core runs as uid 65532 (distroless nonroot), so it owns it. The CA
# folder inside it is root's, readable by the core's group only (first boot fills it).
install -d -m 0755 /docker
install -d -m 0755 /docker/argus
chown 65532:65532 /docker/argus
install -d -m 0750 /docker/argus/pki
chown root:65532 /docker/argus/pki
# setup-core.sh created the shared self-update dir (/docker/argus-update); hand it to the core's uid at
# IMAGE BUILD time only. Never chown/chmod an EXISTING deployment's dir - create-only rule (the core
# container writes its update request.json here and owns the dir from first boot on).
chown 65532:65532 /docker/argus-update

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

#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# setup-core-patching.sh - install ONLY the OS patching & lifecycle piece (DESIGN §14c) on an
# already-running core VM, without re-running the full Zabbix/PostgreSQL installer.
#
# Keeps the core's Debian OS patched with unattended-upgrades (SECURITY suite only; it honours apt
# holds/pins, so the TimescaleDB 2.28 hold is safe) + needrestart (auto-restart services after a
# libc/openssl bump, so most updates need no reboot). The core is a "pet": patches auto-apply, but the
# REBOOT is operator-scheduled from Argus (the Updates page), never unattended.
#
# It also installs a host reporter (posts the core's status to Argus), a reboot watcher (honours the
# window Argus picks, or "Reboot now"), a patcher ("Patch now"), and a Zabbix minor-update watcher (applies same-major zabbix-* updates in an
# operator-scheduled window, or right away when Argus requests one with "Update now"; major upgrades
# stay manual). Patching stays local - Argus never runs apt remotely: it drops a request file, the
# host applies it.
#
# Usage:  sudo ARGUS_STATE_DIR=/docker/argus-update ./setup-core-patching.sh
#   ARGUS_STATE_DIR is the HOST path you bind-mount into the Argus core container as ARGUS_UPDATE_DIR
#   (find it with:  docker inspect argus --format '{{range .Mounts}}{{.Source}} -> {{.Destination}}{{"\n"}}{{end}}'
#   and take the Source whose Destination is /update). Defaults to /docker/argus-update (docs/folder-layout.md).
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "!! run as root (sudo)."; exit 1
fi

ARGUS_STATE_DIR="${ARGUS_STATE_DIR:-/docker/argus-update}"
export DEBIAN_FRONTEND=noninteractive

echo "==> installing unattended-upgrades + needrestart"
apt-get update
apt-get install -y unattended-upgrades needrestart

# Security-only, respect holds/pins, and DON'T reboot unattended (Argus schedules the core reboot).
cat > /etc/apt/apt.conf.d/52argus-unattended <<UAU
Unattended-Upgrade::Origins-Pattern {
        "origin=Debian,codename=\${distro_codename}-security,label=Debian-Security";
        "origin=Ubuntu,archive=\${distro_codename}-security,label=Ubuntu";
};
Unattended-Upgrade::Automatic-Reboot "false";
Unattended-Upgrade::MinimalSteps "true";
UAU
cat > /etc/apt/apt.conf.d/20auto-upgrades <<'AU'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
AU
# needrestart: auto-restart outdated services (no interactive prompt) so patches apply without a reboot.
mkdir -p /etc/needrestart/conf.d
echo "\$nrconf{restart} = 'a';" > /etc/needrestart/conf.d/99argus.conf

echo "==> installing the host scripts and their units (state dir: ${ARGUS_STATE_DIR})"
# One installer, from the files in host/ next to this script and its manifest: the reporter, the
# reboot, patch and Zabbix watchers, the timezone check, the sizing tool, the backup tools' units.
# After this install the Argus image carries the same files and the argus-updater writes them to the
# host after each core update (DESIGN 14c): this is the last time they are installed by hand.
PATCH_DIR="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
ARGUS_STATE_DIR="$ARGUS_STATE_DIR" bash "${PATCH_DIR}/host/install-host-tools.sh"

# The backup tools' packages and archive folder (DESIGN section 14e); their scripts and units came
# with the manifest above.
if [[ -f "${PATCH_DIR}/host/install-backup.sh" ]]; then
  echo "==> backup tools"
  ARGUS_STATE_DIR="$ARGUS_STATE_DIR" bash "${PATCH_DIR}/host/install-backup.sh"
fi

echo
echo "==> done. unattended-upgrades (security only, no auto-reboot) + the host scripts and units installed;"
echo "    from now on the argus-updater keeps the host scripts current after each core update."
echo "    status file: ${ARGUS_STATE_DIR}/os-status.json  (Argus reads it at ARGUS_UPDATE_DIR=/update)"
echo "    In Argus -> Updates you should now see the core's status; set the reboot"
echo "    window and the Zabbix minor-update window there."
echo "    Check the timers:  systemctl list-timers 'argus-*'"
echo "    Backups: Settings -> Backups in Argus (restore guide: docs/backup-and-restore.md)."

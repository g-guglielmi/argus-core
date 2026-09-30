#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# install-backup.sh - install the Argus core backup tools (DESIGN section 14e) on a core host:
# argus-backup and argus-restore, their systemd units, the local archive folder, and the tools the
# remote targets use (gpg, rsync, the SMB client, rclone for S3; the NFS client is installed the
# first time an NFS target is used, since it brings rpcbind along).
#
# setup-core.sh and setup-core-patching.sh run it. On an existing core, copy this folder to the VM and
# run it:  sudo ./install-backup.sh
# ARGUS_STATE_DIR is the host folder shared with the Argus container as /update (default
# /opt/argus/update, the appliance's).
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "!! run as root (sudo)."; exit 1
fi
HERE="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
ARGUS_STATE_DIR="${ARGUS_STATE_DIR:-/opt/argus/update}"
if [[ ! "$ARGUS_STATE_DIR" =~ ^/[A-Za-z0-9/._-]+$ ]]; then
  echo "!! ARGUS_STATE_DIR must be an absolute path of letters, digits and / . _ -"; exit 1
fi
export DEBIAN_FRONTEND=noninteractive

echo "==> backup tools (gpg, rsync, SMB client, rclone for S3)"
apt-get update -qq || true
apt-get install -y --no-install-recommends gnupg rsync cifs-utils rclone python3 || \
  echo "   (!) some packages did not install; argus-backup installs what a target needs when it first uses it"

install -m 0755 "$HERE/argus-backup" /usr/local/sbin/argus-backup
install -m 0755 "$HERE/argus-restore" /usr/local/sbin/argus-restore
for u in argus-backup.service argus-backup.timer argus-backup.path; do
  sed "s|@STATE_DIR@|${ARGUS_STATE_DIR}|g" "$HERE/$u" > "/etc/systemd/system/$u"
  chmod 0644 "/etc/systemd/system/$u"
done
install -d -m 0700 /var/backups/argus
systemctl daemon-reload
systemctl enable --now argus-backup.timer argus-backup.path
echo "    argus-backup + argus-restore installed; archives go to /var/backups/argus (root only)."
echo "    Turn backups on in Argus: Settings -> Backups. Restoring: docs/backup-and-restore.md."

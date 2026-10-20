#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# install-host-tools.sh - install the Argus core's host scripts and their systemd units from the files
# in this folder, as the manifest says (DESIGN 14c): the OS reporter, the reboot, patch and Zabbix
# minor-update watchers, the timezone check, the sizing tool, the backup tools and every unit.
# setup-core.sh and setup-core-patching.sh run it. After that first install the Argus image carries
# the same files and the argus-updater writes them to the host after each core update
# (`argus install-host-tools`), with argus-host-tools.path reloading systemd: nothing here is ever
# re-run by hand again.
#
#   sudo ARGUS_STATE_DIR=/docker/argus-update ./install-host-tools.sh
# ARGUS_STATE_DIR is the host folder shared with the Argus container as its update dir (default
# /docker/argus-update, docs/folder-layout.md). ARGUS_HOST_ROOT prefixes every destination and skips
# systemd (the tests).
set -euo pipefail
ROOT="${ARGUS_HOST_ROOT:-}"
if [[ $EUID -ne 0 && -z "$ROOT" ]]; then
  echo "!! run as root (sudo)."; exit 1
fi
HERE="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
ARGUS_STATE_DIR="${ARGUS_STATE_DIR:-/docker/argus-update}"
if [[ ! "$ARGUS_STATE_DIR" =~ ^/[A-Za-z0-9/._-]+$ ]]; then
  echo "!! ARGUS_STATE_DIR must be an absolute path of letters, digits and / . _ -"; exit 1
fi
# Only create the shared dir if missing: NEVER reset an existing dir's owner or mode (the core
# container writes its self-update request.json there and may own it as a non-root UID).
[[ -d "$ROOT$ARGUS_STATE_DIR" ]] || install -d -m 0755 "$ROOT$ARGUS_STATE_DIR"
enable=()
while read -r src dst mode flags; do
  [[ -z "$src" || "$src" == \#* ]] && continue
  target="$ROOT$dst"
  install -d -m 0755 "$(dirname "$target")"
  if [[ " $flags " == *" render "* ]]; then
    sed "s|@STATE_DIR@|${ARGUS_STATE_DIR}|g" "$HERE/$src" > "$target.argus-new"
  else
    cp "$HERE/$src" "$target.argus-new"
  fi
  chmod "$mode" "$target.argus-new"
  mv -f "$target.argus-new" "$target"
  if [[ " $flags " == *" enable "* ]]; then enable+=("$(basename "$dst")"); fi
done < "$HERE/manifest"
if [[ -z "$ROOT" ]]; then
  systemctl daemon-reload
  systemctl enable --now "${enable[@]}"
  /usr/local/sbin/argus-os-report || true
  echo "    host scripts and units installed (state dir ${ARGUS_STATE_DIR}); the argus-updater keeps them current from now on"
else
  echo "would enable: ${enable[*]}"
fi

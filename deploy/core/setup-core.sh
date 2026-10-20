#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# setup-core.sh - install & configure the monitoring core on a Debian 12/13 / Ubuntu 24.04 VM:
#   Zabbix 7.0 LTS (server + nginx frontend, which serves the JSON-RPC API)
#   PostgreSQL + TimescaleDB (Zabbix's history/trends store = your time-series DB)
#
# Review before running. Assumes a fresh VM and root/sudo. Idempotent-ish but re-runs
# may complain on already-created objects - that's fine.
#
# Two modes (so the manual path and the core appliance image install the SAME stack):
#   SETUP_MODE=full  (default) - install + configure: repos, packages, DB create + schema import,
#                    zabbix_server.conf, OS patching. Needs DBPASS. The manual-install path.
#   SETUP_MODE=image - install ONLY: repos, packages, TimescaleDB pin, OS patching. No DBPASS, no
#                    database, no config - the core appliance's first-boot service does those
#                    per-instance (deploy/core-vm/files/argus-core-firstboot.py). Used by Packer.
#
# Usage:  read -rs -p 'DB password: ' DBPASS; echo; export DBPASS; sudo -E ./setup-core.sh; unset DBPASS
#         (the password is read from the terminal: never on a command line, in ps or in the history)
#         sudo SETUP_MODE=image ./setup-core.sh
set -euo pipefail

SETUP_MODE="${SETUP_MODE:-full}"
# Directory this script lives in - its externalscripts/ sibling ships the external-check collectors.
SCRIPT_DIR="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
if [[ "$SETUP_MODE" == "full" ]]; then
  : "${DBPASS:?Set DBPASS to the Zabbix DB password: read -rs DBPASS; export DBPASS; sudo -E ./setup-core.sh}"
fi
ZBX_MAJOR="7.0"                                   # LTS; bump if a newer LTS is out
CODENAME="$(. /etc/os-release && echo "${VERSION_CODENAME:-trixie}")"   # Debian 13 = trixie
DISTRO_ID="$(. /etc/os-release && echo "$ID")"   # debian | ubuntu
PG_VER="17"                                       # Debian 13 ships PostgreSQL 17
OS_VER_NUM="$(. /etc/os-release && echo "${VERSION_ID%%.*}")"   # e.g. 13 for Debian 13

# Confirmed for Debian 13 / Zabbix 7.0 LTS (per Zabbix's own installer page). Override if needed:
#   ZBX_RELEASE_DEB='https://.../zabbix-release_latest_7.0+debian13_all.deb' ./setup-core.sh
ZBX_RELEASE_DEB="${ZBX_RELEASE_DEB:-https://repo.zabbix.com/zabbix/${ZBX_MAJOR}/${DISTRO_ID}/pool/main/z/zabbix-release/zabbix-release_latest_${ZBX_MAJOR}+${DISTRO_ID}${OS_VER_NUM}_all.deb}"

echo "==> [1/8] Zabbix ${ZBX_MAJOR} repo (${DISTRO_ID}/${CODENAME})"
echo "    using ${ZBX_RELEASE_DEB}"
tmp="$(mktemp -d)"; pushd "$tmp" >/dev/null
wget -q --https-only "$ZBX_RELEASE_DEB" -O zbx.deb || {
  echo "!! Could not fetch the zabbix-release deb. Confirm the URL for ${DISTRO_ID} ${CODENAME}"
  echo "   at https://repo.zabbix.com/zabbix/${ZBX_MAJOR}/release/${DISTRO_ID}/ and re-run with ZBX_RELEASE_DEB=..."
  exit 1
}
dpkg -i zbx.deb
popd >/dev/null

# NOTE: if Zabbix 7.0 packages are not yet published for Debian 13, either use the newer
# Zabbix LTS that is, or run the core on Debian 12 until 7.0/trixie packages land.

echo "==> [2/8] TimescaleDB repo"
apt-get install -y gnupg postgresql-common apt-transport-https lsb-release wget

# The release package only drops a sources file and the repository keyring. Before apt trusts
# that keyring, make sure it holds Zabbix's published signing keys and nothing else (the .deb
# itself is not signed; its URL is a rolling "latest", so a checksum can't be pinned).
ZBX_KEYRING="/usr/share/keyrings/zabbix.gpg"
ZBX_KEY_FPRS="4C3D6F2CC75F5146754FC374D913219AB5333005 A1848F5352D022B9471D83D0082AB56BA14FE591 84162C39F000750DDAEB816305A7FAEC227618D8"
[[ -s "$ZBX_KEYRING" ]] || { echo "!! ${ZBX_KEYRING} missing after installing the release package"; exit 1; }
zbx_fprs="$(gpg --show-keys --with-colons "$ZBX_KEYRING" 2>/dev/null | awk -F: '$1=="pub"{p=1;next} p&&$1=="fpr"{print $10;p=0}')"
[[ -n "$zbx_fprs" ]] || { echo "!! could not read ${ZBX_KEYRING}"; exit 1; }
for fpr in $zbx_fprs; do
  case " $ZBX_KEY_FPRS " in
    *" $fpr "*) ;;
    *) echo "!! unexpected signing key in ${ZBX_KEYRING}: ${fpr}"; exit 1 ;;
  esac
done

echo "deb https://packagecloud.io/timescale/timescaledb/${DISTRO_ID}/ ${CODENAME} main" \
  > /etc/apt/sources.list.d/timescaledb.list
# Same for TimescaleDB's key: pinned to the fingerprint packagecloud publishes for the repository.
TS_KEY_FPR="1005FB68604CE9B8F6879CF759F18EDF47F24417"
wget --quiet -O "$tmp/timescaledb.gpgkey" https://packagecloud.io/timescale/timescaledb/gpgkey
ts_fpr="$(gpg --show-keys --with-colons "$tmp/timescaledb.gpgkey" 2>/dev/null | awk -F: '$1=="fpr"{print $10; exit}')"
[[ "$ts_fpr" == "$TS_KEY_FPR" ]] || { echo "!! TimescaleDB signing key fingerprint mismatch: ${ts_fpr:-none}"; exit 1; }
gpg --dearmor -o /etc/apt/trusted.gpg.d/timescaledb.gpg < "$tmp/timescaledb.gpgkey"
apt-get update

echo "==> [3/8] Install packages"
apt-get install -y \
  zabbix-server-pgsql zabbix-frontend-php php8.4-pgsql zabbix-nginx-conf zabbix-sql-scripts zabbix-agent2 \
  "postgresql-${PG_VER}" \
  python3 openssh-client sshpass

# TimescaleDB: Zabbix 7.0 supports up to 2.28. The repo's "latest" is usually newer (e.g. 2.29),
# which makes Zabbix refuse to manage native compression. Pin the newest 2.28.x we can find.
TS_META="timescaledb-2-postgresql-${PG_VER}"
TS_LOADER="timescaledb-2-loader-postgresql-${PG_VER}"
TS_VERSION="${TS_VERSION:-$(apt-cache madison "$TS_META" 2>/dev/null | awk '{print $3}' | grep -E '^2\.28' | head -1)}"
if [[ -n "$TS_VERSION" ]]; then
  echo "    pinning TimescaleDB ${TS_VERSION} (Zabbix-supported)"
  apt-get install -y --allow-downgrades "${TS_META}=${TS_VERSION}" "${TS_LOADER}=${TS_VERSION}"
  apt-mark hold "$TS_META" "$TS_LOADER"
  # Defense in depth: a hold only blocks `apt upgrade`; a priority-1001 pin makes apt
  # (and any PackageKit-based update tool) refuse 2.29+ even on an explicit install.
  cat > /etc/apt/preferences.d/timescaledb-pin.pref <<'PIN'
Package: timescaledb-2-*
Pin: version 2.28.*
Pin-Priority: 1001
PIN
  echo "    wrote /etc/apt/preferences.d/timescaledb-pin.pref (locks TimescaleDB to 2.28.*)"
else
  echo "    (!) no 2.28.x found; installing latest - Zabbix needs AllowUnsupportedDBVersions=1 and"
  echo "        will not manage compression until you downgrade to 2.28."
  apt-get install -y "$TS_META"
fi

# External-check collectors for agentless device classes (NUT UPS, DNS resolution, XCP-NG, agentless
# Linux over SSH). They run from the Zabbix server's ExternalScripts dir, so a core WITHOUT an external
# proxy - or any device Monitored-by "Core server" - can run them. openssh-client + sshpass (installed
# above) back the SSH collector's key / password auth. The argus-probe image bakes the same scripts
# (kept byte-identical) for proxy-monitored devices. Installed in BOTH modes (manual install + appliance);
# after that the argus-updater keeps them current: the Argus image carries them and it copies the
# running version's in after every core update (the Updates page shows it).
EXT_DST=/usr/lib/zabbix/externalscripts
if [[ -d "${SCRIPT_DIR}/externalscripts" ]]; then
  install -d -m 0755 "$EXT_DST"
  for f in "${SCRIPT_DIR}/externalscripts/"*.py; do
    [[ -e "$f" ]] || continue
    install -m 0755 "$f" "$EXT_DST/"
    echo "    installed external-check collector $(basename "$f")"
  done
fi

# Everything below up to the OS-patching step is per-instance configuration - skipped when baking the
# appliance image (the first-boot service runs the equivalent with per-install credentials, and re-runs
# timescaledb-tune against the DEPLOYED VM's RAM rather than the build VM's).
if [[ "$SETUP_MODE" == "full" ]]; then

echo "==> [4/8] Tune PostgreSQL for TimescaleDB"
timescaledb-tune --quiet --yes || true
systemctl restart postgresql

echo "==> [5/8] Create DB + user"
# The password is an SQL string literal here: a quote inside it is doubled, not left to break out.
DBPASS_SQL="${DBPASS//\'/\'\'}"
sudo -u postgres psql -tc "SELECT 1 FROM pg_roles WHERE rolname='zabbix'" | grep -q 1 || \
  sudo -u postgres psql -c "CREATE USER zabbix WITH PASSWORD '${DBPASS_SQL}';"
sudo -u postgres psql -tc "SELECT 1 FROM pg_database WHERE datname='zabbix'" | grep -q 1 || \
  sudo -u postgres createdb -O zabbix zabbix
sudo -u postgres psql -d zabbix -c "CREATE EXTENSION IF NOT EXISTS timescaledb CASCADE;"

echo "==> [6/8] Import Zabbix schema + enable TimescaleDB partitioning/compression"
# server.sql.gz creates all tables. Safe to skip if already imported.
if ! sudo -u postgres psql -d zabbix -tc "SELECT 1 FROM information_schema.tables WHERE table_name='users'" | grep -q 1; then
  zcat /usr/share/zabbix-sql-scripts/postgresql/server.sql.gz | sudo -u zabbix psql zabbix
  # TimescaleDB conversion (path may be timescaledb/schema.sql on 7.0):
  TS_SQL="$(ls /usr/share/zabbix-sql-scripts/postgresql/timescaledb/schema.sql 2>/dev/null || \
            ls /usr/share/zabbix-sql-scripts/postgresql/timescaledb.sql 2>/dev/null || true)"
  [[ -n "$TS_SQL" ]] && cat "$TS_SQL" | sudo -u zabbix psql zabbix || \
    echo "   (!) TimescaleDB schema file not found - enable it later per Zabbix docs"
else
  echo "   schema already present - skipping import"
fi

echo "==> [7/8] Configure zabbix_server.conf"
CONF=/etc/zabbix/zabbix_server.conf
sed -i "s/^# *DBName=.*/DBName=zabbix/"     "$CONF" || true
sed -i "s/^# *DBUser=.*/DBUser=zabbix/"     "$CONF" || true
grep -q "^DBPassword=" "$CONF" || echo "DBPassword=${DBPASS}" >> "$CONF"
# The file now holds the database password: root writes it, the zabbix user reads it, nobody else.
chown root:zabbix "$CONF" && chmod 640 "$CONF"

fi # SETUP_MODE=full

echo "==> [8/8] OS patching & lifecycle (DESIGN §14c)"
# Keep the core's Debian OS patched without dragging pinned packages forward. unattended-upgrades
# applies the SECURITY suite only; it honours apt holds/pins, so the TimescaleDB 2.28 hold/pin above
# is safe (it will never pull 2.29). The core is a "pet": patches auto-apply but the REBOOT is
# operator-scheduled from Argus (the Updates page), never unattended - it hosts the DB + Zabbix.
# needrestart auto-restarts services after a libc/openssl bump so most updates need no reboot at all.
#
# ARGUS_STATE_DIR is the HOST path you bind-mount into the Argus core container as ARGUS_UPDATE_DIR
# (the self-update channel it already shares with the argus-updater sidecar). The reporter drops the
# core's patch status there for Argus to read; Argus writes the chosen reboot window back for the
# watcher below. Override if you mapped a different host path.
ARGUS_STATE_DIR="${ARGUS_STATE_DIR:-/docker/argus-update}"
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

# The host scripts and their units (the reporter, the reboot, patch and Zabbix watchers, the timezone
# check, the sizing tool, the backup tools): one installer, from the files in host/ and its manifest.
# After this first install the Argus image carries the same files and the argus-updater writes them to
# the host after each core update (DESIGN 14c), so nothing here is ever re-run by hand.
ARGUS_STATE_DIR="$ARGUS_STATE_DIR" bash "${SCRIPT_DIR}/host/install-host-tools.sh"
echo "    unattended-upgrades (security only, no auto-reboot) + host reporter + zabbix-minor watcher installed"
echo "    reporting OS status into ${ARGUS_STATE_DIR} (map this as ARGUS_UPDATE_DIR in the core container)"

# Backups (DESIGN section 14e): argus-backup / argus-restore and their units, in BOTH modes. Argus
# holds the plan (Settings -> Backups); the host does the work as root.
if [[ -f "${SCRIPT_DIR}/host/install-backup.sh" ]]; then
  echo "==> backup tools"
  ARGUS_STATE_DIR="$ARGUS_STATE_DIR" bash "${SCRIPT_DIR}/host/install-backup.sh"
fi

if [[ "$SETUP_MODE" != "full" ]]; then
  echo
  echo ">>> Image mode: packages + OS patching installed. Database, zabbix_server.conf, TLS, and the"
  echo ">>> frontend are configured per-instance by the appliance's first-boot service."
  exit 0
fi
echo
echo ">>> Now append the TLS + tuning lines from core/zabbix_server.conf.snippet to $CONF,"
echo ">>> place ca.crt + zabbix-core.crt/.key under /etc/zabbix/certs/, size Zabbix and PostgreSQL"
echo ">>> to this machine (every boot re-runs this, so a grown VM re-sizes itself), then start everything:"
echo "      argus-size-zabbix --apply --tune"
echo "      systemctl enable --now zabbix-server zabbix-agent2 nginx php8.4-fpm  # Debian 13 = PHP 8.4"
echo ">>> Finish the frontend setup wizard in the browser, and in Housekeeping enable"
echo "    'Override history/trend period' so TimescaleDB compression kicks in."
echo
echo "Retention to set in Housekeeping (matches the UI time tabs):"
echo "  history 30d  |  trends 730d (2y)  |  enable compression after 7d"

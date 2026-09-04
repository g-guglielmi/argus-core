#!/usr/bin/env bash
# setup-core.sh - install & configure the monitoring core on a Debian 12 / Ubuntu 24.04 VM:
#   Zabbix 7.0 LTS (server + nginx frontend, which serves the JSON-RPC API)
#   PostgreSQL 16 + TimescaleDB (Zabbix's history/trends store = your time-series DB)
#
# Review before running. Assumes a fresh VM and root/sudo. Idempotent-ish but re-runs
# may complain on already-created objects - that's fine.
#
# Usage:  sudo DBPASS='choose-a-strong-pass' ./setup-core.sh
set -euo pipefail

: "${DBPASS:?Set DBPASS to the Zabbix DB password, e.g. sudo DBPASS=... ./setup-core.sh}"
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
wget -q "$ZBX_RELEASE_DEB" -O zbx.deb || {
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
echo "deb https://packagecloud.io/timescale/timescaledb/${DISTRO_ID}/ ${CODENAME} main" \
  > /etc/apt/sources.list.d/timescaledb.list
wget --quiet -O - https://packagecloud.io/timescale/timescaledb/gpgkey | \
  gpg --dearmor -o /etc/apt/trusted.gpg.d/timescaledb.gpg
apt-get update

echo "==> [3/8] Install packages"
apt-get install -y \
  zabbix-server-pgsql zabbix-frontend-php php8.4-pgsql zabbix-nginx-conf zabbix-sql-scripts zabbix-agent2 \
  "postgresql-${PG_VER}"

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

echo "==> [4/8] Tune PostgreSQL for TimescaleDB"
timescaledb-tune --quiet --yes || true
systemctl restart postgresql

echo "==> [5/8] Create DB + user"
sudo -u postgres psql -tc "SELECT 1 FROM pg_roles WHERE rolname='zabbix'" | grep -q 1 || \
  sudo -u postgres psql -c "CREATE USER zabbix WITH PASSWORD '${DBPASS}';"
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

echo "==> [8/8] OS patching & lifecycle (DESIGN §14c)"
# Keep the core's Debian OS patched without dragging pinned packages forward. unattended-upgrades
# applies the SECURITY suite only; it honours apt holds/pins, so the TimescaleDB 2.28 hold/pin above
# is safe (it will never pull 2.29). The core is a "pet": patches auto-apply but the REBOOT is
# operator-scheduled from Argus (Settings -> OS updates), never unattended - it hosts the DB + Zabbix.
# needrestart auto-restarts services after a libc/openssl bump so most updates need no reboot at all.
#
# ARGUS_STATE_DIR is the HOST path you bind-mount into the Argus core container as ARGUS_UPDATE_DIR
# (the self-update channel it already shares with the argus-updater sidecar). The reporter drops the
# core's patch status there for Argus to read; Argus writes the chosen reboot window back for the
# watcher below. Override if you mapped a different host path.
ARGUS_STATE_DIR="${ARGUS_STATE_DIR:-/opt/argus/update}"
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

# Host reporter: pending security-update count + reboot-required flag -> os-status.json in the shared dir.
install -d -m 0755 "$ARGUS_STATE_DIR"
cat > /usr/local/sbin/argus-os-report <<'REPORT'
#!/usr/bin/env bash
# Report the core VM's OS patch status for Argus (DESIGN §14c). Writes os-status.json into the shared
# self-update dir the core container reads. Best-effort: an unknown count is reported as -1.
set -u
DIR="${ARGUS_STATE_DIR:-/opt/argus/update}"
sec="$(apt-get -s -o Debug::NoLocking=true upgrade 2>/dev/null | awk '/^Inst/ && /[Ss]ecurity/ {n++} END{print n+0}')"
[ -n "$sec" ] || sec=-1
reboot=false; [ -f /var/run/reboot-required ] && reboot=true
os="$( . /etc/os-release 2>/dev/null && printf '%s' "${PRETTY_NAME:-Linux}" )"
install -d -m 0755 "$DIR"
umask 022  # so the new file is created world-readable, not mktemp's default 0600
tmp="$(mktemp "$DIR/.os-status.XXXXXX")"
printf '{"sec_updates":%d,"reboot_required":%s,"reported_at":%d,"os":"%s"}\n' "$sec" "$reboot" "$(date +%s)" "$os" > "$tmp"
mv -f "$tmp" "$DIR/os-status.json"
chmod 0644 "$DIR/os-status.json"  # bulletproof: the Argus container (possibly non-root) reads it via the bind mount
REPORT
chmod +x /usr/local/sbin/argus-os-report

# Reboot watcher: honour the operator-chosen window Argus writes to reboot-window.json (mode "auto" +
# weekday/hour/minute). Only reboots when the OS actually flagged reboot-required. Local, never remote.
cat > /usr/local/sbin/argus-reboot-check <<'RCHK'
#!/usr/bin/env bash
set -u
DIR="${ARGUS_STATE_DIR:-/opt/argus/update}"
WIN="$DIR/reboot-window.json"
[ -f "$WIN" ] || exit 0
[ -f /var/run/reboot-required ] || exit 0
mode="$(sed -n 's/.*"mode":"\([a-z]*\)".*/\1/p' "$WIN")"
[ "$mode" = "auto" ] || exit 0
wd="$(sed -n 's/.*"weekday":\([0-9]*\).*/\1/p' "$WIN")"
wh="$(sed -n 's/.*"hour":\([0-9]*\).*/\1/p' "$WIN")"
wm="$(sed -n 's/.*"minute":\([0-9]*\).*/\1/p' "$WIN")"
now_wd="$(date +%w)"; now_h="$(date +%-H)"; now_m="$(date +%-M)"
[ "$now_wd" = "$wd" ] && [ "$now_h" = "$wh" ] || exit 0
# The timer fires every 5 min; reboot if we're within a 10-minute slack of the target minute.
if [ "$now_m" -ge "$wm" ] && [ "$now_m" -lt $((wm + 10)) ]; then
  logger -t argus-reboot "OS reboot-required in the operator window; rebooting the core"
  systemctl reboot
fi
RCHK
chmod +x /usr/local/sbin/argus-reboot-check

# systemd units: report hourly, check the reboot window every 5 minutes. ARGUS_STATE_DIR is baked in
# so the scripts and Argus agree on the shared path.
for unit in argus-os-report argus-reboot-check; do
  cat > "/etc/systemd/system/${unit}.service" <<SVC
[Unit]
Description=Argus OS ${unit#argus-} (DESIGN §14c)
[Service]
Type=oneshot
Environment=ARGUS_STATE_DIR=${ARGUS_STATE_DIR}
ExecStart=/usr/local/sbin/${unit}
SVC
done
cat > /etc/systemd/system/argus-os-report.timer <<'T1'
[Unit]
Description=Report the core's OS patch status to Argus hourly
[Timer]
OnBootSec=2min
OnUnitActiveSec=1h
Persistent=true
[Install]
WantedBy=timers.target
T1
cat > /etc/systemd/system/argus-reboot-check.timer <<'T2'
[Unit]
Description=Reboot the core in its operator-scheduled window when the OS requires it
[Timer]
OnBootSec=3min
OnUnitActiveSec=5min
[Install]
WantedBy=timers.target
T2
systemctl daemon-reload
systemctl enable --now argus-os-report.timer argus-reboot-check.timer
/usr/local/sbin/argus-os-report || true
echo "    unattended-upgrades (security only, no auto-reboot) + host reporter installed"
echo "    reporting OS status into ${ARGUS_STATE_DIR} (map this as ARGUS_UPDATE_DIR in the core container)"
echo
echo ">>> Now append the TLS + tuning lines from core/zabbix_server.conf.snippet to $CONF,"
echo ">>> place ca.crt + zabbix-core.crt/.key under /etc/zabbix/certs/, then:"
echo "      systemctl enable --now zabbix-server zabbix-agent2 nginx php8.4-fpm  # Debian 13 = PHP 8.4"
echo ">>> Finish the frontend setup wizard in the browser, and in Housekeeping enable"
echo "    'Override history/trend period' so TimescaleDB compression kicks in."
echo
echo "Retention to set in Housekeeping (matches the UI time tabs):"
echo "  history 30d  |  trends 730d (2y)  |  enable compression after 7d"

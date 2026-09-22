#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# setup-core-patching.sh - install ONLY the OS patching & lifecycle piece (DESIGN §14c) on an
# already-running core VM, without re-running the full Zabbix/PostgreSQL installer.
#
# Keeps the core's Debian OS patched with unattended-upgrades (SECURITY suite only; it honours apt
# holds/pins, so the TimescaleDB 2.28 hold is safe) + needrestart (auto-restart services after a
# libc/openssl bump, so most updates need no reboot). The core is a "pet": patches auto-apply, but the
# REBOOT is operator-scheduled from Argus (Settings -> OS updates), never unattended.
#
# It also installs a host reporter (posts the core's status to Argus), a reboot watcher (honours the
# window Argus picks), and a Zabbix minor-update watcher (applies same-major zabbix-* updates in an
# operator-scheduled window; major upgrades stay manual). Patching stays local - Argus never runs
# apt remotely.
#
# Usage:  sudo ARGUS_STATE_DIR=/docker/argus-update ./setup-core-patching.sh
#   ARGUS_STATE_DIR is the HOST path you bind-mount into the Argus core container as ARGUS_UPDATE_DIR
#   (find it with:  docker inspect argus --format '{{range .Mounts}}{{.Source}} -> {{.Destination}}{{"\n"}}{{end}}'
#   and take the Source whose Destination is /update). Defaults to /opt/argus/update.
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "!! run as root (sudo)."; exit 1
fi

ARGUS_STATE_DIR="${ARGUS_STATE_DIR:-/opt/argus/update}"
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

echo "==> installing the host reporter + reboot watcher (state dir: ${ARGUS_STATE_DIR})"
# Host reporter: pending security-update count + reboot-required flag -> os-status.json in the shared dir.
# Only create it if missing - NEVER reset an existing dir's owner/mode: the core container writes its
# self-update request.json here and may run as a non-root UID that owns the dir.
[ -d "$ARGUS_STATE_DIR" ] || install -d -m 0755 "$ARGUS_STATE_DIR"
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
# Zabbix server package: installed + candidate version (normalized: no epoch, no revision) so
# Argus can show "x.y.z available" and the zbx-update watcher knows when to act. Both empty
# when Zabbix isn't installed on this host.
zi="$(dpkg-query -W -f='${Version}' zabbix-server-pgsql 2>/dev/null)"
zi="${zi#*:}"; zi="${zi%%-*}"
zc="$(apt-cache policy zabbix-server-pgsql 2>/dev/null | awk '/Candidate:/{print $2}')"
[ "$zc" = "(none)" ] && zc=""
zc="${zc#*:}"; zc="${zc%%-*}"
[ -d "$DIR" ] || install -d -m 0755 "$DIR"
umask 022  # so the new file is created world-readable, not mktemp's default 0600
tmp="$(mktemp "$DIR/.os-status.XXXXXX")"
printf '{"sec_updates":%d,"reboot_required":%s,"reported_at":%d,"os":"%s","zbx_server":"%s","zbx_candidate":"%s"}\n' "$sec" "$reboot" "$(date +%s)" "$os" "$zi" "$zc" > "$tmp"
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

# Zabbix minor-update watcher: honour the operator window Argus writes to zbx-update-window.json
# (mode "auto" + weekday/hour/minute, same shape as the reboot window). Applies SAME-major.minor
# zabbix-* updates only - the Zabbix apt repo is pinned per major anyway, this is the
# belt-and-braces guard - then restarts zabbix-server (a seconds-long blip the proxies buffer
# through). Major upgrades stay a planned manual event. Local, never remote.
cat > /usr/local/sbin/argus-zbx-update <<'ZUPD'
#!/usr/bin/env bash
set -u
DIR="${ARGUS_STATE_DIR:-/opt/argus/update}"
WIN="$DIR/zbx-update-window.json"
[ -f "$WIN" ] || exit 0
mode="$(sed -n 's/.*"mode":"\([a-z]*\)".*/\1/p' "$WIN")"
[ "$mode" = "auto" ] || exit 0
wd="$(sed -n 's/.*"weekday":\([0-9]*\).*/\1/p' "$WIN")"
wh="$(sed -n 's/.*"hour":\([0-9]*\).*/\1/p' "$WIN")"
wm="$(sed -n 's/.*"minute":\([0-9]*\).*/\1/p' "$WIN")"
now_wd="$(date +%w)"; now_h="$(date +%-H)"; now_m="$(date +%-M)"
[ "$now_wd" = "$wd" ] && [ "$now_h" = "$wh" ] || exit 0
# The timer fires every 5 min; act if we're within a 10-minute slack of the target minute.
[ "$now_m" -ge "$wm" ] && [ "$now_m" -lt $((wm + 10)) ] || exit 0
zi="$(dpkg-query -W -f='${Version}' zabbix-server-pgsql 2>/dev/null)"
zi="${zi#*:}"; zi="${zi%%-*}"
[ -n "$zi" ] || exit 0
apt-get update -qq -o DPkg::Lock::Timeout=300 || true
zc="$(apt-cache policy zabbix-server-pgsql 2>/dev/null | awk '/Candidate:/{print $2}')"
[ "$zc" = "(none)" ] && zc=""
zc="${zc#*:}"; zc="${zc%%-*}"
[ -n "$zc" ] && [ "$zc" != "$zi" ] || exit 0
# Same major.minor line only (e.g. 7.0.x -> 7.0.y). A major jump is always a planned manual event.
if [ "${zi%.*}" != "${zc%.*}" ]; then
  logger -t argus-zbx-update "candidate $zc is not on the $zi line; skipping (major upgrades are manual)"
  exit 0
fi
pkgs="$(dpkg-query -W -f='${Package} ' 'zabbix-*' 2>/dev/null)"
[ -n "$pkgs" ] || exit 0
logger -t argus-zbx-update "updating Zabbix $zi -> $zc in the operator window: $pkgs"
# shellcheck disable=SC2086
if DEBIAN_FRONTEND=noninteractive apt-get install -y --only-upgrade -o DPkg::Lock::Timeout=300 $pkgs; then
  systemctl restart zabbix-server || true
  logger -t argus-zbx-update "Zabbix updated to $zc; zabbix-server restarted"
else
  logger -t argus-zbx-update "Zabbix update failed; leaving packages as they are"
fi
# Refresh the status file right away so the Argus panel flips without waiting for the hourly run.
/usr/local/sbin/argus-os-report || true
ZUPD
chmod +x /usr/local/sbin/argus-zbx-update

# systemd units: report hourly, check the reboot window every 5 minutes. ARGUS_STATE_DIR is baked in
# so the scripts and Argus agree on the shared path.
for unit in argus-os-report argus-reboot-check argus-zbx-update; do
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
cat > /etc/systemd/system/argus-zbx-update.timer <<'T3'
[Unit]
Description=Apply Zabbix minor updates in the operator-scheduled window
[Timer]
OnBootSec=4min
OnUnitActiveSec=5min
[Install]
WantedBy=timers.target
T3
systemctl daemon-reload
systemctl enable --now argus-os-report.timer argus-reboot-check.timer argus-zbx-update.timer
# Report once now so Argus shows the core's status without waiting for the first hourly run.
/usr/local/sbin/argus-os-report || true

echo
echo "==> done. unattended-upgrades (security only, no auto-reboot) + reporter + reboot watcher + zabbix-minor watcher installed."
echo "    status file: ${ARGUS_STATE_DIR}/os-status.json  (Argus reads it at ARGUS_UPDATE_DIR=/update)"
echo "    In Argus -> Settings -> OS updates you should now see the core's status; set the reboot"
echo "    window and the Zabbix minor-update window there."
echo "    Check the timers:  systemctl list-timers 'argus-*'"

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
DIR="${ARGUS_STATE_DIR:-/docker/argus-update}"
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
# VM timezone + NTP sync state (systemd-timesyncd by default on Debian): the Settings card
# shows both, and a drifting clock on a monitoring box deserves a loud pill.
tz="$(timedatectl show -p Timezone --value 2>/dev/null)"
cs="$(timedatectl show -p NTPSynchronized --value 2>/dev/null)"
[ -d "$DIR" ] || install -d -m 0755 "$DIR"
umask 022  # so the new file is created world-readable, not mktemp's default 0600
tmp="$(mktemp "$DIR/.os-status.XXXXXX")"
printf '{"sec_updates":%d,"reboot_required":%s,"reported_at":%d,"os":"%s","zbx_server":"%s","zbx_candidate":"%s","tz":"%s","clock":"%s","zbx_update_now":true,"reboot_now":true,"patch_now":true}\n' "$sec" "$reboot" "$(date +%s)" "$os" "$zi" "$zc" "$tz" "$cs" > "$tmp"
mv -f "$tmp" "$DIR/os-status.json"
chmod 0644 "$DIR/os-status.json"  # bulletproof: the Argus container (possibly non-root) reads it via the bind mount
REPORT
chmod +x /usr/local/sbin/argus-os-report

# Reboot watcher: honour the operator-chosen window Argus writes to reboot-window.json (mode "auto" +
# weekday/hour/minute), or a request Argus drops as reboot-request.json ("Reboot now" on the Updates
# page; the path unit below starts the script within seconds). Only reboots when the OS actually
# flagged reboot-required, and leaves reboot-result.json for the page. Local, never remote.
cat > /usr/local/sbin/argus-reboot-check <<'RCHK'
#!/usr/bin/env bash
set -u
DIR="${ARGUS_STATE_DIR:-/docker/argus-update}"
WIN="$DIR/reboot-window.json"
REQ="$DIR/reboot-request.json"
RES="$DIR/reboot-result.json"
RR="${ARGUS_REBOOT_FLAG:-/var/run/reboot-required}"
# What Argus shows afterwards (reboot-result.json): rebooting, done or failed, why, one sentence.
why=""
result() {
  umask 022
  tmp="$(mktemp "$DIR/.reboot-result.XXXXXX")"
  printf '{"state":"%s","at":%d,"why":"%s","detail":"%s"}\n' "$1" "$(date +%s)" "$why" "$2" > "$tmp"
  mv -f "$tmp" "$RES"; chmod 0644 "$RES"
}
# On request (Reboot now on the Updates page; the path unit starts this within seconds) the core
# reboots if the OS still asks for it. Otherwise only in the operator window.
if [ -f "$REQ" ]; then
  why="request"
  rm -f "$REQ"
  if [ -f "$RR" ]; then
    logger -t argus-reboot "rebooting the core on request from Argus"
    result rebooting "rebooting the core"
    systemctl reboot
  else
    result done "no reboot is needed"
    /usr/local/sbin/argus-os-report || true
  fi
  exit 0
fi
[ -f "$WIN" ] || exit 0
[ -f "$RR" ] || exit 0
mode="$(sed -n 's/.*"mode":"\([a-z]*\)".*/\1/p' "$WIN")"
[ "$mode" = "auto" ] || exit 0
wd="$(sed -n 's/.*"weekday":\([0-9]*\).*/\1/p' "$WIN")"
wh="$(sed -n 's/.*"hour":\([0-9]*\).*/\1/p' "$WIN")"
wm="$(sed -n 's/.*"minute":\([0-9]*\).*/\1/p' "$WIN")"
now_wd="$(date +%w)"; now_h="$(date +%-H)"; now_m="$(date +%-M)"
[ "$now_wd" = "$wd" ] && [ "$now_h" = "$wh" ] || exit 0
# The timer fires every 5 min; reboot if we're within a 10-minute slack of the target minute.
if [ "$now_m" -ge "$wm" ] && [ "$now_m" -lt $((wm + 10)) ]; then
  why="window"
  logger -t argus-reboot "OS reboot-required in the operator window; rebooting the core"
  result rebooting "rebooting the core in its window"
  systemctl reboot
fi
RCHK
chmod +x /usr/local/sbin/argus-reboot-check

# Patch now: apply the pending security updates right away when Argus drops patch-request.json (the
# path unit starts this within seconds): the same security-only policy unattended-upgrades runs
# daily, just now. Services restart as needrestart decides; a kernel update then asks for a reboot,
# which the reboot watcher above handles. Local, never remote.
cat > /usr/local/sbin/argus-patch <<'PTCH'
#!/usr/bin/env bash
set -u
DIR="${ARGUS_STATE_DIR:-/docker/argus-update}"
REQ="$DIR/patch-request.json"
RES="$DIR/patch-result.json"
[ -f "$REQ" ] || exit 0
rm -f "$REQ"
why="request"
result() {
  umask 022
  tmp="$(mktemp "$DIR/.patch-result.XXXXXX")"
  printf '{"state":"%s","at":%d,"why":"%s","detail":"%s"}\n' "$1" "$(date +%s)" "$why" "$2" > "$tmp"
  mv -f "$tmp" "$RES"; chmod 0644 "$RES"
}
logger -t argus-patch "applying the pending security updates on request from Argus"
DEBIAN_FRONTEND=noninteractive unattended-upgrade -v 2>&1 | logger -t argus-patch
if [ "${PIPESTATUS[0]}" -eq 0 ]; then
  result done "security updates applied"
else
  result failed "unattended-upgrade reported a problem: journalctl -t argus-patch on the core VM has the details"
fi
/usr/local/sbin/argus-os-report || true
PTCH
chmod +x /usr/local/sbin/argus-patch

# Zabbix minor-update watcher: honour the operator window Argus writes to zbx-update-window.json
# (mode "auto" + weekday/hour/minute, same shape as the reboot window), or a request Argus drops as
# zbx-update-request.json ("Update now" on the Updates page; the path unit below starts the script
# within seconds). Applies SAME-major.minor zabbix-* updates only - the Zabbix apt repo is pinned per
# major anyway, this is the belt-and-braces guard - then restarts zabbix-server (a seconds-long blip
# the proxies buffer through) and leaves the outcome in zbx-update-result.json for the page to show.
# Major upgrades stay a planned manual event. Local, never remote.
cat > /usr/local/sbin/argus-zbx-update <<'ZUPD'
#!/usr/bin/env bash
set -u
DIR="${ARGUS_STATE_DIR:-/docker/argus-update}"
WIN="$DIR/zbx-update-window.json"
REQ="$DIR/zbx-update-request.json"
RES="$DIR/zbx-update-result.json"
# Two ways in: Argus dropped a request (Update now on the Updates page; the path unit starts this
# within seconds of it), or the timer found the operator window open. The work is the same.
why=""
if [ -f "$REQ" ]; then
  why="request"
  rm -f "$REQ"
else
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
  why="window"
fi
# What Argus shows afterwards (zbx-update-result.json): done or failed, the versions, why it ran,
# and one sentence. Versions are digits and dots, the sentence has no quotes: plain JSON by printf.
result() {
  umask 022
  tmp="$(mktemp "$DIR/.zbx-update-result.XXXXXX")"
  printf '{"state":"%s","from":"%s","to":"%s","at":%d,"why":"%s","detail":"%s"}\n' "$1" "${zi:-}" "${zc:-}" "$(date +%s)" "$why" "$2" > "$tmp"
  mv -f "$tmp" "$RES"; chmod 0644 "$RES"
}
zi="$(dpkg-query -W -f='${Version}' zabbix-server-pgsql 2>/dev/null)"
zi="${zi#*:}"; zi="${zi%%-*}"
if [ -z "$zi" ]; then
  [ "$why" = "request" ] && result failed "Zabbix is not installed from packages on this host"
  exit 0
fi
apt-get update -qq -o DPkg::Lock::Timeout=300 || true
zc="$(apt-cache policy zabbix-server-pgsql 2>/dev/null | awk '/Candidate:/{print $2}')"
[ "$zc" = "(none)" ] && zc=""
zc="${zc#*:}"; zc="${zc%%-*}"
if [ -z "$zc" ] || [ "$zc" = "$zi" ]; then
  if [ "$why" = "request" ]; then
    result done "already up to date"
    /usr/local/sbin/argus-os-report || true
  fi
  exit 0
fi
# Same major.minor line only (e.g. 7.0.x -> 7.0.y). A major jump is always a planned manual event.
if [ "${zi%.*}" != "${zc%.*}" ]; then
  logger -t argus-zbx-update "candidate $zc is not on the ${zi%.*} line; skipping (major upgrades are manual)"
  [ "$why" = "request" ] && result failed "$zc is not on the ${zi%.*} line: a major upgrade is a planned manual event"
  exit 0
fi
pkgs="$(dpkg-query -W -f='${Package} ' 'zabbix-*' 2>/dev/null)"
[ -n "$pkgs" ] || exit 0
logger -t argus-zbx-update "updating Zabbix $zi -> $zc ($why): $pkgs"
# shellcheck disable=SC2086
if DEBIAN_FRONTEND=noninteractive apt-get install -y --only-upgrade -o DPkg::Lock::Timeout=300 $pkgs; then
  systemctl restart zabbix-server || true
  logger -t argus-zbx-update "Zabbix updated to $zc; zabbix-server restarted"
  result done "zabbix-server restarted on the new version"
else
  logger -t argus-zbx-update "Zabbix update failed; leaving packages as they are"
  result failed "apt-get could not update the packages: journalctl -t argus-zbx-update on the core VM has the details"
fi
# Refresh the status file right away so the Argus panel flips without waiting for the hourly run.
/usr/local/sbin/argus-os-report || true
ZUPD
chmod +x /usr/local/sbin/argus-zbx-update

# Timezone watcher: apply the operator-chosen VM timezone Argus writes to timezone.json (Settings
# card). Validated against the local zoneinfo database - never feeds an arbitrary string to
# timedatectl. Restarts zabbix-server afterwards (long-running daemons cache the zone) and
# re-reports so the panel reflects the change. Local, never remote.
cat > /usr/local/sbin/argus-tz-check <<'TZCK'
#!/usr/bin/env bash
set -u
DIR="${ARGUS_STATE_DIR:-/docker/argus-update}"
WANT="$DIR/timezone.json"
[ -f "$WANT" ] || exit 0
tz="$(sed -n 's/.*"tz":"\([A-Za-z0-9_+/-]*\)".*/\1/p' "$WANT")"
[ -n "$tz" ] || exit 0
cur="$(timedatectl show -p Timezone --value 2>/dev/null)"
[ "$tz" = "$cur" ] && exit 0
if [ ! -f "/usr/share/zoneinfo/$tz" ]; then
  logger -t argus-tz "requested timezone $tz is not in the zoneinfo database; ignoring"
  exit 0
fi
if timedatectl set-timezone "$tz"; then
  logger -t argus-tz "timezone changed $cur -> $tz (operator setting from Argus)"
  systemctl try-restart zabbix-server 2>/dev/null || true
  systemctl try-restart php8*-fpm 2>/dev/null || true
fi
/usr/local/sbin/argus-os-report || true
TZCK
chmod +x /usr/local/sbin/argus-tz-check

# systemd units: report hourly, check the reboot window every 5 minutes. ARGUS_STATE_DIR is baked in
# so the scripts and Argus agree on the shared path.
for unit in argus-os-report argus-reboot-check argus-zbx-update argus-tz-check argus-patch; do
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
cat > /etc/systemd/system/argus-zbx-update.path <<P3
[Unit]
Description=Apply a Zabbix minor update as soon as Argus requests one (Update now)
[Path]
PathChanged=${ARGUS_STATE_DIR}/zbx-update-request.json
Unit=argus-zbx-update.service
[Install]
WantedBy=multi-user.target
P3
cat > /etc/systemd/system/argus-reboot-check.path <<P2
[Unit]
Description=Reboot the core as soon as Argus requests it (Reboot now)
[Path]
PathChanged=${ARGUS_STATE_DIR}/reboot-request.json
Unit=argus-reboot-check.service
[Install]
WantedBy=multi-user.target
P2
cat > /etc/systemd/system/argus-patch.path <<P4
[Unit]
Description=Apply the pending security updates as soon as Argus requests it (Patch now)
[Path]
PathChanged=${ARGUS_STATE_DIR}/patch-request.json
Unit=argus-patch.service
[Install]
WantedBy=multi-user.target
P4
cat > /etc/systemd/system/argus-tz-check.timer <<'T4'
[Unit]
Description=Apply the operator-chosen VM timezone from Argus
[Timer]
OnBootSec=5min
OnUnitActiveSec=5min
[Install]
WantedBy=timers.target
T4
systemctl daemon-reload
systemctl enable --now argus-os-report.timer argus-reboot-check.timer argus-zbx-update.timer argus-zbx-update.path argus-reboot-check.path argus-patch.path argus-tz-check.timer
# Report once now so Argus shows the core's status without waiting for the first hourly run.
/usr/local/sbin/argus-os-report || true

# The backup tools (DESIGN section 14e) ride along, when this script sits in a checkout with its host/
# sibling: refreshing an existing core's host scripts refreshes them too.
PATCH_DIR="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
if [[ -f "${PATCH_DIR}/host/install-backup.sh" ]]; then
  echo "==> backup tools"
  ARGUS_STATE_DIR="$ARGUS_STATE_DIR" bash "${PATCH_DIR}/host/install-backup.sh"
fi

echo
echo "==> done. unattended-upgrades (security only, no auto-reboot) + reporter + reboot watcher + zabbix-minor watcher installed."
echo "    status file: ${ARGUS_STATE_DIR}/os-status.json  (Argus reads it at ARGUS_UPDATE_DIR=/update)"
echo "    In Argus -> Updates you should now see the core's status; set the reboot"
echo "    window and the Zabbix minor-update window there."
echo "    Check the timers:  systemctl list-timers 'argus-*'"
echo "    Backups: Settings -> Backups in Argus (restore guide: docs/backup-and-restore.md)."

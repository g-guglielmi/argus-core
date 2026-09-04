#!/usr/bin/env bash
# setup-core-patching.sh - install ONLY the OS patching & lifecycle piece (DESIGN §14c) on an
# already-running core VM, without re-running the full Zabbix/PostgreSQL installer.
#
# Keeps the core's Debian OS patched with unattended-upgrades (SECURITY suite only; it honours apt
# holds/pins, so the TimescaleDB 2.28 hold is safe) + needrestart (auto-restart services after a
# libc/openssl bump, so most updates need no reboot). The core is a "pet": patches auto-apply, but the
# REBOOT is operator-scheduled from Argus (Settings -> OS updates), never unattended.
#
# It also installs a host reporter (posts the core's status to Argus) and a reboot watcher (honours the
# window Argus picks). Patching stays local - Argus never runs apt remotely.
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
# Report once now so Argus shows the core's status without waiting for the first hourly run.
/usr/local/sbin/argus-os-report || true

echo
echo "==> done. unattended-upgrades (security only, no auto-reboot) + reporter + reboot watcher installed."
echo "    status file: ${ARGUS_STATE_DIR}/os-status.json  (Argus reads it at ARGUS_UPDATE_DIR=/update)"
echo "    In Argus -> Settings -> OS updates you should now see the core's status; set the reboot window there."
echo "    Check the timers:  systemctl list-timers 'argus-*'"

#!/bin/bash
# Pool/cache drive temperatures for unRAID's SNMP plugin - an optional companion to the
# plugin's own "disktemp" extend, which only covers array members (parity + data disks,
# via mdcmd). This script emits every OTHER assigned drive - cache and custom pools,
# including NVMe - in the same "<drive id>: <temp C>" format.
#
# It reads the emhttp state file, so it returns instantly, never wakes a disk, and the
# output is atomic (no partial reads). Temperatures refresh at unRAID's own SMART polling
# cadence (Settings -> Disk Settings -> Tunable (poll_attributes), default 1800 s).
#
# Install:
#   1. Copy this file to /boot/config/plugins/snmp/pool_temps.sh
#   2. In Settings -> SNMP, add this line to the snmpd.conf box and apply:
#        extend pooltemps /bin/bash /boot/config/plugins/snmp/pool_temps.sh
#      (invoked through bash because /boot is mounted noexec on current unRAID)
#   3. The "Argus unRAID by SNMP" template picks the new extend up automatically; hosts
#      without it are unaffected.
#
# Drives that are spun down or unreadable show temp="*" in the state file and are simply
# omitted - the monitoring side keeps their last reading.

awk -F'=' '
  /^\[/    { gsub(/[\["\]]/, ""); slot=$0 }
  /^id=/   { gsub(/"/, "", $2); id=$2 }
  /^temp=/ {
    gsub(/"/, "", $2)
    if (slot !~ /^(parity[0-9]*|disk[0-9]+)$/ && id != "" && $2 ~ /^[0-9]+$/ && $2 + 0 > 0)
      print id ": " $2
    id=""
  }
' /var/local/emhttp/disks.ini

#!/bin/bash
# Drive temperatures for unRAID's SNMP plugin, read from the emhttp state file: returns
# instantly, output is atomic (no partial reads), and no disk is ever woken. Temperatures
# refresh at unRAID's own SMART polling cadence (Settings -> Disk Settings ->
# Tunable (poll_attributes), default 1800 s) - the same numbers the unRAID dashboard shows.
#
# One script, two extends:
#   no argument / "pools"  ->  cache + custom pool drives (incl. NVMe)
#   "array"                ->  parity + data disks
#
# The "array" extend replaces the plugin's own disktemp extend, whose script serves a
# 5-minute cache and returns PARTIAL files while rebuilding it (one disk per second) -
# the cause of gap-toothed temperature charts. The Argus template prefers these extends
# per drive and falls back to the plugin's disktemp on hosts that don't have them.
#
# Install:
#   1. Copy this file to /boot/config/plugins/snmp/pool_temps.sh
#   2. In Settings -> SNMP, add these lines to the snmpd.conf box - and remove the
#      plugin's own "extend disktemp ..." line - then apply:
#        extend arraytemps /bin/bash /boot/config/plugins/snmp/pool_temps.sh array
#        extend pooltemps /bin/bash /boot/config/plugins/snmp/pool_temps.sh
#      (invoked through bash because /boot is mounted noexec on current unRAID)
#   3. The "Argus unRAID by SNMP" template picks the extends up automatically; hosts
#      without them are unaffected.
#
# Drives that are spun down or unreadable show temp="*" in the state file and are simply
# omitted - the monitoring side keeps their last real reading (no fake standby values).

mode="${1:-pools}"

awk -F'=' -v mode="$mode" '
  /^\[/    { gsub(/[\["\]]/, ""); slot=$0 }
  /^id=/   { gsub(/"/, "", $2); id=$2 }
  /^temp=/ {
    gsub(/"/, "", $2)
    isarr = (slot ~ /^(parity[0-9]*|disk[0-9]+)$/)
    want = (mode == "array") ? isarr : !isarr
    if (want && id != "" && $2 ~ /^[0-9]+$/ && $2 + 0 > 0)
      print id ": " $2
    id=""
  }
' /var/local/emhttp/disks.ini

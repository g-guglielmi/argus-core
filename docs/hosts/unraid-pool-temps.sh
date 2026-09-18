#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

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
# A drive spun down for standby (spundown="1") reports temp="*" in the state file; we emit a
# fixed 20 C standby sentinel for it, so a parked drive shows a distinct low flat line on the
# chart - and any heat warning clears - instead of just holding its last reading. This matches
# the plugin's own disktemp extend (where 20 = spun down). A device with no temperature sensor
# (e.g. the USB boot flash) or an unreadable "*" that is NOT spun down is still omitted, and the
# monitoring side keeps its last real reading. Always-on flash/SSD/NVMe never report spundown=1,
# so they never get the sentinel.
#
# Output format: "<slot>|<drive id>: <temp C>" - e.g. "parity|ST12000NM001G_XXXXXXXX: 34".
# The slot (parity, disk1, cache, ...) becomes the sensor's display name; the drive id keys
# the sensor, so history follows the physical drive across slot changes. The template also
# accepts the older "<drive id>: <temp C>" format.

mode="${1:-pools}"

# Collect id/temp/spundown per [section] and decide at the section boundary, so field order in
# disks.ini doesn't matter. A positive temp is emitted as-is; a spun-down drive gets the 20 C
# sentinel; anything else (no sensor, unreadable but not parked) is omitted.
awk -F'=' -v mode="$mode" '
  function flush() {
    if (slot != "" && id != "") {
      isarr = (slot ~ /^(parity[0-9]*|disk[0-9]+)$/)
      want = (mode == "array") ? isarr : !isarr
      if (want) {
        if (temp ~ /^[0-9]+$/ && temp + 0 > 0) print slot "|" id ": " temp
        else if (sd == "1")                    print slot "|" id ": 20"
      }
    }
    slot=""; id=""; temp=""; sd=""
  }
  /^\[/        { flush(); gsub(/[\["\]]/, ""); slot=$0 }
  /^id=/       { gsub(/"/, "", $2); id=$2 }
  /^temp=/     { gsub(/"/, "", $2); temp=$2 }
  /^spundown=/ { gsub(/"/, "", $2); sd=$2 }
  END          { flush() }
' /var/local/emhttp/disks.ini

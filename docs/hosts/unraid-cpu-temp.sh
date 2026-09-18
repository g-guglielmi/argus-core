#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# CPU package temperature for unRAID's SNMP plugin, read from lm-sensors. Emits a single line
# "CPU: <temp C>" that the "Argus unRAID by SNMP" template's cputemp item consumes.
#
# Requires the "Dynamix System Temperature" plugin (Community Applications): it runs
# sensors-detect and loads the right kernel modules (coretemp for Intel, k10temp for AMD, etc.),
# which is what makes the `sensors` command report a CPU temperature at all. This script just
# reads it - it changes nothing and never needs configuring per CPU.
#
# Sensor selection (first match wins): Intel "Package id 0" -> AMD "Tdie" -> AMD "Tctl" ->
# the hottest "Core N" / "Tccd*" as a fallback. That covers the common Intel and AMD desktop/
# server CPUs; on an unusual chip, run `sensors -u` on the host and adjust the labels below.
#
# Install:
#   1. Install the "Dynamix System Temperature" plugin and let it detect sensors.
#   2. Copy this file to /boot/config/plugins/snmp/cpu_temp.sh
#   3. In Settings -> SNMP, add this line to the snmpd.conf box, then apply:
#        extend cputemp /bin/bash /boot/config/plugins/snmp/cpu_temp.sh
#      (invoked through bash because /boot is mounted noexec on current unRAID)
#   4. The "Argus unRAID by SNMP" template picks the extend up automatically; hosts without it
#      are unaffected.
#
# Output format: "CPU: <temp C>" - e.g. "CPU: 45".

sensors -u 2>/dev/null | awk '
  # A label line sits at column 0 and ends with ":" (e.g. "Package id 0:", "Tctl:", "Core 0:").
  # The chip line ("coretemp-isa-0000") and "Adapter: ..." do not end with ":", so they are skipped.
  /^[^ ].*:$/ { lbl=$0; sub(/:$/, "", lbl); next }
  /_input:/ {
    v = $2 + 0
    if      (lbl ~ /^Package id/) pkg = v
    else if (lbl == "Tdie")       tdie = v
    else if (lbl == "Tctl")       tctl = v
    else if (lbl ~ /^Core /)      { if (v > coremax) coremax = v }
    else if (lbl ~ /^Tccd/)       { if (v > coremax) coremax = v }
  }
  END {
    t = (pkg > 0) ? pkg : (tdie > 0) ? tdie : (tctl > 0) ? tctl : coremax
    if (t > 0) printf "CPU: %.0f\n", t
  }
'

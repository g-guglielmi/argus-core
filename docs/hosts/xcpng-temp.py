#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi
#
# xcpng-temp.py - optional Argus XAPI plugin: CPU package temperature for an XCP-NG host.
#
# Install ON EACH XCP-NG HYPERVISOR (dom0) as:   /etc/xapi.d/plugins/argus-temp   (chmod +x)
# The name matters: the Argus collector calls host.call_plugin(..., "argus-temp", "get", {})
# over the same XAPI session it already uses - no extra port, no snmpd on dom0. Hosts without
# the plugin simply have no temperature sensor; everything else is unaffected.
#
# XCP-NG 8.3 dom0 runs python3 (keep the shebang above). On XCP-NG 8.2 change the first line to
#   #!/usr/bin/python2
# - the code below runs unchanged on both.
#
# Reads the CPU package temperature from the kernel hwmon tree (coretemp for Intel, k10temp /
# zenpower for AMD). Prefers the package/Tdie/Tctl label, falls back to the hottest core. When no
# CPU chip is exposed at all - Xen dom0 blocks the MSR probing Intel's coretemp needs, so on Intel
# hosts "modprobe coretemp" typically fails with "No such device" - it falls back to the ACPI
# thermal zone (acpitz), which on most boards tracks the CPU package closely. Prints degrees
# Celsius as a plain number.
import os

import XenAPIPlugin

HWMON = "/sys/class/hwmon"
CHIPS = ("coretemp", "k10temp", "zenpower")
FALLBACK_CHIPS = ("acpitz",)
PREFERRED = ("package id 0", "tdie", "tctl")


def read(path):
    try:
        with open(path) as f:
            return f.read().strip()
    except (IOError, OSError):
        return ""


def scan(chips):
    best = None       # hottest reading fallback
    preferred = None  # package/Tdie/Tctl reading
    if os.path.isdir(HWMON):
        for dev in sorted(os.listdir(HWMON)):
            base = os.path.join(HWMON, dev)
            if read(os.path.join(base, "name")) not in chips:
                continue
            for fn in sorted(os.listdir(base)):
                if not (fn.startswith("temp") and fn.endswith("_input")):
                    continue
                raw = read(os.path.join(base, fn))
                try:
                    temp = int(raw) / 1000.0
                except ValueError:
                    continue
                if temp <= 0 or temp > 150:
                    continue
                label = read(os.path.join(base, fn.replace("_input", "_label"))).lower()
                if label in PREFERRED and preferred is None:
                    preferred = temp
                if best is None or temp > best:
                    best = temp
    return preferred if preferred is not None else best


def get(session, args):
    temp = scan(CHIPS)
    if temp is None:
        temp = scan(FALLBACK_CHIPS)
    if temp is None:
        raise Exception("no CPU temperature sensor found under %s" % HWMON)
    return "%.1f" % temp


XenAPIPlugin.dispatch({"get": get})

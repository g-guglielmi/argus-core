#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# Tests for argus_xcpng.py's VM list for placing hosts (vm_nics): each VM's MACs and guest IPv4
# addresses with the hypervisor it runs on, where it is resident, else its affinity, else the pool's
# only member; templates, snapshots and control domains left out. Stdlib only, no XAPI.
import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "..", "argus_xcpng.py")
spec = importlib.util.spec_from_file_location("argus_xcpng", SCRIPT)
xc = importlib.util.module_from_spec(spec)
spec.loader.exec_module(xc)

HOSTS = {"OpaqueRef:h1": {"name_label": "xen1", "address": "10.0.0.4"},
         "OpaqueRef:h2": {"name_label": "xen2", "address": "10.0.0.5"}}


class VmNicsTest(unittest.TestCase):
    def test_guest_ips(self):
        gm = {"networks": {"0/ip": "10.0.0.7", "0/ipv4/0": "10.0.0.7", "1/ipv4/0": "10.0.1.7", "0/ipv6/0": "fe80::1"}}
        self.assertEqual(xc.guest_ips(gm), ["10.0.0.7", "10.0.1.7"])
        self.assertEqual(xc.guest_ips({}), [])
        self.assertEqual(xc.guest_ips(None), [])

    def test_vm_nics(self):
        vms = {
            "OpaqueRef:v1": {"name_label": "dns", "uuid": "u1", "resident_on": "OpaqueRef:h2", "guest_metrics": "OpaqueRef:g1"},
            "OpaqueRef:v2": {"name_label": "media", "uuid": "u2", "resident_on": "OpaqueRef:NULL", "affinity": "OpaqueRef:h1"},
            "OpaqueRef:v3": {"name_label": "nowhere", "uuid": "u3", "resident_on": "OpaqueRef:NULL"},
            "OpaqueRef:t": {"name_label": "tpl", "is_a_template": True, "resident_on": "OpaqueRef:h1"},
            "OpaqueRef:d": {"name_label": "dom0", "is_control_domain": True, "resident_on": "OpaqueRef:h1"},
            "OpaqueRef:v4": {"name_label": "no-nic", "uuid": "u4", "resident_on": "OpaqueRef:h1"},
        }
        vifs = {
            "a": {"VM": "OpaqueRef:v1", "MAC": "AA:BB:CC:00:00:01"},
            "b": {"VM": "OpaqueRef:v1", "MAC": "aa:bb:cc:00:00:02"},
            "c": {"VM": "OpaqueRef:v2", "MAC": "aa:bb:cc:00:00:03"},
            "d": {"VM": "OpaqueRef:v3", "MAC": "aa:bb:cc:00:00:04"},
            "e": {"VM": "OpaqueRef:t", "MAC": "aa:bb:cc:00:00:05"},
        }
        gms = {"OpaqueRef:g1": {"networks": {"0/ip": "10.0.0.7"}}}
        got = xc.vm_nics(vms, HOSTS, vifs, gms)
        self.assertEqual([e["name"] for e in got], ["dns", "media"])
        self.assertEqual(got[0], {"name": "dns", "uuid": "u1", "host": "xen2", "macs": ["aa:bb:cc:00:00:01", "aa:bb:cc:00:00:02"], "ips": ["10.0.0.7"]})
        self.assertEqual(got[1]["host"], "xen1")  # halted: where it prefers to run
        # One member: a halted VM with no affinity is on it.
        one = {"OpaqueRef:h1": HOSTS["OpaqueRef:h1"]}
        self.assertEqual([e["name"] for e in xc.vm_nics(vms, one, vifs, gms)], ["dns", "media", "nowhere"])
        # A pool that refused the VIF and guest calls: nothing to place, nothing broken.
        self.assertEqual(xc.vm_nics(vms, HOSTS, {}, {}), [])


if __name__ == "__main__":
    unittest.main()

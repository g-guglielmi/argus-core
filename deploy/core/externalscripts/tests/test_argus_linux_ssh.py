#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# Tests for argus_linux_ssh.py's CPU utilisation (cpu_shares): two /proc/stat samples a second
# apart, each counter's change taken on its own, a counter that went backwards counted as no time
# (the kernel documents that iowait can decrease), the result held to 0-100. Stdlib only, no SSH.
import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "..", "argus_linux_ssh.py")
spec = importlib.util.spec_from_file_location("argus_linux_ssh", SCRIPT)
ssh = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ssh)


def stat(user, nice, system, idle, iowait, irq=0, softirq=0, steal=0):
    return ssh.parse_stat("cpu  %d %d %d %d %d %d %d %d 0 0\ncpu0 1 2 3 4 5 6 7 8 0 0\n"
                          % (user, nice, system, idle, iowait, irq, softirq, steal))


class CpuSharesTest(unittest.TestCase):
    def test_plain_load(self):
        # 400 ticks: 100 busy, 300 idle -> 25 %.
        s = ssh.cpu_shares(stat(1000, 0, 0, 5000, 100), stat(1080, 0, 20, 5280, 120))
        self.assertEqual(s["util"], 25.0)
        self.assertEqual(s["iowait"], 5.0)
        self.assertEqual(s["steal"], 0.0)

    def test_iowait_going_backwards_stays_within_100(self):
        # A sample like the one that read 112 %: iowait dropped by 60 between the two reads on a
        # busy host. Summed, idle+iowait moved by -48 and the old formula said 114 %. Counted per
        # counter, the drop is no time, and the share stays inside 0-100.
        s = ssh.cpu_shares(stat(1000, 0, 0, 5000, 900), stat(1300, 0, 88, 5012, 840))
        self.assertLessEqual(s["util"], 100.0)
        self.assertGreaterEqual(s["util"], 0.0)
        self.assertEqual(s["iowait"], 0.0)

    def test_overshoot_after_the_drop_is_not_negative(self):
        # The next sample: iowait jumps back up past where it was, idle+iowait grows by more
        # than the total did when summed (the 0 % dip after the spike).
        s = ssh.cpu_shares(stat(1300, 0, 88, 5012, 840), stat(1310, 0, 90, 5100, 1200))
        self.assertGreaterEqual(s["util"], 0.0)
        self.assertLessEqual(s["util"], 100.0)

    def test_unusable_samples(self):
        self.assertIsNone(ssh.cpu_shares(None, stat(1, 0, 0, 1, 0))["util"])
        same = stat(1000, 0, 0, 5000, 100)
        self.assertIsNone(ssh.cpu_shares(same, same)["util"])
        self.assertIsNone(ssh.parse_stat("intr 1 2 3\n"))


if __name__ == "__main__":
    unittest.main()

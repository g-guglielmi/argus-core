#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# Tests for argus_speedtest.py: Cloudflare's server time read off its Server-Timing header, the
# arguments' bounds, the throughput sum after the warm-up, and the reason printed when it can't reach
# the test. Stdlib only, no network.
import contextlib
import importlib.util
import io
import json
import os
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "..", "argus_speedtest.py")
spec = importlib.util.spec_from_file_location("argus_speedtest", SCRIPT)
st = importlib.util.module_from_spec(spec)
spec.loader.exec_module(st)


class FakeResp:
    def __init__(self, timing):
        self.timing = timing

    def getheader(self, name):
        return self.timing if name == "Server-Timing" else None


class ParseTest(unittest.TestCase):
    def test_server_time(self):
        self.assertEqual(st.server_ms(FakeResp('cfSpeedEdge;dur=3, cfSpeedWorker;dur=22, cfL4;desc="?proto=TCP&rtt=11667"')), 25.0)
        self.assertEqual(st.server_ms(FakeResp("cfRequestDuration;dur=12.5")), 12.5)
        self.assertEqual(st.server_ms(FakeResp(None)), 0.0)
        self.assertEqual(st.server_ms(FakeResp("cfSpeedEdge;dur=x")), 0.0)

    def test_refusals_say_why(self):
        self.assertIn("for about 60 minutes (HTTP 429)", str(st.Refused(429, "Too Many Requests", "3580")))
        self.assertIn("(HTTP 429)", str(st.Refused(429, "Too Many Requests", "")))
        self.assertEqual(str(st.Refused(403, "Forbidden")), "speed.cloudflare.com answered HTTP 403 Forbidden")

    def test_provider_names(self):
        self.assertEqual(st.holder_name("ASN-EXAMPLENET Example Telecom S.p.A."), "Example Telecom S.p.A.")
        self.assertEqual(st.holder_name("CLOUDFLARENET - Cloudflare, Inc."), "Cloudflare, Inc.")
        self.assertEqual(st.holder_name("Example Telecom"), "Example Telecom")
        self.assertEqual(st.holder_name(None), "")

    def test_args(self):
        old = st.sys.argv
        try:
            st.sys.argv = ["argus_speedtest.py", "99", "abc"]
            self.assertEqual((st.arg_int(1, 8, 3, 15), st.arg_int(2, 8, 1, 16)), (15, 8))
            st.sys.argv = ["argus_speedtest.py", "", "0"]
            self.assertEqual((st.arg_int(1, 8, 3, 15), st.arg_int(2, 8, 1, 16)), (8, 1))
        finally:
            st.sys.argv = old

    def test_throughput_after_warmup(self):
        ph = st.Phase(1)
        ph.warm = ph.start  # no warm-up for the test
        ph.add(1000)  # the first byte past the warm-up marks it
        ph.add(125_000)
        time.sleep(0.05)
        bps = ph.bps()
        self.assertIsNotNone(bps)
        self.assertGreater(bps, 0)
        self.assertIsNone(st.Phase(1).bps(), "nothing moved past the warm-up: no rate")


class MainTest(unittest.TestCase):
    def test_unreachable(self):
        old = st.HOST
        st.HOST = "speed.invalid"
        out = io.StringIO()
        try:
            with contextlib.redirect_stdout(out):
                st.main()
        finally:
            st.HOST = old
        d = json.loads(out.getvalue())
        self.assertIn("could not reach speed.invalid", d["error"])
        self.assertIsNone(d["down_bps"])
        self.assertEqual(set(d), {"down_bps", "up_bps", "latency_ms", "jitter_ms", "loaded_down_ms", "loaded_up_ms", "ip", "isp", "colo", "city", "error"})


if __name__ == "__main__":
    unittest.main(verbosity=1)

#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# Tests for argus_speedtest.py: Cloudflare's server time read off its Server-Timing header, the
# arguments' bounds, the rate from the warm-up to the first stream done, the reason printed when it
# can't reach the test, and the Ookla engine: its result read in this collector's terms, its errors,
# and its CLI installed only when the download matches the pinned checksum. Stdlib only, no network.
import contextlib
import hashlib
import importlib.util
import io
import json
import os
import tarfile
import tempfile
import threading
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
        self.assertIn("for about a minute (HTTP 429)", str(st.Refused(429, "Too Many Requests", "50")))
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
        ph = st.Phase(1, 100_000_000)
        ph.warm = ph.start  # no warm-up for the test
        ph.add(1000)  # the first byte past the warm-up marks it
        ph.add(125_000)
        time.sleep(0.05)
        bps = ph.bps()
        self.assertIsNotNone(bps)
        self.assertGreater(bps, 0)
        self.assertIsNone(st.Phase(1, 100_000_000).bps(), "nothing moved: no rate")

    def test_rate_from_warmup_to_first_stream_done(self):
        ph = st.Phase(5, 1_000_000)
        ph.add(300_000)
        ph.add(100_000)  # a quarter of the run's data had moved: the warm-up ends before its second
        self.assertEqual(ph.at_warm[0], 300_000)
        time.sleep(0.3)
        ph.add(500_000)
        ph.done()  # a stream moved its share
        ph.add(2_000_000)  # what the others move after it, with the line no longer full: left out
        self.assertTrue(ph.over.is_set())
        (b0, t0), (b1, t1) = ph.at_warm, ph.at_done
        self.assertEqual(b1, 900_000)
        self.assertAlmostEqual(ph.bps(), (b1 - b0) * 8 / (t1 - t0))

    def test_done_within_warmup_counts_from_start(self):
        ph = st.Phase(5, 100_000_000)
        ph.add(1000)
        ph.done()
        self.assertIsNone(ph.at_warm)
        self.assertIsNotNone(ph.bps())

    def test_pinger_survives_a_failed_round_trip(self):
        class Conn:
            closed = 0

            def close(self):
                Conn.closed += 1

        n = {"pings": 0}

        def ping(conn):
            n["pings"] += 1
            if n["pings"] == 3:  # the second sample: refused under the load
                raise st.Refused(429, "Too Many Requests", "60")
            return 20.0

        ph = st.Phase(5, 1_000_000)
        ph.at_warm = (0, ph.start)
        with patched(st, "connect", Conn), patched(st, "ping", ping), patched(st, "LOADED_EVERY", 0.01):
            t = threading.Thread(target=ph.pinger)
            t.start()
            time.sleep(0.3)
            ph.over.set()
            t.join(2)
        self.assertFalse(t.is_alive())
        self.assertGreater(len(ph.loaded), 3, "sampling went on past the failed round trip")
        self.assertGreaterEqual(Conn.closed, 2, "the failed connection was replaced, and the last one closed")

    def test_budget_bounds_a_run(self):
        self.assertLess(st.DOWN_REQUEST, 100_000_000, "Cloudflare refuses 100 MB a request")
        self.assertLessEqual(st.DOWN_BUDGET, 970_000_000, "no more than one test on Cloudflare's page")
        self.assertLessEqual(st.UP_BUDGET, 300_000_000, "no more than one test on Cloudflare's page")


# One result as Ookla's CLI prints it (--format=json), with documentation addresses.
OOKLA_RESULT = {
    "type": "result", "timestamp": "2026-10-04T15:00:00Z",
    "ping": {"jitter": 0.4, "latency": 3.2, "low": 2.9, "high": 3.6},
    "download": {"bandwidth": 293_750_000, "bytes": 3_000_000_000, "elapsed": 10_000,
                 "latency": {"iqm": 12.5, "low": 3.1, "high": 40.2, "jitter": 2.0}},
    "upload": {"bandwidth": 117_500_000, "bytes": 1_200_000_000, "elapsed": 10_000,
               "latency": {"iqm": 30.25, "low": 3.0, "high": 90.1, "jitter": 5.0}},
    "packetLoss": 0.5, "isp": "Example Telecom",
    "interface": {"internalIp": "10.0.0.20", "name": "eth0", "macAddr": "00:00:5E:00:53:01", "isVpn": False,
                  "externalIp": "192.0.2.10"},
    "server": {"id": 12345, "host": "speedtest.example.net", "port": 8080, "name": "Example ISP",
               "location": "Milan", "country": "Italy", "ip": "198.51.100.5"},
    "result": {"id": "x", "url": "https://www.speedtest.net/result/c/x", "persisted": True},
}


class FakeDownload:
    def __init__(self, data):
        self.data = data

    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False

    def read(self, n=-1):
        return self.data


@contextlib.contextmanager
def patched(obj, name, value):
    old = getattr(obj, name)
    setattr(obj, name, value)
    try:
        yield
    finally:
        setattr(obj, name, old)


class OoklaTest(unittest.TestCase):
    def test_result_in_collector_terms(self):
        out = {"error": ""}
        st.ookla_fill(out, OOKLA_RESULT)
        self.assertEqual(out["down_bps"], 2_350_000_000)  # bytes a second x 8
        self.assertEqual(out["up_bps"], 940_000_000)
        self.assertEqual((out["latency_ms"], out["jitter_ms"]), (3.2, 0.4))
        self.assertEqual((out["loaded_down_ms"], out["loaded_up_ms"]), (12.5, 30.25))
        self.assertEqual(out["loss_pct"], 0.5)
        self.assertEqual((out["ip"], out["isp"]), ("192.0.2.10", "Example Telecom"))
        self.assertEqual(out["colo"], "Example ISP, Milan (server 12345)")
        self.assertEqual(out["error"], "")

    def test_missing_direction_says_so(self):
        r = dict(OOKLA_RESULT)
        del r["upload"]
        r.pop("packetLoss")
        out = {"error": ""}
        st.ookla_fill(out, r)
        self.assertIsNone(out["up_bps"])
        self.assertIsNone(out["loss_pct"], "no packet loss from a server that doesn't measure it")
        self.assertEqual(out["error"], "Ookla's test reported no upload speed")

    def test_errors_say_why(self):
        line = json.dumps({"type": "log", "timestamp": "2026-10-04T15:00:00Z", "level": "error",
                           "message": "No servers defined (NoServersException)"})
        with self.assertRaisesRegex(st.OoklaError, "No servers defined"):
            st.ookla_parse("", line + "\n", 2)
        with self.assertRaisesRegex(st.OoklaError, "Cannot read"):
            st.ookla_parse("", "[error] Cannot read: Resource temporarily unavailable\n", 1)
        with self.assertRaisesRegex(st.OoklaError, r"ended \(code 1\) with no result"):
            st.ookla_parse("", "", 1)
        self.assertEqual(st.ookla_parse(json.dumps(OOKLA_RESULT) + "\n", "", 0)["isp"], "Example Telecom")

    def test_no_build_for_processor(self):
        with patched(st.platform, "machine", lambda: "sparc64"):
            with self.assertRaisesRegex(st.OoklaError, r"no build for this probe's processor \(sparc64\)"):
                st.ookla_binary(tempfile.mkdtemp())

    def test_download_must_match_checksum(self):
        d = tempfile.mkdtemp()
        with patched(st.platform, "machine", lambda: "x86_64"), \
                patched(st.urllib.request, "urlopen", lambda *a, **k: FakeDownload(b"not ookla")):
            with self.assertRaisesRegex(st.OoklaError, "didn't match its checksum"):
                st.ookla_binary(d)
        self.assertEqual(os.listdir(d), [], "nothing installed")

    def test_installs_the_program_once(self):
        prog = b"#!/bin/sh\necho ookla\n"
        buf = io.BytesIO()
        with tarfile.open(fileobj=buf, mode="w:gz") as tf:
            for name, data in (("speedtest.md", b"readme"), ("speedtest", prog)):
                ti = tarfile.TarInfo(name)
                ti.size = len(data)
                tf.addfile(ti, io.BytesIO(data))
        archive = buf.getvalue()
        d = tempfile.mkdtemp()
        sums = dict(st.OOKLA_SHA256, x86_64=hashlib.sha256(archive).hexdigest())
        calls = []

        def fetch(*a, **k):
            calls.append(a)
            return FakeDownload(archive)

        with patched(st.platform, "machine", lambda: "x86_64"), patched(st, "OOKLA_SHA256", sums), \
                patched(st.urllib.request, "urlopen", fetch):
            path = st.ookla_binary(d)
            self.assertEqual(path, os.path.join(d, "speedtest-%s-x86_64" % st.OOKLA_VERSION))
            with open(path, "rb") as f:
                self.assertEqual(f.read(), prog)
            self.assertEqual(st.ookla_binary(d), path)
        self.assertEqual(len(calls), 1, "downloaded once, then kept")

    def test_pins_every_build(self):
        self.assertEqual(set(st.OOKLA_ARCH.values()), set(st.OOKLA_SHA256))
        for v in st.OOKLA_SHA256.values():
            self.assertRegex(v, "^[0-9a-f]{64}$")


class MainTest(unittest.TestCase):
    def test_ookla_engine(self):
        def refuse(server):
            self.assertEqual(server, "12345")
            raise st.OoklaError("Ookla's test failed: No servers defined (NoServersException)")

        old = st.sys.argv
        out = io.StringIO()
        try:
            st.sys.argv = ["argus_speedtest.py", "8", "8", "ookla", "12345"]
            with patched(st, "ookla_run", refuse), contextlib.redirect_stdout(out):
                st.main()
        finally:
            st.sys.argv = old
        d = json.loads(out.getvalue())
        self.assertEqual(d["engine"], "ookla")
        self.assertIn("No servers defined", d["error"])

    def test_server_must_be_a_number(self):
        seen = []
        old = st.sys.argv
        try:
            st.sys.argv = ["argus_speedtest.py", "8", "8", "OOKLA", "--help"]
            with patched(st, "ookla_run", lambda server: seen.append(server) or OOKLA_RESULT), \
                    contextlib.redirect_stdout(io.StringIO()):
                st.main()
        finally:
            st.sys.argv = old
        self.assertEqual(seen, [""], "an option-looking server is dropped, never passed on")

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
        self.assertEqual(set(d), {"down_bps", "up_bps", "latency_ms", "jitter_ms", "loaded_down_ms", "loaded_up_ms", "ip", "isp", "colo", "city", "loss_pct", "engine", "error"})
        self.assertEqual(d["engine"], "cloudflare")


if __name__ == "__main__":
    unittest.main(verbosity=1)

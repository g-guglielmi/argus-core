#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# Tests for argus_http.py against real local servers: plain HTTP, and HTTPS with a self-signed
# certificate made by openssl for the run. Stdlib + the openssl command.
import importlib.util
import json
import os
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "..", "argus_http.py")
spec = importlib.util.spec_from_file_location("argus_http", SCRIPT)
ah = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ah)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):
        pass

    def do_GET(self):
        p = self.path
        if p == "/ok":
            self.reply(200, b"<html>Welcome back, Argus</html>")
        elif p == "/bad":
            self.reply(502, b"upstream down")
        elif p == "/moved":
            self.send_response(302)
            self.send_header("Location", "/ok")
            self.send_header("Content-Length", "0")
            self.end_headers()
        elif p == "/loop":
            self.send_response(301)
            self.send_header("Location", "/loop")
            self.send_header("Content-Length", "0")
            self.end_headers()
        elif p == "/slow":
            time.sleep(2)
            self.reply(200, b"late")
        else:
            self.reply(404, b"nope")

    def reply(self, code, body):
        self.send_response(code)
        self.send_header("Content-Type", "text/html")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def serve(tls_ctx=None):
    srv = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    srv.handle_error = lambda *a: None  # a client that gave up (the timeout test) is not news
    if tls_ctx:
        srv.socket = tls_ctx.wrap_socket(srv.socket, server_side=True)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, srv.server_address[1]


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class ParseTest(unittest.TestCase):
    def test_urls(self):
        es = ah.parse_urls("https://portal.example.com/app, /login  http://10.0.0.20:8080/health#ok%20now /x#!Error", "10.0.0.10", "https", 8443)
        self.assertEqual([e.url for e in es], ["https://portal.example.com/app", "https://10.0.0.10:8443/login",
                                              "http://10.0.0.20:8080/health", "https://10.0.0.10:8443/x"])
        self.assertEqual([e.name for e in es], ["portal.example.com/app", "10.0.0.10:8443/login",
                                               '10.0.0.20:8080/health with "ok now"', '10.0.0.10:8443/x without "Error"'])
        self.assertEqual((es[2].text, es[2].absent, es[3].text, es[3].absent), ("ok now", False, "Error", True))
        self.assertEqual([e.tls for e in es], [True, True, False, True])
        self.assertEqual(len({e.id for e in es}), 4, "each URL has its own id")
        self.assertEqual(ah.parse_urls("https://a.example.com/x", "h", "https", 443)[0].id,
                         ah.parse_urls("https://a.example.com/x", "other", "http", 80)[0].id, "the id stays the same")

    def test_blank_list_checks_the_host(self):
        es = ah.parse_urls("  ", "10.0.0.10", "https", 443)
        self.assertEqual((es[0].url, es[0].name), ("https://10.0.0.10/", "10.0.0.10"))
        es = ah.parse_urls("", "fe80::1", "http", 8080)
        self.assertEqual(es[0].url, "http://[fe80::1]:8080/")

    def test_bad_lists(self):
        for bad in ['https://a.example.com/"x"', "ftp://a.example.com/", "https://a.example.com/$(id)", "portal.example.com",
                    "https://user:pw@a.example.com/", "https://a.example.com:99999/", ",".join("/p%d" % i for i in range(20))]:
            with self.assertRaises(ValueError, msg=bad):
                ah.parse_urls(bad, "10.0.0.10", "https", 443)
        self.assertEqual(len(ah.parse_urls("/a /a, /a", "h", "https", 443)), 1, "duplicates are dropped")

    def test_codes(self):
        self.assertEqual(ah.parse_codes(""), [(200, 299)])
        self.assertEqual(ah.parse_codes("200-399, 401"), [(200, 399), (401, 401)])
        for bad in ["2xx", "600", "200-", "abc"]:
            with self.assertRaises(ValueError):
                ah.parse_codes(bad)


class HTTPTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv, cls.port = serve()

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()
        cls.srv.server_close()

    def check(self, entry, codes="200-299", timeout=5):
        es = ah.parse_urls(entry, "127.0.0.1", "http", self.port)
        return ah.run(es, ah.parse_codes(codes), True, timeout)[0]

    def test_up(self):
        r = self.check("/ok")
        self.assertEqual((r["up"], r["status"], r["error"], r["cert_days"]), (1, 200, "", None))
        self.assertGreater(r["time"], 0)

    def test_status(self):
        r = self.check("/bad")
        self.assertEqual((r["up"], r["status"], r["error"]), (0, 502, "returned 502 Bad Gateway"))
        self.assertEqual(self.check("/bad", "200-299,502")["up"], 1, "an accepted code")

    def test_text(self):
        self.assertEqual(self.check("/ok#welcome%20BACK")["up"], 1, "case-insensitive")
        r = self.check("/ok#Goodbye")
        self.assertEqual((r["up"], r["error"]), (0, 'the page does not contain "Goodbye"'))
        r = self.check("/ok#!Argus")
        self.assertEqual((r["up"], r["error"]), (0, 'the page contains "Argus"'))

    def test_redirects(self):
        r = self.check("/moved#Welcome")
        self.assertEqual((r["up"], r["status"]), (1, 200), "followed to the page")
        r = self.check("/loop")
        self.assertEqual((r["up"], r["error"]), (0, "more than 5 redirects"))

    def test_not_answering(self):
        es = ah.parse_urls("http://127.0.0.1:%d/" % free_port(), "h", "http", 80)
        r = ah.run(es, ah.parse_codes(""), True, 3)[0]
        self.assertEqual(r["up"], 0)
        self.assertIn("connection refused", r["error"])
        r = self.check("/slow", timeout=1)
        self.assertEqual(r["up"], 0)
        self.assertIn("no answer within 1 s", r["error"])


@unittest.skipUnless(shutil.which("openssl"), "needs openssl")
class HTTPSTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.tmp = tempfile.mkdtemp()
        cls.crt, key = os.path.join(cls.tmp, "c.pem"), os.path.join(cls.tmp, "k.pem")
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "30", "-subj", "/CN=localhost",
                        "-keyout", key, "-out", cls.crt], check=True, capture_output=True)
        ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        ctx.load_cert_chain(cls.crt, key)
        cls.srv, cls.port = serve(ctx)

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()
        cls.srv.server_close()
        shutil.rmtree(cls.tmp, ignore_errors=True)

    def test_der_not_after(self):
        with open(self.crt) as f:
            der = ssl.PEM_cert_to_DER_cert(f.read())
        out = subprocess.run(["openssl", "x509", "-in", self.crt, "-noout", "-enddate"], capture_output=True, text=True, check=True).stdout
        want = ssl.cert_time_to_seconds(out.strip().split("=", 1)[1])
        self.assertEqual(ah.der_not_after(der), want)

    def test_self_signed(self):
        es = ah.parse_urls("https://127.0.0.1:%d/ok" % self.port, "h", "https", 443)
        r = ah.run(es, ah.parse_codes(""), True, 5)[0]
        self.assertEqual(r["up"], 0)
        self.assertTrue(r["error"].startswith("the certificate is not trusted") or r["error"] == "the certificate is for another name", r["error"])
        self.assertAlmostEqual(r["cert_days"], 30, delta=1, msg="its expiry is read anyway")
        r = ah.run(es, ah.parse_codes(""), False, 5)[0]
        self.assertEqual((r["up"], r["status"], r["error"]), (1, 200, ""), "ignore accepts it")
        self.assertAlmostEqual(r["cert_days"], 30, delta=1)


class MainTest(unittest.TestCase):
    def test_output(self):
        srv, port = serve()
        try:
            out = subprocess.run([sys.executable, SCRIPT, "127.0.0.1", "/ok, /bad", "http", str(port), "", "verify", "5"],
                                 capture_output=True, text=True, check=True).stdout
        finally:
            srv.shutdown()
            srv.server_close()
        d = json.loads(out)
        self.assertEqual(d["error"], "")
        self.assertEqual([u["up"] for u in d["urls"]], [1, 0])
        self.assertEqual([x["{#URLNAME}"] for x in d["url_discovery"]], ["127.0.0.1:%d/ok" % port, "127.0.0.1:%d/bad" % port])
        self.assertEqual(d["tls_discovery"], [], "no certificate on plain http")
        self.assertEqual(d["urls"][0]["id"], d["url_discovery"][0]["{#URLID}"])

    def test_bad_list_is_the_error(self):
        out = subprocess.run([sys.executable, SCRIPT, "10.0.0.10", "ftp://x"], capture_output=True, text=True, check=True).stdout
        d = json.loads(out)
        self.assertTrue(d["error"].startswith("the URL settings are not valid"), d["error"])
        self.assertEqual((d["urls"], d["url_discovery"]), ([], []))


if __name__ == "__main__":
    unittest.main(verbosity=1)

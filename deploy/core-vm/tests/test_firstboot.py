#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi
"""Tests for the first-boot setup page (files/argus-core-firstboot.py): the parts that need no VM.

Run: python3 -m unittest discover -s deploy/core-vm/tests
"""
import contextlib
import datetime
import importlib.util
import io
import json
import os
import shutil
import sys
import tempfile
import threading
import time
import types
import unittest
import urllib.error
import urllib.parse
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "..", "files", "argus-core-firstboot.py")


def load():
    spec = importlib.util.spec_from_file_location("argus_core_firstboot", SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    sys.dont_write_bytecode = True
    spec.loader.exec_module(mod)
    return mod


FB = load()

WINTER = datetime.datetime(2026, 1, 15, 12, 0, tzinfo=datetime.timezone.utc)
SUMMER = datetime.datetime(2026, 7, 15, 12, 0, tzinfo=datetime.timezone.utc)


class TimezoneListTest(unittest.TestCase):
    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, ignore_errors=True)

    def zone(self, name):
        path = os.path.join(self.root, *name.split("/"))
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "wb") as f:
            f.write(b"TZif")

    def zone_tab(self, *names):
        with open(os.path.join(self.root, "zone.tab"), "w", encoding="utf-8") as f:
            f.write("# country\tcoordinates\tTZ\tcomments\n")
            for i, n in enumerate(names):
                f.write("X%d\t+0000+00000\t%s%s\n" % (i, n, "\tsome comment" if i % 2 else ""))

    def test_the_list_is_the_databases_own(self):
        for n in ("Europe/Rome", "Europe/Oslo", "Asia/Kolkata", "America/Argentina/Buenos_Aires", "UTC"):
            self.zone(n)
        # A row whose file is not there is not offered: the form would refuse it.
        self.zone_tab("Europe/Rome", "Europe/Oslo", "Asia/Kolkata", "America/Argentina/Buenos_Aires",
                      "Pacific/Nowhere")
        self.assertEqual(FB.zone_names(self.root),
                         ["America/Argentina/Buenos_Aires", "Asia/Kolkata", "Europe/Oslo", "Europe/Rome"])

    def test_without_zone_tab_every_zone_under_the_areas(self):
        for n in ("Europe/Rome", "America/Indiana/Knox", "Etc/GMT+5", "posix/Europe/Rome", "US/Eastern",
                  "Europe/readme.txt", "UTC"):
            self.zone(n)
        self.assertEqual(FB.zone_names(self.root), ["America/Indiana/Knox", "Europe/Rome"])

    def test_without_a_database_the_short_list(self):
        names = FB.zone_names(os.path.join(self.root, "missing"))
        self.assertIn("Europe/Rome", names)
        self.assertNotIn("UTC", names)
        html = FB.timezone_options("UTC", os.path.join(self.root, "missing"))
        self.assertIn('<option value="UTC" selected>UTC</option>', html)

    def test_options_are_grouped_and_carry_todays_offset(self):
        for n in ("Europe/Rome", "Asia/Kolkata", "America/Argentina/Buenos_Aires", "America/New_York"):
            self.zone(n)
        self.zone_tab("Europe/Rome", "Asia/Kolkata", "America/Argentina/Buenos_Aires", "America/New_York")
        html = FB.timezone_options("Europe/Rome", self.root, WINTER)
        self.assertTrue(html.startswith('<option value="UTC">UTC</option>'))
        self.assertIn('<optgroup label="Europe">', html)
        self.assertIn('<option value="Europe/Rome" selected>Rome (UTC+01:00)</option>', html)
        self.assertIn('<option value="Asia/Kolkata">Kolkata (UTC+05:30)</option>', html)
        self.assertIn('<option value="America/Argentina/Buenos_Aires">Argentina, Buenos Aires (UTC-03:00)</option>', html)
        self.assertIn('<option value="America/New_York">New York (UTC-05:00)</option>', html)
        self.assertEqual(html.count(" selected"), 1)
        # The areas in order, and the places in order inside one.
        self.assertLess(html.index('label="America"'), html.index('label="Asia"'))
        self.assertLess(html.index('label="Asia"'), html.index('label="Europe"'))
        self.assertLess(html.index("Argentina, Buenos Aires"), html.index("New York"))
        # Daylight saving moves the offset the page shows.
        self.assertIn(">Rome (UTC+02:00)<", FB.timezone_options("UTC", self.root, SUMMER))

    def test_a_chosen_zone_outside_the_list_keeps_its_row(self):
        self.zone("Europe/Rome")
        self.zone_tab("Europe/Rome")
        html = FB.timezone_options("Asia/Calcutta", self.root, WINTER)
        self.assertTrue(html.startswith('<option value="Asia/Calcutta" selected>Asia/Calcutta</option>'))
        self.assertEqual(html.count(" selected"), 1)

    def test_only_a_real_zone_file_is_a_zone(self):
        self.zone("Europe/Rome")
        self.zone("UTC")
        self.assertTrue(FB.valid_zone("Europe/Rome", self.root))
        self.assertTrue(FB.valid_zone("UTC", self.root))
        for bad in ("", None, "Europe", "Europe/Nowhere", "../Europe/Rome", "Europe/../UTC", "/etc/passwd",
                    "Europe/Rome/", "a/b/c/d", "Europe/Ro me", "zone.tab"):
            self.assertFalse(FB.valid_zone(bad, self.root), bad)


class FormPageTest(unittest.TestCase):
    def setUp(self):
        self.ip = FB.primary_ip
        FB.primary_ip = lambda: "10.0.0.10"
        self.addCleanup(setattr, FB, "primary_ip", self.ip)

    def test_a_fresh_form_starts_on_the_browsers_zone_and_an_answered_one_does_not(self):
        fresh = FB.form_page(csrf="x")
        self.assertIn('<select id="tz" name="timezone" data-auto="1">', fresh)
        self.assertIn("Intl.DateTimeFormat().resolvedOptions().timeZone", fresh)
        self.assertNotIn("{{", fresh)
        self.assertNotIn("{TZ_SCRIPT}", fresh)
        # The search field is the script's to show: without scripts the list is a plain drop-down.
        self.assertIn('<input id="tzq" type="search"', fresh)
        self.assertRegex(fresh, r'<input id="tzq"[^>]* hidden>')
        self.assertIn(r"/\(UTC[^)]*\)$/", fresh)
        answered = FB.form_page(vals={"timezone": "Europe/Rome"}, csrf="x")
        self.assertIn('<select id="tz" name="timezone">', answered)
        self.assertIn('<option value="Europe/Rome" selected>', answered)


class SetupCodeTest(unittest.TestCase):
    def setUp(self):
        self.announced = []
        for name, fake in (("announce_code", self.announced.append), ("gen_setup_code", lambda: "NEWC-ODE9")):
            self.addCleanup(setattr, FB, name, getattr(FB, name))
            setattr(FB, name, fake)
        self.addCleanup(setattr, FB.time, "sleep", FB.time.sleep)
        FB.time.sleep = lambda s: None
        self.handler = types.SimpleNamespace(server=types.SimpleNamespace(setup_code="ACDE-FHJK", code_failures=0))

    def check(self, typed):
        return FB.Handler._code_error(self.handler, {"setup_code": [typed]})

    def test_no_character_of_a_code_reads_like_another(self):
        for alike in "0O1IL2Z5S6G8B":
            self.assertNotIn(alike, FB.CODE_ALPHABET)
        self.assertEqual(len(set(FB.CODE_ALPHABET)), len(FB.CODE_ALPHABET))
        self.assertGreaterEqual(len(FB.CODE_ALPHABET), 20)

    def test_the_hyphen_spaces_and_capitals_do_not_matter(self):
        for typed in ("ACDE-FHJK", "ACDEFHJK", "acde-fhjk", " acde fhjk ", "ACDE - FHJK"):
            self.assertEqual(self.check(typed), "", typed)
        for typed in ("", "ACDE-FHJ", "ACDE-FHJKK", "XCDE-FHJK", "ACDEéFHJK"):
            self.assertIn("isn't right", self.check(typed), typed)
            self.handler.server.code_failures = 0

    def test_after_too_many_misses_a_new_code_and_the_page_says_so(self):
        for _ in range(FB.CODE_MAX_FAILURES - 1):
            self.assertIn("isn't right", self.check("XXXX-XXXX"))
        self.assertEqual(self.announced, [])
        self.assertIn("made a new one", self.check("XXXX-XXXX"))
        self.assertEqual(self.announced, ["NEWC-ODE9"])
        self.assertIn("isn't right", self.check("ACDE-FHJK"))
        self.assertEqual(self.check("NEWC-ODE9"), "")

    def test_a_right_code_forgives_the_misses_before_it(self):
        for _ in range(FB.CODE_MAX_FAILURES - 1):
            self.check("XXXX-XXXX")
        self.assertEqual(self.check("ACDE-FHJK"), "")
        self.assertEqual(self.handler.server.code_failures, 0)


INFO = {"archive": "argus-backup-core1-20261122T013016Z.tar.gpg", "host": "core1", "created_at": "20261122T013016Z",
        "made_at": 1795311016, "argus_version": "v0.9.29", "history": True,
        "parts": ["argus.db.gz", "containers.json", "files.tar.gz", "network.json", "postgres-globals.sql", "zabbix.dump"],
        "versions": {"zabbix-server-pgsql": "1:7.0.32-2+debian13"}, "here": {},
        "network": {"addresses": ["10.0.0.10/24"], "gateway": "10.0.0.1", "dhcp": False, "hostname": "core1"},
        "timescaledb": {"here": "2.28.3", "wanted": "2.30.3", "installs": "2.30.3"}, "problems": []}
CODE = "ACDE-FHJK"


class Browser:
    """One browser at the page: it keeps the cookie it is given."""

    def __init__(self, base):
        self.base, self.cookie = base, ""

    def ask(self, method, path, form=None, body=None, headers=None):
        data = body if body is not None else (urllib.parse.urlencode(form).encode() if form is not None else None)
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=dict(headers or {}))
        if self.cookie:
            req.add_header("Cookie", self.cookie)
        if form is not None:
            req.add_header("Content-Type", "application/x-www-form-urlencoded")

        class NoRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, *a, **kw):
                return None
        try:
            resp = urllib.request.build_opener(NoRedirect).open(req, timeout=20)
        except urllib.error.HTTPError as e:
            resp = e
        for k, v in resp.headers.items():
            if k.lower() == "set-cookie":
                self.cookie = v.split(";", 1)[0]
        return resp.status if hasattr(resp, "status") else resp.code, resp.read().decode("utf-8", "replace"), resp.headers.get("Location", "")

    def get(self, path="/"):
        return self.ask("GET", path)

    def post(self, path, form):
        return self.ask("POST", path, form=form)


class RestoreFlowTest(unittest.TestCase):
    """The restore path of the first-boot page, driven over HTTP like a browser would, with stand-ins
    for the host tools and for the steps that need a VM."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        state = os.path.join(self.tmp, "state")
        os.makedirs(state)
        patches = {"STATE_DIR": state, "CONFIG_PATH": os.path.join(state, "config.json"), "STATE_PATH": os.path.join(state, "state.json"),
                   "DONE_PATH": os.path.join(state, "setup-done"), "PASS_PATH": os.path.join(state, "passphrase"),
                   "TARGET_PATH": os.path.join(state, "target.json"), "RESTORE_KEY": os.path.join(state, "restore_key"),
                   "STAGE_DIR": os.path.join(self.tmp, "stage"), "NGINX_REDIRECT": os.path.join(self.tmp, "port80.conf"),
                   "ORCH": FB.Orchestrator(), "PREP": FB.Prepare(),
                   "primary_ip": lambda: "10.0.0.20", "vm_identity": lambda: "argus-core · 10.0.0.20",
                   "announce_code": lambda code: None, "clear_code_banner": lambda: None,
                   "run": lambda args, **kw: types.SimpleNamespace(returncode=0, stdout="", stderr=""),
                   "ARGUS_ENV": os.path.join(self.tmp, "argus.env"),
                   "restore_public_key": lambda: "ssh-ed25519 AAAATESTKEY argus-first-boot-restore"}
        for name, value in patches.items():
            self.addCleanup(setattr, FB, name, getattr(FB, name))
            setattr(FB, name, value)
        with open(FB.RESTORE_KEY, "w") as f:
            f.write("PRIVATE KEY OF THE TEST\n")
        # The steps that need a VM are stand-ins that say they ran; finish is the real one.
        self.ran = []
        funcs = dict(FB.STEP_FUNCS)
        for key in funcs:
            if key != "finish":
                funcs[key] = (lambda k: lambda cfg, state: self.ran.append(k))(key)
        self.addCleanup(setattr, FB, "STEP_FUNCS", FB.STEP_FUNCS)
        FB.STEP_FUNCS = funcs
        self.tools, self.inspected = [], []
        self.info, self.listing, self.fetch_error = dict(INFO), [], ""

        def run_tool(args, on_line=None, timeout=None):
            self.tools.append(list(args))
            if "remote-list" in args:
                return 0, [json.dumps({"archives": self.listing})]
            if "remote-get" in args:
                if self.fetch_error:
                    return 1, ["argus-backup: " + self.fetch_error]
                with open(args[-1], "wb") as f:
                    f.write(b"FETCHED")
                return 0, []
            return 0, []

        def inspect_archive(archive):
            self.inspected.append(archive)
            return (self.info, "") if self.info else (None, "The archive could not be opened: gpg could not decrypt the archive (wrong passphrase?)")
        for name, value in (("run_tool", run_tool), ("inspect_archive", inspect_archive)):
            self.addCleanup(setattr, FB, name, getattr(FB, name))
            setattr(FB, name, value)
        self.httpd = FB.ThreadingHTTPServer(("127.0.0.1", 0), FB.Handler)
        self.httpd.csrf, self.httpd.setup_code, self.httpd.code_failures = "csrf-token", CODE, 0
        self.httpd.session, self.httpd.upload, self.httpd.upload_name = "", "", ""
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()
        self.addCleanup(self.httpd.server_close)
        self.addCleanup(self.httpd.shutdown)
        self.me = Browser("http://127.0.0.1:%d" % self.httpd.server_address[1])
        self.other = Browser(self.me.base)

    def form(self, **more):
        base = {"csrf": "csrf-token", "setup_code": CODE, "source": "upload", "rtype": "smb", "passphrase": "correct horse battery",
                "keymap": "it", "linux_user": "argus", "linux_password": "a-good-one", "linux_password2": "a-good-one"}
        base.update(more)
        return base

    def wait(self, what, seconds=10):
        end = time.time() + seconds
        while time.time() < end:
            if what():
                return
            time.sleep(0.05)
        self.fail("timed out waiting")

    def upload(self, name="argus-backup-core1-20261122T013016Z.tar.gpg", body=b"ARCHIVE BYTES" * 100):
        return self.me.ask("PUT", "/restore/upload", body=body, headers={"X-Csrf": "csrf-token", "X-Archive-Name": urllib.parse.quote(name)})

    def test_the_first_page_asks_what_this_vm_should_be(self):
        status, page, _ = self.me.get("/")
        self.assertEqual(status, 200)
        self.assertIn("What should this VM be?", page)
        self.assertIn('href="/?new"', page)
        self.assertIn('href="/?restore"', page)
        self.assertIn("Set up your monitoring core", self.me.get("/?new")[1])
        restore = self.me.get("/?restore")[1]
        self.assertIn("Restore a core from a backup", restore)
        for label in ("SMB share (Windows, NAS)", "NFS export", "rsync over SSH", "S3 bucket (or compatible)"):
            self.assertIn(label, restore, "the targets read as in Settings, Backups")
        self.assertIn("ssh-ed25519 AAAATESTKEY", restore)
        self.assertNotIn("{RESTORE_SCRIPT}", restore)
        self.assertNotIn("{{", restore)

    def test_a_test_restore_from_an_uploaded_archive(self):
        # Nothing of the restore path answers a browser that has not typed the setup code.
        self.assertEqual(self.upload()[0], 403)
        status, body, _ = self.me.post("/restore/auth", self.form(setup_code="XXXX-XXXX"))
        self.assertEqual(status, 403)
        self.assertIn("isn't right", json.loads(body)["error"])
        status, body, _ = self.me.post("/restore/auth", self.form(setup_code="acdefhjk"))
        self.assertEqual((status, json.loads(body)), (200, {"ok": True}))
        self.assertTrue(self.me.cookie.startswith("argus_setup="))
        # The upload: only an archive's name, streamed to the root-only folder.
        self.assertEqual(self.upload(name="../../etc/passwd")[0], 400)
        self.assertEqual(self.upload(name="notes.txt")[0], 400)
        status, body, _ = self.upload()
        self.assertEqual((status, json.loads(body)["size"]), (200, 1300))
        staged = os.path.join(FB.STAGE_DIR, "archive.tar.gpg")  # a name of the page's own, never the browser's
        with open(staged, "rb") as f:
            self.assertEqual(f.read(), b"ARCHIVE BYTES" * 100)
        self.assertEqual(os.listdir(FB.STAGE_DIR), [os.path.basename(staged)])
        # Checking it changes nothing on the VM, and shows what it holds to this browser only.
        status, _, where = self.me.post("/restore/check", self.form())
        self.assertEqual((status, where), (303, "/"))
        self.wait(lambda: FB.PREP.snapshot()["state"] == "ready")
        self.assertEqual(self.inspected, [staged])
        with open(FB.PASS_PATH) as f:
            self.assertEqual(f.read(), "correct horse battery")
        self.assertFalse(os.path.exists(FB.CONFIG_PATH), "nothing is set up before the last question")
        confirm = self.me.get("/")[1]
        self.assertIn("The archive is complete", confirm)
        self.assertIn("22 Nov 2026, 01:30 UTC, on <b>core1</b> (Argus v0.9.29)", confirm)
        self.assertIn("The Zabbix database with its metric history, the Argus database, the configuration, keys and certificates", confirm)
        self.assertIn("This VM has 2.28.3: the restore installs 2.30.3 first.", confirm)
        self.assertIn("10.0.0.10/24, gateway 10.0.0.1 (static)", confirm)
        self.assertRegex(confirm, r'name="mode" value="test" checked')
        self.assertNotRegex(confirm, r'name="mode" value="live" checked')
        self.assertNotIn("correct horse", confirm)
        elsewhere = self.other.get("/")[1]
        self.assertIn("A restore is being prepared", elsewhere)
        self.assertNotIn("core1", elsewhere)
        self.assertEqual(self.other.post("/restore/go", {"csrf": "csrf-token", "mode": "live"})[0], 303)
        self.assertFalse(os.path.exists(FB.CONFIG_PATH), "another browser cannot start the restore")
        # The last question, answered: a test restore.
        status, _, where = self.me.post("/restore/go", {"csrf": "csrf-token", "mode": "test", "take_address": "1"})
        self.assertEqual((status, where), (303, "/"))
        self.wait(lambda: os.path.exists(FB.DONE_PATH))
        self.assertEqual(self.ran, ["system", "database", "pki", "zabbix", "https", "accounts", "argus", "tools", "restore"],
                         "a test restore never takes the old core's address, whatever the form sent")
        with open(FB.DONE_PATH) as f:
            done = json.load(f)
        self.assertEqual(done["restored"], {"host": "core1", "created_at": "20261122T013016Z", "address": "", "test": True})
        last = self.me.get("/")[1]
        self.assertIn("Restore complete", last)
        self.assertIn("This VM is a test restore of core1.", last)
        self.assertIn("sudo argus-restore live", last)
        # What held the core's secrets on the way is gone.
        for gone in (FB.PASS_PATH, FB.STAGE_DIR, FB.RESTORE_KEY, FB.CONFIG_PATH):
            self.assertFalse(os.path.exists(gone), gone)

    def test_the_new_core_from_an_archive_at_a_target(self):
        self.listing = [{"name": "argus-backup-core1-20261122T013016Z.tar.gpg", "host": "core1", "ts": "20261122T013016Z", "size": 632586240, "encrypted": True},
                        {"name": "argus-backup-core1-20261121T013000Z.tar", "host": "core1", "ts": "20261121T013000Z", "size": 5000, "encrypted": False}]
        target = dict(rtype="smb", share="//nas.example.lan/backups", smb_path="argus", username="backup", smb_password="p@ss word")
        status, body, _ = self.me.post("/restore/find", self.form(**target))
        self.assertEqual(status, 200)
        found = json.loads(body)
        self.assertEqual(found["title"], "2 archives in //nas.example.lan/backups/argus, the newest first")
        self.assertEqual(found["archives"][0], {"name": "argus-backup-core1-20261122T013016Z.tar.gpg", "host": "core1",
                                               "when": "22 Nov 2026, 01:30 UTC", "size": "603 MB", "encrypted": True})
        self.assertEqual(self.tools[-1][:3], [FB.BACKUP_TOOL, "remote-list", "--from"])
        with open(FB.TARGET_PATH) as f:
            doc = json.load(f)
        self.assertEqual(doc["remote"], {"type": "smb", "share": "//nas.example.lan/backups", "path": "argus", "username": "backup", "domain": ""})
        self.assertEqual(doc["password"], "p@ss word")
        # The chosen archive is fetched, then checked.
        chosen = "argus-backup-core1-20261122T013016Z.tar.gpg"
        self.assertEqual(self.me.post("/restore/check", self.form(source="remote", archive_name=chosen, **target))[0], 303)
        self.wait(lambda: FB.PREP.snapshot()["state"] == "ready")
        get = [t for t in self.tools if "remote-get" in t][0]
        self.assertEqual(get[-2:], [chosen, os.path.join(FB.STAGE_DIR, "archive.tar.gpg")])
        self.assertFalse(os.path.exists(FB.TARGET_PATH), "the target's password is not kept after the fetch")
        confirm = self.me.get("/")[1]
        self.assertIn("Also take the old core's address <b>10.0.0.10</b>", confirm)
        self.assertNotIn("p@ss", confirm)
        # The new core, taking the old one's address.
        self.assertEqual(self.me.post("/restore/go", {"csrf": "csrf-token", "mode": "live", "take_address": "1"})[0], 303)
        self.wait(lambda: os.path.exists(FB.DONE_PATH))
        self.assertEqual(self.ran[-3:], ["tools", "restore", "address"])
        with open(FB.DONE_PATH) as f:
            self.assertEqual(json.load(f)["restored"], {"host": "core1", "created_at": "20261122T013016Z", "address": "10.0.0.10", "test": False})
        last = self.me.get("/")[1]
        self.assertIn("This VM is core1 now.", last)
        self.assertIn("https://10.0.0.10/", last)

    def test_an_rsync_target_gets_the_key_this_vm_made(self):
        self.listing = [{"name": "argus-backup-core1-20261122T013016Z.tar.gpg", "host": "core1", "ts": "20261122T013016Z", "size": -1, "encrypted": True}]
        status, body, _ = self.me.post("/restore/find", self.form(rtype="rsync", target="backup@nas.example.lan:/volume1/argus", port="2222"))
        self.assertEqual(status, 200, body)
        with open(FB.TARGET_PATH) as f:
            doc = json.load(f)
        self.assertEqual(doc["remote"], {"type": "rsync", "target": "backup@nas.example.lan:/volume1/argus", "port": 2222})
        self.assertEqual(doc["private_key"], "PRIVATE KEY OF THE TEST\n")
        self.assertEqual(json.loads(body)["archives"][0]["size"], "")
        for bad, why in ((dict(rtype="rsync", target="x@y:/z", port="99999"), "SSH port"), (dict(rtype="smb"), "Type the share"),
                         (dict(rtype="s3", endpoint="https://s3.example.com"), "endpoint, bucket and access key"), (dict(rtype="ftp"), "Choose where")):
            status, body, _ = self.me.post("/restore/find", self.form(**bad))
            self.assertEqual(status, 400, bad)
            self.assertIn(why, json.loads(body)["error"])

    def test_a_fetch_or_an_archive_that_fails_brings_the_form_back_with_the_reason(self):
        self.fetch_error = "can't reach nas.example.lan on TCP 445 (SMB)"
        chosen = "argus-backup-core1-20261122T013016Z.tar.gpg"
        self.me.post("/restore/check", self.form(source="remote", archive_name=chosen, share="//nas.example.lan/backups"))
        self.wait(lambda: FB.PREP.snapshot()["state"] == "failed")
        self.assertEqual(json.loads(self.other.get("/status")[1])["prep"], {"state": "failed", "detail": ""}, "the reason is for the browser that asked")
        back = self.me.get("/")[1]
        self.assertIn("The archive could not be fetched: can&#x27;t reach nas.example.lan on TCP 445 (SMB)", back)
        self.assertIn('value="//nas.example.lan/backups"', back, "what was typed is still there")
        self.assertRegex(back, r'name="source" value="remote" checked')
        self.assertNotIn("correct horse", back)
        self.assertNotIn("on the VM already", back, "a fetch that failed left nothing to offer again")
        # A wrong passphrase is found when the archive is checked, before anything is set up.
        self.info = None
        self.upload()
        self.me.post("/restore/check", self.form())
        self.wait(lambda: FB.PREP.snapshot()["state"] == "failed")
        self.assertIn("wrong passphrase?", self.me.get("/")[1])
        self.assertFalse(os.path.exists(FB.CONFIG_PATH))

    def test_a_check_that_failed_stays_on_the_page_and_costs_no_second_upload(self):
        # What a real browser does around the page: it asks for the page's icon too, and people reload.
        # The reason was shown once and then forgotten, so the first page came back with no word of it.
        self.info = None
        self.me.post("/restore/auth", self.form())
        self.upload()
        self.me.post("/restore/check", self.form(passphrase="a wrong one"))
        self.wait(lambda: FB.PREP.snapshot()["state"] == "failed")
        status, icon, _ = self.me.get("/favicon.ico")
        self.assertEqual(status, 404)
        self.assertNotIn("<html", icon.lower(), "only / is a page")
        for _ in range(3):
            back = self.me.get("/")[1]
            self.assertIn("Restore a core from a backup", back)
            self.assertIn("wrong passphrase?", back)
        # The archive is still on the VM and the form says so: only the passphrase is typed again.
        self.assertIn('data-kept="argus-backup-core1-20261122T013016Z.tar.gpg"', back)
        self.assertIn('data-kept-size="1.3 KB"', back)
        self.assertIn("This archive is on the VM already", back)
        self.assertNotIn("a wrong one", back)
        self.assertIn('placeholder="Typed already"', back, "the setup code is not asked for twice")
        self.assertNotIn('name="setup_code" placeholder="XXXX-XXXX"', back)
        self.assertIn('name="setup_code" placeholder="XXXX-XXXX" autocomplete="off" required', self.other.get("/?restore")[1])
        # Another browser is not held up by it, and is told nothing of it.
        elsewhere = self.other.get("/")[1]
        self.assertIn("What should this VM be?", elsewhere)
        self.assertNotIn("data-kept=\"argus", self.other.get("/?restore")[1])
        # The same archive, checked again with the right passphrase, with no upload in between.
        self.info = dict(INFO)
        self.assertEqual(self.me.post("/restore/check", self.form(setup_code=""))[0], 303)
        self.wait(lambda: FB.PREP.snapshot()["state"] == "ready")
        self.assertEqual(self.inspected, [os.path.join(FB.STAGE_DIR, "archive.tar.gpg")] * 2)
        self.assertIn("The archive is complete", self.me.get("/")[1])

    def test_back_on_the_form_after_a_failed_check_starts_over(self):
        self.info = None
        self.me.post("/restore/auth", self.form())
        self.upload()
        self.me.post("/restore/check", self.form())
        self.wait(lambda: FB.PREP.snapshot()["state"] == "failed")
        self.assertIn('href="/?choose"', self.me.get("/")[1])
        self.assertIn("What should this VM be?", self.other.get("/?choose")[1])
        self.assertEqual(FB.PREP.snapshot()["state"], "failed", "another browser's Back changes nothing")
        self.assertIn("What should this VM be?", self.me.get("/?choose")[1])
        self.assertEqual(FB.PREP.snapshot()["state"], "idle")
        self.assertIn("Set up your monitoring core", self.me.get("/?new")[1])

    def test_an_encrypted_archive_wants_its_passphrase_before_anything_else(self):
        self.me.post("/restore/auth", self.form())
        self.upload()
        status, page, _ = self.me.post("/restore/check", self.form(passphrase=""))
        self.assertEqual(status, 400)
        self.assertIn("This archive is encrypted: type its passphrase.", page)
        self.assertIn("This archive is on the VM already", page)
        chosen = "argus-backup-core1-20261122T013016Z.tar.gpg"
        status, page, _ = self.me.post("/restore/check", self.form(source="remote", archive_name=chosen, share="//nas.example.lan/backups", passphrase=""))
        self.assertEqual(status, 400)
        self.assertIn("This archive is encrypted: type its passphrase.", page)
        self.assertEqual((self.inspected, [t for t in self.tools if "remote-get" in t]), ([], []), "nothing was checked or fetched for it")
        self.assertEqual(FB.PREP.snapshot()["state"], "idle")
        # An archive that is not encrypted needs none.
        self.upload(name="argus-backup-core1-20261121T013000Z.tar")
        self.assertEqual(self.me.post("/restore/check", self.form(passphrase=""))[0], 303)
        self.wait(lambda: FB.PREP.snapshot()["state"] == "ready")
        # The form's script asks for it before the upload starts, and skips an upload already made.
        script = FB.RESTORE_SCRIPT
        self.assertIn("This archive is encrypted: type its passphrase first.", script)
        self.assertIn("if (!upload || !file) { f.submit(); return; }", script)
        self.assertIn("/\\.gpg$/i.test(name)", script)

    def test_an_archive_from_other_versions_is_not_restored(self):
        self.info = dict(INFO, problems=["Zabbix here is 1:7.2.1-1, the backup was made on 1:7.0.32-2+debian13"])
        self.me.post("/restore/auth", self.form())
        self.upload()
        self.me.post("/restore/check", self.form())
        self.wait(lambda: FB.PREP.snapshot()["state"] == "ready")
        page = self.me.get("/")[1]
        self.assertIn("This archive does not fit this VM", page)
        self.assertIn("Zabbix here is 1:7.2.1-1", page)
        self.assertNotIn('action="/restore/go"', page)
        self.me.post("/restore/go", {"csrf": "csrf-token", "mode": "test"})
        self.assertFalse(os.path.exists(FB.CONFIG_PATH), "and asking for it anyway starts nothing")
        self.assertEqual(self.me.post("/restore/cancel", {"csrf": "csrf-token"})[2], "/?restore")
        self.assertEqual(FB.PREP.snapshot()["state"], "idle")
        self.assertFalse(os.path.exists(FB.STAGE_DIR))

    def test_the_form_is_checked_before_anything_is_fetched(self):
        self.me.post("/restore/auth", self.form())
        self.upload()
        for bad, why in ((dict(linux_password2="another"), "passwords don&#x27;t match"), (dict(linux_password="short", linux_password2="short"), "at least 8"),
                         (dict(keymap="xx"), "Unknown keyboard layout"), (dict(linux_user="Root!"), "Linux username")):
            status, page, _ = self.me.post("/restore/check", self.form(**bad))
            self.assertEqual(status, 400, bad)
            self.assertIn(why, page)
        self.assertEqual(self.inspected, [])
        self.assertEqual(self.me.post("/restore/check", dict(self.form(), csrf="stale"))[0], 400)
        # Without an archive on the VM there is nothing to check.
        self.httpd.upload = ""
        status, page, _ = self.me.post("/restore/check", self.form())
        self.assertEqual(status, 400)
        self.assertIn("did not arrive", page)

    def test_a_new_core_is_set_up_as_before(self):
        form = {"csrf": "csrf-token", "setup_code": CODE, "hostname": "core9", "keymap": "us", "timezone": "UTC", "email": "admin@example.com",
                "linux_user": "argus", "public_url": "", "password": "a-good-one", "password2": "a-good-one"}
        real = FB.valid_zone
        self.addCleanup(setattr, FB, "valid_zone", real)
        FB.valid_zone = lambda name, root=None: name == "UTC"
        self.assertEqual(self.me.post("/", form)[0], 303)
        self.wait(lambda: os.path.exists(FB.DONE_PATH))
        self.assertEqual(self.ran, ["system", "database", "pki", "zabbix", "https", "accounts", "argus"])
        with open(FB.DONE_PATH) as f:
            self.assertNotIn("restored", json.load(f))
        self.assertIn("Your monitoring core is ready.", self.me.get("/")[1])


class RestoreStepsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.archive = os.path.join(self.tmp, "argus-backup-core1-20261122T013016Z.tar.gpg")
        with open(self.archive, "wb") as f:
            f.write(b"x")
        for name, value in (("PASS_PATH", os.path.join(self.tmp, "passphrase")), ("ORCH", FB.Orchestrator())):
            self.addCleanup(setattr, FB, name, getattr(FB, name))
            setattr(FB, name, value)
        self.addCleanup(setattr, FB, "run_tool", FB.run_tool)

    def test_the_steps_of_each_kind_of_run(self):
        keys = lambda cfg: [k for k, _ in FB.steps_for(cfg)]  # noqa: E731
        new = ["system", "database", "pki", "zabbix", "https", "accounts", "argus", "finish"]
        self.assertEqual(keys({}), new)
        self.assertEqual(keys({"mode": "restore", "test": True}), new[:-1] + ["tools", "restore", "finish"])
        self.assertEqual(keys({"mode": "restore", "take_address": True}), new[:-1] + ["tools", "restore", "address", "finish"])
        self.assertEqual(set(keys({"mode": "restore", "take_address": True})), set(FB.STEP_FUNCS))

    def test_the_restore_step_runs_the_tool_a_person_would(self):
        seen = {}

        def run_tool(args, on_line=None, timeout=None):
            seen["args"] = list(args)
            with contextlib.redirect_stdout(io.StringIO()):
                for line in ("gpg: AES256.CFB encrypted data", "==> stopping services", "==> recreating the Zabbix database (this can take a while with history)"):
                    on_line(line)
            seen["note"] = FB.ORCH.snapshot()["note"]
            return seen.get("rc", 0), ["==> starting services", "argus-restore: the Zabbix database did not load: its tables are not there"]
        FB.run_tool = run_tool
        with open(FB.PASS_PATH, "w") as f:
            f.write("p")
        FB.step_restore({"archive": self.archive, "test": True}, {})
        self.assertEqual(seen["args"], [FB.RESTORE_TOOL, "restore", self.archive, "--yes", "--passphrase-file", FB.PASS_PATH, "--first-boot", "--test"])
        self.assertEqual(seen["note"], "recreating the Zabbix database (this can take a while with history)", "the page says what the tool is doing")
        self.assertEqual(FB.ORCH.snapshot()["note"], "")
        os.unlink(FB.PASS_PATH)
        FB.step_restore({"archive": self.archive, "test": False}, {})
        self.assertEqual(seen["args"], [FB.RESTORE_TOOL, "restore", self.archive, "--yes", "--first-boot"], "the new core: no --test")
        # A restore that fails stops the page there, with the tool's own reason.
        seen["rc"] = 1
        with self.assertRaises(FB.StepError) as e:
            FB.step_restore({"archive": self.archive, "test": True}, {})
        self.assertEqual(str(e.exception), "the Zabbix database did not load: its tables are not there")
        with self.assertRaises(FB.StepError):
            FB.step_restore({"archive": os.path.join(self.tmp, "gone.tar.gpg")}, {})
        # The address step is the tool's network action.
        seen["rc"] = 0
        with contextlib.redirect_stdout(io.StringIO()):
            FB.step_address({"archive": self.archive}, {})
        self.assertEqual(seen["args"], [FB.RESTORE_TOOL, "network", self.archive, "--yes"])

    def test_small_things_the_page_relies_on(self):
        self.assertEqual(FB.archive_file_name("C:\\Users\\me\\argus-backup-core1-20261122T013016Z.tar.gpg"), "argus-backup-core1-20261122T013016Z.tar.gpg")
        self.assertEqual(FB.archive_file_name("my copy.tar"), "", "a name with a space is not kept")
        for bad in ("", None, ".tar", "..tar.gpg", "x" * 200 + ".tar", "a.zip", ".hidden.tar", "a/../b.tar"[:0]):
            self.assertEqual(FB.archive_file_name(bad), "", bad)
        self.assertEqual(FB.fmt_made("20261122T013016Z"), "22 Nov 2026, 01:30 UTC")
        self.assertEqual(FB.fmt_made("soon"), "soon")
        self.assertEqual([FB.fmt_size(n) for n in (-1, 512, 5000, 632586240, 3 * 1024 ** 3, "x")], ["", "512 bytes", "4.9 KB", "603 MB", "3.0 GB", ""])
        self.assertEqual(FB.tool_error(["gpg: noise", "argus-backup: the remote share is not valid", "", "trailing"]), "the remote share is not valid")
        self.assertEqual(FB.tool_error(["E: Unable to locate package", ""]), "E: Unable to locate package")
        self.assertEqual(FB.old_address(INFO), ("10.0.0.10", True))
        self.assertEqual(FB.old_address({"network": {"addresses": ["10.0.0.10/24"], "dhcp": True}}), ("10.0.0.10", False))
        self.assertEqual(FB.old_address({}), ("", False))
        self.assertEqual(os.path.basename(FB.staged_archive("anything at all/../x.tar.gpg")), "archive.tar.gpg")
        self.assertEqual(os.path.basename(FB.staged_archive("plain.tar")), "archive.tar")


if __name__ == "__main__":
    unittest.main()

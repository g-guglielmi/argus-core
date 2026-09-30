#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

# Tests for argus-backup and argus-restore. The commands that need a real core (docker, pg_dump,
# runuser, mount, systemctl) are faked; tar and gpg run for real, so an archive is actually built,
# encrypted, exported, decrypted and checked. CI runs this on Linux; it also runs on a Windows
# workstation with Git for Windows (tar and gpg from its usr/bin).
#
#   python3 deploy/core/host/tests/test_host_scripts.py
import contextlib
import datetime
import importlib.machinery
import io
import importlib.util
import itertools
import json
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile
import types
import unittest

HOST = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NT = os.name == "nt"
WHICH = shutil.which


def winpath(a):
    """Git for Windows tools (msys) read /c/x, not C:\\x."""
    import re
    m = re.match(r"^([A-Za-z]):[\\/](.*)$", a)
    return "/" + m.group(1).lower() + "/" + m.group(2).replace("\\", "/") if m else a
_n = itertools.count()

if "fcntl" not in sys.modules:
    try:
        import fcntl  # noqa: F401
    except ImportError:  # Windows: the lock is a no-op here
        fake = types.ModuleType("fcntl")
        fake.LOCK_EX, fake.LOCK_NB = 2, 4
        fake.flock = lambda f, op: None
        sys.modules["fcntl"] = fake


def as_root(test):
    """The scripts refuse to run as anyone but root: let them, here."""
    real = getattr(os, "geteuid", None)
    os.geteuid = lambda: 0
    test.addCleanup(lambda: setattr(os, "geteuid", real) if real else delattr(os, "geteuid"))


def load(script, env):
    os.environ.update(env)
    name = "%s_%d" % (script.replace("-", "_"), next(_n))
    loader = importlib.machinery.SourceFileLoader(name, os.path.join(HOST, script))
    spec = importlib.util.spec_from_loader(name, loader)
    mod = importlib.util.module_from_spec(spec)
    loader.exec_module(mod)
    return mod


PASS = "correct horse battery staple"

# `docker inspect argus` on a core installed by hand: the CA in a folder of its own, the secret key
# and the Zabbix token only in the container's settings.
ARGUS_INSPECT = {
    "Name": "/argus",
    "Config": {"Image": "ghcr.io/g-guglielmi/argus:latest",
               "Env": ["PATH=/usr/bin", "ARGUS_SECRET_KEY=k3y with space", "ARGUS_UPDATE_DIR=/update"]},
    "HostConfig": {"RestartPolicy": {"Name": "unless-stopped"}, "NetworkMode": "bridge",
                   "PortBindings": {"8080/tcp": [{"HostIp": "", "HostPort": "8081"}]},
                   "ExtraHosts": ["host.docker.internal:host-gateway"]},
    "Mounts": [{"Type": "bind", "Source": "/docker/argus", "Destination": "/data", "RW": True},
               {"Type": "bind", "Source": "/docker/argus/pki", "Destination": "/ca", "RW": False}],
}


def plan(**kw):
    p = {"version": 1, "argus_version": "v0.5.20", "enabled": True, "hour": 2, "minute": 30, "timezone": "Europe/Rome",
         "keep": 3, "history": True, "passphrase": "", "remote": {"type": ""}}
    p.update(kw)
    return p


class FakeRun:
    """Stands in for the core's commands; tar and gpg run for real."""

    def __init__(self, mod, data_dir, the_plan):
        self.mod, self.real, self.data, self.plan = mod, mod.run, data_dir, the_plan
        self.calls, self.plan_fails = [], False
        self.update_dir = ""  # the host folder the container shares as /update ("" = none)
        self.ca_dir = ""  # the host folder it mounts as /ca ("" = none)
        self.inspect = None  # what `docker inspect argus` says (None = no such container)

    def __call__(self, cmd, *, stdout=None, input_bytes=None, env=None, timeout=None, pass_fds=(), check=True):
        self.calls.append(list(cmd))
        done = lambda out=b"", rc=0: subprocess.CompletedProcess(cmd, rc, out, b"")
        c = cmd
        if c[:2] == ["docker", "exec"] and "backup-plan" in c:
            if self.plan_fails:
                raise self.mod.Fail("docker failed: No such container: argus")
            return done(json.dumps(self.plan).encode())
        if c[:2] == ["docker", "exec"] and "backup-db" in c:
            with open(os.path.join(self.data, ".argus-backup.db"), "wb") as f:
                f.write(b"SQLite format 3\x00 fake database")
            return done()
        if c[:2] == ["docker", "inspect"]:
            if "{{.State.Running}}" in c:
                return done(b"true\n")
            if "{{json .Mounts}}" in c:
                mounts = [{"Destination": "/data", "Source": self.data}]
                if self.update_dir:
                    mounts.append({"Destination": "/update", "Source": self.update_dir})
                if self.ca_dir:
                    mounts.append({"Destination": "/ca", "Source": self.ca_dir})
                return done(json.dumps(mounts).encode())
            if "{{json .Config.Env}}" in c:
                return done(json.dumps(["PATH=/usr/bin", "ARGUS_UPDATE_DIR=/update/"]).encode())
            if "-f" not in c:  # the whole record of one container
                return done(json.dumps([self.inspect] if self.inspect and c[-1] == "argus" else []).encode())
            return done(b"ghcr.io/g-guglielmi/argus:latest\n")
        if c[0] == "docker":  # stop / start
            return done()
        if c[0] == "runuser":
            if "pg_dump" in c:
                stdout.write(b"PGDMP fake dump")
            elif "pg_dumpall" in c:
                stdout.write(b"-- roles\n")
            elif "psql" in c:
                return done(b"2.28.3\n")
            return done()
        if c[0] == "dpkg-query":
            return done({"zabbix-server-pgsql": b"1:7.0.31-1+debian13", "postgresql-17": b"17.6-1",
                         "timescaledb-2-postgresql-17": b"2.28.3~debian13"}.get(c[-1], b""))
        if c[0] == "tar" and "-czpf" in c:  # the files part: nothing of / exists here
            with tarfile.open(c[2], "w:gz"):
                pass
            return done()
        if c[0] == "tar" and "-xzpf" in c:  # restoring the files part would write under /
            return done()
        if c[0] in ("chown", "chmod"):
            return done()
        if c[0] in ("mount", "umount", "gpgconf", "apt-get", "systemctl", "rsync"):
            return done()
        if c[0] == "gpg" and NT and "--passphrase-fd" in c:  # Windows can't hand a pipe to a child
            i = c.index("--passphrase-fd")
            fd = int(c[i + 1])
            phrase = os.read(fd, 4096).decode()
            c = [WHICH("gpg")] + [winpath(a) for a in c[1:i] + ["--passphrase", phrase] + c[i + 2:]]
            return self.real(c, stdout=stdout, env=env, timeout=timeout, check=check)
        if c[0] in ("tar", "gpg") and NT:  # Git's GNU tar and gpg, not Windows' own tar
            c = [WHICH(c[0])] + (["--force-local"] if c[0] == "tar" else []) + [winpath(a) for a in c[1:]]
        kw = {"pass_fds": pass_fds} if pass_fds else {}
        return self.real(c, stdout=stdout, input_bytes=input_bytes, env=env, timeout=timeout, check=check, **kw)


class BackupTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.dirs = {k: os.path.join(self.tmp, k) for k in ("state", "local", "run", "data", "etc")}
        for d in self.dirs.values():
            os.makedirs(d)
        self.mod = load("argus-backup", {
            "ARGUS_STATE_DIR": self.dirs["state"], "ARGUS_BACKUP_DIR": self.dirs["local"], "ARGUS_BACKUP_RUN_DIR": self.dirs["run"],
            "ARGUS_BACKUP_PLAN_CACHE": os.path.join(self.dirs["etc"], "backup-plan.json"),
            "ARGUS_BACKUP_KNOWN_HOSTS": os.path.join(self.dirs["etc"], "known_hosts")})
        self.plan = plan()
        self.fake = FakeRun(self.mod, self.dirs["data"], self.plan)
        self.mod.run = self.fake
        self.mod.host_label = lambda: "core1"
        self.mod.ensure_tool = lambda binary, package: None
        as_root(self)

    def tearDown(self):
        shutil.rmtree(self.tmp, ignore_errors=True)

    def status(self):
        with open(os.path.join(self.dirs["state"], "backup-status.json"), encoding="utf-8") as f:
            return json.load(f)

    def test_schedule(self):
        m, p = self.mod, plan(hour=2, minute=30, timezone="Europe/Rome")
        at = lambda y, mo, d, h, mi: datetime.datetime(y, mo, d, h, mi, tzinfo=m.plan_zone(p))
        # 2026-10-04 03:00 Rome: today's 02:30 passed and nothing ran -> due
        now = at(2026, 10, 4, 3, 0)
        self.assertTrue(m.is_due(p, {}, now))
        # it ran OK at 02:31 -> not due again until tomorrow 02:30
        ok = {"last_ok_at": int(at(2026, 10, 4, 2, 31).timestamp()), "last_run": {"at": int(at(2026, 10, 4, 2, 31).timestamp()), "ok": True}}
        self.assertFalse(m.is_due(p, ok, now))
        self.assertEqual(m.next_due(p, ok, now), int(at(2026, 10, 5, 2, 30).timestamp()))
        # it failed at 02:31 -> retried an hour later
        failed = {"last_ok_at": int(at(2026, 10, 3, 2, 31).timestamp()), "last_run": {"at": int(at(2026, 10, 4, 2, 31).timestamp()), "ok": False}}
        self.assertFalse(m.is_due(p, failed, at(2026, 10, 4, 3, 0)))
        self.assertTrue(m.is_due(p, failed, at(2026, 10, 4, 3, 32)))
        # before today's slot, yesterday's counts: done yesterday -> not due at 01:00
        yday = {"last_ok_at": int(at(2026, 10, 3, 2, 40).timestamp())}
        self.assertFalse(m.is_due(p, yday, at(2026, 10, 4, 1, 0)))

    def test_check_plan(self):
        m = self.mod
        m.check_plan(plan())
        bad = [
            plan(keep=0), plan(hour=25),
            plan(remote={"type": "smb", "share": "//nas/backups"}),  # no passphrase
            plan(passphrase=PASS, remote={"type": "smb", "share": "//nas/backups", "username": "a;rm"}),
            plan(passphrase=PASS, remote={"type": "rsync", "target": "-oProxyCommand=x@h:/p"}),
            plan(passphrase=PASS, remote={"type": "nfs", "export": "nas:/x", "path": "../etc"}),
            plan(passphrase=PASS, remote={"type": "s3", "endpoint": "ftp://x", "bucket": "argus", "access_key": "A"}),
            plan(passphrase=PASS, remote={"type": "ftp"}),
        ]
        for p in bad:
            with self.assertRaises(m.Fail, msg=str(p)):
                m.check_plan(p)
        m.check_plan(plan(passphrase=PASS, remote={"type": "s3", "endpoint": "https://s3.example.com", "bucket": "argus-backups", "access_key": "AKIA1", "path": "core"}))

    def test_local_backup_and_retention(self):
        m = self.mod
        for i in range(4):
            self.assertTrue(m.do_backup(self.plan, "manual", ""))
            if i < 3:  # runs within one second share a name: date the earlier ones back
                newest = m.local_archives()[-1]
                os.rename(os.path.join(m.LOCAL_DIR, newest), os.path.join(m.LOCAL_DIR, "argus-backup-core1-20260930T00000%dZ.tar" % i))
        names = m.local_archives()
        self.assertEqual(len(names), 3, names)  # keep = 3: the oldest went
        self.assertEqual(names[0], "argus-backup-core1-20260930T000001Z.tar")
        with tarfile.open(os.path.join(m.LOCAL_DIR, names[-1])) as t:
            members = sorted(t.getnames())
            manifest = json.load(t.extractfile("manifest.json"))
        for part in ("argus.db.gz", "files.tar.gz", "manifest.json", "postgres-globals.sql", "zabbix.dump"):
            self.assertIn(part, members)
        self.assertEqual(manifest["argus_db"], "consistent copy (VACUUM INTO)")
        self.assertEqual(manifest["versions"]["timescaledb-2-postgresql-17"], "2.28.3~debian13")
        st = self.status()
        self.assertTrue(st["last_run"]["ok"])
        self.assertEqual(len(st["local"]), 3)
        self.assertFalse(st["running"])
        self.assertFalse(os.path.exists(os.path.join(self.dirs["data"], ".argus-backup.db")), "the temporary database copy is removed")
        self.assertEqual([n for n in os.listdir(m.LOCAL_DIR) if n.startswith(".")], [], "no work or partial files left")

    def test_hand_installed_core_keeps_its_ca_and_container_settings(self):
        # A core installed by hand keeps its CA in a folder of its own (mounted as /ca), and the secret
        # key and the Zabbix token only in the container's settings: both go into the archive.
        ca = os.path.join(self.tmp, "pki")
        os.makedirs(ca)
        self.fake.ca_dir, self.fake.inspect = ca, ARGUS_INSPECT
        self.assertTrue(self.mod.do_backup(self.plan, "manual", ""))
        with tarfile.open(os.path.join(self.mod.LOCAL_DIR, self.mod.local_archives()[-1])) as t:
            manifest = json.load(t.extractfile("manifest.json"))
            kept = json.load(t.extractfile("containers.json"))
        self.assertIn(self.mod.under_root(ca), manifest["files"])
        self.assertEqual(manifest["argus_mounts"]["/ca"], ca)
        self.assertEqual(manifest["containers"], ["argus"])
        self.assertIn("ARGUS_SECRET_KEY=k3y with space", kept[0]["Config"]["Env"])
        # The data folder and the update folder are not packed as files: the database has its own part.
        self.assertNotIn(self.mod.under_root(self.dirs["data"]), manifest["files"])

    def test_history_off_excludes_metric_data(self):
        self.mod.do_backup(plan(history=False), "manual", "")
        dump = [c for c in self.fake.calls if "pg_dump" in c][0]
        self.assertIn("--exclude-table-data=public.history*", dump)
        self.assertIn("--exclude-table-data=_timescaledb_internal.*", dump)

    def test_encrypted_backup_exported_to_a_share(self):
        m = self.mod
        mnt = os.path.join(self.dirs["run"], "mnt")
        p = plan(passphrase=PASS, keep=2, remote={"type": "smb", "share": "//nas.example.lan/backups", "username": "argus", "path": "core/argus"}, password="p@ss")
        os.makedirs(os.path.join(mnt, "core", "argus"))
        # another core's archive and an unrelated file on the share are never touched
        for other in ("argus-backup-core2-20260101T000000Z.tar.gpg", "notes.txt"):
            open(os.path.join(mnt, "core", "argus", other), "w").close()
        for ts in ("20260901T000000Z", "20260902T000000Z"):
            open(os.path.join(mnt, "core", "argus", "argus-backup-core1-%s.tar.gpg" % ts), "w").close()
        self.assertTrue(m.do_backup(p, "manual", ""))
        name = m.local_archives()[-1]
        self.assertTrue(name.endswith(".tar.gpg"))
        st = self.status()
        self.assertTrue(st["remote"]["ok"], st["remote"])
        there = sorted(os.listdir(os.path.join(mnt, "core", "argus")))
        self.assertIn(name, there)
        self.assertIn("argus-backup-core2-20260101T000000Z.tar.gpg", there)
        self.assertIn("notes.txt", there)
        self.assertNotIn("argus-backup-core1-20260901T000000Z.tar.gpg", there)  # beyond keep=2
        mount = [c for c in self.fake.calls if c[0] == "mount"][0]
        self.assertEqual(mount[:4], ["mount", "-t", "cifs", "//nas.example.lan/backups"])
        self.assertNotIn("p@ss", " ".join(mount), "the password goes through a credentials file, not the command line")
        self.assertEqual([n for n in os.listdir(self.dirs["run"]) if n.startswith("smb-")], [], "the credentials file is removed")
        # the archive decrypts with the passphrase (argus-restore inspect)
        r = load("argus-restore", {"ARGUS_BACKUP_DIR": self.dirs["local"]})
        rfake = FakeRun(r, self.dirs["data"], p)
        r.run = rfake
        pf = os.path.join(self.tmp, "pass")
        with open(pf, "w") as f:
            f.write(PASS)
        work = tempfile.mkdtemp(dir=self.tmp)
        manifest = r.unpack(os.path.join(m.LOCAL_DIR, name), work, pf)
        self.assertEqual(manifest["host"], __import__("socket").gethostname())
        self.assertTrue(os.path.exists(os.path.join(work, "zabbix.dump")))
        self.assertEqual(r.version_problems(manifest), [])

    def test_plan_fallback_when_argus_is_down(self):
        m = self.mod
        m.load_plan()  # cached
        self.fake.plan_fails = True
        p, note = m.load_plan()
        self.assertEqual(p["keep"], 3)
        self.assertIn("using the last one it gave", note)
        os.unlink(m.PLAN_CACHE)
        p, note = m.load_plan()
        self.assertIsNone(p)
        self.assertIn("none is kept", note)

    def test_request_runs_a_test(self):
        m = self.mod
        self.fake.plan.update(passphrase=PASS, remote={"type": "nfs", "export": "nas.example.lan:/volume1/backups", "path": "argus"})
        with open(m.REQUEST_FILE, "w") as f:
            json.dump({"kind": "test", "by": "ops@example.com"}, f)
        self.assertEqual(m.main(["argus-backup", "tick"]), 0)
        st = self.status()
        self.assertTrue(st["test"]["ok"], st.get("test"))
        self.assertFalse(os.path.exists(m.REQUEST_FILE), "the request is taken")
        self.assertFalse(any(c[:1] == ["runuser"] for c in self.fake.calls), "a test doesn't back up")

    def test_state_dir_found_from_the_container(self):
        # A core installed by hand shares another folder than the one the tools were told: they find
        # the folder the Argus container really mounts as its update dir, and report there.
        real = os.path.join(self.tmp, "docker-argus-update")
        os.makedirs(real)
        self.fake.update_dir = real
        os.environ["ARGUS_STATE_DIR"] = os.path.join(self.tmp, "missing")
        self.addCleanup(os.environ.__setitem__, "ARGUS_STATE_DIR", self.dirs["state"])
        self.assertEqual(self.mod.resolve_state_dir(), real)
        self.assertEqual(self.mod.main(["argus-backup", "tick"]), 0)
        with open(os.path.join(real, "backup-status.json"), encoding="utf-8") as f:
            self.assertTrue(json.load(f)["configured"])
        # A folder that exists is used as given, without asking Docker.
        os.environ["ARGUS_STATE_DIR"] = self.dirs["state"]
        self.fake.calls.clear()
        self.assertEqual(self.mod.resolve_state_dir(), self.dirs["state"])
        self.assertFalse(any(c[:2] == ["docker", "inspect"] for c in self.fake.calls))

    def test_tick_not_due_reports_next(self):
        m = self.mod
        self.fake.plan.update(enabled=True)
        now = datetime.datetime.now(datetime.timezone.utc)
        st = {"version": 1, "last_ok_at": int(now.timestamp()), "last_run": {"at": int(now.timestamp()), "ok": True}}
        with open(os.path.join(self.dirs["state"], "backup-status.json"), "w") as f:
            json.dump(st, f)
        self.assertEqual(m.main(["argus-backup", "tick"]), 0)
        st = self.status()
        self.assertGreater(st["next_due_at"], int(now.timestamp()))
        self.assertFalse(any("pg_dump" in c for c in self.fake.calls))

    def test_rsync_and_s3_commands(self):
        m = self.mod
        key = "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"
        p = plan(passphrase=PASS, remote={"type": "rsync", "target": "backup@nas.example.lan:/volume1/argus", "port": 2222}, private_key=key)
        with m.RsyncTarget(p) as t:
            t.put("/var/backups/argus/a.tar.gpg", "a.tar.gpg")
            t.delete("old.tar.gpg")
            keyfile = t.key
        put, delete = [c for c in self.fake.calls if c[0] == "rsync"][-2:]
        self.assertIn("-p 2222", put[2])
        self.assertIn("StrictHostKeyChecking=accept-new", put[2])
        self.assertEqual(put[-1], "backup@nas.example.lan:/volume1/argus/a.tar.gpg")
        self.assertIn("--include=old.tar.gpg", delete)
        self.assertIn("--exclude=*", delete)
        self.assertFalse(os.path.exists(keyfile), "the key file is removed")
        s3 = plan(passphrase=PASS, remote={"type": "s3", "endpoint": "https://s3.example.com", "region": "eu-central-1", "bucket": "argus-backups", "access_key": "AKIA1", "path": "core"}, secret_key="SECRET")
        seen = {}
        real = self.fake.__call__

        def spy(cmd, **kw):
            if cmd and cmd[0] == "rclone":
                seen["cmd"], seen["env"] = cmd, kw.get("env") or {}
                return subprocess.CompletedProcess(cmd, 0, b"", b"")
            return real(cmd, **kw)
        m.run = spy
        with m.S3Target(s3) as t:
            t.put("/x/a.tar.gpg", "a.tar.gpg")
        self.assertEqual(seen["cmd"][-2:], ["/x/a.tar.gpg", "arguss3:argus-backups/core/a.tar.gpg"])
        self.assertNotIn("SECRET", " ".join(seen["cmd"]), "the secret key stays out of the command line")
        self.assertEqual(seen["env"]["RCLONE_CONFIG_ARGUSS3_SECRET_ACCESS_KEY"], "SECRET")


class RestoreTest(unittest.TestCase):
    """The restore flow, with the core's commands faked: the order of the steps and what they touch."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.local, self.data = os.path.join(self.tmp, "local"), os.path.join(self.tmp, "data")
        os.makedirs(self.local)
        os.makedirs(self.data)
        # an archive, the way argus-backup builds it
        b = load("argus-backup", {"ARGUS_STATE_DIR": os.path.join(self.tmp, "nostate"), "ARGUS_BACKUP_DIR": self.local,
                                  "ARGUS_BACKUP_RUN_DIR": os.path.join(self.tmp, "run"),
                                  "ARGUS_BACKUP_PLAN_CACHE": os.path.join(self.tmp, "plan.json")})
        bf = FakeRun(b, self.data, plan())
        bf.inspect = ARGUS_INSPECT
        b.run, b.host_label = bf, (lambda: "core1")
        self.assertTrue(b.do_backup(plan(), "manual", ""))
        self.archive = os.path.join(self.local, b.local_archives()[-1])
        self.r = load("argus-restore", {"ARGUS_BACKUP_DIR": self.local})
        self.fake = FakeRun(self.r, self.data, plan())
        self.r.run = self.fake
        self.chowns = []
        self._chown = getattr(os, "chown", None)
        os.chown = lambda p, u, g: self.chowns.append((p, u, g))
        as_root(self)

    def tearDown(self):
        if self._chown:
            os.chown = self._chown
        else:
            del os.chown
        shutil.rmtree(self.tmp, ignore_errors=True)

    def test_inspect_changes_nothing(self):
        self.assertEqual(self.r.main(["argus-restore", "inspect", self.archive]), 0)
        self.assertFalse(any(c[0] in ("systemctl", "docker", "runuser") and "dropdb" in c for c in self.fake.calls))

    def test_full_restore_order(self):
        # the archive says where Argus kept its data: point it at the test folder
        self.assertEqual(self.r.main(["argus-restore", "restore", self.archive, "--yes"]), 0)
        flat = [" ".join(c) for c in self.fake.calls]
        idx = lambda s: next(i for i, c in enumerate(flat) if s in c)
        self.assertLess(idx("systemctl stop zabbix-server"), idx("dropdb --if-exists zabbix"))
        self.assertLess(idx("timescaledb_pre_restore"), idx("pg_restore"))
        self.assertLess(idx("pg_restore"), idx("timescaledb_post_restore"))
        self.assertLess(idx("timescaledb_post_restore"), idx("systemctl enable --now zabbix-server"))
        files = [c for c in self.fake.calls if "-xzpf" in c][0]
        self.assertIn("--exclude=etc/postgresql", files)
        restored = os.path.join(self.data, "argus.db")
        with open(restored, "rb") as f:
            self.assertTrue(f.read().startswith(b"SQLite format 3"))
        self.assertIn((restored, 65532, 65532), self.chowns)

    def test_containers_prints_docker_run(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(self.r.main(["argus-restore", "containers", self.archive]), 0)
        text = out.getvalue()
        self.assertIn("docker run -d --name argus --restart unless-stopped -p 8081:8080 "
                      "--add-host host.docker.internal:host-gateway -v /docker/argus:/data -v /docker/argus/pki:/ca:ro "
                      "-e 'ARGUS_SECRET_KEY=k3y with space' -e ARGUS_UPDATE_DIR=/update ghcr.io/g-guglielmi/argus:latest", text)
        self.assertNotIn("PATH=", text)
        self.assertFalse(any(c[0] in ("systemctl", "runuser") for c in self.fake.calls), "printing changes nothing")

    def test_version_mismatch_stops(self):
        real = self.fake.__call__

        def newer(cmd, **kw):
            if cmd[0] == "dpkg-query" and cmd[-1] == "timescaledb-2-postgresql-17":
                return subprocess.CompletedProcess(cmd, 0, b"2.29.0~debian13", b"")
            return real(cmd, **kw)
        self.r.run = newer
        self.assertEqual(self.r.main(["argus-restore", "restore", self.archive, "--yes"]), 1)


if __name__ == "__main__":
    unittest.main(verbosity=1)

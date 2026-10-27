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
import re
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
               "Env": ["PATH=/usr/bin", "ARGUS_SECRET_KEY=k3y with space", "ARGUS_ADMIN_PASSWORD=hunter2", "ARGUS_UPDATE_DIR=/update"]},
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
        self.mount_fails = ""  # mount.cifs's error, when the share can't be mounted
        self.net = None  # the machine's network: {"dev", "mac", "addr", "gw", "routes", "dhcp"} (None = no default route)

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
        if c[0] == "ip":
            n = self.net
            if not n:
                return done(b"[]")
            if c[1:] == ["-j", "route", "show"]:
                rts = [{"dst": "default", "gateway": n["gw"], "dev": n["dev"], "protocol": "dhcp" if n.get("dhcp") else "static"}]
                rts += [{"dst": to, "gateway": via, "dev": n["dev"], "protocol": "static"} for to, via in n.get("routes", [])]
                rts += [{"dst": "172.17.0.0/16", "dev": "docker0", "protocol": "kernel"}]
                return done(json.dumps(rts).encode())
            if c[1:4] == ["-j", "addr", "show"]:
                ip, plen = n["addr"].split("/")
                v4 = {"family": "inet", "local": ip, "prefixlen": int(plen), "scope": "global"}
                if n.get("dhcp"):
                    v4["dynamic"] = True
                return done(json.dumps([{"ifname": n["dev"], "address": n["mac"], "addr_info": [
                    v4, {"family": "inet6", "local": "fe80::1", "prefixlen": 64, "scope": "link"}]}]).encode())
            return done(b"[]")
        if c[0] in ("networkctl", "hostnamectl"):
            return done()
        if c[0] == "mount" and self.mount_fails:
            raise self.mod.Fail("mount failed: " + self.mount_fails)
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
        # The one-time admin seed (a human's password) is never archived; the machine secrets are.
        self.assertNotIn("ARGUS_ADMIN_PASSWORD=hunter2", kept[0]["Config"]["Env"])
        # The data folder and the update folder are not packed as files: the database has its own part.
        self.assertNotIn(self.mod.under_root(self.dirs["data"]), manifest["files"])

    def test_smb_errors_in_plain_words(self):
        r = self.mod.smb_reason
        timeout = "mount failed: mount error(115): could not connect to 10.0.0.20Unable to find suitable address."
        self.assertEqual(r(timeout, "//10.0.0.20/Backup"),
                         "can't reach 10.0.0.20 on TCP 445 (SMB): a firewall between the core and it drops the "
                         "connection, the core has no route there, or SMB is off on it (mount error 115)")
        self.assertIn("refused the login", r("mount failed: mount error(13): Permission denied", "//nas/Backup"))
        self.assertIn("nas has no such share", r("mount failed: mount error(2): No such file or directory", "//nas/Backup"))
        self.assertEqual(r("mount failed: mount error: could not resolve address for nas: Unknown error", "//nas/Backup"),
                         "the core can't resolve nas: use its IP address, or a DNS name the core resolves")
        self.assertEqual(r("mount failed: something new", "//nas/Backup"), "mount failed: something new")

    def test_network_is_kept(self):
        resolv = os.path.join(self.tmp, "resolv.conf")
        with open(resolv, "w") as f:
            f.write("nameserver 127.0.0.53\nnameserver 10.0.0.1\nsearch example.lan\n")
        self.mod.RESOLV_FILES = [resolv]
        self.fake.net = {"dev": "ens18", "mac": "aa:bb:cc:00:11:22", "addr": "10.0.0.10/24", "gw": "10.0.0.1",
                         "routes": [("10.9.0.0/16", "10.0.0.254")]}
        self.assertTrue(self.mod.do_backup(self.plan, "manual", ""))
        with tarfile.open(os.path.join(self.mod.LOCAL_DIR, self.mod.local_archives()[-1])) as t:
            net = json.load(t.extractfile("network.json"))
            manifest = json.load(t.extractfile("manifest.json"))
        self.assertEqual(net["addresses"], ["10.0.0.10/24"], "the link-local address is left out")
        self.assertEqual((net["interface"], net["mac"], net["gateway"], net["dhcp"]), ("ens18", "aa:bb:cc:00:11:22", "10.0.0.1", False))
        self.assertEqual((net["dns"], net["search"]), (["10.0.0.1"], ["example.lan"]), "resolved's local stub is left out")
        self.assertEqual(net["routes"], [{"to": "10.9.0.0/16", "via": "10.0.0.254"}], "only routes through a gateway, not docker0's")
        self.assertEqual(net["managed_by"], "systemd-networkd")
        self.assertEqual(manifest["network"]["addresses"], ["10.0.0.10/24"])

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
        # Settings lists it under Recent activity, saying who asked and what it did.
        act = st["activity"][0]
        self.assertEqual((act["what"], act["ok"], act["why"]), ("check", True, "asked in Settings by ops@example.com"))
        self.assertIn("wrote, listed and deleted a test file on the NFS export nas.example.lan:/volume1/backups", act["text"])
        self.assertNotIn("busy", st, "nothing is under way any more")

    def test_failed_check_says_where_and_is_not_a_service_failure(self):
        m = self.mod
        self.fake.plan.update(passphrase=PASS, remote={"type": "smb", "share": "//10.0.0.20/Backup", "username": "rclone", "path": "Argus"})
        self.fake.mount_fails = "mount error(115): could not connect to 10.0.0.20Unable to find suitable address."
        with open(m.REQUEST_FILE, "w") as f:
            json.dump({"kind": "test", "by": "ops@example.com"}, f)
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(m.main(["argus-backup", "tick"]), 0, "a failed check is reported, not a failed service")
        st = self.status()
        self.assertTrue(st["test"]["error"].startswith("while mounting //10.0.0.20/Backup: can't reach 10.0.0.20 on TCP 445"), st["test"])
        self.assertFalse(st["activity"][0]["ok"])
        log = out.getvalue()
        for line in ("checking the export target, asked in Settings by ops@example.com",
                     "target: SMB share //10.0.0.20/Backup, folder Argus as rclone",
                     "mounting //10.0.0.20/Backup",
                     "the target check failed while mounting //10.0.0.20/Backup: can't reach"):
            self.assertIn(line, log)
        # From the command line a failed check still exits non-zero, for scripts.
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(m.main(["argus-backup", "test"]), 1)

    def test_check_catches_up_a_failed_export(self):
        # The export failed (the share was unreachable); once a check finds the target working, the same
        # run copies the archive the target is missing, and the failure is gone.
        m = self.mod
        mnt = os.path.join(self.dirs["run"], "mnt")
        os.makedirs(os.path.join(mnt, "argus"))
        self.fake.plan.update(passphrase=PASS, remote={"type": "smb", "share": "//10.0.0.20/Backup", "username": "rclone", "path": "argus"})
        archive = "argus-backup-core1-20260930T224513Z.tar.gpg"
        with open(os.path.join(m.LOCAL_DIR, archive), "wb") as f:
            f.write(b"encrypted")
        with open(m.STATUS_FILE, "w") as f:
            json.dump({"version": 1, "remote": {"type": "smb", "ok": False, "at": 1, "error": "while mounting //unraid/Backup: ..."}}, f)
        with open(m.REQUEST_FILE, "w") as f:
            json.dump({"kind": "test"}, f)
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(m.main(["argus-backup", "tick"]), 0)
        st = self.status()
        self.assertTrue(st["test"]["ok"])
        self.assertTrue(st["remote"]["ok"], st["remote"])
        self.assertEqual(st["remote"]["files"], 1)
        self.assertIn(archive, os.listdir(os.path.join(mnt, "argus")))
        self.assertIn("the last export had failed, so it copied the 1 archive the target was missing", st["activity"][0]["text"])
        # With the export fine, a check only checks.
        self.fake.calls.clear()
        with open(m.REQUEST_FILE, "w") as f:
            json.dump({"kind": "test"}, f)
        with contextlib.redirect_stdout(io.StringIO()):
            m.main(["argus-backup", "tick"])
        self.assertNotIn("copied", self.status()["activity"][0]["text"])

    def test_backup_activity_tells_the_export(self):
        m = self.mod
        mnt = os.path.join(self.dirs["run"], "mnt")
        os.makedirs(os.path.join(mnt, "argus"))
        p = plan(passphrase=PASS, remote={"type": "smb", "share": "//nas.example.lan/backups", "username": "argus", "path": "argus"}, password="p@ss")
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertTrue(m.do_backup(p, "schedule", "", "daily at 02:30"))
        act = self.status()["activity"][0]
        self.assertEqual((act["what"], act["ok"], act["why"]), ("backup", True, "daily at 02:30"))
        self.assertIn("; exported to SMB share //nas.example.lan/backups, folder argus as argus: copied 1, 1 there", act["text"])
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(m.main(["argus-backup", "log"]), 0)
        self.assertIn("backup", out.getvalue())
        self.assertIn("(daily at 02:30,", out.getvalue())

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
        bf.net = {"dev": "ens18", "mac": "aa:bb:cc:00:11:22", "addr": "10.0.0.10/24", "gw": "10.0.0.1",
                  "routes": [("10.9.0.0/16", "10.0.0.254")]}
        resolv = os.path.join(self.tmp, "resolv.conf")
        with open(resolv, "w") as f:
            f.write("nameserver 10.0.0.1\nsearch example.lan\n")
        b.RESOLV_FILES = [resolv]
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

    def test_network_takes_over_the_old_address(self):
        # A new core VM came up on DHCP with another address: it takes the old core's, matched by its own
        # MAC, in a file that sorts before the appliance's DHCP one.
        self.fake.net = {"dev": "enX0", "mac": "52:54:00:aa:bb:cc", "addr": "10.0.0.99/24", "gw": "10.0.0.1"}
        self.r.NETWORKD_DIR = os.path.join(self.tmp, "network")
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(self.r.main(["argus-restore", "network", self.archive, "--yes"]), 0)
        with open(os.path.join(self.r.NETWORKD_DIR, "05-argus-restored.network"), encoding="utf-8") as f:
            unit = f.read()
        for line in ("MACAddress=52:54:00:aa:bb:cc", "Address=10.0.0.10/24", "Gateway=10.0.0.1", "DNS=10.0.0.1",
                     "Domains=example.lan", "[Route]", "Destination=10.9.0.0/16", "Gateway=10.0.0.254"):
            self.assertIn(line, unit)
        flat = [" ".join(c) for c in self.fake.calls]
        self.assertIn("networkctl reload", flat)
        self.assertIn("networkctl reconfigure enX0", flat)
        self.assertFalse(any(c.startswith("hostnamectl") for c in flat), "the hostname is the same here")

    def test_network_nothing_to_do_or_dhcp(self):
        self.r.NETWORKD_DIR = os.path.join(self.tmp, "network")
        # Already at the old address: nothing is written.
        self.fake.net = {"dev": "enX0", "mac": "52:54:00:aa:bb:cc", "addr": "10.0.0.10/24", "gw": "10.0.0.1"}
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(self.r.main(["argus-restore", "network", self.archive, "--yes"]), 0)
        self.assertIn("already answers at the old core's address", out.getvalue())
        self.assertFalse(os.path.exists(self.r.NETWORKD_DIR))
        # The old core's address came from DHCP: move the reservation, don't pin it (unless --static).
        work = tempfile.mkdtemp(dir=self.tmp)
        with open(os.path.join(work, "network.json"), "w") as f:
            json.dump({"hostname": __import__("socket").gethostname(), "interface": "ens18", "addresses": ["10.0.0.10/24"],
                       "gateway": "10.0.0.1", "dns": [], "dhcp": True}, f)
        self.fake.net = {"dev": "enX0", "mac": "52:54:00:aa:bb:cc", "addr": "10.0.0.99/24", "gw": "10.0.0.1"}
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(self.r.take_network(work, "a.tar", yes=True), 0)
        self.assertIn("move it in", out.getvalue())
        self.assertIn("52:54:00:aa:bb:cc", out.getvalue())
        self.assertFalse(os.path.exists(self.r.NETWORKD_DIR))
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(self.r.take_network(work, "a.tar", yes=True, static=True), 0)
        self.assertTrue(os.path.exists(os.path.join(self.r.NETWORKD_DIR, "05-argus-restored.network")))

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


class SizingTest(unittest.TestCase):
    """argus-size-zabbix: the sizes a machine gets, and the one block it writes."""

    def setUp(self):
        self.mod = load("argus-size-zabbix", {})

    def test_production_vm_gets_the_sizing_pass_numbers(self):
        zabbix, postgres = self.mod.plan(16, 8)
        got = dict(zabbix)
        self.assertEqual(got["CacheSize"], "256M")
        self.assertEqual(got["HistoryCacheSize"], "256M")
        self.assertEqual(got["HistoryIndexCacheSize"], "64M")
        self.assertEqual(got["TrendCacheSize"], "64M")
        self.assertEqual(got["ValueCacheSize"], "512M")
        self.assertEqual(got["StartDBSyncers"], "8")
        self.assertEqual(got["StartTrappers"], "16")
        self.assertEqual(got["StartPreprocessors"], "8")
        self.assertEqual(got["StartPollers"], "16")
        self.assertEqual(got["StartPingers"], "4")
        self.assertEqual(got["StartSNMPPollers"], "2")
        self.assertEqual(got["Timeout"], "10")
        self.assertEqual(postgres["max_connections"], 200)

    def test_small_vm_never_goes_below_the_snippet(self):
        got = dict(self.mod.plan(4, 2)[0])
        self.assertEqual(got["CacheSize"], "64M")
        self.assertEqual(got["HistoryCacheSize"], "64M")
        self.assertEqual(got["ValueCacheSize"], "128M")
        self.assertEqual(got["StartPollers"], "10")
        self.assertEqual(got["StartPreprocessors"], "6")
        self.assertEqual(got["StartDBSyncers"], "6")
        self.assertEqual(got["StartTrappers"], "5")
        self.assertEqual(got["StartPingers"], "1")
        self.assertEqual(got["StartSNMPPollers"], "1")
        self.assertEqual(self.mod.plan(4, 2)[1]["max_connections"], 100)
        # a 7.7 GB MemTotal is an 8 GB VM
        self.assertEqual(dict(self.mod.plan(7.7, 4)[0])["CacheSize"], "128M")

    def test_caches_grow_with_ram_and_stop_at_the_ceiling(self):
        sizes = [int(dict(self.mod.plan(gb, 8)[0])["ValueCacheSize"][:-1]) for gb in (4, 8, 16, 32, 64, 128)]
        self.assertEqual(sizes, sorted(sizes))
        self.assertEqual(sizes[-1], 2048)
        self.assertEqual(sizes[-2], 2048)

    def test_one_block_at_the_end_replaced_on_rerun(self):
        stock = "# stock\nTimeout=4\nLogFile=/var/log/zabbix/zabbix_server.log\n\n### snippet\nCacheSize=64M\nTimeout=10\n"
        first = self.mod.with_block(stock, self.mod.render_block(self.mod.plan(8, 4)[0], 8, 4, "2026-10-19"))
        self.assertTrue(first.startswith(stock.rstrip("\n")))
        self.assertEqual(first.count(self.mod.BEGIN), 1)
        self.assertTrue(first.rstrip("\n").endswith(self.mod.END))
        self.assertIn("\nCacheSize=128M\n", first)
        # the snippet's own CacheSize line stays (Zabbix takes the last value: ours)
        self.assertIn("### snippet\nCacheSize=64M\n", first)
        second = self.mod.with_block(first, self.mod.render_block(self.mod.plan(16, 8)[0], 16, 8, "2026-11-01"))
        self.assertEqual(second.count(self.mod.BEGIN), 1)
        self.assertEqual(second.count(self.mod.END), 1)
        self.assertIn("CacheSize=256M", second)
        self.assertNotIn("CacheSize=128M", second)
        self.assertNotIn("2026-10-19", second)
        self.assertEqual(second.index("### snippet"), first.index("### snippet"))

    def test_tune_runs_timescaledb_tune_and_names_postgresql_when_it_changed(self):
        tmp = tempfile.mkdtemp()
        conf = os.path.join(tmp, "zabbix_server.conf")
        with open(conf, "w") as fh:
            fh.write("Timeout=4\n")
        pgd = os.path.join(tmp, "conf.d")
        os.makedirs(pgd)
        pgconf = os.path.join(tmp, "postgresql.conf")
        with open(pgconf, "w") as fh:
            fh.write("shared_buffers = 128MB\n")
        self.mod.pg_conf_files = lambda: [pgconf]
        calls = []

        def fake_run(args, **kw):
            calls.append(args)
            with open(pgconf, "w") as fh:
                fh.write("shared_buffers = 4GB\n")
            return types.SimpleNamespace(returncode=0)
        self.mod.subprocess = types.SimpleNamespace(run=fake_run)
        self.mod.shutil = types.SimpleNamespace(which=lambda name: "/usr/bin/timescaledb-tune")
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            rc = self.mod.main(["--apply", "--postgres", "--tune", "--ram-gb", "16", "--cores", "8", "--conf", conf, "--pg-conf-dir", pgd])
        self.assertEqual(rc, 0)
        self.assertEqual(calls[0][0], "/usr/bin/timescaledb-tune")
        self.assertIn("settings changed for this machine", out.getvalue())
        self.assertIn("restart to apply: systemctl restart postgresql", out.getvalue())
        # a second boot finds everything in place: nothing to restart
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.mod.main(["--apply", "--postgres", "--tune", "--ram-gb", "16", "--cores", "8", "--conf", conf, "--pg-conf-dir", pgd])
        self.assertIn("already tuned", out.getvalue())
        self.assertIn("nothing changed", out.getvalue())

    def test_tune_without_the_tool_is_said_and_not_fatal(self):
        tmp = tempfile.mkdtemp()
        pgd = os.path.join(tmp, "conf.d")
        os.makedirs(pgd)
        self.mod.shutil = types.SimpleNamespace(which=lambda name: None)
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            rc = self.mod.main(["--apply", "--postgres", "--tune", "--ram-gb", "8", "--cores", "4", "--pg-conf-dir", pgd])
        self.assertEqual(rc, 0)
        self.assertIn("timescaledb-tune is not installed", err.getvalue())

    def test_boot_unit_runs_before_the_database_and_the_server(self):
        with open(os.path.join(HOST, "argus-size-zabbix.service")) as fh:
            unit = fh.read()
        self.assertIn("Before=postgresql.service zabbix-server.service", unit)
        self.assertIn("ExecStart=/usr/local/sbin/argus-size-zabbix --apply --tune", unit)
        self.assertIn("WantedBy=multi-user.target", unit)
        with open(os.path.join(HOST, "argus-size-zabbix-postgresql.conf")) as fh:
            dropin = fh.read()
        self.assertIn("After=argus-size-zabbix.service", dropin)

    def test_apply_writes_both_files_and_says_what_to_restart(self):
        tmp = tempfile.mkdtemp()
        conf = os.path.join(tmp, "zabbix_server.conf")
        pgd = os.path.join(tmp, "conf.d")
        os.makedirs(pgd)
        with open(conf, "w") as fh:
            fh.write("Timeout=4\n")
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            rc = self.mod.main(["--apply", "--ram-gb", "16", "--cores", "8", "--conf", conf, "--pg-conf-dir", pgd])
        self.assertEqual(rc, 0)
        self.assertIn("restart to apply: systemctl restart zabbix-server postgresql", out.getvalue())
        with open(conf) as fh:
            text = fh.read()
        self.assertIn("ValueCacheSize=512M", text)
        with open(os.path.join(pgd, "argus-sizing.conf")) as fh:
            self.assertIn("max_connections = 200", fh.read())
        # a second run changes nothing and says so
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.mod.main(["--apply", "--ram-gb", "16", "--cores", "8", "--conf", conf, "--pg-conf-dir", pgd])
        self.assertIn("nothing changed", out.getvalue())
        # the zabbix scope alone leaves PostgreSQL out
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.mod.main(["--zabbix", "--ram-gb", "4", "--cores", "2", "--conf", conf])
        self.assertNotIn("max_connections", out.getvalue())
        self.assertIn("(shown only", out.getvalue())


CORE_DEPLOY = os.path.dirname(HOST)
PATCHING = os.path.join(CORE_DEPLOY, "setup-core-patching.sh")
SETUP = os.path.join(CORE_DEPLOY, "setup-core.sh")


def installer_block(path, start, end):
    """The text of an installer from the line `start` begins up to and including the line `end` begins."""
    with open(path, encoding="utf-8") as fh:
        s = fh.read()
    i = s.index(start)
    j = s.index(end, i)
    return s[i:s.index("\n", j) + 1]


def script_body(name):
    """One of the host scripts, as the manifest installs it."""
    with open(os.path.join(HOST, name), encoding="utf-8") as fh:
        return fh.read()


class PatchingTest(unittest.TestCase):
    """The host scripts setup-core.sh and setup-core-patching.sh install, and the Zabbix minor updater."""

    def test_both_installers_install_the_host_tools_from_the_manifest(self):
        for path in (SETUP, PATCHING):
            with open(path, encoding="utf-8") as fh:
                text = fh.read()
            self.assertIn("host/install-host-tools.sh", text, path)
            self.assertNotIn("<<'REPORT'", text, "the scripts live in host/, not in the installers")

    def test_reporter_says_the_host_honours_requests(self):
        self.assertIn('"zbx_update_now":true,"reboot_now":true,"patch_now":true', script_body("argus-os-report"))
        with open(os.path.join(HOST, "manifest"), encoding="utf-8") as fh:
            manifest = fh.read()
        for unit in ("argus-zbx-update.path", "argus-reboot-check.path", "argus-patch.path", "argus-host-tools.path"):
            self.assertRegex(manifest, r"%s +0644 render enable" % re.escape(unit))

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_install_host_tools_renders_the_manifest_into_a_root(self):
        root = tempfile.mkdtemp()
        env = dict(os.environ, ARGUS_HOST_ROOT=winpath(root), ARGUS_STATE_DIR="/docker/argus-update")
        r = subprocess.run([WHICH("bash"), winpath(os.path.join(HOST, "install-host-tools.sh"))], env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=120)
        self.assertEqual(r.returncode, 0, r.stderr)
        with open(os.path.join(root, "etc", "systemd", "system", "argus-os-report.service")) as fh:
            self.assertIn("ARGUS_STATE_DIR=/docker/argus-update", fh.read())
        with open(os.path.join(root, "etc", "systemd", "system", "argus-backup.path")) as fh:
            self.assertIn("/docker/argus-update/backup-request.json", fh.read())
        with open(os.path.join(root, "etc", "systemd", "system", "postgresql@.service.d", "argus-size-zabbix.conf")) as fh:
            self.assertIn("After=argus-size-zabbix.service", fh.read())
        for script in ("argus-os-report", "argus-patch", "argus-host-tools-apply", "argus-size-zabbix", "argus-backup"):
            self.assertTrue(os.path.exists(os.path.join(root, "usr", "local", "sbin", script)), script)
        self.assertIn("would enable: ", r.stdout)
        for unit in ("argus-os-report.timer", "argus-host-tools.path", "argus-size-zabbix.service", "argus-backup.path", "argus-patch.path"):
            self.assertIn(unit, r.stdout)
        self.assertTrue(os.path.isdir(os.path.join(root, "docker", "argus-update")), "the shared dir is created when missing")

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_host_tools_apply_reloads_and_enables_what_the_marker_lists(self):
        shims = {"systemctl": "echo \"systemctl $*\" >> \"$LOG\"\n", "logger": ":\n"}
        marker = '{"at":1,"version":"v0.9.2","enable":"argus-os-report.timer argus-host-tools.path","changed":"argus-os-report.timer"}'
        results, calls = self.host_script("argus-host-tools-apply", shims, {"host-tools-apply.json": marker})
        self.assertNotIn("host-tools-apply.json", results, "the marker must be consumed")
        self.assertIn("systemctl daemon-reload", calls)
        self.assertIn("systemctl enable --now argus-os-report.timer", calls)
        self.assertIn("systemctl enable --now argus-host-tools.path", calls)
        self.assertNotIn("systemctl start", calls, "nothing listed to run again")
        _, quiet = self.host_script("argus-host-tools-apply", shims, {})
        self.assertEqual(quiet, "", "without a marker it does nothing")

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_host_tools_apply_starts_again_what_the_marker_lists_to_run(self):
        # A round that wrote only a script: nothing to enable that changed, the guard runs now.
        shims = {"systemctl": "echo \"systemctl $*\" >> \"$LOG\"\n", "logger": "echo \"logger $*\" >> \"$LOG\"\n"}
        marker = ('{"at":1,"changed":"","enable":"argus-os-report.timer argus-web-guard.service",'
                  '"run":"argus-web-guard.service","version":"v0.9.9"}')
        _, calls = self.host_script("argus-host-tools-apply", shims, {"host-tools-apply.json": marker})
        self.assertIn("systemctl start --no-block argus-web-guard.service", calls)
        self.assertIn("run again: argus-web-guard.service", calls)
        self.assertLess(calls.index("enable --now argus-web-guard.service"), calls.index("start --no-block"))

    def host_script(self, name, shims, state_files, env=None):
        """Run one host script under bash with stand-ins on PATH; returns (results, log)."""
        tmp = tempfile.mkdtemp()
        state = os.path.join(tmp, "state")
        sdir = os.path.join(tmp, "bin")
        os.makedirs(state)
        os.makedirs(sdir)
        script = os.path.join(tmp, "script")
        with open(script, "w", newline="\n") as fh:
            fh.write(script_body(name))
        log = os.path.join(tmp, "calls.log")
        for name, body in shims.items():
            path = os.path.join(sdir, name)
            with open(path, "w", newline="\n") as fh:
                fh.write("#!/usr/bin/env bash\n" + body)
            os.chmod(path, 0o755)
        for name, body in state_files.items():
            with open(os.path.join(state, name), "w") as fh:
                fh.write(body)
        e = dict(os.environ, ARGUS_STATE_DIR=winpath(state), LOG=winpath(log), PATH=winpath(sdir) + os.pathsep + os.environ.get("PATH", ""))
        e.update(env or {})
        # stdin closed: a stand-in that drains its input (logger) must not wait on the test runner
        r = subprocess.run([WHICH("bash"), winpath(script)], env=e, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=60)
        self.assertEqual(r.returncode, 0, r.stderr)
        results = {}
        for name in os.listdir(state):
            if name.endswith("-result.json"):
                with open(os.path.join(state, name)) as fh:
                    results[name] = json.load(fh)
            else:
                results[name] = "still there"
        calls = ""
        if os.path.exists(log):
            with open(log) as fh:
                calls = fh.read()
        return results, calls

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_reboot_now_reboots_when_the_os_asks_and_says_so(self):
        flag = os.path.join(tempfile.mkdtemp(), "reboot-required")
        with open(flag, "w") as fh:
            fh.write("")
        shims = {"systemctl": "echo \"systemctl $*\" >> \"$LOG\"\n", "logger": ":\n"}
        results, calls = self.host_script("argus-reboot-check", shims,
                                          {"reboot-request.json": '{"at":1}'}, {"ARGUS_REBOOT_FLAG": winpath(flag)})
        self.assertNotIn("reboot-request.json", results, "the request must be consumed")
        self.assertEqual((results["reboot-result.json"]["state"], results["reboot-result.json"]["why"]), ("rebooting", "request"))
        self.assertIn("systemctl reboot", calls)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_reboot_now_without_a_pending_reboot_reports_done(self):
        shims = {"systemctl": "echo \"systemctl $*\" >> \"$LOG\"\n", "logger": ":\n"}
        results, calls = self.host_script("argus-reboot-check", shims,
                                          {"reboot-request.json": '{"at":1}'}, {"ARGUS_REBOOT_FLAG": winpath(os.path.join(tempfile.mkdtemp(), "absent"))})
        self.assertEqual((results["reboot-result.json"]["state"], results["reboot-result.json"]["detail"]), ("done", "no reboot is needed"))
        self.assertNotIn("systemctl reboot", calls)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_reboot_window_alone_does_not_reboot_outside_it(self):
        shims = {"systemctl": "echo \"systemctl $*\" >> \"$LOG\"\n", "logger": ":\n"}
        flag = os.path.join(tempfile.mkdtemp(), "reboot-required")
        with open(flag, "w") as fh:
            fh.write("")
        results, calls = self.host_script("argus-reboot-check", shims,
                                          {"reboot-window.json": '{"mode":"notify"}'}, {"ARGUS_REBOOT_FLAG": winpath(flag)})
        self.assertNotIn("reboot-result.json", results)
        self.assertNotIn("systemctl reboot", calls)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_patch_now_runs_the_nightly_policy_and_reports(self):
        shims = {"unattended-upgrade": "echo \"unattended-upgrade $*\" >> \"$LOG\"; exit \"${UA_RC:-0}\"\n", "logger": "cat > /dev/null\n"}
        results, calls = self.host_script("argus-patch", shims, {"patch-request.json": '{"at":1}'})
        self.assertNotIn("patch-request.json", results)
        self.assertEqual((results["patch-result.json"]["state"], results["patch-result.json"]["detail"]), ("done", "security updates applied"))
        self.assertIn("unattended-upgrade -v", calls)
        failed, _ = self.host_script("argus-patch", shims, {"patch-request.json": '{"at":1}'}, {"UA_RC": "1"})
        self.assertEqual(failed["patch-result.json"]["state"], "failed")
        self.assertIn("unattended-upgrade reported a problem", failed["patch-result.json"]["detail"])
        quiet, _ = self.host_script("argus-patch", shims, {})
        self.assertEqual(quiet, {}, "without a request the patcher does nothing")

    def zbx_update(self, zi, zc, request=True, apt_rc=0, window=None):
        """Run the updater with stand-ins for apt, dpkg and systemctl; returns (result, log, request_left)."""
        tmp = tempfile.mkdtemp()
        state = os.path.join(tmp, "state")
        shims = os.path.join(tmp, "bin")
        os.makedirs(state)
        os.makedirs(shims)
        script = os.path.join(tmp, "argus-zbx-update")
        with open(script, "w", newline="\n") as fh:
            fh.write(script_body("argus-zbx-update"))
        log = os.path.join(tmp, "calls.log")
        for name, body in {
            "dpkg-query": "case \"$*\" in *'zabbix-*'*) printf 'zabbix-server-pgsql zabbix-frontend-php ' ;; *) printf '%s' \"$ZI\" ;; esac\n",
            "apt-cache": "printf 'Candidate: %s\\n' \"$ZC\"\n",
            "apt-get": "echo \"apt-get $*\" >> \"$LOG\"; case \"$*\" in *install*) exit \"${APT_RC:-0}\" ;; esac\n",
            "systemctl": "echo \"systemctl $*\" >> \"$LOG\"\n",
            "logger": "echo \"logger $*\" >> \"$LOG\"\n",
        }.items():
            path = os.path.join(shims, name)
            with open(path, "w", newline="\n") as fh:
                fh.write("#!/usr/bin/env bash\n" + body)
            os.chmod(path, 0o755)
        if request:
            with open(os.path.join(state, "zbx-update-request.json"), "w") as fh:
                fh.write('{"at":1,"by":"test"}')
        if window:
            with open(os.path.join(state, "zbx-update-window.json"), "w") as fh:
                fh.write(window)
        env = dict(os.environ, ARGUS_STATE_DIR=winpath(state), ZI=zi, ZC=zc, LOG=winpath(log), APT_RC=str(apt_rc),
                   PATH=winpath(shims) + os.pathsep + os.environ.get("PATH", ""))
        r = subprocess.run([WHICH("bash"), winpath(script)], env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=60)
        self.assertEqual(r.returncode, 0, r.stderr)
        result = None
        rp = os.path.join(state, "zbx-update-result.json")
        if os.path.exists(rp):
            with open(rp) as fh:
                result = json.load(fh)
        calls = ""
        if os.path.exists(log):
            with open(log) as fh:
                calls = fh.read()
        return result, calls, os.path.exists(os.path.join(state, "zbx-update-request.json"))

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_update_now_applies_the_minor_and_reports(self):
        result, calls, left = self.zbx_update("1:7.0.31-1+debian13", "1:7.0.32-1+debian13")
        self.assertFalse(left, "the request must be consumed")
        self.assertEqual((result["state"], result["from"], result["to"], result["why"]), ("done", "7.0.31", "7.0.32", "request"))
        self.assertIn("--only-upgrade", calls)
        self.assertIn("zabbix-server-pgsql zabbix-frontend-php", calls)
        self.assertIn("systemctl restart zabbix-server", calls)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_update_now_refuses_a_major_jump_and_says_so(self):
        result, calls, left = self.zbx_update("1:7.0.31-1", "1:8.0.1-1")
        self.assertFalse(left)
        self.assertEqual(result["state"], "failed")
        self.assertIn("not on the 7.0 line", result["detail"])
        self.assertNotIn("--only-upgrade", calls)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_update_now_on_a_current_core_reports_done(self):
        result, calls, _ = self.zbx_update("1:7.0.32-1", "1:7.0.32-1")
        self.assertEqual((result["state"], result["detail"]), ("done", "already up to date"))
        self.assertNotIn("--only-upgrade", calls)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_update_now_reports_an_apt_failure(self):
        result, calls, _ = self.zbx_update("1:7.0.31-1", "1:7.0.32-1", apt_rc=1)
        self.assertEqual(result["state"], "failed")
        self.assertIn("apt-get could not", result["detail"])
        self.assertNotIn("systemctl restart", calls)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_without_a_request_the_window_rules(self):
        result, calls, _ = self.zbx_update("1:7.0.31-1", "1:7.0.32-1", request=False, window='{"mode":"notify"}')
        self.assertIsNone(result)
        self.assertNotIn("--only-upgrade", calls)


class ConsoleTest(unittest.TestCase):
    """argus-console (DESIGN 14f): the menu's actions, driven through faked commands and a rooted file tree."""

    def setUp(self):
        self.root = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.root, True)
        self.state = os.path.join(self.root, "update")
        os.makedirs(self.state)
        self.mod = m = load("argus-console", {"ARGUS_CONSOLE_ROOT": self.root, "ARGUS_STATE_DIR": self.state, "ARGUS_CONSOLE_ROLE": "core"})
        self.addCleanup(os.environ.pop, "ARGUS_CONSOLE_ROLE", None)
        os.makedirs(m.NET_DIR)
        with open(m.DHCP_FILE, "w") as fh:
            fh.write("[Match]\nName=en* eth*\n\n[Network]\nDHCP=yes\n")
        os.makedirs(os.path.dirname(m.RESOLV_FILES[0]))
        with open(m.RESOLV_FILES[0], "w") as fh:
            fh.write("nameserver 10.0.0.1\n")
        self.calls, self.prompts, self.answers, self.outputs = [], [], [], {}
        self.remote, self.enter = False, False

        def fake_run(cmd, input_text=None, timeout=120):
            self.calls.append(" ".join(cmd))
            for prefix, result in self.outputs.items():
                if tuple(cmd[:len(prefix)]) == prefix:
                    return result
            return 0, "", ""

        def fake_ask(prompt, default=None):
            self.prompts.append(prompt)
            a = self.answers.pop(0) if self.answers else ""
            return a if a != "" else (default or "")

        self.fake_run, self.fake_ask = fake_run, fake_ask
        self.unreachable = set()
        self.fake(m)
        self.net()

    def fake(self, m):
        m.run = self.fake_run
        m.ask = self.fake_ask
        m.is_remote = lambda: self.remote
        m.wait_enter = lambda seconds: self.enter
        m.log = lambda msg: self.calls.append("log " + msg)
        m.clear = lambda: None
        m.pause = lambda: None
        m.interactive = lambda cmd, env=None, preexec=None: self.calls.append("interactive " + " ".join(cmd)) or 0
        m.socket = types.SimpleNamespace(gethostname=lambda: "zbx-core", getaddrinfo=lambda *a: [], create_connection=self.connect)

    def probe(self, core="10.0.0.10", checkin="https://monitoring.example.com/api/probes/checkin", pinned=""):
        """Reload the console as a probe VM: proxy.env in the data folder, probe.env on the host."""
        state = os.path.join(self.root, "docker", "argus-probe")
        os.makedirs(os.path.join(state, "enroll"), exist_ok=True)
        with open(os.path.join(state, "enroll", "proxy.env"), "w") as fh:
            fh.write("PROXY_NAME=site1\nCORE_HOST=%s\nPROBE_TOKEN=tok-123\nCHECKIN_URL=%s\n" % (core, checkin))
        os.makedirs(os.path.join(self.root, "etc", "argus-probe"), exist_ok=True)
        with open(os.path.join(self.root, "etc", "argus-probe", "probe.env"), "w") as fh:
            fh.write("ARGUS_ENROLL_URL=x\n" + ("ZBX_SERVER_HOST=%s\n" % pinned if pinned else ""))
        os.environ.pop("ARGUS_STATE_DIR", None)
        self.mod = m = load("argus-console", {"ARGUS_CONSOLE_ROOT": self.root, "ARGUS_CONSOLE_ROLE": "probe"})
        self.fake(m)
        self.state = m.STATE_DIR
        return m

    def connect(self, addr, timeout=5):
        if addr[0] in self.unreachable:
            raise OSError("timed out")
        return contextlib.nullcontext()

    def net(self, dev="ens18", gw="10.0.0.1", addr="10.0.0.10/24"):
        self.outputs[("ip", "-j", "route", "show", "default")] = (0, json.dumps([{"dev": dev, "gateway": gw}]), "")
        ip, plen = addr.split("/")
        self.outputs[("ip", "-j", "-4", "addr", "show", "dev", dev)] = (
            0, json.dumps([{"addr_info": [{"family": "inet", "scope": "global", "local": ip, "prefixlen": int(plen)}]}]), "")

    def do(self, fn, *answers):
        """Run a menu action with scripted answers; (what it printed, what it returned)."""
        self.answers = list(answers)
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf):
            result = fn()
        return buf.getvalue(), result

    def static(self):
        return self.mod.read_file(self.mod.STATIC_FILE) if os.path.exists(self.mod.STATIC_FILE) else None

    STATIC = ("# Written by argus-console: a static address for the primary NIC. Delete this file (or pick DHCP\n"
              "# in the console menu) to go back to the DHCP of 10-argus-dhcp.network.\n"
              "[Match]\nName=ens18\n\n[Network]\nAddress=10.0.0.20/24\nGateway=10.0.0.1\nDNS=10.0.0.1\nDNS=1.1.1.1\nDomains=example.lan\n")

    def test_a_static_address_is_validated_written_and_applied(self):
        text, _ = self.do(self.mod.configure_network, "2", "10.0.0.20", "10.0.0.20/24", "10.0.1.1", "10.0.0.1",
                          "10.0.0.1 1.1.1.1", "example.lan", "yes")
        self.assertIn("prefix length", text, "an address without its prefix is refused")
        self.assertIn("outside 10.0.0.0/24", text, "a gateway outside the network is refused")
        self.assertEqual(self.static(), self.STATIC)
        self.assertEqual(self.mod.net_mode(), "static")
        self.assertIn("networkctl reload", self.calls)
        self.assertIn("networkctl reconfigure ens18", self.calls)
        self.assertFalse(any(c.startswith("systemd-run") for c in self.calls), "on the console nothing is armed")
        self.assertFalse(os.path.exists(self.mod.REVERT_BACKUP))
        self.assertIn("log network changed to static 10.0.0.20/24 via 10.0.0.1 on ens18, DNS 10.0.0.1 1.1.1.1", self.calls)

    def test_without_the_yes_nothing_changes(self):
        self.do(self.mod.configure_network, "2", "10.0.0.20/24", "10.0.0.1", "10.0.0.1", "", "no")
        self.assertIsNone(self.static())
        self.assertNotIn("networkctl reload", self.calls)

    def test_dhcp_removes_the_static_file(self):
        with open(self.mod.STATIC_FILE, "w") as fh:
            fh.write("[Match]\nName=ens18\n")
        self.do(self.mod.configure_network, "1")
        self.assertIsNone(self.static())
        self.assertIn("networkctl reload", self.calls)
        self.assertIn("log network changed to DHCP on ens18", self.calls)
        text, _ = self.do(self.mod.configure_network, "1")
        self.assertIn("Already DHCP", text)

    def test_over_ssh_a_change_reverts_unless_confirmed_or_reached(self):
        self.remote = True
        text, _ = self.do(self.mod.configure_network, "2", "10.0.0.20/24", "10.0.0.1", "10.0.0.1", "", "yes")
        armed = [c for c in self.calls if c.startswith("systemd-run")]
        self.assertEqual(len(armed), 1)
        self.assertIn("--on-active=180", armed[0])
        self.assertIn("--revert-network", armed[0])
        self.assertTrue(os.path.exists(self.mod.REVERT_BACKUP))
        self.assertIsNotNone(self.static())
        self.assertIn("reverts in a moment", text)
        # The timer fires: the files from before the change come back.
        self.assertEqual(self.mod.revert_network_main(), 0)
        self.assertIsNone(self.static())
        self.assertFalse(os.path.exists(self.mod.REVERT_BACKUP))
        self.assertIn("log network change reverted: no login within 180 s", self.calls)
        # Enter in a session that survived keeps it.
        self.enter, self.calls = True, []
        self.do(self.mod.configure_network, "2", "10.0.0.20/24", "10.0.0.1", "10.0.0.1", "", "yes")
        self.assertIn("systemctl stop argus-console-net-revert.timer", self.calls)
        self.assertFalse(os.path.exists(self.mod.REVERT_BACKUP))
        self.assertIsNotNone(self.static())
        self.assertIn("log network change kept: confirmed in the session", self.calls)
        # A new login reaching the VM keeps it too (the menu does this as it starts).
        self.enter, self.calls = False, []
        self.do(self.mod.configure_network, "1")
        self.assertTrue(os.path.exists(self.mod.REVERT_BACKUP))
        self.assertTrue(self.mod.cancel_revert("a login reached the VM on the new address"))
        self.assertFalse(os.path.exists(self.mod.REVERT_BACKUP))
        self.assertIsNone(self.static())
        self.assertFalse(self.mod.cancel_revert("again"), "nothing pending")

    def test_when_networkd_refuses_the_files_go_back(self):
        self.outputs[("networkctl", "reload")] = (1, "", "Failed to reload")
        text, _ = self.do(self.mod.configure_network, "2", "10.0.0.20/24", "10.0.0.1", "10.0.0.1", "", "yes")
        self.assertIn("did not take the change", text)
        self.assertIsNone(self.static())

    def test_hostname_change_rewrites_the_hosts_line(self):
        with open(self.mod.HOSTS_FILE, "w") as fh:
            fh.write("127.0.0.1\tlocalhost\n127.0.1.1\tzbx-core\n")
        text, _ = self.do(self.mod.change_hostname, "Bad_Name!", "core-01.example.lan")
        self.assertEqual(len(self.prompts), 2, "an invalid name is asked again")
        self.assertIn("hostnamectl set-hostname core-01.example.lan", self.calls)
        self.assertEqual(self.mod.read_file(self.mod.HOSTS_FILE), "127.0.0.1\tlocalhost\n127.0.1.1\tcore-01.example.lan core-01\n")
        self.assertIn("log hostname changed zbx-core -> core-01.example.lan", self.calls)
        self.calls = []
        text, _ = self.do(self.mod.change_hostname, "")
        self.assertIn("Unchanged", text)
        self.assertEqual(self.calls, [])

    def test_the_status_block_reads_the_host(self):
        m = self.mod
        self.outputs[("timedatectl", "show", "-p", "NTPSynchronized", "--value")] = (0, "yes\n", "")
        self.outputs[("timedatectl", "show", "-p", "Timezone", "--value")] = (0, "Europe/Rome\n", "")
        self.outputs[("systemctl", "is-active", "docker")] = (3, "inactive\n", "")
        self.outputs[("systemctl", "is-active")] = (0, "active\n", "")
        self.outputs[("docker", "inspect", "-f", m.INSPECT_FMT, "argus")] = (0, "running|healthy|sha256:aaa\n", "")
        self.outputs[("docker", "inspect", "-f", m.IMAGE_FMT, "sha256:aaa")] = (0, "<no value>|v0.9.4\n", "")
        self.outputs[("docker", "inspect", "-f", m.INSPECT_FMT, "argus-updater")] = (1, "", "Error: No such object")
        with open(os.path.join(self.state, "os-status.json"), "w") as fh:
            fh.write('{"sec_updates":3,"reboot_required":true,"zbx_server":"1:7.0.32-1","zbx_candidate":"1:7.0.33-1"}')
        text, _ = self.do(lambda: m.render_status(m.status()))
        for needle in ("zbx-core", "10.0.0.10/24 via 10.0.0.1 (DHCP) on ens18", "https://10.0.0.10/", "%-11s %s" % ("DNS", "10.0.0.1"),
                       "synced (NTP)  Europe/Rome", "3 security updates, reboot pending, Zabbix 1:7.0.33-1 available",
                       "Docker inactive", "Argus v0.9.4 healthy", "updater missing"):
            self.assertIn(needle, text)
        with open(m.STATIC_FILE, "w") as fh:
            fh.write("[Match]\nName=ens18\n")
        text, _ = self.do(lambda: m.render_status(m.status()))
        self.assertIn("(static)", text)
        self.assertIn("/ ___|| | | |/ ___|", text, "the banner")
        self.assertIn("Monitoring cockpit, core", text)

    def test_versions_come_from_the_image_not_the_container(self):
        m = self.mod
        # The updater's recreate clones the old container's config, labels included: only the image tells.
        self.outputs[("docker", "inspect", "-f", m.INSPECT_FMT, "argus-updater")] = (0, "running|healthy|sha256:bbb\n", "")
        self.outputs[("docker", "inspect", "-f", m.IMAGE_FMT, "sha256:bbb")] = (0, "0.2.16|latest\n", "")
        self.assertEqual(m.container_state("argus-updater"), ("healthy", "0.2.16"), "the updater's own label wins over the OCI one")
        self.outputs[("docker", "inspect", "-f", m.IMAGE_FMT, "sha256:bbb")] = (0, "|v0.9.4-1-gee76ed1\n", "")
        self.assertEqual(m.container_state("argus-updater"), ("healthy", "v0.9.4-1-gee76ed1"))
        self.outputs[("docker", "inspect", "-f", m.IMAGE_FMT, "sha256:bbb")] = (1, "", "gone")
        self.assertEqual(m.container_state("argus-updater"), ("healthy", ""))
        self.outputs[("docker", "inspect", "-f", m.INSPECT_FMT, "argus-updater")] = (0, "exited||sha256:bbb\n", "")
        self.assertEqual(m.container_state("argus-updater")[0], "exited")

    def test_one_disk_line_when_docker_sits_on_the_root_file_system(self):
        m = self.mod
        m.DOCKER_DIR = self.root
        m.same_fs = lambda a, b: True
        lines = m.disk_lines()
        self.assertEqual(len(lines), 1)
        self.assertTrue(lines[0].startswith("/ ") and lines[0].endswith("(holds /docker)"), lines)
        m.same_fs = lambda a, b: False
        self.assertEqual(len(m.disk_lines()), 2)

    def test_make_login_shell_wires_the_account_and_undoes_it(self):
        m = self.mod
        as_root(self)
        os.makedirs(os.path.dirname(m.SUDOERS_FILE))
        with open(m.SHELLS_FILE, "w") as fh:
            fh.write("/bin/sh\n/bin/bash\n")
        m.user_exists = lambda u: u == "argus"
        text, rc = self.do(lambda: m.main(["--make-login-shell", "nobody-here"]))
        self.assertEqual(rc, 1)
        self.assertIn("no user nobody-here", text)
        text, rc = self.do(lambda: m.main(["--make-login-shell", "argus"]))
        self.assertEqual(rc, 0)
        self.assertEqual(m.read_file(m.SHELLS_FILE), "/bin/sh\n/bin/bash\n/usr/local/sbin/argus-console-login\n")
        self.assertIn("argus ALL=(root) NOPASSWD: /usr/local/sbin/argus-console\n", m.read_file(m.SUDOERS_FILE))
        self.assertIn("visudo -cf " + m.SUDOERS_FILE, self.calls)
        self.assertIn("usermod -s /usr/local/sbin/argus-console-login argus", self.calls)
        self.assertIn("log console menu is now the login shell of argus", self.calls)
        self.assertIn("Undo with", text)
        # Twice is fine: /etc/shells gets the line once.
        self.do(lambda: m.main(["--make-login-shell", "argus"]))
        self.assertEqual(m.read_file(m.SHELLS_FILE).count("argus-console-login"), 1)
        # A sudoers line visudo rejects is not left behind.
        self.outputs[("visudo",)] = (1, "", "syntax error")
        text, rc = self.do(lambda: m.main(["--make-login-shell", "argus"]))
        self.assertEqual(rc, 0)
        self.assertFalse(os.path.exists(m.SUDOERS_FILE))
        self.assertIn("will ask the password", text)
        del self.outputs[("visudo",)]
        self.do(lambda: m.main(["--make-login-shell", "argus"]))
        self.assertTrue(os.path.exists(m.SUDOERS_FILE))
        # The account comes from sudo when none is named; root is refused.
        os.environ["SUDO_USER"] = "argus"
        self.addCleanup(os.environ.pop, "SUDO_USER", None)
        text, rc = self.do(lambda: m.main(["--remove-login-shell"]))
        self.assertEqual(rc, 0)
        self.assertIn("usermod -s /bin/bash argus", self.calls)
        self.assertFalse(os.path.exists(m.SUDOERS_FILE))
        os.environ["SUDO_USER"] = "root"
        text, rc = self.do(lambda: m.main(["--make-login-shell"]))
        self.assertEqual(rc, 1)
        self.assertIn("root keeps its shell", text)

    def test_restarts_ask_first(self):
        self.do(lambda: self.mod.restart("Argus", container="argus"), "no")
        self.assertNotIn("docker restart argus", self.calls)
        self.do(lambda: self.mod.restart("Argus", container="argus"), "yes")
        self.assertIn("docker restart argus", self.calls)
        self.do(lambda: self.mod.restart("Zabbix server", unit="zabbix-server"), "yes")
        self.assertIn("systemctl restart zabbix-server", self.calls)
        self.assertIn("log Zabbix server restarted from the console", self.calls)

    def test_grow_disk_knows_the_device_names(self):
        split = self.mod.split_partition
        self.assertEqual(split("/dev/sda1"), ("/dev/sda", 1))
        self.assertEqual(split("/dev/vda3"), ("/dev/vda", 3))
        self.assertEqual(split("/dev/xvda1"), ("/dev/xvda", 1))
        self.assertEqual(split("/dev/nvme0n1p2"), ("/dev/nvme0n1", 2))
        self.assertEqual(split("/dev/mmcblk0p2"), ("/dev/mmcblk0", 2))
        self.assertEqual(split("/dev/mapper/vg-root"), (None, None))
        self.outputs[("findmnt", "-n", "-o", "SOURCE", "/")] = (0, "/dev/sda1\n", "")
        self.outputs[("findmnt", "-n", "-o", "FSTYPE", "/")] = (0, "ext4\n", "")
        self.do(self.mod.grow_disk, "yes")
        self.assertIn("growpart /dev/sda 1", self.calls)
        self.assertIn("resize2fs /dev/sda1", self.calls)
        self.calls = []
        self.outputs[("growpart",)] = (1, "NOCHANGE: partition 1 is size 419428319. it cannot be grown", "")
        text, _ = self.do(self.mod.grow_disk, "yes")
        self.assertIn("already fills the disk", text)
        self.assertNotIn("resize2fs /dev/sda1", self.calls)
        self.outputs[("growpart",)] = (127, "", "growpart is not installed")
        text, _ = self.do(self.mod.grow_disk, "yes")
        self.assertIn("cloud-guest-utils", text)
        self.calls = []
        self.outputs[("growpart",)] = (0, "CHANGED: partition=1 start=2048 old: size=1 end=2 new: size=3 end=4", "")
        self.outputs[("findmnt", "-n", "-o", "FSTYPE", "/")] = (0, "xfs\n", "")
        self.do(self.mod.grow_disk, "yes")
        self.assertIn("xfs_growfs /", self.calls)
        self.outputs[("findmnt", "-n", "-o", "SOURCE", "/")] = (0, "/dev/mapper/vg-root\n", "")
        text, _ = self.do(self.mod.grow_disk, "yes")
        self.assertIn("doesn't know how to grow", text)

    def test_connectivity_reports_each_check(self):
        self.unreachable = {"deb.debian.org"}
        self.outputs[("ping",)] = (0, "", "")
        self.outputs[("timedatectl",)] = (0, "no\n", "")
        text, results = self.do(self.mod.test_connectivity)
        self.assertIn("PASS  gateway 10.0.0.1", text)
        self.assertIn("PASS  DNS (10.0.0.1)", text)
        self.assertIn("PASS  ghcr.io:443", text)
        self.assertIn("FAIL  deb.debian.org:443  no connection", text)
        self.assertIn("FAIL  time sync (NTP)  clock not synchronised", text)
        self.assertEqual(len(results), 6)

    def test_os_updates_run_only_when_pending(self):
        with open(os.path.join(self.state, "os-status.json"), "w") as fh:
            fh.write('{"sec_updates":0,"reboot_required":true}')
        text, _ = self.do(self.mod.install_updates)
        self.assertIn("No security updates are pending", text)
        self.assertIn("A reboot is pending", text)
        self.assertFalse(any(c.startswith("interactive") for c in self.calls))
        with open(os.path.join(self.state, "os-status.json"), "w") as fh:
            fh.write('{"sec_updates":2,"reboot_required":false}')
        self.do(self.mod.install_updates, "yes")
        self.assertIn("interactive unattended-upgrade -v", self.calls)
        self.assertIn("log security updates installed from the console", self.calls)

    def test_keyboard_and_the_time_zone_argus_manages(self):
        m = self.mod
        with open(m.VCONSOLE_FILE, "w") as fh:
            fh.write("KEYMAP=us\nFONT=eurlatgr\n")
        with open(os.path.join(self.state, "timezone.json"), "w") as fh:
            fh.write('{"tz":"Europe/Rome"}')
        text, _ = self.do(m.keyboard_and_timezone, "it")
        self.assertEqual(m.read_file(m.VCONSOLE_FILE), "KEYMAP=it\nFONT=eurlatgr\n")
        self.assertIn("systemctl restart systemd-vconsole-setup.service", self.calls)
        self.assertIn("follows the one set in Argus", text)
        self.assertEqual(len(self.prompts), 1, "no time zone question while Argus manages it")
        os.remove(os.path.join(self.state, "timezone.json"))
        os.makedirs(self.root + "/usr/share/zoneinfo/Europe")
        with open(self.root + "/usr/share/zoneinfo/Europe/Rome", "w") as fh:
            fh.write("")
        self.calls = []
        text, _ = self.do(m.keyboard_and_timezone, "", "Mars/Olympus")
        self.assertIn("not in the zoneinfo database", text)
        self.assertFalse(any(c.startswith("timedatectl set-timezone") for c in self.calls))
        text, _ = self.do(m.keyboard_and_timezone, "", "Europe/Rome")
        self.assertIn("timedatectl set-timezone Europe/Rome", self.calls)
        self.assertIn("log time zone set to Europe/Rome", self.calls)

    def test_reboot_and_power_off_want_the_word(self):
        self.do(lambda: self.mod.power("reboot"), "no")
        self.assertNotIn("systemctl reboot", self.calls)
        self.do(lambda: self.mod.power("reboot"), "REBOOT")
        self.assertIn("systemctl reboot", self.calls)
        self.do(lambda: self.mod.power("poweroff"), "poweroff")
        self.assertIn("systemctl poweroff", self.calls)
        self.assertIn("log poweroff requested from the console", self.calls)

    def test_the_menu_shows_logs_out_and_refuses_non_root(self):
        as_root(self)
        m = self.mod
        text, rc = self.do(lambda: m.main([]), "15")
        self.assertEqual(rc, 0)
        self.assertIn("Monitoring cockpit, core", text)
        self.assertIn("15) Log out", text)
        self.assertIn("!! Reboot the VM !!", text)
        text, rc = self.do(lambda: m.main(["--status"]))
        self.assertEqual(rc, 0)
        self.assertIn("Address", text)
        # A pending network revert is kept by the login that reached the VM.
        m.save_network_backup()
        text, rc = self.do(lambda: m.main([]), "15")
        self.assertIn("is kept", text)
        self.assertFalse(os.path.exists(m.REVERT_BACKUP))
        os.geteuid = lambda: 1000
        buf = io.StringIO()
        with contextlib.redirect_stderr(buf):
            self.assertEqual(m.main([]), 1)
        self.assertIn("sudo argus-console", buf.getvalue())

    def test_a_probe_vm_is_told_apart_and_shows_its_core(self):
        m = self.probe()
        self.assertEqual(m.ROLE, "probe")
        self.assertEqual(m.STATE_DIR, self.root + "/docker/argus-probe", "the probe's data folder is its state dir")
        self.outputs[("docker", "inspect", "-f", m.INSPECT_FMT, "argus-probe")] = (0, "running|healthy|sha256:p\n", "")
        self.outputs[("docker", "inspect", "-f", m.IMAGE_FMT, "sha256:p")] = (0, "|7.0.32-r1\n", "")
        self.outputs[("docker", "inspect", "-f", m.INSPECT_FMT, "argus-updater")] = (0, "running|healthy|sha256:u\n", "")
        self.outputs[("docker", "inspect", "-f", m.IMAGE_FMT, "sha256:u")] = (0, "0.2.17|latest\n", "")
        with open(os.path.join(self.root, "docker", "argus-probe", "host-tools.json"), "w") as fh:
            fh.write('{"state":"ok","version":"7.0.32-r1","at":"2026-10-23T00:00:00Z"}')
        text, _ = self.do(lambda: m.render_status(m.status()))
        self.assertIn("Monitoring cockpit, probe site1", text)
        self.assertIn("%-11s %s" % ("Core", "10.0.0.10   check-in monitoring.example.com"), text)
        self.assertNotIn("Web ", text)
        self.assertIn("Probe 7.0.32-r1 healthy   updater 0.2.17 healthy", text)
        self.assertIn("%-11s %s" % ("Tools", "ok  7.0.32-r1"), text)
        text, _ = self.do(lambda: m.render_menu())
        self.assertIn("6) Check in with Argus now", text)
        self.assertIn("8) The core this probe talks to", text)
        self.assertNotIn("Zabbix server", text)
        self.assertNotIn("console password", text)
        # Role detection without the override: proxy.env present and no Zabbix server = a probe.
        os.environ.pop("ARGUS_CONSOLE_ROLE", None)
        self.assertEqual(load("argus-console", {"ARGUS_CONSOLE_ROOT": self.root}).ROLE, "probe")
        os.makedirs(os.path.join(self.root, "etc", "zabbix"))
        with open(os.path.join(self.root, "etc", "zabbix", "zabbix_server.conf"), "w") as fh:
            fh.write("")
        self.assertEqual(load("argus-console", {"ARGUS_CONSOLE_ROOT": self.root}).ROLE, "core", "a core wins when both signs exist")

    def test_a_probe_checks_its_core_in_the_connectivity_test(self):
        m = self.probe()
        self.unreachable = {"10.0.0.10"}
        self.outputs[("ping",)] = (0, "", "")
        self.outputs[("timedatectl",)] = (0, "yes\n", "")
        text, results = self.do(m.test_connectivity)
        self.assertIn("FAIL  core 10.0.0.10:10051 (Zabbix server)  no connection", text)
        self.assertIn("PASS  check-in monitoring.example.com:443 (Argus)", text)
        self.assertEqual(len(results), 8)

    def test_check_in_now_uses_the_probe_credential(self):
        m = self.probe()
        posts = []
        m.http_post = lambda url, token, body, timeout=15: posts.append((url, token, body)) or (200, '{"target":"latest"}')
        text, _ = self.do(m.checkin_now)
        self.assertEqual(posts, [("https://monitoring.example.com/api/probes/checkin", "tok-123", {})])
        self.assertIn("check-in recorded, fleet target latest", text)
        m.http_post = lambda url, token, body, timeout=15: (401, '{"error":"unauthorized"}')
        text, _ = self.do(m.checkin_now)
        self.assertIn("HTTP 401", text)
        m.http_post = lambda url, token, body, timeout=15: (0, "timed out")
        text, _ = self.do(m.checkin_now)
        self.assertIn("No answer from monitoring.example.com: timed out", text)

    def test_the_core_a_probe_talks_to_can_be_changed_from_the_console(self):
        m = self.probe()
        text, _ = self.do(m.change_core, "bad host!", "10.0.0.20", "ftp://x", "https://new.example.com/api/probes/checkin", "yes")
        self.assertIn("looks like https://", text)
        meta = m.read_kv(m.PROBE_META)
        self.assertEqual((meta["CORE_HOST"], meta["CHECKIN_URL"], meta["PROBE_TOKEN"]), ("10.0.0.20", "https://new.example.com/api/probes/checkin", "tok-123"))
        self.assertIn("docker restart argus-probe", self.calls)
        self.assertIn("log core changed from the console: 10.0.0.20, https://new.example.com/api/probes/checkin", self.calls)
        # Pinned on the container: probe.env moves too and the unit recreates the container.
        m = self.probe(pinned="10.0.0.10")
        self.do(m.change_core, "core.example.lan", "", "yes")
        self.assertEqual(m.read_kv(m.PROBE_ENV)["ZBX_SERVER_HOST"], "core.example.lan")
        self.assertEqual(m.read_kv(m.PROBE_ENV)["ARGUS_ENROLL_URL"], "x", "other keys stay")
        self.assertIn("systemctl restart argus-probe.service", self.calls)
        text, _ = self.do(m.change_core, "", "")
        self.assertIn("Unchanged", text)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_the_login_wrapper_opens_the_menu_or_passes_a_command_to_bash(self):
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp, True)
        shims = os.path.join(tmp, "bin")
        os.makedirs(shims)
        with open(os.path.join(shims, "sudo"), "w", newline="\n") as fh:
            fh.write("#!/usr/bin/env bash\necho \"sudo $*\"\n")
        os.chmod(os.path.join(shims, "sudo"), 0o755)
        script = os.path.join(tmp, "argus-console-login")
        with open(script, "w", newline="\n") as fh:
            fh.write(script_body("argus-console-login"))
        env = dict(os.environ, PATH=winpath(shims) + os.pathsep + os.environ.get("PATH", ""))
        r = subprocess.run([WHICH("bash"), winpath(script)], env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=30)
        self.assertEqual(r.stdout.strip(), "sudo /usr/local/sbin/argus-console")
        r = subprocess.run([WHICH("bash"), winpath(script), "-c", "echo passed-through"], env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=30)
        self.assertEqual(r.stdout.strip(), "passed-through", "an ssh command or scp goes to bash, not the menu")


# A stateful stand-in for iptables/ip6tables: the chain and INPUT's rules live in files, as `-S`
# prints them, so the guard's idempotence and clean-up are tested against what it would leave.
FAKE_IPT = r'''
[ "$1" = "-w" ] && shift
d="$IPT_DIR/$(basename "$0")"; mkdir -p "$d"
echo "$(basename "$0") $*" >> "$LOG"
op="$1"; ch="$2"; shift 2
case "$op" in
  -S) if [ "$ch" = INPUT ]; then echo "-P INPUT ACCEPT"; cat "$d/input" 2>/dev/null; exit 0; fi
      [ -f "$d/$ch" ] || exit 1; cat "$d/$ch" ;;
  -N) [ -f "$d/$ch" ] && exit 1; echo "-N $ch" > "$d/$ch" ;;
  -A) [ -f "$d/$ch" ] || exit 1; echo "-A $ch $*" >> "$d/$ch" ;;
  -F) [ -f "$d/$ch" ] || exit 1; echo "-N $ch" > "$d/$ch" ;;
  -X) [ -f "$d/$ch" ] || exit 1; rm -f "$d/$ch" ;;
  -I) shift; echo "-A INPUT $*" >> "$d/input" ;;
  -C) grep -qxF -- "-A INPUT $*" "$d/input" 2>/dev/null ;;
  -D) grep -vxF -- "-A INPUT $*" "$d/input" > "$d/input.new"; mv "$d/input.new" "$d/input" ;;
esac
'''

# Stand-ins for what the https step calls: nginx -t fails on NGINX_T_FAIL, ss reports :8443 taken on
# SS_TAKEN, openssl writes its -out/-keyout files (and logs the extension file it was given).
FAKE_NGINX = 'echo "nginx $*" >> "$LOG"; [ -z "${NGINX_T_FAIL:-}" ]\n'
FAKE_SS = '[ -n "${SS_TAKEN:-}" ] && echo "LISTEN 0 511 0.0.0.0:8443 0.0.0.0:*"; exit 0\n'
FAKE_OPENSSL = r'''
echo "openssl $*" >> "$LOG"
[ "$1" = rand ] && { echo 00112233445566778899aabbccddeeff; exit 0; }
[ -n "${OPENSSL_FAIL:-}" ] && exit 1
while [ $# -gt 0 ]; do
  case "$1" in
    -out|-keyout) echo fake > "$2"; shift ;;
    -extfile) cat "$2" >> "$LOG"; shift ;;
  esac
  shift
done
'''
FAKE_HOSTNAME = 'case "${1:-}" in -f) echo core.example.lan ;; -I) echo "10.0.0.10 172.17.0.1" ;; *) echo core ;; esac\n'
FAKE_IP = 'echo "1.1.1.1 via 10.0.0.1 dev eth0 src 10.0.0.10 uid 0"\n'

ZBX_VHOST = "server {\n        listen          8080;\n        server_name     _;\n        root    /usr/share/zabbix;\n}\n"
HTTPS_CONF = ("server {\n    listen 443 ssl default_server;\n    location / { proxy_pass http://127.0.0.1:8081; }\n}\n"
              "server {\n    listen 8443 ssl;\n    location / {\n        proxy_pass http://127.0.0.1:8080;\n    }\n}\n")


class WebGuardTest(unittest.TestCase):
    """argus-web-guard: PHP's upload progress off, https on :8443, the plain port for this machine only."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.etc = os.path.join(self.tmp, "etc")
        for d in ("zabbix", "nginx/conf.d", "php/8.4/fpm/conf.d", "php/8.4/cli/conf.d", "argus-core", "pki"):
            os.makedirs(os.path.join(self.etc, d))
        self.write("zabbix/nginx.conf", ZBX_VHOST)
        self.write("nginx/conf.d/argus-https.conf", HTTPS_CONF)
        self.ipt = os.path.join(self.tmp, "ipt")
        self.bin = os.path.join(self.tmp, "bin")
        os.makedirs(self.bin)
        for name, body in {"iptables": FAKE_IPT, "ip6tables": FAKE_IPT,
                           "systemctl": "echo \"systemctl $*\" >> \"$LOG\"\n",
                           "logger": "echo \"logger $*\" >> \"$LOG\"\n",
                           "nginx": FAKE_NGINX, "ss": FAKE_SS, "openssl": FAKE_OPENSSL,
                           "hostname": FAKE_HOSTNAME, "ip": FAKE_IP}.items():
            path = os.path.join(self.bin, name)
            with open(path, "w", newline="\n") as fh:
                fh.write("#!/usr/bin/env bash\n" + body)
            os.chmod(path, 0o755)
        self.script = os.path.join(self.tmp, "argus-web-guard")
        with open(self.script, "w", newline="\n") as fh:
            fh.write(script_body("argus-web-guard"))

    def write(self, rel, body):
        with open(os.path.join(self.etc, rel), "w", newline="\n") as fh:
            fh.write(body)

    def hand_installed(self, ca=True):
        """A core installed by hand: the zabbix vhost and no https frontend, with or without the CA."""
        os.remove(os.path.join(self.etc, "nginx", "conf.d", "argus-https.conf"))
        if ca:
            self.write("pki/ca.crt", "ca\n")
            self.write("pki/ca.key", "key\n")

    def ours(self):
        p = os.path.join(self.etc, "nginx", "conf.d", "argus-zabbix-https.conf")
        if not os.path.exists(p):
            return None
        with open(p) as fh:
            return fh.read()

    def run_guard(self, extra=None):
        log = os.path.join(self.tmp, "calls.log")
        if os.path.exists(log):
            os.remove(log)
        e = dict(os.environ, LOG=winpath(log), IPT_DIR=winpath(self.ipt),
                 ARGUS_ZBX_NGINX=winpath(os.path.join(self.etc, "zabbix", "nginx.conf")),
                 ARGUS_NGINX_DIR=winpath(os.path.join(self.etc, "nginx")),
                 ARGUS_PHP_DIR=winpath(os.path.join(self.etc, "php")),
                 ARGUS_WEB_GUARD_OPEN=winpath(os.path.join(self.etc, "argus-core", "zabbix-http-open")),
                 ARGUS_PKI_DIR=winpath(os.path.join(self.etc, "pki")),
                 ARGUS_WEB_GUARD_NOTE=winpath(os.path.join(self.tmp, "note")),
                 PATH=winpath(self.bin) + os.pathsep + os.environ.get("PATH", ""))
        e.update(extra or {})
        r = subprocess.run([WHICH("bash"), winpath(self.script)], env=e, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=60)
        self.assertEqual(r.returncode, 0, r.stderr)
        if not os.path.exists(log):
            return ""
        with open(log) as fh:
            return fh.read()

    def rules(self, tool="iptables"):
        out = {}
        for name in ("ARGUS-ZBX-WEB", "input"):
            p = os.path.join(self.ipt, tool, name)
            out[name] = open(p).read() if os.path.exists(p) else None
        return out

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_appliance_guards_the_plain_port_once(self):
        calls = self.run_guard()
        for tool in ("iptables", "ip6tables"):
            r = self.rules(tool)
            self.assertEqual(r["ARGUS-ZBX-WEB"].splitlines(), [
                "-N ARGUS-ZBX-WEB", "-A ARGUS-ZBX-WEB -i lo -j RETURN", "-A ARGUS-ZBX-WEB -i docker+ -j RETURN",
                "-A ARGUS-ZBX-WEB -i br-+ -j RETURN", "-A ARGUS-ZBX-WEB -p tcp -j REJECT --reject-with tcp-reset"], tool)
            self.assertEqual(r["input"].splitlines(), ["-A INPUT -p tcp --dport 8080 -j ARGUS-ZBX-WEB"], tool)
        self.assertIn("answers this machine and its containers only", calls)
        self.assertIsNone(self.ours(), "the appliance's own https frontend is left alone")
        self.assertNotIn("openssl", calls)
        with open(os.path.join(self.etc, "php", "8.4", "fpm", "conf.d", "99-argus-hardening.ini")) as fh:
            self.assertIn("session.upload_progress.enabled = Off", fh.read())
        self.assertFalse(os.path.exists(os.path.join(self.etc, "php", "8.4", "cli", "conf.d", "99-argus-hardening.ini")), "php-fpm only")
        self.assertIn("systemctl reload php8.4-fpm", calls)
        again = self.run_guard()
        self.assertNotIn("systemctl reload", again, "an unchanged file reloads nothing")
        self.assertNotIn(" -N ", again, "rules in place are left alone")
        self.assertNotIn("logger", again, "a run that changes nothing says nothing")
        self.assertEqual(self.rules()["input"].count("ARGUS-ZBX-WEB"), 1)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_a_new_port_moves_the_jump(self):
        self.run_guard()
        self.write("zabbix/nginx.conf", ZBX_VHOST.replace("8080", "8088"))
        self.write("nginx/conf.d/argus-https.conf", HTTPS_CONF.replace("127.0.0.1:8080", "127.0.0.1:8088"))
        self.run_guard()
        self.assertEqual(self.rules()["input"].splitlines(), ["-A INPUT -p tcp --dport 8088 -j ARGUS-ZBX-WEB"])

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_hand_installed_core_gets_https_on_8443_then_the_guard(self):
        self.hand_installed()
        calls = self.run_guard()
        conf = self.ours()
        self.assertIn("listen 8443 ssl;", conf)
        self.assertIn("proxy_pass http://127.0.0.1:8080;", conf)
        self.assertIn("ssl_certificate %s;" % winpath(os.path.join(self.etc, "nginx", "argus", "argus-web.crt")), conf)
        self.assertIn("proxy_set_header Host $host;", conf)
        for name in ("argus-web.crt", "argus-web.key"):
            self.assertTrue(os.path.exists(os.path.join(self.etc, "nginx", "argus", name)), name)
        self.assertIn("-subj /CN=core.example.lan", calls)
        self.assertIn("subjectAltName=DNS:core.example.lan,DNS:core,IP:10.0.0.10", calls)
        self.assertIn("-set_serial 0x00112233445566778899aabbccddeeff", calls)
        self.assertIn("nginx -t", calls)
        self.assertIn("systemctl reload nginx", calls)
        self.assertIn(":8443 serves the Zabbix frontend and its API over https", calls)
        self.assertEqual(self.rules()["input"].splitlines(), ["-A INPUT -p tcp --dport 8080 -j ARGUS-ZBX-WEB"])
        quiet = self.run_guard()
        for word in ("logger", "nginx", "openssl"):
            self.assertNotIn(word, quiet, "a run that changes nothing touches nothing")
        # The vhost moves to another port: the https server follows it, and so does the guard.
        self.write("zabbix/nginx.conf", ZBX_VHOST.replace("8080", "8088"))
        calls = self.run_guard()
        self.assertIn("proxy_pass http://127.0.0.1:8088;", self.ours())
        self.assertNotIn("openssl", calls, "the certificate is kept")
        self.assertEqual(self.rules()["input"].splitlines(), ["-A INPUT -p tcp --dport 8088 -j ARGUS-ZBX-WEB"])

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_hand_installed_core_without_the_ca_keeps_its_port_and_drops_an_old_guard(self):
        self.run_guard()
        self.hand_installed(ca=False)
        calls = self.run_guard()
        self.assertIsNone(self.ours())
        self.assertEqual(self.rules(), {"ARGUS-ZBX-WEB": None, "input": ""})
        self.assertIn("no monitoring CA in", calls)
        self.assertIn("no https frontend here", calls)
        quiet = self.run_guard()
        self.assertNotIn("logger", quiet, "nothing to remove, the lasting condition said once")

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_a_taken_8443_is_left_alone(self):
        self.hand_installed()
        calls = self.run_guard(extra={"SS_TAKEN": "1"})
        self.assertIsNone(self.ours())
        self.assertNotIn("openssl", calls)
        self.assertIn(":8443 is taken by another service", calls)
        self.assertEqual(self.rules()["ARGUS-ZBX-WEB"], None)
        self.assertNotIn("logger", self.run_guard(extra={"SS_TAKEN": "1"}), "said once")

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_a_server_nginx_refuses_is_taken_back(self):
        self.hand_installed()
        calls = self.run_guard(extra={"NGINX_T_FAIL": "1"})
        self.assertIsNone(self.ours())
        self.assertNotIn("systemctl reload nginx", calls)
        self.assertIn("nginx refused the :8443 server", calls)
        self.assertEqual(self.rules()["ARGUS-ZBX-WEB"], None)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_an_https_frontend_of_your_own_retires_ours(self):
        self.hand_installed()
        self.run_guard()
        self.write("nginx/conf.d/argus-https.conf", HTTPS_CONF)
        calls = self.run_guard()
        self.assertIsNone(self.ours())
        self.assertIn("the guard's :8443 server removed", calls)
        self.assertEqual(self.rules()["input"].splitlines(), ["-A INPUT -p tcp --dport 8080 -j ARGUS-ZBX-WEB"])

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_an_argus_without_the_zabbix_frontend_is_left_alone(self):
        # Argus on its own VM: the host tools run there too, but there is no zabbix vhost.
        self.hand_installed()
        os.remove(os.path.join(self.etc, "zabbix", "nginx.conf"))
        calls = self.run_guard()
        self.assertIsNone(self.ours())
        self.assertNotIn("openssl", calls)
        self.assertNotIn("nginx", calls)
        self.assertEqual(self.rules()["ARGUS-ZBX-WEB"], None)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_a_plain_proxy_is_not_an_https_frontend(self):
        self.write("nginx/conf.d/argus-https.conf", "server {\n    listen 8443;\n    location / { proxy_pass http://127.0.0.1:8080; }\n}\n")
        self.run_guard()
        self.assertEqual(self.rules()["ARGUS-ZBX-WEB"], None)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_the_open_flag_keeps_the_port_open(self):
        self.run_guard()
        self.write("argus-core/zabbix-http-open", "")
        calls = self.run_guard()
        self.assertEqual(self.rules()["ARGUS-ZBX-WEB"], None)
        self.assertIn("says to keep :8080 open", calls)

    @unittest.skipUnless(WHICH("bash"), "needs bash")
    def test_without_iptables_it_says_so_and_still_turns_upload_progress_off(self):
        calls = self.run_guard(extra={"ARGUS_IPT_TOOLS": "no-such-iptables"})
        self.assertIn("no iptables on this host", calls)
        self.assertTrue(os.path.exists(os.path.join(self.etc, "php", "8.4", "fpm", "conf.d", "99-argus-hardening.ini")))

    def test_manifest_installs_and_enables_it(self):
        with open(os.path.join(HOST, "manifest"), encoding="utf-8") as fh:
            manifest = fh.read()
        self.assertRegex(manifest, r"argus-web-guard +/usr/local/sbin/argus-web-guard +0755\n")
        self.assertRegex(manifest, r"argus-web-guard\.service +/etc/systemd/system/argus-web-guard\.service +0644 enable run\n",
                         "a new guard acts at the update, not at the timer's next tick")
        self.assertRegex(manifest, r"argus-web-guard\.timer +/etc/systemd/system/argus-web-guard\.timer +0644 enable\n")


if __name__ == "__main__":
    unittest.main(verbosity=1)

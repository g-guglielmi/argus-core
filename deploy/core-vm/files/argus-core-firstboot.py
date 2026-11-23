#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (C) 2026 g-guglielmi

"""Argus CORE appliance first-boot setup (DESIGN §14d).

Runs on first boot. Serves a one-form setup page on http://<vm>/ that collects the instance basics
(hostname, console keymap, timezone) and ONE administrator identity (email + password), then
configures the whole core in place while a live progress page shows each step:

  system    hostname, timezone, console keymap, the local Debian sudo user
  database  timescaledb-tune for THIS VM's RAM, zabbix DB + role (generated password), schema import,
            TimescaleDB conversion
  pki       the monitoring CA + core server cert (probe enrollment works out of the box)
  zabbix    zabbix_server.conf (DB + TLS + tuning), frontend zabbix.conf.php (no browser wizard),
            nginx/php-fpm wiring, agent2 self-monitoring, services enabled
  accounts  rotate the default Zabbix Admin password; create the argus-svc super-admin + its API
            token (machine-managed - never shown to a human); housekeeping retention
  https     a server certificate for this VM signed by the monitoring CA, nginx on :443 in front of
            Argus (which listens on 127.0.0.1:8081 only)
  argus     write /etc/argus-core/argus.env (token, first-admin seed, secret key, CA/update mounts),
            start the argus + argus-updater containers, seed Public URL + timezone via the API
  finish    scrub the one-time admin seed from argus.env, arm the :80 -> https redirect, mark done

The page is reachable by anyone on the network until setup completes, and it creates every
credential of this core, so a submission must carry a one-time SETUP CODE printed on the VM's
console (and its login banner) when the page starts serving.

The password model: one "administrator password" is used for the Debian user, the Zabbix Admin, and
the Argus admin (each individually overridable under Advanced). The database password and the Zabbix
API token are machine-generated and never displayed. Secrets needed for retries are cached root-only
under /var/lib/argus-core-setup and scrubbed once setup completes.

The first page asks what this VM should be: a new core (the form above), or a core restored from a
backup (DESIGN 14e). The restore path takes an archive, uploaded from the browser or fetched from
where the old core exported its backups, checks it before anything is changed, asks whether this is
a test restore or the new core, then runs the same steps with values of this VM's own and hands the
archive to argus-restore, the tool a person would run by hand.

Steps are idempotent; a failed run can be retried (same answers) or edited. A reboot mid-setup
resumes automatically. Once done, the service disables itself and hands port 80 to nginx (which then
just redirects to Argus on :8081). Stdlib only.
"""
import datetime
import hmac
import html
import json
import os
import re
import secrets
import shutil
import string
import subprocess
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, unquote, urlparse

STATE_DIR = "/var/lib/argus-core-setup"
CONFIG_PATH = os.path.join(STATE_DIR, "config.json")   # the submitted answers (0600, deleted at finish)
STATE_PATH = os.path.join(STATE_DIR, "state.json")     # step markers + cached secrets (0600, scrubbed at finish)
DONE_PATH = os.path.join(STATE_DIR, "setup-done")      # marker + summary for the success page

ARGUS_ENV = "/etc/argus-core/argus.env"
IMAGE_ENV = "/etc/argus-core/image.env"
SNIPPET = "/etc/argus-core/zabbix_server.conf.snippet"
SIZER = "/usr/local/sbin/argus-size-zabbix"          # sizes Zabbix + PostgreSQL to this VM (DESIGN 14b)
ZBX_CONF = "/etc/zabbix/zabbix_server.conf"
ZBX_WEB_CONF = "/etc/zabbix/web/zabbix.conf.php"
ZBX_NGINX = "/etc/zabbix/nginx.conf"
ZBX_PHP_FPM = "/etc/zabbix/php-fpm.conf"
AGENT2_DROPIN = "/etc/zabbix/zabbix_agent2.d/argus.conf"
NGINX_REDIRECT = "/etc/nginx/conf.d/argus-port80.conf"
NGINX_HTTPS = "/etc/nginx/conf.d/argus-https.conf"
WEB_CERT_DIR = "/etc/nginx/argus"                      # the VM's own https certificate for Argus
PKI_DIR = "/docker/argus/pki"                          # mounted read-only into the core as /ca
ZBX_CERTS = "/etc/zabbix/certs"
CA_CN = "Monitoring Core CA"                           # matches deploy/pki/gen-certs.sh

ZBX_API = "http://127.0.0.1:8080/api_jsonrpc.php"
ARGUS_URL = "http://127.0.0.1:8081"
ARGUS_UID = "65532"                                    # the distroless core's nonroot uid
FIRSTBOOT_SERVICE = "argus-firstboot.service"
LISTEN = ("0.0.0.0", 80)
SSHD_DROPIN = "/etc/ssh/sshd_config.d/20-argus-user.conf"
CONSOLE = "/usr/local/sbin/argus-console"              # the console menu (DESIGN 14f), from the host tools
ISSUE_FILE = "/etc/issue.d/argus-setup.issue"
# A disk labelled ARGUSSEED with an argus.env holding ARGUS_SETUP_CODE=XXXX-XXXX supplies the setup
# code when there is no console to read it from (attaching media is the same proof of hypervisor
# control). Same label and file as the probe's seed disk, matched case-insensitively (plain ISO9660
# may uppercase names and add ";1").
SEED_LABEL = "ARGUSSEED"
SEED_ENV = "argus.env"
SEED_MOUNT = "/run/argus-seed"
# The code is read off a console font and typed by hand, so no character that reads like another:
# no 0/O, 1/I/L, 2/Z, 5/S, 6/G, 8/B (a Z read as a 2 cost a first setup its first tries).
CODE_ALPHABET = "ACDEFHJKMNPQRTUVWXY3479"
CODE_MAX_FAILURES = 10

# Restoring a backup at first boot (DESIGN 14e).
BACKUP_TOOL = "/usr/local/sbin/argus-backup"
RESTORE_TOOL = "/usr/local/sbin/argus-restore"
STAGE_DIR = "/var/backups/argus/first-boot"            # the archive to restore from (root only, removed at finish)
PASS_PATH = os.path.join(STATE_DIR, "passphrase")      # its passphrase (0600, removed at finish)
TARGET_PATH = os.path.join(STATE_DIR, "target.json")   # the target it is fetched from (0600, removed after the fetch)
RESTORE_KEY = os.path.join(STATE_DIR, "restore_key")   # the SSH key pair an rsync target is asked to accept
UPDATE_DIR = "/docker/argus-update"                    # the folder shared with the Argus container
SESSION_COOKIE = "argus_setup"
UPLOAD_CHUNK = 1 << 20
# The same targets, in the same words, as Settings, Backups.
REMOTES = [("smb", "SMB share (Windows, NAS)"), ("nfs", "NFS export"), ("rsync", "rsync over SSH"),
           ("s3", "S3 bucket (or compatible)")]
# What the restore form shows again after an error: never a password, a secret key or the passphrase.
RESTORE_KEEP = ("source", "rtype", "share", "smb_path", "username", "domain", "export", "nfs_path", "options",
                "target", "port", "endpoint", "bucket", "region", "s3_path", "access_key", "keymap", "linux_user")

KEYMAPS = [("us", "US English"), ("uk", "UK English"), ("it", "Italian"), ("de", "German"),
           ("fr", "French"), ("es", "Spanish"), ("pt-latin1", "Portuguese")]
# The time zones on the page come from the VM's own zone database (zone_names), so the page offers
# what timedatectl can set. This short list is only what is left when that database cannot be read.
ZONEINFO = "/usr/share/zoneinfo"
ZONE_AREAS = ("Africa", "America", "Antarctica", "Arctic", "Asia", "Atlantic", "Australia",
              "Europe", "Indian", "Pacific")
FALLBACK_TIMEZONES = [
    "UTC",
    "Europe/Amsterdam", "Europe/Athens", "Europe/Berlin", "Europe/Brussels", "Europe/Bucharest",
    "Europe/Copenhagen", "Europe/Dublin", "Europe/Helsinki", "Europe/Lisbon", "Europe/London",
    "Europe/Madrid", "Europe/Oslo", "Europe/Paris", "Europe/Prague", "Europe/Rome",
    "Europe/Stockholm", "Europe/Vienna", "Europe/Warsaw", "Europe/Zurich",
    "America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles",
    "America/Sao_Paulo", "Asia/Dubai", "Asia/Kolkata", "Asia/Singapore", "Asia/Shanghai",
    "Asia/Tokyo", "Australia/Sydney", "Pacific/Auckland",
]

STEPS = [
    ("system", "Applying system settings"),
    ("database", "Initializing the database"),
    ("pki", "Generating the monitoring PKI"),
    ("zabbix", "Starting Zabbix"),
    ("https", "Enabling HTTPS"),
    ("accounts", "Securing Zabbix accounts"),
    ("argus", "Starting Argus"),
    ("finish", "Finishing up"),
]


class StepError(Exception):
    pass


# ---------------------------------------------------------------- small helpers

def run(args, timeout=120, check=True, input_text=None):
    """Run a command; on check failure raise StepError with stderr detail."""
    try:
        r = subprocess.run(args, capture_output=True, text=True, timeout=timeout, input=input_text)
    except subprocess.TimeoutExpired:
        raise StepError("timed out: %s" % " ".join(args))
    if check and r.returncode != 0:
        detail = (r.stderr or r.stdout or "").strip().splitlines()
        raise StepError("%s failed: %s" % (args[0], detail[-1] if detail else "rc=%d" % r.returncode))
    return r


def sh(*args):
    try:
        return subprocess.run(args, capture_output=True, text=True, timeout=15).stdout
    except Exception:
        return ""


def write_private(path, content):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    tmp = path + ".tmp"
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(content)
    os.replace(tmp, path)


def read_json(path):
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except Exception:
        return {}


def write_json(path, data):
    write_private(path, json.dumps(data, indent=1))


def gen_password(n=24):
    alphabet = string.ascii_letters + string.digits
    return "".join(secrets.choice(alphabet) for _ in range(n))


def primary_ip():
    ips = [ip for ip in sh("hostname", "-I").split() if not ip.startswith("127.")]
    return ips[0] if ips else "<this-vm>"


def gen_setup_code():
    raw = "".join(secrets.choice(CODE_ALPHABET) for _ in range(8))
    return raw[:4] + "-" + raw[4:]


def announce_code(code):
    """Show the setup code where only the console can see it: the login banner (/etc/issue.d, read
    by agetty at each prompt) and the console itself, right now."""
    text = ("\n  Argus core setup: open http://%s/ in a browser on this network\n"
            "  and enter the setup code  %s\n\n" % (primary_ip(), code))
    try:
        os.makedirs(os.path.dirname(ISSUE_FILE), exist_ok=True)
        with open(ISSUE_FILE, "w", encoding="utf-8") as fh:
            fh.write(text)
    except Exception:
        pass
    for dev in ("/dev/console", "/dev/tty1"):
        try:
            with open(dev, "w") as fh:
                fh.write(text)
        except Exception:
            pass
    print("argus-core-firstboot: setup code %s (shown on the console)" % code, flush=True)


def clear_code_banner():
    try:
        os.remove(ISSUE_FILE)
    except Exception:
        pass


def setup_code_from_disk():
    """The ARGUS_SETUP_CODE of an attached ARGUSSEED disk, normalised ("XXXX-XXXX"), or ""."""
    dev = None
    for line in sh("blkid").splitlines():
        m = re.match(r"^(\S+?):", line)
        lab = re.search(r'LABEL="([^"]*)"', line)
        if m and lab and lab.group(1).strip().upper() == SEED_LABEL:
            dev = m.group(1)
            break
    if not dev:
        return ""
    os.makedirs(SEED_MOUNT, exist_ok=True)
    try:
        if subprocess.run(["mount", "-o", "ro", dev, SEED_MOUNT], capture_output=True, timeout=15).returncode != 0:
            return ""
        for fn in os.listdir(SEED_MOUNT):
            if fn.split(";", 1)[0].lower() == SEED_ENV:
                with open(os.path.join(SEED_MOUNT, fn), encoding="utf-8") as fh:
                    for raw in fh:
                        k, _, v = raw.strip().partition("=")
                        if k.strip() == "ARGUS_SETUP_CODE":
                            c = v.strip().upper().replace("-", "")
                            if re.fullmatch(r"[A-Z0-9]{8}", c):
                                return c[:4] + "-" + c[4:]
        return ""
    except Exception:
        return ""
    finally:
        subprocess.run(["umount", SEED_MOUNT], check=False, capture_output=True)


def replace_in_file(path, pattern, repl, append_if_missing=None):
    """Regex-replace (first match) in a file; optionally append a line when nothing matched."""
    with open(path, encoding="utf-8") as fh:
        text = fh.read()
    new, n = re.subn(pattern, repl, text, count=1, flags=re.M)
    if n == 0 and append_if_missing is not None:
        new = text.rstrip("\n") + "\n" + append_if_missing + "\n"
    if new != text:
        with open(path, "w", encoding="utf-8") as fh:
            fh.write(new)


# ---------------------------------------------------------------- Zabbix + Argus API clients

def zbx_call(method, params, auth=None, timeout=25):
    body = json.dumps({"jsonrpc": "2.0", "method": method, "params": params, "id": 1}).encode("utf-8")
    headers = {"Content-Type": "application/json-rpc"}
    if auth:
        headers["Authorization"] = "Bearer " + auth
    req = urllib.request.Request(ZBX_API, data=body, headers=headers)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        out = json.loads(resp.read().decode("utf-8"))
    if "error" in out:
        e = out["error"]
        raise StepError("Zabbix %s: %s %s" % (method, e.get("message", ""), e.get("data", "")))
    return out.get("result")


def argus_login(email, password):
    body = json.dumps({"email": email, "password": password}).encode("utf-8")
    req = urllib.request.Request(ARGUS_URL + "/api/login", data=body,
                                 headers={"Content-Type": "application/json"}, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            cookies = [v.split(";", 1)[0] for k, v in resp.getheaders() if k.lower() == "set-cookie"]
    except urllib.error.HTTPError as e:
        raise StepError("Argus sign-in as %s failed (HTTP %d) - the first-admin seed did not take" % (email, e.code))
    if not cookies:
        raise StepError("Argus sign-in returned no session cookie")
    return "; ".join(cookies)


def argus_patch_settings(cookie, values):
    body = json.dumps({"values": values}).encode("utf-8")
    req = urllib.request.Request(ARGUS_URL + "/api/settings", data=body,
                                 headers={"Content-Type": "application/json", "Cookie": cookie},
                                 method="PATCH")
    try:
        urllib.request.urlopen(req, timeout=15).read()
    except urllib.error.HTTPError as e:
        raise StepError("seeding Argus settings failed (HTTP %d): %s" % (e.code, e.read().decode()[:200]))


# ---------------------------------------------------------------- the steps

def step_system(cfg, state):
    run(["hostnamectl", "set-hostname", cfg["hostname"]])
    if valid_zone(cfg["timezone"]):
        run(["timedatectl", "set-timezone", cfg["timezone"]])
    km = cfg["keymap"]
    if re.fullmatch(r"[a-z][a-z0-9-]{1,15}", km):
        with open("/etc/vconsole.conf", "w", encoding="utf-8") as fh:
            fh.write("KEYMAP=%s\n" % km)
        run(["systemctl", "restart", "systemd-vconsole-setup.service"], check=False)
    user = cfg["linux_user"]
    if subprocess.run(["id", "-u", user], capture_output=True).returncode != 0:
        run(["useradd", "-m", "-s", "/bin/bash", "-G", "sudo", user])
    # sudo is the whole privilege; a docker-group membership would be a second, unlogged root.
    run(["gpasswd", "-d", user, "docker"], check=False)
    run(["chpasswd"], input_text="%s:%s\n" % (user, cfg["linux_password"]))
    # SSH: password login for this one account (the image turns it off for everyone else and for
    # root), so the console password also works over the network when the console isn't at hand.
    os.makedirs(os.path.dirname(SSHD_DROPIN), exist_ok=True)
    with open(SSHD_DROPIN, "w", encoding="utf-8") as fh:
        fh.write("# Written by the Argus core appliance first-boot setup.\nMatch User %s\n    PasswordAuthentication yes\n" % user)
    run(["systemctl", "reload-or-restart", "ssh"], check=False)
    # The console menu (DESIGN 14f) is this account's login shell: an interactive login (the
    # hypervisor console or SSH) lands in it, as root through sudo without a second password for
    # that one command; ssh commands and scp pass through to bash. The menu does its own wiring
    # (login shell, /etc/shells, the sudoers drop-in checked with visudo).
    if os.path.exists(CONSOLE):
        run([CONSOLE, "--make-login-shell", user], check=False)


def step_database(cfg, state):
    # Size PostgreSQL's connection limit to the DEPLOYED VM, then timescaledb-tune for its RAM (the
    # image build ran on a small builder): argus-size-zabbix does both, and its boot-time unit does
    # the same at every later boot, so a grown VM re-sizes itself. A sizing failure is logged by the
    # tool and not fatal: the stock limit still works.
    if os.path.exists(SIZER):
        run([SIZER, "--apply", "--postgres", "--tune"], check=False, timeout=240)
    else:
        run(["timescaledb-tune", "--quiet", "--yes"], check=False, timeout=120)
    run(["systemctl", "restart", "postgresql"], timeout=180)
    for _ in range(60):
        if subprocess.run(["sudo", "-u", "postgres", "pg_isready", "-q"], capture_output=True).returncode == 0:
            break
        time.sleep(2)
    else:
        raise StepError("PostgreSQL did not come up")

    dbpass = state.get("dbpass")
    if not dbpass:
        dbpass = gen_password(24)
        state["dbpass"] = dbpass
        write_json(STATE_PATH, state)

    def psql_postgres(sql, db=None):
        args = ["sudo", "-u", "postgres", "psql", "-qAt"]
        if db:
            args += ["-d", db]
        return run(args + ["-c", sql], timeout=120).stdout.strip()

    if psql_postgres("SELECT 1 FROM pg_roles WHERE rolname='zabbix'") != "1":
        psql_postgres("CREATE USER zabbix WITH PASSWORD '%s'" % dbpass)
    else:  # make the stored password authoritative on retries
        psql_postgres("ALTER USER zabbix WITH PASSWORD '%s'" % dbpass)
    if psql_postgres("SELECT 1 FROM pg_database WHERE datname='zabbix'") != "1":
        run(["sudo", "-u", "postgres", "createdb", "-O", "zabbix", "zabbix"], timeout=120)
    psql_postgres("CREATE EXTENSION IF NOT EXISTS timescaledb CASCADE", db="zabbix")

    have_schema = psql_postgres(
        "SELECT 1 FROM information_schema.tables WHERE table_name='users'", db="zabbix") == "1"
    if not have_schema:
        # The big one: ~a minute or three on real hardware. ON_ERROR_STOP so a failure surfaces here.
        run(["bash", "-c",
             "set -o pipefail; zcat /usr/share/zabbix-sql-scripts/postgresql/server.sql.gz"
             " | sudo -u zabbix psql -q -v ON_ERROR_STOP=1 zabbix"], timeout=3600)

    is_hyper = psql_postgres(
        "SELECT 1 FROM timescaledb_information.hypertables WHERE hypertable_name='history'",
        db="zabbix") == "1"
    if not is_hyper:
        ts_sql = None
        for cand in ("/usr/share/zabbix-sql-scripts/postgresql/timescaledb/schema.sql",
                     "/usr/share/zabbix-sql-scripts/postgresql/timescaledb.sql"):
            if os.path.exists(cand):
                ts_sql = cand
                break
        if not ts_sql:
            raise StepError("TimescaleDB schema file not found under /usr/share/zabbix-sql-scripts")
        run(["bash", "-c",
             "set -o pipefail; cat '%s' | sudo -u zabbix psql -q -v ON_ERROR_STOP=1 zabbix" % ts_sql],
            timeout=600)


def step_pki(cfg, state):
    os.makedirs(PKI_DIR, exist_ok=True)
    os.makedirs(ZBX_CERTS, exist_ok=True)
    ca_crt, ca_key = os.path.join(PKI_DIR, "ca.crt"), os.path.join(PKI_DIR, "ca.key")
    if not os.path.exists(ca_key):
        run(["openssl", "req", "-x509", "-newkey", "rsa:4096", "-nodes",
             "-keyout", ca_key, "-out", ca_crt, "-days", "3650", "-subj", "/CN=" + CA_CN], timeout=180)
    crt = os.path.join(ZBX_CERTS, "zabbix-core.crt")
    key = os.path.join(ZBX_CERTS, "zabbix-core.key")
    csr = os.path.join(ZBX_CERTS, "zabbix-core.csr")
    if not os.path.exists(crt):
        run(["openssl", "req", "-newkey", "rsa:2048", "-nodes",
             "-keyout", key, "-out", csr, "-subj", "/CN=zabbix-core"], timeout=120)
        run(["openssl", "x509", "-req", "-in", csr, "-CA", ca_crt, "-CAkey", ca_key,
             "-CAcreateserial", "-out", crt, "-days", "1825", "-sha256"], timeout=120)
        os.unlink(csr)
    run(["bash", "-c", "cp -f '%s' '%s/ca.crt'" % (ca_crt, ZBX_CERTS)])
    # zabbix-server reads its copies; the Argus container (uid 65532) reads the CA to sign enrollments.
    # The CA is root's and only readable by the container's group, so Argus can't change it.
    run(["bash", "-c",
         "chown -R zabbix:zabbix '%s' && chmod 644 '%s'/*.crt && chmod 600 '%s'/zabbix-core.key"
         % (ZBX_CERTS, ZBX_CERTS, ZBX_CERTS)])
    run(["bash", "-c",
         "chown -R root:%s '%s' && chmod 750 '%s' && chmod 440 '%s/ca.key' && chmod 444 '%s/ca.crt'"
         % (ARGUS_UID, PKI_DIR, PKI_DIR, PKI_DIR, PKI_DIR)])


def php_fpm_unit():
    out = sh("bash", "-c", "ls /lib/systemd/system/php*-fpm.service 2>/dev/null | head -1").strip()
    return os.path.basename(out) if out else "php8.4-fpm.service"


def step_zabbix(cfg, state):
    dbpass = state.get("dbpass") or ""
    if not dbpass:
        raise StepError("no database password in the setup state (database step incomplete?)")

    # zabbix_server.conf: exactly one live line each for DBName/DBUser/DBPassword, then the TLS +
    # tuning snippet (appended once - it carries the cert paths written by the pki step).
    replace_in_file(ZBX_CONF, r"^DBName=.*$", "DBName=zabbix", append_if_missing="DBName=zabbix")
    replace_in_file(ZBX_CONF, r"^DBUser=.*$", "DBUser=zabbix", append_if_missing="DBUser=zabbix")
    replace_in_file(ZBX_CONF, r"^DBPassword=.*$", "DBPassword=" + dbpass,
                    append_if_missing="DBPassword=" + dbpass)
    # Append the TLS + tuning snippet once. Guard on OUR marker line, NOT on "TLSCAFile=": the stock
    # zabbix_server.conf ships commented "# TLSCAFile=" example lines, so a substring test wrongly
    # concluded TLS was already set and skipped the append - leaving the server with no active TLS, so
    # proxies fail the cert handshake (SSL alert 40). The uncommented lines we append win over the
    # stock commented examples (same as the manual install path's unconditional append).
    snippet_marker = "# --- appended by the Argus core appliance first-boot setup ---"
    with open(ZBX_CONF, encoding="utf-8") as fh:
        conf = fh.read()
    if snippet_marker not in conf and os.path.exists(SNIPPET):
        with open(SNIPPET, encoding="utf-8") as fh:
            snippet = fh.read()
        with open(ZBX_CONF, "a", encoding="utf-8") as fh:
            fh.write("\n" + snippet_marker + "\n" + snippet)

    # The server's caches and process counts for this VM's RAM and cores (argus-size-zabbix, DESIGN
    # 14b): one marked block after the snippet, so its values win; a re-run replaces it. After
    # growing the VM: `argus-size-zabbix --apply`, then restart what it names.
    if os.path.exists(SIZER):
        run([SIZER, "--apply", "--zabbix"], check=False, timeout=60)

    # Frontend config - what the browser setup wizard would have written.
    php_pass = dbpass.replace("\\", "\\\\").replace("'", "\\'")
    write_private(ZBX_WEB_CONF, """<?php
// Written by the Argus core appliance first-boot setup (argus-core-firstboot.py).
$DB['TYPE']            = 'POSTGRESQL';
$DB['SERVER']          = 'localhost';
$DB['PORT']            = '0';
$DB['DATABASE']        = 'zabbix';
$DB['USER']            = 'zabbix';
$DB['PASSWORD']        = '%s';
$DB['SCHEMA']          = '';
$DB['ENCRYPTION']      = false;
$DB['KEY_FILE']        = '';
$DB['CERT_FILE']       = '';
$DB['CA_FILE']         = '';
$DB['VERIFY_HOST']     = false;
$DB['CIPHER_LIST']     = '';
$DB['VAULT_URL']       = '';
$DB['VAULT_DB_PATH']   = '';
$DB['VAULT_TOKEN']     = '';
$DB['DOUBLE_IEEE754']  = true;
$ZBX_SERVER_NAME       = 'Argus core';
$IMAGE_FORMAT_DEFAULT  = IMAGE_FORMAT_PNG;
""" % php_pass)
    run(["bash", "-c", "chown www-data:www-data '%s' && chmod 640 '%s'" % (ZBX_WEB_CONF, ZBX_WEB_CONF)],
        check=False)

    # nginx: uncomment the zabbix vhost's listen/server_name (the Debian default :80 site was removed
    # at image build; :80 stays free for this setup page, later the finish step parks a redirect there).
    replace_in_file(ZBX_NGINX, r"^#(\s*listen\s+8080;)", r"\1")
    replace_in_file(ZBX_NGINX, r"^#?(\s*server_name)\s+.*;$", r"\1     _;")

    # php-fpm: the zabbix pool wants an explicit date.timezone.
    if os.path.exists(ZBX_PHP_FPM):
        replace_in_file(ZBX_PHP_FPM, r"^;?\s*php_value\[date\.timezone\].*$",
                        "php_value[date.timezone] = " + cfg["timezone"],
                        append_if_missing="php_value[date.timezone] = " + cfg["timezone"])

    # agent2 self-monitoring: the stock "Zabbix server" host expects this agent identity.
    os.makedirs(os.path.dirname(AGENT2_DROPIN), exist_ok=True)
    with open(AGENT2_DROPIN, "w", encoding="utf-8") as fh:
        fh.write("# Written by the Argus core appliance first-boot setup.\nHostname=Zabbix server\n")

    php = php_fpm_unit()
    run(["systemctl", "enable", "zabbix-server", "zabbix-agent2", "nginx", php], timeout=60)
    for svc in ("zabbix-server", "zabbix-agent2", php, "nginx"):
        run(["systemctl", "restart", svc], timeout=180)


def step_https(cfg, state):
    """Argus over https on :443. nginx terminates TLS with a certificate for this VM (its hostname
    and address) signed by the monitoring CA, so installing ca.crt on a PC makes the warning go
    away, and proxies to the core on 127.0.0.1:8081 with the forwarded headers Argus reads from a
    trusted proxy (seeded as 127.0.0.1 in the argus step)."""
    os.makedirs(WEB_CERT_DIR, exist_ok=True)
    os.chmod(WEB_CERT_DIR, 0o700)
    ca_crt, ca_key = os.path.join(PKI_DIR, "ca.crt"), os.path.join(PKI_DIR, "ca.key")
    crt, key = os.path.join(WEB_CERT_DIR, "argus-web.crt"), os.path.join(WEB_CERT_DIR, "argus-web.key")
    ip = primary_ip()
    if not os.path.exists(crt):
        csr, ext = crt + ".csr", crt + ".ext"
        run(["openssl", "req", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", csr,
             "-subj", "/CN=" + cfg["hostname"]], timeout=120)
        san = "DNS:" + cfg["hostname"]
        if re.fullmatch(r"[0-9.]+|[0-9a-fA-F:]+", ip):
            san += ",IP:" + ip
        with open(ext, "w", encoding="utf-8") as fh:
            fh.write("subjectAltName=%s\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n" % san)
        run(["openssl", "x509", "-req", "-in", csr, "-CA", ca_crt, "-CAkey", ca_key, "-CAcreateserial",
             "-out", crt, "-days", "730", "-sha256", "-extfile", ext], timeout=120)
        os.unlink(csr)
        os.unlink(ext)
        os.chmod(key, 0o600)
    # HSTS: once a browser trusts the certificate it refuses a plain-http downgrade of the login
    # page from then on (a replacement certificate must therefore be trusted before this one ends;
    # the README says so). The Zabbix frontend and its API get the same certificate on :8443, so
    # an Argus running on another host can use https://<vm>:8443/api_jsonrpc.php and its
    # super-admin token never crosses the LAN in clear (:8080 stays plain for the VM itself).
    with open(NGINX_HTTPS, "w", encoding="utf-8") as fh:
        fh.write("# Written by the Argus core appliance first-boot setup: Argus over https, and the\n"
                 "# Zabbix frontend/API over https on :8443 for an Argus on another host.\n"
                 "server {\n"
                 "    listen 443 ssl default_server;\n"
                 "    server_name _;\n"
                 "    ssl_certificate %s;\n"
                 "    ssl_certificate_key %s;\n"
                 "    ssl_protocols TLSv1.2 TLSv1.3;\n"
                 "    ssl_prefer_server_ciphers off;\n"
                 "    add_header Strict-Transport-Security \"max-age=15552000\" always;\n"
                 "    client_max_body_size 8m;\n"
                 "    location / {\n"
                 "        proxy_pass http://127.0.0.1:8081;\n"
                 "        proxy_http_version 1.1;\n"
                 "        proxy_set_header Host $host;\n"
                 "        proxy_set_header X-Forwarded-For $remote_addr;\n"
                 "        proxy_set_header X-Forwarded-Proto https;\n"
                 "        proxy_set_header X-Forwarded-Host $host;\n"
                 "        proxy_read_timeout 90s;\n"
                 "    }\n"
                 "}\n"
                 "server {\n"
                 "    listen 8443 ssl;\n"
                 "    server_name _;\n"
                 "    ssl_certificate %s;\n"
                 "    ssl_certificate_key %s;\n"
                 "    ssl_protocols TLSv1.2 TLSv1.3;\n"
                 "    ssl_prefer_server_ciphers off;\n"
                 "    client_max_body_size 16m;\n"
                 "    location / {\n"
                 "        proxy_pass http://127.0.0.1:8080;\n"
                 "        proxy_http_version 1.1;\n"
                 "        proxy_set_header Host $host;\n"
                 "        proxy_set_header X-Forwarded-For $remote_addr;\n"
                 "        proxy_set_header X-Forwarded-Proto https;\n"
                 "        proxy_read_timeout 300s;\n"
                 "    }\n"
                 "}\n" % (crt, key, crt, key))
    run(["nginx", "-t"], timeout=30)
    run(["systemctl", "reload-or-restart", "nginx"], timeout=60)


def step_accounts(cfg, state):
    # Wait for the frontend API (php-fpm warm-up + first zabbix-server start).
    deadline = time.time() + 300
    last = "no response yet"
    while time.time() < deadline:
        try:
            zbx_call("apiinfo.version", {})
            break
        except Exception as e:  # noqa: BLE001 - keep probing until the deadline
            last = str(e)
            time.sleep(3)
    else:
        raise StepError("the Zabbix API did not come up: " + last)

    new_pw = cfg["zabbix_password"]
    # Sign in as Admin: try the chosen password first (a prior attempt may already have rotated it),
    # else the Zabbix default. Whichever works tells us whether rotation still needs doing.
    rotated = False
    try:
        auth = zbx_call("user.login", {"username": "Admin", "password": new_pw})
        rotated = True
    except StepError:
        try:
            auth = zbx_call("user.login", {"username": "Admin", "password": "zabbix"})
        except StepError:
            raise StepError("could not sign in to Zabbix as Admin with either the chosen or the "
                            "default password - was the password changed manually?")

    # The machine account Argus talks through: a dedicated super admin, so rotating or disabling the
    # human Admin later never breaks Argus. Its password is random and never used interactively.
    # Create it (and the API token) BEFORE rotating Admin - rotating a user's own password can end the
    # current API session, so anything that must use `auth` runs first.
    svc = zbx_call("user.get", {"filter": {"username": ["argus-svc"]}, "output": ["userid"]}, auth)
    if svc:
        svc_id = svc[0]["userid"]
    else:
        roles = zbx_call("role.get", {"filter": {"name": ["Super admin role"]}, "output": ["roleid"]}, auth)
        groups = zbx_call("usergroup.get", {"filter": {"name": ["Zabbix administrators"]},
                                            "output": ["usrgrpid"]}, auth)
        if not roles or not groups:
            raise StepError("stock 'Super admin role' / 'Zabbix administrators' not found")
        svc_id = zbx_call("user.create", {
            "username": "argus-svc", "name": "Argus", "surname": "service",
            "passwd": gen_password(24), "roleid": roles[0]["roleid"],
            "usrgrps": [{"usrgrpid": groups[0]["usrgrpid"]}],
        }, auth)["userids"][0]

    if not state.get("zbx_token"):
        stale = zbx_call("token.get", {"filter": {"name": ["argus-core"]}, "output": ["tokenid"]}, auth)
        if stale:
            zbx_call("token.delete", [t["tokenid"] for t in stale], auth)
        tid = zbx_call("token.create", {"name": "argus-core", "userid": svc_id,
                                        "description": "Argus core (created by first-boot setup)",
                                        "expires_at": 0}, auth)["tokenids"][0]
        state["zbx_token"] = zbx_call("token.generate", [tid], auth)[0]["token"]
        write_json(STATE_PATH, state)

    # Housekeeping retention to match the Argus UI's time tabs. Compression is best-effort (a DB that
    # isn't TimescaleDB-managed rejects it) - retention still applies, so don't fail the whole setup.
    zbx_call("housekeeping.update", {
        "hk_history_global": 1, "hk_history": "30d",
        "hk_trends_global": 1, "hk_trends": "730d",
    }, auth)
    try:
        zbx_call("housekeeping.update", {"compression_status": 1, "compress_older": "7d"}, auth)
    except StepError as e:
        print("argus-core-firstboot: compression not enabled (%s) - retention still set" % e, flush=True)

    # Rotate the Admin password LAST. Zabbix 7.0 requires current_passwd when a user changes their OWN
    # password (we're logged in AS Admin); we only reach the rotation path after authenticating with
    # the default "zabbix", so that is the current password.
    if not rotated:
        admins = zbx_call("user.get", {"filter": {"username": ["Admin"]}, "output": ["userid"]}, auth)
        if not admins:
            raise StepError("stock Zabbix Admin user not found")
        zbx_call("user.update", {"userid": admins[0]["userid"], "passwd": new_pw,
                                 "current_passwd": "zabbix"}, auth)

    try:
        zbx_call("user.logout", [], auth)
    except StepError:
        pass


def step_argus(cfg, state):
    if not state.get("secret_key"):
        state["secret_key"] = secrets.token_hex(32)
        write_json(STATE_PATH, state)
    token = state.get("zbx_token") or ""
    if not token:
        raise StepError("no Zabbix API token in the setup state (accounts step incomplete?)")

    write_private(ARGUS_ENV, "\n".join([
        "# /etc/argus-core/argus.env - written by the appliance first-boot setup. Root-only: it holds",
        "# the Zabbix API token and the at-rest encryption key. Image/tag pins live in image.env.",
        "ARGUS_ZABBIX_API_URL=http://host.docker.internal:8080/api_jsonrpc.php",
        "ARGUS_ZABBIX_API_TOKEN=" + token,
        "ARGUS_ADMIN_EMAIL=" + cfg["email"],
        "ARGUS_ADMIN_PASSWORD=" + cfg["argus_password"],
        "ARGUS_SECRET_KEY=" + state["secret_key"],
        "ARGUS_UPDATE_DIR=/update",
        "ARGUS_CA_CERT_FILE=/ca/ca.crt",
        "ARGUS_CA_KEY_FILE=/ca/ca.key",
    ]) + "\n")

    if not os.path.exists(IMAGE_ENV):
        write_private(IMAGE_ENV, "\n".join([
            "# /etc/argus-core/image.env - image/tag pins for the two container units (systemd reads",
            "# this file; keep it simple KEY=VALUE). Uncomment to pin instead of tracking latest;",
            "# `systemctl restart argus-core argus-updater` applies a change.",
            "#ARGUS_CORE_TAG=latest",
            "#ARGUS_CORE_IMAGE=ghcr.io/g-guglielmi/argus",
            "#ARGUS_UPDATER_TAG=latest",
            "#ARGUS_UPDATER_IMAGE=ghcr.io/g-guglielmi/argus-updater",
        ]) + "\n")

    run(["systemctl", "enable", "argus-core", "argus-updater"], timeout=60)
    for svc in ("argus-core", "argus-updater"):
        run(["systemctl", "restart", svc], timeout=300)

    deadline = time.time() + 240
    last = "no response yet"
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(ARGUS_URL + "/healthz", timeout=5) as resp:
                if resp.status == 200:
                    break
        except Exception as e:  # noqa: BLE001
            last = str(e)
        time.sleep(3)
    else:
        raise StepError("Argus did not come up on :8081: " + last)

    # Seed the UI-editable settings through the API (not env) so they stay changeable in Settings.
    cookie = argus_login(cfg["email"], cfg["argus_password"])
    # nginx on this VM is the proxy in front of Argus: its forwarded headers (client address, https)
    # are believed from 127.0.0.1 only. Editable in Settings, like the Public URL.
    values = {"timezone": cfg["timezone"], "trust_proxy": "127.0.0.1"}
    if cfg.get("public_url"):
        values["public_url"] = cfg["public_url"]
    argus_patch_settings(cookie, values)


def step_finish(cfg, state):
    if cfg.get("mode") == "restore":
        # The env file is the old core's now (from its archive or its container) and Argus already
        # runs on it: no seed to scrub, no restart.
        finish_page(cfg, state)
        return
    # The first admin exists (the argus step signed in with it), so the one-time seed can go.
    try:
        with open(ARGUS_ENV, encoding="utf-8") as fh:
            env = fh.read()
        env = re.sub(r"^ARGUS_ADMIN_PASSWORD=.*$",
                     "# ARGUS_ADMIN_PASSWORD was used once to seed the first admin, then removed.",
                     env, flags=re.M)
        write_private(ARGUS_ENV, env)
    except FileNotFoundError:
        pass
    # The running container still carries the password in its environment (docker inspect shows
    # it, and the updater clones that environment on every recreate): recreate it from the
    # scrubbed file now, so the human's password lives on only as a hash in Argus's database.
    run(["systemctl", "restart", "argus-core"], timeout=300, check=False)
    finish_page(cfg, state)


def finish_page(cfg, state):
    """The end of a run, a new core's or a restore's: http://<vm>/ goes to Argus from now on, the
    setup is marked done and what it no longer needs is removed."""
    restore = cfg.get("mode") == "restore"
    # Park a redirect on :80 so http://<vm>/ lands on Argus (over https) from now on: the Public URL
    # when it is https, else this VM's own address (never the request's Host header, which a client
    # chooses). Written here, but nginx only picks it up when this service exits and releases the port
    # (main() reloads nginx after shutdown).
    target = "https://$server_addr$request_uri"
    pu = (cfg.get("public_url") or "").rstrip("/")
    if pu.startswith("https://"):
        target = pu + "$request_uri"
    with open(NGINX_REDIRECT, "w", encoding="utf-8") as fh:
        fh.write("# Written by the Argus core appliance first-boot setup: the setup page is gone;\n"
                 "# send http://<vm>/ visitors to Argus over https.\n"
                 "server {\n    listen 80 default_server;\n    server_name _;\n"
                 "    return 301 %s;\n}\n" % target)

    done = {"email": cfg["email"], "hostname": cfg["hostname"], "done_at": int(time.time())}
    if restore:
        done["restored"] = dict(cfg.get("restored") or {}, test=bool(cfg.get("test")))
        clear_restore_leftovers()
    write_json(DONE_PATH, done)
    try:
        os.unlink(CONFIG_PATH)  # the submitted answers (passwords) are no longer needed
    except FileNotFoundError:
        pass
    for k in ("dbpass", "zbx_token", "secret_key"):  # live copies now sit in root-only config files
        state.pop(k, None)
    write_json(STATE_PATH, state)


# ---------------------------------------------------------------- restoring a backup (DESIGN 14e)
# "Restore from a backup" on the first page: this VM is set up the usual way, with values of its own
# that the restore then replaces, and argus-restore (the same tool a person runs by hand) puts the
# old core's databases, settings, keys and certificates on it. As a test restore the copy sends
# nothing and never takes the old core's address; as the new core it takes its place.

def steps_for(cfg):
    """The steps of this run: a new core's, or with the restore after them."""
    if (cfg or {}).get("mode") != "restore":
        return STEPS
    steps = STEPS[:-1] + [("tools", "Bringing the restore tool up to date"), ("restore", "Restoring the backup")]
    if cfg.get("take_address"):
        steps.append(("address", "Taking over the old core's address"))
    return steps + [STEPS[-1]]


def run_tool(args, on_line=None, timeout=None):
    """Run one of the host tools; returns (exit code, its output lines). on_line sees each line as it
    comes, which is how the page follows a restore that takes minutes."""
    try:
        p = subprocess.Popen(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL,
                             text=True, errors="replace")
    except OSError as e:
        return 127, ["%s could not be started: %s" % (args[0], e.strerror or e)]
    killer = None
    if timeout:
        killer = threading.Timer(timeout, p.kill)
        killer.daemon = True
        killer.start()
    lines = []
    for line in p.stdout:
        line = line.rstrip("\n")
        lines.append(line)
        if on_line:
            on_line(line)
    rc = p.wait()
    if killer:
        killer.cancel()
    return rc, lines


def tool_error(lines):
    """What a host tool said went wrong: its own "argus-...: reason" line when there is one, else its
    last line. gpg's and apt's chatter above it is not the reason."""
    said = [l.strip() for l in lines if l.strip()]
    for line in reversed(said):
        for prefix in ("argus-restore: ", "argus-backup: "):
            if line.startswith(prefix):
                return line[len(prefix):]
    return said[-1] if said else ""


def step_tools(cfg, state):
    """The restore tool on this VM is as old as its image, and the archive may come from a newer Argus.
    The Argus image that just started carries the current host tools: they are installed the way the
    updater does it after every update (the image run once, as root, without a network, the host's
    two folders bound in). A failure leaves the tools the image shipped, which is no reason to stop."""
    img = sh("docker", "inspect", "-f", "{{.Config.Image}}", "argus").strip()
    if not img:
        return
    label = sh("docker", "image", "inspect", "-f", '{{index .Config.Labels "io.argus.host-tools"}}', img).strip()
    if not label or label == "<no value>":
        return
    r = run(["docker", "run", "--rm", "--network", "none", "--user", "0:0", "--read-only", "--cap-drop", "ALL",
             "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--cap-add", "CHOWN",
             "--mount", "type=bind,src=/usr/local/sbin,dst=/host/usr/local/sbin",
             "--mount", "type=bind,src=/etc/systemd/system,dst=/host/etc/systemd/system",
             "--mount", "type=bind,src=%s,dst=/update" % UPDATE_DIR,
             "--entrypoint", "/argus", img, "install-host-tools", "--root", "/host", "--state-dir", UPDATE_DIR,
             "--update-dir", "/update"], timeout=300, check=False)
    if r.returncode != 0:
        print("argus-core-firstboot: the host tools stay as the image shipped them: %s"
              % ((r.stderr or r.stdout or "").strip().splitlines() or ["no reason given"])[-1], flush=True)
    run(["systemctl", "daemon-reload"], check=False)


def restore_command(cfg, action):
    cmd = [RESTORE_TOOL, action, cfg["archive"], "--yes"]
    if os.path.exists(PASS_PATH):
        cmd += ["--passphrase-file", PASS_PATH]
    return cmd


def step_restore(cfg, state):
    if not os.path.isfile(cfg.get("archive") or ""):
        raise StepError("the archive is no longer on this VM: reboot it and start the restore again")
    cmd = restore_command(cfg, "restore") + ["--first-boot"]
    if cfg.get("test"):
        cmd.append("--test")

    def seen(line):
        print("argus-restore | " + line, flush=True)
        if line.startswith("==> "):
            ORCH.say(line[4:])
    rc, lines = run_tool(cmd, on_line=seen)
    ORCH.say("")
    if rc != 0:
        raise StepError(tool_error(lines) or "argus-restore ended with %d" % rc)


def step_address(cfg, state):
    """The new core answers where the old one did: the probes dial that address. The page goes with
    it, so the browser is told where to carry on before the address changes."""
    rc, lines = run_tool(restore_command(cfg, "network"), on_line=lambda l: print("argus-restore | " + l, flush=True))
    if rc != 0:
        raise StepError(tool_error(lines) or "argus-restore network ended with %d" % rc)


def clear_restore_leftovers():
    """The archive, its passphrase, the target's secrets and the key made for it: gone once they are
    not needed, they hold every secret of the core."""
    for path in (PASS_PATH, TARGET_PATH, RESTORE_KEY, RESTORE_KEY + ".pub"):
        try:
            os.unlink(path)
        except OSError:
            pass
    try:
        for name in os.listdir(STAGE_DIR):
            os.unlink(os.path.join(STAGE_DIR, name))
        os.rmdir(STAGE_DIR)
    except OSError:
        pass


STEP_FUNCS = {"system": step_system, "database": step_database, "pki": step_pki,
              "zabbix": step_zabbix, "https": step_https, "accounts": step_accounts,
              "argus": step_argus, "tools": step_tools, "restore": step_restore,
              "address": step_address, "finish": step_finish}


# ---------------------------------------------------------------- orchestrator

class Orchestrator:
    def __init__(self):
        self.lock = threading.Lock()
        self.thread = None
        self.steps = list(STEPS)
        self.state_of = {k: "pending" for k, _ in self.steps}
        self.detail = ""
        self.note = ""       # what the running step is doing right now, in the tool's own words
        self.overall = "idle"  # idle | running | failed | done

    def snapshot(self):
        with self.lock:
            return {
                "state": self.overall,
                "detail": self.detail,
                "note": self.note,
                "steps": [{"key": k, "label": lbl, "state": self.state_of.get(k, "pending")} for k, lbl in self.steps],
                "argus_url": "https://%s/" % primary_ip(),
            }

    def running(self):
        with self.lock:
            return self.overall == "running"

    def say(self, note):
        with self.lock:
            self.note = note

    def start(self, fresh=False):
        with self.lock:
            if self.overall == "running":
                return
            self.overall = "running"
            self.detail = ""
            self.note = ""
        if fresh:
            st = read_json(STATE_PATH)
            st["steps_done"] = []
            write_json(STATE_PATH, st)
        # The steps of this run are known before the first page asks for them: a restore has more.
        steps = steps_for(read_json(CONFIG_PATH))
        done = set(read_json(STATE_PATH).get("steps_done") or [])
        with self.lock:
            self.steps = list(steps)
            self.state_of = {k: ("done" if k in done else "pending") for k, _ in steps}
        self.thread = threading.Thread(target=self._run, daemon=True)
        self.thread.start()

    def _run(self):
        cfg = read_json(CONFIG_PATH)
        state = read_json(STATE_PATH)
        steps = steps_for(cfg)
        keys = [k for k, _ in steps]
        done = set(state.get("steps_done") or [])
        with self.lock:
            self.steps = list(steps)
            self.state_of = {k: ("done" if k in done else "pending") for k in keys}
        for key, label in steps:
            if key in done:
                continue
            with self.lock:
                self.state_of[key] = "active"
            print("argus-core-firstboot: step %s (%s)" % (key, label), flush=True)
            try:
                STEP_FUNCS[key](cfg, state)
            except StepError as e:
                print("argus-core-firstboot: step %s FAILED: %s" % (key, e), flush=True)
                with self.lock:
                    self.state_of[key] = "fail"
                    self.detail = str(e)
                    self.overall = "failed"
                return
            except Exception as e:  # noqa: BLE001 - surface anything unexpected on the page
                print("argus-core-firstboot: step %s CRASHED: %r" % (key, e), flush=True)
                with self.lock:
                    self.state_of[key] = "fail"
                    self.detail = "unexpected error in %s: %s" % (key, e)
                    self.overall = "failed"
                return
            state = read_json(STATE_PATH)  # steps may persist secrets
            done.add(key)
            state["steps_done"] = [k for k in keys if k in done]
            write_json(STATE_PATH, state)
            with self.lock:
                self.state_of[key] = "done"
                self.note = ""
        with self.lock:
            self.overall = "done"
        print("argus-core-firstboot: setup complete", flush=True)


ORCH = Orchestrator()


def inspect_archive(archive):
    """What argus-restore inspect says of an archive: (its one JSON object, "") when the archive opens
    and every part matches its checksum, else (None, why not). Nothing on the machine is changed."""
    cmd = [RESTORE_TOOL, "inspect", archive, "--json"]
    if os.path.exists(PASS_PATH):
        cmd += ["--passphrase-file", PASS_PATH]
    try:
        r = subprocess.run(cmd, capture_output=True, text=True, errors="replace", stdin=subprocess.DEVNULL, timeout=3600)
    except (OSError, subprocess.TimeoutExpired) as e:
        return None, "The archive could not be checked: %s" % e
    if r.returncode == 0:
        try:
            info = json.loads(r.stdout.strip().splitlines()[-1])
            if isinstance(info, dict):
                return info, ""
        except (ValueError, IndexError):
            pass
    said = tool_error((r.stdout + "\n" + r.stderr).splitlines()) or "argus-restore said nothing"
    return None, "The archive could not be opened: " + said


class Prepare:
    """Getting the archive onto this VM and looking into it, before anything is changed: fetch it when
    it is at a target, then argus-restore inspect. The page waits on this and then asks its last
    question (a test restore, or the new core)."""

    def __init__(self):
        self.lock = threading.Lock()
        self.state = "idle"  # idle | fetching | checking | ready | failed
        self.detail = ""
        self.info = {}
        self.cfg = {}

    def snapshot(self):
        with self.lock:
            return {"state": self.state, "detail": self.detail}

    def reset(self):
        with self.lock:
            if self.state in ("fetching", "checking"):
                return False
            self.state, self.detail, self.info, self.cfg = "idle", "", {}, {}
        return True

    def start(self, cfg, fetch_name=""):
        with self.lock:
            if self.state in ("fetching", "checking"):
                return False
            self.state = "fetching" if fetch_name else "checking"
            self.detail, self.info, self.cfg = "", {}, dict(cfg)
        threading.Thread(target=self._run, args=(fetch_name,), daemon=True).start()
        return True

    def _fail(self, why):
        with self.lock:
            self.state, self.detail = "failed", why

    def _run(self, fetch_name):
        archive = self.cfg["archive"]
        if fetch_name:
            rc, lines = run_tool([BACKUP_TOOL, "remote-get", "--from", TARGET_PATH, fetch_name, archive])
            try:
                os.unlink(TARGET_PATH)  # the target's password or key is not needed again
            except OSError:
                pass
            if rc != 0:
                self._fail("The archive could not be fetched: " + (tool_error(lines) or "argus-backup ended with %d" % rc))
                return
            with self.lock:
                self.state = "checking"
        info, why = inspect_archive(archive)
        if info is None:
            self._fail(why)
            return
        with self.lock:
            self.state, self.info = "ready", info


PREP = Prepare()


# ---------------------------------------------------------------- pages

STYLE = """
  :root { color-scheme: light dark;
    --bg: #eaeef4; --card: #ffffff; --border: #dbe2ec; --text: #141d28; --muted: #4f5b69; --faint: #647082;
    --field: #f4f7fb; --accent: #2ea8c9; --ok: #3fa66a; --err: #e2564d; --shadow: 0 1px 2px rgba(20,30,45,.06), 0 6px 20px rgba(20,30,45,.06); }
  @media (prefers-color-scheme: dark) { :root {
    --bg: #0e1218; --card: #151b23; --border: #262f3b; --text: #eef2f8; --muted: #b3bfcd; --faint: #8b98a8;
    --field: #0e1218; --shadow: 0 12px 40px rgba(0,0,0,.4); } }
  * { box-sizing: border-box; }
  body { margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 1rem;
    background: var(--bg); color: var(--text); font: 15px/1.5 system-ui, -apple-system, Segoe UI, Roboto, sans-serif; }
  .card { width: min(36rem, 100%); background: var(--card); border: 1px solid var(--border); border-radius: 14px;
    padding: 1.6rem 1.6rem 1.4rem; box-shadow: var(--shadow); margin: 1rem 0; }
  .brand { display: flex; align-items: center; gap: .6rem; font-weight: 700; letter-spacing: .12em; text-transform: uppercase;
    font-size: .95rem; margin-bottom: 1.1rem; }
  .brand svg { width: 26px; height: 26px; color: var(--accent); flex: none; }
  .brand small { display: block; font-size: .62rem; letter-spacing: .14em; color: var(--faint); font-weight: 600; margin-top: 1px; }
  h1 { font-size: 1.15rem; margin: 0 0 .4rem; }
  h2 { font-size: .8rem; letter-spacing: .08em; text-transform: uppercase; color: var(--faint); margin: 1.4rem 0 .2rem;
    padding-top: .9rem; border-top: 1px solid var(--border); }
  p.hint { color: var(--muted); font-size: .88rem; margin: 0 0 .6rem; }
  label { display: block; font-weight: 600; font-size: .82rem; margin: 1rem 0 .3rem; color: var(--muted); }
  input, select { width: 100%; padding: .6rem .7rem; font-size: 1rem; color: var(--text); background: var(--field);
    border: 1px solid var(--border); border-radius: 8px; }
  input:focus, select:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px rgba(46,168,201,.2); }
  .sub { color: var(--faint); font-size: .78rem; margin-top: .3rem; }
  [hidden] { display: none !important; }
  textarea { width: 100%; padding: .6rem .7rem; font-size: .78rem; color: var(--text); background: var(--field);
    border: 1px solid var(--border); border-radius: 8px; resize: vertical; font-family: ui-monospace, Menlo, Consolas, monospace; }
  .back { display: inline-block; margin: 0 0 .9rem; font-size: .82rem; color: var(--accent); text-decoration: none; }
  .way { display: flex; align-items: center; gap: .9rem; padding: 1rem 0; border-top: 1px solid var(--border); text-decoration: none; color: inherit; }
  .way:last-of-type { border-bottom: 1px solid var(--border); }
  .way b { display: block; font-size: 1rem; } .way span { display: block; color: var(--muted); font-size: .85rem; margin-top: .15rem; }
  .way i { margin-left: auto; color: var(--faint); font-style: normal; font-size: 1.3rem; }
  .opt { display: flex; gap: .7rem; align-items: flex-start; padding: .8rem 0; border-top: 1px solid var(--border); margin: 0; font-weight: 400; color: inherit; }
  .opt.first { border-top: none; }
  .opt input, .arch input, .check input { width: auto; accent-color: var(--accent); flex: none; }
  .opt input { margin: .25rem 0 0; } .arch input { margin: 0; } .check input { margin: .2rem 0 0; }
  .opt b { display: block; font-size: .92rem; color: var(--text); } .opt span { display: block; color: var(--faint); font-size: .8rem; margin-top: .15rem; }
  .in { margin: 0 0 .4rem 1.75rem; }
  .file { display: flex; align-items: center; gap: .7rem; margin-top: .5rem; }
  .file .pickbtn { margin: 0; padding: .45rem .8rem; border: 1px solid var(--accent); border-radius: 8px; color: var(--accent); font-size: .85rem; font-weight: 600; white-space: nowrap; cursor: pointer; }
  .file .name { color: var(--text); font-size: .88rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bar { height: 6px; border-radius: 3px; background: var(--field); border: 1px solid var(--border); margin-top: .6rem; overflow: hidden; }
  .bar div { height: 100%; width: 0; background: var(--accent); }
  .found { margin-top: .9rem; font-size: .8rem; color: var(--muted); font-weight: 600; }
  .arch { display: flex; gap: .7rem; align-items: center; padding: .55rem 0; border-top: 1px solid var(--border); margin: 0; font-weight: 400; font-size: .88rem; color: var(--text); }
  .arch .when { flex: 1; } .arch .size { color: var(--faint); font-variant-numeric: tabular-nums; }
  .facts { display: grid; grid-template-columns: 9.5rem 1fr; gap: .45rem .9rem; margin: .9rem 0 0; font-size: .88rem; }
  .facts dt { color: var(--faint); } .facts dd { margin: 0; color: var(--text); }
  .facts .note { color: var(--muted); display: block; font-size: .8rem; }
  .check { display: flex; gap: .55rem; align-items: flex-start; margin: .6rem 0 0 1.75rem; font-weight: 400; font-size: .84rem; color: var(--muted); }
  .why { margin: .9rem 0 0; padding-left: 1.1rem; color: var(--err); font-size: .88rem; }
  button.small { width: auto; margin-top: .7rem; padding: .5rem .9rem; font-size: .85rem; }
  button:disabled { opacity: .55; cursor: default; }
  .steps li { align-items: flex-start; } .steps small { display: block; color: var(--muted); font-size: .8rem; }
  .steps small:empty { display: none; }
  .testnote { margin-top: 1rem; padding: .7rem .9rem; border-left: 3px solid #d9a441; color: var(--muted); font-size: .85rem; }
  select.list { height: auto; padding: .3rem; margin-top: .4rem; }
  select.list option { padding: .2rem .5rem; border-radius: 5px; }
  select.list optgroup { color: var(--faint); font-size: .78rem; } select.list optgroup option { color: var(--text); font-size: .95rem; }
  .sub.pick { color: var(--text); font-size: .85rem; font-weight: 600; } .sub.pick:empty { display: none; }
  .row { display: flex; gap: .8rem; } .row > div { flex: 1; }
  details { margin-top: 1.2rem; } details summary { cursor: pointer; color: var(--muted); font-size: .85rem; font-weight: 600; }
  button { margin-top: 1.5rem; width: 100%; padding: .75rem 1rem; font-size: .95rem; font-weight: 600;
    color: #fff; background: var(--accent); border: none; border-radius: 8px; cursor: pointer; }
  button:hover { filter: brightness(1.05); }
  button.ghost { background: transparent; color: var(--accent); border: 1px solid var(--accent); margin-top: .6rem; }
  .err { color: var(--err); font-size: .85rem; margin: .8rem 0 0; }
  .steps { list-style: none; padding: 0; margin: 1.2rem 0 0; }
  .steps li { display: flex; align-items: center; gap: .6rem; padding: .35rem 0; color: var(--faint); font-size: .92rem; }
  .steps li.done { color: var(--ok); } .steps li.active { color: var(--text); } .steps li.fail { color: var(--err); }
  .ic { width: 18px; text-align: center; flex: none; }
  .result { margin-top: 1.2rem; font-weight: 600; } .result.ok { color: var(--ok); } .result.bad { color: var(--err); }
  .result a { color: var(--accent); }
  .next { margin: 1rem 0 0; padding: .9rem 1rem; background: var(--field); border: 1px solid var(--border);
    border-radius: 10px; font-size: .88rem; color: var(--muted); }
  .next b { color: var(--text); } .next a { color: var(--accent); }
  .vm { margin-top: 1.4rem; padding-top: .8rem; border-top: 1px solid var(--border); font-size: .78rem; color: var(--faint); }
  .vm a { color: inherit; text-decoration: underline; }
  .vm .src { margin-top: .4rem; }
  .vm b { color: var(--muted); font-weight: 600; }
"""

LOGO = ('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">'
        '<circle cx="12" cy="12" r="2"/><path d="M16.2 7.8a6 6 0 0 1 0 8.4M7.8 16.2a6 6 0 0 1 0-8.4M19 5a10 10 0 0 1 0 14M5 19A10 10 0 0 1 5 5"/></svg>')

NOSCRIPT_REFRESH = "<noscript><meta http-equiv=refresh content=3></noscript>"

# The time zone picker: a search field over the list. Without scripts the page still works, the
# list is then a plain drop-down. Rows are redrawn from what the server sent, never fetched.
TZ_SCRIPT = """<script>
(function () {
  var sel = document.getElementById("tz"), q = document.getElementById("tzq"), line = document.getElementById("tzc");
  if (!sel || !q) return;
  var rows = [], i;
  for (i = 0; i < sel.options.length; i++) {
    var o = sel.options[i], g = o.parentNode.tagName === "OPTGROUP" ? o.parentNode.label : "";
    var k = (o.value + " " + g + " " + o.text).toLowerCase().replace(/_/g, " ");
    rows.push({v: o.value, t: o.text, g: g, k: k, w: k.split(/[\\s\\/,()]+/)});
  }
  // A search word is the start of a word of the zone (ro finds Rome, not every zone of Europe); one
  // with a digit is an offset, looked for anywhere (+05:30, 5:30).
  function has(r, word) {
    if (/[0-9]/.test(word)) return r.k.indexOf(word) >= 0;
    for (var j = 0; j < r.w.length; j++) if (r.w[j].indexOf(word) === 0) return true;
    return false;
  }
  function row(v) { for (var j = 0; j < rows.length; j++) if (rows[j].v === v) return rows[j]; return null; }
  function say() {
    var r = row(sel.value), off = r ? /\\(UTC[^)]*\\)$/.exec(r.t) : null;
    if (line) line.textContent = r ? "Selected: " + r.v + (off && r.g ? " " + off[0] : "") : "";
  }
  function draw() {
    var words = q.value.toLowerCase().replace(/_/g, " ").split(/\\s+/).filter(Boolean);
    var keep = sel.value, first = "", hit = false, name = null, group = null;
    sel.textContent = "";
    rows.forEach(function (r) {
      for (var j = 0; j < words.length; j++) if (!has(r, words[j])) return;
      var o = new Option(r.t, r.v);
      if (r.g) {
        if (r.g !== name) { group = document.createElement("optgroup"); group.label = name = r.g; sel.appendChild(group); }
        group.appendChild(o);
      } else sel.appendChild(o);
      if (!first) first = r.v;
      if (r.v === keep) hit = true;
    });
    if (!first) {
      // Nothing matches: the choice made so far stays, and stays what the form sends.
      var none = new Option("No time zone matches", ""), r = row(keep);
      none.disabled = true;
      sel.appendChild(none);
      if (r) sel.appendChild(new Option(r.g ? r.v : r.t, r.v));
    }
    sel.value = hit || !first ? keep : first;
    say();
  }
  // A form nobody has answered yet starts on this browser's own zone, when the VM knows it.
  if (sel.getAttribute("data-auto") === "1") {
    try { var z = Intl.DateTimeFormat().resolvedOptions().timeZone; if (row(z)) sel.value = z; } catch (e) {}
  }
  sel.size = 7;
  sel.className = "list";
  q.hidden = false;
  q.addEventListener("input", draw);
  q.addEventListener("keydown", function (e) {
    if (e.key === "Enter") e.preventDefault();
    if (e.key === "ArrowDown") { e.preventDefault(); sel.focus(); }
  });
  sel.addEventListener("change", say);
  draw();
})();
</script>"""


def vm_identity():
    name = sh("hostname").strip()
    # Skip loopback and the Docker default bridge (172.17.x) - the footer should show the LAN address.
    ips = [ip for ip in sh("hostname", "-I").split()
           if not ip.startswith("127.") and not ip.startswith("172.17.")]
    parts = [html.escape(p) for p in ([name] if name else []) + ips[:2]]
    return " · ".join(parts)


def page(body, head_extra=""):
    ident = vm_identity()
    # AGPL-3.0 §13: this network-served UI must let its users reach the corresponding source.
    vm_line = f"<div>This VM: <b>{ident}</b></div>" if ident else ""
    src_line = "<div class=src>Argus - free software under the " \
               "<a href='https://www.gnu.org/licenses/agpl-3.0.html'>AGPL-3.0</a>. " \
               "<a href='https://github.com/g-guglielmi/argus-core'>Source code</a>.</div>"
    foot = f"<div class=vm>{vm_line}{src_line}</div>"
    return f"<!doctype html><html lang=en><head><meta charset=utf-8>" \
           f"<meta name=viewport content='width=device-width, initial-scale=1'>" \
           f"<meta name=color-scheme content='light dark'>{head_extra}" \
           f"<title>Argus core setup</title><style>{STYLE}</style></head><body>" \
           f"<div class=card><div class=brand>{LOGO}<div>Argus<small>Core setup</small></div></div>{body}{foot}</div></body></html>"


def options_html(pairs, selected):
    out = []
    for val, lab in pairs:
        sel = " selected" if val == selected else ""
        out.append(f'<option value="{html.escape(val)}"{sel}>{html.escape(lab)}</option>')
    return "\n".join(out)


def valid_zone(name, root=ZONEINFO):
    """A name timedatectl can set: UTC or Area/Place (up to three parts), a file in the zone database.
    The name comes from the form: it is taken apart by hand, with no regular expression to slow down
    on a long odd one."""
    name = name if isinstance(name, str) else ""
    parts = name.split("/")
    letters = set(string.ascii_letters + string.digits + "_+-")
    if len(name) > 64 or len(parts) > 3 or any(not p or not set(p) <= letters for p in parts):
        return False
    return os.path.isfile(os.path.join(root, *parts))


def zone_names(root=ZONEINFO):
    """Every zone of the VM's zone database, UTC aside, sorted.

    zone.tab is the database's own list for people who pick a zone: one row per country and zone,
    so each country finds its own name (Europe/Oslo, not only the Europe/Berlin it shares its clock
    with). Without it: every file under the areas. Without a database: the short built-in list."""
    names = set()
    try:
        with open(os.path.join(root, "zone.tab"), encoding="utf-8") as f:
            for line in f:
                cols = line.rstrip("\n").split("\t")
                if not line.startswith("#") and len(cols) >= 3:
                    names.add(cols[2].strip())
    except (OSError, UnicodeError):
        pass
    if not names:
        for area in ZONE_AREAS:
            for folder, _, files in os.walk(os.path.join(root, area)):
                rel = os.path.relpath(folder, root).replace(os.sep, "/")
                names.update(rel + "/" + fn for fn in files)
    names = {n for n in names if n.split("/")[0] in ZONE_AREAS and valid_zone(n, root)}
    return sorted(names or set(FALLBACK_TIMEZONES) - {"UTC"})


def utc_offset(name, now=None):
    """A zone's offset right now, like UTC+01:00 (it moves with daylight saving); "" when unknown."""
    try:
        from zoneinfo import ZoneInfo
        now = now or datetime.datetime.now(datetime.timezone.utc)
        mins = int(now.astimezone(ZoneInfo(name)).utcoffset().total_seconds() // 60)
    except Exception:
        return ""
    return "UTC%s%02d:%02d" % ("+" if mins >= 0 else "-", abs(mins) // 60, abs(mins) % 60)


def timezone_options(selected, root=ZONEINFO, now=None):
    """The time zone list: UTC, then every zone under its area (Europe: Rome (UTC+01:00)). The value
    stays the database's name. A chosen zone the list does not carry (an old alias from a seed disk)
    keeps a row of its own, so a redraw of the form never swaps it for another one."""
    names = zone_names(root)
    out = []
    if selected and selected != "UTC" and selected not in names:
        out.append(f'<option value="{html.escape(selected)}" selected>{html.escape(selected)}</option>')
    out.append('<option value="UTC"%s>UTC</option>' % (" selected" if selected == "UTC" else ""))
    areas = {}
    for n in names:
        area, _, place = n.partition("/")
        areas.setdefault(area, []).append((place.replace("/", ", ").replace("_", " "), n))
    for area in sorted(areas):
        out.append(f'<optgroup label="{html.escape(area)}">')
        for place, n in sorted(areas[area]):
            off = utc_offset(n, now)
            lab = place + (" (%s)" % off if off else "")
            sel = " selected" if n == selected else ""
            out.append(f'<option value="{html.escape(n)}"{sel}>{html.escape(lab)}</option>')
        out.append("</optgroup>")
    return "\n".join(out)


def form_page(error="", vals=None, csrf=""):
    v = vals or {}
    ip = primary_ip()
    d = lambda key, default="": html.escape(v.get(key) or default)  # noqa: E731
    err = f'<p class="err">{html.escape(error)}</p>' if error else ""
    body = f"""
  <a class="back" href="/">&lsaquo; Back</a>
  <h1>Set up your monitoring core</h1>
  <p class="hint">One form configures everything on this VM: Zabbix, its database, the probe
  enrollment PKI, and Argus. Nothing here has defaults left over from the image: every
  credential is created now, for this instance only.</p>
  {err}
  <form method="post" action="/">
    <input type="hidden" name="csrf" value="{html.escape(csrf)}">
    <label for="s">Setup code</label>
    <input id="s" name="setup_code" placeholder="XXXX-XXXX" autocomplete="off" required>
    <div class="sub">Shown on this VM's console (the hypervisor's console window), so only someone who can see it can set up this core.
    The hyphen and capitals don't matter. It does not expire; the console shows a new one when this VM restarts, or after {CODE_MAX_FAILURES} wrong tries.</div>
    <h2>System</h2>
    <div class="row"><div>
      <label for="h">Hostname</label>
      <input id="h" name="hostname" value="{d('hostname', 'argus-core')}" required>
    </div><div>
      <label for="k">Console keyboard layout</label>
      <select id="k" name="keymap">{options_html(KEYMAPS, v.get('keymap') or 'us')}</select>
    </div></div>
    <label for="tz">Timezone</label>
    <input id="tzq" type="search" placeholder="Search: rome, new york, asia, +05:30" aria-label="Search the time zones" autocomplete="off" hidden>
    <select id="tz" name="timezone"{'' if v.get('timezone') else ' data-auto="1"'}>{timezone_options(v.get('timezone') or 'UTC')}</select>
    <div class="sub pick" id="tzc"></div>
    <div class="sub">Used for the system clock, Zabbix, and Argus notifications (changeable later in Argus &rarr; Settings).
    Every zone this VM knows, with its offset from UTC today; it starts on your browser's zone.</div>

    <h2>Administrator</h2>
    <label for="e">Email</label>
    <input id="e" name="email" type="email" placeholder="you@example.com" value="{d('email')}" required>
    <div class="sub">Your Argus sign-in.</div>
    <label for="p1">Administrator password</label>
    <input id="p1" name="password" type="password" minlength="8" required>
    <label for="p2">Confirm password</label>
    <input id="p2" name="password2" type="password" minlength="8" required>
    <div class="sub">Used for the Argus admin, the Zabbix <b>Admin</b> user, and the local Linux user;
    rotate any of them independently later. The database password and the Zabbix API token are
    generated for you and never shown.</div>

    <details>
      <summary>Advanced</summary>
      <label for="lu">Linux username</label>
      <input id="lu" name="linux_user" value="{d('linux_user', 'argus')}">
      <div class="sub">Local sudo user for the hypervisor console and SSH.</div>
      <label for="lp">Linux password <span style="font-weight:400;color:var(--faint)">(blank = administrator password)</span></label>
      <input id="lp" name="linux_password" type="password">
      <label for="zp">Zabbix Admin password <span style="font-weight:400;color:var(--faint)">(blank = administrator password)</span></label>
      <input id="zp" name="zabbix_password" type="password">
      <label for="ap">Argus admin password <span style="font-weight:400;color:var(--faint)">(blank = administrator password)</span></label>
      <input id="ap" name="argus_password" type="password">
      <label for="pu">Public URL</label>
      <input id="pu" name="public_url" value="{d('public_url', 'https://%s' % ip)}">
      <div class="sub">The address people open Argus at; links in notifications use it, and probes check
      in there. Argus is served over https on this VM (a certificate from its own CA). Set your
      reverse-proxy FQDN here if you have one. Changeable later in Argus &rarr; Settings.</div>
    </details>

    <button type="submit">Set up this core</button>
  </form>
  {TZ_SCRIPT}
"""
    return page(body)


def render_steps(snap):
    items = []
    for s in snap["steps"]:
        cls = {"done": "done", "active": "active", "fail": "fail"}.get(s["state"], "")
        ic = {"done": "✓", "active": "…", "fail": "✕"}.get(s["state"], "•")
        doing = html.escape(snap.get("note") or "") if s["state"] == "active" else ""
        items.append(f'<li class="{cls}"><span class="ic">{ic}</span> <div>{html.escape(s["label"])}<small>{doing}</small></div></li>')
    return "\n".join(items)


def progress_page(snap, csrf="", cfg=None):
    cfg = cfg or {}
    restore = cfg.get("mode") == "restore"
    host = html.escape((cfg.get("restored") or {}).get("host") or "the backup")
    if snap["state"] == "failed":
        what = "The restore failed: " if restore else "Setup failed: "
        result = ('<div class="result bad" id="result">' + what + html.escape(snap["detail"]) +
                  '</div><form method="post" action="/retry"><input type="hidden" name="csrf" value="' + html.escape(csrf) + '">'
                  '<button type="submit">Retry from the failed step</button></form>')
        if restore:
            # The archive and the answers are what they are: there is nothing to edit, and a restore that
            # stopped half way is put right by running it again from that step.
            result += ('<div class="sub">Retrying runs the failed step again; the steps before it are kept. To start over with '
                       'another archive, make a new VM from the image.</div>')
        else:
            result += ('<form method="get" action="/"><input type="hidden" name="edit" value="1">'
                       '<button class="ghost" type="submit">Change the answers</button></form>')
    elif snap["state"] == "done":
        result = restored_fragment() if restore else success_fragment()
    else:
        result = '<div class="result" id="result"></div>'
    if restore:
        title = "Restoring %s&hellip;" % host
        hint = ("First this VM is set up, then the backup is put on it. The Zabbix database is the long step: "
                "with metric history it can take a while. Keep this page open: it follows the progress live.")
    else:
        title = "Setting up your monitoring core…"
        hint = ("This takes a few minutes (the database schema import is the long step). Keep this "
                "page open: it follows the progress live.")
    notes = ""
    if restore and cfg.get("test"):
        notes += '<div class="testnote">A test restore: this copy will alert nobody and never takes the old core\'s address.</div>'
    addr = html.escape((cfg.get("restored") or {}).get("address") or "")
    if restore and cfg.get("take_address") and addr:
        notes += (f'<div class="testnote">At the end this VM moves to the old core\'s address. If this page stops answering then, '
                  f'carry on at <a href="http://{addr}/">http://{addr}/</a>.</div>')
    body = f"""
  <h1>{title}</h1>
  <p class="hint">{hint}</p>
  <ul class="steps" id="steps">
{render_steps(snap)}
  </ul>
  {notes}
  <div id="tail">{result}</div>
  <script>
    // The state this page was RENDERED with: reload only when the live state differs, otherwise a
    // page already showing "failed"/"done" would reload itself forever.
    const RENDERED = "{html.escape(snap["state"])}";
    async function poll() {{
      let s;
      try {{ s = await (await fetch("/status")).json(); }} catch (e) {{ setTimeout(poll, 2500); return; }}
      const icons = {{done: "✓", active: "…", fail: "✕", pending: "•"}};
      const lis = [...document.querySelectorAll("#steps li")];
      if (s.steps.length !== lis.length) {{ location.reload(); return; }}
      s.steps.forEach((st, i) => {{
        lis[i].className = st.state === "pending" ? "" : st.state;
        lis[i].querySelector(".ic").textContent = icons[st.state] || "•";
        lis[i].querySelector("small").textContent = st.state === "active" ? (s.note || "") : "";
      }});
      if (s.state === "failed" || s.state === "done") {{
        if (s.state !== RENDERED) location.reload();
        return;
      }}
      setTimeout(poll, 1500);
    }}
    poll();
  </script>
"""
    return page(body, head_extra=NOSCRIPT_REFRESH)


def success_fragment():
    done = read_json(DONE_PATH)
    ip = primary_ip()
    email = html.escape(done.get("email", ""))
    return f"""
  <div class="result ok">✓ Your monitoring core is ready.</div>
  <div class="next">
    <b>Argus:</b> <a href="https://{ip}/">https://{ip}/</a> - sign in as <b>{email}</b>.
    Your browser will warn about the certificate once: it is signed by this core's own CA
    (<code>/docker/argus/pki/ca.crt</code>); install that CA on your PCs, or put your own certificate on
    the reverse proxy later.<br>
    <b>Zabbix UI</b> (engine room, rarely needed): <a href="http://{ip}:8080/">http://{ip}:8080/</a> - user <b>Admin</b>.<br><br>
    Next steps: add your first probe (Argus &rarr; <b>Probes</b> &rarr; Add probe), and take a
    hypervisor snapshot of this VM. From now on <a href="http://{ip}/">http://{ip}/</a> redirects to
    Argus over https, and this setup page is gone after you leave it.
  </div>
"""


def done_page():
    if read_json(DONE_PATH).get("restored"):
        return page("<h1>Restore complete</h1>" + restored_fragment())
    return page("<h1>Setup complete</h1>" + success_fragment())


# ---------------------------------------------------------------- pages: restoring a backup

MONTHS = ("Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec")


def fmt_made(ts):
    """'20261122T013016Z' -> '22 Nov 2026, 01:30 UTC' (an archive's name and manifest carry UTC)."""
    try:
        t = time.strptime(ts or "", "%Y%m%dT%H%M%SZ")
    except (ValueError, TypeError):
        return ts or "?"
    return "%d %s %d, %02d:%02d UTC" % (t.tm_mday, MONTHS[t.tm_mon - 1], t.tm_year, t.tm_hour, t.tm_min)


def fmt_size(n):
    try:
        n = float(n)
    except (ValueError, TypeError):
        return ""
    if n < 0:
        return ""
    for unit in ("bytes", "KB", "MB", "GB"):
        if n < 1024 or unit == "GB":
            return ("%d %s" % (n, unit)) if unit == "bytes" else ("%.0f %s" % (n, unit) if n >= 10 else "%.1f %s" % (n, unit))
        n /= 1024


def restore_public_key():
    """The public half of a key made for this restore, for an rsync target to accept: the old core's
    own key is in the archive that is not here yet. "" when ssh-keygen is not there."""
    if not os.path.exists(RESTORE_KEY + ".pub"):
        for leftover in (RESTORE_KEY, RESTORE_KEY + ".pub"):
            try:
                os.unlink(leftover)
            except OSError:
                pass
        try:
            subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "argus-first-boot-restore", "-f", RESTORE_KEY],
                           capture_output=True, timeout=30, stdin=subprocess.DEVNULL)
        except (OSError, subprocess.TimeoutExpired):
            return ""
    try:
        with open(RESTORE_KEY + ".pub", encoding="utf-8") as fh:
            return fh.read().strip()
    except OSError:
        return ""


def choice_page():
    return page("""
  <h1>What should this VM be?</h1>
  <p class="hint">Either way one page does it, and nothing on this VM has defaults left over from the image.</p>
  <a class="way" href="/?new"><div><b>A new core</b><span>Start empty: one form, then you add your probes and hosts.</span></div><i>&rsaquo;</i></a>
  <a class="way" href="/?restore"><div><b>A core restored from a backup</b><span>Bring a core back from one of its archives: to replace one that is lost,
    or to try a backup next to the core that still runs.</span></div><i>&rsaquo;</i></a>
""")


# The restore form's script: which fields show, the look for archives at a target, and the upload
# with its progress. The form needs it (an archive of hundreds of megabytes is not a form field).
RESTORE_SCRIPT = """<script>
(function () {
  var f = document.getElementById("rf");
  if (!f) return;
  var msg = document.getElementById("msg"), found = document.getElementById("found"), barBox = document.getElementById("bar");
  function tell(t) { msg.textContent = t || ""; msg.hidden = !t; if (t) msg.scrollIntoView({block: "center"}); }
  function source() { var r = f.querySelector("input[name=source]:checked"); return r ? r.value : "upload"; }
  function show() {
    var up = source() === "upload", t = document.getElementById("rtype").value;
    document.getElementById("up").hidden = !up;
    document.getElementById("rm").hidden = up;
    Array.prototype.forEach.call(document.querySelectorAll(".t"), function (d) { d.hidden = d.getAttribute("data-t") !== t; });
  }
  function busy(on) {
    Array.prototype.forEach.call(f.querySelectorAll("button"), function (b) { b.disabled = on; });
  }
  function bar(part) {
    barBox.hidden = part < 0;
    if (part >= 0) {
      barBox.firstChild.style.width = Math.round(part * 100) + "%";
      document.getElementById("barnote").textContent = part < 1 ? "Uploading: " + Math.round(part * 100) + " %" : "Uploaded. Checking the archive is next.";
    } else document.getElementById("barnote").textContent = "";
  }
  function fields() {
    var out = [];
    Array.prototype.forEach.call(f.elements, function (e) {
      if (!e.name || e.type === "file" || ((e.type === "radio" || e.type === "checkbox") && !e.checked)) return;
      out.push(encodeURIComponent(e.name) + "=" + encodeURIComponent(e.value));
    });
    return out.join("&");
  }
  function post(path, done) {
    var x = new XMLHttpRequest();
    x.open("POST", path);
    x.setRequestHeader("Content-Type", "application/x-www-form-urlencoded");
    x.onload = function () {
      var d;
      try { d = JSON.parse(x.responseText); } catch (e) { d = {error: "This VM did not answer as expected. Reload the page and try again."}; }
      done(d);
    };
    x.onerror = function () { done({error: "This VM did not answer. Is it still up?"}); };
    x.send(fields());
  }
  function auth(next) {
    post("/restore/auth", function (d) {
      if (d.ok) { next(); return; }
      busy(false); bar(-1); found.textContent = ""; tell(d.error);
    });
  }
  Array.prototype.forEach.call(f.querySelectorAll("input[name=source]"), function (r) { r.addEventListener("change", show); });
  document.getElementById("rtype").addEventListener("change", function () { found.textContent = ""; show(); });
  document.getElementById("file").addEventListener("change", function () {
    var file = this.files[0];
    document.getElementById("fname").textContent = file ? file.name + " (" + Math.round(file.size / 1048576) + " MB)" : "";
  });
  show();
  document.getElementById("find").addEventListener("click", function () {
    tell(""); busy(true); found.textContent = "Looking\\u2026";
    auth(function () {
      post("/restore/find", function (d) {
        busy(false); found.textContent = "";
        if (d.error) { tell(d.error); return; }
        var head = document.createElement("div");
        head.className = "found"; head.textContent = d.title;
        found.appendChild(head);
        d.archives.forEach(function (a, i) {
          var row = document.createElement("label"), radio = document.createElement("input"), when = document.createElement("span"), size = document.createElement("span");
          row.className = "arch"; radio.type = "radio"; radio.name = "archive_name"; radio.value = a.name; radio.checked = i === 0;
          when.className = "when"; when.textContent = a.host + " \\u00b7 " + a.when + (a.encrypted ? "" : " \\u00b7 not encrypted");
          size.className = "size"; size.textContent = a.size;
          row.appendChild(radio); row.appendChild(when); row.appendChild(size);
          found.appendChild(row);
        });
      });
    });
  });
  f.addEventListener("submit", function (ev) {
    ev.preventDefault(); tell("");
    var upload = source() === "upload", file = document.getElementById("file").files[0];
    if (upload && !file) { tell("Choose the archive file first."); return; }
    if (!upload && !f.querySelector("input[name=archive_name]:checked")) { tell("Look for archives and choose one first."); return; }
    if (f.elements["linux_password"].value !== f.elements["linux_password2"].value) { tell("The two Linux passwords don't match."); return; }
    busy(true);
    auth(function () {
      if (!upload) { f.submit(); return; }
      var x = new XMLHttpRequest();
      x.open("PUT", "/restore/upload");
      x.setRequestHeader("X-Csrf", f.elements["csrf"].value);
      x.setRequestHeader("X-Archive-Name", encodeURIComponent(file.name));
      x.upload.onprogress = function (e) { if (e.lengthComputable) bar(e.loaded / e.total); };
      x.onload = function () {
        var d;
        try { d = JSON.parse(x.responseText); } catch (e) { d = {error: "The upload did not finish."}; }
        if (d.ok) { bar(1); f.submit(); } else { busy(false); bar(-1); tell(d.error); }
      };
      x.onerror = function () { busy(false); bar(-1); tell("The upload stopped: the connection to this VM was lost."); };
      bar(0);
      x.send(file);
    });
  });
})();
</script>"""


def restore_form_page(error="", vals=None, csrf=""):
    v = vals or {}
    d = lambda key, default="": html.escape(v.get(key) or default)  # noqa: E731
    err = html.escape(error)
    remote = v.get("source") == "remote"
    key = restore_public_key()
    key_block = (f'<label for="rk">Key for the target</label><textarea id="rk" class="mono" rows="3" readonly>{html.escape(key)}</textarea>'
                 '<div class="sub">This VM made a key for the restore: add this line to <code>~/.ssh/authorized_keys</code> of that user on the '
                 'target, then look for archives. The old core\'s own key is in the backup, which is not here yet.</div>') if key else \
        '<div class="sub">This VM could not make an SSH key (ssh-keygen is missing): upload the archive instead.</div>'
    body = f"""
  <a class="back" href="/">&lsaquo; Back</a>
  <h1>Restore a core from a backup</h1>
  <p class="hint">This VM becomes the core an archive was made of: its hosts and history, its users,
  settings and certificates. You need one archive and its passphrase.</p>
  <p class="err" id="msg"{'' if err else ' hidden'}>{err}</p>
  <noscript><p class="err">This form needs scripts turned on in your browser: it uploads the archive and shows the progress.</p></noscript>
  <form method="post" action="/restore/check" id="rf">
    <input type="hidden" name="csrf" value="{html.escape(csrf)}">
    <label for="s">Setup code</label>
    <input id="s" name="setup_code" placeholder="XXXX-XXXX" autocomplete="off" required>
    <div class="sub">Shown on this VM's console (the hypervisor's console window), so only someone who can see it can set up this core.
    The hyphen and capitals don't matter.</div>

    <h2>The backup</h2>
    <label class="opt first"><input type="radio" name="source" value="upload"{'' if remote else ' checked'}><div><b>Upload it from this computer</b>
      <span>The <code>.tar.gpg</code> file, as the core wrote it.</span></div></label>
    <div class="in" id="up">
      <div class="file"><label class="pickbtn" for="file">Choose the archive</label><span class="name" id="fname"></span></div>
      <input id="file" type="file" accept=".gpg,.tar" hidden>
      <div class="bar" id="bar" hidden><div></div></div>
      <div class="sub" id="barnote"></div>
      <div class="sub">A large archive takes a few minutes to upload. Keep this page open meanwhile.</div>
    </div>
    <label class="opt"><input type="radio" name="source" value="remote"{' checked' if remote else ''}><div><b>Fetch it from where the backups are exported</b>
      <span>An SMB share, an NFS export, an SSH server or an S3 bucket: the place set under Settings, Backups on the old core.</span></div></label>
    <div class="in" id="rm" hidden>
      <label for="rtype">Exported to</label>
      <select id="rtype" name="rtype">{options_html(REMOTES, v.get('rtype') or 'smb')}</select>
      <div class="t" data-t="smb">
        <label for="share">Share</label><input id="share" name="share" value="{d('share')}" placeholder="//nas.example.lan/backups">
        <label for="spath">Folder</label><input id="spath" name="smb_path" value="{d('smb_path')}" placeholder="argus">
        <div class="row"><div><label for="suser">User</label><input id="suser" name="username" value="{d('username')}" placeholder="backup (empty = guest)"></div>
        <div><label for="sdom">Domain</label><input id="sdom" name="domain" value="{d('domain')}" placeholder="optional"></div></div>
        <label for="spass">Password</label><input id="spass" name="smb_password" type="password" autocomplete="off">
      </div>
      <div class="t" data-t="nfs" hidden>
        <label for="export">Export</label><input id="export" name="export" value="{d('export')}" placeholder="nas.example.lan:/volume1/backups">
        <label for="npath">Folder</label><input id="npath" name="nfs_path" value="{d('nfs_path')}" placeholder="argus">
        <label for="nopt">Mount options</label><input id="nopt" name="options" value="{d('options')}" placeholder="soft,timeo=150,retrans=3">
        <div class="sub">Empty uses these. This VM's address must be allowed to read the export.</div>
      </div>
      <div class="t" data-t="rsync" hidden>
        <div class="row"><div><label for="target">Target</label><input id="target" name="target" value="{d('target')}" placeholder="backup@nas.example.lan:/volume1/argus"></div>
        <div><label for="port">SSH port</label><input id="port" name="port" value="{d('port')}" placeholder="22" inputmode="numeric"></div></div>
        {key_block}
      </div>
      <div class="t" data-t="s3" hidden>
        <label for="endpoint">Endpoint</label><input id="endpoint" name="endpoint" value="{d('endpoint')}" placeholder="https://s3.eu-central-1.amazonaws.com">
        <div class="row"><div><label for="bucket">Bucket</label><input id="bucket" name="bucket" value="{d('bucket')}" placeholder="argus-backups"></div>
        <div><label for="region">Region</label><input id="region" name="region" value="{d('region')}" placeholder="eu-central-1"></div></div>
        <label for="s3path">Folder</label><input id="s3path" name="s3_path" value="{d('s3_path')}" placeholder="core">
        <label for="akey">Access key</label><input id="akey" name="access_key" value="{d('access_key')}" autocomplete="off">
        <label for="skey">Secret key</label><input id="skey" name="s3_secret" type="password" autocomplete="off">
      </div>
      <button class="ghost small" type="button" id="find">Look for archives</button>
      <div id="found"></div>
    </div>

    <label for="pp">Encryption passphrase</label>
    <input id="pp" name="passphrase" type="password" autocomplete="off">
    <div class="sub">The one set under Settings, Backups on the old core. Without it no encrypted archive can be opened.</div>

    <h2>This VM</h2>
    <div class="row"><div>
      <label for="k">Console keyboard layout</label>
      <select id="k" name="keymap">{options_html(KEYMAPS, v.get('keymap') or 'us')}</select>
    </div><div>
      <label for="lu">Linux username</label>
      <input id="lu" name="linux_user" value="{d('linux_user', 'argus')}">
    </div></div>
    <div class="row"><div>
      <label for="lp">Linux password</label><input id="lp" name="linux_password" type="password" minlength="8" required>
    </div><div>
      <label for="lp2">Confirm password</label><input id="lp2" name="linux_password2" type="password" minlength="8" required>
    </div></div>
    <div class="sub">The local user for the console and SSH: a backup does not carry it. Everything else comes from the archive.</div>

    <button type="submit">Check the archive</button>
    <div class="sub">Nothing is changed yet. The next page shows what the archive holds and asks what this VM should become.</div>
  </form>
  {RESTORE_SCRIPT}
"""
    return page(body)


RELOAD_ON_CHANGE = """<script>
(function () {
  var was = document.body.getAttribute("data-prep");
  function poll() {
    fetch("/status").then(function (r) { return r.json(); }).then(function (s) {
      if ((s.prep || {}).state !== was) location.reload(); else setTimeout(poll, 1500);
    }).catch(function () { setTimeout(poll, 2500); });
  }
  poll();
})();
</script>"""


def prepare_page(snap):
    fetching = snap["state"] == "fetching"
    body = f"""
  <h1>{'Fetching the archive' if fetching else 'Checking the archive'}&hellip;</h1>
  <p class="hint">Nothing on this VM is changed yet. Keep this page open: it moves on by itself.</p>
  <ul class="steps">
    <li class="{'active' if fetching else 'done'}"><span class="ic">{'&hellip;' if fetching else '&#10003;'}</span> Getting the archive onto this VM</li>
    <li class="{'' if fetching else 'active'}"><span class="ic">{'&bull;' if fetching else '&hellip;'}</span> Opening it and checking every part against its checksum</li>
  </ul>
  {RELOAD_ON_CHANGE}
"""
    return page(body, head_extra=NOSCRIPT_REFRESH).replace("<body>", '<body data-prep="%s">' % html.escape(snap["state"]), 1)


def elsewhere_page():
    return page("""
  <h1>A restore is being prepared</h1>
  <p class="hint">Somebody started restoring a backup onto this VM from another browser. That browser
  carries on from here. To start again instead, reboot this VM: it then shows a new setup code on its console.</p>
""")


PARTS = (("zabbix.dump", "the Zabbix database"), ("argus.db.gz", "the Argus database"),
         ("files.tar.gz", "the configuration, keys and certificates"))


def old_address(info):
    """(the old core's first IPv4 address, whether this VM can take it over by itself): only an address
    the old core had as a fixed one is taken; one it had from DHCP is a reservation to move."""
    n = info.get("network") or {}
    v4 = [a.split("/")[0] for a in (n.get("addresses") or []) if isinstance(a, str) and ":" not in a]
    return (v4[0] if v4 else ""), bool(v4) and not n.get("dhcp")


def confirm_page(info, csrf="", error=""):
    v, ts = info.get("versions") or {}, info.get("timescaledb") or {}
    host = html.escape(info.get("host") or "the old core")
    holds = [label + (" with its metric history" if key == "zabbix.dump" and info.get("history") else
                      " without metric history (charts start empty)" if key == "zabbix.dump" else "")
             for key, label in PARTS if key in (info.get("parts") or [])]
    facts = [("Made", "%s, on <b>%s</b>%s" % (html.escape(fmt_made(info.get("created_at"))), host,
                                              " (Argus %s)" % html.escape(info["argus_version"]) if info.get("argus_version") else "")),
             ("Holds", html.escape(", ".join(holds)[:1].upper() + ", ".join(holds)[1:]))]
    zbx = (v.get("zabbix-server-pgsql") or "").split(":")[-1].split("-")[0]
    if zbx:
        facts.append(("Zabbix", html.escape(zbx)))
    if ts.get("wanted"):
        note = ""
        if ts.get("installs"):
            note = '<span class="note">This VM has %s: the restore installs %s first.</span>' % (html.escape(ts.get("here") or "another version"), html.escape(ts["installs"]))
        facts.append(("TimescaleDB", html.escape(ts["wanted"]) + note))
    n = info.get("network") or {}
    addr, takeable = old_address(info)
    if n.get("addresses"):
        facts.append(("The old core was at", "%s, gateway %s (%s)" % (html.escape(", ".join(n.get("addresses") or [])), html.escape(n.get("gateway") or "?"),
                                                                      "from DHCP" if n.get("dhcp") else "static")))
    rows = "\n".join("    <dt>%s</dt><dd>%s</dd>" % (k, val) for k, val in facts)
    problems = info.get("problems") or []
    err = f'<p class="err">{html.escape(error)}</p>' if error else ""
    cancel = (f'<form method="post" action="/restore/cancel"><input type="hidden" name="csrf" value="{html.escape(csrf)}">'
              '<button class="ghost" type="submit">Choose another archive</button></form>')
    if problems:
        why = "".join("<li>%s</li>" % html.escape(p) for p in problems)
        return page(f"""
  <h1>This archive does not fit this VM</h1>
  <p class="hint">Every part matches its checksum, but the archive was made on other versions than this
  VM has, and a restore over them is not safe. Nothing on this VM has changed.</p>
  <dl class="facts">
{rows}
  </dl>
  <ul class="why">{why}</ul>
  <p class="hint">Use the VM image that goes with that core, or an archive made after it was updated.</p>
  {cancel}
""")
    if takeable:
        take = (f'<label class="check"><input type="checkbox" name="take_address" value="1" id="ta" disabled><div>Also take the old core\'s address <b>{html.escape(addr)}</b> '
                f'and its hostname <b>{host}</b>. The probes dial that address, and this page then continues at http://{html.escape(addr)}/.</div></label>')
    elif n.get("dhcp"):
        take = ('<div class="check"><div>The old core got its address from DHCP: for the probes to find the new core, move that reservation '
                'to this VM in your DHCP server.</div></div>')
    else:
        take = ""
    return page(f"""
  <h1>The archive is complete</h1>
  <p class="hint">Every part matches its checksum. Nothing on this VM has changed yet.</p>
  {err}
  <dl class="facts">
{rows}
  </dl>
  <form method="post" action="/restore/go">
    <input type="hidden" name="csrf" value="{html.escape(csrf)}">
    <h2>What should this VM become?</h2>
    <label class="opt first"><input type="radio" name="mode" value="test" checked><div><b>A test restore</b>
      <span>The old core keeps running. This copy alerts nobody, mails no reports, exports no backups and never takes
      the old core's address. It is for looking at, then deleting.</span></div></label>
    <label class="opt"><input type="radio" name="mode" value="live"><div><b>The new core</b>
      <span>The old core is off or gone. This VM takes its place: it alerts, reports and exports backups as the old one did.</span></div></label>
    {take}
    <button type="submit">Restore</button>
  </form>
  {cancel}
  <script>
    (function () {{
      var box = document.getElementById("ta");
      if (!box) return;
      Array.prototype.forEach.call(document.querySelectorAll("input[name=mode]"), function (r) {{
        r.addEventListener("change", function () {{
          var live = document.querySelector("input[name=mode]:checked").value === "live";
          box.disabled = !live;
          if (!live) box.checked = false;
        }});
      }});
    }})();
  </script>
""")


def restored_fragment():
    done = read_json(DONE_PATH)
    r = done.get("restored") or {}
    ip = html.escape(r.get("address") or primary_ip())
    host, made = html.escape(r.get("host") or "the old core"), html.escape(fmt_made(r.get("created_at")))
    if r.get("test"):
        return f"""
  <div class="result ok">✓ This VM is a test restore of {host}.</div>
  <div class="next">
    <b>Argus:</b> <a href="https://{ip}/">https://{ip}/</a> - sign in with your own user of the old core, with the
    password and the second factor (a passkey is tied to the real core's address). Your browser will warn
    about the certificate: it names the real core.<br><br>
    Every page says it is a test restore. It shows the hosts and charts up to {made}, when the backup was
    made; probes read offline here, they still talk to the real core.<br><br>
    When you have looked, <b>delete this VM and its disk</b>: it holds every secret of the core.
    Should the real core be lost meanwhile, <code>sudo argus-restore live</code> on this VM's console makes this one real.
  </div>
"""
    return f"""
  <div class="result ok">✓ This VM is {host} now.</div>
  <div class="next">
    <b>Argus:</b> <a href="https://{ip}/">https://{ip}/</a> - sign in with your own user, as on the old core.
    It holds the hosts and charts up to {made}, when the backup was made.<br><br>
    The probes reconnect by themselves once this VM answers at the old core's address: their certificates
    came back with the backup. Check <b>Probes</b> (all online within a few minutes) and <b>Settings, Backups</b>.<br><br>
    The old core must stay off: two cores with the same identity would both alert and export.
  </div>
"""


# ---------------------------------------------------------------- validation + HTTP

def email_ok(s):
    """Something, one @, a domain with a dot inside it, no whitespace: the shape the form has always
    accepted, checked without a regular expression that backtracks on long odd input."""
    if not s or any(c.isspace() for c in s):
        return False
    local, at, domain = s.partition("@")
    return bool(at) and bool(local) and "@" not in domain and "." in domain[1:-1]


def validate(form):
    def one(key):
        return (form.get(key, [""])[0] or "").strip()

    vals = {k: one(k) for k in ("hostname", "keymap", "timezone", "email", "linux_user", "public_url")}
    pw, pw2 = form.get("password", [""])[0], form.get("password2", [""])[0]
    overrides = {k: form.get(k, [""])[0] for k in ("linux_password", "zabbix_password", "argus_password")}

    host = vals["hostname"].lower()
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,62}", host) or host.endswith("-"):
        return None, "Hostname must be lowercase letters, digits and hyphens."
    if vals["keymap"] not in {k for k, _ in KEYMAPS}:
        return None, "Unknown keyboard layout."
    if not valid_zone(vals["timezone"]):
        return None, "Unknown timezone."
    if not email_ok(vals["email"]):
        return None, "That email address doesn't look valid."
    user = vals["linux_user"].lower() or "argus"
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,31}", user):
        return None, "Linux username must start with a letter (lowercase letters, digits, hyphens)."
    if pw != pw2:
        return None, "The two passwords don't match."
    for label, p in [("Administrator", pw)] + [(k.replace("_", " "), v) for k, v in overrides.items() if v]:
        if len(p) < 8:
            return None, f"{label} password must be at least 8 characters."
        if any(c in p for c in "\r\n\x00"):
            return None, f"{label} password can't contain line breaks."
    pu = vals["public_url"]
    if pu:
        u = urlparse(pu)
        if u.scheme not in ("http", "https") or not u.netloc or re.search(r"[\s'\"\\]", pu):
            return None, "Public URL must start with http:// or https://."
        pu = pu.rstrip("/")

    cfg = {
        "hostname": host, "keymap": vals["keymap"], "timezone": vals["timezone"],
        "email": vals["email"], "linux_user": user, "public_url": pu,
        "linux_password": overrides["linux_password"] or pw,
        "zabbix_password": overrides["zabbix_password"] or pw,
        "argus_password": overrides["argus_password"] or pw,
    }
    return cfg, ""


def setup_done():
    return os.path.exists(DONE_PATH)


def archive_file_name(name):
    """The name an uploaded archive is kept under: the browser's file name when it is a plain
    'something.tar' or 'something.tar.gpg', else "". Taken apart by hand, no pattern to slow down on
    a long odd one."""
    name = (name or "").replace("\\", "/").rsplit("/", 1)[-1]
    ok = set(string.ascii_letters + string.digits + "._-")
    if not 5 <= len(name) <= 160 or not set(name) <= ok or name.startswith(".") or ".." in name:
        return ""
    return name if name.endswith(".tar") or name.endswith(".tar.gpg") else ""


def target_from_form(form):
    """The export target the form describes, in the backup plan's own words (argus-backup checks it
    again): ({"remote": ..., secrets}, "") or (None, what is missing)."""
    def one(key):
        return (form.get(key, [""])[0] or "").strip()
    kind = one("rtype")
    doc = {"remote": {"type": kind}, "password": "", "secret_key": "", "private_key": ""}
    r = doc["remote"]
    if kind == "smb":
        r.update(share=one("share"), path=one("smb_path"), username=one("username"), domain=one("domain"))
        doc["password"] = form.get("smb_password", [""])[0]
        if not r["share"]:
            return None, "Type the share the backups are on, like //nas.example.lan/backups."
    elif kind == "nfs":
        r.update(export=one("export"), path=one("nfs_path"), options=one("options"))
        if not r["export"]:
            return None, "Type the export the backups are on, like nas.example.lan:/volume1/backups."
    elif kind == "rsync":
        port = one("port") or "22"
        if not port.isdigit() or not 1 <= int(port) <= 65535:
            return None, "The SSH port is a number from 1 to 65535."
        r.update(target=one("target"), port=int(port))
        if not r["target"]:
            return None, "Type the target the backups are on, like backup@nas.example.lan:/volume1/argus."
        restore_public_key()
        try:
            with open(RESTORE_KEY, encoding="utf-8") as fh:
                doc["private_key"] = fh.read()
        except OSError:
            return None, "This VM has no SSH key for the target (ssh-keygen is missing): upload the archive instead."
    elif kind == "s3":
        r.update(endpoint=one("endpoint"), region=one("region"), bucket=one("bucket"), access_key=one("access_key"), path=one("s3_path"))
        doc["secret_key"] = form.get("s3_secret", [""])[0]
        if not r["endpoint"] or not r["bucket"] or not r["access_key"]:
            return None, "An S3 target needs its endpoint, bucket and access key."
    else:
        return None, "Choose where the backups are exported to."
    return doc, ""


def target_title(doc, n):
    r = doc["remote"]
    where = {"smb": (r.get("share", "") + ("/" + r["path"] if r.get("path") else "")),
             "nfs": (r.get("export", "") + ("/" + r["path"] if r.get("path") else "")),
             "rsync": r.get("target", ""),
             "s3": (r.get("bucket", "") + ("/" + r["path"] if r.get("path") else ""))}.get(r.get("type"), "")
    return "%d archive%s in %s, the newest first" % (n, "" if n == 1 else "s", where)


def validate_restore(form):
    """The restore form's own fields (the archive is dealt with apart): (cfg, "") or (None, why)."""
    def one(key):
        return (form.get(key, [""])[0] or "").strip()
    keymap = one("keymap")
    if keymap not in {k for k, _ in KEYMAPS}:
        return None, "Unknown keyboard layout."
    user = one("linux_user").lower() or "argus"
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,31}", user):
        return None, "Linux username must start with a letter (lowercase letters, digits, hyphens)."
    pw, pw2 = form.get("linux_password", [""])[0], form.get("linux_password2", [""])[0]
    if pw != pw2:
        return None, "The two Linux passwords don't match."
    if len(pw) < 8:
        return None, "Linux password must be at least 8 characters."
    passphrase = form.get("passphrase", [""])[0]
    for label, p in (("Linux password", pw), ("The passphrase", passphrase)):
        if any(c in p for c in "\r\n\x00"):
            return None, "%s can't contain line breaks." % label
    # Everything a new core's setup asks for and a restore replaces gets a value of this VM's own.
    cfg = {"mode": "restore", "hostname": "argus-core", "keymap": keymap, "timezone": "UTC",
           "email": "first-boot@restore.invalid", "linux_user": user, "public_url": "",
           "linux_password": pw, "zabbix_password": gen_password(), "argus_password": gen_password()}
    return cfg, passphrase


class Handler(BaseHTTPRequestHandler):
    def _send(self, body, status=200, ctype="text/html; charset=utf-8", cookie=""):
        data = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Frame-Options", "DENY")
        if cookie:
            self.send_header("Set-Cookie", "%s=%s; Path=/; HttpOnly; SameSite=Strict" % (SESSION_COOKIE, cookie))
        self.end_headers()
        self.wfile.write(data)

    def _json(self, doc, status=200, cookie=""):
        self._send(json.dumps(doc), status=status, ctype="application/json", cookie=cookie)

    def _code_error(self, form):
        """The setup code check: constant-time, counted, replaced after too many misses. "" when the
        code is right, else what to tell the person (a replaced code is said, or they keep typing the
        old one). The hyphen, spaces and capitals are theirs to leave out."""
        given = re.sub(r"[\s-]", "", form.get("setup_code", [""])[0] or "").upper()
        want = self.server.setup_code.replace("-", "")
        if hmac.compare_digest(given.encode("utf-8"), want.encode("utf-8")):
            self.server.code_failures = 0
            return ""
        self.server.code_failures += 1
        time.sleep(1)
        if self.server.code_failures >= CODE_MAX_FAILURES:
            self.server.setup_code = gen_setup_code()
            self.server.code_failures = 0
            announce_code(self.server.setup_code)
            return ("Too many wrong setup codes: this VM has made a new one. Read it again on the "
                    "console, the old one no longer works.")
        return "That setup code isn't right. It's shown on this VM's console."

    def _session(self):
        """Whether this browser typed the setup code on the restore path: the cookie it got then. The
        restore takes several requests (an upload, a look at a target, a last question), and the
        answers in between show what the archive holds, so they are for that browser only."""
        want = getattr(self.server, "session", "") or ""
        if not want:
            return False
        for part in (self.headers.get("Cookie") or "").split(";"):
            name, _, value = part.strip().partition("=")
            if name == SESSION_COOKIE and hmac.compare_digest(value.encode("utf-8"), want.encode("utf-8")):
                return True
        return False

    def _redirect(self, location="/"):
        # Post-Redirect-Get: a POST always answers with a 303 to a GET, so the progress page is only
        # ever reached by GET. Otherwise a browser reload (or the noscript meta-refresh) on a
        # POST-loaded page re-submits the form and restarts the whole setup - the observed "loop".
        self.send_response(303)
        self.send_header("Location", location)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_GET(self):
        if self.path.startswith("/status"):
            snap = ORCH.snapshot()
            prep = PREP.snapshot()
            if not self._session():
                prep["detail"] = ""
            snap["prep"] = prep
            self._send(json.dumps(snap), ctype="application/json")
            return
        if setup_done():
            self._send(done_page())
            return
        if self.path.startswith("/?edit") and not ORCH.running():
            cfg = read_json(CONFIG_PATH)
            if cfg.get("mode") != "restore":
                safe = {k: cfg.get(k, "") for k in ("hostname", "keymap", "timezone", "email",
                                                    "linux_user", "public_url")}
                self._send(form_page(vals=safe, csrf=self.server.csrf))
                return
        if os.path.exists(CONFIG_PATH):
            self._send(progress_page(ORCH.snapshot(), csrf=self.server.csrf, cfg=read_json(CONFIG_PATH)))
            return
        prep = PREP.snapshot()
        if prep["state"] != "idle":
            if not self._session():
                self._send(elsewhere_page())
            elif prep["state"] in ("fetching", "checking"):
                self._send(prepare_page(prep))
            elif prep["state"] == "ready":
                self._send(confirm_page(PREP.info, csrf=self.server.csrf))
            else:
                keep = dict(PREP.cfg.get("form") or {})
                detail = prep["detail"]
                PREP.reset()
                self._send(restore_form_page(error=detail, vals=keep, csrf=self.server.csrf))
            return
        if self.path.startswith("/?restore"):
            self._send(restore_form_page(csrf=self.server.csrf))
            return
        if self.path.startswith("/?new"):
            self._send(form_page(csrf=self.server.csrf))
            return
        self._send(choice_page())

    def do_PUT(self):
        """The archive, uploaded by the restore form's script as the request's body: streamed to a
        root-only folder, never held in memory."""
        if self.path != "/restore/upload" or setup_done() or os.path.exists(CONFIG_PATH):
            self._json({"error": "Nothing takes an upload here."}, status=404)
            return
        if not hmac.compare_digest(self.headers.get("X-Csrf") or "", self.server.csrf) or not self._session():
            self._json({"error": "Type the setup code again, then choose the archive."}, status=403)
            return
        name = archive_file_name(unquote(self.headers.get("X-Archive-Name") or ""))
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            length = 0
        if not name:
            self._json({"error": "That is not an archive: its name ends in .tar.gpg (or .tar when it is not encrypted)."}, status=400)
            return
        if length <= 0:
            self._json({"error": "The upload has no size: choose the archive again."}, status=411)
            return
        if PREP.snapshot()["state"] in ("fetching", "checking"):
            self._json({"error": "An archive is being checked already."}, status=409)
            return
        os.makedirs(STAGE_DIR, mode=0o700, exist_ok=True)
        free = shutil.disk_usage(STAGE_DIR).free
        if free < length * 3 + (1 << 30):
            self._json({"error": "This VM's disk is too small for that archive: it needs about %s free to unpack and restore it, and has %s."
                                 % (fmt_size(length * 3 + (1 << 30)), fmt_size(free))}, status=507)
            return
        for old in os.listdir(STAGE_DIR):  # one archive at a time
            try:
                os.unlink(os.path.join(STAGE_DIR, old))
            except OSError:
                pass
        part = os.path.join(STAGE_DIR, name + ".part")
        left = length
        try:
            with open(os.open(part, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "wb") as out:
                while left > 0:
                    chunk = self.rfile.read(min(UPLOAD_CHUNK, left))
                    if not chunk:
                        break
                    out.write(chunk)
                    left -= len(chunk)
        except OSError as e:
            self._json({"error": "The archive could not be written on this VM: %s" % (e.strerror or e)}, status=500)
            return
        if left:
            try:
                os.unlink(part)
            except OSError:
                pass
            self._json({"error": "The upload stopped before the end: try again."}, status=400)
            return
        os.replace(part, os.path.join(STAGE_DIR, name))
        self.server.upload = os.path.join(STAGE_DIR, name)
        self._json({"ok": True, "name": name, "size": length})

    def do_POST(self):
        length = min(int(self.headers.get("Content-Length", 0) or 0), 16384)
        form = parse_qs(self.rfile.read(length).decode("utf-8", "replace"), keep_blank_values=True)
        if setup_done():
            self._redirect("/")
            return
        restore = self.path.startswith("/restore/")
        wants_json = self.path in ("/restore/auth", "/restore/find")
        if not hmac.compare_digest(form.get("csrf", [""])[0], self.server.csrf):
            stale = "This form is stale; reload the page and try again."
            if wants_json:
                self._json({"error": stale}, status=400)
            elif restore:
                self._send(restore_form_page(error=stale, csrf=self.server.csrf), status=400)
            else:
                self._send(form_page(error=stale, csrf=self.server.csrf), status=400)
            return
        if self.path.startswith("/retry"):
            if os.path.exists(CONFIG_PATH) and not ORCH.running():
                ORCH.start(fresh=False)
            self._redirect("/")
            return
        if ORCH.running() or os.path.exists(CONFIG_PATH) and restore:
            if wants_json:
                self._json({"error": "This VM is being set up already: reload the page."}, status=409)
            else:
                self._redirect("/")
            return
        if restore:
            self._restore_post(form)
            return
        keep = {k: form.get(k, [""])[0] for k in ("hostname", "keymap", "timezone", "email",
                                                  "linux_user", "public_url")}
        code_error = self._code_error(form)
        if code_error:
            self._send(form_page(error=code_error, vals=keep, csrf=self.server.csrf), status=403)
            return
        cfg, err = validate(form)
        if not cfg:
            self._send(form_page(error=err, vals=keep, csrf=self.server.csrf), status=400)
            return
        write_json(CONFIG_PATH, cfg)
        clear_code_banner()
        ORCH.start(fresh=True)  # edited answers rerun every (idempotent) step against the new values
        self._redirect("/")

    def _restore_post(self, form):
        """The restore path's requests. Each needs the browser that typed the setup code: the first one
        checks the code (counted, like the setup form's) and hands that browser a cookie."""
        path = self.path
        cookie = ""
        if path in ("/restore/go", "/restore/cancel") and not self._session():
            self._redirect("/")  # the last question is for the browser that got this far
            return
        if not self._session():
            code_error = self._code_error(form)
            if code_error:
                if path in ("/restore/auth", "/restore/find"):
                    self._json({"error": code_error}, status=403)
                else:
                    keep = {k: form.get(k, [""])[0] for k in RESTORE_KEEP}
                    self._send(restore_form_page(error=code_error, vals=keep, csrf=self.server.csrf), status=403)
                return
            if not getattr(self.server, "session", ""):
                self.server.session = secrets.token_urlsafe(24)
            cookie = self.server.session
        if path == "/restore/auth":
            self._json({"ok": True}, cookie=cookie)
            return
        if path == "/restore/find":
            doc, why = target_from_form(form)
            if not doc:
                self._json({"error": why}, status=400, cookie=cookie)
                return
            write_json(TARGET_PATH, doc)
            rc, lines = run_tool([BACKUP_TOOL, "remote-list", "--from", TARGET_PATH], timeout=420)
            found = None
            if rc == 0:
                try:
                    found = json.loads(lines[-1]).get("archives")
                except (ValueError, IndexError, AttributeError):
                    found = None
            if found is None:
                self._json({"error": "This VM could not look there: " + (tool_error(lines) or "argus-backup ended with %d" % rc)},
                           status=502, cookie=cookie)
                return
            if not found:
                self._json({"error": "No Argus archive is there (the files are named argus-backup-...tar.gpg). Check the folder."},
                           status=404, cookie=cookie)
                return
            self._json({"title": target_title(doc, len(found)),
                        "archives": [{"name": a.get("name", ""), "host": a.get("host", ""), "when": fmt_made(a.get("ts")),
                                      "size": fmt_size(a.get("size", -1)), "encrypted": bool(a.get("encrypted"))} for a in found]},
                       cookie=cookie)
            return
        if path == "/restore/cancel":
            if PREP.reset():
                clear_restore_leftovers()
                self.server.upload = ""
            self._redirect("/?restore")
            return
        if path == "/restore/go":
            snap = PREP.snapshot()
            if snap["state"] != "ready" or (PREP.info.get("problems") or []):
                self._redirect("/")
                return
            cfg = dict(PREP.cfg)
            cfg.pop("form", None)
            cfg["test"] = form.get("mode", ["test"])[0] != "live"
            _, takeable = old_address(PREP.info)
            cfg["take_address"] = bool(not cfg["test"] and takeable and form.get("take_address", [""])[0] == "1")
            cfg["restored"] = {"host": PREP.info.get("host") or "", "created_at": PREP.info.get("created_at") or "",
                               "address": old_address(PREP.info)[0] if cfg["take_address"] else ""}
            write_json(CONFIG_PATH, cfg)
            clear_code_banner()
            ORCH.start(fresh=True)
            self._redirect("/")
            return
        if path != "/restore/check":
            self._redirect("/")
            return
        keep = {k: form.get(k, [""])[0] for k in RESTORE_KEEP}
        cfg, passphrase = validate_restore(form)
        if not cfg:
            self._send(restore_form_page(error=passphrase, vals=keep, csrf=self.server.csrf), status=400, cookie=cookie)
            return
        fetch_name = ""
        if form.get("source", ["upload"])[0] == "remote":
            fetch_name = form.get("archive_name", [""])[0]
            doc, why = target_from_form(form)
            if not doc or not archive_file_name(fetch_name):
                self._send(restore_form_page(error=why or "Look for archives and choose one first.", vals=keep, csrf=self.server.csrf),
                           status=400, cookie=cookie)
                return
            write_json(TARGET_PATH, doc)
            os.makedirs(STAGE_DIR, mode=0o700, exist_ok=True)
            cfg["archive"] = os.path.join(STAGE_DIR, fetch_name)
        else:
            cfg["archive"] = getattr(self.server, "upload", "") or ""
            if not os.path.isfile(cfg["archive"]):
                self._send(restore_form_page(error="The archive did not arrive on this VM: choose it again.", vals=keep, csrf=self.server.csrf),
                           status=400, cookie=cookie)
                return
        if passphrase:
            write_private(PASS_PATH, passphrase)
        else:
            try:
                os.unlink(PASS_PATH)
            except OSError:
                pass
        cfg["form"] = keep
        if not PREP.start(cfg, fetch_name):
            self._redirect("/")
            return
        self.send_response(303)
        self.send_header("Location", "/")
        self.send_header("Content-Length", "0")
        if cookie:
            self.send_header("Set-Cookie", "%s=%s; Path=/; HttpOnly; SameSite=Strict" % (SESSION_COOKIE, cookie))
        self.end_headers()

    def log_message(self, *args):
        pass


def monitor(httpd):
    """Once setup completes, give the success page a moment to be seen, then release port 80."""
    while True:
        time.sleep(3)
        if setup_done():
            time.sleep(20)
            httpd.shutdown()
            return


def main():
    os.makedirs(STATE_DIR, exist_ok=True)
    os.chmod(STATE_DIR, 0o700)
    if setup_done():
        # Shouldn't normally run again (the service is disabled below), but never squat on :80.
        subprocess.run(["systemctl", "disable", FIRSTBOOT_SERVICE], check=False)
        return 0
    httpd = ThreadingHTTPServer(LISTEN, Handler)
    httpd.csrf = secrets.token_urlsafe(24)
    disk_code = setup_code_from_disk()
    httpd.setup_code = disk_code or gen_setup_code()
    httpd.code_failures = 0
    httpd.session = ""   # the browser that typed the code on the restore path
    httpd.upload = ""    # the archive it uploaded
    if os.path.exists(CONFIG_PATH):
        print("argus-core-firstboot: resuming an interrupted setup", flush=True)
        ORCH.start(fresh=False)
    elif disk_code:
        print("argus-core-firstboot: setup code taken from the attached disk", flush=True)
    else:
        announce_code(httpd.setup_code)
    threading.Thread(target=monitor, args=(httpd,), daemon=True).start()
    print("argus-core-firstboot: serving setup page on http://%s:%d/" % LISTEN, flush=True)
    httpd.serve_forever()
    # We only get here after monitor() saw setup-done: retire this service and let nginx take :80
    # (the finish step already wrote the redirect site).
    clear_code_banner()
    subprocess.run(["systemctl", "disable", FIRSTBOOT_SERVICE], check=False)
    subprocess.run(["systemctl", "reload-or-restart", "nginx"], check=False)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

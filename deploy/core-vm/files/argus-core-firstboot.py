#!/usr/bin/env python3
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
  argus     write /etc/argus-core/argus.env (token, first-admin seed, secret key, CA/update mounts),
            start the argus + argus-updater containers, seed Public URL + timezone via the API
  finish    scrub the one-time admin seed from argus.env, arm the :80 -> :8081 redirect, mark done

The password model: one "administrator password" is used for the Debian user, the Zabbix Admin, and
the Argus admin (each individually overridable under Advanced). The database password and the Zabbix
API token are machine-generated and never displayed. Secrets needed for retries are cached root-only
under /var/lib/argus-core-setup and scrubbed once setup completes.

Steps are idempotent; a failed run can be retried (same answers) or edited. A reboot mid-setup
resumes automatically. Once done, the service disables itself and hands port 80 to nginx (which then
just redirects to Argus on :8081). Stdlib only.
"""
import html
import json
import os
import re
import secrets
import string
import subprocess
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

STATE_DIR = "/var/lib/argus-core-setup"
CONFIG_PATH = os.path.join(STATE_DIR, "config.json")   # the submitted answers (0600, deleted at finish)
STATE_PATH = os.path.join(STATE_DIR, "state.json")     # step markers + cached secrets (0600, scrubbed at finish)
DONE_PATH = os.path.join(STATE_DIR, "setup-done")      # marker + summary for the success page

ARGUS_ENV = "/etc/argus-core/argus.env"
IMAGE_ENV = "/etc/argus-core/image.env"
SNIPPET = "/etc/argus-core/zabbix_server.conf.snippet"
ZBX_CONF = "/etc/zabbix/zabbix_server.conf"
ZBX_WEB_CONF = "/etc/zabbix/web/zabbix.conf.php"
ZBX_NGINX = "/etc/zabbix/nginx.conf"
ZBX_PHP_FPM = "/etc/zabbix/php-fpm.conf"
AGENT2_DROPIN = "/etc/zabbix/zabbix_agent2.d/argus.conf"
NGINX_REDIRECT = "/etc/nginx/conf.d/argus-port80.conf"
PKI_DIR = "/etc/argus/pki"                             # mounted read-only into the core as /ca
ZBX_CERTS = "/etc/zabbix/certs"
CA_CN = "Monitoring Core CA"                           # matches deploy/pki/gen-certs.sh

ZBX_API = "http://127.0.0.1:8080/api_jsonrpc.php"
ARGUS_URL = "http://127.0.0.1:8081"
ARGUS_UID = "65532"                                    # the distroless core's nonroot uid
FIRSTBOOT_SERVICE = "argus-firstboot.service"
LISTEN = ("0.0.0.0", 80)

KEYMAPS = [("us", "US English"), ("uk", "UK English"), ("it", "Italian"), ("de", "German"),
           ("fr", "French"), ("es", "Spanish"), ("pt-latin1", "Portuguese")]
TIMEZONES = [
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
    ("accounts", "Securing Zabbix accounts"),
    ("argus", "Starting Argus"),
    ("finish", "Finishing up"),
]
STEP_KEYS = [k for k, _ in STEPS]


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
    if os.path.exists("/usr/share/zoneinfo/" + cfg["timezone"]):
        run(["timedatectl", "set-timezone", cfg["timezone"]])
    km = cfg["keymap"]
    if re.fullmatch(r"[a-z][a-z0-9-]{1,15}", km):
        with open("/etc/vconsole.conf", "w", encoding="utf-8") as fh:
            fh.write("KEYMAP=%s\n" % km)
        run(["systemctl", "restart", "systemd-vconsole-setup.service"], check=False)
    user = cfg["linux_user"]
    if subprocess.run(["id", "-u", user], capture_output=True).returncode != 0:
        run(["useradd", "-m", "-s", "/bin/bash", "-G", "sudo", user])
    run(["usermod", "-aG", "docker", user], check=False)
    run(["chpasswd"], input_text="%s:%s\n" % (user, cfg["linux_password"]))


def step_database(cfg, state):
    # Tune PostgreSQL for the DEPLOYED VM's RAM (the image build ran on a small builder).
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
    run(["bash", "-c",
         "chown -R zabbix:zabbix '%s' && chmod 644 '%s'/*.crt && chmod 600 '%s'/zabbix-core.key"
         % (ZBX_CERTS, ZBX_CERTS, ZBX_CERTS)])
    run(["bash", "-c",
         "chown -R %s:%s '%s' && chmod 700 '%s' && chmod 400 '%s/ca.key' && chmod 444 '%s/ca.crt'"
         % (ARGUS_UID, ARGUS_UID, PKI_DIR, PKI_DIR, PKI_DIR, PKI_DIR)])


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
    values = {"timezone": cfg["timezone"]}
    if cfg.get("public_url"):
        values["public_url"] = cfg["public_url"]
    argus_patch_settings(cookie, values)


def step_finish(cfg, state):
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

    # Park a redirect on :80 so http://<vm>/ lands on Argus from now on. Written here, but nginx only
    # picks it up when this service exits and releases the port (main() reloads nginx after shutdown).
    with open(NGINX_REDIRECT, "w", encoding="utf-8") as fh:
        fh.write("# Written by the Argus core appliance first-boot setup: the setup page is gone;\n"
                 "# send http://<vm>/ visitors to Argus.\n"
                 "server {\n    listen 80 default_server;\n    server_name _;\n"
                 "    return 301 http://$host:8081$request_uri;\n}\n")

    write_json(DONE_PATH, {"email": cfg["email"], "hostname": cfg["hostname"], "done_at": int(time.time())})
    try:
        os.unlink(CONFIG_PATH)  # the submitted answers (passwords) are no longer needed
    except FileNotFoundError:
        pass
    for k in ("dbpass", "zbx_token", "secret_key"):  # live copies now sit in root-only config files
        state.pop(k, None)
    write_json(STATE_PATH, state)


STEP_FUNCS = {"system": step_system, "database": step_database, "pki": step_pki,
              "zabbix": step_zabbix, "accounts": step_accounts, "argus": step_argus,
              "finish": step_finish}


# ---------------------------------------------------------------- orchestrator

class Orchestrator:
    def __init__(self):
        self.lock = threading.Lock()
        self.thread = None
        self.state_of = {k: "pending" for k in STEP_KEYS}
        self.detail = ""
        self.overall = "idle"  # idle | running | failed | done

    def snapshot(self):
        with self.lock:
            return {
                "state": self.overall,
                "detail": self.detail,
                "steps": [{"key": k, "label": lbl, "state": self.state_of[k]} for k, lbl in STEPS],
                "argus_url": "http://%s:8081/" % primary_ip(),
            }

    def running(self):
        with self.lock:
            return self.overall == "running"

    def start(self, fresh=False):
        with self.lock:
            if self.overall == "running":
                return
            self.overall = "running"
            self.detail = ""
        if fresh:
            st = read_json(STATE_PATH)
            st["steps_done"] = []
            write_json(STATE_PATH, st)
        self.thread = threading.Thread(target=self._run, daemon=True)
        self.thread.start()

    def _run(self):
        cfg = read_json(CONFIG_PATH)
        state = read_json(STATE_PATH)
        done = set(state.get("steps_done") or [])
        with self.lock:
            for k in STEP_KEYS:
                self.state_of[k] = "done" if k in done else "pending"
        for key, label in STEPS:
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
            state["steps_done"] = [k for k in STEP_KEYS if k in done]
            write_json(STATE_PATH, state)
            with self.lock:
                self.state_of[key] = "done"
        with self.lock:
            self.overall = "done"
        print("argus-core-firstboot: setup complete", flush=True)


ORCH = Orchestrator()


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
  .vm b { color: var(--muted); font-weight: 600; }
"""

LOGO = ('<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true">'
        '<circle cx="12" cy="12" r="2"/><path d="M16.2 7.8a6 6 0 0 1 0 8.4M7.8 16.2a6 6 0 0 1 0-8.4M19 5a10 10 0 0 1 0 14M5 19A10 10 0 0 1 5 5"/></svg>')

NOSCRIPT_REFRESH = "<noscript><meta http-equiv=refresh content=3></noscript>"


def vm_identity():
    name = sh("hostname").strip()
    # Skip loopback and the Docker default bridge (172.17.x) - the footer should show the LAN address.
    ips = [ip for ip in sh("hostname", "-I").split()
           if not ip.startswith("127.") and not ip.startswith("172.17.")]
    parts = [html.escape(p) for p in ([name] if name else []) + ips[:2]]
    return " · ".join(parts)


def page(body, head_extra=""):
    ident = vm_identity()
    foot = f"<div class=vm>This VM: <b>{ident}</b></div>" if ident else ""
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


def form_page(error="", vals=None):
    v = vals or {}
    ip = primary_ip()
    d = lambda key, default="": html.escape(v.get(key) or default)  # noqa: E731
    err = f'<p class="err">{html.escape(error)}</p>' if error else ""
    body = f"""
  <h1>Set up your monitoring core</h1>
  <p class="hint">One form configures everything on this VM: Zabbix, its database, the probe
  enrollment PKI, and Argus. Nothing here has defaults left over from the image &mdash; every
  credential is created now, for this instance only.</p>
  {err}
  <form method="post" action="/">
    <h2>System</h2>
    <div class="row"><div>
      <label for="h">Hostname</label>
      <input id="h" name="hostname" value="{d('hostname', 'argus-core')}" required>
    </div><div>
      <label for="k">Console keyboard layout</label>
      <select id="k" name="keymap">{options_html(KEYMAPS, v.get('keymap') or 'us')}</select>
    </div></div>
    <label for="tz">Timezone</label>
    <select id="tz" name="timezone">{options_html([(t, t) for t in TIMEZONES], v.get('timezone') or 'UTC')}</select>
    <div class="sub">Used for the system clock, Zabbix, and Argus notifications (changeable later in Argus &rarr; Settings).</div>

    <h2>Administrator</h2>
    <label for="e">Email</label>
    <input id="e" name="email" type="email" placeholder="you@example.com" value="{d('email')}" required>
    <div class="sub">Your Argus sign-in.</div>
    <label for="p1">Administrator password</label>
    <input id="p1" name="password" type="password" minlength="8" required>
    <label for="p2">Confirm password</label>
    <input id="p2" name="password2" type="password" minlength="8" required>
    <div class="sub">Used for the Argus admin, the Zabbix <b>Admin</b> user, and the local Linux user
    &mdash; rotate any of them independently later. The database password and the Zabbix API token are
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
      <input id="pu" name="public_url" value="{d('public_url', 'http://%s:8081' % ip)}">
      <div class="sub">External base URL for links in notifications; set your reverse-proxy FQDN here if
      you have one. Changeable later in Argus &rarr; Settings.</div>
    </details>

    <button type="submit">Set up this core</button>
  </form>
"""
    return page(body)


def render_steps(snap):
    items = []
    for s in snap["steps"]:
        cls = {"done": "done", "active": "active", "fail": "fail"}.get(s["state"], "")
        ic = {"done": "✓", "active": "…", "fail": "✕"}.get(s["state"], "•")
        items.append(f'<li class="{cls}"><span class="ic">{ic}</span> {html.escape(s["label"])}</li>')
    return "\n".join(items)


def progress_page(snap):
    if snap["state"] == "failed":
        result = ('<div class="result bad" id="result">Setup failed: ' + html.escape(snap["detail"]) +
                  '</div><form method="post" action="/retry"><button type="submit">Retry from the failed step</button></form>'
                  '<form method="get" action="/"><input type="hidden" name="edit" value="1">'
                  '<button class="ghost" type="submit">Change the answers</button></form>')
    elif snap["state"] == "done":
        result = success_fragment()
    else:
        result = '<div class="result" id="result"></div>'
    body = f"""
  <h1>Setting up your monitoring core…</h1>
  <p class="hint">This takes a few minutes (the database schema import is the long step). Keep this
  page open &mdash; it follows the progress live.</p>
  <ul class="steps" id="steps">
{render_steps(snap)}
  </ul>
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
      s.steps.forEach((st, i) => {{
        lis[i].className = st.state === "pending" ? "" : st.state;
        lis[i].querySelector(".ic").textContent = icons[st.state] || "•";
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
    <b>Argus:</b> <a href="http://{ip}:8081/">http://{ip}:8081/</a> &mdash; sign in as <b>{email}</b>.<br>
    <b>Zabbix UI</b> (engine room, rarely needed): <a href="http://{ip}:8080/">http://{ip}:8080/</a> &mdash; user <b>Admin</b>.<br><br>
    Next steps: add your first probe (Argus &rarr; <b>Probes</b> &rarr; Add probe), and take a
    hypervisor snapshot of this VM. From now on <a href="http://{ip}/">http://{ip}/</a> redirects to
    Argus &mdash; this setup page is gone after you leave it.
  </div>
"""


def done_page():
    return page("<h1>Setup complete</h1>" + success_fragment())


# ---------------------------------------------------------------- validation + HTTP

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
    if not os.path.exists("/usr/share/zoneinfo/" + vals["timezone"]) or ".." in vals["timezone"]:
        return None, "Unknown timezone."
    if not re.fullmatch(r"[^@\s]+@[^@\s]+\.[^@\s]+", vals["email"]):
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
        if u.scheme not in ("http", "https") or not u.netloc:
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


class Handler(BaseHTTPRequestHandler):
    def _send(self, body, status=200, ctype="text/html; charset=utf-8"):
        data = body.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

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
            self._send(json.dumps(ORCH.snapshot()), ctype="application/json")
            return
        if setup_done():
            self._send(done_page())
            return
        if self.path.startswith("/?edit") and not ORCH.running():
            cfg = read_json(CONFIG_PATH)
            safe = {k: cfg.get(k, "") for k in ("hostname", "keymap", "timezone", "email",
                                                "linux_user", "public_url")}
            self._send(form_page(vals=safe))
            return
        if os.path.exists(CONFIG_PATH):
            self._send(progress_page(ORCH.snapshot()))
            return
        self._send(form_page())

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0) or 0)
        form = parse_qs(self.rfile.read(length).decode("utf-8"), keep_blank_values=True)
        if setup_done():
            self._redirect("/")
            return
        if self.path.startswith("/retry"):
            if os.path.exists(CONFIG_PATH) and not ORCH.running():
                ORCH.start(fresh=False)
            self._redirect("/")
            return
        if ORCH.running():
            self._redirect("/")
            return
        cfg, err = validate(form)
        if not cfg:
            keep = {k: form.get(k, [""])[0] for k in ("hostname", "keymap", "timezone", "email",
                                                      "linux_user", "public_url")}
            self._send(form_page(error=err, vals=keep), status=400)
            return
        write_json(CONFIG_PATH, cfg)
        ORCH.start(fresh=True)  # edited answers rerun every (idempotent) step against the new values
        self._redirect("/")

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
    if os.path.exists(CONFIG_PATH):
        print("argus-core-firstboot: resuming an interrupted setup", flush=True)
        ORCH.start(fresh=False)
    threading.Thread(target=monitor, args=(httpd,), daemon=True).start()
    print("argus-core-firstboot: serving setup page on http://%s:%d/" % LISTEN, flush=True)
    httpd.serve_forever()
    # We only get here after monitor() saw setup-done: retire this service and let nginx take :80
    # (the finish step already wrote the redirect site).
    subprocess.run(["systemctl", "disable", FIRSTBOOT_SERVICE], check=False)
    subprocess.run(["systemctl", "reload-or-restart", "nginx"], check=False)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

# Phase 0 - Step-by-Step Checklist (command-forward)

Rule: each `RUN` step is **one line** - copy it, paste it, run it. Do them in order.
`BROWSER` / `EDIT` steps are done by hand and say exactly what to do.
Replace `YOURPASS` with your real DB password (same one every time it appears).

Values already set: core IP = **10.0.0.10** · unRAID appdata = `/mnt/user/appdata`

---

## Stage A - PKI (on the core VM, 10.0.0.10)

A1 · RUN - generate the CA + all certs:
```bash
cd ~/deploy/pki && chmod +x gen-certs.sh && ./gen-certs.sh
```

A2 · RUN - verify the certs exist:
```bash
ls -l ~/deploy/pki/out
```

A3 · MANUAL - back up `~/deploy/pki/out/ca.key` somewhere offline. Never copy it to a probe.

---

## Stage B - Install & configure the core (on the core VM)

B1 · RUN - make the installer executable:
```bash
cd ~/deploy/core && chmod +x setup-core.sh
```

B2 · RUN - install Zabbix + PostgreSQL + TimescaleDB (use your real password):
```bash
sudo DBPASS='YOURPASS' ./setup-core.sh
```

B3 · RUN - append the TLS + tuning settings to the server config:
```bash
cat ~/deploy/core/zabbix_server.conf.snippet | sudo tee -a /etc/zabbix/zabbix_server.conf
```

B4 · RUN - create the cert folder, copy the core certs, fix ownership + key permission (one line):
```bash
sudo mkdir -p /etc/zabbix/certs && sudo cp ~/deploy/pki/out/{ca.crt,zabbix-core.crt,zabbix-core.key} /etc/zabbix/certs/ && sudo chown -R zabbix:zabbix /etc/zabbix/certs && sudo chmod 600 /etc/zabbix/certs/zabbix-core.key
```

B5 · RUN - verify the certs are in place:
```bash
sudo ls -l /etc/zabbix/certs
```

B6 · EDIT - open the nginx config:
```bash
sudo nano /etc/zabbix/nginx.conf
```
Inside, remove the `#` in front of these two lines and set the name, then save (Ctrl+O, Enter, Ctrl+X):
```
        listen          8080;
        server_name     monitoring.example.com;
```

B7 · RUN - enable and start all services:
```bash
sudo systemctl enable --now zabbix-server zabbix-agent2 nginx php8.4-fpm
```

B8 · RUN - restart to apply the config changes:
```bash
sudo systemctl restart zabbix-server nginx php8.4-fpm
```

B9 · RUN - check the server log is clean (look for "started", no DB/TLS errors; Ctrl+C to exit):
```bash
sudo tail -n 50 -f /var/log/zabbix/zabbix_server.log
```
> If the server refuses to start with **"TimescaleDB version is too new / Unsupported DB!"**,
> the `AllowUnsupportedDBVersions=1` line in the snippet handles it. If you appended the
> snippet before that line existed, add it once:
> `echo 'AllowUnsupportedDBVersions=1' | sudo tee -a /etc/zabbix/zabbix_server.conf` then restart.

B10 · BROWSER - open `http://10.0.0.10:8080` and complete the setup wizard.
DB connection step - set exactly these:
- Database type: **PostgreSQL**
- Database host: **localhost**
- Database port: **0**  (0 = use default 5432 - leave it at 0)
- Database name: **zabbix**
- Database schema: **(leave empty)**
- Store credentials in: **Plain text**
- User: **zabbix**
- Password: **YOURPASS** (same one from B2)
- Database TLS encryption: **UNCHECKED** ← local DB has no TLS; leaving it checked makes the test fail
Settings step:
- Zabbix server name: **cosmetic label only** (shown in the tab title / top-right; not an FQDN,
  not used for connections). This instance is named **`Monitoring`**.
- Set your timezone
Then: finish · log in as `Admin` / `zabbix` → **change the admin password immediately**

B11 · BROWSER - set retention: Administration → Housekeeping → enable "Override item history/trend period" → History `30d`, Trends `730d`.
- Also enable **compression** (Compress records older than `7d`).
- If Housekeeping warns **"Unsupported TimescaleDB ... should not be higher than 2.28"**, the
  installed TimescaleDB is too new and Zabbix won't manage compression. Fix by pinning
  TimescaleDB to 2.28 (the updated `setup-core.sh` does this automatically on fresh installs;
  to fix a live box, downgrade TS to the newest 2.28.x, `apt-mark hold` it, and recreate the
  empty `zabbix` DB + re-import schema + timescaledb schema - note this resets the admin login
  to `Admin`/`zabbix`).

B12 · MANUAL - firewall: allow inbound **TCP 10051** to 10.0.0.10 from the sites (Site Magic), and **TCP 8080** from your LAN only.

---

## Stage C - Register the site1 proxy (BROWSER, in the Zabbix UI)

> Zabbix 7.0 note: Proxies live under **Administration → Proxies** (NOT Data collection).

C1 · Administration → Proxies → **Create proxy**
- Proxy name: `proxy-site1`
- Mode: **Active**
- Encryption tab → Connections from proxy: **Certificate**
  - Issuer:  `CN=Monitoring Core CA`
  - Subject: `CN=proxy-site1`
- Save

---

## Stage D - Deploy the site1 probe (on the site1 unRAID box)

D1 · RUN (on unRAID) - create the folders:
```bash
mkdir -p /mnt/user/appdata/zbx-proxy-site1/certs /mnt/user/appdata/zbx-proxy-site1/data
```

D2 · MANUAL - copy **only site1's** certs into `/mnt/user/appdata/zbx-proxy-site1/certs/`:
`ca.crt`, `proxy-site1.crt`, `proxy-site1.key`  (do NOT copy `ca.key` or other sites' keys).

D3 · Deploy the container - pick ONE:

**Option A - unRAID template (via the UI):**
1. Copy the template file into unRAID's user-template folder (over SSH on the unRAID box, with the XML already on the box):
```bash
cp zabbix-proxy-site1.xml /boot/config/plugins/dockerMan/templates-user/
```
2. Web UI → **Docker** tab → **Add Container** → **Template** dropdown → *User templates* → **Zabbix-Proxy-site1**.
3. Set **Zabbix Server (core)** = `10.0.0.10`, confirm the cert/data paths, click **Apply**.
   (There is no "import" command - dropping the XML in the folder above is the import.)

**Option B - script (no template, simplest):** if you copied `deploy/` to the unRAID box:
```bash
bash ~/deploy/probe/run-probe.sh site1 10.0.0.10 /mnt/user/appdata
```

D4 · RUN (on unRAID) - watch the logs (want a connection to the core, no TLS errors; Ctrl+C to exit):
```bash
docker logs -f zbx-proxy-site1
```

---

## Stage E - Prove the pipeline (BROWSER + one command)

E1 · BROWSER - Data collection → Proxies → `proxy-site1` shows **Last seen** ticking (green).

E2 · BROWSER - Data collection → Hosts → Create host:
- Host name: `gateway`
- Templates: **`ICMP Ping`** (provides the ping / loss / response-time items)
- Host groups: e.g. `site1/Network` (required - red asterisk)
- Interfaces → Add → **SNMP**, IP = gateway IP (e.g. `10.0.0.1`), port `161`, Connect to **IP**
  - For the ping test the interface type/port are irrelevant (ICMP uses only the IP), but **SNMP/161**
    is the correct type since this device will be SNMP-monitored later. Avoid Agent/10050 (implies a
    Zabbix agent on the device - there isn't one).
- Monitored by → **Proxy** → `proxy-site1`
- Add
- Expected in Monitoring → Latest data within ~1-2 min: `ICMP ping = Up (1)`, `ICMP loss = 0 %`,
  `ICMP response time ≈ Xms`.

E3 · BROWSER - Monitoring → Latest data → confirm the ping value arrives.

E4 · RUN (buffer test, on the core) - stop the server briefly, wait ~2 min, start it, confirm the gap backfills:
```bash
sudo systemctl stop zabbix-server; sleep 120; sudo systemctl start zabbix-server
```

---

## Done when
- `proxy-site1` is green and last-seen ticks every minute.
- The test host's ping shows in Latest data.
- The buffer test backfilled after reconnect.

Report back: repo fetch OK on trixie? proxy green? any TLS errors in the logs?
Then → Phase 1 (auth + PKI/enrollment backend).

# Ugreen NAS (Zabbix agent)

Ugreen's **UGOS** exposes **no SNMP**, so the Ugreen device class doesn't use the SNMP path - it
uses **Zabbix agent 2, run in a Docker container on the NAS itself** (UGOS ships Docker as an app).
The site proxy then **polls the agent passively** on `:10050`, exactly like it polls an SNMP device
on `:161` - the agent never has to reach out, and nothing extra is baked into the probe image.

1. In Argus, **Add device → Ugreen (Zabbix agent)** with the NAS's IP. This creates the host with
   an agent interface on `:10050` and attaches the *Argus NAS by Zabbix agent* template. Note the
   **host name** you give it.
2. On the NAS, run the block below. The `cat` writes a small config with two custom readings - CPU
   temperature, and per-disk SMART temperature read **without waking the disk** (`smartctl -n
   standby`); the rest starts the agent:
   ```bash
   mkdir -p /volume1/docker/argus-agent
   cat > /volume1/docker/argus-agent/nas-agent.conf <<'EOF'
   UserParameter=ugreen.cpu.temp,for h in /sys/class/hwmon/hwmon*; do case "$(cat "$h/name" 2>/dev/null)" in coretemp|k10temp) cat "$h/temp1_input"; exit 0;; esac; done; cat /sys/class/thermal/thermal_zone*/temp 2>/dev/null | sort -rn | head -1
   UserParameter=ugreen.disk.temp[*],case "$1" in *nvme*) n= ;; *) n="-n standby" ;; esac; o=$(smartctl $n -a -jc "$1" 2>/dev/null); t=$(printf '%s' "$o" | grep -oE '"current": *[0-9]+' | head -1 | grep -oE '[0-9]+'); if [ -n "$t" ]; then echo "$t"; elif [ -n "$n" ] && printf '%s' "$o" | grep -qiE 'standby|sleep'; then echo 20; fi
   EOF
   docker run -d --name argus-agent --restart unless-stopped \
     --network host --pid host --privileged --user root \
     -e ZBX_SERVER_HOST="<SITE-PROXY-IP>" \
     -e ZBX_HOSTNAME="<the name you gave the device in Argus>" \
     -v /volume1:/volume1:ro -v /proc:/proc:ro -v /sys:/sys:ro \
     -v /volume1/docker/argus-agent/nas-agent.conf:/etc/zabbix/zabbix_agent2.d/plugins.d/nas-agent.conf:ro \
     zabbix/zabbix-agent2:alpine-7.0-latest
   ```
   > **Why `plugins.d/`?** The `zabbix/zabbix-agent2` image's config only `Include`s
   > `/etc/zabbix/zabbix_agent2.d/plugins.d/*.conf` (and `/etc/zabbix/zabbix_agentd.d/*.conf`) - **not**
   > `/etc/zabbix/zabbix_agent2.d/*.conf` itself. A UserParameter file dropped in the parent dir is
   > silently ignored (`Unknown metric`), so mount it into `plugins.d/`.
   What each part is for:
   - **`--network host`** - so the proxy can reach the agent on `:10050` (and the agent sees the
     real NICs). Required.
   - **`ZBX_SERVER_HOST=<proxy IP>`** - becomes the agent's `Server=` **allow-list**: only that
     proxy may poll it. This *is* the access control (see the PSK note below).
   - **`--pid host` + the `/proc`, `/sys` mounts** - so CPU / memory readings are the host's, not the
     container's, and the CPU-temp UserParameter can read the coretemp/k10temp sensor from `/sys`.
   - **`-v /volume1:/volume1:ro`** - each data volume you want disk-usage for, mounted at its real
     path (add `/volume2`, ... if you have more). The class filters filesystem discovery down to the
     `volumeN` mounts, so the container's own filesystems don't clutter the Disk section.
   - **`--privileged --user root`** - so `smartctl` can read the raw disks for **per-disk SMART
     temperatures** (it needs root + raw access). `smartmontools` is already in the stock agent2 image.
   - **the `nas-agent.conf` mount** - the two UserParameters: `ugreen.cpu.temp` (CPU package temp from
     coretemp/k10temp, else the hottest thermal zone) and `ugreen.disk.temp` (per-disk SMART temp).
     Skip the `cat`/`-v` lines if you don't want the temperatures; everything else still works.

CPU utilization, memory, filesystems, NICs and uptime use the **same item keys** as the SNMP
classes, so they render identically. Memory used-% is computed from **MemAvailable**, so page cache
counts as free (matching what UGOS shows), not as used. Disk temperatures group into the same **Disk
temperatures** overlay chart as unRAID, and CPU temperature is a standalone Temperature sensor
(*running hot* ≥ `{$CPU.TEMP.WARN}` 75 °C, *overheating* ≥ `{$CPU.TEMP.HIGH}` 85 °C).

**Disk temperature thresholds follow the disk type.** The SMART discovery reports each disk's type
(`{#DISKTYPE}` = `hdd` / `ssd` / `nvme`), and the trigger picks the matching threshold, since SSDs
tolerate more heat than spinning disks:

| Disk type | Warning (`{$DISK.TEMP.WARN...}`) | High (`{$DISK.TEMP.HIGH...}`) |
|---|---|---|
| HDD (default) | 40 °C | 45 °C |
| SSD (`:ssd`) | 65 °C | 75 °C |
| NVMe (`:nvme`) | 65 °C | 75 °C |

Override any of these per host in **host settings**, or globally on the template - e.g. set
`{$DISK.TEMP.WARN:ssd}` to change the SSD warning level. A disk whose type is unknown falls back to
the HDD default.

> **Spun-down disks.** For a spinning disk `ugreen.disk.temp` uses `smartctl -n standby`, so a parked
> one is **not woken** - it reports a fixed **20 °C standby sentinel** (like the unRAID class), a
> distinct low flat line = *parked* rather than a stale warm value, and any heat alert clears; an awake
> disk reports its real temperature. An **NVMe never spins down**, so it's always read normally (no
> sentinel). The item polls slowly (every 10 min) to avoid keeping an idle disk awake; if your drives
> still aren't spinning down, raise that item's interval past your NAS's disk-standby timeout (SMART
> reads on some drives reset the idle timer).

> **Encryption (PSK).** The link is unencrypted by default; the `Server=` allow-list only checks the
> source IP. On a trusted site LAN that's usually fine. To encrypt + mutually authenticate, add a
> **PSK** on the agent (`TLSConnect`/`TLSAccept=psk`, `TLSPSKIdentity`, `TLSPSKFile`) and set the
> matching TLS fields on the Zabbix host - no template change needed.

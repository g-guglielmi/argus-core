# Device monitoring guides

One guide per device class Argus can add today. Adding any of them is **Add device** in Argus - pick the
class, give the address, and (for API classes) the credentials the form asks for. These guides cover
what each class monitors, where to get its credentials, any setup needed **on the device** first, and
the per-host tunables.

## By how it is monitored

**SNMP** (enable SNMP on the device, the proxy polls it)

| Class | Guide |
|---|---|
| Linux (SNMP) | [linux.md](linux.md) |
| Windows (SNMP) - incl. opt-in service monitoring | [windows.md](windows.md) |
| unRAID (SNMP) - disk + CPU temperatures | [unraid.md](unraid.md) |

**Zabbix agent** (agent container on the device, proxy polls it passively)

| Class | Guide |
|---|---|
| Ugreen NAS (Zabbix agent) | [ugreen.md](ugreen.md) |

**HTTP / API** (the class reads a vendor API with a key or token)

| Class | Guide |
|---|---|
| UniFi Switch / Gateway / Access Point / OS Console | [unifi.md](unifi.md) |
| AdGuard Home | [dns.md](dns.md) |
| Home Assistant | [home-assistant.md](home-assistant.md) |
| UPS (NUT via PeaNUT) | [ups.md](ups.md) |

**Collector** (the proxy runs a check or protocol client - nothing on the device)

| Class | Guide |
|---|---|
| DNS server (per-name resolve) | [dns.md](dns.md) |
| UPS (NUT, direct) | [ups.md](ups.md) |
| XCP-NG (XAPI) - pool, hypervisors, opt-in VMs | [xcpng.md](xcpng.md) |

**Agentless / SSH** (the proxy logs into the device over SSH - no agent, no SNMP)

| Class | Guide |
|---|---|
| Linux (SSH, agentless) | [linux-ssh.md](linux-ssh.md) |

**Base**

- **Ping only** - no setup and no guide: **Add device → Ping only** gives ICMP reachability/latency.
  Every class also gets Base Ping automatically on top of its own metrics.

**Automatic**

- **Probe health** - nothing to add: Argus gives every probe a **Probe <site>** host in its site
  group, monitored by that probe and carrying the **Argus Probe Health** template. It shows whether
  the probe is reaching the core (alerting when it stops), values waiting to be sent, items running
  late, cache use and how busy its processes are. It's linked from each probe's **Health** cell on
  the Probes page, and deleted along with the probe. Its thresholds are on the Thresholds screen.
  Argus also reads its process load to size the probe's Zabbix process counts (Probes page,
  **Processes**; Settings, **Probes**, Process autoscaling).

## Planned (TBD)

These classes are on the roadmap and **cannot be added yet** - they have no template and do not appear
in **Add device**. Each gets its own guide when it ships. Order and timing are not a commitment; the
list is here so you can see where the catalog is heading. See [../DESIGN.md](../DESIGN.md) section 5 and
[../../ROADMAP.md](../../ROADMAP.md) for the authoritative plan.

**SNMP (planned)**

| Class | Expected to monitor |
|---|---|
| HPE Aruba CX | CPU, memory, temperature, PSU/fan, per-port traffic, PoE |
| Aruba InstantOn 1960 | CPU, memory, per-port traffic, PoE |
| Sophos XGS | CPU, memory, disk, interfaces, HA, live users, VPN |
| Citrix NetScaler | CPU, memory, throughput, vserver state/health, SSL, HA |
| QNAP | CPU, memory, volume/disk, temperature, fan, RAID, SMART |
| Libraesva ESG | host CPU/RAM/disk, mail queue, admin certificate |
| Hyper-V | host CPU/RAM/disk/net/uptime (per-VM state needs WMI/agent - a known gap) |

**HTTP / API (planned)**

| Class | Expected to monitor |
|---|---|
| Nutanix AHV | cluster / host / VM CPU/mem/storage and VM state (Prism REST); one endpoint spawns child hosts |
| Citrix farm | registered-machine count/state, failed logons, sessions, load (Monitor OData) |

**Native VMware (planned)**

| Class | Expected to monitor |
|---|---|
| vSphere ESXi + vCenter | hypervisor CPU/mem, datastore, per-VM state/CPU/mem; register vCenter once, LLD spawns the child hosts |

## Per-host options

Class tunables (the Windows service filter, API credentials, and the like) can be set when you add the
device **and** changed later in **host settings** (the settings panel on the host row) under the class
options section. That same dialog also has:

- a **Thresholds** section for per-device alert-threshold overrides (fleet-wide defaults live on the
  admin **Thresholds** screen - see [../thresholds.md](../thresholds.md));
- a **Master sensor** section - the sensor whose failure means the whole device is down (its **ICMP
  ping** by default; the Probe host's is its reporting sensor). While it's down, the host's other
  sensors don't notify, so an unreachable device alerts once, and the Overview and the Error and
  Warning lists show only the master, with the rest behind **Show held**. Pick another sensor, or
  **None** to never hold this host's alerts. Separately, while a probe isn't reporting, every device at its site is held;
- a **Sensor order** section;
- a **Push sensors** section - jobs that report their runs to Argus at their own URL (a backup, a cron
  job, a scheduled task): a failed run, a late one and a missed one alert. See
  [../push-sensors.md](../push-sensors.md);
- an **Add-ons** section - optional Argus checks you can layer on a host at any time (not just when
  adding it): the **HTTP/HTTPS endpoint** (a real request to each URL, see below), **DNS
  resolution** (resolve names against the host) and **TCP ports** (does each listed port accept a
  connection, see below). Toggling one on links its template and lets you set its options; toggling
  off removes its sensors;
- a **Change class** control (admin) - switch a host to a different device class in place, without
  deleting and re-adding it. It swaps the class's templates (keeping Base Ping + add-ons), keeps
  history for any template the old and new class share, adds the new class's interface type if the
  host lacks it, and collects the new class's credentials. Sensors from templates only in the old
  class are removed.

**TCP ports add-on.** For a port that isn't a web page: a mail server's SMTP, a Windows box's RDP, a
database, a firewall's admin port. List the ports under **Ports**, each `port` or `name:port`
(`SMTP:25, RDP:3389, 8443`, at most 32); a port without a name shows its usual service name
(`3389` reads **RDP (3389)**). Once a minute the host's probe opens a plain TCP connection to every
port at once (nothing is sent) and each port becomes its own sensor under **TCP**: its connect time,
with a red band while it doesn't answer. A port that refuses or ignores the connection reads **not
answering**, with why (`connection refused: nothing listens on 3389`, `no answer within 3 s (a
firewall drops it, or the host is down)`, `no route to host`). Alerts: **Port RDP (3389) is not
answering** (High, after 3 checks), and slow to connect (`{$TCP.TIME.WARN}` 0.5 s, `{$TCP.TIME.HIGH}`
1 s). **Timeout** (default 3 s) is how long a port has to answer. Taking a port out of the list deletes
its sensor, and any open problem of it, at the next check.

**HTTP/HTTPS endpoint add-on.** A real request to each web page you list, from the host's probe, once
a minute. Under **URLs**, **+ Add URL** adds a row, up to 16; with no rows, it checks the host itself on
**Scheme** and **Port**. A row takes:

- the URL: a full one (`https://portal.example.com/app`, any host, subdomain, port or path), a host
  without a scheme (`10.7.0.2`, `10.7.0.2:8443/admin`, which gets **Scheme**), or a path on the host's
  own address (`/login`). The port goes in the URL;
- its **Certificate** check, when it should differ from the add-on's (see below);
- text the page must contain, or must not (**Page doesn't contain**), case-insensitive. Optional.

A mistake in a URL shows under its row as you type. Each URL becomes its own sensor under **Web**: its response time, with a red band while it
is down, and for https the days its certificate has left. A URL is up when it answers with one of the
**Accepted status codes** (`200-299` by default, after following up to 5 redirects), has its text, and
presents a trusted certificate for its name. When it isn't, it says why (`returned 502 Bad Gateway`,
`the page does not contain "Welcome"`, `the certificate is not trusted: self-signed certificate`, `the
certificate is for another name`, `no answer within 10 s`). **Certificate** sets how the certificate
is checked, for every URL that doesn't set its own: `verify` wants one a known CA issued, for the URL's name and not expired; `self-signed`
also takes one no CA vouches for (the device's own, or one from a private CA), still not expired and,
when the URL uses a name, for that name. A URL by IP address (a blank list on a host added by IP) isn't
name-checked, since a device's own certificate rarely lists its address. `ignore` takes any. The
expiry counts in every mode. Taking a URL out of the list deletes its sensors, and any
open problem of theirs, at the next check. If the probe can't run the check at all (it doesn't have
`argus_http.py` yet), the host shows an **HTTP checks** sensor under **Web** saying so, and it alerts.
Alerts: **portal.example.com/app is down** (High, after 3 checks), slow (`{$HTTP.RESPONSE.WARN}` 1 s,
`{$HTTP.RESPONSE.HIGH}` 3 s) and the certificate expiring (`{$HTTP.CERT.WARN}` 21 days,
`{$HTTP.CERT.HIGH}` 7 days); all four thresholds are on the Thresholds screen. **Timeout** (default 10 s)
is how long each page has to answer.

**Why a sensor isn't reading.** Hover (or tap) a `not supported` or `Not reachable` value anywhere in
Argus to see the reason: Zabbix's error, or what the device's collector reported (a refused API key,
an unknown site, `Permission denied (publickey)`, `ERR UNKNOWN-UPS`). Alerts carry the same text.
Every Argus template reports its reasons this way ([../DESIGN.md](../DESIGN.md) section 5, "Every
template says why").

For the full device-class catalog (including classes still on the roadmap) and what each one collects,
see [../DESIGN.md](../DESIGN.md) section 5.

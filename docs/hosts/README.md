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
  sensors don't notify, so an unreachable device alerts once. Pick another sensor, or **None** to never
  hold this host's alerts. Separately, while a probe isn't reporting, every device at its site is held;
- a **Sensor order** section;
- an **Add-ons** section - optional Argus checks you can layer on a host at any time (not just when
  adding it): the **HTTP/HTTPS endpoint** (reachability + response time on a web port) and **DNS
  resolution** (resolve names against the host). Toggling one on links its template and lets you set
  its options; toggling off removes its sensors;
- a **Change class** control (admin) - switch a host to a different device class in place, without
  deleting and re-adding it. It swaps the class's templates (keeping Base Ping + add-ons), keeps
  history for any template the old and new class share, adds the new class's interface type if the
  host lacks it, and collects the new class's credentials. Sensors from templates only in the old
  class are removed.

**Why a sensor isn't reading.** Hover (or tap) a `not supported` or `Not reachable` value anywhere in
Argus to see the reason: Zabbix's error, or what the device's collector reported (a refused API key,
an unknown site, `Permission denied (publickey)`, `ERR UNKNOWN-UPS`). Alerts carry the same text.
Every Argus template reports its reasons this way ([../DESIGN.md](../DESIGN.md) section 5, "Every
template says why").

For the full device-class catalog (including classes still on the roadmap) and what each one collects,
see [../DESIGN.md](../DESIGN.md) section 5.

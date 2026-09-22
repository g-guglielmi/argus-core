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
options section. That same dialog has a **Thresholds** section for per-device alert-threshold overrides
and a **Sensor order** section; fleet-wide threshold defaults live on the admin **Thresholds** screen.
See [../thresholds.md](../thresholds.md).

For the full device-class catalog (including classes still on the roadmap) and what each one collects,
see [../DESIGN.md](../DESIGN.md) section 5.

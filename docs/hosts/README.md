# Device monitoring guides

One guide per device class Argus can add today. Adding any of them is **Add device** in Argus - pick the
class, give the address, and (for API classes) the credentials the form asks for. These guides cover
what each class monitors, where to get its credentials, any setup needed **on the device** first, and
the per-host tunables.

## By how it is monitored

**SNMP** (enable SNMP on the device, the proxy polls it)

| Class | Guide |
|---|---|
| Generic Linux (SNMP) | [linux.md](linux.md) |
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

**Base**

- **Ping only** - no setup and no guide: **Add device → Ping only** gives ICMP reachability/latency.
  Every class also gets Base Ping automatically on top of its own metrics.

## Per-host options

Class tunables (thresholds, the Windows service filter, API credentials) can be set when you add the
device **and** changed later in **host settings** (the settings panel on the host row) under the class
options section.

For the full device-class catalog (including classes still on the roadmap) and what each one collects,
see [../DESIGN.md](../DESIGN.md) section 5.

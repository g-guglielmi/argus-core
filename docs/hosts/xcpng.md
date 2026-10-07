# XCP-NG (XAPI)

The **XCP-NG (XAPI)** class monitors an XCP-NG pool through XAPI: pool health, every hypervisor in
the pool (CPU, memory, uptime, version, liveness, optional CPU temperature) and - opt-in - the
virtual machines. Add it with **Add device → XCP-NG (XAPI)**. Nothing is installed on the
hypervisor: the proxy/core runs a small collector (`argus_xcpng.py`, an external check) that logs
into the pool master over XML-RPC/HTTPS once per poll and reads everything in one session.

## Adding a pool

- **Address**: the **pool master's IP**. A single host is its own master; if you point at a slave,
  the collector follows XAPI's redirect to the master automatically. One Argus device covers the
  whole pool - each member appears as its own set of sensors, named after the hypervisor.
- **Certificate check** (`{$XCP.TLS}`, host settings): XCP-NG hosts run self-signed XAPI
  certificates, so the default **pin** trusts the certificate on first contact and remembers its
  SHA-256 on the collector (the probe or the core, under `/var/lib/zabbix/argus-pins/`, one file per
  address); a different certificate later is refused and the host reads as down, with the reason
  (pinned and presented fingerprints, the pin file) shown on hover and in the alert. After a
  deliberate certificate change, delete that pin file (or switch
  to **ignore** once, then back). **verify** checks against the collector's CA store instead; **ignore**
  checks nothing.
- **Credentials**: the XAPI login. On a stock XCP-NG that is **`root`** and the host root password
  (local XAPI accounts are root-only; only pools with external/AD authentication have other users).
  The password is stored as a Zabbix **secret macro** (write-only after saving). Zabbix hands it to
  the collector as a command-line argument (the only way an external check receives a value); the
  collector wipes its own command line as soon as it has read it, so it shows in `ps` only during
  start-up.
- **VM monitoring**: a dropdown, off by default (see below).

> Multi-host pools are fully coded (the collector enumerates every member and reads each host's
> RRD feed) but so far lab-verified only on single-host pools.

## What it monitors

**Pool**: name, HA enabled (hidden while off), members live vs total (hidden on single-host pools,
with an alert when a member drops), VMs running vs defined - the VM counters work even with VM
monitoring off and lead the Virtual machines section.

**Per hypervisor**: CPU utilization (from the host's RRD feed - XAPI itself stopped exposing live
CPU long ago), used/total memory and %, uptime, XCP-NG version, liveness. Alerts: member down,
high CPU (warning `{$XCP.CPU.UTIL.WARN}` 85%, high `{$XCP.CPU.UTIL.HIGH}` 95%), high memory (warning
`{$XCP.MEM.WARN}` 85%, high `{$XCP.MEM.HIGH}` 95%), unreachable XAPI and rejected credentials.

## VM monitoring (optional)

The **VM monitoring** dropdown (also changeable later in host settings under the class options)
selects `{$XCP.VM.MODE}`:

| Mode | Per-VM sensors |
|---|---|
| **off** (default) | none - only the pool-level VM counters |
| **state** | power state per VM (Running / Halted / Paused / Suspended), with a *not running* warning |
| **full** | state + CPU %, memory, disk read/write, network in/out per running VM |

Templates, snapshots and control domains are always excluded. Memory-used inside the guest needs
the XCP-NG **guest tools** in the VM; without them only the assigned total is reported. The *not
running* alert can be closed manually for an intentionally stopped VM - it re-fires only after the
VM runs and stops again.

**Ignoring VMs**: host settings shows the discovered VMs as a checklist under **Monitored VMs** -
untick the ones that should not be monitored (parked templates, scratch VMs) and they disappear
from the per-VM sensors **and** the running/defined counts on the next poll; any open *not
running* warning for them is closed automatically when you save. An ignored VM stays in the list
(struck through) so it can be re-enabled later. Under the hood this is the `{$XCP.VM.IGNORE}`
macro (comma-separated names). Sensors of a VM that leaves the list - ignored, deleted, or the
mode turned down - are hidden from the curated view right away, disabled in Zabbix, and deleted
after 7 days.

## VMs under their hypervisor

Whatever **VM monitoring** is set to, every poll also lists each VM's network cards (MAC addresses)
and, when the XCP-NG **guest tools** report them, its IPv4 addresses, with the hypervisor it runs on.
Argus uses it to place the hosts that are those VMs under their hypervisor (their **upstream
device**): on the site's map, on the Device tab's path, and for alerts, so while the hypervisor is
down its VMs' alerts wait and its own alert names them. Nothing to set up:

- A VM is matched to an Argus host by its guest address, or by its MAC through the UniFi controller's
  client lists (MAC to address) or what a discovery scan saw. A VM without guest tools is still found
  by its MAC once it is on the network.
- On a multi-host pool, a VM hangs off the member it runs on when that member is an Argus host (found
  by its address); a live migration moves it once the new answer has held for 15 minutes.
- When XAPI doesn't answer (the hypervisor is down, or the login fails), the last list is kept, so the
  VMs stay behind their hypervisor while it is down.
- Ignored VMs (the Monitored VMs list) are placed too: placing a host monitors nothing.

A host can still be set to another upstream device, or to none, in its settings.

## CPU temperature (optional)

XAPI does not expose host temperatures, so this class reads them through a tiny **XAPI plugin** on
dom0 - same session, no extra port, no snmpd. Hosts without the plugin simply have no temperature
sensor; nothing else changes.

Install [`xcpng-temp.py`](xcpng-temp.py) **on each hypervisor**:

```
scp xcpng-temp.py root@<hypervisor>:/etc/xapi.d/plugins/argus-temp
ssh root@<hypervisor> chmod +x /etc/xapi.d/plugins/argus-temp
```

The file name **must** be `argus-temp` (the collector calls the plugin by that name). On XCP-NG
8.2, change the first line of the file to `#!/usr/bin/python2` (8.3 keeps the `python3` shebang).
The plugin reads the CPU package temperature from the kernel hwmon tree (`coretemp` for Intel,
`k10temp`/`zenpower` for AMD) and prefers the package reading over the hottest core. **On Intel
hosts, Xen usually blocks the MSR access `coretemp` needs** (`modprobe coretemp` fails with *No
such device* in dom0) - the plugin then falls back to the **ACPI thermal zone** (`acpitz`), which
on most boards tracks the CPU package closely. AMD's `k10temp` reads PCI config space and works
under Xen normally (`modprobe k10temp`, persisted via `/etc/modules-load.d/`, if it isn't loaded
already). Test it from dom0:

```
xe host-call-plugin host-uuid=<uuid> plugin=argus-temp fn=get
```

The sensor appears under Temperature with a *running hot* (≥ `{$XCP.TEMP.WARN}`, default 80 °C)
and *overheating* (≥ `{$XCP.TEMP.HIGH}`, default 90 °C) alert.

## Troubleshooting

- **Start with the reason.** Hover (or tap) the sensor's value in Argus - `not supported`, or `Not reachable`
  on a down collector - to see why, as XAPI or the connection reported it; the alert carries the same text.
- **"XCP-NG XAPI is unreachable"** - the proxy/core cannot reach `https://<master>` (host down,
  wrong address, or 443 blocked between the probe and the hypervisor).
- **"XCP-NG credentials rejected"** - XAPI answers but refuses the login; fix the username or
  password in host settings.
- **No CPU utilization** - the value comes from the host's `rrd_updates` feed; a host that just
  booted may need a couple of minutes to serve it.
- **No temperature sensor** - the `argus-temp` plugin is not installed on that hypervisor (see
  above), or its hwmon chip is not one the plugin recognises - run `head /sys/class/hwmon/hwmon*/name`
  on dom0 and check for `coretemp`/`k10temp`/`acpitz`; anything else is a one-line addition to the
  plugin's chip list.

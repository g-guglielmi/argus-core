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
- **Credentials**: the XAPI login. On a stock XCP-NG that is **`root`** and the host root password
  (local XAPI accounts are root-only; only pools with external/AD authentication have other users).
  The password is stored as a Zabbix **secret macro** (write-only after saving).
- **VM monitoring**: a dropdown, off by default (see below).

> Multi-host pools are fully coded (the collector enumerates every member and reads each host's
> RRD feed) but so far lab-verified only on single-host pools.

## What it monitors

**Pool**: name, HA enabled (hidden while off), members live vs total (hidden on single-host pools,
with an alert when a member drops), VMs running vs defined - the VM counters work even with VM
monitoring off and lead the Virtual machines section.

**Per hypervisor**: CPU utilization (from the host's RRD feed - XAPI itself stopped exposing live
CPU long ago), used/total memory and %, uptime, XCP-NG version, liveness. Alerts: member down,
high CPU (`{$XCP.CPU.UTIL.WARN}`, default 90%), high memory (`{$XCP.MEM.WARN}`, default 90%),
unreachable XAPI and rejected credentials.

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

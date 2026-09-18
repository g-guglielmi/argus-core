# unRAID (SNMP)

The **unRAID (SNMP)** class monitors unRAID over SNMP: CPU load, RAM %, uptime, per-share free space,
NICs, and - with the plugins below - per-disk **temperatures** and **CPU temperature**. Add it with
**Add device → unRAID (SNMP)**; the base metrics work as soon as the SNMP plugin is installed. The
temperature extras need the extend scripts described here.

## Monitoring unRAID disk temperatures

The **Argus unRAID by SNMP** template (auto-attached to a host detected as unRAID) adds per-disk
temperatures and per-share free space on top of the base Linux-SNMP metrics (CPU, RAM, uptime,
filesystems, NICs). Those extras are read from **NET-SNMP `extend` scripts**, so two things are
required on the unRAID host - without them the disk-temperature group shows as a gap:

1. **Install the Community Applications "SNMP" plugin** (Apps → search *SNMP*). It provides the
   `snmpd` service and the Settings → SNMP config box.
2. **Install the temperature extend script** ([`unraid-pool-temps.sh`](../unraid-pool-temps.sh)):
   - Copy it to `/boot/config/plugins/snmp/pool_temps.sh` on the unRAID host.
   - In **Settings → SNMP**, add these lines to the snmpd.conf box, **remove** the plugin's own
     `extend disktemp …` line, then Apply:
     ```
     extend arraytemps /bin/bash /boot/config/plugins/snmp/pool_temps.sh array
     extend pooltemps /bin/bash /boot/config/plugins/snmp/pool_temps.sh
     ```
     (invoked through `bash` because `/boot` is mounted `noexec` on current unRAID.)

The script reads temperatures from unRAID's own emhttp state file, so it is **atomic, instant, and
never wakes a disk** - the numbers match the unRAID dashboard and refresh at unRAID's SMART polling
cadence (Settings → Disk Settings → *Tunable (poll_attributes)*, default 1800 s). It ships two
extends: **`arraytemps`** (parity + data disks) and **`pooltemps`** (cache + custom pools, incl.
NVMe). The template prefers these per drive and falls back to the plugin's own `disktemp` extend on
hosts that don't have them; hosts without the plugin at all are simply unaffected.

A **spun-down (parked) drive reads a fixed `20 °C`** standby sentinel, so it shows as a distinct low
flat line on the chart (and any heat warning clears) instead of dropping off - only genuine standby
disks get this, never the USB boot flash or an always-on SSD/NVMe cache.

**Temperature thresholds are split by role**, since SSDs tolerate more heat than spinning disks. Array
members are HDDs, pool/cache devices are SSD/NVMe, so each uses its own macro pair:

| Disks | Warning | High |
|---|---|---|
| Array (`{$DISK.TEMP.WARN}` / `{$DISK.TEMP.HIGH}`) | 40 °C | 45 °C |
| Pool / cache (`{$POOL.TEMP.WARN}` / `{$POOL.TEMP.HIGH}`) | 65 °C | 75 °C |

If you run an SSD in the array (or an HDD in a pool), override the relevant macro on that host in host
settings - the split is by array/pool role, not by reading the drive's rotation status.

## Monitoring unRAID CPU temperature

CPU temperature comes from **lm-sensors**, not SNMP, so it needs one more plugin and one more
extend script (same pattern as the disk temps above):

1. **Install the "Dynamix System Temperature" plugin** (Apps → search *System Temperature*), and
   let it detect sensors. This loads the kernel sensor modules (`coretemp` for Intel, `k10temp`
   for AMD, …) - it's what makes CPU temperature readable at all, and it's the same source as the
   temperature shown on the unRAID dashboard footer.
2. **Install the CPU-temp extend script** ([`unraid-cpu-temp.sh`](../unraid-cpu-temp.sh)):
   - Copy it to `/boot/config/plugins/snmp/cpu_temp.sh` on the unRAID host.
   - In **Settings → SNMP**, add this line to the snmpd.conf box, then Apply:
     ```
     extend cputemp /bin/bash /boot/config/plugins/snmp/cpu_temp.sh
     ```

The script reads `sensors` and reports the CPU **package** temperature (Intel `Package id 0` /
AMD `Tdie`/`Tctl`, or the hottest core as a fallback) - it needs no per-CPU configuration. It shows
up as a standalone **CPU temperature** sensor under the Temperature category, with a *running hot*
(≥ `{$CPU.TEMP.WARN}`, default 80 °C) and *overheating* (≥ `{$CPU.TEMP.HIGH}`, default 90 °C) alert.

> If the CPU sensor doesn't appear, run `sensors -u` on the host and check the label names - an
> unusual chip may use labels the script doesn't recognise, which are a one-line tweak.

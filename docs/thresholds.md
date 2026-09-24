# Thresholds

Every alert in Argus fires when a reading crosses a **threshold**. Thresholds are Zabbix user macros
defined on each monitoring template (for example `{$CPU.UTIL.WARN}` = 80, `{$DISK.TEMP.HIGH}` = 45,
`{$PING.LOSS.WARN}` = 20) and compared inside the templates' trigger expressions (DESIGN §6). You can
tune them from the UI - fleet-wide or for a single device - without touching Zabbix. Editing the
fleet-wide defaults is **admin-only** and lives in the sidebar under **Admin → Thresholds**.

## Two levels

- **Fleet-wide default** - the value every host inherits. The **Thresholds** screen lists the
  monitoring templates; click one to open a dialog that edits its thresholds. A template row shows the
  classes it affects and how many of its thresholds you've customized. Most compute classes share the
  same template (`Argus Linux by SNMP`, `Argus NAS by Zabbix agent`, ...), so a change there applies to
  every host of those classes that has no override.
- **Per-device override** - a value for one host only. Edited in that host's settings dialog
  (**Monitoring → a host → Settings → Thresholds**). An override always wins over the fleet-wide
  default.

In both places, a field left blank means "use the level above": a blank per-host field inherits the
fleet-wide default, and a blank/reset fleet-wide field uses the template's factory value. The current
effective value is always shown in the field as the placeholder, and a **Reset** control puts a
fleet-wide value back to its factory default.

## How it is stored (and why a re-import can't wipe it)

A per-device override is a host macro of the same name - Zabbix resolves a host macro ahead of the
template default, so nothing else is needed and a reset simply deletes the host macro.

Fleet-wide overrides are kept in Argus's own database and **re-applied onto the Zabbix templates after
every startup reconcile**. That matters because an app upgrade that ships edited templates re-imports
them, which would otherwise reset the template macros to their shipped values; Argus overlays your
stored defaults again immediately, so they survive upgrades.

## Per-type disk temperatures

Disk-temperature thresholds are type-aware. Spinning disks (HDD) use the base
`{$DISK.TEMP.WARN}` / `{$DISK.TEMP.HIGH}`; SSD and NVMe drives use the higher `:ssd` / `:nvme`
context values, picked per drive from its SMART type. All of them appear as separate rows in the
template's edit dialog. Setting a threshold for one *individual* drive (as opposed to all HDDs, or all
SSDs) is not exposed today - it would need per-instance context macros in the triggers.

## Sensor order

The order sensor categories read in a host view (CPU before disk, network gear network-first, and so
on) has a **fixed default** - one of three built-in profiles chosen by the host's shape
(server = compute-first, network gear = network-first, storage = drive-temps-before-disks).

You can override that order **per host**: in a host's settings dialog, turn on "Custom order for this
host" and reorder the categories. A per-host order wins over the built-in default; categories the host
doesn't have are skipped, so a partial reorder is always safe. There is no fleet-wide/per-class order
override - the built-in default is deliberately fixed, and only individual hosts deviate.

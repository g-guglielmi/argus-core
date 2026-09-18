# Device monitoring guides

Most device classes need nothing beyond **Add device** in Argus - pick the class, give the address,
and the template does the rest. The hosts below need a bit of setup **on the device** first (a plugin,
an agent container, an SNMP feature) or expose a per-host option worth explaining. Each guide covers
that.

| Host / class | Guide | What it covers |
|---|---|---|
| **Windows (SNMP)** | [windows.md](windows.md) | Enabling the SNMP feature, and opt-in **service monitoring** (the `{$WIN.SERVICE.MATCHES}` regex). |
| **unRAID (SNMP)** | [unraid.md](unraid.md) | Per-disk **temperatures** and **CPU temperature** via the SNMP + System Temperature plugins and NET-SNMP extend scripts. |
| **Ugreen NAS (Zabbix agent)** | [ugreen.md](ugreen.md) | Running the Zabbix agent 2 container on UGOS (no SNMP), with no-wake per-disk SMART temps. |

Per-host tunables (like the Windows service filter) can be set when you add the device **and** changed
later in **host settings** (the settings panel on the host row) under the class options section.

For the full device-class catalog and what each one collects, see [../DESIGN.md](../DESIGN.md) section 5.

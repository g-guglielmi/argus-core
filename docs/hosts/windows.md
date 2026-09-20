# Windows (SNMP)

The **Windows (SNMP)** class monitors a Windows host over SNMP using the HOST-RESOURCES and IF-MIB
tables plus the LAN Manager service table: CPU utilization (overall and per-core), physical memory,
fixed-disk volumes, network interfaces, uptime, and - opt-in - selected Windows **services**. It reuses
the same item keys as the Linux (SNMP) class, so the readings curate identically.

## 1. Enable SNMP on the Windows host

1. **Settings → Apps → Optional features → Add a feature → "Simple Network Management Protocol (SNMP)"**
   (on Windows 10/11; on Windows Server, add the **SNMP Service** role feature). Install it.
2. Open **services.msc → SNMP Service → Properties**:
   - **Security** tab: add your monitoring **community** (read-only is enough), and either accept SNMP
     packets from the site proxy's IP or from any host on the trusted LAN.
   - Start the service and set it to **Automatic**.
3. Make sure the host firewall allows **UDP 161** from the site proxy.

## 2. Add the host in Argus

**Add device → Windows (SNMP)**, give the host's IP or DNS name. The SNMP credentials inherit the
collector's SNMP default - the site proxy's, or Core SNMP for core-monitored hosts (both set in the
Probes tab) - unless you override them on the host. CPU, memory, disk,
network and uptime start reporting within a minute or two.

## 3. Service monitoring (opt-in)

Service monitoring is **off by default**. It is driven by a per-host macro:

- **`{$WIN.SERVICE.MATCHES}`** - a regular expression of the service **display names** you want to
  monitor. Empty (the default) means "monitor no services".

Set it in either place:

- **When adding the host** - the "Monitored services (regex)" field in the Add-device wizard.
- **On an existing host** - **host settings** (the settings panel on the host row) → **Windows (SNMP)
  options → Monitored services (regex)**. This is the way to enable it on a host you already added.

Each matched service becomes a `win.service.state[...]` sensor under a **Services** group, with a HIGH
**"Service not running"** trigger that fires if the service stops, is paused, or drops out of the
service table. Discovery re-runs on its own cycle (about once an hour), so give it a cycle - or use
**Discover now** on the host to force it.

> Only services that are **currently running** appear in the SNMP LAN Manager table (`svSvcTable`). So
> the regex should match services you expect to be up; the trigger then catches them going down. A
> service that is already stopped when discovery runs will not be picked up at all.

## How the regex is formed

The value is matched against each service's display name (the `{#WINSVC}` value discovered from
`svSvcTable`). A service is monitored if the regex matches its name. Example:

```
DNS Server|Print Spooler|SQL Server .*
```

Broken down:

| Piece | Meaning |
|---|---|
| `\|` | **OR** (alternation). The value is a list of alternatives; a service matches if it matches **any** of them. |
| `DNS Server` | Literal text - every character (including the space) matches itself. |
| `Print Spooler` | Literal text. |
| `SQL Server .*` | Literal `SQL Server ` then `.*`, where `.` is any single character and `*` is "zero or more of the preceding". So `.*` is any run of characters. Matches `SQL Server (MSSQLSERVER)`, `SQL Server Agent`, etc. |

**The match is a partial (substring) match, not a whole-string match.** The pattern matches if it is
found *anywhere* in the service name. Two consequences:

- `DNS Server` alone already matches a service literally named "DNS Server" - you do not need `.*` on
  the ends.
- `SQL Server .*` and plain `SQL Server` behave almost the same here; the `.*` just signals "and
  whatever follows". You can drop it: `DNS Server|Print Spooler|SQL Server` works the same.

### Rules and gotchas

- **Case-sensitive.** `print spooler` does not match `Print Spooler`. Use the exact display name as
  shown in `services.msc`.
- **Anchor for an exact match.** Use `^` (start) and `$` (end): `^Print Spooler$` matches only that
  exact name, not "Print Spooler Extended".
- **Escape regex metacharacters that appear literally in a name.** `. ( ) [ ] + * ? | \ ^ $` are
  special. A name like `SQL Server (MSSQLSERVER)` contains literal parentheses, so to match it exactly
  write `SQL Server \(MSSQLSERVER\)`. Escaping is the safe habit even in a loose substring match.

### Common forms

```
Print Spooler|W32Time|Windows Update
```
Match those three by substring.

```
^(Print Spooler|Netlogon|DNS Server)$
```
Match exactly those three, nothing broader.

```
SQL Server.*
```
Match every service whose name starts with "SQL Server".

### Finding the display names

Open **services.msc** on the host - the leftmost column is the **display name**, which is exactly what
the regex matches against. (The short "service name" underneath, e.g. `Spooler`, is *not* what is
matched; use the display name, e.g. `Print Spooler`.)

## Troubleshooting

- **No services show up.** Confirm `{$WIN.SERVICE.MATCHES}` is set (not empty), the names are spelled
  and cased exactly, and the services are actually running. Force **Discover now** on the host.
- **A service you expect is missing.** It may be stopped (so it is absent from `svSvcTable`), or its
  display name differs from what you typed. Check `services.msc`.
- **Nothing at all over SNMP.** Re-check the community, the "accept from" host list, and that UDP 161
  is open from the proxy.

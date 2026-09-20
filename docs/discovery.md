# Network discovery

Argus can sweep a subnet from one of your probes, fingerprint what answers, and let you adopt the
results as monitored devices in a couple of clicks - the auto-provisioning replacement for PRTG's
"Add Sensor" flow (DESIGN §8). Discovery is **admin-only** and lives in the sidebar under
**Configure → Discovery**.

## Requirements

- **Scanning from a probe** needs a probe image that ships the scanner (any `probe/v7.0.30-r13` or
  later; the rolling `latest` tag picks it up automatically) **and** check-in enabled (probes
  enrolled through Argus have it out of the box; older ones can be issued a token from the Probes
  page). A probe that qualifies advertises the capability at check-in - until it does, the
  Discovery form shows it as "(needs probe update)".
- **Scanning from the core server** needs nothing extra: the Argus server runs an equivalent
  built-in scanner in-process (for the networks the core reaches - the same ones you'd monitor with
  "Monitored by: Core server"). Two container-imposed differences: devices that answer **only ICMP
  ping** (phones, IoT with no open ports) are found only where the container runtime allows
  unprivileged ping (Docker does by default) and are otherwise skipped, and **MAC addresses** are
  never reported.
- For SNMP fingerprinting, either set the probe's **SNMP default** (Probes → SNMP) or type a
  community into the scan form (the core has no SNMP default, so core scans always need it typed).
  Only SNMP **v1/v2c** are used for scanning; a v3-only site simply scans without SNMP facts.

## Running a scan

1. Pick where to scan from ("Scan from": the **core server** or a probe), enter the subnet in CIDR
   form (e.g. `10.0.0.0/24`; a bare IP means just that host; a `/22` = 1024 addresses is the
   maximum), optionally override the SNMP community, and pick the **site** new devices should join
   (pre-filled from the probe's site).
2. **Start scan.** A probe picks the job up at its next check-in (within a minute); a core scan
   starts immediately. The scan itself typically takes one to a few minutes for a /24. One scan
   runs per source at a time.
3. Results appear in the review table as soon as the probe reports back.

For each live address the probe reports: ICMP reachability, open TCP ports from a small service set
(SSH 22, DNS 53, HTTP 80/8080, HTTPS 443/8443, SMB 445, NUT 3493, Zabbix agent 10050), SNMP
`sysDescr`/`sysObjectID`/`sysName`, an HTTP(S) banner (status, `Server` header, page `<title>`),
whether a real DNS query got an answer, reverse DNS, and the MAC address (same-L2 subnets only).
The scanner is stdlib-Python (`argus_netscan.py`, baked into the probe image), sends one packet-level
probe per check - no nmap involved - and posts a single results blob back to the core.

## Reviewing results

The core maps each fingerprint to a **suggested device class** (UniFi models by `sysObjectID`,
Windows/Linux/Ugreen by `sysDescr`, AdGuard Home / Home Assistant / XCP-NG by page title, DNS
servers by a real query, NUT by port, SSH-only boxes as Linux (SSH, agentless), plain web boxes as
Ping + HTTP). Per row you can:

- edit the **name** (pre-filled from `sysName`, reverse DNS, or the IP),
- change the **class** (searchable dropdown - the suggestion is only a starting point),
- open the row's settings (chevron) to fill **class macros** (e.g. SSH credentials for the
  agentless Linux class) and toggle the **HTTP/HTTPS add-on** when the device answered on a web
  port (scheme and port are taken from the scan).

Then select the rows you want (header checkbox = all) and **Add selected** - each device is created
exactly like the manual Add-device path (Base Ping + the class templates, thresholds from the class,
LLD fired right away), bound to the scanning probe, tagged `argus.source=discovered`. SNMP-class
devices inherit the probe's SNMP default like everywhere else in Argus.

Rows you don't care about: **Ignore selected**. Ignored devices stay ignored on future re-scans of
that probe (unignore any time via "Show ignored"). Devices Argus already monitors are flagged
"monitored" and can't be selected, so a re-scan is a cheap way to find what's new on a subnet.

## Notes

- Recent scans (the last 10 per probe) are kept and can be reopened from the strip at the bottom.
- A scan that the probe never picks up fails after 5 minutes ("is it online and running a
  scan-capable image?"); one that never reports back fails after 15.
- Scan traffic is polite: TCP connects and single UDP probes with 1-3 s timeouts, ~64 addresses in
  parallel, an 8-minute budget per scan.
- The UniFi controller API sweep (managed inventory as a second candidate source) is the next slice
  of this pipeline - see the roadmap.

# Network discovery

Argus can sweep a subnet from one of your probes, fingerprint what answers, and let you adopt the
results as monitored devices in a couple of clicks - the auto-provisioning replacement for PRTG's
"Add Sensor" flow (DESIGN §8). A second source, the **UniFi controller sweep**, asks a UniFi
Network controller for its adopted devices instead of probing the wire - both feed the same
review-and-adopt screen. Discovery is **admin-only** and lives in the sidebar under
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
  never reported. The core's SNMP default is set under Probes → **Core SNMP** - it also enables
  SNMP-credential inheritance for every device monitored by the core, exactly like a probe's
  default does for its site.
- For SNMP fingerprinting, the scan **inherits the collector's SNMP default** - the probe's
  (Probes → the probe's Defaults) or the core server's (Probes → **Core SNMP**) - unless you type a
  community into the form for this one scan. Only SNMP **v1/v2c** are used for scanning; a v3-only
  site simply scans without SNMP facts.

## Running a scan

1. Pick where to scan from ("Scan from": the **core server** or a probe), enter the subnet in CIDR
   form (e.g. `10.0.0.0/24`; a bare IP means just that host; a `/22` = 1024 addresses is the
   maximum), and optionally override the SNMP community for this scan. The **site** is chosen at
   review time, not here.
2. **Start scan.** A probe picks the job up at its next check-in (within a minute); a core scan
   starts immediately. The scan itself typically takes one to a few minutes for a /24. Scans
   **queue per source** (a handful at a time, running one after another - the scanner is
   single-file by design), and different sources scan in parallel, so you can fire several scans
   back to back.
3. Every scan lands in the **Recent scans** list (kept for 30 days) with its found/new counts -
   open any of them to review, adopt or ignore its results; the just-started scan opens itself when
   it finishes.

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
- change the **class** (searchable dropdown - the suggestion is only a starting point). A class
  that still needs required fields shows an **amber warning** next to the picker (click it to fill
  them in); a row with missing required fields is refused at Add time with the reason inline,
- open the row's settings (chevron) to override the **site**, fill **class macros** (e.g. SSH
  credentials for the agentless Linux class) and toggle the **HTTP/HTTPS add-on** with its scheme
  and port - offered for every web-capable class, pre-filled when the scan saw a web port.

Then select the rows you want (header checkbox = all), pick the **site** they join in the toolbar
(pre-filled from the scanning probe's site; override per device in its row settings), and **Add
selected** - each device is created exactly like the manual Add-device path (Base Ping + the class
templates, thresholds from the class, LLD fired right away), bound to the scanning collector,
tagged `argus.source=discovered`. SNMP-class devices inherit their collector's SNMP default like
everywhere else in Argus - including the core's own default for core-monitored devices.

Rows you don't care about: **Ignore selected**. Ignored devices stay ignored on future re-scans of
that probe (unignore any time via "Show ignored"). Devices Argus already monitors are flagged
"monitored" and can't be selected, so a re-scan is a cheap way to find what's new on a subnet.

## UniFi controller sweep

Where the subnet scan fingerprints and guesses, the sweep **asks the controller** - so every result
comes back with the exact model, type, MAC, IP, firmware and UniFi site, and the class suggestion
is certain (switch/AP/gateway by the controller's own device type).

1. Save the controller once under **Manage controllers**: a name, the base URL (the same one you'd
   put in `{$UNIFI.URL}`, e.g. `https://unifi.example.lan:11443`) and an **API key** (UniFi
   Network → Settings → Control Plane → Integrations). The key is stored encrypted and never
   returned to the browser; editing a controller with the key field left blank keeps the stored
   one.
2. Pick the controller, pick where to sweep from (the **core server**, if it can reach the
   controller URL, or a **probe** on the controller's network - probe sweeps need
   `probe/v7.0.30-r15` or later, older ones show "(needs probe update)"), and **Start sweep**. A
   sweep is a handful of HTTPS calls and finishes in seconds; it covers **all sites** of a
   multi-site controller and lists **adopted devices with an IP** (clients are the subnet scan's
   job).
3. Review and adopt exactly as with a scan - with one extra convenience: for a device adopted with
   a UniFi class, the four controller macros (`{$UNIFI.URL}`, `{$UNIFI.KEY}`, `{$UNIFI.MAC}`,
   `{$UNIFI.SITE}`) are **filled in server-side from the saved controller and the sweep facts** -
   the fields the manual Add-device flow makes you type per device. The API key goes straight from
   the encrypted store onto the host (as a secret macro); the row settings show these fields as
   auto-filled, and only the site can be overridden.

Sweeps share the scan queue and history (same per-source queueing, same 30-day retention, same
ignore carry-over - a device ignored in a scan stays ignored in a sweep of the same source and
vice versa). The sweep speaks the same API the UniFi class templates poll (`X-API-KEY` against the
Network API, `/proxy/network/...` with a fallback to the bare path for plain self-hosted
controllers), so a controller that works for monitoring works for the sweep. TLS is not verified -
consoles ship self-signed certificates.

**Subnet scans use the controllers too.** Once a controller is saved, every subnet scan's results
are cross-checked against its adopted devices (matched by MAC, or by IP where the scan saw no
MAC): matching rows get the controller facts - exact model, real name, firmware, site - the
certain class suggestion, and the same adopt-time macro injection as a sweep row. So a plain scan
of a range with UniFi gear in it already reviews like a sweep; the standalone sweep remains the
way to import a controller's whole estate (all sites, no wire scan) in one go. Best-effort: the
core queries the controllers, so one it can't reach simply doesn't enrich.

## Notes

- Scan history is kept for 30 days (capped per source); reopen any scan from the Recent scans
  list, or delete one there (trash icon, or the Delete button inside the opened scan) - e.g. an
  obsolete run or a wrong subnet. A running job can be deleted too; its late results are dropped.
  Deleting a scan also forgets any ignores recorded only in it.
- A scan nothing picks up fails after 5 minutes ("is the probe online and running a scan-capable
  image?" - a scan queued behind a running one waits as long as it needs); one that is picked up
  but never reports back fails after 15.
- Scan traffic is polite: TCP connects and single UDP probes with 1-3 s timeouts, ~64 addresses in
  parallel, an 8-minute budget per scan.
- Deleting a saved controller never touches adopted devices (their macros are their own); queued
  sweeps referencing it fail with a clear message.

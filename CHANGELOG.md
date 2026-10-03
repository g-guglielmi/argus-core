# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/), and the project
follows [Semantic Versioning](https://semver.org/) (`MAJOR.MINOR.PATCH`).

**Versioning policy:** increment the PATCH (`0.0.x`) for each change during development -
`0.0.1`, `0.0.2`, `0.0.3`, … - reserving **`1.0.0`** for the first production-ready release.
Each release is a git tag `vX.Y.Z` that triggers CI to build the versioned image and publish a
GitHub Release from the matching section below.

---

## [Unreleased]

## [0.7.2] - 2026-10-03

A patch release: notes on sensors in trouble that go out with their alerts and clear themselves once
the sensor is OK, a history that leaves hidden sensors out, and a fix for the ⋯ menu being cut off
near the bottom of a host card. Nothing to update besides the core.

**Sensor notes:**
- Leave a note on a sensor in trouble from its ⋯ menu (**Add note**), like "ISP ticket 4471 open,
  technician on site at 14:00". It shows with the sensor in Overview, the lists and on the host page,
  and on the status pages, and goes out with the sensor's alerts, reminders and RESOLVED in every
  channel. It doesn't acknowledge anything: the alert keeps its colour and its reminders.
- The note clears itself once the sensor is OK again (it stays through a blip shorter than the alert
  delay) and the incident history keeps it. **Edit note** and **Remove note** change it before then.
  Admins and helpdesk can write notes.

**History:**
- The incidents of a hidden sensor are left out of the History page and of a host's history, and on
  the History page a hidden host's too: hiding one says it isn't news. **Show N from hidden sensors**
  brings them back, dimmed and tagged hidden. A sensor you open still shows its own history.

**Fixed:**
- A sensor's ⋯ menu near the bottom of a host card was cut off by the card (on the host page the last
  items, like Disable alerts, were hidden). The menu now draws above the page: it opens upward when
  there's more room there, always stays on screen, and follows its button when the page scrolls.

## [0.7.1] - 2026-10-03

A patch release: one **Updates** page for every update (the core, its sidecar, the probes and the
VMs' operating systems), laid out the same way in every section, and a fix so a sidecar never takes
a proxy update and its own update at once. Nothing to update besides the core.

**Updates page:**
- A new admin page, **Updates**, puts every update in one place, with one **Check for updates** that
  looks up the core, its sidecar, the probe image, the probe sidecar and the probe VM image at once
  and says in one line what can update. The three sections are laid out the same way, in rows: each
  machine shows one line per part with its version, one status (up to date, available, queued,
  updating, failed) and its **Update** button, with the step log underneath while an update runs.
  Settings read the same way and change in place.
  - **Argus core:** the channel the core runs on (latest, testing or a pinned version) with the
    switch, then the core (release notes), its updater sidecar and the collectors it installs.
  - **Probes:** the fleet target, then every probe's proxy and sidecar, with **Update all** for every
    one that is behind. A probe without a sidecar shows how to update it by hand.
  - **Operating systems:** the core's reboot window and Zabbix minor-update window, the core VM's
    patch state and Zabbix version, each probe VM's patch state, and the newest probe VM image.
- Every update now asks first, naming the version it goes from and to.
- The Probes page keeps the versions as read-only status in the same words; a probe or sidecar that
  is behind links to Updates. Its Check for updates, update buttons and fleet target moved there.
- Settings keeps a short **About** (version and licence); the old About and OS updates cards moved to
  Updates. **Core SNMP default** moved up, right under About. The version pill in the sidebar and
  the update notices (a new release, a core or probe update, a probe behind, OS updates and reboots)
  open Updates.
- A sidecar asked for both a proxy update and its own gets them one check-in apart, the proxy first:
  its own update replaces it, and could do so while it was still recreating the proxy.

## [0.7.0] - 2026-10-02

A minor release: real HTTP checks (any URL, subdomain or path, each with its own certificate check
and page text), push sensors for jobs that report their runs, notes and maintenance on status pages,
six new alert channels (Teams, Slack, ntfy, Gotify, Pushover, a webhook), and updates you can follow
to the end: every step of a core, sidecar or probe update stays shown until it is done, and the
core's own collectors now come with each update. Update the sidecar to argus-updater 0.2.12 and the
probes to `probe/v7.0.31-r22` for all of it.

**HTTP checks:**
- The HTTP/HTTPS add-on now makes a real request instead of only checking that the port answers, so a
  page serving an error or an expired certificate no longer counts as up. List any number of pages
  under **URLs or paths** (up to 16): full URLs (`https://portal.example.com/app`, any host, subdomain
  or path) or paths on the host (`/login`); left blank it checks the host itself, as before. Each URL is
  its own sensor under **Web** with its response time and, for https, the days its certificate has
  left. A URL is up when it answers with an accepted status code (`200-299` by default, redirects
  followed), has the text a `#text` suffix asks for (`#!text`: must not), and presents an accepted
  certificate; when it isn't, it says why. **Certificate** sets the check: `verify` wants one a known
  CA issued; `self-signed` also takes the device's own (or a private CA's), still not expired and, for
  a URL by name, for that name (a URL by IP address isn't name-checked); `ignore` takes any and
  leaves the certificate out: no days-left sensor and no expiry alerts. In the other two modes, new
  alerts for a certificate expiring in under 21 days (warning) and 7 days (high), both on the
  Thresholds screen, and one for a certificate that has expired.
- The URLs are rows in host settings, each with its own **Certificate** check and text its page must
  or must not contain; the add-on's **Scheme**, **Port** and **Certificate** fields show only while
  something uses them (a blank list, a host without a scheme, a path). A row takes a full URL, a host without a scheme (`10.7.0.2`,
  `10.7.0.2:8443/admin`) or a path, and a mistake shows under its row as you type. A host settings
  save that fails also shows its error as a toast, not only at the bottom of the dialog.
- Taking a URL out of the list (or a port out of the TCP ports add-on) deletes its sensors and their
  open problems at the next check, instead of an hour later.
- A URL whose certificate is refused is still down for it, but its page is now asked for anyway
  (without checking the certificate), so its response time and status code keep coming instead of
  stopping with the certificate (from `probe/v7.0.31-r22`; on the core, with the argus-updater's
  next collectors install).
- A URL that stays down after a check failed once no longer adds "response time stopped collecting"
  and "status code stopped collecting" to its "is down": Zabbix kept those two "not supported" from
  that one failure for as long as the URL didn't answer.
- The check runs in the probe (`argus_http.py`, `probe/v7.0.31-r15`; hosts without a scheme and
  per-URL options from `probe/v7.0.31-r18`; the `self-signed` mode as described from
  `probe/v7.0.31-r19`, which also takes a device whose own certificate chain a strict check calls
  "invalid ca certificate", as some NAS and gateway firmware sends; `ignore` leaving the certificate
  out from `probe/v7.0.31-r21`); for a host the core server monitors, the core's copy now comes with
  each Argus update (below). The add-on's two old sensors are replaced by the
  new ones, so their history goes.
- A certificate's days left read as whole days, rounded down (6.6 days left is "6 days", in the
  sensor list, the chart and alerts), while the chart draws the precise countdown (from
  `probe/v7.0.31-r20`) as a smooth line instead of a step every 2.4 hours.
- A URL's chart keeps at least 15 days on the certificate's axis, so its slow countdown reads flat
  instead of a cliff every time the days left tick down, and that axis is the right-hand one (the
  downtime band no longer takes it).
- A group of sensors (a URL, a port) shows its latest check, not "never" while its response time has
  no value because it is down.
- A probe collector that can't run (a script the probe doesn't have yet, a timeout) now shows as a
  sensor of its own on the host, **HTTP checks** under **Web** for instance, says why (for a missing
  script, which probe release brings it), and alerts. Before, the host just had no such sensors.

**Push sensors:**
- A backup, a cron job or a scheduled task can now report to Argus when it runs: give it a push sensor
  in the host's settings (**Push sensors**) and have the job call its URL at the end, with
  `status=ok` or `status=fail` and an optional message (curl and PowerShell examples are next to the
  URL). A failed run is an error with the job's message as the reason; no run for longer than the
  late time is a warning, longer than the missed time an error. The sensors show under **Push** on the
  host, with their history, uptime and alerts like any other. The host's probe reads them from Argus
  at its Public URL once a minute (docs/push-sensors.md).

**Status pages:**
- Pin a note on a status page (**Add note** on its card in Status pages): what's going on, what's being
  done, when it's expected back. It shows in a bar at the top of the page in its style (info, warning
  or problem) for the time you pick, or until you remove it.
- A status page shows the maintenance windows of its hosts: the ones in progress with their end, and
  the next ones within a week. A problem whose host is in a window says so.

**Alert channels:**
- Six new channel types. **Microsoft Teams** takes the URL of a Teams Workflow (the "Send webhook
  alerts to a channel" template) and shows each alert as a card in the status colour, with **Open in
  Argus** and **Acknowledge** buttons. **Slack** takes an incoming webhook. **ntfy**, **Gotify** and
  **Pushover** send a phone push that is louder for errors than for warnings; Pushover carries the
  graph too. A generic **webhook** POSTs each alert as JSON, for n8n, Node-RED, Home Assistant and
  similar tools.
- Personal channels (Account, Personal notifications) can be Teams, Slack, ntfy or Pushover too. They
  only send to addresses on the internet, so a personal channel can't point inside the network; a
  webhook, Gotify or an ntfy server on the LAN is a shared channel an admin sets up.

**Updates:**
- Settings shows what an update is doing in the background, one timestamped line per step, from the
  moment it is asked for until it ends: queued, picked up, pulling, swapping, verifying, done (or why
  it failed). It appears only while an update is under way or just finished, for the core and for the
  updater sidecar, and a page reload no longer hides it: before, **Update sidecar** showed "update
  queued" and then nothing at all, even though the update went on and finished. A finished update's
  box closes by itself after 5 minutes; a failed one stays until it is closed. The sidecar reports its
  steps from argus-updater 0.2.12; an older one reports none, so Argus shows its new version as the
  outcome.
- The updater sidecar's row works like the core's: **Check for updates** looks up the newest
  published sidecar, the version reads **latest** or **vX available**, and **Update to vX** shows only
  when there is a newer one (it read **Update sidecar** at all times before). A check that finds
  nothing newer says so in green, for the core and the sidecar alike.
- The Probes page has a **Check for updates** too: it looks up the newest probe image, updater and
  probe VM now (Argus otherwise looks every 3 hours) and says what that means for the fleet: every
  probe on the newest version (in green), which probes and updaters can update, or which are
  updating already.
- The core server's SNMP default moved from the Probes page (**Core SNMP**) to **Settings, Core SNMP
  default**: it is the core's own setting, not a probe's. Host settings and Add device point there.
- The Probes page keeps showing a probe or sidecar update after a reload too: **update queued**
  until the sidecar's next check-in takes it, then **updating** (with how long ago it was handed
  over) until the probe reports the new version, and **update didn't take** when it still runs the
  old one 20 minutes later (the updater rolled it back), until it is retried. Before, the queued tag
  lived only in the open page, and the **Update** of a sidecar only popped up a message. The page
  looks every 5 seconds while an update is in hand.

**The core's collectors:**
- The scripts the core's own Zabbix runs for the hosts it monitors itself (HTTP, TCP ports, SSH, UPS,
  XCP-ng and the rest) now update with Argus. Before, they stayed as setup installed them, so a fix to
  a collector needed copying by hand on the core. The Argus image carries them and the argus-updater
  sidecar copies the ones that changed into the core host's Zabbix after every update (and puts back
  a deleted one within a day). **Settings, About** shows whether they're current, and what to do
  if they aren't: an updater from before this needs updating once (to argus-updater 0.2.11 or later).

**Acknowledged problems:**
- Everything about an acknowledged problem now takes the acknowledged colour: the host's dot, problem
  count and ping graph once all its problems are acknowledged, the problems box (titled
  **Acknowledged problems**) and its "acked" tags, and a group's dot when that's all it has left.
  Before, the sensor row changed but the host still read red and the tag green.

**Host settings:**
- The sections (Interfaces, the class options, Add-ons, Push sensors, Master sensor, Thresholds,
  Sensor order) are folded, since most hosts keep their defaults, and their titles are larger and
  brighter. Folded, a title says in a line what its section holds: the interfaces' addresses, the
  values this host sets ("1 set for this host: CPU utilization - warning 90%") or that all are at
  the defaults, the add-ons that are on, the push sensors (in the warning colour when one failed).
  The title opens the section.

## [0.6.1] - 2026-10-01

Backups, tried on a real core: they now work on a core installed by hand, say what they are doing,
and keep the core's address. Every container's files are in `/docker/<container name>`.

**Backups:**
- Backups tell what they are doing. **Settings, Backups** shows **Recent activity**: the last runs, what
  started each (the daily time, or who pressed the button), how it went, and for a failure the step
  it failed at ("failed while mounting //10.0.0.20/Backup: ..."). The page follows a check or a backup
  until its result is in, instead of showing the previous one; an old export failure says so once a
  newer check works. In the journal each run starts with what it does and why, names the target and
  logs each step; a failed target check no longer shows as a failed service. `argus-backup log` prints
  the recent activity on the VM.
- Backups keep the core's network identity (`network.json`): its address, gateway, DNS, static routes
  and hostname. `argus-restore network ARCHIVE` gives a new core the same address and hostname, so the
  probes and the web certificate find it where they expect: on a core VM it sets them after you type
  `TAKE OVER`; an address from DHCP is better moved to the new machine's MAC, which it prints; on a
  machine whose network another tool manages it prints the settings to enter. `argus-restore inspect`
  shows the old address, and a restore says when this machine doesn't have it yet.
- The backup tools run on the core host: on an existing core, install them again from this release
  (`deploy/core/host/install-backup.sh`, see docs/backup-and-restore.md).

**Folders:**
- Every container keeps its files in `/docker/<container name>`, on the core VM, on the probe VM and in
  the manual install: `/docker/argus` (the database), `/docker/argus/pki` (the CA the probes trust),
  `/docker/argus-update` (the self-update channel, shared with the sidecar) and `/docker/argus-probe`
  on a probe VM. [docs/folder-layout.md](docs/folder-layout.md) lists what each holds. The core VM
  image uses them from `core-vm/v0.1.4`; `setup-core.sh`, `setup-core-patching.sh` and the backup tools default to them.
- The CA is owned by root and only readable by the Argus container's group, and it is also mounted
  read-only over `/data/pki`, so the container can't change or replace it.

**Fixed:**
- Backups on a core installed by hand: the backup tools reported to the appliance's folder
  (`/opt/argus/update`) unless `install-backup.sh` was told otherwise, so on a core that shares another
  folder with Argus, Settings, Backups kept saying the core host hadn't reported. The installer and
  `argus-backup` now find the folder the Argus container actually mounts as its update dir, the
  installer stops with the command to find it when it can't, and the journal says so when the folder
  is missing. `argus-backup status` reads the same folder the service writes to.
- Backups of a core installed by hand now hold everything a restore needs. They missed the CA when it
  lives outside `/etc/argus/pki` (a folder the Argus container mounts, such as `/docker/argus/pki`), so
  after a restore every probe would have had to be enrolled again; they now pack every folder the
  Argus container mounts besides its database and update dir. They also keep how the Argus and
  updater containers ran (`containers.json`: image, environment, folders, ports), since on such a core
  the secret key and the Zabbix API token live only there; `argus-restore containers ARCHIVE` prints
  them back as the `docker run` commands to recreate the containers before restoring.
- An SMB export that fails says why in plain words, instead of the mount's own run-together message:
  the server can't be reached on TCP 445 (a firewall, no route, SMB off), refused the login, has no
  such share, or its name doesn't resolve on the core. The mount's error code stays in brackets.
- **Check the target** catches up after a failed export: when the last export failed and the target
  works again, the check copies the archives it is missing right away, instead of the failure staying
  on screen until the next backup.

## [core-vm/v0.1.4] - 2026-09-30

Refresh of the core appliance golden image: the new folder layout and the backup fixes. It is what a
new deployment gets. Argus and the updater are pre-pulled at build time from their `:latest` images
(v0.6.0 and v0.2.10 today).

**Folders:**
- The containers keep their files in `/docker/<container name>`: `/docker/argus` (Argus's database, as
  `/data`), `/docker/argus/pki` (the CA the probes trust, as `/ca`) and `/docker/argus-update` (the
  self-update channel, as `/update`, shared with the sidecar). See
  [docs/folder-layout.md](docs/folder-layout.md).
- The CA is owned by root and readable by the Argus container's group only, and it is also mounted
  read-only over `/data/pki`, so the container can't change or replace it.

**Backups:**
- The backup tools find the folder shared with Argus from the running container, pack every folder the
  Argus container mounts, and keep the containers' settings (`containers.json`), which
  `argus-restore containers` prints back as `docker run` commands.

## [core-vm/v0.1.3] - 2026-09-30

Refresh of the core appliance golden image, for Argus 0.6.0. Existing VMs are not changed by this; it
is what a new deployment gets. Argus and the updater are pre-pulled at build time from their
`:latest` images (v0.6.0 and v0.2.10 today).

**Backups:**
- The backup tools are in the image (`argus-backup`, `argus-restore` and their systemd units, with
  gpg, rsync, the SMB client and rclone), so **Settings, Backups** works on a new core from its first
  boot. See [docs/backup-and-restore.md](docs/backup-and-restore.md).

**Collectors:**
- The core's copies of the collectors match `probe/v7.0.31-r14`: the Linux SSH collector's failed
  units, CPU iowait and steal, and the TCP ports check (`argus_tcp.py`).

**Look:**
- The first-boot setup page no longer uses long dashes in its text.

## [0.6.0] - 2026-09-30

A minor release: backups with export and a one-command restore, per-site visibility, maintenance
windows and quiet hours, an outside heartbeat, and alerts that treat a device that goes down as one
incident everywhere. It also fixes a device read through a collector that went down completely and
sent no alert at all (since 0.5.9).

**Heartbeat:**
- Argus can ping an outside monitor (a healthchecks.io check, an Uptime Kuma push monitor) once a
  minute, set in **Settings, Heartbeat** or `ARGUS_HEARTBEAT_URL`. The ping only goes out while Argus
  is healthy end to end: the Zabbix API answers, some probe delivered data in the last 5 minutes, the
  alert loop is running, the database takes writes and not every alert channel is failing. When the
  pings stop, the outside monitor tells you, even when the VM itself is down. Settings shows the last
  check, why a ping was held, and has a **Send now** button.

**Linux (SSH):**
- **Failed units**: how many units systemd reports failed, any unit, whether it is listed in
  Services or not, with their names as the reason. No setup and no extra rights; a host without
  systemd has no such sensor. Warning from 1 failed unit, high from 3, both editable.
- **CPU iowait** and **CPU steal**. CPU utilization counts iowait as idle and steal as busy, so a box
  stuck on its disks looked idle and a VM losing CPU time to other guests looked loaded. Warning and
  high thresholds for both (iowait 20% / 40%, steal 10% / 25%).
- CPU utilization no longer counts guest time twice on a host that runs VMs itself.
- The collector comes with `probe/v7.0.31-r14`; a host monitored by the core server needs the core's
  copy of `argus_linux_ssh.py` updated too (`setup-core.sh` installs it).

**TCP ports add-on:**
- A new add-on in host settings checks that TCP ports accept a connection: a mail server's SMTP, a
  Windows box's RDP, a database, a firewall's admin port. List them as `port` or `name:port`
  (`SMTP:25, RDP:3389, 8443`); each becomes its own sensor under **TCP** with its connect time and a
  red band while it doesn't answer, and says why it doesn't (`connection refused: nothing listens on
  3389`, `no answer within 3 s`, `no route to host`). Alerts when a port stops answering and when it
  is slow to connect. The probe checks every port of a host at once, once a minute (`argus_tcp.py`,
  in `probe/v7.0.31-r14`).
- Add-on options are now checked like device-class options before they reach the probe: the DNS
  add-on's names and port, and the TCP ports list.

**Backups:**
- The core now backs itself up. **Settings, Backups** turns on a daily backup at a time you choose. One
  archive holds Argus's database, the Zabbix database (with its metric history, or only its settings)
  and the configuration, keys and certificates a new core needs to take over, including the CA the
  probes trust. The newest archives are kept on the VM (7 by default). With a passphrase set they are
  encrypted (gpg, AES-256) and can be exported to an SMB share, an NFS export, rsync over SSH (Argus
  makes the key) or an S3 bucket, where the newest are kept as well. **Back up now** and **Check the
  target** act at once; the status line shows the last backup, the free space and the last export,
  and a system notice goes out when a backup or an export fails or none succeeded for 36 hours.
- `argus-restore` restores an archive onto a new core in one command, after checking its checksums
  and versions: the probes reconnect by themselves. `argus-restore inspect` checks an archive without
  changing anything. The guide is [docs/backup-and-restore.md](docs/backup-and-restore.md).
- The tools run on the core host: the core VM image has them from `core-vm/v0.1.3`; on an existing
  core, run `deploy/core/host/install-backup.sh` once (or `setup-core-patching.sh` from a checkout).

**Maintenance windows:**
- **Configure, Maintenance** plans the times when some hosts' alerts should wait: a nightly backup, a
  monthly parity check, a patch night. A window covers sites (with their subgroups) and single hosts,
  and runs once, every day, on chosen weekdays or on a day of the month (or its last day), in the
  Argus timezone, for 5 minutes to 7 days; it may run past midnight. Admins and helpdesk edit them,
  viewers see them.
- While a window is on its hosts keep collecting and their problems stay on screen, marked as in
  maintenance in the tree, on the host card and in the Overview, and nobody is alerted about them.
  When it ends, whatever is still wrong is alerted straight away.

**Quiet hours:**
- **Account, Quiet hours** lets each user quiet their personal channels overnight (or any daily
  stretch): only problems at or above a chosen severity come through (High by default). A quieter one
  that is still open when the quiet hours end is sent then. Shared channels are not affected.

**Per-site visibility:**
- A helpdesk or viewer account can be limited to some sites in **Users, Sites** (a site covers its
  subgroups; empty means every site; an admin always sees everything). Such a user sees only those
  sites' hosts and sensors everywhere (the tree, the Overview and pills, problems, triggers, history,
  search, charts and probes) and can act only on them; anything else answers as if it didn't exist.
  The server enforces it on every request.
- Alerts follow: their personal channels serve only their sites, an email channel that goes to every
  registered user sends them only their sites' alerts, and news about the whole install isn't sent to
  them. The labels above the cross-site lists name their sites.

**Master sensors:**
- A device whose master sensor is down is one incident in the lists too, as it already was in the
  alerts. The Overview and the Error and Warning lists show the master ("Reachable (ICMP)" when the
  device is gone, the collector when only its service stopped, the probe when a whole site is out of
  reach) with how many sensors it holds; the others fold behind it, and **Show held** lists them under
  it, dimmed, each with the master it waits on. The status pills count them apart, so a dead device
  counts as one error (the Errors pill says on hover how many more are held).
- Status pages do the same: a dead device is one row, the master's, "holding 4 other sensors", and
  one error in the pills.
- A collector no longer holds the ping's own sensors: packet loss or a slow response while the service
  is down is a network problem of its own, and alerts (and lists) as one.

**Charts:**
- An interface's traffic chart shows how much it moved over the range beside the range tabs ("In
  1.24 TB · Out 301 GB over 30 days"), and so does a VM's disk read and write chart.
- The graph in an alert for an up/down sensor (ping, a collector's reachability, an HTTP endpoint, a
  TCP port, a service or container) shades the time it was down in red and reads "up" and "down", like
  the app's Downtime band. It used to fill under a 1/0 line, so the red blocks were the time it was up.

**Fixed:**
- Alerts: a device read through a collector (AdGuard Home, Home Assistant, a UPS through NUT, XCP-NG,
  Linux over SSH) that went down completely sent no alert at all, since 0.5.9. Its ping and its
  collector were both down, and as its two master sensors each held the other's alert back, so every
  alert on the device stayed held. The ping now ranks above the collector: the machine going down
  sends "Unavailable by ICMP ping", and a service that stops while the machine still answers sends the
  collector's "unreachable" alert, as intended. A device already down when the new version starts
  alerts then.

## [0.5.19] - 2026-09-30

**Uptime:**
- Every host card opens with its uptime over 24 hours, 7 days and 30 days and a strip of its last 60
  checks, measured on its master sensor when that is an up/down one, else on its ping. An up/down
  sensor (ping, an HTTP/HTTPS or TCP check, a collector's reachable flag, a DNS name resolving) shows
  the same in its chart, plus one bar per day for the last 30 days. 7 and 30 days are calendar days in
  the Argus timezone, from Zabbix's hourly trends weighted by their samples. A figure never rounds up
  to 100%: one missed check in a month reads 99.99%.
- Status pages show the average 30-day uptime of their hosts in the header, and list the hosts under
  100% (worst first, at most ten) when there's nothing wrong. Complete days are kept in Argus, so a
  page covering hundreds of hosts reads only today from Zabbix.

**Linux services and containers:**
- The Linux (SSH, agentless) class can watch the host's services. List systemd units in **Services
  to watch** (`nginx, jellyfin`) and each gets a sensor under Services reading Running or Down, from
  `systemctl show` (no extra rights); a Down one says why (`failed (failed), result exit-code`, `not
  found`). Give **Containers to watch** a name pattern (`^(jellyfin|immich.*)$`) and each matching
  Docker container gets a sensor under Containers: Running when up and healthy, Down when exited,
  restarting, paused, unhealthy or removed, with docker's status as the reason. Each alerts on its own
  (Service down / Container down) and has an uptime.
- Containers need the SSH login to run `docker ps`, which on a standard install means the `docker`
  group: root-equivalent on that host (see docs/hosts/linux-ssh.md). Without it the container sensors
  read "not supported" with that reason instead of Down.
- The collector script that reads them ships in the core's scripts and in `probe/v7.0.31-r13`;
  until a probe runs it the options have no effect.

**Incident history:**
- A new **History** page lists what went wrong and when across the fleet: each problem at Warning and
  above with its start, how long it lasted (or that it is still open), the sensor, who acknowledged it
  and the reason the device's collector gave. 24 hours to 90 days, Errors + Warnings or Errors only,
  filterable by host, sensor or reason. Every host card ends with its own last 30 days.
- The problems Argus raises itself (a sensor that stopped collecting, an interface that stopped
  answering) have no Zabbix event, so Argus now logs when each one opens and closes; they appear in
  the history from this version on.
- A drilled-down sensor ends with its own history, open: its incidents over the last 30 days (every
  channel of a group, such as a ping's loss and latency), read from its own triggers so a busy host's
  other incidents can't push them out.

**Look:**
- Panel titles carry a small label above them: the sidebar section, and for a list across sites its
  scope ("Watch · all sites" over Active problems, "Admin" over Users).
- State dots (a host's badge in the tree, a group's dot, a host's problem list) have a soft halo in
  their own colour, and a dot that needs someone's attention pulses: a warning or error nobody has
  acknowledged yet (a group pulses while any of its hosts does). OK, acknowledged, paused and hidden
  dots keep a steady halo, and nothing pulses when the system asks for reduced motion.

## [core-vm/v0.1.2] - 2026-09-30

Refresh of the core appliance golden image (review waves 2 and 3, and the collector work since
v0.1.1). Existing VMs are not changed by this; it is what a new deployment gets. Argus and the
updater are pre-pulled at build time from their `:latest` images (v0.5.18 and v0.2.10 today).

**Security:**
- Docker is installed from Docker's apt repository with the signing key's fingerprint pinned, instead
  of the `get.docker.com` script.
- The Zabbix and TimescaleDB repository signing keys are checked against pinned fingerprints before
  they are trusted (`setup-core.sh`, the same script the manual install runs).

**Collectors** (the scripts the core runs for the devices it monitors itself):
- XCP-NG certificate pinning, the SSH collector's input checks, and the controller certificate policy
  for UniFi sweeps, including reading a controller's certificate so it can be pinned.
- The collectors wipe their own command line once they have read it, so a password passed as an
  argument doesn't linger in `ps`.
- Every failed poll says why (the reason Argus shows on hover and in the alert), and a network scan
  matched to a UniFi controller keeps the device MAC the controller knows.

## [0.5.18] - 2026-09-29

**Why a sensor isn't reading:**
- Argus shows the reason next to a sensor that can't read. Hover a `not supported` value (or tap it on
  a phone) for Zabbix's error, and a down collector's `Not reachable` for what the collector reported.
  The Overview, the status lists and the host page all have it, and the alert carries the same text
  ("Not reachable: Permission denied (publickey)"). The reason is left off the public status pages.
- The UniFi templates (switch, AP, gateway, console) pass on the controller's own reason for a refused
  call instead of the bare status, with a hint for the usual causes:
  `UniFi API HTTP 400 (api.err.NoSiteContext) - no site named "x": the Site name must be the internal
  name (the first site is "default")`; a rejected API key, `http://` on the HTTPS port, a URL that
  isn't a UniFi OS console. A device missing from the site says to check the MAC.
- The collector templates (Linux by SSH, XCP-NG, NUT, PeaNUT, AdGuard Home, Home Assistant, DNS
  resolution) gain a **Collection error** item next to their down flag, filled by the collector:
  ssh's own message, upsd's `ERR` answer and what it means, the XAPI failure, the HTTP status, the
  DNS rcode. The collector scripts that print it (SSH, XCP-NG, NUT, DNS) ship in the core's own
  scripts and in `probe/v7.0.31-r11`; until a probe runs them the item stays empty.
- The AdGuard, Home Assistant and PeaNUT login hints name the setting to check the way Argus labels
  it, not the Zabbix macro.

**Discovery:**
- A UniFi gateway added from a network scan got the wrong MAC. A gateway answers on its LAN with a
  derived address, not the device MAC the controller knows it by, so the scan matched it to the
  controller by IP but kept the scanned MAC, and every sensor failed with
  `HTTP 400 (api.err.UnknownDevice)`. A matched row now carries the controller's own MAC, and adding
  the device uses it (probe scans from `probe/v7.0.31-r12`, which carries the probe side). A host added
  this way before the fix keeps its wrong MAC: its sensors read "not supported", with a hint to use
  the MAC the controller lists. Correct it in the host settings.
- The UniFi templates add that hint for `api.err.UnknownDevice`.

## [0.5.17] - 2026-09-29

**Container health:**
- The image declares a Docker `HEALTHCHECK`, so `docker ps`, the Unraid GUI and Dockhand show the core
  as healthy or unhealthy. The image is distroless (no shell, no interpreter), so the binary checks
  itself: `argus healthcheck` asks the running server's `/healthz` over the loopback (at
  `ARGUS_LISTEN`) and exits 0 or 1. Every 30 s, a 60 s start period, unhealthy after 3 failures.
  Zabbix being unreachable is not a container fault and stays out of it.
- The probe (`probe/v7.0.31-r10`, `/app/healthcheck.py`) and the updater sidecar (`v0.2.10`,
  `/app/healthcheck.sh`) gained theirs in their own releases. A container picks the check up when it
  is recreated from the new image (an update through Argus, the Unraid GUI or Dockhand), not on a
  plain restart.

## [0.5.16] - 2026-09-29

**Probe process autoscaling:**
- Argus sizes each probe's Zabbix process counts (ICMP pingers, pollers, unreachable pollers, agent,
  SNMP and HTTP agent pollers, trappers, history syncers, preprocessing workers) from the probe's own
  load: every 15 minutes it reads each kind's busiest hourly average from the Probe health host and
  raises a count whose busiest hour reached 60% (aiming for 50%), or lowers one that stayed under
  20%, never below the image's default and never above a ceiling per kind. Counts are judged only
  after they have run for 6 hours. A `ZBX_START*` variable set on the container wins.
- The counts go out at check-in and the probe starts with them. With the updater sidecar the probe
  restarts to apply a change (at most once per 6 hours; a few seconds down, collected data kept);
  otherwise it applies at the probe's next start. Needs probe image 7.0.31-r8 and argus-updater
  0.2.9 or newer; older ones keep their counts.
- New setting **Process autoscaling** (Settings, now **Probes**; `ARGUS_PROBE_AUTOSCALE` = `restart`
  (default), `next-restart` or `off`). The Probes page gains a **Processes** panel per probe (running
  count, busiest hour, what Argus wants) and a tag while a change waits; each change is sent once as
  a system notice.
- The Settings section "Probe enrollment" is now "Probes".
- Every raise is judged at the next evaluation: when the busiest hour didn't come down by at least
  a third of what the raise predicted, the count goes back and that kind is held until the probe's
  CPU count changes (or an admin presses **Try again** on the Processes panel).
- Short on CPU: probes report their CPU count, a container CPU limit and the load average at every
  check-in (probe image 7.0.31-r9). When the busiest hour's load average reached the usable CPUs,
  Argus adds no processes. Short on CPU or holding a kind, the probe gets one system notice while it
  lasts, with the advice for a VM (more vCPUs, or split the site) or a container (the Docker host
  may be short on CPU, or the site needs a second probe). The Processes panel shows the CPU line,
  the held kinds and a "short on CPU?" tag in the Health cell.

**Server-side census:**
- The sensor census behind the status pills, the Overview and the status pages is built in the
  background on the core and served from memory: every 20 s while someone has looked in the last
  10 minutes, every minute otherwise, one build shared by every open browser and status page.
  Pages answer at once instead of waiting for Zabbix, and the pills no longer sit on dimmed counts
  after a reload.
- A change made through Argus (acknowledge, pause, hide, a threshold, any other change by a signed-in
  user or through an alert link) marks the census stale, and the next read waits for a fresh one, so
  the result of an action shows at once.
- The app fetches every state's count plus only the rows on screen (`GET /api/census`): the
  Overview's errors, warnings and acknowledged sensors, and the OK, paused or hidden list only
  while it is open. The whole census is no longer downloaded every 30 s.
- The header counts down to the next refresh ("Refresh in 18s", then "Refreshing…"): the app times
  each fetch to just after the server's next build (`next_ms`), so the count runs to a fresh answer
  every 20 s. The data's age on the server is in the tooltip.
- Template items are left out at Zabbix instead of after the download.

## [0.5.15] - 2026-09-29

**Security review, wave 3 (the Low and Info findings):**
- `ARGUS_SECRET_KEY` is stretched into the at-rest key with argon2id instead of a single SHA-256, so
  a passphrase can't be guessed offline from a database dump. A database written under the old
  derivation is re-encrypted once at the first start (same variable, nothing to do); an older image
  started on it afterwards refuses to run, as with any key mismatch.
- Sign-in: the per-account failure counter no longer locks a user out (over the limit a wrong password
  is answered 429, the right one still signs in and clears it); anonymous passkey-login begins are
  capped per address; concurrent argon2 runs are capped at eight; the rate limiter hashes long keys
  and caps how many it tracks.
- A second-factor challenge is redeemed in the statement that deletes it, so two completions racing
  with the same code yield one session.
- One-shot update hand-outs and the paired deletes (discovery jobs, notification state, proxy records)
  run atomically; a probe update taken by a concurrent check-in is handed out once.
- Per-day counters: at most 20 items and 93 days per request, one item list per host.
- Zabbix RPC errors reach non-admin users without their `data` part; the password-reset log line no
  longer records the requested address; the public features flag is answered from a short cache.
- A probe check-in token is issued only for a proxy that exists in Zabbix.
- CI: `govulncheck` and `npm audit` (production dependencies, high and above) gate the image build;
  the image builds with a pinned, supported Go (1.27) and its own toolchain (`GOTOOLCHAIN=local`).
- `setup-core.sh` verifies the Zabbix repository keyring and the TimescaleDB signing key against their
  published fingerprints before apt trusts them.
- Collector passwords (XCP-NG, Linux SSH): the scripts wipe their own command line once they have read
  their arguments, so the password shows in `ps` only during start-up (Zabbix can pass a value to an
  external check no other way; the proxy's configuration database holds the same macros). The core's
  mirror of the scripts and the XCP-NG and Linux SSH guides say so; the earlier guide claim that the
  SSH password never appeared on a command line was wrong.

**Fixed:**
- Discovery: the history list failed since 0.5.14 (the job select gained the certificate column, its row
  scan did not), so the page showed no discoveries and new scans seemed not to start. Nothing was lost;
  every scan is listed again.

## [0.5.14] - 2026-09-29

**Security:**
- **UniFi controllers get a site scope and a certificate policy.** Only the probes of a controller's
  sites receive it (and its API key) for scans and sweeps; empty means every site, as before. The
  certificate is now checked: *Verify* against the system roots (saving a console with its own
  self-signed certificate shows it to you and offers to **pin** its SHA-256, so a later swap fails
  loudly), *Pin a fingerprint* (paste one for a controller only a probe can reach), or *Ignore*.
  Controllers saved before this keep working unchanged (they are marked *Ignore*; edit them to pin).
  The probe scripts apply the same policy (probe image 7.0.31-r5 or later). For a controller only a
  probe can reach, **Ask a probe for the certificate** has that probe read it over its check-in
  channel (a "cert" job: no API key travels, nothing is imported) and offers the same pin dialog
  (probe image 7.0.31-r6); a sweep refused by the check reports the certificate it saw, and the
  result page offers to pin it.
- **XCP-NG hosts pin their XAPI certificate** (`{$XCP.TLS}` in host settings, default *pin*: trust
  on first contact, remembered on the collector, a change refused and reported as `tls_error`);
  *verify* and *ignore* are the alternatives. Existing XCP-NG hosts get the default at the next
  template reconcile.
- The Linux-by-SSH collector refuses a login name that would read as an ssh option, a port outside
  1-65535 and a key outside `/var/lib/zabbix/ssh/`, and passes the target after `--`.
- Revealing a probe's break-glass credential is logged with the admin who did it.

**Fixed:**
- A cancelled or timed-out passkey setup or sign-in reads as such ("Passkey setup was cancelled or
  timed out. Nothing was added.") instead of the browser's spec-pointing error text; the same for an
  authenticator that already holds a passkey, a non-https address and an unsupported authenticator.
- **Updates are verified by digest.** Every update the core hands out keeps naming a tag (`latest`,
  `testing`, a version), and now also carries the digest that tag pointed to at hand-out time: the
  fleet target and one-shot updates in the probe check-in (`target_digest`, `update_digest`,
  `updater_update_digest`), the core's own update request (`digests` per tag, since the sidecar may
  pull the preserved channel rather than the requested tag) and the sidecar self-update request
  (`digest`). An updater from `v0.2.7` refuses to run a pulled image whose digest differs (an image
  swapped under the tag in between); an older updater ignores the fields. Digests are resolved from
  the registry with a 5-minute cache; when the registry can't be asked, the update goes out without
  one and is applied unverified, logged as such.
- **CI supply chain:** every GitHub Action is pinned to a commit (`# vN` comments say which release),
  workflow permissions are granted per job (read-only where nothing is published), and image pushes
  carry provenance and SBOM attestations.

## [core-vm/v0.1.1] - 2026-09-29

Security hardening of the core appliance golden image (review wave 2). Existing VMs are not changed
by this; it is what a new deployment gets.

**Security:**
- **A setup code guards the first-boot page.** The one-form setup is reachable by anyone on the VM's
  network until it completes and creates every credential of the core, so the VM now prints an
  8-character code on its console (and the console login banner) and refuses a submission without it;
  ten wrong codes replace it. Forms carry a per-boot CSRF field; answers are `no-store`.
- **Argus is served over https from the start.** A new "Enabling HTTPS" step issues this VM a server
  certificate (its hostname and address) signed by the monitoring CA and puts nginx on `:443` in
  front of Argus, which now listens on `127.0.0.1:8081` only. The Public URL defaults to
  `https://<vm-ip>`, Argus trusts nginx (`127.0.0.1`) as its proxy, session cookies are Secure, and
  probes enrolled against the appliance check in over TLS. `http://<vm>/` redirects to https once
  setup is done (to the Public URL when it is https, else the VM's own address; never to whatever
  `Host` the request carried). Install `/etc/argus/pki/ca.crt` on your PCs to lose the browser
  warning, or front the VM with your own certificate.
- **SSH password login is limited to the administrator account** created at setup: off for every
  other account and for root (keys work for all). The local admin user is no longer in the `docker`
  group.
- **No console to read the setup code from?** A disk labelled `ARGUSSEED` with an `argus.env`
  holding `ARGUS_SETUP_CODE=XXXX-XXXX` supplies your own code (`mkisofs -V ARGUSSEED -o code.iso
  dir/`), attaching media being the same proof of hypervisor control as reading the console.
- `setup-core.sh` (also used by the image build) doubles a quote inside the database password
  before it goes into SQL, and fetches the Zabbix release package over https only.

## [0.5.13] - 2026-09-29

Security hardening from a code review of the three repositories (wave 1: the core). Nothing here
changes what Argus monitors; what changes is who can make it do things, and what leaves it.

**Security:**
- **Password-reset links** are built from the Public URL, or from the address the request was made
  to only when that address is one Argus is known by (Allowed FQDNs and IPs, localhost). Before,
  with no Public URL, the link pointed at whatever `Host` the request carried, which the sender
  chooses: a reset asked for a victim's address could deliver the token to the attacker's server.
  With neither set, no reset email is sent (and the login page hides "Forgot password").
- **Probe settings can't carry anything but an address.** "Probe core host" must be a host or
  `host:port` (it is written into every probe's configuration), the check-in URL handed to probes
  comes from the Public URL alone, and a seed ISO's values are validated the same way.
- **Disabling a user, resetting their password (admin) or changing your own** ends the affected
  sessions at once. Before, a disabled account kept working until its session expired.
- **Passkeys require user verification** (PIN, fingerprint, face) at registration and at every
  login: a passkey signs in without a TOTP prompt, which is only sound when the authenticator
  verified the user. A key whose signature counter didn't advance (a cloned credential) is refused.
  Adding a passkey asks for the password again; turning two-factor off asks for a current code too.
- **A TOTP code works once.** The time step of the last accepted code is kept per user.
- **Cross-site requests are refused on every state-changing API call**, not only once Allowed
  FQDNs and IPs is filled in: the browser's `Sec-Fetch-Site`/`Origin` must be Argus's own, and a
  body must be JSON. This closes a sibling-subdomain write and a login-CSRF form post.
- **Security headers** on the app and the API: a Content-Security-Policy (no inline scripts; the
  theme bootstrap moved to a file), `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: same-origin`, a `Permissions-Policy`, and `Cache-Control: no-store` on `/api`.
- **Session cookies follow the Public URL:** `Secure` by default when it is https (the
  `ARGUS_COOKIE_SECURE` switch still wins), and named `__Host-argus_session` then, which a browser
  only accepts from a secure, path-wide, domain-less setter. Everyone signs in again once.
- **Trusted proxies:** `true` now believes forwarded headers only from a private or loopback peer
  (a proxy beside Argus); a public peer is a direct client whatever it sends. A forwarded value that
  isn't an address is ignored. The Unraid template no longer defaults the setting to `true`.
- `/api/health` (the Zabbix version and connection error) is admin-only; `/healthz` stays public.
- **Discord webhooks must be `https://discord.com/api/webhooks/...`** (also `discordapp.com`, ptb,
  canary), on admin and personal channels alike; the sender refuses anything else and never follows
  a redirect. Before, any signed-in user could point a personal channel at an address inside the
  network and have the core post there.
- **Channel secrets are write-only:** the SMTP password, Telegram bot token and Discord webhook are
  never sent back to the browser (a "set" flag is); editing a channel with the field blank keeps the
  stored one. A Telegram token that surfaced inside a transport error is redacted before the error
  is logged, shown or sent as a system notice.
- **Viewers no longer receive SNMP communities** in host settings or proxy defaults (blank for them,
  like the v3 passphrases are for everyone).
- **At-rest encryption fails closed:** a key that doesn't match the database stops Argus at start
  with a clear message instead of running with unreadable secrets (and handing ciphertext out as
  tokens); an unreadable value decrypts to nothing, never to its ciphertext; a corrupt `secret.key`
  is reported, not silently replaced. `ARGUS_SECRET_KEY_RESET=true` drops the unreadable secrets
  once so credentials can be entered again (two-factor is switched off for the users affected).
- **Enrollment tokens are claimed atomically**, so two probes racing with the same token can't both
  enrol; a failed enrollment hands the token back. The error a probe sees no longer carries Zabbix's
  internal detail (it is in the log).
- **Device-class settings that reach a command line** (the SSH user, port and key path) must match a
  pattern; a threshold must be a plain number (`ParseFloat` also took `NaN`, `Inf` and hex, which
  silently turned a sensor's triggers "unknown"); creating a host accepts only the class's own
  macros, its thresholds and the HTTP add-on, as editing already did.
- Updater image tags handed to the sidecars must be `latest`, `testing` or a version.
- Smaller: SMTP in `starttls` mode refuses a server that doesn't offer STARTTLS instead of sending
  in clear; a probe's certificate request must carry an RSA key of 2048 bits or more (or an EC key);
  Discord messages escape Markdown in host names, sites and acknowledgement notes; Zabbix and GitHub
  responses are size-bounded; expired sessions, second-factor challenges, reset links and passkey
  ceremonies are pruned; `foreign_keys`/`busy_timeout` apply to every SQLite connection, not only the
  first; the database file is 0600; the HTTP write timeout covers the longest handlers.

**Changed:**
- Adding a passkey and turning two-factor off ask for the password (and a current code); Discord
  webhooks must be Discord's own; channel credentials can't be read back out of Argus.

**Fixed:**
- An update handed out as `latest` (or `testing`) no longer ends in a false "failed to update"
  notice 20 minutes later: a rolling tag names no version, so the "still on ..." check could never
  be satisfied. The hand-out now counts as done when the version changed since (the "updated to"
  notice already sent) or when the probe already runs the newest published version.

## [0.5.12] - 2026-09-28

**Changed:**
- Status pages list the same rows as the Overview, in the same order (priority first): each sensor
  that needs attention, with the reason under its name, and new **Value**, **Trend** (mini graph) and
  **Priority** columns.

**Fixed:**
- A problem that belongs to no sensor (a Zabbix agent or SNMP endpoint that stopped answering) now
  shows in the Overview and counts in the status pills, as a "Zabbix agent" or "SNMP" row. Before,
  only the host's problem list and the status pages had it, so the counts disagreed.
- Reachability sensors read as words in the Overview, the status pages and alerts: "No reply to
  ping", "Not reachable" (the HTTP/HTTPS check and the collectors' reachable flags) instead of 0 or 1.

## [0.5.11] - 2026-09-28

**Added:**
- **Status pages** for a wall screen. A new admin screen, **Status pages**, makes read-only dashboards
  that open with a secret link instead of a login. The screen is a PRTG-style alarm list: one row per
  open problem (host, site, problem, the sensor with its reading, how long it's been open), worst and
  newest first, with **Errors / Acknowledged / Warnings** pills in a slim top bar to switch lists.
  With no errors it just says "All systems operational". A list longer
  than the screen pages itself every 15 seconds; data refreshes every 30, and a lost connection is
  flagged while the last known state stays up.
  - Opening the link swaps it for a cookie and a plain `/status` address, so the secret doesn't sit
    in the address bar. The link is kept encrypted, so an admin can copy it again with **Show link**.
  - Each page can be limited to networks (like your LAN) and given an expiry. **New link** shuts the
    old one out at once; deleting the page does too.
  - The page shows only its sites, never addresses, credentials or settings, reads through its own
    endpoint (never the app's API), and is kept out of search engines and other sites' frames. It
    uses the app's colours: the pills are tinted with their state's colour, and the tab icon is a
    green check with no errors and a red "!" with any. The pills start grey and take their colour
    once the first data arrives.
- **A clock in the top bar**, with how long ago the status data refreshed, in Argus's timezone.
- **Time format** in Settings -> General (`ARGUS_TIME_FORMAT`): 24-hour (the default) or 12-hour,
  for Argus's clocks and the status pages.
- A status page opens on a chosen list with `#acknowledged` or `#warnings` at the end of its
  address (the secret link too); a plain address always opens on errors, and picking a pill updates
  the address.

**Changed:**
- The status pills read by urgency: Errors, Acknowledged, Warnings, OK (then Paused and Hidden).
- **Trusted proxies** are a setting now (**Settings -> Reverse proxy**, still `ARGUS_TRUST_PROXY`,
  which locks it). Besides `true` (one proxy), it takes the proxies' addresses or networks, like
  `10.0.0.2, 10.0.5.0/24`: forwarded headers then count only from those, and a chain of proxies
  (NetScaler in front of HAProxy) resolves to the real client.

**Fixed:**
- Behind a reverse proxy, Argus took the client's address from the first `X-Forwarded-For` entry,
  which the client itself can set. It now reads from the right, where the proxies add the address
  they actually saw, so the address can't be faked to dodge the login rate limit or a status page's
  network list.

## [0.5.10] - 2026-09-28

**Added:**
- **System notices.** Argus can now tell you about itself, as neutral `[INFO]` messages sent once
  each. Turn on **System notices** on any channel, shared or personal (off by default):
  - a new Argus release is available, and when a self-update finishes or fails;
  - a probe or its updater is behind the newest release (for six hours: the fleet normally updates
    itself sooner), and when a probe self-update goes through or doesn't;
  - security updates still pending after two days, or a reboot needed for a day, on the core VM or
    a probe VM (security updates normally apply by themselves);
  - a Zabbix server update is available for the core;
  - a discovery scan or UniFi sweep finished, with how many devices it found and how many are new;
  - a notification channel keeps failing (for half an hour): told on your other channels, and a
    personal channel's failure only to its owner.

  A probe's notices follow the channel's sites. A condition that clears and comes back is told
  again.
- A channel's alert level can be **None**, for a channel that only carries system notices.

**Changed:**
- While the status pills load after opening Argus (it reads every sensor, so it takes a moment),
  they show your last visit's counts, dimmed, instead of a row of zeros.

## [0.5.9] - 2026-09-28

**Added:**
- **Escalation.** Every notification channel, shared or personal, has a **Notify after** setting:
  Immediately (as before), or only if the problem is still open and unacknowledged after 5 minutes
  up to 4 hours. For example, the team's Telegram at once and the managers' email after 30 minutes.
- **Reminders.** A channel can repeat an open alert (**Remind every** 15 minutes up to a day) until
  someone acknowledges it: "[HIGH REMINDER] ... Still open after 1h 5m (reminder 2)". Off by default.
  **Remind for** sets which severities are repeated, separately from the alert floor: for example,
  alert on warnings and errors, but remind only about errors.
- **Acknowledged notice.** When someone acknowledges an alert, the channels that got it are told who
  took it and their note ("[ACKNOWLEDGED] ... Acknowledged by Alice"), so the team knows it's handled.
  Acknowledging also stops escalation and reminders; un-acknowledging resumes them.
- **Master sensor.** While a device's master sensor is down, its other sensors don't notify, so an
  unreachable device sends one alert instead of one per sensor. The master is the ICMP ping by
  default; pick another sensor, or none, in the host's settings. Alerts held this way go out if they
  are still open once the master is back.
  - Devices read through a collector (UPS via NUT, XCP-NG via XAPI, Linux via SSH, AdGuard Home,
    Home Assistant) also have the collector's reachability as a master: if the service stops but
    the machine still pings, you get "monitoring is unreachable" instead of one alert per reading.
  - A probe that isn't reporting holds the alerts of every device at its site, and Zabbix's own
    "proxy last seen" checks for it, so a probe outage sends only the probe's own alerts and one
    recovery.
- **Alerts when monitoring stops working.** Zabbix raises no problem for these, so nothing alerted
  until now:
  - a sensor that goes **not supported** after collecting before (a collector script that fails or
    times out, like a UPS sensor losing its NUT server): an error, "<sensor> stopped collecting",
    on its third failed check in a row (about 2 minutes for a sensor checked every minute), with
    Zabbix's reason as the reading;
  - a device's **Zabbix agent or SNMP** that stops answering while the device itself is up: an
    error, "Zabbix agent not reachable" or "SNMP not responding", with Zabbix's reason.

  Both show in the Overview and on the host like any problem, and can be acknowledged; while the
  device is down, its master sensor holds them. A sensor that never had a value (a process that
  isn't started, a second WAN a gateway doesn't have, a reading the hardware doesn't report) doesn't
  apply to that device, so it never alerts; neither do sensors already not supported when you
  update.
- **Alert delay setting.** How long a problem must last before anyone is notified (60 seconds until
  now, fixed) is now in **Settings -> Alerting** (`ARGUS_ALERT_DELAY_SECONDS`).

**Changed:**
- Notification channels (shared and personal) open in a dialog instead of an inline editor, with
  one row each for the channel itself, what it receives (sites and severity), and escalation and
  reminders.
- A channel's severity is now one of two choices matching what the app shows: **Warnings and
  errors** or **Errors only**. Errors only covers every problem shown in red (Zabbix's Average, High
  and Disaster); the old "High & up" skipped Average errors like "HTTP/HTTPS endpoint down". Channels
  set to High & up or Disaster only become Errors only.
- Reminders, acknowledged notices and recoveries go to exactly the channels that received the alert.
  A channel set to errors only now also hears when an incident it was alerted about ends, even if it
  eased to a warning first; a delayed channel that was never alerted gets no recovery either.

**Fixed:**
- Adding a device whose name has characters Zabbix doesn't allow in a host name (like `+`, `#`,
  `/` or accented letters) no longer fails with "Incorrect characters used for host name". The name
  you typed stays the visible name, and Argus derives a technical name Zabbix accepts
  ("U6+ Salotto" becomes "U6_ Salotto"). Editing the technical name in host settings explains which
  characters it can use.
- Sensors from Zabbix's own templates that count seconds but declare no unit (like a proxy's "Last
  seen, in seconds") now read in seconds in the app, on charts and in alerts, instead of a bare
  number.
- A ping alert reads "No reply to ping" instead of a bare 0.
- The status pills and a host's problem count in the tree include every problem: a sensor that
  stopped collecting counts as an error, and so does a problem on a sensor the sensor lists leave out
  (a collector's "reachable" flag, a sensor from one of Zabbix's own templates), which now also
  appears in the pill's list while it lasts.
- Alert thresholds carry the sensor's unit, scaled like the reading: "688 s (threshold >600 s)",
  "96 % (threshold >90 %)".

## [0.5.8] - 2026-09-28

**Added:**
- **Probe health.** Every probe now gets a **Probe <site>** host in its site group, measured by the
  probe itself, so its health shows up like any other device's sensors and alerts.
  - **Probe unreachable:** a warning after 3 minutes without data from a probe and an error after
    5. Until now the Probes page showed a probe as offline, but nothing sent an alert.
  - Unsent values waiting for the core, items running more than 10 minutes late, history and
    configuration cache use, and a **Process load** sensor with one line per process type
    (headlined by the busiest).
  - Every sensor has a warning and an error threshold, editable on the Thresholds screen.
  - The Probes page's **Health** cell shows each probe's worst open problem (ok, warning or
    error) and opens its sensors in Monitoring.
  - In the Monitoring tree it's pinned first in its site, even in sites you reordered by hand
    before it existed; move it with Reorder and your placement sticks.

**Changed:**
- The manual probe deploy files (`deploy/probe/run-probe.sh`, the Unraid proxy template) start 5
  ICMP pingers, matching the `argus-probe` image from `probe/v7.0.31-r3`.

**Fixed:**
- **No more false "RESOLVED" when an alert escalates.** A warning that turned into an error (or an
  error that eased to a warning) on the same sensor sent a RESOLVED for the old one, although the
  problem was still going on. RESOLVED now goes out only when the sensor has actually recovered.
  This applied to every sensor with warning and error thresholds, not just probes.
- A "no data" alert, like "Probe unreachable", now reads **No data for 4m (since 00:56)** instead of
  the last value that arrived before the data stopped, and it's sent as soon as its period is up:
  it skips the extra one-minute flap guard (the no-data period already is one), and the probe's
  uptime check runs every 30 seconds. The probe alerts now arrive about 3.5 and 5.5 minutes after
  a probe goes quiet, instead of 5 and 7.
- **"Recovered after" now covers the whole incident.** It used to count only how long the last alert
  was open, so a probe down for 9 minutes read "Recovered after 4m" (the error alert only started at
  5). It now counts from when the incident began: when the data stopped for a "no data" alert, and
  from the first alert when a warning escalated to an error.
- Alerts show their threshold from the real value (for example `>=75`), now that Argus reads trigger
  expressions with their macros resolved.
- The Probes page counts a probe as **offline** after 2 minutes without contact (was 5), so it no
  longer reads "online" next to a "health: warning".
- Sparklines of percentages near zero (a cache at 0.002 %) no longer draw a tiny change as a
  full-height spike: they use the same minimum 10-point span as the big chart.
  - Argus creates the hosts for existing probes at startup and for new ones at enrollment. Deleting
    a probe deletes its Probe host too. They can't be added by hand or have their class changed.

## [0.5.7] - 2026-09-27

**Changed:**
- **Each Settings section saves on its own.** The single "Save changes" button at the top is gone:
  every section has its own **Save** at the bottom, which saves just that section and leaves
  unsaved edits in the others alone. Pressing Enter in a field saves its section. Data retention's
  button is now a plain **Save** too.

## [0.5.6] - 2026-09-27

**Added:**
- **Allowed FQDNs and IPs.** A new **Settings → Allowed FQDNs and IPs** list (also
  `ARGUS_TRUSTED_ORIGINS`) names the addresses people type in the browser to reach Argus, such as
  `monitoring.example.com` or `10.0.0.10`. With a list set, Argus refuses API requests for any
  other address and changes coming from other sites, which blocks DNS-rebinding and cross-site
  attacks on top of the existing SameSite session cookie.
  - Off until you fill it in, so upgrading changes nothing. The card shows the address you're using
    and suggests a list in one click.
  - The Public URL's host and localhost always work, and probes are never checked, so a typo can't
    take the fleet offline.
  - A save that would lock you out is refused, with the address to add. If you do get locked out,
    set `ARGUS_TRUSTED_ORIGINS=*` and restart.
  - A refused sign-in says why instead of "Invalid email or password".

## [0.5.5] - 2026-09-27

**Added:**
- **Data retention in Settings.** How long Zabbix keeps raw history and hourly trends, and
  TimescaleDB compression, can now be changed from **Settings → Data retention** instead of the
  Zabbix frontend.
  - The chart tabs set the limits: history can't go below 2 days (the 2d tab reads raw history) and
    trends below 7 days; a trend period under a year warns that the 1Y tab won't reach back a full
    year.
  - Shortening a period asks first, since Zabbix deletes the older data within the hour and it
    can't be recovered.
  - Needs a Super admin Zabbix token (Zabbix's rule for housekeeping); with any other token the
    section explains why it's read-only. Compression is only offered on TimescaleDB databases.

## [0.5.4] - 2026-09-27

**Fixed:**
- **An expired session now returns to the login screen.** When a session ended while the app was
  open (max lifetime, idle timeout, or signed out elsewhere), the app stayed on screen showing
  "unauthorized" and "Failed to load sensors" until a manual reload. It now goes straight to the
  login screen with a short "Your session has ended" note, and signing in again returns to the
  same view.

## [0.5.3] - 2026-09-26

**Changed:**
- Clearer example hints in the **Add probe** wizard's site field and the **New group** dialog.
- Documentation: the roadmap lists the threshold-banded graphs shipped in 0.5.2.

## [0.5.2] - 2026-09-26

**Changed:**
- **Graphs colour by threshold, not by state.** A sensor's chart used to paint the whole line in the
  sensor's current state colour (all red while a problem was open, all blue again once it cleared).
  It now colours by value: the normal colour inside the normal range, the warning colour only where
  the line is past the warning value, the error colour only where it is past high (mirrored for
  lower-is-worse sensors), with dashed reference lines at both values - so past excursions stay
  visible after the alert clears, across all history on screen.
  - The values are read from the sensor's own triggers (host overrides, fleet defaults and
    per-disk-type values already applied), so the chart matches what alerts.
  - On multi-channel graphs the **main channel** (the primary, when it is the only one on its unit:
    ICMP response time, a disk's Used %) is banded the same way; the other channels, and peer groups
    like drive temperatures, CPU cores or In/Out, keep their per-channel colours and draw the
    reference lines only.
  - Each reference line ends in a small **tag on its own axis** (amber for the warning, red for
    high) showing its value: outside the plot, so it never covers the data, and on the side of the
    scale it belongs to - on the two-axis ICMP graph the Loss tags sit on the % axis and the
    response-time tags on the ms axis. Tags line up with the axis numbers (same position and font);
    numbers a tag would cover are hidden (a tagged axis shows more numbers, so a readable scale
    remains, and they come back when that channel is hidden in the legend); nearby tags stack.
  - Sensors without a numeric threshold (up/down, state checks) are unchanged.
- **The graph in alert notifications is banded the same way** (Telegram / Discord / email): normal
  colour in range, warning and error colour only where the line crosses, dashed reference lines with
  the same value tags on the axis as the app. A recovery's graph now shows the spike that caused it
  instead of an all-green line. The channel Test button previews a banded sample graph.

## [0.5.1] - 2026-09-25

**Added:**
- **Thresholds UI (§D) - edit alert thresholds without touching Zabbix.** Thresholds are Zabbix user
  macros (`{$CPU.UTIL.WARN}`, `{$DISK.TEMP.HIGH}`, `{$PING.LOSS.WARN}`, ...) that until now could only
  be changed by hand-editing host macros in Zabbix. A new admin **Thresholds** screen (under Admin)
  lists the monitoring templates; clicking one opens a dialog that edits the fleet-wide default for
  each of its thresholds (so a switch's CPU band and a server's are edited independently, and each row
  shows the classes it affects plus how many thresholds you've customized). Overrides are stored in
  Argus and re-applied to the Zabbix templates after every startup reconcile, so a template re-import
  on an app upgrade can't silently reset them; a factory default is always one click away via **Reset**.
- **Per-device threshold overrides in host settings.** Each host's settings dialog gains a
  **Thresholds** section listing the thresholds that apply to it (its class templates + Base Ping),
  each showing the effective default as the placeholder; type a number to override it for that host
  only, blank it to fall back to the default. Disk temperature keeps its HDD/SSD/NVMe split.
- **Per-host sensor-category order (§D).** The order sensor categories read in a host view can be
  reordered per host, in that host's settings dialog. The built-in server / network / NAS shape
  profiles are the fixed default; a per-host order wins over it, and a category the host doesn't have
  is simply skipped. (There is no fleet-wide/per-class order override - the default is deliberately
  fixed.)

- **Every sensor now carries a warning AND an error threshold.** Six graded bands that shipped
  warn-only gained a high (error) band and trigger, so alerts can escalate: ICMP packet loss (high
  60%), ICMP response time (0.5s), DNS resolve time (1s), XCP-NG CPU (warning realigned to 85%, high
  95%), XCP-NG memory (85% / 95%), and HTTP response time (3s). Each shows as a warn/high pair in the
  Thresholds editor and is tunable there. The hard-failure triggers (host down, DNS not resolving,
  endpoint down) are unchanged.

- **Manage optional add-ons from host settings.** A host's settings dialog now has an **Add-ons**
  section to layer optional Argus checks on the fly (not just at add time): the **HTTP/HTTPS endpoint**
  (reachability + response time, with port/scheme) and **DNS resolution** (resolve names against the
  host). Each toggle links/unlinks its template (clearing its sensors on disable) and edits its macros.
  Add-ons are driven by a small registry, so new ones are one entry each; an add-on a host's class
  already provides isn't offered (it's managed as a class option). Supersedes the earlier HTTP-only
  toggle.
- **Change a host's device class in place.** A **Change class** control (admin) in host settings swaps
  a host to a different class without delete + recreate: it diffs and swaps the class's templates
  (Base Ping + add-ons untouched), keeps history for any template the old and new class share, adds the
  new class's interface type when the host lacks it (SNMP inherits the proxy default), and collects the
  new class's credentials. Sensors from templates only in the old class are removed (with a confirm).
  The host's Zabbix `argus.class` tag is kept aligned with the new class (preserving `argus.source`).
- **Disable alerts per sensor, granular to one channel.** A **Disable alerts** action (in a host's
  sensor view) turns off a sensor's Zabbix trigger(s) while the sensor keeps collecting and graphing -
  for when an alert isn't wanted. It works on a whole sensor/group, and, by expanding a multi-channel
  sensor, on a single channel (e.g. mute the temperature alert on just one drive while its siblings
  keep alerting). A muted sensor shows an "alerts off" tag; the state persists until re-enabled.

**Fixed:**
- **Ugreen NAS class woke parked disks every hour.** The class used the Zabbix agent2 SMART plugin
  (`smart.disk.discovery`, hourly) to enumerate disks; that plugin runs a full `smartctl` on every
  drive, which opens - and spins up - a standby disk, and then overran the agent timeout on the
  sleeping drive (the discovery showing as *Not supported / timeout*). It's replaced with a custom
  `ugreen.disk.discovery` UserParameter that reads the disk list, model and HDD/SSD/NVMe type purely
  from `/sys` (never opening a disk), so nothing in the class wakes a parked drive any more (the
  temperature reads already used `smartctl -n standby`). Existing Ugreen NASes need the agent
  re-deployed with the updated `nas-agent.conf` (the new UserParameter); see docs/hosts/ugreen.md.
- **Ugreen filesystem discovery matched nested `/volumeN` paths.** The data-volume filter
  (`{$FS.NAME.MATCHES}`) was `(^|/)volume[0-9]+$`, which matched any mount **ending** in `/volumeN` -
  so a docker overlay mounted under `/volume2/@docker/.../merged/volume1` showed up as a monitored
  filesystem. Anchored to `^(/rootfs)?/volume[0-9]+$` so only a top-level data volume matches.
- **Long sensor names overflowed their column.** A very long mount path (or any long sensor name) ran
  past its fixed column and overlapped the neighbouring cells - in the host-detail sensor table and in
  the cross-site status/problem/trigger lists (Overview, Errors, the status-chip drill-downs, Paused,
  Triggers). Names now truncate with an ellipsis everywhere; the caret, channel count and paused/hidden
  tags stay put, the sensor's "reason" sub-line still wraps, and the phone layout no longer scrolls
  sideways on a long name. (The monitoring tree and the global search already truncated.)
- **Disk-temperature channels sorted oddly.** The "Disk temperatures" overlay ordered channels by a
  trailing digit (meant for unRAID slots), which split device names like `nvme0` and `nvme1` and
  pushed a newly added disk to the end. Non-unRAID device names now sort naturally/alphabetically
  (`nvme0, nvme1, sda, sdb`); unRAID keeps its parity/data/pool slot order.

## [0.5.0] - 2026-09-22

> **Milestone: the PRTG "Add Sensor" replacement is complete.** This version number was reserved
> for finished auto-discovery (§B): the universal subnet scan, the UniFi controller sweep, the
> controller-enriched scans and the review-and-adopt pipeline shipped across v0.4.56-v0.4.58 and
> are lab-validated on a real multi-site fleet. v0.5.0 caps the arc and adds the lifecycle
> management below - the core now keeps ITSELF current, not just its probes.

**Added:**
- **Core Zabbix minor updates, managed from Settings (§14c extension).** The core's `zabbix-*`
  packages come from the per-major-pinned Zabbix repo and are outside the security-only
  auto-patching, so the core could silently drift behind its own auto-tracking probe fleet.
  Settings → OS updates now shows the core's Zabbix server version, the available candidate and
  the fleet's probe version, and offers a second weekly window (notify-only by default) next to
  the reboot mask: in it, a new host timer applies pending same-major `zabbix-*` updates locally
  and restarts `zabbix-server` - a seconds-long blip the proxies buffer through. Major upgrades
  are never automated. Existing cores enable the new reporter + watcher by re-running
  `deploy/core/setup-core-patching.sh` (one command, documented in deploy/README).
- **Core VM clock in Settings: live time + clock-sync monitoring.** The General card now shows
  the core VM's clock as a live ticking field in its timezone, plus whether it is
  NTP-synchronized (Debian's systemd-timesyncd), with a warning pill when it is not - a monitoring box with a drifting clock corrupts every
  timestamp. It sits right under its source of truth: the existing Settings → General → Timezone
  (ARGUS_TZ) now also drives the VM - a host timer applies the IANA name via timedatectl
  (validated against the zoneinfo database) and restarts zabbix-server to pick it up; the
  built-in UTC default is never pushed, so a first-boot timezone is never overridden. The
  "reported Xm ago" stamp moved into the
  card header to make clear one host report feeds every section, and the window hints now say
  "the core VM's local time" instead of the ambiguous "(local)". Ships in the same
  setup-core-patching.sh re-run as the Zabbix-updates reporter.

**Fixed:**
- **The About card's testing-channel notice no longer stacks its words vertically.** The two
  dev-channel sentences ("A newer :testing build..." / "The :testing channel is now at...")
  carried the grid-layout row class, which puts every inline span on its own line - ":testing",
  the build tag and the rest of the sentence each rendered as a separate row.
- **The OS-updates Save buttons no longer wrap unpredictably.** The two window rows sat within
  2px of the settings card's width, so whether Save fit on the line depended on font-rendering
  hairlines - it could sit beside one row and below the other. Shorter mode labels (the hints
  carry the detail, including a new notify-mode line) and tighter field caps give both rows the
  same one-line layout with real slack.

## [0.4.58] - 2026-09-22

**Changed:**
- **Discovery page reworked into a wizard-style flow.** The page itself is now just the scan
  history with two header actions: **+ New scan** opens a wizard whose first step picks the
  discovery source (subnet scan or UniFi controller sweep - future vendor APIs become more
  cards there), and **Discovery settings** holds the saved controllers. Each step carries a
  one-line description of what Argus will do; while no controller is saved, a single tip on the
  page advises configuring them first, so discovery identifies UniFi gear exactly from the start.

**Added (discovery, minor):**
- **Scans can be deleted from the history** (trash icon on the row, or Delete inside the opened
  scan) - for obsolete runs or a scan started with the wrong subnet. Running jobs can be deleted
  too (late results are dropped); ignores recorded only in the deleted scan are forgotten.

**Fixed:**
- **Adopting controller-enriched scan rows no longer fails on a missing MAC.** A scan run across
  a routed segment sees no ARP, so enriched rows carried no MAC and adoption was rejected with
  "MAC is required for this device class" although the controller knew it. The enrichment merge
  (core-side and the r16+ scanner) now backfills the row's MAC from the controller device, and
  the review UI only shows the MAC as auto-filled when the row really has one - otherwise it
  stays an editable required field.
- **The scan history's "new" count now means actually new.** It counted every result never
  adopted or ignored through Argus, so a range full of devices monitored since before discovery
  existed read "7 new" while the review screen showed them all as monitored. The list now runs
  the same live already-monitored check as the review screen and subtracts those rows.
- **A host moved to the core server could not be moved back to a proxy.** Zabbix reports a
  server-monitored host's proxy id as the sentinel `"0"`; the settings dialog carried it into the
  Server -> Proxy flip, where the proxy select displayed the first proxy while the state still
  held `"0"` - saving then failed with a Zabbix "object does not exist" error (the workaround was
  the Zabbix UI). The sentinel is now normalized out of the config read, the flip lands on a real
  proxy, and both write paths reject `"0"` outright.

**Added:**
- **UniFi controller sweep - auto-discovery slice 2 (completes §B).** The Discovery tab gains a
  second candidate source: save a UniFi Network controller once (name + base URL + API key, the
  key encrypted at rest and write-only from the browser) and sweep it - the controller reports its
  adopted devices across all sites with exact model, type, MAC, IP, firmware and site, so the
  class suggestion is deterministic (switch/AP/gateway from the controller's own device type,
  `provision.SuggestUniFiClass`). Sweeps run from the **core server** (in-process `internal/unifi`
  client) or from a **probe** on the controller's network (`argus_unifi_sweep.py` rides the same
  check-in channel as the scanner, advertised as the `sweeps` capability - probe image r15+), and
  share the scan queue, history, retention and ignore carry-over. Devices only - clients stay the
  subnet scan's job. The sweep speaks the same API the UniFi class templates poll (`X-API-KEY`
  against the Network API, `/proxy/network/...` with a bare-path fallback for plain self-hosted
  controllers).
- **Subnet scans are enriched from your saved controllers.** Scan results are cross-checked
  against every saved controller's adopted devices (matched by MAC, or by IP where the scan saw
  none): matching rows gain the controller facts - exact model, real name, firmware, site - the
  certain class suggestion, and the same adopt-time macro injection as a sweep row. A plain scan
  of a range with UniFi gear in it now reviews like a sweep. With probe image **r16** the probe
  queries the controllers ITSELF right after scanning (the scan job carries them), so enrichment
  works even for controllers only that site's network can reach; the core does the same for
  core-run scans and remains the best-effort fallback for older probe images.
- **Controller client-table naming hints.** A scanned host that isn't UniFi gear but is known to
  the controller as a client gets its controller name/hostname as the suggested device name
  (with a "wired client"/"Wi-Fi client" pill in the review) - a hint only, never a class, and
  clients are still never imported by sweeps.
- **Adopting a swept UniFi device fills its macros for you.** For a sweep-adopted device on a
  UniFi class, the four controller macros (`{$UNIFI.URL}`, `{$UNIFI.KEY}`, `{$UNIFI.MAC}`,
  `{$UNIFI.SITE}`) are injected **server-side** from the saved controller and the sweep facts -
  the fields the manual Add-device flow makes you type per device. The API key goes straight from
  the encrypted store onto the host as a secret macro and never travels through the browser; the
  review row shows the fields as auto-filled (only the site stays overridable).

## [0.4.57] - 2026-09-21

**Fixed:**
- **Discovery suggestions, refined by the first probe fleet scan.** UniFi gear on old firmware is
  now recognized by model tokens alone (a US-8 whose only hints were "US-8-60W"/"USW8"); a UniFi OS
  console is spotted by its "UniFi OS" page title; and a live service now outranks the generic
  Linux guess - a Debian box answering real DNS queries suggests DNS server, a Pi serving upsd
  suggests UPS (NUT), while specific identities (Windows, unRAID, Ugreen, UniFi) still win.
  The **MAC address joins the fingerprint**: Ubiquiti's OUI identifies UniFi gear that answers
  nothing but SSH (the USW-Flex/Ultra models ship no SNMP agent) - suggested as UniFi Switch, or
  UniFi Gateway when the device also answers DNS. And the "UniFi OS" page title maps to UniFi
  Gateway (that's what UDM/UCG/UXG serve), while a self-hosted Network Server's "UniFi Network"
  title maps to UniFi OS Console.
  **Hostname hints**: an rDNS/sysName that names the product (adguard, homeassistant, pihole) beats
  the OS identity - a Debian VM called AdGuard is monitored as AdGuard. **Deeper HTTP fingerprint**
  (scanner side, core now / probe from r14): same-host redirects are followed for the real page
  title (AdGuard's / 302s to /login.html, which titles "AdGuard Home" - no more "302 Found"), the
  Location header is recorded (a `/login.html` redirect + live DNS = AdGuard even without the
  title), and the **SSH version banner** is captured - dropbear identifies embedded gear (UniFi
  switches), which no longer gets the Linux (SSH) suggestion its collector couldn't serve. Note:
  MAC/OUI identification needs a host-network probe - a bridged container sees no LAN ARP.
  Suggestions are recomputed from the stored scan facts on every read, so these fixes apply to
  scans you already ran - no re-scan needed. Also: PTR answers that echo the IP no longer seed
  device names like "10", and the adopt-site resets per opened scan instead of carrying over from
  the previous one.
- **Host settings is now a dialog (and part of the URL).** The old inline band under the host row
  could be left open while drilling elsewhere in the tree - back at the root you'd still see one
  host's settings floating in the list. It's now a proper modal: any navigation means closing it
  first (Escape, backdrop, Cancel - or the browser's Back button, since the open dialog lives in
  the URL as `&edit=<host>`, making it reload-safe and deep-linkable like the rest of the app).

---

## [0.4.56] - 2026-09-21

**Added:**
- **Network discovery (auto-provisioning §B, slice 1).** A new admin-only **Discovery** tab: pick a
  probe and a subnet (CIDR, up to a /22), and the scan job rides the probe's existing check-in
  channel - no new ports, the probe stays a pure reporter. The probe's new `argus_netscan.py`
  (stdlib-only, baked into the probe image, no nmap) fingerprints every live address: ICMP, a small
  TCP service-port set, SNMP `sysDescr`/`sysObjectID`/`sysName` (v1/v2c, defaulting to the probe's
  SNMP default), an HTTP(S) banner (status/Server/title), a real DNS query, reverse DNS and the ARP
  cache. The core maps each fingerprint to a **suggested device class** (UniFi models, Windows/Linux
  SNMP, AdGuard Home, Home Assistant, XCP-NG, DNS server, NUT, Linux over SSH, or plain Ping+HTTP)
  and the review table lets you multi-select, rename, change the class, fill class macros per row,
  and **adopt** - each device is created through the same path as Add-device (thresholds, LLD
  auto-fire, proxy SNMP inheritance) and tagged `argus.source=discovered`. **Ignored** devices stay
  ignored across re-scans; IPs Argus already monitors are flagged. Requires a probe image with the
  scanner (`probe/v7.0.30-r13+`; older probes show "needs probe update"). Guide: `docs/discovery.md`.
- **Scan from the core server too.** "Scan from: Core server" runs an equivalent built-in Go
  scanner inside Argus itself (no probe involved) - for no-proxy deployments and anything monitored
  by the core directly. Same fingerprints and review flow; ICMP-only devices are found where the
  container runtime allows unprivileged ping (Docker's default), and MAC addresses are probe-only.
- **The core server now has its own SNMP default** (Probes → **Core SNMP**), completing the
  PRTG-style inheritance story for no-proxy setups: core-monitored SNMP devices can inherit it in
  Add-device and host settings (changes propagate to inheriting hosts, same as a probe default),
  and core-run discovery scans fingerprint with it automatically.
- **Discovery review picks the site, not the scan form.** The site for adopted devices moved to
  where it belongs: a toolbar select at review time (pre-filled from the scanning probe's site)
  with a per-device override in each row's settings band - so one scan can adopt into several sites.
- **Scan history + queueing.** Scans land in a proper **Recent scans** list (kept 30 days) with
  found/new counts - open any scan to review or re-adopt its results, keeping the main Discovery
  page clean. Scans queue per source and run one after another (different sources in parallel), so
  several scans can be fired back to back.
- **Review polish from the first real scan:** a class with unfilled required fields shows an amber
  warning next to the picker (and the row is refused at Add time with the reason); the HTTP/HTTPS
  add-on is offered for every web-capable class with scheme + port fields (pre-filled from the
  scan); the "monitored" pill is green; and the review table's row dividers no longer fade under
  already-monitored rows.
- **Every device class now offers the HTTP/HTTPS add-on** - the API-based and collector classes
  (UniFi family, AdGuard, Home Assistant, DNS server, UPS, XCP-NG) previously hid it, but most of
  those devices expose a web UI worth watching; whether to check it is the admin's call. Applies
  to Add-device and the discovery review alike.
- **Sidebar: Configure now reads Discovery → Probes → Notifications.**
- **An opened scan is its own screen.** Opening a result from Recent scans (or starting a scan)
  replaces the whole Discovery page - no scan form or history alongside the review. The page header
  and the URL reflect it (`?view=discovery&scan=N`, so results are deep-linkable), and "Back to
  scans" or the browser's Back button return to the clean list.

**Changed:**
- **Probe check-in tick: 5 minutes → 1 minute** (probe image), so queued scans are picked up within
  a minute and fleet status is fresher. The check-in stays a single tiny HTTPS POST.

---

## [0.4.55] - 2026-09-19

**Added:**
- **Linux (SSH, agentless) device class.** The no-SNMP, no-agent fallback for a Linux box you can
  only reach over SSH: the proxy (or core) runs the `argus_linux_ssh.py` collector, which opens
  **one** SSH session per poll and reads `/proc`, `df` and `/proc/net/dev` in a single login -
  returning CPU utilization, 1/5/15-minute load, memory, uptime, per-filesystem usage (LLD) and
  per-interface traffic (LLD). The item keys match the native Linux keys, so a host reads exactly
  like an SNMP- or agent-monitored one. **Authentication is your choice per host** (`{$SSH.AUTH}`):
  an SSH **key** (private key mounted on the proxy - recommended) or a **password** (secret macro,
  fed to the collector through the environment, never on the command line). A read-only login is
  enough. Thresholds and the filesystem / interface skip lists are the usual Linux macros. Guide:
  `docs/hosts/linux-ssh.md`. (The proxy image now bakes `openssh-client` + `sshpass`; the core
  installs them via `setup-core.sh`.)

**Changed:**
- **The "Generic Linux (SNMP)" class is now just "Linux (SNMP)"** - so it lines up with the new
  "Linux (SSH, agentless)" class and matches its template name (`Argus Linux by SNMP`). Display only;
  the class id, template and item keys are unchanged.

---

## [0.4.54] - 2026-09-18

**Fixed:**
- **XCP-NG: the Monitored VMs field hides while there is nothing to pick.** With VM monitoring off
  (or nothing discovered yet) host settings no longer shows a raw comma-separated names input; the
  checklist appears once VMs are discovered.

---

## [0.4.53] - 2026-09-18

**Added:**
- **XCP-NG (XAPI) device class.** One Argus device monitors a whole XCP-NG pool: point it at the
  pool master (a slave address is followed automatically) and the `argus_xcpng.py` collector reads
  everything over one XAPI session per poll - pool health (HA, members live, VM counts), and per
  hypervisor: CPU utilization (from the host's RRD feed), memory, uptime, XCP-NG version and
  liveness, with member-down / high-CPU / high-memory / unreachable / bad-credentials alerts.
  **Per-VM monitoring is opt-in** via a VM-monitoring dropdown (`{$XCP.VM.MODE}`): `off` (default),
  `state` (power state per VM with a *not running* warning), or `full` (state + per-VM CPU, memory,
  disk I/O and network I/O). **CPU temperature per hypervisor** works through an optional
  `argus-temp` XAPI plugin dropped on dom0 (same session, no extra port; hosts without it simply
  have no temperature sensor). Guide: `docs/hosts/xcpng.md`.
- **Choice macros render as dropdowns.** A class macro with a fixed value set (like the XCP-NG
  VM-monitoring mode) now shows as a select in the Add-device wizard and host settings, with a
  "default" entry that keeps the template default.
- **XCP-NG: ignore individual VMs.** Host settings lists the discovered VMs as a **Monitored VMs**
  checklist - unticked VMs drop out of the per-VM sensors and the running/defined counts (the
  `{$XCP.VM.IGNORE}` macro under the hood); saving also closes their open "not running" warnings.
  Sensors of a lost VM are hidden from the curated view immediately, disabled in Zabbix, and
  deleted after 7 days; an ignored VM stays listed (struck through) so it can be re-enabled later.

**Changed:**
- **Servers read CPU, temperature, memory.** The Temperature section moved up to sit right after
  CPU on server-shaped hosts (it is the CPU temperature there); network gear and storage boxes
  keep their own orders.
- **An XCP-NG host's Virtual machines section leads with the running/defined count**, and a
  single-member pool hides its meaningless "Pool members 1/1" rows.

---

## [0.4.52] - 2026-09-18

**Added:**
- **Per-host class options in host settings.** A device class that declares per-host macros (like the
  Windows service filter) now shows those fields in the host settings editor, so you can tune the
  monitoring of an existing host without touching Zabbix. Only the class's own macros are edited;
  preset macros and template defaults are left untouched, and a cleared field reverts to the default.
- **Windows service monitoring is now configurable in the UI.** The Windows (SNMP) class exposes
  `{$WIN.SERVICE.MATCHES}` as a field, in both the Add-device wizard and the host settings editor. Set
  it to a regex of service display names (e.g. `DNS Server|Print Spooler|SQL Server .*`) and those
  services appear as sensors with a "Service not running" alert; the discovery re-runs on its next
  cycle. It was already in the template but had no UI knob.

**Changed:**
- **Memory sensors read used, available, total.** The Memory category now lists used first, then
  available, then total, instead of alphabetical order.
- **Disk temperature thresholds split by disk type (HDD vs SSD).** SSDs tolerate more heat than
  spinning disks, so the defaults are now **HDD 40 °C warn / 45 °C high** and **SSD/NVMe 65 °C warn /
  75 °C high**. On the **Ugreen** class the trigger picks the threshold per disk from its SMART type
  (`{#DISKTYPE}`, with `{$DISK.TEMP.WARN:ssd}` / `:nvme` contexts over the HDD default). On **unRAID**
  the array (HDD) uses `{$DISK.TEMP.*}` and the pool/cache (SSD/NVMe) uses `{$POOL.TEMP.*}`, now set to
  those values. All overridable per host.

**Docs:**
- **Per-device monitoring guides.** [`docs/hosts/`](docs/hosts/README.md) now has a guide for every
  device class Argus can add - Generic Linux (SNMP), Windows (SNMP, with the service-monitoring regex),
  unRAID and Ugreen, the UniFi family (Switch/Gateway/AP/OS Console), AdGuard Home and generic DNS,
  Home Assistant, and UPS (NUT direct and via PeaNUT) - each covering what it monitors, credentials,
  device-side setup, and per-host tunables. The unRAID/Ugreen guides moved here out of the long
  deployment README; the main README and `deploy/README.md` point to the folder.

## [0.4.51] - 2026-09-18

**Added:**
- **Direct appliance downloads in the Add-probe wizard.** The VM deploy step now lists the built
  proxy appliances (OVA, qcow2, VHD) as one-click download buttons with their sizes, resolved live
  from the latest `probe-vm` GitHub Release, plus the seed ISO that every appliance needs. No more
  hunting for the files on GitHub.
- **Clearer, structured deploy instructions for every method.** The VM, unRAID, `docker run` and
  `docker compose` steps now share a consistent layout: a bold titled action, numbered steps and a
  short note. The VM step spells out that you pick one appliance for your hypervisor and that the
  seed ISO is required for zero-touch provisioning and static IP; the container steps explain
  self-enrollment and the single-use token.

**Changed:**
- **Wider Add-probe wizard.** The dialog widened (up to 800px) so the generated commands and the
  unRAID template read without horizontal scrolling.

**Fixed:**
- **Docs-only pushes no longer rebuild the image.** A push that touches only Markdown or the LICENSE
  now skips the container build, so a documentation edit stops producing a phantom `:testing` update
  pill. Tag builds and manual runs always build.

## [0.4.50] - 2026-09-17

**Added:**
- **Ugreen NAS (Zabbix agent) device class.** Ugreen's UGOS exposes no SNMP, so this is the catalog's
  first **agent-based** class: a Zabbix agent 2 container runs on the NAS and the site proxy polls it
  passively on `:10050`. CPU utilization, memory (computed from **MemAvailable**, so page cache counts
  as free), uptime, per-volume disk usage and per-NIC traffic reuse the native agent item keys, so
  they curate exactly like the SNMP classes; a small mounted config adds CPU package temperature and
  per-disk SMART temperature. Disk temps are read with `smartctl -n standby`, so a spun-down drive is
  **never woken** - it reports a fixed **20 °C parked sentinel** (like the unRAID class), and an NVMe
  (which never parks) is always read. **Add device → Ugreen (Zabbix agent)** shows the exact container
  command to run.
- **Add-device setup steps.** A device class can carry prerequisite setup instructions (title, steps,
  a copyable command), shown right in the Add-device dialog - the Ugreen class uses it to hand you the
  agent-container command with the device name filled in.
- **"New version available - Reload" prompt.** An open tab now notices when the core has been updated
  underneath it (the running build id changed) and offers a one-click reload, instead of silently
  running the old page bundle until a manual refresh.

**Fixed:**
- **Probes: an up-to-date probe is no longer offered a downgrade.** A probe running a just-published
  revision while the core's registry "latest" cache still held the previous one was flagged "update
  available → «older»". A probe at or ahead of the newest published revision now reads as current;
  only a genuinely older probe is flagged for update.

## [0.4.49] - 2026-09-15

**Added:**
- **unRAID CPU temperature.** A new optional `cputemp` NET-SNMP extend
  ([`docs/hosts/unraid-cpu-temp.sh`](docs/hosts/unraid-cpu-temp.sh)) reports the CPU package temperature from
  lm-sensors (Intel package / AMD Tdie/Tctl, hottest core as fallback), shown as a standalone
  Temperature sensor with *running hot* / *overheating* alerts (`{$CPU.TEMP.WARN}` 80 °C,
  `{$CPU.TEMP.HIGH}` 90 °C). Needs the Dynamix System Temperature plugin.
- **Docs: monitoring unRAID temperatures.** `deploy/README.md` now documents the plugin +
  extend-script setup for both disk temperatures and CPU temperature, linked from the main README.

## [0.4.48] - 2026-09-15

**Added:**
- **Parked unRAID drives read 20 °C.** A spun-down array/pool drive now reports a fixed 20 °C
  standby sentinel instead of dropping off the chart, so a parked disk shows a distinct low flat
  line (and any heat warning clears) - matching the SNMP plugin's own `disktemp` extend. Gated on
  the drive's standby flag, so the USB flash and always-on SSD/NVMe caches never get it. _(Re-copy
  `docs/hosts/unraid-pool-temps.sh` to the unRAID host to pick this up.)_

**Fixed:**
- **Hidden chart channels stay hidden.** Channels you hide in a multi-channel sensor's legend no
  longer reappear on the 60 s auto-refresh or when you switch the time range.
- **Two near-identical yellow lines** in a 6-channel group (e.g. an unRAID array with two cache
  drives): the sixth series is now a distinct violet instead of a second yellow-green.

**Changed:**
- **Unraid templates recommend an IP** for the probe core host (same DNS-flood rationale as
  v0.4.47), on both the core and manual-proxy Community Applications templates.

## [0.4.47] - 2026-09-15

**Added:**
- **Central probe re-point.** The probe core host (**Settings → Probe enrollment** /
  `ARGUS_PROBE_CORE_HOST`) is now handed out on every probe check-in, not just baked in at
  enrollment. Change it once and the whole fleet converges: each probe applies the new address at
  its next restart - no re-enrollment. An explicit `ZBX_SERVER_HOST` on a probe still pins that one
  probe and wins over the central value. Fail-safe: an unreachable core or an older probe keeps the
  last-known host, so a bad value can't strand the fleet. _(The probe side ships in argus-probe
  `probe/v7.0.30-r9`.)_

**Changed:**
- **Recommend an IP for the probe core host.** The Zabbix proxy re-resolves this address on every
  data send, so an FQDN there generates heavy DNS load; the setting hint and deploy docs now advise
  an IP. Combined with the central re-point above, switching the fleet from a name to an IP is a
  one-field change plus a restart.

## [0.4.46] - 2026-09-15

**Argus is now free software under the GNU AGPL-3.0.**

**Added:**
- **Drill into channel groups** - a multi-channel sensor's name (ICMP, DNS activity, a disk, a
  NIC) is now a drill link like single sensors: click it to focus just that group, chart open,
  with its own breadcrumb and shareable URL. Deep links to a member channel now open the whole
  group's chart too, instead of nothing.

**Changed:**
- **Relicensed to AGPL-3.0** (was MIT). All three repos (core, probe, updater) are now under the
  GNU Affero General Public License v3.0. Source files carry SPDX headers, and - as the AGPL's §13
  network clause expects - the app's About page and the first-boot setup pages link to the source.

**Fixed:**
- **Phantom "update available" for updater sidecars.** A sidecar tracking `:latest` reports a
  development build whose version base equals the newest release; the Probes page compared the whole
  string and flagged it "outdated → x.y.z" permanently - an update that clicking could never clear.
  It now compares the version base, so a sidecar at or past the newest release reads "up to date"
  (matching a digest-based check).

---

## [0.4.45] - 2026-09-15

**AdGuard's daily numbers are now exact - true midnight-to-midnight days, one source of truth.**

**Added:**
- **AdGuard counts real days now** - the queries/blocked sensors read **"today so far"** from
  AdGuard's own per-day statistics, reconstructed into **true local calendar days**: AdGuard
  buckets its stats at UTC midnights (a known AdGuard Home limitation its own dashboard shares),
  so Argus splits the counter's growth at *your* midnight instead - at 00:15 the row shows the
  real count since 00:00, and yesterday's bar freezes at its genuine total. (Replaces the
  rolling-window totals, whose deltas could read "0 blocked" all day; those items are removed on
  upgrade, so the daily bars restart from the day you update.)
- **Block rate, per day** - the Block rate row charts as daily bars too (7d·1M·3M·6M·1Y, mini
  bars in the row), each day's rate derived from that day's blocked ÷ total - the same days,
  the same math as the activity chart above it. Its reading is today's local-day rate.
- **One source of truth** - the row headline, the mini bars, and both big bar charts all render
  the same server-computed buckets (`/api/daily`), so they can never disagree with each other.

**Fixed:**
- **Daily bar chart on a fresh device** - a just-added AdGuard shows today's bar immediately
  (before, the chart stayed empty until two full days of history existed).
- **Stale frontend after a self-update** - the app now serves its page shell with an explicit
  `no-cache` policy (and the hashed asset bundles as immutable), so a browser tab can no longer
  keep running the previous build's frontend after the core updates underneath it. Charts and
  panels always match the running backend after a plain reload.
- **"What's new" on the testing channel** - when the newer `:testing` build is itself a release
  (right after a cut), the About panel now shows that release's changelog instead of the generic
  "unreleased changes" line.

---

## [0.4.44] - 2026-09-14

**AdGuard's DNS activity now charts as daily stacked bars.**

**Added:**
- **Daily activity bar chart for AdGuard** - the queries/blocked counters now collapse into one
  "DNS activity" sensor whose chart draws a **stacked bar per calendar day** (full bar = total
  queries, red share = blocked; midnight-to-midnight in the viewer's timezone) instead of the old
  slowly-drifting lines. The day's growth is computed from AdGuard's rolling totals, so a
  window-slide dip or a stats reset reads as a low/zero bar, never a negative one. Built as a
  general counter→bars chart mode future counter metrics can reuse.
- **Dedicated 7d timeframe** - bar-mode charts get their own day-scale tabs: **7d (default) ·
  1M · 3M · 6M · 1Y** (no 2h/2d, which would hold at most two bars). Line charts keep the usual
  set. The AdGuard sensor row headline now reads "N queries · M blocked".

---

## [0.4.43] - 2026-09-14

**Five new self-hosted-service device classes - AdGuard Home, Home Assistant, DNS servers, and UPSes
over NUT - plus the agentless collectors that make them work.** These extend the device catalogue
beyond SNMP/UniFi: a DNS server gets a genuine resolve check, a UPS is monitored through Network UPS
Tools, and AdGuard / Home Assistant are polled over their own APIs. Two new sensor sections -
**Battery** and **DNS** - organize them. Proxy-monitored NUT and DNS need the proxy updated to
`argus-probe` `probe/v7.0.30-r7` (`:latest`), which bakes the collectors in; core-monitored devices
work out of the box.

**Added:**
- **AdGuard Home** - DNS filtering stats (queries, blocked, block rate, average processing time),
  whether protection is on, the version, and a real per-name resolve check. Admin login optional.
- **Home Assistant** - API up/down plus the Core, Supervisor, and Operating-system versions (read
  from HA's own update entities; Supervisor/OS appear on HA OS / Supervised installs).
- **DNS server** - a genuine per-name resolution check against any resolver (Pi-hole, Microsoft DNS,
  Ubiquiti, Sophos, AdGuard, …): resolves yes/no, response time, and the resolved IP, each name its
  own collapsible row (the pass/fail shows as a downtime band). Backed by a dependency-free
  `dns-resolver.py` external check.
- **UPS (NUT)** - two ways to monitor a UPS through Network UPS Tools: a **direct collector**
  (`argus_nut.py`, which speaks the NUT protocol to `upsd`) and **via PeaNUT** (polls PeaNUT's HTTP
  API). Battery charge / runtime / status, load, input + output voltage and power draw, with
  on-battery and low-battery alerts.
- Agentless external-check collectors are baked into **both** the probe image and the core install
  (`setup-core.sh`), so a device Monitored-by the Core server works with no external proxy.

**Changed:**
- Add-device auto-fills a host-addressed URL from the IP/DNS you enter (AdGuard, Home Assistant,
  PeaNUT), overridable per host.
- Creating a group while drilled into one now nests it there by default, matching the per-node "add
  subgroup" and add-device-into-current-site behaviour.

**Fixed:**
- Adding a device no longer snaps the tree to a different group - the deep-link that placed you there
  is applied once, so a host-list reload (Add device, the background poll) can't re-apply a stale
  target over the group you've drilled into.
- The AdGuard admin-URL field no longer clips its example text.

## [core-vm/v0.1.0] - 2026-09-11

**The whole monitoring core as one self-installing VM.** A new golden-image appliance
(`deploy/core-vm/`) bakes the entire core - Zabbix 7.0 (server + frontend + agent2), PostgreSQL +
TimescaleDB, and the Argus + updater containers - and configures all of it on **first boot from a
single web form** (hostname, keyboard, timezone, admin email + password). No Zabbix setup wizard, no
API-token copy-pasting, no SQL: first boot creates the local admin user, initializes the database and
schema, generates the probe-enrollment PKI (so enrollment works out of the box), writes the Zabbix
server + frontend config, rotates the stock `Admin` password and mints a dedicated `argus-svc` API
token (machine-managed, never shown), sets housekeeping retention, and starts Argus - then hands you a
sign-in link and parks a `:80 → :8081` redirect. One administrator password fans out to the Debian,
Zabbix, and Argus accounts (each overridable under Advanced); the database password, API token, and
encryption key are generated and never displayed. Ships as **OVA / qcow2 / VHD** (a separate
`core-vm/vX.Y.Z` release). The **manual install path is unchanged and stays first-class** - any distro,
an existing Zabbix, or Argus split from the Zabbix core (README **Option A** appliance / **Option B**
manual; `setup-core.sh SETUP_MODE=image` shares one installer with the image build). See DESIGN **§14d**.

## [0.4.42] - 2026-09-10

**Parked disks keep their line.** A spun-down unRAID array or pool disk now stays on the temperature
chart, drawn as a flat line at its last reading, instead of dropping off the graph entirely.

**Fixed:**
- **A parked drive no longer vanishes from the temperature chart.** When an array or pool disk spins
  down it drops out of the emhttp temperature extend, so Zabbix flags that drive's item "not
  supported" while keeping its last reading - but the group chart only plotted currently-supported
  channels, so the drive (a spun-down parity disk, say) fell off the graph even though its reading
  was intact and it was still counted among the group's channels. Parked temperature channels now
  stay on the chart and hold their last reading as a flat line across the idle stretch, seeded from
  the last value when the drive parked before the visible window. Actively-reporting drives are
  unchanged - the hold only fills the gap a parked drive leaves.

## [0.4.41] - 2026-09-10

**Windows monitoring, the UniFi family finished, and a smoother Add-device.** Two more device classes
(Windows over SNMP, the UniFi OS Console), the Add-device flow reworked into a searchable modal, and
a batch of lab-driven fixes from putting a real Windows host and slow-filling disks under the lens.

**Added:**
- **Windows (SNMP) device class** - any edition, client or server. HOST-RESOURCES for CPU
  (overall + per-core), physical memory, and fixed-disk volumes; IF-MIB for network; uptime; and a
  new **Services** category that watches selected Windows services from the LAN Manager service
  table (opt-in by a name regex, each with a not-running alert). Reuses the Generic Linux item keys,
  so the curated view labels and groups everything identically.
- **UniFi OS Console device class** - the console host itself (Cloud Key, UNVR, or a
  console-capable gateway): state, CPU/memory, uptime, firmware, temperature, and internal storage
  usage per volume. Completes the UniFi family (Switch, Gateway, AP, Console).
- **Searchable device-class picker** - the Add-device class list is now a type-to-filter combobox,
  sorted alphabetically (with "Ping only" pinned first), for a catalog that keeps growing.

**Changed:**
- **Add device is a modal** now, matching the Add-probe dialog, instead of an inline band that
  pushed the host tree down - wider, with each field row on one line.

**Fixed:**
- **Windows network now collects** and is readable: Windows doesn't serve the 64-bit interface
  counters, so traffic is read from the 32-bit counters; discovery keeps only physical ethernet
  ports (dropping the swarm of virtual/tunnel/vSwitch adapters); and each NIC is labelled by its
  Windows **connection name** (ifAlias - "Ethernet 2") rather than the raw internal name. A NIC that
  briefly drops offline during discovery keeps its history (7-day grace before a truly removed one
  is dropped).
- **Near-constant values read flat**, in both the charts and the mini graphs: a disk sitting at a
  steady 57 % (drifting a hundredth of a percent) no longer stretches that sliver across the full
  height as a dramatic ramp. Percentage axes hold a minimum span; sparklines hold one relative to
  the series' own magnitude. Genuinely varying series are unchanged.

## [0.4.40] - 2026-09-09

**C2 breadth - the UniFi family and unRAID join the catalog.** Three new device classes ride the
patterns C1 proved: two more UniFi types off the same controller API, and unRAID as the first
multi-template class (the Generic Linux SNMP template plus a storage add-on). Along the way, a
per-core CPU breakdown for every SNMP host, a corrected memory reading, and a batch of
lab-driven monitoring-view refinements.

**Added:**
- **UniFi Gateway device class.** One controller call per poll (by MAC), like the switch: state,
  CPU/memory, uptime, firmware, temperature, LAN port discovery (WAN ports excluded), total PoE
  draw on models that power devices, the last speedtest result, and **per-WAN discovery** - traffic
  in/out plus the gateway's own uplink-monitor latency and availability (shown as a "WAN quality"
  group beside ICMP), with a WAN-degraded alert.
- **UniFi Access Point device class.** Adds a **Wireless** sensor category: connected clients, the
  controller's experience score, and per-radio discovery (clients and channel utilization per band,
  2.4/5/6 GHz) - plus the shared state/CPU/memory/uptime/uplink/firmware sensors.
- **unRAID device class (SNMP).** The first class to stack templates: Generic Linux SNMP (CPU,
  memory, array/pool/`docker.img` filesystems, interfaces, uptime) plus an unRAID add-on for
  per-disk temperatures and per-share free space, read from the Community Applications "SNMP"
  plugin. Utility mount roots are filtered out by a class-preset skip list. Ships an optional
  companion script (`docs/hosts/unraid-pool-temps.sh`) that adds cache/pool drives (incl. NVMe, which the
  plugin omits) and reads temperatures from the emhttp state - atomic, dash-free, never waking a
  disk - with drives named by slot ("Parity", "Disk 1", "Cache 2") and ordered like the Main tab.
- **Per-core CPU utilization** on the Generic Linux SNMP class: `hrProcessorLoad` walked in one
  request, discovered into a "Cores" group whose headline and chart follow the busiest core.

**Changed:**
- **Storage-box category order:** a host with drive-temperature sensors (unRAID now, QNAP/Ugreen
  later) reads Temperature right before Disk. Third built-in shape profile beside compute-first
  (servers) and network-first (switches/APs).
- **HTTP/HTTPS collapses into one group** like ICMP: response time is the primary channel, the
  reachability check becomes the inverted Downtime band.
- **Available memory now reflects what the kernel can actually reclaim** - free plus buffers and
  page cache, minus the tmpfs/shared pages that live in cache but can't be evicted. Matches what
  `htop` and the unRAID dashboard report; memory utilization inherits the correction across every
  SNMP host.
- **The UniFi WAN-degraded alert arms itself:** it only fires for a WAN that was healthy in the
  last few hours, so a configured-but-unplugged failover never alerts while a WAN that worked and
  then died still does - and stays open through the whole outage. A per-WAN threshold override
  remains for deliberate decommissions.
- A **one-member group keeps its group name** (a WAN with only availability data reads "WAN 2
  quality", not the raw item label) and folds back into a full group when more channels report.

**Fixed:**
- **Memory utilization read far too high** on any box with a warm cache - the previous "available
  memory" counted only truly-free RAM. (See above.)
- **Percentage charts no longer amplify a flat line:** a disk sitting at a steady 57 % auto-ranged
  to a sliver of a percent and drew as a full-height ramp with every gridline rounding to the same
  label. Percentage axes now keep a minimum visible span, so a steady value reads flat while a
  genuinely varying one keeps its detail.
- **unRAID disk temperatures no longer flicker in and out.** The plugin's temperature script serves
  a partial file while rebuilding its cache, and Zabbix was disabling the momentarily-missing disks;
  discovered drives are now kept through partial reads. Spun-down disks keep their last real reading
  instead of a placeholder.
- **Right-axis labels stopped clipping for good:** the auto-size pass now re-measures as the axis
  settles, so a late switch to finer tick labels (`32.5 °C`) can't overflow the gutter.
- **Warning-severity problems read as warnings, not errors:** the host banner shows "Active
  warnings" in amber (red "Active problems" only when something is at error level), and the tree
  problem count matches.

## [0.4.39] - 2026-09-09

**The UniFi Switch class - and a discovery trigger.** §C phase C1 is complete: switches join the
device catalog, polled from their UniFi controller with an API key - ports, PoE, traffic and all -
and a freshly added host gets its discovered per-instance sensors in seconds instead of an hour.

**Added:**
- **UniFi Switch device class.** One controller call per poll, addressed by the switch's MAC -
  works against a UniFi OS console (cloud gateway on :443) or a self-hosted console on a custom
  port via a per-host `{$UNIFI.URL}`. Sensors: controller state (offline trigger), CPU and memory
  utilization (threshold triggers), uptime, firmware, temperature (models that report one), uplink
  traffic, total **PoE power draw**, and port discovery - per-port link, negotiated speed, traffic
  in/out (controller-computed rates) and **PoE watts** on PoE-capable ports only. Ports carry the
  controller's names ("Port 1 · Office-AP") and sort naturally (Port 2 before Port 10).
- **Class-declared per-host inputs.** A device class can declare the macros it needs (controller
  URL, API key, MAC, site) and the Add-device form renders them generically - secrets are masked in
  the form and stored as Zabbix **secret macros**, write-only after creation. The seam every future
  API-source class reuses.
- **Discovery trigger.** After Add-device creates a host, its LLD rules fire automatically
  ("execute now", delayed until the proxy has synced the new config, with a retry) - and a
  **Discover now** action in the host menu runs the same trigger on demand for any host.

**Changed:**
- **Sensor categories order by host shape:** network gear (anything with switch ports) reads
  network-first, servers keep the classic compute-first order, and Power sorts before CPU in both.
  A GUI-editable order is on the roadmap.
- **Port rows read like network interfaces:** live ↓/↑ traffic in the value column (a down port
  reads "down"), a traffic sparkline - and every traffic-style group's sparkline (NICs, uplinks,
  ports) now shows the **sum of in + out**: total throughput at a glance.
- **Port charts start with the constant Speed/Link lines hidden** (their live values stay in the
  legend; one click reveals the line) - so the traffic axis ranges to the actual in/out rates
  instead of being pinned at the negotiated gigabits.

**Fixed:**
- Capability placeholders no longer occupy sensor rows: a switch without a temperature probe or
  without PoE reports a constant 0 for those - the template stops collecting them and the curated
  view hides the stale rows. The controller-state item feeds its offline trigger without taking up
  a row of its own.

## [0.4.38] - 2026-09-08

**Device classes - Argus provisions hosts now - plus a PRTG-grade monitoring view.** The first slice
of the device-class catalog: hand-authored Zabbix templates ship inside Argus and reconcile at
startup, **"+ Add device"** creates and binds a host end-to-end, and a **Generic Linux SNMP** class
arrives with full discovery. On the viewing side: channel groups with multi-series charts, a tree
that separates groups from hosts at a glance, and a deep chart/sparkline rework.

**Added:**
- **Device-class framework (C0).** Class templates live inside the binary and are versioned and
  re-imported at startup via `configuration.import` (needs a super-admin API token; soft-skips
  without one). A class registry plus a per-host `device_class` overlay; **"+ Add device"** (admin)
  in Sites & hosts creates the host in Zabbix, binds it to the site's proxy and attaches the class's
  templates, macros and interface. First classes: **Ping only** with an optional **HTTP/HTTPS
  endpoint** add-on (custom port).
- **Generic Linux SNMP class (C1).** Core metrics (CPU, memory, uptime) plus discovery: filesystem
  LLD → per-mount Disk total / used / used % (tmpfs filtered; OIDs matched in numeric *and*
  MIB-symbolic form), interface LLD → per-NIC traffic in/out. Thresholds ride as user macros.
  Add-device **inherits the proxy's SNMP defaults** - credentials are asked for only to override.
- **PRTG-style channel groups.** Per-instance sensors (a disk mount, a NIC, the ICMP trio) collapse
  into one group row; expanding it opens a single chart overlaying every channel - mixed units on two
  y-axes, click-to-toggle legend - and pause/hide/priority act on the whole instance. ICMP reads
  response time as the main field, loss beside it, and reachability inverted into a red **downtime
  band**.
- **Tree readability.** Compact accent-tinted group bands vs. taller host rows with device-type
  icons, indent guides, and a per-host ICMP latency + sparkline; sparkline, latency and menu align in
  shared columns at any nesting depth. Groups and hosts start collapsed and remember their open state
  for the login session.
- **Charts.** Min/max envelope band on trend ranges (1M+); the ping chart's % scale is pinned 0-100
  and owns a fixed 0/20/…/100 grid, so "100 % loss" and "down" peak at the same height; the second
  axis labels ride the shared gridlines; axis gutters size themselves to their longest label so
  nothing clips; ticks are unit-aware for every unit; the primary channel is shaded like the
  single-metric charts; drag-zoom pauses that chart's auto-refresh.

**Changed:**
- The host sensor table and the overview/status lists now share the tree's column order (… Trend,
  Priority, Last check) and stable proportions, every sparkline in the app is one size, single-scale
  charts label their axis on the right, and the Loss channel is amber-gold - clearly apart from the
  red downtime.

**Fixed:**
- **The stray blue dot** in every chart's top-left corner: uPlot parks its drag-zoom selection box
  collapsed at 0×0 there, and the dark theme's 1px accent border on it painted a 2-px speck. The
  selection edge is now an inset shadow - identical while dragging, invisible when idle.
- The 30-second refresh could steal the open chart, snapping the view back to the sensor originally
  drilled into from the Overview.
- Flat sparklines rendered dim and blurry (a stroke centered on a pixel boundary splits across two
  rows - half-pixel centers now); axis labels could clip (`7d 4h 46m`, `953.67 MB`); a plain % axis
  lost its unit and followed the browser locale; the overview list's column widths never applied
  (Chrome ignores `calc(% - px)` on fixed-layout table columns).
- Zabbix 7.0 rejects non-v4 UUIDs in template imports; proxy/load-balancer names no longer
  icon-guess as a NAS.

## [0.4.37] - 2026-09-06

**Human-readable time units.** Second-based readings now auto-scale to a sensible magnitude.

**Fixed:**
- A seconds metric like ICMP response time showed as `0.0138 s`, and its chart's y-axis labels all
  collapsed to `0.014`/`0.015`. Seconds now scale to `ms` / `µs` / `ns` (staying `s` at ≥ 1 s) in the
  sensor value, the chart axis, and the chart legend, with the duplicate `(s)` legend suffix dropped.
- Alert messages scale to match: the "Value:" line reads e.g. `13.8 ms` instead of `0.0138 s` (bytes,
  bits and uptime format the same way the UI does).

## [0.4.36] - 2026-09-06

**Monitoring tree** fixes for hosts that belong directly to a parent group (alongside its subgroups).

**Fixed:**
- A host that belongs directly to a group (e.g. a host in `site2` while `site2` also has subgroups) was
  drawn indented under - and after - its sibling subgroups, so it looked like a member of one of them.
  It now sits at the group's own level, above the subgroups.
- Row dividers were inconsistent once hosts and subgroups interleaved: some boundaries drew a double
  line, others none. Every tree row now draws exactly one divider, in any order.

**Added:**
- **Reorder hosts and subgroups together.** In the tree's Reorder mode a group's direct hosts and its
  subgroups are now one list, so a host can be moved above or below the subgroups (saved per parent).

## [0.4.35] - 2026-09-06

**Per-user notifications.** Everyone can now get alerts on their *own* Telegram or Discord, and email
can fan out to every registered user - additively, without changing the existing shared channels.

**Added:**
- **Personal notifications (Account tab).** Any signed-in user (any role) can register their own
  Telegram (their own @BotFather bot token + chat ID) or Discord (webhook URL) and receive alerts there,
  scoped by site and severity like a global channel - with the same per-channel delivery-health line and
  a **Send test** button. Self-service and private: everything is under `/api/me/notify/*`, a user only
  ever sees their own channels (`user_notify_channels`, config encrypted at rest). Groundwork for the
  future mobile apps, where a phone is just another personal channel.
- **Email → registered users.** An email channel can now deliver to **each active user's registered
  email** instead of a fixed address - a "Send to" choice on the channel (admin-controlled), alongside
  the existing fixed-address mode. Each recipient gets a private, individual message.
- **Multi-site channels.** Any channel - global or personal - can target **several sites at once**
  (a multi-select of host-groups) rather than only one site or all. The picker is hierarchical:
  selecting a probe's root group (e.g. `site1`) covers all its subgroups (`site1/Network`, …), and a
  channel fires when any of its sites matches - or is an ancestor of - one of the host's groups.

**Changed:**
- The notifier fires a problem as soon as a **global or personal** channel matches its site + severity
  (previously a problem stayed pending until a *global* channel existed), so personal-only setups alert.

## [0.4.34] - 2026-09-05

A whole-app **UI and notifications polish pass**, driven by a review of every screen at desktop and
phone widths: a handful of real defects, a denser and more legible phone layout, proper feedback
primitives (toasts, skeletons, empty states), a reworked Notifications page with per-channel delivery
health, one consistent set of controls, and clearer alert messages on every channel. Companion:
`argus-probe` **probe-vm/v0.3.2** restyles the VM's first-boot setup page to match.

**Fixed:**
- **Overview / status lists:** the per-row ⋯ button spilled outside the card once the panel was narrower
  than ~1100px (the fixed-layout table gave the actions column a 4% share). It is now a fixed 48px
  column; narrow desktops also trim cell padding and show the compact `★4` priority.
- **Phone drill-down:** the expanded host's sensor table overflowed its card (Last check and the ⋯
  clipped, values wrapping mid-number). Compact priority, nowrap values, tighter cells, and the tree
  indent no longer eats the card width.
- **Future times read "in 22h"** (pending-enrollment expiries, the wizard's token expiry) instead of the
  misleading "0s ago".
- **Sensor chart colours** come from the theme tokens (the hard-coded white-alpha grid was invisible on
  the light theme) and the series takes the sensor's state colour, matching its sparkline; the chart
  repaints when the theme flips.
- **Phone touch targets:** ⋯ buttons 36px, buttons/chips/segments/nav rows enlarged, 16px inputs so
  iOS stops zooming the page on focus. Probe cards align every value - text, ✓ states, pill boxes and
  the ⋯ - on one right edge.

**Changed:**
- **Phone header** drops the subtitle (it repeated the panel header underneath) and keeps the tools
  beside the panel title; **Overview cards** are a compact 4-row grid - host + ⋯, sensor + reason,
  sparkline beside the value, "last check 1m ago" beside the priority - about 30% shorter. The smallest
  phone text moved up to 11px labels / 12.5px sub-lines.
- **Notifications page:** each channel card has an enable switch, a single **Send test** button,
  Edit/Delete in a ⋯, and a **delivery-health line** - "Last sent 2h ago · 42 delivered", "Last delivery
  failed 5m ago · HTTP 400 …", or "Nothing sent yet". Every send attempt (alert, recovery, test) is now
  recorded on the channel, so a broken webhook or SMTP password is visible in the UI, not only in the
  core log. (New `notify_channels` columns, added by the idempotent migration.)
- **Alert messages carry the Zabbix severity** - subjects/titles read `[HIGH]`, `[DISASTER]`,
  `[WARNING]` (what the UI shows) instead of the coarse `[ERROR]`/`[WARNING]` state; recoveries stay
  `[RESOLVED]`. Discord gains a Severity field; the plain-text and email bodies name it too.
- **Email** is a single HTML card with a plain-text alternative: severity-coloured header, no duplicated
  trigger name, buttons that wrap on narrow screens, a footer linking back to the Notifications page,
  and a dark-mode override for clients that honour `prefers-color-scheme`.
- **Telegram** is a compact card (title, site · host, reading, since) with **Open in Argus** and
  **Acknowledge** as inline-keyboard buttons; non-http links fall back to inline anchors.
- **One set of controls:** the channel editor, user roles and the Add-probe wizard use the shared
  Select / Switch / input skin (three select styles, bare checkboxes and an inline style object are
  gone); native checkboxes take the accent colour. The Monitoring toolbar on phones folds Reorder / Show
  hidden / Key-All sensors into a ⋯. Read-only priority stars are slightly muted.

**Added:**
- **Toasts** for action outcomes (Saved, Test sent, Could not delete …) that auto-dismiss, replacing
  persistent inline text lines; contextual form errors stay inline.
- **Loading skeletons** on every list while it fetches, and the Overview no longer flashes "All clear"
  before the first data arrives.
- **Designed empty states** - a green all-clear on the Overview and Triggers, muted "nothing here"
  blocks with an action where one makes sense (Probes, Notifications).
- **Sidebar version tag** (amber "update available" when Settings has an update) and tooltips on the
  collapsed rail.

## [0.4.33] - 2026-09-05

A readability pass over the **Probes** and **Users** tables (both the same labelled-card pattern -
a data table on desktop, stacked cards on mobile). No functional changes; UI only.

**Changed:**
- **Probes table restructured for scannability.** Dropped the `Argus-` prefix from the version headers
  (`Proxy Version` / `Updater Version` / `VM OS Version`). The all-good states - `up to date` and
  `patched` - are now a quiet green ✓ + muted text instead of a full pill, so colored pills are reserved
  for things that need attention (drift → `Update`, `reboot`, `offline`) and the eye lands on the probes
  that need action.
- **Grid dividers** on both tables: faint vertical rules between columns and stronger horizontal rules
  between rows on desktop; on mobile, faint dividers between each field and a stronger boundary between
  cards, so rows/cards read apart from the fainter within-row/field dividers.
- **Mobile cards tidied:** values stack (value over status pill), every value lines up down the right
  edge (bare text inset to match pill text), labels vertically centered, roomier/centered card padding,
  and the kebab tucked under the last field.
- The per-row **⋯ menu now flips upward** when there isn't room below the button, so the last row's menu
  stays on-screen (both the Probes and Users menus).
- Applied the **same treatment to the Users table** for consistency.

## [0.4.32] - 2026-09-05

**OS patching & lifecycle** (roadmap §A, DESIGN §14c): keep the Debian OS under the core and probe VMs
patched without accumulating CVEs, while leaving the reboot policy appropriate to each role. The OS
patches itself locally - Argus reports status and schedules the core's reboot, but never runs `apt`
remotely (there's no clean rollback; hypervisor snapshots are the safety net).

**Added:**
- **Automatic security patching on both roles.** The probe golden image (`argus-probe`
  `probe-vm/v0.3.1`) and the core (`deploy/core/setup-core.sh`) install `unattended-upgrades`
  (**security suite only**, so the core's TimescaleDB 2.28 hold is safe) + `needrestart` (auto-restart
  services after a libc/openssl bump, so most updates need no reboot).
- **Role-appropriate reboots.** **Probe VMs** (cattle) auto-reboot in a weekly ~03:00 window - they
  buffer 7 days offline, so a ~60 s reboot is invisible. The **core** (a pet hosting the DB + Zabbix)
  never reboots unattended by default: a new **Settings → OS updates** mask picks a day + time, or
  "notify only" (the default). A host-side watcher honours the window locally.
- **Fleet patch visibility.** Probe VMs report their pending **security-update count** + **reboot-
  required** flag hourly (`POST /api/probes/os-status`, probe-token auth); the core reports its own via
  a host timer into the shared self-update dir. The **Probes** page gains an **OS** column and a
  "N need a reboot" header rollup; **Settings → OS updates** shows the core's status.
- Endpoints: `GET /api/os/status` (core status + reboot window), `PUT /api/os/reboot-window` (admin),
  `POST /api/probes/os-status` (probe report). New `probe_agents` columns `sec_updates` /
  `reboot_required` / `os_reported_at` (additive migration).

**Changed:**
- **Probes table reworked.** Adding the OS column pushed it to 11 columns; regrouped into a clearer
  layout - **Probe** (name + `mode · enrolled` subline), **Health** (online/offline + last check-in),
  **Argus-Proxy Version** (Zabbix proxy version + up-to-date/update), **Argus-Updater Version** (the
  argus-updater sidecar's version + drift + an Update button when behind), **Argus-VM OS Version** (the
  OS name + patch chip), and a per-row **⋯ menu** (SNMP defaults, Console, Delete). The menu is portaled
  to `<body>` so the table's scroll container can't clip it.
- **Updater drift.** Argus now resolves the newest `argus-updater` version from GHCR (like it does for
  the probe image) so each probe's sidecar shows up-to-date / behind, not just its version.
- **Probes report their OS name** (`os` in `POST /api/probes/os-status`, new `os_version` column) so the
  VM's Debian version shows alongside the patch status.
- The proxy SNMP-default band is a centered card with balanced fields; fixed its prose spilling past the
  card - the probes table forces `white-space: nowrap` on cells and the inline panel inherited it, so its
  note couldn't wrap (reset to `normal`; also guarded the field grid against `min-width: auto` overflow).

**Deploy:**
- `setup-core.sh` gains an OS-patching step: `unattended-upgrades` (auto-reboot **off**), a host
  reporter writing `os-status.json`, and a reboot watcher reading `reboot-window.json` - both under
  `ARGUS_STATE_DIR` (the host path you map as the core container's `ARGUS_UPDATE_DIR`).

## [0.4.31] - 2026-09-03

Finish the **self-configuring probe VM** (roadmap §A): full-fleet OVA delivery, a downloadable seed
ISO, **break-glass** console access, a configurable keyboard layout, and dropping cloud-init entirely.
Completes DESIGN §14a's delivery-vs-enrollment matrix and the break-glass item.

**Added:**
- **Downloadable seed ISO.** Add probe → **VM** gains a **Download seed ISO** button for hypervisors
  with no cloud-init field: `POST /api/probes/seed-iso` (admin) streams a small ISO the probe VM's
  first-boot service reads to self-enroll. It's an Argus-owned image (volume label `ARGUSSEED`, one
  8.3-safe `ARGUS.ENV`) - deliberately **not** a cloud-init NoCloud seed (which would need
  Joliet/Rock-Ridge to keep the `user-data`/`meta-data` names), so it sidesteps cloud-init's NoCloud
  datasource detection (fiddly on XCP-NG). The token is never persisted - the ISO is built on demand.
- **OVA delivery** (in `argus-probe`'s golden-image CI): the VM ships as an **OVA** (stream-optimized
  VMDK + OVF) for VMware/Nutanix/VirtualBox and Xen Orchestra (*Import → OVA*), alongside qcow2 and VHD.
  The image base moved to Debian 13 **`generic`** (full drivers) so it boots on non-virtio hypervisors
  and can read the seed CD.
- **Break-glass console access.** The probe VM generates a per-VM `argus` sudo user with a random
  password on first boot and reports it to Argus (`POST /api/probes/break-glass`, authenticated by the
  probe check-in token); Argus stores it **encrypted at rest** and reveals it to admins on the Probes
  page (a **Console** button;  `GET /api/probes/{name}/break-glass`). For hypervisor-console or
  VPN-SSH access when something's wrong.
- **Configurable console keyboard layout** for the probe VM - a picker in Add probe → VM and on the
  first-boot page; applied to `/etc/vconsole.conf` on first boot (default `us`).
- **Static networking for no-DHCP sites** - an optional Static IP section in Add probe → VM (address, a
  **subnet-mask dropdown** showing both the /prefix and dotted mask, gateway, and **two DNS** fields for
  redundancy, all validated) baked into the seed ISO (`ARGUS_IP` etc.); the first-boot service writes a
  static `systemd-networkd` file before enrollment, so the VM enrolls on a fixed address with no
  interaction. Seed-only (the first-boot page needs an IP to be reachable); a stuck VM is recoverable by
  re-attaching a corrected seed.
- **The probe VM takes the hostname `argus-probe-<site>`** (e.g. `argus-probe-site5`) on enrollment - the
  VM is the probe appliance; the container it runs is the Zabbix proxy (`proxy-<site>`). The break-glass
  `argus` user is also added to the `docker` group so it can run `docker` without sudo.
- **Terminology tidy-up:** the operator-facing copy consistently calls the deployable a **probe**;
  **proxy** is reserved for the Zabbix entity (the `proxy-<site>` name, the "Monitored by" assignment).

**Changed:**
- **Reworked "Add probe" into a guided modal wizard** - name → deploy method (+ only that method's
  settings) → deploy instructions → an optional live "waiting for enrolment ✓". Replaces the crowded
  inline panel that showed every method and setting at once. Back navigation between steps doesn't
  waste a token (it's minted once, on leaving the method step, and only re-minted if the name changes);
  the wait step polls the token status and is purely a visual aid (closing it changes nothing).
- **Dropped cloud-init from the probe VM.** The golden image purges cloud-init after the build; the
  appliance self-configures via systemd-networkd + the first-boot service. The wizard's VM option is now
  seed ISO + first-boot page (the cloud-init paste path is retired). VM defaults bumped to 30 GB disk /
  4 GB RAM.

**Fixed:**
- **A failed update check no longer masquerades as "up to date."** When the core couldn't reach GHCR,
  the check bailed out and left the previous verdict in place, so "Check for updates" showed a
  falsely-reassuring "You're on the latest available build" - indistinguishable from an actual up-to-date
  result (as a GHCR outage made obvious). `/api/version` now reports the last successful-check time and
  whether the most recent attempt reached the registry; the About panel shows a **check failed** tag +
  "Couldn't reach the registry - showing the result from N ago" instead of the green "latest" verdict.
- **Core "Check for updates" missed newer `:testing` builds.** Channel resolution let the argus-updater
  sidecar's reported image tag override the version stamp, so if the sidecar reported anything but
  `testing` a development build (which can only come from `:testing`) was treated as the `latest`
  channel and never offered newer `:testing` images - the in-app check found nothing while Dockhand
  correctly saw the newer digest. A dev-stamped build is now authoritatively the testing channel
  regardless of the reported tag (the tag still disambiguates a clean build). Pure `channelFor` helper,
  unit-tested.

**Dependencies:** adds `github.com/kdomanski/iso9660` (pure-Go, builds the seed image).

---

## [0.4.30] - 2026-09-02

Unify self-update on one model across the core and the whole probe fleet: **two containers**
everywhere - the main container (a pure reporter, never holding the Docker socket) plus the shared
**argus-updater** sidecar that recreates it via the Docker Engine API and rolls back on failure.

**Added:**
- **One updater for everything.** The core self-updater and every probe now use the same
  `ghcr.io/g-guglielmi/argus-updater` image (its own version line), sharing one recreate engine, with
  modes `core` (file-channel), `probe-watch` (socket-holding probe sidecar, no compose), and
  `probe-recreate` (the one-shot self-update primitive). The probe image dropped `docker-cli` and is a
  pure reporter; the socket is only ever on the sidecar.
- **Update the updater.** The sidecar can update itself (via an ephemeral `probe-recreate` copy). A
  new **Updater** column on the Probes page (per-probe sidecar version + an admin Update button) and
  an **Updater sidecar** section in Settings → About (version + Update sidecar) drive it. New
  `POST /api/probes/{name}/updater-update` and `POST /api/update/updater`.
- **Two-container deploys from the wizard.** The Add-probe wizard's Docker-run and Compose tabs now
  emit both containers (proxy + `probe-watch` sidecar); the probe VM installs them as two systemd
  units (`argus-probe` + `argus-updater`); unRAID keeps its native auto-update.

**Changed:**
- **Retired the socket-on-proxy self-update path** (`ARGUS_PROBE_SELFUPDATE`) and the compose-specific
  `probe-poll` updater mode in favour of the single sidecar model.
- Two-reporter check-in: a socket-less proxy reports its version but omits self-update capability
  while the sidecar advertises capability but omits a version; the fields are sticky (an omitted field
  keeps the stored value) and one-shots are handed only to a capability-advertising caller, so the two
  never clobber each other or race. `RecordProbeCheckin` takes `selfupdate *bool`.
- Settings → About standardized: parallel **Core** / **Updater sidecar** rows with prominent labels
  and real (bordered) buttons.

## [0.4.29] - 2026-09-01

**Added:**

- **Delete a proxy from the Probes page.** Admins can remove a decommissioned probe: it's deleted from
  Zabbix (`proxy.delete`) and its Argus-side records (enrollment tokens, check-in/version state, SNMP
  default) are cleaned up. Zabbix's "still monitored by hosts" error is surfaced so you can reassign
  them first; the `proxy-<site>` host group is left in place.
- **"Clean up" on the Probes page** prunes Argus records orphaned by proxies deleted directly in the
  Zabbix UI (out of band), keeping pending enrollment tokens whose proxy doesn't exist yet.

**Changed:**

- **The update-available state on the Probes page is now highlighted in amber** (the button + the
  "→ version" chip), so an outdated probe stands out at a glance instead of rendering muted like the
  other states.
- **Project split into three repositories** - `argus-core` (this app), `argus-probe` (the probe Docker
  image + self-configuring golden VM), and `argus-updater` (the self-update sidecar). Image names are
  unchanged, so deployments are unaffected; the Add-probe **Compose** command now fetches its compose
  file from the argus-probe repo.

---

## [0.4.28] - 2026-09-01

**Improved:**

- **Alert trend graphs now scale the Y axis by the sensor's units**, matching the app. Byte counters
  read as KB/MB/GB (1024-based), bit rates as Kbps/Mbps/Gbps, and uptime as a duration (e.g. `817.1d`,
  `4.1h`, `45s`) - instead of raw or scientific-notation values like `7.06e+05`. Unitless values keep a
  compact SI form (`70.6M`).

**Added:**

- **Add probe → "VM (cloud-init)"** deploy output: emits cloud-init user-data for the self-configuring
  probe VM, so a VM enrolls with zero touch (paste it into the hypervisor's cloud-init field). The
  golden image itself is built and released separately under `probe-vm/vX.Y.Z`.

---

## [0.4.27] - 2026-09-01

**Added:**

- **Global quick-switcher (Ctrl-K).** A new search box in the top bar - and the **Ctrl/Cmd-K**
  shortcut from anywhere - opens a palette that searches **hosts** (by name or IP), **sensors** (by
  name), and **host groups** (by name). Arrow keys move, Enter jumps: a host opens in the tree, a
  sensor opens its chart, a group focuses the tree on it. Matches rank prefix and word-boundary hits
  above mid-word ones.
- **Per-channel notification severity floor.** Each notification channel can now set its own minimum
  severity - **Warning & up**, **Average & up**, **High & up**, or **Disaster only** - in the channel
  editor (next to Site), with the floor shown on the channel card when it's above the default. A
  problem below a channel's floor no longer routes to it, and its recovery notice follows the same
  rule. Defaults to Warning, matching the previous behavior, so existing channels are unchanged.
- **Add-probe self-update toggle.** The "Add probe" deploy panel gained an **Enable self-update**
  checkbox that adds the Docker-socket mount and `ARGUS_PROBE_SELFUPDATE=1` to the generated
  Docker-run command (and the equivalent socket volume + variable to the unRAID XML), so a
  socket-enabled probe deploys straight from the wizard instead of hand-editing the command. The
  Compose format already bundles the updater sidecar, so there it's always on.
- **Labeled axes on alert trend graphs.** The 2-hour trend PNG in every problem and recovery alert now
  carries axis labels - min/mid/max value gridlines on the Y axis and relative time (2h ago → now) on
  the X axis - for at-a-glance scale without opening the app.

---

## [0.4.26] - 2026-08-31

**Fixes:**

- **Group drill-down is now reflected in the URL.** Focusing a group in the monitoring tree (clicking a
  group name or a breadcrumb) now updates the address bar to `?view=monitoring&group=<path>`, so a
  reload, bookmark, shared link, or Back/Forward restores that group view instead of dropping back to
  the tree root. Host and sensor focus were already URL-persisted; group focus was the gap.
- **Browser Back/Forward now step through the tree's drill levels.** Explicit drills (clicking a
  group/host/sensor name or a breadcrumb) push history entries, so Back walks back up root ← group ←
  host ← sensor and Forward re-drills - instead of Back jumping straight out of the Monitoring tab.
  Inline accordion toggles (expanding a host card or sensor row) still don't add history entries.

---

## [0.4.25] - 2026-08-31

**Changes:**

- **Advanced mode declutters the monitoring toolbar.** The Sites & hosts toolbar had grown crowded, so
  the two rarely-used controls - the **All sensors** view toggle and **hidden-group management** (Show
  hidden / per-group hide) - now appear only when **Advanced mode** is on. It's a per-user preference
  (like the landing page), toggled from the admin-only **Settings → Interface** tab, so only an admin
  can enable it and only for their own view - no one else is affected. Off by default; with it off the
  toolbar is just **+ New group** and **Reorder**.
- **"+ New group" is now the primary (blue) button**, matching "+ Add channel" and "+ Add probe" on the
  other tabs.
- **Sidebar icon rework and polish.** Reworked two nav icons to match their tab: **Monitoring** is now a
  hierarchy/tree (sites → hosts → sensors) instead of stacked bars, and **Triggers** is a pulse/threshold
  spike instead of a generic list. Also fixed the Settings gear, whose hand-rounded path left a misshapen
  tooth in the lower-right, by swapping in the clean canonical gear; and vertically centered the Overview
  gauge (it was top-weighted, which made the space below it in the collapsed rail look like an uneven gap).
- **Group separators in the collapsed sidebar.** When the rail is collapsed to icons, the
  Watch/Configure/Admin section labels can't show their text, so they now render as thin divider lines -
  keeping the same grouping the expanded sidebar shows.
- **Hiding a group is now admin-only and asks for confirmation.** Unhiding a group is only reachable in
  advanced mode (an admin-only preference), so the **Hide from tree** / **Show in tree** actions now
  appear only there too - closing a gap where a helpdesk user could hide a group and then have no way to
  bring it back. Hiding also now shows an in-app confirm (it can tuck a group out of everyone's view),
  and the write endpoint (`PUT /api/tree/hidden`) is restricted to admins.

---

## [0.4.24] - 2026-08-31

**Features:**

- **Manually reorder the monitoring tree.** The tree defaulted to alphabetical; a **Reorder** button in
  the Sites & hosts toolbar now reveals inline up/down arrows on every group and host so their order can
  be set by hand (e.g. put Network above Infrastructure). The order is stored in Argus (Zabbix has no
  group/host ordering) and applies per sibling set; groups or hosts added later fall to the end,
  alphabetically, until moved. Admin/helpdesk only. New endpoints `GET`/`PUT /api/tree/order`.
- **Hide groups from the monitoring tree.** A group's kebab now has **Hide from tree** (and **Show in
  tree**), tucking a group and its subtree out of view without touching Zabbix - handy for the stock
  Zabbix groups (Applications, Databases, …) that can't be deleted because a host prototype references
  them. Hidden groups are hidden for everyone; admin/helpdesk can reveal and manage them with the
  **Show hidden** toolbar toggle. Argus-local, new endpoints `GET`/`PUT /api/tree/hidden`.

---

## [0.4.23] - 2026-08-31

**Fixes:**

- **Self-update no longer loops on a just-released build.** Cutting a release rebuilds the release
  commit twice (the main-push build and the tag build), producing two images with the same code but
  different digests; whichever `:testing` push landed last could differ from the `vX.Y.Z` tag, and the
  digest-only update check then offered a "newer `:testing`" update to the box's own commit forever.
  The check is now **commit-aware**: if `:testing` and the running image share a git revision
  (`org.opencontainers.image.revision`), it's the same code and no update is offered. CI also
  **serializes image builds** (a `concurrency` group) so the tag build is the last writer of the
  rolling tags and leaves `:testing` cleanly stamped `vX.Y.Z`.

---

## [0.4.22] - 2026-08-31

**Host settings editor (phase 2b) - manage a host's identity + connection from Argus:**

- A **"Settings…"** action on each host (admin/helpdesk) edits the **visible + technical name**,
  **Monitored by** (Server / Proxy with a proxy picker), and the host's **Agent and SNMP interfaces**
  - add, edit and remove, with connect-via IP/DNS, port, and SNMP version/community (v1/v2c/v3).
  One Save reconciles the whole desired state.
- **Removing an interface no longer strands its checks.** If items still use the interface, Argus
  moves them to a surviving interface first (so you can swap an Agent interface for SNMP without
  losing ICMP ping). A check that needs an interface of the same type still surfaces a clear refusal.
- **Group moves can keep the collector in sync.** Moving a host into a site group that matches a proxy
  (`proxy-<site>` ↔ group `<site>`) offers to switch its **Monitored by** to that proxy too.

**PRTG-style SNMP inheritance:**

- Each proxy holds a **default** SNMP credential set - edit it in the Probes tab (the proxy's
  **Defaults** button). Community and v3 passphrases are encrypted at rest.
- A host's SNMP interface can **Inherit** its proxy's default or **Override** per host. New SNMP
  interfaces default to Inherit when a default exists.
- Changing a proxy default **propagates live** to every inheriting host, and setting a default
  **offers to adopt** existing per-host overrides. Both report how many hosts were updated.

**Fixes:**

- Clicking the **Monitoring** tab now returns the tree to the root even when it's already the active
  view and you're drilled into a group/host/sensor.

## [0.4.21] - 2026-08-31

**Manage tree groups from Argus (first Zabbix *config* writes):**

- **Create, rename, delete groups and move hosts between them** from the Monitoring tree
  (admin/helpdesk). New group CRUD (`/api/groups`) + per-host membership (`/api/hosts/{id}/groups`),
  backed by new Zabbix host-group client methods. Deleting a non-empty group is refused; a host always
  keeps at least one group. Requires the Zabbix API token to have **super-admin** rights.
- **Nested groups render as a real hierarchy.** Zabbix nests host groups by name with `/`
  (`site1/Network`); the tree now draws that as `site1` › `Network`, with rolled-up host counts and
  status per node. There are **no virtual parents** - only real groups are nodes; a group with no real
  ancestor sits at the top level under its full name.
- **A host group per probe.** Enrolling a probe now also creates its site's top-level group (named
  after the site). Probes enrolled before this are seeded once, at startup.

**Everything in the web UI - no browser popups:**

- Replaced every native `window.confirm` / `window.prompt` / `window.alert` with an in-app,
  theme-aware dialog (`useConfirm` / `usePrompt` / `useAlert`): version/channel switch, delete channel,
  revoke probe token, user reset-password / reset-MFA / reset-passkeys / disable / delete, MFA disable +
  recovery-code regen, passkey add/remove, and probe update/check-in errors. Group create/rename/delete
  use inline editor bands.

**Fixes:**

- **Self-update on a clean-tag testing box.** A box running a clean `vX.Y.Z` image that tracks
  `:testing` showed the "Update" button but the click was refused ("no newer release available") because
  the action gated on `status=="development"` while the button used a broader rule. The button and the
  click now share one predicate, so the in-place testing update applies.

## [0.4.20] - 2026-08-30

**Monitoring drill-down (PRTG-style):**

- **Click through the tree.** The group, host and sensor names in the Monitoring tree are now links
  that narrow the view to just that level, with a breadcrumb (`Sites & hosts / Group / Host / Sensor`)
  to step back up. The chevrons still expand inline for a quick peek - name = drill in, chevron = peek.
- **Deep-links land focused.** Opening a host or sensor from the Overview, a status-chip list or the
  Triggers tab now drops you straight into that focused view; the address bar, the Back button and a
  reload all restore it.
- **Key sensors / All sensors** toggle is hidden at the single-sensor level (where it does nothing) and
  kept at the group and host levels, so you can still switch modes while focused on one host.

## [0.4.19] - 2026-08-30

**Sensor priority (PRTG-style):**

- **Per-sensor priority (1-5).** Set a priority on each sensor from the Monitoring tree (a star column,
  editable by admin/helpdesk). It's stored in Argus and never touches Zabbix. Higher priority sorts
  first in the Overview and the status-chip lists, and is shown read-only in those lists.

**The Overview is now sensor-centric (one unified list):**

- The Overview and the status-chip lists are the **same** sensor list with different filters: the
  Overview is the "needs attention" (not-OK) filter with an Errors / Errors+Warnings toggle, and each
  top-bar chip is a single-state filter. They share the same fixed-width columns, so every list lines
  up (a 1-row list matches a busy one) instead of the browser re-sizing columns per row count.
- Each unhappy sensor shows a coloured **"why" line** under its name - the worst trigger's severity and
  name, and how long it's been firing, e.g. "High · Unavailable by ICMP ping · 17d". Severity is
  therefore visible in every list, without an empty column on the OK list.
- The old problem-centric overview is gone; its actions moved into a per-row **kebab** (acknowledge for
  any user; pause/hide the host for admin+helpdesk), matching the sensor lists.

**New Triggers tab:**

- A trigger-centric view with a **Firing / All** toggle. *Firing* is a flat cross-host table of the
  triggers currently in problem; *All* groups every monitored trigger by host with OK / firing state.
  Both list the sensor(s) each trigger's expression watches, so **multi-sensor triggers are visible**.
  Backed by a new `GET /api/triggers`.

**Update system:**

- **Testing-channel updates are detected on a clean "aligned" build.** Right after a release, `:testing`
  and `:latest` are the same clean `vX.Y.Z` image, so the core couldn't tell which channel it tracked
  and stopped offering newer `:testing` builds. The `argus-updater` sidecar now reports the tag the core
  container actually runs under (into the shared dir), so a box on `:testing` keeps getting testing
  updates - no configuration needed.

**Polish:**

- Priority and severity columns in the Overview/status lists, evened-out column spacing, and a taller,
  wider trend sparkline for readability. Sensor graph height 260 -> 320.

## [0.4.18] - 2026-08-26

**Probes table:**

- **Update column no longer wraps or splits a version.** In the probes fleet table the "Update" cell
  could break mid-version (e.g. `7.0.30-` on one line, `r1` on the next). The button, the `-> version`
  chip and the `auto` tag now sit on one rigid line.
- **Columns size to their content.** The 7-column probes table had been inheriting the 4-column
  enrollment table's fixed widths (column 1 = 40%), which ballooned the Probe column and starved the
  Update column (forcing the wrap above). The table now uses content-based auto-layout - every column
  sizes to its text, the table fills the card width, and a horizontal-scroll wrapper covers very narrow
  windows instead of wrapping a cell.

## [0.4.17] - 2026-08-20

**Update checks:**

- **Name the exact target of a `:testing` update.** A testing update previously read "new testing
  build" / "Update to the latest testing build" without saying *which* build. Images are now stamped
  with their `git describe` version as an OCI label (`org.opencontainers.image.version`), and the core
  reads the `:testing` image's label so the About card names the target precisely (e.g. "↑
  v0.4.16-3-gabcdef1" and "Update to v0.4.16-3-gabcdef1"). Falls back to the generic wording if the
  label isn't present (an image built before this change).

**Fixes:**

- **`:testing` no longer looks "rolled back" right after a release.** A release tags the commit that is
  also tip-of-main, but the pre-tag main build had already published `:testing` stamped `vPREV-N-g<sha>`
  (git describe couldn't see the not-yet-created tag). So switching `latest -> testing` straight after a
  release swapped to an identically-coded image that merely *read* as an older version. The v* release
  build now also refreshes `:testing` (cleanly stamped, same commit), so `:testing` is never older than
  the release it contains; it advances again on the next main push.
- **Testing build no longer shows a phantom "update to release".** A `:testing` build is named by
  `git describe` from the last tag (e.g. `v0.4.15-9-g0517cd4` is the v0.4.16 commit). The update check
  compared that numeric base (`0.4.15`) against the newest release (`0.4.16`) and reported "outdated -
  update to v0.4.16" - but an in-place update channel-preserves `:testing` (the same commit), so it
  could never reach the clean release tag, leaving a permanent update prompt that did nothing.
  `appUpdateStatus` now classifies any development build as "development" up front and never compares
  its base against releases; whether a *newer testing image* exists is decided solely by the
  digest-based dev-channel check. To move onto a clean release, use "Change channel or version".

## [0.4.16] - 2026-08-20

**Fixes:**

- **Detect updates on the `:testing` channel.** A development build (e.g. `v0.4.15-4-g1098b9e`) reads
  as "ahead of the newest release", so the update check - which only compared against `v*` releases -
  never flagged an update for it, even when a newer `:testing` image had been published (a container
  manager would show the drift; Argus wouldn't). The core now also compares, via GHCR image digests,
  the running commit's image (`sha-<short>`) against the current `:testing` digest; when they differ it
  surfaces "new testing build" with an "Update to the latest testing build" button. The updater's
  existing channel-preserve re-pulls `:testing` in place. Release-channel behaviour is unchanged.

**Update checks:**

- **Switch channel / version from the GUI.** The About card now has a "Change channel or version"
  control to deliberately move the core between `latest`, `testing`, and the last 5 published releases
  (pinning). A plain "Update" still preserves the current channel; a switch sends the exact target and
  the updater converges on it (a new `exact` flag bypasses channel-preserve). Picking a specific
  version pins the core until you switch back to a channel; the UI confirms first (and warns that a
  version pin stops channel tracking). **Requires redeploying the `argus-updater` sidecar** to pick up
  the new script (the updater doesn't self-update). New `GET /api/version/tags`; `POST
  /api/update/start` accepts an optional `{target}`.
- **Nightly check + manual "Check for updates".** The automatic GHCR update check now runs once daily
  at 04:00 (in the configured timezone) instead of every 3 hours - releases are rare, so nightly is
  plenty and lighter on the registry. For on-demand checks (e.g. right after publishing a build), the
  About card gained a **Check for updates** button that forces an immediate re-check and reflects the
  fresh verdict.

**UI:**

- **Subtle motion pass.** Added short, tasteful transitions so the UI no longer hard-cuts: the app
  fades in right after sign-in (the biggest, most noticeable cut), switching
  sections fades + rises the content (~180ms), opening a sensor chart and expanding a host reveal with
  the same fade-rise, banners fade in, and the sensor caret rotates smoothly. Backed by shared
  `--dur-*` / `--ease-out` motion tokens, and every animation is disabled under
  `prefers-reduced-motion`.
- **Green Reload button after an update.** The post-update "Reload to finish updating" button now uses
  a green (success) variant instead of the default accent colour, so it visually matches the success
  banner above it. New `success` Button variant backed by the `--ok` token.

## [0.4.15] - 2026-08-20

**Fixes:**

- **Long-range charts (1M/3M/6M/1Y) rendered as a shattered "comb".** These ranges read Zabbix
  *trends*, and `trend.get` - unlike `history.get`, which we sort ascending - has no reliable sort
  order, so points could arrive out of chronological order. uPlot requires strictly-increasing
  timestamps, so an unsorted series drew lines jumping back and forth in time (the spiky/gappy look);
  short ranges using raw history were unaffected. The history endpoint now sorts every series by
  timestamp before returning it, so trend charts render as a clean, monotonic line.
- **Core/updater version stamped with the probe's tag.** CI computed the build version with
  `git describe --tags`, which matches *any* tag - so a `:testing` build cut before a `v*` release tag
  existed fell back to the nearest tag of any kind, e.g. the automated `probe/v7.0.29-r7` bump
  (`probe/v7.0.29-r7-5-g0cc2d40` shown as the core's version). The core and updater builds now use
  `git describe --match 'v*'`, so only app release tags name the build (probe tags start with
  `probe/` and are excluded). Cosmetic - it never affected behaviour, only the displayed version.
- **Clearer post-update action.** After a successful self-update the About card kept showing the
  "Update to vX.Y.Z" button (the still-running old bundle thinks it's outdated) alongside a small
  inline "Reload" link - confusing. It now replaces that button with a single primary **"Reload to
  finish updating"** button, so the only offered action is the one that actually completes the update.

## [0.4.14] - 2026-08-20

**Fixes:**

- **Max session length is now enforced live.** The absolute session lifetime was written into the
  session at login and only ever checked against that frozen value, so changing "Max session length"
  in Settings only affected sessions created *after* the change - an existing sign-in kept running for
  its original lifetime (e.g. a session issued when the max was higher could stay valid for days).
  The middleware now also enforces `created_at + current max` on every request (effective expiry =
  `min(stored expiry, created_at + current max)`), matching the idle timeout's live behaviour:
  lowering the max signs affected users out on their next request; raising it never retroactively
  extends an existing session. Settings note updated accordingly.

## [0.4.13] - 2026-08-19

**Self-update fixes** (from first real-world testing):

- **Self-update channel permissions.** A fresh Docker named volume mounts root-owned (`0755`), so the
  non-root, distroless core could not write `request.json` ("could not queue the update") nor clear a
  finished banner ("Dismiss" did nothing) - it could only read the sidecar's status. The
  `argus-updater` sidecar (root, holds the socket) now makes the shared channel dir writable on
  startup, self-healing both new and existing volumes. Ships in `argus-updater:latest`.
- **Preserve the release channel instead of pinning.** A `:latest` / `:testing` core self-updated to a
  fixed `:vX.Y.Z` tag, dropping it off its channel. The updater now re-pulls the core's current tag in
  place when it's a rolling channel (`:latest` / `:testing`), and only bumps a genuinely pinned
  (`:vX.Y.Z`) deployment. Ships in `argus-updater:latest`.
- **"Reload" after a successful update.** The success banner offered only "Dismiss", but the running
  SPA is still the old bundle (and shows the old version) until reloaded - so it now offers a
  **Reload** action (which clears the job, then reloads to the new frontend + version).
- **Dismiss/Reload no longer trip "no settings to update".** Those raw buttons sit inside the Settings
  `<form>` and defaulted to `type="submit"`, so clicking them submitted the (empty) settings form.
  Marked them `type="button"`.

**Other fixes:**

- **Login-screen flash on refresh.** The initial-load state reused the branded login `Frame`, so an
  authenticated page refresh briefly flashed the login chrome before the app mounted. Replaced with a
  neutral loader.

## [0.4.12] - 2026-08-19

**One-click core self-update.** (Roadmap §F/§G)

Argus **Settings → About** can now update the core to the newest release with a single click. Because
the public-facing core is a distroless, non-root container with **no Docker socket**, it cannot
recreate itself - so a small companion container, **`argus-updater`**, holds the socket and does the
work on the core's behalf. The core never touches Docker.

- **`argus-updater` sidecar** (new image `ghcr.io/<owner>/argus-updater`): watches a small volume
  shared with the core. When an admin clicks "Update now", the core drops a request there; the updater
  pulls the target release, recreates the core cloning its config (binds/mounts, env, restart policy,
  network, ports), **verifies** the new container stays healthy (crash-loop guard + best-effort
  `/healthz`), and **rolls back** to the previous container on any failure. It runs no listening
  service. Reuses the proven `argus-recreate.sh` recreate/rollback approach.
- **Core endpoints** (`coreupdate.go`): `POST /api/update/start` (admin), `GET /api/update/state`,
  `POST /api/update/dismiss` (admin). A file-drop channel (`request.json` / `status.json`, atomic
  writes) - no socket, no network endpoint, no tokens; least-privilege (the sidecar shares only a
  dedicated `argus-update` volume, not the core's `/data`). New env `ARGUS_UPDATE_DIR` enables it;
  unset leaves the feature off and Settings shows a manual update note instead.
- **UI**: an "Update now" button plus a running / success / **failure** banner (with the reason and a
  "rolled back" note), and the update state is polled so it reconnects across the brief restart.
- **Deploy**: `deploy/updater/` (Dockerfile + `core-update.sh` + `docker-compose.yml`), a new
  `argus-updater` Unraid template, `ARGUS_UPDATE_DIR` + shared-volume rows added to the core's Unraid
  template and the README env table, a README "One-click self-update" section, and a
  `updater-image.yml` CI workflow.

## [0.4.11] - 2026-08-19

**Release channels - `:testing` for main, `:latest` for releases.** (Roadmap §G)

Pushes to `main` previously published `:latest`, so `:latest` meant "tip of main" (unreleased code).
Now `main` builds publish **`:testing`** and `:latest` is reserved for actual `v*` releases (alongside
`:vX.Y.Z`), so production can safely pin `:latest` and a test box can track `:testing` - no manual
tagging. Every build still gets a `:sha` tag.

**Version indicator - correct "development build" verdict + changelog.** (Roadmap §F)

- The verdict no longer shows a green **LATEST** for a build that is merely *ahead* of the newest
  release. `appUpdateStatus` now distinguishes a clean release tag equal to the newest release
  (`current` -> green tick) from a `git describe` build past its tag or a base newer than anything
  published (`development` -> neutral "development build" tag). Fixes a `:testing`/local build reading
  as "latest", and drops the redundant "newest release" line next to the badge.
- **"What's new"**: when an update is available, Settings shows the release notes, fetched from the
  GitHub Releases API (the CHANGELOG section published as the release body) via `GET
  /api/version/notes`, cached alongside the GHCR latest-version poll.

## [0.4.10] - 2026-08-19

**UI standardization / design system.** (Roadmap §F)

The SPA had grown two competing styling systems: the token-based CSS classes
(`.btn`/`.panel`/`.field`/`.badge`) used by most views, and a legacy inline-style-object system
(`card`/`btn`/`ghost`/`input`) with **hardcoded, non-token colors** (`crimson`, `seagreen`, `#aaa`,
`#888`, `#e59`, …) used by the auth flows, the Account cards, Dashboard and a few others. The
hardcoded colors ignored the theme tokens, so error/success text and muted labels rendered wrong
across light/dark, and the same widgets were rebuilt ad-hoc per view.

- **New `web/src/ui.tsx`** with shared primitives backed by the existing CSS classes: `Button`
  (variant/block), `Card` (title/note), `Field` (labeled input/select), `Banner` (theme-aware
  error/success/info/warn), `Badge` (on/off/err), and `CopyButton` (wraps the shared
  `copyToClipboard`).
- **Migrated** Login / ForgotPassword / ResetPassword, the whole Account family
  (Appearance / Landing / Password / 2FA / Recovery codes / Passkeys), Dashboard, the Users disabled
  badge, `DurationButton`, `SensorChart`, and the three Probes copy buttons onto the primitives.
- **Removed** the legacy `card`/`btn`/`ghost`/`input` objects and every hardcoded color, so the app
  themes correctly and pages look and behave the same. Additive CSS only: `.card`, `.banner`,
  `.callout-warn`, `.btn.block`, `.badge.err`, and `.field select` styling.
- No behavior changes - pure markup/style consolidation.

**Text readability - higher contrast in both themes.** (Roadmap §F)

The secondary-text tokens were too weak, especially on a dim display: dark `--faint` sat at ~3.5:1
on the panel and light `--faint` at ~3.1:1, both below the WCAG AA 4.5:1 target. Lifted
`--text`/`--muted`/`--faint` in all four token blocks (light `:root`, the dark `@media`, and both
`[data-theme]` overrides). Measured on the rendered page: dark faint 3.5 -> 5.9:1 and muted
6.3 -> 9.3:1; light faint 3.1 -> 5.0:1 and muted ~5 -> 6.9:1.

**Version indicator - "are we on the latest release?"**

- The running version is stamped into the binary at build time (`git describe --tags` via
  `-ldflags`), and a new authenticated `GET /api/version` reports
  `{version, latest, update_available, status}`.
- The core polls public GHCR for the newest published release (`vX.Y.Z`) - like the probe fleet
  already does - and compares only the `X.Y.Z` base, so a `:latest`/dev build ahead of the last
  release tag reads as **current** and a genuinely newer release reads as **outdated**.
- The sidebar footer now shows the running build with a green **latest** tick or an amber
  **↑ vX.Y.Z** update pill, so you can tell at a glance without checking the registry.
- Fixed a latent GHCR bug along the way: `tags/list` pages at 100, so once a repo passed 100 tags a
  single fetch missed the newest (the app repo has 167). Tag resolution now follows Link-header
  pagination for both the app and probe images.

**Dev: local build toolchain + CI now typechecks.**

- The CI Docker build ran only `vite build` (esbuild strips types without checking them), so
  TypeScript errors never failed the build. The web `build` script is now `tsc --noEmit && vite
  build`, making type-checking an enforced gate; fixed two pre-existing latent `tsc` errors it
  surfaced (dead `DashboardView`, an over-wide icon index).
- **Reproducible builds.** `go.mod` (now with its full `require` set), `go.sum`, and
  `package-lock.json` are committed, and the Docker build uses the pinned installers
  (`go mod download` + `npm ci`) instead of resolving/tidying at build time - so an image builds
  from locked dependency versions rather than whatever floats to the top on build day.

## [0.4.9] - 2026-08-19

**Dashboard-triggered probe self-update (`docker run`, no compose needed).** (Roadmap §A; DESIGN §18)

Builds on the exact-version check-in: a probe reports its full version *including our wrapper
revision* (`7.0.29-r2`) over the Argus channel, so the core sees subrelease drift - not just the
Zabbix release the API exposes.

- **Update now.** Give a probe the Docker socket + `ARGUS_PROBE_SELFUPDATE=1` and the Probes view
  shows an **Update now** button. It queues the fleet target for that probe
  (`POST /api/probes/{name}/update`), handed to the probe once at its next check-in.
- **Sister-container recreate.** Since a container can't `rm -f` itself mid-update, the probe spawns
  a short-lived `ARGUS_PROBE_ROLE=recreate` helper that clones the proxy's config onto the new image
  via the Docker Engine API (env, binds/mounts, restart policy, network, labels preserved) and
  **rolls back to the previous container if the new one fails to create or start** - a bad update
  never leaves a site without a probe.
- Only offered when the probe reports it's socket-capable; everything else keeps the read-only
  visibility + one-click manual command. unRAID probes stay on native auto-update.
- **Redeploy-aware wizard.** When the Add-probe wizard targets a name that already exists, the
  Docker-run command prepends `docker rm -f <name>` so a redeploy is a single paste (the data
  volume is a host bind mount, so it's kept and the probe stays enrolled). Compose and unRAID
  already recreate in place, so they're unchanged.
- **Enable reporting for pre-existing probes.** A probe enrolled before fleet updates has no
  check-in token (enrollment, which mints it, is skipped once certs exist), so it never reported its
  version even after a plain image update. The Probes view now offers **Enable reporting** on such
  probes (`POST /api/probes/{name}/checkin-token`): it mints a token to drop in as a single env var
  (`ARGUS_PROBE_TOKEN`) via the container's GUI - the check-in URL is derived from the enroll URL, so
  no re-enrollment is needed. The probe **saves the token to its data volume on first boot**, so the
  env var can be removed on later runs (a fresh env token always wins, for rotation).
- **No stray anonymous volume on probes.** The stock Zabbix image marks `/var/lib/zabbix/snmptraps`
  as a `VOLUME`, so Docker created an anonymous volume for it alongside the probe's bind mount. The
  generated Docker-run command, the compose file, and the unRAID templates now bind that subpath
  into the probe's data folder too, so everything stays in one place and no anonymous volume is
  created. The compose file also switches from a named volume to a `./data` bind.
- **Real drift against `latest`.** Argus core now polls the public GHCR tags list anonymously
  (every 3h, no auth) to learn the newest published `X.Y.Z-rN` probe revision, and compares it to
  each probe's reported version. When the fleet target is `latest`, probes behind the newest now
  show **outdated → 7.0.29-rN** instead of just "tracking latest"; the Fleet target control shows
  the newest published version too. Falls back to "tracking" until the first resolve succeeds.

---

## [0.4.8] - 2026-08-18

**Probe fleet updates - control plane + opt-in self-update.** (Roadmap §A; DESIGN §18)

Argus now centrally controls and sees probe versions, over the same outbound-only channel probes
already use (nothing inbound is opened).

- **Version check-in.** Enrollment issues each probe a long-lived check-in token; the probe reports
  its running image version (baked in at build, `/etc/argus-probe.version`) every ~5 min and reads
  the fleet target to converge on (`POST /api/probes/checkin`).
- **Fleet target.** Admins set the target in **Probes → Fleet target version**: `latest` or an exact
  pin in the decoupled probe scheme (e.g. `7.0.29-r1`). Stored server-side
  (`GET`/`PUT /api/probes/target`).
- **Fleet visibility.** The Probes view gains **Version** and **Update** columns showing each probe
  as up-to-date / outdated / tracking-latest / unknown, with an **auto** pill when a probe's
  self-updater is on. A **Last check-in** time that turns amber once a proxy's data is >1 min stale.
- **Version always visible.** Probes that don't check in (older images, or ones updated outside
  Argus like unRAID) still show their installed version, read from the version Zabbix already
  tracks for every connected 7.0 proxy (without the `-rN` wrapper revision, marked as externally
  managed). The precise self-reported version wins when a probe checks in.
- **Guided manual update (any deployment).** Drifted probes offer a one-click
  `docker pull … && docker restart …` - no Docker socket needed.
- **Opt-in self-updater (compose sidecar).** New `deploy/probe-image/docker-compose.yml` runs an
  `ARGUS_PROBE_ROLE=updater` sidecar that, with the Docker socket mounted, recreates the proxy to
  match the target automatically. Off unless deployed; the socket is isolated to the sidecar, never
  the proxy. The Add-probe wizard gains a **Compose + auto-update** output that generates it.

---

## [0.4.7] - 2026-08-18

**Configurable session timeouts + per-user landing page.** (Roadmap §E)

- **Max session length** is now configurable and defaults to **12 hours** (previously a fixed
  7-day absolute expiry). Set in **Settings → Sessions** or via `ARGUS_SESSION_MAX_HOURS`. Applies
  to sessions created after the change.
- **Idle timeout** (new, **off by default**): sign out after a period of inactivity. Each request
  refreshes the session's `last_seen` (throttled to at most once a minute); crossing the window
  drops the session. Set in **Settings → Sessions** or via `ARGUS_SESSION_IDLE_MINUTES` (`0`
  disables it). Changes take effect immediately for existing sessions.
- **Per-user landing page**: choose whether Argus opens on **Overview** or the **Errors** list on
  sign-in / a fresh visit, from **Account → Landing page**. A deep link or reload still restores
  the exact screen in the URL. Stored per user (`POST /api/me/preferences`), so it follows you
  across devices.
- Both timeout settings honour the usual **env-wins** precedence and are marked _(UI)_ in the
  README; the Unraid template gains the two `ARGUS_SESSION_*` variables (advanced).

---

## [0.4.6] - 2026-08-18

- **Removed the duplicate theme toggle.** The dark/light switch existed in both **Account** and the
  admin-only **Settings** page (with different markup). Theme is a per-device preference, so it now
  lives only in **Account**, where every role can reach it - Settings is server-wide config and no
  longer carries it. (Roadmap: a broader UI-standardization pass is now tracked under §F.)

---

## [0.4.5] - 2026-08-17

**Probes view tidy-up.**
- The token list now shows only **actionable** entries (pending / expired) and drops rows once a
  probe is enrolled - a redeemed token is no longer useful information. Heading is now
  "Pending enrollments".
- The live probe row gains an **Enrolled** column showing when each probe self-enrolled via Argus
  (a `-` for proxies registered by hand in Zabbix). Backed by a new `enrolled_at` field on
  `/api/proxies`, derived from the enrollment token's redemption time.
- The generated **unRAID probe template** now includes the Argus `<Icon>` (matching the server
  template), so the probe container shows the logo in unRAID's Docker tab.

---

## [0.4.4] - 2026-08-17

- **Probe container naming.** The generated **Docker run** and **unRAID XML** now name the probe
  container `argus-<proxy_name>` (e.g. `argus-proxy-site5`) with a matching data volume, instead
  of a fixed `argus-probe`. This makes each site's container self-identifying in the Docker/unRAID
  UI when several probes run on one host. Updated the Dockerfile example and the probe update/
  migration runbook to match.

---

## [0.4.3] - 2026-08-15

- **Login screen** is centered horizontally and anchored in the upper-middle (instead of top-left),
  and now shows the **Argus logo** beside the title.
- **Sidebar** uses the logo in place of the old eye glyph.

---

## [0.4.2] - 2026-08-15

**Branding - the Argus logo.** Added the eagle/radar logo across the project:
- **Browser tab favicon** (+ apple-touch icon) - served from the app (`web/public/`).
- **READMEs** (root + `argus/`) show the logo at the top.
- **unRAID template** `<Icon>` points at the logo (this is also the container's icon in unRAID's
  Docker tab).

Assets live in `argus/web/public/`: `argus-logo.png` (512², used by the READMEs + unRAID icon),
`favicon.png` (48²), `apple-touch-icon.png` (180²).

---

## [0.4.1] - 2026-08-15

Mobile polish:
- **Probes tables are now responsive** - the tokens and live-proxy tables stack into labelled
  cards on a phone (like the Users and status lists), instead of a cramped fixed 4-column table
  with truncated text and wrapped status pills.
- **Users: the passkeys count lines up** with the other values - it's inset to match the internal
  padding of the boxed values (2FA badge / role select / inputs) instead of sitting flush past them.
- Status pills (`.tag`) no longer wrap.

---

## [0.4.0] - 2026-08-14

**Probe enrollment - one-click, from the GUI.** Adding a site probe no longer needs
`gen-certs.sh` or manual Zabbix proxy registration.

- **Probes → Add probe** (admin): pick a site name + token TTL → Argus mints a **single-use,
  time-limited token** and shows a ready-to-deploy **`docker run`** *or* **unRAID XML template**
  for the new **`argus-probe`** image, with the enroll URL + token filled in (the token is shown
  once). Pending/recent tokens are listed with status (pending / enrolled / expired) and can be
  revoked. `ARGUS_PROBE_CORE_HOST` (the address probes dial for `:10051`) is editable in **Settings**.
- **Self-enrolling probe image** (`ghcr.io/<owner>/argus-probe`): on first boot it generates its
  own key + CSR **locally**, redeems the token against `POST /api/enroll`, receives its signed
  certificate + `ca.crt`, and starts the stock Zabbix proxy. **The private key never leaves the
  probe.** Certs persist on the volume, so a restart doesn't re-redeem the single-use token.
- Argus signs the CSR with the **mounted monitoring CA** (same CA Zabbix already trusts; the leaf
  subject is forced to the token's proxy name) and registers the proxy in Zabbix via `proxy.create`
  (active, certificate-pinned by issuer + subject).
- Enrollment is **off unless the CA is mounted** - new config `ARGUS_CA_CERT_FILE`,
  `ARGUS_CA_KEY_FILE` (mount read-only), and `ARGUS_PROBE_CORE_HOST` (address probes dial for
  `:10051`, defaults to the Public URL host). The Zabbix API token needs **super-admin** rights to
  register proxies. The `/api/enroll` endpoint is IP rate-limited.

New: `internal/pki` (CA load + CSR signing), `enroll_tokens` table, `POST /api/enroll` +
admin `GET/POST/DELETE /api/probes/tokens`, `zabbix.EnsureActiveProxyCert`, and
`deploy/probe-image/` (Dockerfile + enrollment entrypoint) built by CI.

---

## [0.3.3] - 2026-08-14

**Self-service password reset.** Users who forget their password can now recover it themselves
via an emailed link - no admin intervention.

- A **"Forgot password?"** link on the sign-in screen takes an email and sends a **single-use,
  1-hour reset link**; the reset page sets a new password. The link (`/?reset=…`) carries a
  256-bit token whose **SHA-256 is stored** (never the token itself), consumed on use.
- **Anti-enumeration**: the request endpoint always responds the same way and does its work in
  the background, so it never reveals whether an account exists. Requests are **rate-limited** by
  IP and by email; the reset submission is throttled by IP.
- On success, **all of that user's sessions are revoked** (sign-out everywhere). **MFA is
  untouched** - a reset changes only the password, so two-factor is still required at sign-in.
- **Delivery reuses your existing email notification channel** as the SMTP sender - no separate
  mail config. The "Forgot password?" link only appears when an email channel is configured
  (advertised via `/api/features`, like passkeys). The link uses the Public URL when set, else
  the request's own origin.

New: `password_resets` table + token lifecycle, `POST /api/password-reset/{request,confirm}`,
a reusable `notify.SMTP` transport (factored out of the alert email sender).

---

## [0.3.2] - 2026-08-14

**Sidebar polish.**
- The desktop sidebar **remembers whether it's collapsed or expanded** across reloads (stored
  per-device, like the theme).
- Removed the **theme toggle from the sidebar** now that it lives on the Settings page. To keep
  it reachable for every role (Settings is admin-only), the theme switch is also on the
  **Account** page - available to all signed-in users.

---

## [0.3.1] - 2026-08-14

**Deep-link URLs - the view now lives in the address bar.** Navigation was tracked in React
state only, so the URL stayed at the base FQDN and a **reload always dropped you back to
Overview**; notification "Open in Argus" links also didn't survive a refresh.

- The active view is encoded in the URL - `?view=…`, with `&filter=…` for the status lists and
  `&host=…&item=…` for an open host/sensor in the Monitoring tree. **Reload, bookmark, and share
  now restore the exact screen**, and notification deep-links are reload-safe.
- **Back/Forward** work: tab switches and deep-link jumps push history; expanding a host or
  opening a chart refines the URL in place (so Back steps between screens, not accordion toggles).
- Admin-only views (Users, Settings) are clamped to Overview if a non-admin opens a shared or
  stale link.
- Frontend-only; no backend change and no new dependency (native History API).

---

## [0.3.0] - 2026-08-14

**In-app Settings (admin).** A new admin-only **Settings** page moves the most frequently
changed configuration off the `docker run` command and into the UI - no redeploy to change it.

- **Editable in the UI**: the **Zabbix API URL + token**, the **Public URL** (notification
  links), the **timezone**, and the **login rate-limit** thresholds. Changes apply live - the
  Zabbix client, notifier, and rate limiter are reconfigured in place, no restart.
- **Env-wins precedence**: if the backing `ARGUS_*` variable is set, that value is used and the
  field is shown **read-only** ("via env"). Your existing `docker run … -e …` keeps working
  unchanged; drop a variable to manage that setting in the GUI instead.
- The Zabbix token is stored **encrypted at rest** (same AES-256-GCM as channel credentials);
  it's never sent back to the browser. The connection card shows live reachability after a save.
- **Not** movable (stay in env, by design): `ARGUS_SECRET_KEY` (it *is* the encryption key),
  `ARGUS_COOKIE_SECURE` / `ARGUS_TRUST_PROXY` (they govern the session you'd edit them with),
  the passkey `ARGUS_RP_*` (changing them invalidates passkeys), and `ARGUS_LISTEN` /
  `ARGUS_DATA_DIR` (needed before the app starts).
- Theme selection now also appears on the Settings page (still a per-device preference; the
  sidebar toggle stays for everyone).

New: `internal/settings` (env/DB resolver + live apply), `GET`/`PATCH /api/settings`,
`zabbix.Client.Configure` and `ratelimit.Limiter.Configure` for runtime reconfiguration.

---

## [0.2.8] - 2026-08-14

**Encrypt secrets at rest.** Sensitive values in the SQLite database are now stored encrypted with
AES-256-GCM instead of plaintext: notification channel credentials (Discord webhook, Telegram bot
token, SMTP password), users' TOTP seeds, and the alert-link signing key.

- **Zero-config by default**: a key is generated once and kept in `<data>/secret.key` (mode 0600).
  For real protection of database backups, set **`ARGUS_SECRET_KEY`** to a long random string -
  it's hashed to the key and kept off the data volume. Set it on the first v0.2.8 deploy and keep
  it stable (changing the key, or losing the keyfile, makes existing encrypted values unreadable -
  channels would need re-entering and 2FA re-enrolling).
- Existing plaintext values are migrated to encrypted form automatically on startup. Encryption is
  transparent - values are decrypted only in memory when needed.
- Password hashes (argon2id), recovery codes (hashed), and passkeys (public keys) were already not
  reversible and are unchanged.

New: `internal/secret` (AES-256-GCM, marker-prefixed ciphertext, passthrough when disabled).

---

## [0.2.7] - 2026-08-14

**Login rate limiting** (brute-force protection). Repeated failed sign-ins are now throttled by
both **client IP** and **account**, with a `429 Too Many Requests` (and `Retry-After`) once the
limit is hit; a successful sign-in clears the counters.

- Applies to the password step and the TOTP-code step (per-account limiting means IP rotation
  can't grind a single account, and it works even behind a shared proxy IP).
- Tunable via `ARGUS_LOGIN_MAX_ATTEMPTS` (default 7) and `ARGUS_LOGIN_WINDOW_MINUTES` (default 15).
- **Behind a reverse proxy** (HAProxy), set **`ARGUS_TRUST_PROXY=true`** so the real client IP is
  read from `X-Forwarded-For` (ensure the proxy sends it, e.g. HAProxy `option forwardfor`).
  Without it, all requests share the proxy's IP; account-level limiting still protects each login.

---

## [0.2.6] - 2026-08-14

Mobile card polish:
- **Status-list kebab** no longer opens off-screen - the action cell kept its desktop 44px width,
  which pinned the kebab to the left of the stacked card so its menu opened past the screen edge.
  It now spans full width with the kebab on the right.
- **Users cards**: the name/surname values are right-aligned to match the role, 2FA, and passkeys
  rows (the email title stays left-aligned).
- **Trend sparkline restored on mobile**: v0.2.4 dropped the trend column to save width; it's back
  as a labelled "Trend" row in the stacked Overview/status-list cards (hidden only when a
  problem/sensor has no graphable series).

---

## [0.2.5] - 2026-08-13

- **Mobile card labels**: the stacked lists from v0.2.4 dropped their column headers, so on a phone
  the Users page and status lists read as unlabeled values. Each stacked cell now shows its label
  (Name / Role / 2FA / Passkeys, Value / Last check / Age), with the email/host as the card title
  and the kebab in the corner. Empty name/surname show a clearer placeholder.

---

## [0.2.4] - 2026-08-13

**Mobile-responsive layout.** The dashboard was desktop-only; on a phone the sidebar squeezed the
content, the status chips stacked, and tables ran off-screen. Now (≤768px wide):

- The sidebar becomes an **off-canvas drawer** - hidden by default, slid in by the ☰ button over a
  dimmed backdrop, and closed by tapping the backdrop or a nav item. On desktop ☰ still collapses
  the rail as before.
- The top bar wraps: title + ☰ on the first row, the **status chips on their own horizontally
  scrollable row**.
- Problem/status lists and the users table **stack each row into a card** instead of scrolling
  sideways; the monitoring tree drops its trend column and tightens indentation.

---

## [0.2.3] - 2026-08-13

- **Configurable timezone** for notification timestamps: set `ARGUS_TZ` to an IANA name
  (e.g. `Europe/Rome`); defaults to `UTC`. The binary embeds the tz database (`time/tzdata`) so
  it works on the distroless image without a system zoneinfo.
- **Email graph fix**: the inline chart now uses a fully-qualified `Content-ID` (`<chart@argus>`)
  for broader client compatibility (some clients, Gmail included, are picky about bare cids).

Note: pulling a new image doesn't replace a *running* container - recreate it (or update the
pinned tag) to pick up a release.

---

## [0.2.2] - 2026-08-13

**2-hour trend graph in alerts.** Every problem and recovery notification now carries a compact
chart of the offending sensor's last two hours, rendered server-side to PNG (pure stdlib, no new
deps) in the status color.

- The image is **uploaded directly** to each channel - Discord webhook attachment
  (`attachment://`), Telegram `sendPhoto`, and an inline `cid:` image in the HTML email - so it
  works whether the instance is internal-only or public, with no image URL to host or expose.
- Graphs are best-effort: non-numeric sensors or sensors without history simply omit the chart.
- The **Test** button now renders a demo graph too, so the whole message format previews at once.

New: `internal/server/chart.go` (history fetch + PNG renderer) and `internal/notify/multipart.go`
(shared multipart uploader).

---

## [0.2.1] - 2026-08-13

**Richer alert messages.** Notifications now carry status, context, and one-click actions.

- **Status icon + color** on every channel: a 🔴/🟡/🟢 indicator in the title, Discord's colored
  embed with structured Host / Site / Reading fields, and a colored HTML email (with a plain-text
  fallback) instead of plain text.
- **The reading that fired it**: current value plus a best-effort threshold parsed from the trigger
  expression, e.g. "Value: 96 % (threshold >90)".
- **Recovery duration**: resolved notices say how long the problem lasted ("recovered after 14m").
- **Open in Argus** deep-link straight to the offending sensor's chart (the SPA now honours
  `?host=…&item=…` on load). Requires the new `ARGUS_PUBLIC_URL` env (your external base URL);
  links are omitted when it's unset.
- **One-click acknowledge**: a signed, HMAC-verified link that acknowledges the problem. A GET
  shows a confirmation page (so link previewers can't silently ack); the POST performs it. The
  signing secret is generated once and stored in the data volume.
- Removed the stale "Soon" badge from the Notifications sidebar item (Probes keeps its badge -
  enrollment is still pending).

Next: the 2-hour trend graph in the message body (email inline / link-out, since the instance is
internal-only).

---

## [0.2.0] - 2026-08-12

**Notifications - alerting engine + channel management.** Argus now watches active problems
itself and delivers alerts, respecting the same suppression rules as the Overview: acknowledged,
paused, and hidden items stay quiet.

- **Channels** (admin, Notifications tab): add/edit/enable/delete **Discord** (webhook),
  **Telegram** (bot + optional forum topic), and **Email** (SMTP: STARTTLS / implicit TLS / none)
  targets, each scoped to **all sites** or one **host group**. A **Test** button sends a sample
  notification so credentials can be verified end-to-end. Config is stored in the SQLite data
  volume (plaintext - single-tenant, private-VM assumption; env-key encryption may come later).
- **Engine** (background goroutine, 30s poll): Warning and Error problems alert; a **60-second
  debounce** suppresses flapping; a **recovery** notice follows when a fired problem clears.
  Problems already active at first-ever startup are **baselined** (never retro-alerted), and a
  problem stays pending (does not fire) while it is acknowledged / on a paused or hidden host, or
  until a channel serving its site exists.
- **Routing**: a problem routes to every enabled channel whose site matches one of its host's
  Zabbix host groups (or is "all sites").
- New: `internal/notify` package (Discord/Telegram/email dispatchers), notifier state machine,
  `notify_channels` / `notify_events` / `app_meta` tables, and admin `/api/notify/*` endpoints.

---

## [0.1.0] - 2026-08-12

**First minor release - feature-complete UI.** No code changes since v0.0.32; this marks the
milestone where the redesigned interface and the core monitoring feature set are complete. From
here, `0.1.x` continues for fixes and the next features (notifications), with `1.0.0` reserved
for the production-ready release.

What 0.1.0 delivers:
- **Auth & users**: roles (admin / helpdesk / viewer), argon2id + server-side sessions, TOTP
  two-factor with recovery codes, WebAuthn passkeys, and admin user management including
  enable/disable.
- **Monitoring** (Zabbix-backed): a site → host → sensor tree (grouped by host group), curated
  "key" sensors with live values, per-sensor charts (uPlot, history + trends) and inline
  sparklines.
- **States**: acknowledge, pause (actually stop collecting), and hide (suppress) at host and
  sensor level - with durations, auto-expiry, inheritance, and honest graph gaps.
- **Overview & summary**: a cross-site active-problem list with deep-links into the tree, and a
  six-state status summary (OK / Warning / Error / Acknowledged / Paused / Hidden) whose chips
  open filtered, cross-site sensor lists.
- **Live Probes** view (real Zabbix proxy status) and a polished, theme-aware (dark/light),
  collapsible shell that updates on its own.
- Placeholders for the upcoming **Notifications** and **Probe enrollment** work.

---

## [0.0.32] - 2026-08-12

**New UI, stage 4 of 4 - the Users page (+ real "disable user"), and Overview spacing.**
This completes the UI port from the approved mockup.

### Added
- **Redesigned Users page**: inline-editable **email / name / surname** (save on blur), a role
  dropdown, and a per-user **⋮ menu** - Reset password · Remove 2FA · Remove passkeys ·
  Disable/Enable user · Remove user. Add-user is a toggle form in the header.
- **Disable user** is now a real feature: a disabled account **cannot sign in** (blocked on every
  login path - password, 2FA, passkey), shown faded with a "disabled" badge, and re-enablable.
  Guarded so you can't disable yourself or the last remaining admin. New `disabled` column
  (additive migration), `POST /api/users/{id}/disabled`, and email is now editable via PATCH.

### Changed
- **Overview** is now a properly spaced table (Host · Problem · Trend · Age · action), matching
  the status lists - no more large gap between the description and the controls on wide screens.

---

## [0.0.31] - 2026-08-12

**Mini-graphs (for real) & collapsed-sidebar fix.**

### Added
- **Inline sparklines** are back - and now real, drawn from live history - in the Monitoring
  tree (new Trend column), the Overview problem rows, and the status-chip lists. Backed by a new
  batched **`GET /api/spark?items=…`** endpoint (one `item.get` + up to two `history.get`,
  server-downsampled to ~24 points) so a whole host/list loads its sparks in a single request.

### Fixed
- **Collapsed sidebar**: the user menu (Account · Log out) no longer gets clipped by the
  sidebar - it now overflows correctly and sits above the content.

---

## [0.0.30] - 2026-08-12

**Fixes & Account polish.**

### Fixed
- **Sensor rows keep their left status stripe when acknowledged / paused / hidden**, recoloured
  to that state instead of vanishing. State colours are now unified on the design tokens
  (acknowledged = its own washed-red everywhere, matching the chip).
- **Unacknowledge (and acknowledge) from the status-chip lists**: the sensor census now carries
  each sensor's problem event ids, so the Acknowledged list's ⋮ menu offers **Unacknowledge**,
  and the Errors/Warnings lists offer **Acknowledge**.
- **Account page**: added the **Confirm new password** field (with a match check), and the cards
  now share one width so their edges line up.

---

## [0.0.29] - 2026-08-12

**New UI, stage 3b of 4 - full status summary & filtered lists.**

### Added
- **Six-chip status summary** in the top bar - **OK · Warnings · Errors · Acknowledged ·
  Paused · Hidden** - counted from a new cross-host sensor census, updating live (and instantly
  on any ack/pause/hide).
- **Clickable chips → filtered lists**: click any chip to see just those sensors across all
  sites (host · sensor · value · last check · actions), with deep-links to each sensor's host or
  chart and a per-row kebab (pause / hide / resume / show).
- Backend **`GET /api/sensors`** - a census of the curated "key" sensors, each tagged with one
  state (hidden > paused > error > warning > acknowledged > ok). New `item.get`-based
  `AllItems` client method.

### Notes
- The census covers curated key sensors (the same set as Monitoring's "Key sensors"); unsupported
  sensors are treated as unknown, not counted as OK. On very large deployments this census will
  move to server-side counts.

---

## [0.0.28] - 2026-08-12

**New UI, stage 3a of 4 - Overview redesign, deep-links & instant refresh.**

### Added
- **Overview redesigned** onto the design tokens: severity-striped problem rows, faded
  acknowledged state, inline acknowledge / unacknowledge.
- **Deep-links from the Overview into the tree**: click a problem's **host name** to jump to it
  in Monitoring; click the **problem** to open that sensor's chart. `/api/problems` now returns
  each problem's `item_ids` for the sensor link.

### Fixed
- **The top-bar status summary now updates instantly** after an acknowledge / pause / hide,
  instead of lagging up to 30s until the next poll. A lightweight refresh signal fans a mutation
  out to the summary and any open view.

---

## [0.0.27] - 2026-08-12

**New UI, stage 2 of 4 - the Monitoring tree & live Probes.**

### Added
- **Site → host → sensor tree** in Monitoring, grouped by **Zabbix host group** (site = host
  group; a host in several groups appears under each; hosts with none fall under "Ungrouped").
  Collapsible sites and hosts, a worst-state dot per site, and a panel-level Key sensors / All
  sensors toggle. Backend: `host.get` now returns each host's groups (`selectHostGroups`), and
  `hostView` carries `groups`.
- **Kebab (⋮) action menus** on hosts and sensors, replacing the inline Pause/Hide buttons -
  Pause / Hide (with the duration picker), Resume / Show, and **Acknowledge** on a sensor that
  has an unacknowledged problem. Inherited (host-controlled) pause/hide is cleared at the host.
- **Live Probes page**: shows the real Zabbix proxies (the per-site collectors) with online /
  offline status (seen within 5 min), last check-in, and mode - replacing the placeholder table.
  New `GET /api/proxies` (proxy.get) and client method. Token enrollment is still "coming soon".

### Changed
- The sensor table, charts (range tabs), and problem panel now use the design-token styling.
  Full-size charts keep uPlot, so they retain axis ticks and the hover legend.

---

## [0.0.26] - 2026-08-12

**New UI, stage 1 of 4 - foundation & shell.** Start of the port from the approved design
mockup. This release lands the visual foundation and app shell; Monitoring, Overview, and Users
keep working inside it and get their full redesign in the next stages.

### Added
- **Design-token system** (`theme.css`): a cohesive set of CSS variables for colour, surfaces,
  borders, and shadow, driving every component. Cerulean accent kept distinct from the status
  palette (ok/warn/err/paused/hidden/acknowledged).
- **Light & dark themes** with a toggle (in the sidebar). The choice persists and is applied
  before first paint to avoid a flash; a forced repaint on toggle keeps text readable.
- **Left-sidebar shell**: collapsible sidebar (Overview, Monitoring, Notifications, Probes,
  Users) plus a top bar with the page title and a live status summary (errors / warnings /
  acknowledged) that links to the Overview.
- **Account moved into the user chip** menu (Account settings · Log out) - no longer a nav tab.
- **Placeholders** for the upcoming **Notifications** and **Probes** sections, marked "Soon".

### Changed
- Shared surfaces (cards, inputs, buttons, dropdowns) now read from the design tokens, so the
  existing views are theme-aware. Full per-view redesigns follow in stages 2-4.

---

## [0.0.25] - 2026-08-12

### Added
- **"Suppressed until" labels**: paused, hidden, and acknowledged items now show when they'll
  clear (e.g. "paused · until Aug 12, 14:30", or "no expiry" when indefinite) in the host list,
  sensor rows, the host problem panel, and the Overview.
- **Auto-refresh for the Monitoring view and graphs**: the host list, the expanded sensor
  values/last-check/problems (30s), and open charts (60s) now update on their own - matching
  the Overview, which already refreshed. Background refreshes don't flash a loading state.

### Internal
- Suppression reads return the expiry (`ActiveSuppressionMap`); views carry `*_until` fields.

---

## [0.0.24] - 2026-08-12

### Changed
- **Custom duration now uses a date/time picker** instead of a "how many hours" prompt. Pick a
  calendar date and time and the state (ack/pause/hide) holds from now until that moment.

---

## [0.0.23] - 2026-08-12

**Durations, un-acknowledge, and faded acknowledged state.**

### Added
- **Every suppression takes a duration** - Acknowledge, Pause, and Hide now offer
  **1 hour / 8 hours / 1 day / 1 week / Indefinitely / Custom…** When the timer expires the
  state clears automatically: hide/ack lazily, and a background **sweeper** re-enables timed
  **Pause**s in Zabbix.
- **Un-acknowledge** brings a problem back into the error state. Acknowledge is now tracked in
  Argus (with expiry) and mirrored to Zabbix, so it can be undone and can expire.
- **Acknowledged problems fade** (PRTG-style): red/amber become a muted tone in the Overview,
  the host problem panel, and the sensor-row highlight - still visible, clearly de-emphasized.

### Changed
- Suppression storage generalized to a single `suppressions` table (kind hide/pause/ack, scope
  host/item/event, optional `until`). Endpoints `POST /api/.../{pause,hide}` and
  `POST /api/events/{id}/ack` accept `duration_seconds` (0 = indefinite); `DELETE
  /api/events/{id}/ack` un-acknowledges.

---

## [0.0.22] - 2026-08-12

**Overview dashboard.** The cross-host "what's wrong right now" view - now the default landing.

### Added
- **Overview** tab: a single list of active problems across all hosts, with an
  **Errors / Errors + Warnings** toggle. Errors-only hides acknowledged problems; both views
  exclude problems on hidden or paused hosts (and whose sensors are all hidden).
- Acknowledge directly from the list; rows sort worst-first, then unacknowledged, then newest,
  and the view auto-refreshes every 30s. Shows "✓ All clear" when there's nothing to report.
- Endpoint `GET /api/problems` (`problem.get` across all hosts, joined to host/items via
  `trigger.get`).

---

## [0.0.21] - 2026-08-12

### Fixed
- **Graphs now show a gap when data is missing** (e.g. a paused sensor) instead of drawing a
  straight line across the empty period. Where the interval between two points exceeds ~1.75x
  the typical sampling interval, the line breaks.

---

## [0.0.20] - 2026-08-12

### Changed
- **Sensors inherit their host's Pause/Hide state.** When a host is paused or hidden, all its
  sensors now show as paused/hidden too (marked "· host"), and their individual Pause/Hide
  toggles are disabled - you can't resume a single sensor while its whole host is paused. This
  matches how disabling a host in Zabbix actually stops all of its sensors collecting.

---

## [0.0.19] - 2026-08-12

**Two distinct suppression actions: Pause and Hide.** Hosts and sensors each get both.

### Added / Changed
- **Pause** (blue) = PRTG-style stop: disables the host/item **in Zabbix**, so collection
  actually stops (a gap in the graph while paused). Resuming re-enables it. Requires the API
  token to have **write** permission - a read-only token returns a clear error.
- **Hide** (grey) = Argus-side suppression: keeps collecting but mutes alerting/surfacing.
  Instant, reversible, no extra Zabbix permissions.
- Both available for **hosts and individual sensors** (Helpdesk + Admin). Paused/hidden rows
  are dimmed and marked, with a blue or grey status dot.
- Endpoints: `POST`/`DELETE /api/{hosts,items}/{id}/pause` (Zabbix enable/disable) and
  `.../hide` (Argus). A host/item disabled directly in Zabbix now shows as **Paused** in Argus.

### Internal
- The Argus suppression store/table was renamed from `pauses` to `hidden` to match the new
  naming; the old `pauses` rows (test data) are not migrated.

---

## [0.0.18] - 2026-08-12

### Changed
- **Pause/Resume buttons are aligned** to the right edge for both hosts and sensors, so they
  sit in a consistent vertical column and are easy to find. The per-sensor button moved from
  the sensor-name cell to the right of its "last check" time.

---

## [0.0.17] - 2026-08-12

### Added
- **Per-sensor pause** - each sensor row now has its own Pause/Resume control (Helpdesk +
  Admin); paused sensors are dimmed and marked "(paused)". Complements host-level pause.
- Endpoints: `POST`/`DELETE /api/items/{id}/pause`; items carry a `paused` flag.

### Notes
- Acknowledge was already per-problem (each active problem has its own Acknowledge button);
  Zabbix acknowledges at the event level, tied to the specific failing trigger/sensor.

---

## [0.0.16] - 2026-08-12

**States model - Acknowledge & Pause.** The first of the state controls the dashboards will
build on.

### Added
- **Acknowledge** a problem from the Active problems panel - uses Zabbix's native
  `event.acknowledge`, so it's reflected in Zabbix too; acked problems show a "✓ acknowledged"
  marker. Available to any signed-in user.
- **Pause / Resume** a host (Helpdesk + Admin) - an Argus-side flag: paused hosts go grey,
  hide their problem count, and are marked "(paused)". Zabbix keeps collecting; pausing just
  suppresses Argus-side surfacing (and, later, alerting). Instant and reversible.
- Endpoints: `POST /api/events/{id}/ack`, `POST`/`DELETE /api/hosts/{id}/pause`.
- Problems now carry their Zabbix `event_id` and `acknowledged` state; hosts carry `paused`.

### Notes
- Acknowledge runs with the **API token's** Zabbix permissions - the token's user needs rights
  to acknowledge events (a read-only token will get a permission error).
- Pause is host-level for now; per-sensor pause and the acknowledged/paused dashboards come
  with the error/warning list views.

---

## [0.0.15] - 2026-08-12

### Changed
- **Trimmed CPU-state noise** in the curated view: "Key sensors" now shows only the meaningful
  CPU utilization states (overall + user/system/iowait/idle/steal); the near-zero states
  (nice/interrupt/softirq/guest/…) remain available under "All sensors".

---

## [0.0.14] - 2026-08-12

### Changed
- **Network traffic scales** to Kbps/Mbps/Gbps (1000-based bits), and **uptime** renders as a
  duration (e.g. `1d 4h 14m`) instead of raw seconds - in the table and on the graphs.
- **Network sensors are de-duplicated**: the per-interface error/dropped/packet counters that
  share the `net.if.in`/`net.if.out` key are now labeled distinctly (e.g. "Traffic in dropped
  (enX0)"), so the byte-rate row is no longer repeated.

---

## [0.0.13] - 2026-08-12

### Changed
- **Byte values are now human-readable** - sizes/throughput auto-scale to KB/MB/GB/TB
  (1024-based) in both the sensor table and the graphs, instead of raw bytes.
- **CPU utilization rows are labeled by state** (idle/user/system/iowait/…) instead of a dozen
  identical "CPU utilization" rows sharing the `system.cpu.util` base key.

### Fixed
- **Graph legend no longer shows "--" when idle** - it now displays the latest point's time and
  value when the cursor isn't over the chart, and formats them (scaled units, readable time).

---

## [0.0.12] - 2026-08-11

**Curated sensor views.** The Monitoring view no longer dumps every raw template item.

### Added
- Items are classified by their Zabbix key into a small set of **categories** (Ping, CPU,
  Memory, Disk, Network, Temperature, Uptime) with friendly labels, and the default view shows
  only those - grouped by category - so you see the sensors that matter and only when the host
  actually reports them.
- A **Key sensors / All sensors** toggle per host; "All sensors" still shows the complete raw
  list (via `GET /api/hosts/{id}/items?all=1`).

### Notes
- Classification is key-pattern based (`internal/server/curate.go`), covering the common
  Zabbix agent2 Linux + ICMP keys; multi-instance sensors (per-mount disk, per-interface
  network) stay distinct. Unrecognized items fall under "All sensors" and the mapping is easy
  to extend as new device classes come online.

---

## [0.0.11] - 2026-08-11

**Per-sensor graphs.** Click a numeric sensor to chart its history.

### Added
- Numeric sensor rows are now **clickable** and expand into a time-series chart (uPlot) with
  **2h / 2d / 1M / 3M / 6M / 1Y** range tabs and drag-to-zoom.
- Short ranges (2h/2d) read raw **history**; long ranges read **trends** and draw the avg line
  with a shaded min/max band - matching Zabbix's 30-day history / 730-day trend retention.
- Endpoint `GET /api/items/{id}/history?range=…` (history.get / trend.get). Non-numeric
  sensors aren't clickable and the endpoint rejects them.

### Notes
- New frontend dependency: `uplot` (tiny, dependency-free charting).

### Not yet (upcoming slices)
- Curated per-device-class sensor views (hide the raw template noise), self-service email
  reset, login rate-limiting, and the probe enrollment/PKI backend.

---

## [0.0.10] - 2026-08-11

**Show what's actually wrong.** A host's problem count now has detail behind it.

### Added
- Expanding a host shows an **Active problems** panel listing each firing trigger (name +
  severity color), and the **sensor row(s)** a problem references are highlighted and
  left-barred in the trigger's severity color.
- Endpoint `GET /api/hosts/{id}/problems` (via `trigger.get` with `selectItems`).

### Notes
- Some triggers reference an item that isn't in the visible list (or a computed expression),
  so the problem still appears in the panel even when no specific row highlights.

---

## [0.0.9] - 2026-08-11

### Fixed
- **Sensor values are now rounded** (2 decimals for values ≥ 1, 4 for sub-1 so small timings
  don't collapse to zero), with trailing zeros stripped. Text values and checksums are left
  as-is. No more 16-digit readings.
- **Sensor table alignment**: switched to a fixed table layout so long values (e.g. a 64-char
  checksum) wrap within the Value column instead of overflowing and pushing the right border
  out of alignment. "Last check" no longer wraps.

---

## [0.0.8] - 2026-08-11

**Read path - hosts & sensors.** The first monitoring-facing feature: Argus now reads live
data from Zabbix and shows it. A flat host list with per-host sensor values; graphs land next.

### Added
- **Monitoring** tab: lists Zabbix hosts with a status dot (OK / Warning / Error, derived from
  active trigger severity) and a problem count. Click a host to expand its **sensors** with the
  latest value + units and how long ago each was checked.
- **Authenticated Zabbix client**: read calls use an API token via a Bearer header
  (Zabbix 7.0 style). New methods `host.get`, `item.get`, `trigger.get`.
- Endpoints: `GET /api/hosts`, `GET /api/hosts/{id}/items` (any signed-in user).
- New config: `ARGUS_ZABBIX_API_TOKEN` (create it in Zabbix under Users → API tokens).

### Notes
- Severity mapping: Zabbix info/unclassified → OK, warning → Warning, average/high/disaster →
  Error. Without a token the health probe still works but Monitoring returns a clear error.

### Not yet (upcoming slices)
- Per-sensor history/trend **graphs** with the 2h/2d/1M/3M/6M/1Y time tabs, self-service email
  reset, login rate-limiting, and the probe enrollment/PKI backend.

---

## [0.0.7] - 2026-08-11

### Changed
- Widened the app's content area (max width 820 → 1200px) so the Dashboard and the Users
  table use more of the screen on desktop/wide displays. Individual forms keep their own
  narrower max-widths for readability, and the layout still scales down on phones/tablets.

---

## [0.0.6] - 2026-08-11

**WebAuthn passkeys.** Passwordless, phishing-resistant sign-in that completes the
auth-hardening track. Uses discoverable (resident) credentials, so login needs no username -
the authenticator lists available passkeys. Works with Bitwarden, platform authenticators
(Windows Hello, Face ID/Touch ID), and hardware security keys.

### Added
- **Register passkeys** in Account: add multiple, name each, see when they were added and
  last used, and remove them individually.
- **Passwordless login**: a "Sign in with a passkey" button on the sign-in screen runs a
  discoverable-credential ceremony and starts a session on success.
- **Admin "Reset passkeys"** on the Users table (with a per-user passkey count) to recover a
  locked-out account.
- Endpoints: `GET /api/features`, `POST /api/login/passkey/{begin,finish}`,
  `GET /api/me/passkeys`, `POST /api/me/passkeys/register/{begin,finish}`,
  `DELETE /api/me/passkeys/{id}`, `POST /api/users/{id}/passkeys/reset`.
- New config: `ARGUS_RP_ID`, `ARGUS_RP_DISPLAY_NAME`, `ARGUS_RP_ORIGINS`.

### How it degrades
- Passkeys are **feature-gated**: they appear only when the server is configured
  (`ARGUS_RP_ID` + `ARGUS_RP_ORIGINS`) **and** the page is a secure context (HTTPS or
  localhost). Reaching Argus by private IP over plain HTTP simply hides the passkey UI and
  falls back to password + TOTP - WebAuthn RP IDs can't be a bare IP.

### Changed
- The admin user list now reports a passkey count per user.
- Additive SQLite migration adds a `webauthn_handle` column and the `passkeys` and
  `webauthn_sessions` tables (no manual steps).

### Not yet (upcoming slices)
- Self-service email password reset, login rate-limiting, and the probe enrollment/PKI backend.

---

## [0.0.5] - 2026-08-11

**MFA usability fixes** from first-run validation.

### Fixed
- **Copy recovery codes** now works over plain HTTP on a private IP. `navigator.clipboard`
  only exists in a secure context, so the button silently did nothing when Argus was reached
  by IP over HTTP; it now falls back to a `textarea` + `execCommand('copy')` and shows a
  brief "Copied!" confirmation.

### Changed
- The two-factor login step now includes a visually-hidden `autocomplete="username"` field
  (the account email) so password managers such as **Bitwarden** recognize it as a login form
  and offer to autofill the one-time code; the code input also carries a stable `id`.

---

## [0.0.4] - 2026-08-11

**Two-factor authentication (TOTP).** Optional, self-service, and standards-based so it
works with authenticator apps and password managers (Bitwarden, 1Password, Google/Microsoft
Authenticator) - both scanning the QR and pasting the setup key.

### Added
- **TOTP enrollment** in Account: shows a QR code and the base32 setup key, then confirms
  with a live 6-digit code before turning MFA on (RFC 6238 defaults: SHA1 / 6 digits / 30s).
- **Two-step login**: when a user has MFA on, a correct password yields a short-lived
  challenge (no session yet); the second step verifies the code and then signs in. The code
  field is marked `autocomplete="one-time-code"` so Bitwarden can autofill it.
- **One-time recovery codes** (10) generated when MFA is enabled, shown once with copy and
  download; usable in place of a code at login. Regenerate at any time (re-auth required).
- **Disable MFA** yourself (re-auth with your password), and an admin **"Reset 2FA"** action
  on the Users table to recover a locked-out account.
- Endpoints: `POST /api/login/totp`, `GET`/`POST /api/me/mfa`,
  `POST /api/me/mfa/{setup,enable,disable,recovery-codes}`, `POST /api/users/{id}/mfa/reset`.

### Changed
- `GET /api/me` and the user list now report `mfa_enabled`.
- Additive SQLite migration adds `totp_secret` / `totp_enabled` to existing databases and
  creates the `recovery_codes` and `mfa_challenges` tables (no manual steps).

### Not yet (upcoming slices)
- WebAuthn passkeys, self-service email password reset, login rate-limiting, and the probe
  enrollment/PKI backend.

---

## [0.0.3] - 2026-08-11

**User management.** Makes the role model usable - admins can now manage the other accounts.

### Added
- Admin-only **user management**: list users, create, change role, reset password, delete.
- **Role enforcement** on the user endpoints (helpdesk/viewer receive 403).
- **Change-my-password** for any signed-in user (verifies the current password).
- Guardrails: you can't delete your own account, can't delete or demote the **last admin**,
  passwords must be ≥ 8 characters, and a duplicate email returns a clear conflict.
- Endpoints: `GET`/`POST /api/users`, `PATCH`/`DELETE /api/users/{id}`,
  `POST /api/users/{id}/password`, `POST /api/me/password`.
- UI: top navigation (Dashboard / Users / Account), a Users table with an add-user form and
  per-row role/reset/delete controls, and an Account password-change form.

### Not yet (upcoming slices)
- TOTP MFA, WebAuthn passkeys, self-service email reset, and the probe enrollment/PKI backend.

---

## [0.0.2] - 2026-08-11

**Authentication.** Adds persistent users, password login with sessions, and the role model -
the first real feature on top of the skeleton.

### Added
- **Embedded SQLite** persistence in the data volume (`argus.db`) for users and sessions.
- **Password login** using argon2id hashing and server-side sessions (HttpOnly cookie; session
  ids are stored hashed, so a DB leak can't yield usable tokens).
- **Three roles** recorded on each user: `admin`, `helpdesk`, `viewer`.
- **First-run admin bootstrap** from `ARGUS_ADMIN_EMAIL` / `ARGUS_ADMIN_PASSWORD` (only when the
  database has no users yet).
- **Endpoints:** `POST /api/login`, `POST /api/logout`, `GET /api/me` (auth-protected).
- **UI:** a sign-in screen and an authenticated dashboard showing the current user, role, and a
  log-out button.

### Changed
- New configuration: `ARGUS_ADMIN_EMAIL`, `ARGUS_ADMIN_PASSWORD`, `ARGUS_COOKIE_SECURE`.
- Image now builds with Go 1.24 and resolves modules via `go mod tidy` at build time.

### Not yet (upcoming slices)
- Role enforcement on write actions, user-management UI, TOTP MFA, WebAuthn passkeys,
  self-service password reset, and the probe enrollment/PKI backend.

---

## [0.0.1] - 2026-08-11

**Initial release.** Establishes the two foundations of the system: a validated Zabbix-based
collection layer, and the first "walking skeleton" of **Argus** (the custom web app), packaged
for automated delivery to GitHub Container Registry.

### Added - Argus application (`argus/`)
- **Go backend** that serves an embedded React single-page app from a single binary/container.
- **Health endpoints:** `GET /healthz` (liveness) and `GET /api/health`, which reports backend
  status and whether the Zabbix JSON-RPC API is reachable (with its version).
- **Zabbix API client** (JSON-RPC) with an unauthenticated `apiinfo.version` connectivity check.
- **Environment-based configuration** (`ARGUS_LISTEN`, `ARGUS_ZABBIX_API_URL`, `ARGUS_DATA_DIR`)
  so the container is configured entirely via `docker run`.
- **React + Vite frontend** showing live backend and Zabbix status, with a dark theme.

### Added - delivery & CI
- **Multi-stage Dockerfile:** build frontend → build Go binary (frontend embedded) → minimal
  distroless runtime image.
- **GitHub Actions** workflow that builds and pushes `ghcr.io/<owner>/argus` (`:latest` on the
  default branch, `:vX.Y.Z` on tags) and auto-publishes a GitHub Release for each tag.

### Added - deployment kit (`deploy/`)
- `setup-core.sh` - installs Zabbix 7.0 + PostgreSQL 17 + TimescaleDB on Debian 13, including
  auto-pinning TimescaleDB to a Zabbix-supported 2.28.x.
- `gen-certs.sh` - one shared CA + unique per-site mutual-TLS client certs; supports adding a
  new site with a single command.
- `zabbix_server.conf.snippet` - mutual-TLS config, tuning, and the TimescaleDB compatibility flag.
- `run-probe.sh` and an **unRAID template** for deploying an active proxy (single container,
  mTLS, 7-day offline buffer).
- `PHASE0-CHECKLIST.md` (command-by-command runbook) and `README.md` (including a documented
  TimescaleDB version-regression fix).

### Added - documentation
- `docs/DESIGN.md` - the full system design (architecture, device classes, thresholds, state
  model, notifications, auth, roadmap).
- Top-level `README.md` describing the repository layout and status.

### Project milestone (infrastructure validated outside the repo)
- Zabbix 7.0 core live on Debian 13 (`10.0.0.10`) with PostgreSQL 17 + TimescaleDB 2.28.3.
- The **site1** active proxy is online over mutual TLS; live ICMP data flows through it; the
  7-day offline buffer was verified by backfill after a simulated outage.

### Notes
- TimescaleDB is pinned to 2.28.x because Zabbix 7.0 rejects 2.29+.
- This is a **walking skeleton**: authentication, the PKI/enrollment backend, dashboards,
  auto-discovery, and notifications are not yet implemented - they arrive in later releases.

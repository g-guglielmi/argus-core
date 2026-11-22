# Backups and restore

The core VM backs itself up. One archive holds everything a new core needs to take over from the
old one, and one command puts it back. This guide covers what is in an archive, how to set backups up
and export them off the VM, and how to restore after a disaster.

- [What a backup holds](#what-a-backup-holds)
- [Setting up](#setting-up)
- [Export targets](#export-targets)
- [Checking your backups](#checking-your-backups)
- [Testing a restore](#testing-a-restore)
- [Restoring after a disaster](#restoring-after-a-disaster)
- [Restoring one part](#restoring-one-part)
- [Restoring by hand](#restoring-by-hand)
- [Troubleshooting](#troubleshooting)

## What a backup holds

| Part | What | Why it matters |
|---|---|---|
| `argus.db.gz` | Argus's database: users, two-factor and passkeys, notification channels, status pages, maintenance windows, thresholds, discovery, the incident log, Settings | Everything you set up in Argus |
| `zabbix.dump` | The Zabbix database (`pg_dump`), with its metric history or only its settings | Hosts, templates, triggers, the Argus service account and its API token, the probes and their TLS pins, secret macros (SSH and SNMP passwords), and the charts' data |
| `postgres-globals.sql` | The PostgreSQL roles | The `zabbix` role and its password |
| `files.tar.gz` | `/etc/argus-core` (the env files with the at-rest key and the Zabbix API token), `/docker/argus/pki` (**the CA the probes trust**, with its key), `/etc/zabbix` (server and frontend config, TLS certificates), the nginx TLS front, the collectors' SSH keys and XCP-NG pins, the external scripts, the Argus host units and scripts, and any other folder the Argus container mounts besides its database and update folder | Without the CA every probe would have to be enrolled again; without the at-rest key Argus can't read its own secrets |
| `network.json` | The core's address and prefix, gateway, DNS servers and search domains, static routes, hostname, whether the address was fixed or from DHCP, and a copy of the network settings files for reference | `argus-restore network` gives a new core the same address and hostname, so probes and the web certificate find it where they expect |
| `containers.json` | How the Argus and updater containers ran: image, environment (with the secret key and the Zabbix API token), folders, ports, network | On a core installed by hand these settings live nowhere else; `argus-restore containers` prints them back as `docker run` commands |
| `manifest.json` | When, where and from which versions the archive was made, with a checksum for each part | Checked before anything is restored |

An archive holds **every secret of the core**: the CA's private key, the at-rest key, the Zabbix API
token and database passwords. Treat it like the core itself.

Archives are named `argus-backup-<host>-<UTC time>.tar`, or `.tar.gpg` when encrypted, and kept in
`/var/backups/argus` on the VM (readable by root only). Each run keeps the newest ones and removes
the rest.

## Setting up

Everything is in **Settings, Backups** (admin):

- **Daily backup**: on or off, and when (the Argus timezone). Turning it on runs the first backup within
  15 minutes; after that one runs every day at the time you pick. A failed one is tried again an hour
  later. **Back up now** runs one at once.
- **Keep**: how many archives to keep, on the VM and on the export target (1 to 90, default 7).
- **Metric history**: included by default. Without it an archive holds only the Zabbix settings
  (hosts, templates, triggers, users): much smaller and faster, but the charts start empty after a
  restore.
- **Encryption passphrase**: at least 12 characters. With it set, every archive is encrypted (gpg,
  AES-256), and exporting needs one. **Store it in your password manager now**: Argus never shows it
  again, and without it no archive can be opened, not even by us. Changing it later only affects new
  archives. An archive holds Argus's database together with the key that opens its stored secrets
  (a restore needs both), so the passphrase is the only thing protecting them once an archive leaves
  the VM; without a passphrase the copies on the VM itself are plain, readable by root only.
- **Export to**: where a copy of each archive goes (below). Only encrypted archives leave the VM.

The status line shows the last backup (when, how big, how long), how many archives are on the VM and
the free space there, the last export and the next backup. When a backup fails, the export fails, or
no backup has succeeded for 36 hours, Argus sends a system notice to the channels that take them,
and an all-clear once a backup succeeds again
(**Notifications**, "System notices").

**On an existing core.** The core VM image has the backup tools from `core-vm/v0.1.3` on. On a core
installed before that (or by hand), install them once, as root on the core:

```
cd /tmp && curl -fsSL https://github.com/g-guglielmi/argus-core/archive/refs/tags/vX.Y.Z.tar.gz | tar -xz
cd argus-core-X.Y.Z/deploy/core/host && sudo ./install-backup.sh
```

(`vX.Y.Z` is the Argus release the core runs; running `setup-core-patching.sh` from a checkout does
the same, and on a core set up that way the argus-updater keeps the tools current afterwards.) The tools report to the folder Argus shares with the host as its update dir, and the
installer finds it from the running Argus container; `ARGUS_STATE_DIR=<folder>` names it instead.
Settings, Backups shows "The core host hasn't reported yet" until the first report arrives (within 15
minutes, or at once after `sudo systemctl start argus-backup.service`).

## Export targets

Each run copies the new archive to the target and keeps this core's newest ones there (as many as
**Keep**). It never touches other files, or another core's archives in the same folder. **Check the
target** writes, lists and deletes a small test file, so you know the settings work before the first
backup needs them. When the last export failed and the check works, the same run copies the archives
the target is missing, so the failure doesn't wait for the next backup to clear.

### SMB share (Windows, a NAS)

- **Share**: `//server/share`, like `//nas.example.lan/backups`.
- **Folder**: a folder inside the share, created when missing (`argus`).
- **User**, **Password**, **Domain**: an account that may write there (empty user = guest). The
  password is handed to the mount through a file only root can read, never on a command line.
- **SMB version**: leave on Negotiate unless the server needs a specific one.

Give the account a folder of its own, so a compromised core can't touch anything else.

### NFS export

- **Export**: `server:/path`, like `nas.example.lan:/volume1/backups`.
- **Folder**: a folder inside the export (`argus`).
- **Mount options**: empty uses `soft,timeo=150,retrans=3`, so a server that goes away fails the export
  instead of hanging it.

The export must let the core's address write: allow its IP in the export's client list, and mind
root squashing (the backup runs as root: either allow root, or squash to a user that owns the folder).
The NFS client is installed the first time an NFS target is used; its `rpcbind` service is switched
off again unless the mount options ask for NFS version 2 or 3.

### rsync over SSH

- **Target**: `user@server:/path`, like `backup@nas.example.lan:/volume1/argus`.
- **SSH port**: 22 unless the server uses another.
- **Key for the target**: Argus creates an ed25519 key pair when you save. Add the public line it shows
  to `~/.ssh/authorized_keys` of that user on the server. **New key** replaces it (put the new line in
  place of the old one).

The server's host key is learned on the first connection (`/etc/argus-core/backup_known_hosts`); if
the server is rebuilt, remove its line there.

To keep the key from doing anything but backups, restrict it on the server to rsync into that one
folder, with the `rrsync` helper that ships with rsync:

```
command="rrsync /volume1/argus",restrict ssh-ed25519 AAAA... argus-backup
```

With `rrsync` the paths are relative to that folder, so set the target to `backup@nas.example.lan:/`.

### S3 bucket (or compatible)

- **Endpoint**: the service's address, like `https://s3.eu-central-1.amazonaws.com` (AWS), or
  Backblaze B2, Wasabi, Cloudflare R2, MinIO.
- **Bucket**: it must exist already. **Region**: when the service wants one.
- **Folder**: the key prefix inside the bucket (`core`).
- **Access key** and **Secret key**: a key limited to this bucket (list, read, write, delete) is all it
  needs.

Turn on the bucket's **versioning** or **object lock** if the service has it: then even a compromised
core can't destroy the archives already there (a delete only hides the newest version).

## Checking your backups

- The status line in Settings, Backups, and the notices when something fails. **Recent activity**
  under it lists the last runs: each backup and target check, what started it (the daily time, or who
  pressed the button), how it went and, when it failed, at which step (mounting the share, writing
  the test file, copying an archive) and why. While a run is under way the status says so.
- On the VM, `sudo argus-backup log` prints the same list, `sudo argus-backup status` the whole
  status, and `ls -l /var/backups/argus` lists the archives. The detailed log of every run is in the
  journal: `sudo journalctl -u argus-backup -n 50` (it needs sudo). Each run starts by saying what
  it does and why, names the target, and logs each step.
- `sudo argus-restore inspect /var/backups/argus/<archive>` opens an archive (asking for the
  passphrase when it is encrypted), checks every part against its checksum, and shows when, where and
  from which versions it was made. It changes nothing.
- Now and then, **restore one on a spare VM** as a test (the next section). It is the only way to
  be sure.

## Testing a restore

A restored core is a complete copy of the old one: the same notification channels, heartbeat URL,
scheduled reports, syslog server and backup target. After a disaster that is the point. Next to a
core that still runs it is not: the copy hears none of the probes (they keep talking to the real
core), so it would report everything down through your real channels, and export its own archives
to your target. A **test restore** restores everything the same way and keeps the copy quiet.

1. **Get a spare core VM** from the appliance image (the first line of
   [the latest release](https://github.com/g-guglielmi/argus-core/releases/latest) links it), boot it on DHCP and go through the first-boot
   page with any values. On its **Updates** page, bring Argus and Host tools to the release your
   core runs (the restore tool comes with the host tools), and Zabbix to the same version.
2. **Copy an archive** into your home folder on the spare VM (`/home/argus` for the default user;
   `scp`, WinSCP or any SFTP client, signed in as that user) and check it:
   `sudo argus-restore inspect ~/<archive>`. The restore tool reads an archive from wherever it is.
   Not into `/var/backups/argus`: that folder is root's alone, so a copy over SSH answers
   "Permission denied".
3. **Restore it as a test**: `sudo argus-restore restore ~/<archive> --test`,
   and type `RESTORE`. The VM needs the internet for this when its TimescaleDB differs from the
   backup's (the restore installs that version first).
4. **Look**: sign in at the spare VM's address with your own user (the browser warns: the
   certificate names the real core, and a passkey is tied to the real address, so use the password
   and the second factor). Every page carries a **Test restore** band. Open a few hosts and their
   charts: the data goes up to the moment of the backup. Probes read offline, which is right here.
5. **Throw it away**: delete the VM and its disk, and every copy of the archive made on the way. An
   archive holds every secret of the core.

What a test restore does not do, until it ends:

- it sends no alert, reminder, recovery or system notice, on any channel, shared or personal, and
  queues none for later; a channel's **Send test** says it is a test restore;
- it mails no scheduled report and no password link, and sends nothing to syslog;
- it holds the heartbeat (a copy must not vouch for the real core);
- it exports no backup and prunes nothing at the target: `argus-backup` gets a plan without one;
- `argus-restore network` refuses: a test restore never takes the real core's address.

It still reads what a core reads (its own Zabbix, the public status pages, the registry for new
versions), and the collectors on the VM still log in to the devices they watch.

`--test` refuses to start on a machine whose Argus does not know test restores yet (it would ignore
the mark and come up as a full core), and stops the copy again if the Argus that came back with the restore
does not confirm it is a test restore.

**If the real core is lost while a test restore is up**, the copy can take over: `sudo argus-restore
live`, type `GO LIVE`, and it is a real core within seconds, without a restart. Then take over the
address (step 5 below). Its data is as old as its backup.

## Restoring after a disaster

The old core is gone; a new one takes over, with the same data, the same probes and the same links.

1. **Get a new core VM** from the appliance image: the first line of
   [the latest release](https://github.com/g-guglielmi/argus-core/releases/latest) links the current
   one. Keep a copy of the image next to your backups, so a restore does not depend on a download.
   (Use the same `core-vm` release as the old one, or a
   newer one with the same Zabbix and PostgreSQL lines: `argus-restore` checks. TimescaleDB may
   differ: the restore installs the version the backup was made on by itself).
   On its **Updates** page, bring Argus and Host tools to the current release before going on:
   the restore tool comes with the host tools, and the one on an older image is older too.
   Boot it on DHCP and go through the first-boot page with any values; the restore replaces them.
   It takes over the old core's address and hostname in step 5.
   (A core installed by hand: run `setup-core.sh` with the same versions, copy the archive over (step
   2), and create the Argus containers from `sudo argus-restore containers ~/<archive>`,
   which prints the `docker run` commands they ran with, secret key and Zabbix API token included:
   keep that output private. The restore then puts their folders back where they were.)
2. **Copy the archive** into your home folder on the new VM (`/home/argus` for the default user):
   `scp` it from wherever you keep it (WinSCP or any SFTP client does the same), mount the share,
   or fetch it from the bucket (`rclone copy`). The restore tool reads an archive from wherever it
   is. `/var/backups/argus`, where the core keeps its own archives, is root's alone: a copy into
   it over SSH answers "Permission denied".
3. **Check it**: `sudo argus-restore inspect ~/<archive>`. It asks for the passphrase
   and warns when a version differs from the one the archive was made on.
4. **Restore**: `sudo argus-restore restore ~/<archive>`, and type `RESTORE` when it
   asks. The old core must be off or gone by now: this one comes up as its full copy (for a test
   next to a running core, see [Testing a restore](#testing-a-restore)). It stops Argus, the updater and Zabbix, puts back the configuration, keys and certificates,
   recreates the Zabbix database from the dump (with TimescaleDB's pre- and post-restore steps) and
   the Argus database, and starts everything again. With metric history this takes a while.
5. **Take over the address**: `sudo argus-restore network ~/<archive>`, best from the
   hypervisor's console (an SSH session drops when the address changes). It shows the old core's
   address, gateway, DNS and hostname next to this machine's, and on a core VM (systemd-networkd) sets
   them on this machine's network card, matched by its MAC, after you type `TAKE OVER`. Probes dial the
   core's address on port 10051, and the restored web certificate names that address and hostname.
   An address the old core had from DHCP is better moved: the tool prints this machine's MAC to put on
   the DHCP reservation (`--static` sets it as fixed anyway). On a machine whose network another tool
   manages (ifupdown, NetworkManager) it prints the settings to enter there and changes nothing.
   `--keep-hostname` leaves the hostname alone.
6. **Check**: sign in to Argus as before (same users, same two-factor). Within a few minutes the probes
   are online again: they trust the CA that came back. Look at a few hosts' charts, then at Settings,
   Backups: the plan came back too, and the next backup runs from the new VM.

**If the address changed** (the old one can't be taken over). Point the Argus FQDN at the new IP. Probes check in by that address and
learn the core host from Argus (Settings, Probes, **Probe core host**): set it to the new IP, and each
probe follows at its next restart. The restored web certificate still names the old hostname and IP,
so browsers warn until you replace it (`/etc/nginx/argus`).

PostgreSQL's own tuning stays as the new VM has it, since it is sized for that machine's memory. The
old one is in the archive for reference (`files.tar.gz`, `etc/postgresql`).

## Restoring one part

`--only` puts back a single part and leaves the rest as it is:

- `sudo argus-restore restore <archive> --only argus`: the Argus database only (Argus is stopped
  meanwhile). For an Argus mistake, like a deleted status page or channel. Changes made in Argus since
  the archive are lost.
- `sudo argus-restore restore <archive> --only zabbix`: the Zabbix database only (zabbix-server is
  stopped meanwhile). Metric data since the archive is lost.
- `sudo argus-restore restore <archive> --only files`: the configuration, keys and certificates only.

`--yes` skips the confirmation, `--passphrase-file` reads the passphrase from a file, and `--force`
restores even when the versions differ (for when you know why). `--test` goes with a whole restore
only, not with `--only`; putting one part back on a machine that is a test restore leaves it one,
and a whole restore without `--test` ends it.

## Restoring by hand

When `argus-restore` isn't at hand, these are its steps (as root):

```
gpg -d argus-backup-<host>-<time>.tar.gpg > archive.tar      # asks for the passphrase
mkdir restore && tar -xf archive.tar -C restore && cd restore
systemctl stop argus-updater argus-core zabbix-server
tar -xzpf files.tar.gz -C / --exclude=etc/postgresql
runuser -u postgres -- psql -f postgres-globals.sql           # "already exists" errors are fine
runuser -u postgres -- dropdb --if-exists zabbix
runuser -u postgres -- createdb -O zabbix zabbix
runuser -u postgres -- psql -d zabbix -c "CREATE EXTENSION IF NOT EXISTS timescaledb"
runuser -u postgres -- psql -d zabbix -c "SELECT timescaledb_pre_restore()"
runuser -u postgres -- pg_restore -d zabbix zabbix.dump       # never -j: it breaks TimescaleDB's catalogs
runuser -u postgres -- psql -d zabbix -c "SELECT timescaledb_post_restore()"
gunzip -c argus.db.gz > /docker/argus/argus.db
rm -f /docker/argus/argus.db-wal /docker/argus/argus.db-shm
chown -R 65532:65532 /docker/argus
chown -R root:65532 /docker/argus/pki && chmod 750 /docker/argus/pki && chmod 440 /docker/argus/pki/ca.key
chown -R zabbix:zabbix /etc/zabbix/certs
systemctl daemon-reload && systemctl start zabbix-server argus-core argus-updater
```

## Troubleshooting

- **"The core host hasn't reported yet" although the tools are installed**: they report to another
  folder than the one Argus reads. The installer takes the folder the Argus container mounts as its
  update folder, or `/docker/argus-update` when Argus wasn't running then
  ([folder-layout.md](folder-layout.md)). See which folder the container mounts:
  `docker inspect argus --format '{{range .Mounts}}{{.Source}} -> {{.Destination}}{{println}}{{end}}'`,
  run the installer again with it, `sudo ARGUS_STATE_DIR=<that folder> ./install-backup.sh`, and then
  `sudo systemctl start argus-backup.service`. `journalctl -u argus-backup -n 20` says so when the
  folder it was given doesn't exist.

- **"Not restoring over a different version"**: the new VM runs another Zabbix or PostgreSQL line
  than the old one, or a TimescaleDB version the restore could not put in place. A TimescaleDB dump
  restores only onto the extension version it was made on. The restore sees to that itself: when
  this machine is on another version (the core moved to the next TimescaleDB line after the backup,
  or the image is newer), it installs the backup's version first, holds it and pins its line, and
  `argus-restore inspect` says so beforehand. Only when the package repository no longer offers
  that version does it stop here; install it by hand then (`apt-get install
  timescaledb-2-postgresql-17=<version> timescaledb-2-loader-postgresql-17=<version>`, then
  `apt-mark hold` both) and run the restore again.
- **Going back from a TimescaleDB line move**: restore a backup from before the move, on the same
  core, with `--only zabbix` when only the database should go back. The restore puts the older
  TimescaleDB line in place first; the Updates page then offers the newer line again.
- **"No test restore on this machine yet"**: the Argus on the spare VM does not know test restores
  yet, or its   container is not running, so nothing was touched. Update Argus and Host tools on its Updates page
  and run the restore again.
- **"the Argus that started after the restore does not confirm it is a test restore"**: the old core was
  pinned to an older Argus version, and its settings came back with the restore. The copy was
  stopped before it could alert. Everything is restored: set the version in
  `/etc/argus-core/image.env` to the current release (or remove the pin) and
  `sudo systemctl restart argus-core`; the mark is still in place.
- **"gpg could not decrypt the archive"**: the passphrase is wrong. It is the one set when that archive
  was made.
- **"the checksum does not match"**: the archive was damaged on its way (a partial copy). Copy it again,
  or take an older one.
- **The export says it "can't reach" the server on TCP 445**: SMB needs TCP 445 from the core to the
  server and nothing else (not 137 to 139). A firewall between the two networks usually drops it; allow
  that one port from the core's address. "can't resolve" means the core's DNS doesn't know the name:
  use the server's IP address, or a DNS name the core resolves.
- **The export fails with "Permission denied"**: the account can't write the folder (SMB), the core's
  address isn't allowed or root is squashed (NFS), or the public key isn't in `authorized_keys`
  (rsync). **Check the target** repeats the test at once.
- **"Permission denied" when you copy an archive into `/var/backups/argus`** (WinSCP: "error
  listing directory"): the folder is root's alone and you sign in as the VM's user. Copy the
  archive into your home folder and give the restore tool that path:
  `sudo argus-restore inspect ~/<archive>`. Afterwards, delete the copy from the home folder.
- **"the disk holding /var/backups/argus is full"**: lower **Keep**, turn **Metric history** off, or
  give the VM a bigger disk. The status line shows the free space.
- **pg_restore reported errors**: a TimescaleDB dump can print a few harmless ones. The log is kept in
  `/var/backups/argus`; if Zabbix starts and the charts show data, the restore worked.
- **"table(s) of the Zabbix database did not load"**: `pg_restore` could not load the rows of a
  table, so the database is not what was backed up; the restore stops there and starts nothing.
  The kept log names the tables and why. (The restore runs `pg_restore` as one job on purpose: with
  `-j`, TimescaleDB's catalog tables load out of order and the hypertables come back broken.)

# Backups and restore

The core VM backs itself up. One archive holds everything a new core needs to take over from the
old one, and one command puts it back. This guide covers what is in an archive, how to set backups up
and export them off the VM, and how to restore after a disaster.

- [What a backup holds](#what-a-backup-holds)
- [Setting up](#setting-up)
- [Export targets](#export-targets)
- [Checking your backups](#checking-your-backups)
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
  archives.
- **Export to**: where a copy of each archive goes (below). Only encrypted archives leave the VM.

The status line shows the last backup (when, how big, how long), how many archives are on the VM and
the free space there, the last export and the next backup. When a backup fails, the export fails, or
no backup has succeeded for 36 hours, Argus sends a system notice to the channels that take them
(**Notifications**, "System notices").

**On an existing core.** The core VM image has the backup tools from `core-vm/v0.1.3` on. On a core
installed before that (or by hand), install them once, as root on the core:

```
cd /tmp && curl -fsSL https://github.com/g-guglielmi/argus-core/archive/refs/tags/vX.Y.Z.tar.gz | tar -xz
cd argus-core-X.Y.Z/deploy/core/host && sudo ./install-backup.sh
```

(`vX.Y.Z` is the Argus release the core runs; re-running `setup-core-patching.sh` from a checkout does
the same.) The tools report to the folder Argus shares with the host as its update dir, and the
installer finds it from the running Argus container; `ARGUS_STATE_DIR=<folder>` names it instead.
Settings, Backups shows "The core host hasn't reported yet" until the first report arrives (within 15
minutes, or at once after `sudo systemctl start argus-backup.service`).

## Export targets

Each run copies the new archive to the target and keeps this core's newest ones there (as many as
**Keep**). It never touches other files, or another core's archives in the same folder. **Check the
target** writes, lists and deletes a small test file, so you know the settings work before the first
backup needs them.

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

- The status line in Settings, Backups, and the notices when something fails.
- On the VM, `sudo argus-backup status` prints the same status, and `ls -l /var/backups/argus`
  lists the archives.
- `sudo argus-restore inspect /var/backups/argus/<archive>` opens an archive (asking for the
  passphrase when it is encrypted), checks every part against its checksum, and shows when, where and
  from which versions it was made. It changes nothing.
- Now and then, **restore one on a spare VM** (the steps below). It is the only way to be sure.

## Restoring after a disaster

The old core is gone; a new one takes over, with the same data, the same probes and the same links.

1. **Get a new core VM** from the appliance image (the same `core-vm` release as the old one, or a
   newer one with the same Zabbix, PostgreSQL and TimescaleDB versions: `argus-restore` checks).
   Give it **the old core's IP address** if you can: probes dial it on port 10051, and the web
   certificate names it. Go through the first-boot page with any values; the restore replaces them.
   (A core installed by hand: run `setup-core.sh` with the same versions, copy the archive over (step
   2), and create the Argus containers from `sudo argus-restore containers /var/backups/argus/<archive>`,
   which prints the `docker run` commands they ran with, secret key and Zabbix API token included:
   keep that output private. The restore then puts their folders back where they were.)
2. **Copy the archive** into `/var/backups/argus` on the new VM: `scp` it from wherever you keep it,
   mount the share, or fetch it from the bucket (`rclone copy`).
3. **Check it**: `sudo argus-restore inspect /var/backups/argus/<archive>`. It asks for the passphrase
   and warns when a version differs from the one the archive was made on.
4. **Restore**: `sudo argus-restore restore /var/backups/argus/<archive>`, and type `RESTORE` when it
   asks. It stops Argus, the updater and Zabbix, puts back the configuration, keys and certificates,
   recreates the Zabbix database from the dump (with TimescaleDB's pre- and post-restore steps) and
   the Argus database, and starts everything again. With metric history this takes a while.
5. **Check**: sign in to Argus as before (same users, same two-factor). Within a few minutes the probes
   are online again: they trust the CA that came back. Look at a few hosts' charts, then at Settings,
   Backups: the plan came back too, and the next backup runs from the new VM.

**If the address changed.** Point the Argus FQDN at the new IP. Probes check in by that address and
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
restores even when the versions differ (for when you know why).

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
runuser -u postgres -- pg_restore -j 4 -d zabbix zabbix.dump
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

- **"Not restoring over a different version"**: the new VM runs another Zabbix, PostgreSQL or
  TimescaleDB release than the old one. A TimescaleDB dump restores only onto the same extension
  version; install that one (`apt-get install timescaledb-2-postgresql-17=<version>
  timescaledb-2-loader-postgresql-17=<version>`, then `apt-mark hold` both) and run the restore again.
- **"gpg could not decrypt the archive"**: the passphrase is wrong. It is the one set when that archive
  was made.
- **"the checksum does not match"**: the archive was damaged on its way (a partial copy). Copy it again,
  or take an older one.
- **The export fails with "Permission denied"**: the account can't write the folder (SMB), the core's
  address isn't allowed or root is squashed (NFS), or the public key isn't in `authorized_keys`
  (rsync). **Check the target** repeats the test at once.
- **"the disk holding /var/backups/argus is full"**: lower **Keep**, turn **Metric history** off, or
  give the VM a bigger disk. The status line shows the free space.
- **pg_restore reported errors**: a TimescaleDB dump can print a few harmless ones. The log is kept in
  `/var/backups/argus`; if Zabbix starts and the charts show data, the restore worked.

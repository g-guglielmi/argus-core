# Container folders

Every Argus container keeps its files on the host in **`/docker/<container name>`**: on the core VM,
on the probe VM, and in every `docker run` example in these docs. One folder per container, named
after it, so `ls /docker` shows what runs on the machine, and copying or backing up a container is a
folder copy.

## Core

| Host folder | Mounted in | Holds |
|---|---|---|
| `/docker/argus` | `argus` as `/data` | Argus's database (`argus.db`), and its key file when `ARGUS_SECRET_KEY` isn't set |
| `/docker/argus/pki` | `argus` as `/ca` (read-only) and over `/data/pki` (read-only) | The CA the probes trust: `ca.crt`, and `ca.key`, which Argus signs probe certificates with |
| `/docker/argus-update` | `argus` and `argus-updater` as `/update` | The self-update channel the two share, and what the host reports into it: OS updates, the backup status |

- `/docker/argus` belongs to the container's user (uid 65532), which writes the database.
- `/docker/argus/pki` belongs to root and is readable by the container's group only: folder `0750`,
  `ca.key` `0440`, `ca.crt` `0444`. Argus reads the CA as `/ca`. Since the folder sits inside the
  writable `/data`, it is mounted read-only over `/data/pki` too: a mount point can't be renamed or
  replaced, so Argus can't swap the CA.
- `argus-updater` keeps no files of its own. It holds the Docker socket and shares
  `/docker/argus-update`, named after the channel it serves.

Software on the host rather than in a container keeps its usual places: Zabbix in `/etc/zabbix` (its
TLS certificates in `/etc/zabbix/certs`), PostgreSQL in its own directories, the core VM's container
settings in `/etc/argus-core` (`argus.env`, `image.env`, root only), backups in `/var/backups/argus`
([backup-and-restore.md](backup-and-restore.md)).

The core's `docker run`, with these folders (the rest of the options are in the
[README](../README.md#2b-run-the-container)):

```
docker run -d --name argus --restart unless-stopped \
  -v /docker/argus:/data \
  -v /docker/argus/pki:/data/pki:ro \
  -v /docker/argus/pki:/ca:ro \
  -v /docker/argus-update:/update \
  -e ARGUS_UPDATE_DIR=/update -e ARGUS_CA_CERT_FILE=/ca/ca.crt -e ARGUS_CA_KEY_FILE=/ca/ca.key \
  ...
docker run -d --name argus-updater --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /docker/argus-update:/update \
  -e ARGUS_CORE_CONTAINER=argus \
  ghcr.io/g-guglielmi/argus-updater:latest
```

## Probe

| Host folder | Mounted in | Holds |
|---|---|---|
| `/docker/argus-probe` | `argus-probe` as `/var/lib/zabbix`, `argus-updater` as `/probe` (read-only) | The proxy's database and its 7-day buffer, its TLS certificates (`ssl/`), the enrollment state (`enroll/`) |
| `/docker/argus-probe/snmptraps` | `argus-probe` as `/var/lib/zabbix/snmptraps` | The SNMP traps it receives |

On the probe VM the enrollment settings are in `/etc/argus-probe/probe.env` (root only). A probe run
with `docker run` follows the same rule under its own name: the **Add probe** wizard's command uses
`/docker/<container name>`, so a probe called `argus-proxy-site1` keeps its files in
`/docker/argus-proxy-site1`.

## Unraid

The Unraid templates follow Unraid's own convention, `/mnt/user/appdata/<container name>`, with the
same shape: `/mnt/user/appdata/argus` (the CA in `pki/` inside it), `/mnt/user/appdata/argus-update`,
and one folder per probe.

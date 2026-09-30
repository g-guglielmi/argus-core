# Linux (SSH, agentless)

The **Linux (SSH, agentless)** class is the fallback for a Linux box you can only reach over SSH -
no SNMP daemon, no Zabbix agent. Add it with **Add device → Linux (SSH, agentless)**. Nothing is
installed on the target: the proxy/core runs a small collector (`argus_linux_ssh.py`, an external
check) that opens **one** SSH session per poll, reads `/proc`, `df` and `/proc/net/dev`, and returns
CPU, memory, load, uptime, per-filesystem usage and per-interface traffic in a single login. The
item keys match the native agent/SNMP keys, so the host reads exactly like an SNMP- or agent-
monitored Linux box - it just gets its numbers a different way.

Prefer SNMP or a Zabbix agent when you can run one; this class exists for the machines where you
can't but you do have SSH.

## Adding a host

- **Address**: the Linux box's own IP or DNS name (Base Ping and the collector both run against it).
- **SSH user** (`{$SSH.USER}`, default `root`): any account that can read `/proc`, run `df` and read
  `/proc/net/dev` - a plain **read-only** login is enough. The collector never writes.
- **SSH port** (`{$SSH.PORT}`, default 22).
- **Authentication** (`{$SSH.AUTH}`): **`key`** (recommended) or **`password`**.
- **Private key path** (`{$SSH.KEYFILE}`, default `/var/lib/zabbix/ssh/argus_id`): for key auth, the
  path to the private key **on the proxy** (see below).
- **Services to watch** (`{$SSH.UNITS}`, optional): systemd units, comma or space separated
  (`nginx, jellyfin, docker`; a name without a suffix means `.service`). See below.
- **Containers to watch** (`{$SSH.CONTAINERS}`, optional): a regular expression of Docker container
  names (`^(jellyfin|immich.*)$`). See below; it needs `docker ps` rights.
- **SSH password** (`{$SSH.PASSWORD}`): for password auth, stored as a Zabbix **secret macro**. Zabbix
  can hand it to the collector only as a command-line argument; the collector wipes its own command
  line as soon as it has read it, and `ssh` gets it through the environment, so it shows in `ps` only
  during the collector's start-up. The proxy's own configuration database holds the macro as well, as
  for every secret macro.

The first connection to a host is trust-on-first-use (`accept-new`); a later change to the host key
is rejected.

## Key auth (recommended)

One monitoring key per proxy covers every SSH host that proxy monitors, so this is a one-time setup:

1. Generate a keypair (no passphrase - it's unattended):
   ```
   ssh-keygen -t ed25519 -N '' -C argus-monitor -f argus_id
   ```
2. Put the **public** half (`argus_id.pub`) in each target's `~/.ssh/authorized_keys` for the login
   you chose. To lock it down, prefix the line with `from="<proxy-ip>",no-pty,no-agent-forwarding,no-port-forwarding`.
3. Put the **private** half on the proxy at the path in **Private key path** and mount it into the
   `argus-probe` container there, read-only:
   ```
   -v /docker/argus-proxy-<site>/ssh/argus_id:/var/lib/zabbix/ssh/argus_id:ro
   ```
   On the core (no proxy), place it at that same path under the Zabbix user and `chmod 600` it.
4. Set **Authentication** to `key`.

## Password auth

Set **Authentication** to `password` and fill in the **SSH password**. The proxy image ships
`sshpass` for this. Password auth stores a working credential with shell access to the box, so key
auth is the better choice where you can use it; password auth is there for the hosts where you can't
place a key.

## What it monitors

- **CPU**: overall utilization (two `/proc/stat` samples a second apart) and the 1 / 5 / 15-minute
  load averages. Alerts: high CPU (`{$CPU.UTIL.WARN}` 80%, `{$CPU.UTIL.HIGH}` 95%).
- **Memory**: total, available, and utilization %. Alerts: high memory (`{$MEM.USED.WARN}` 85%,
  `{$MEM.USED.HIGH}` 95%).
- **Uptime**.
- **Filesystems** (LLD): total, used and used-% per real mount, with disk-space low/critical alerts
  (`{$DISK.PUSED.WARN}` 90%, `{$DISK.PUSED.HIGH}` 95%). Pseudo/temporary filesystems are dropped by
  the collector; trim further per host with `{$FS.NAME.SKIP}`.
- **Network** (LLD): in/out bit rate per physical interface. Loopback is dropped; virtual/container
  NICs are trimmed with `{$NET.IF.SKIP}`.

A filesystem or interface that disappears is disabled immediately and deleted after 7 days.

## Services and containers

The same SSH session can also report whether the services you care about are running.

- **systemd units** (`{$SSH.UNITS}`): one sensor per unit you list, under **Services**, reading
  **Running** or **Down**. Read with `systemctl show`, which any login may run: no extra rights. A
  unit that is failed, inactive, restarting (`activating (auto-restart)`) or missing reads Down, and
  the reason says which (`failed (failed), result exit-code`, `not found (no such unit on this
  host)`). The list is not a discovery: a unit the host doesn't have reads Down until you take it
  off the list (its sensor is deleted a day later).
- **Docker containers** (`{$SSH.CONTAINERS}`): one sensor per container whose name matches the
  expression (matched by the collector, among `docker ps -a`), under **Containers**. **Running**
  means up and, when it has a healthcheck, healthy; exited, restarting, paused, unhealthy or removed
  reads Down, with docker's status as the reason (`Exited (1) 2 hours ago`, `Up 5 minutes
  (unhealthy)`, `not listed by docker ps -a (removed?)`). A new container that matches appears on
  the next discovery; one that is removed reads Down until its sensor is deleted a day later.
- **Rights for containers.** `docker ps` talks to the Docker socket, and on a standard install only
  root and the `docker` group may. Membership of the `docker` group is **root-equivalent** on that
  host (anyone in it can start a privileged container), so give it to the monitoring login only if
  that trade is acceptable there, or leave containers off and watch the container's own service or
  port instead. Without the rights, the container sensors read "not supported" with the reason
  ("the SSH login may not run docker ps (permission denied on the Docker socket)") rather than Down.
- Each unit and container has an uptime (its chart shows the last checks and 30 days), and alerts on
  its own: **Service down: nginx**, **Container down: jellyfin** (High, after 3 checks). While the
  whole host is unreachable, its SSH down alert speaks for them.

## Troubleshooting

- **Start with the reason.** Hover (or tap) the sensor's value in Argus - `not supported`, or `Not reachable`
  on a down collector - to see why, in ssh's own words (`Permission denied (publickey)`,
  `Connection refused`, `Host key verification failed`); the alert carries the same text.
- **"SSH monitoring is unreachable"** - the collector could not complete a poll on the last 3
  checks: the host is down, SSH is refused/filtered on `{$SSH.PORT}`, or the login is wrong. For key
  auth, confirm the private key is mounted at `{$SSH.KEYFILE}` on the proxy and its public half is in
  the target's `authorized_keys`; for password auth, re-check `{$SSH.PASSWORD}`.
- **Host key changed** - if you rebuilt the target, its host key changed and `accept-new` now
  rejects it. Remove the stale entry from `/var/lib/zabbix/ssh/argus_known_hosts` on the proxy (or
  just delete that file - it re-learns on the next poll).
- **A mount or NIC is missing** - it may be matched by `{$FS.NAME.SKIP}` / `{$NET.IF.SKIP}`, or (for
  filesystems) it is a pseudo/tmpfs mount the collector filters out by design.
- **A service reads Down but it runs** - hover the reading for systemd's answer: a unit name without
  a suffix means `.service`, so a timer or a socket needs its full name (`backup.timer`).
- **Containers read "not supported"** - hover it: usually the login can't run `docker ps` (see
  "Rights for containers" above), or docker isn't installed on that host.
- **Works from your shell but not the proxy** - remember the collector connects **from the proxy**,
  not your workstation: the key/authorized_keys and any `from=` restriction must match the proxy's IP.

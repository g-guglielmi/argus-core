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
- **SSH password** (`{$SSH.PASSWORD}`): for password auth, stored as a Zabbix **secret macro**. It is
  handed to the collector through the environment, so it never appears in the command line or `ps`.

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

## Troubleshooting

- **"SSH monitoring is unreachable"** - the collector could not complete a poll on the last 3
  checks: the host is down, SSH is refused/filtered on `{$SSH.PORT}`, or the login is wrong. For key
  auth, confirm the private key is mounted at `{$SSH.KEYFILE}` on the proxy and its public half is in
  the target's `authorized_keys`; for password auth, re-check `{$SSH.PASSWORD}`.
- **Host key changed** - if you rebuilt the target, its host key changed and `accept-new` now
  rejects it. Remove the stale entry from `/var/lib/zabbix/ssh/argus_known_hosts` on the proxy (or
  just delete that file - it re-learns on the next poll).
- **A mount or NIC is missing** - it may be matched by `{$FS.NAME.SKIP}` / `{$NET.IF.SKIP}`, or (for
  filesystems) it is a pseudo/tmpfs mount the collector filters out by design.
- **Works from your shell but not the proxy** - remember the collector connects **from the proxy**,
  not your workstation: the key/authorized_keys and any `from=` restriction must match the proxy's IP.

# Linux (SNMP)

The **Linux (SNMP)** class monitors any Linux host that exposes SNMP (HOST-RESOURCES / UCD
MIBs): CPU utilization, memory, filesystems, network interfaces and uptime. It is the baseline SNMP
template that several other classes (unRAID, Windows) build on, so its readings curate the same way.

## 1. Enable SNMP on the host

Install and configure `snmpd` (Debian/Ubuntu: `snmpd`; RHEL/Fedora: `net-snmp`):

1. Install the daemon:
   ```bash
   sudo apt-get install -y snmpd     # Debian/Ubuntu
   # or: sudo dnf install -y net-snmp
   ```
2. In `/etc/snmp/snmpd.conf`, expose a read-only community to the site proxy and bind on the LAN (the
   stock config often listens only on `127.0.0.1` - change `agentaddress` to the host's LAN IP or
   `udp:161`):
   ```
   rocommunity <your-community> <proxy-ip>
   agentaddress udp:161
   ```
3. Restart and confirm the firewall allows **UDP 161** from the proxy:
   ```bash
   sudo systemctl restart snmpd
   ```

## 2. Add the host in Argus

**Add device → Linux (SNMP)**, give the IP or DNS name. SNMP credentials inherit the site
proxy's SNMP default (set in the Probes tab) unless you override them on the host. CPU, memory,
filesystems, NICs and uptime start reporting within a minute or two.

## What it monitors

- **CPU** utilization (and per-core where the MIB exposes it).
- **Memory** used / available / total.
- **Filesystems** (discovered) with used-% alerts. Tune `{$FS.NAME.SKIP}` on the host to exclude
  mounts (pseudo-filesystems are already filtered).
- **Network interfaces** (discovered) traffic in/out. `{$NET.IF.SKIP}` excludes NICs by name.
- **Uptime**.

## Troubleshooting

- **Nothing over SNMP.** From the proxy, test `snmpwalk -v2c -c <community> <host> system`. If it
  times out, re-check the community, the `agentaddress` bind, and that UDP 161 is open from the proxy.
- **A filesystem or NIC is missing/noisy.** Adjust `{$FS.NAME.SKIP}` / `{$NET.IF.SKIP}` on the host in
  host settings, then use **Discover now**.

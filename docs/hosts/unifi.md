# UniFi (Switch, Gateway, AP, OS Console)

The UniFi classes monitor UniFi devices **through the UniFi Network controller's Integration API**, not
by polling the device directly. Base Ping still runs against the device's own IP, but all the rich
metrics come from the controller, addressed by the macros below. This works for both a self-hosted
Network controller and a cloud/UniFi OS gateway that hosts the controller.

There are four classes, same access pattern, different metrics:

| Class | Monitors beyond ping |
|---|---|
| **UniFi Switch** | uptime, CPU/memory, per-port traffic + PoE, client count |
| **UniFi Gateway** | the switch set, plus WAN up/down and WAN throughput |
| **UniFi Access Point** | per-radio traffic, clients, channel utilization |
| **UniFi OS Console** | the console host's own CPU/memory/temp/disk, adoption count, version |

## 1. Create an Integration API key

In the UniFi Network application: **Settings → Control Plane → Integrations** (on some versions
**Settings → System → Integrations**), create an **API key**. Copy it - it is shown once. This key is
read at the controller, so one key covers every UniFi device on that controller.

## 2. Find the device MAC and controller URL

- **Controller URL** - the base URL of the Network controller, including its port, e.g.
  `https://unifi.example.lan:11443` (self-hosted) or the cloud console URL. For a gateway that hosts
  the controller, this usually points back at the gateway itself.
- **Device MAC** - the switch / gateway / AP / console MAC, from its device page in the controller
  (format `aa:bb:cc:dd:ee:ff`).
- **Site name** - the controller site the device belongs to (default is `default`).

## 3. Add the device in Argus

**Add device →** the matching UniFi class, then fill in:

| Field | Macro | Notes |
|---|---|---|
| Controller URL | `{$UNIFI.URL}` | required, include the port |
| API key | `{$UNIFI.KEY}` | required, stored as a secret |
| Device MAC | `{$UNIFI.MAC}` | required, the switch/gateway/AP/console MAC |
| Site name | `{$UNIFI.SITE}` | optional, defaults to `default` |

You can change any of these later in **host settings → UniFi options** (the API key field shows
"unchanged" and only overwrites when you type a new one).

## Notes

- **OS Console vs Gateway.** A gateway that also hosts the console is better added as a **UniFi
  Gateway** (you get WAN + ports). Use **UniFi OS Console** for a dedicated console host (Cloud Key /
  UNVR) where you want its own health and storage.
- **The API key is per controller, not per device.** Reuse the same key for every UniFi device on that
  controller.

## Troubleshooting

- **No metrics but ping is up.** Check the controller URL and port are reachable from the proxy, the
  API key is valid, and the MAC matches a device on the named site. A wrong site name is a common
  cause of an empty device.
- **HTTPS certificate errors** on a self-hosted controller are expected (self-signed); the template
  does not verify the certificate for the Integration API call.

# UPS (NUT)

A UPS is monitored through **NUT** (Network UPS Tools). There are two classes with the same curated
sensors - pick the one that matches how your UPS is exposed:

| Class | Use when | How it reads |
|---|---|---|
| **UPS (NUT)** | you have a NUT server (`upsd`) on the network | the proxy's `argus_nut.py` collector speaks the NUT protocol to `upsd` directly - no extra app |
| **UPS (NUT via PeaNUT)** | you front NUT with **PeaNUT** (an HTTP UI/API for NUT) | a plain HTTP-API call to PeaNUT on `:8080` |

Both report the same sensors: **battery %**, **on-battery / low-battery** status, **runtime**, **load**,
**input voltage**, and **power draw**.

## UPS (NUT) - direct

The proxy talks to `upsd` on the NUT server. `argus_nut.py` is baked into the probe image, so nothing
extra is needed on the proxy.

1. **Add device → UPS (NUT)**, give the NUT server's IP as the host.
2. Fill in:

   | Field | Macro | Notes |
   |---|---|---|
   | UPS name | `{$NUT.UPS}` | required, the upsd UPS name - find it with `upsc -l <host>` |
   | upsd port | `{$NUT.PORT}` | optional, defaults to `3493` |
   | upsd username | `{$NUT.USER}` | only if upsd needs a login to read |
   | upsd password | `{$NUT.PASSWORD}` | only if upsd needs a login; stored as a secret |

`upsd` must allow the proxy's IP (its `upsd.conf` `LISTEN` and `upsd.users`), and TCP **3493** must be
open from the proxy.

## UPS (NUT via PeaNUT)

Use this if you already run PeaNUT in front of NUT. It is a plain HTTP-API class - no proxy collector.

1. **Add device → UPS (NUT via PeaNUT)**, give the PeaNUT box's IP as the host.
2. Fill in:

   | Field | Macro | Notes |
   |---|---|---|
   | PeaNUT URL | `{$PEANUT.URL}` | required, auto-filled to `http://<host>:8080` |
   | UPS name | `{$PEANUT.UPS}` | required, see PeaNUT `/api/v1/devices` (often `ups`) |
   | PeaNUT username | `{$PEANUT.USER}` | blank if PeaNUT runs with `AUTH_DISABLED` |
   | PeaNUT password | `{$PEANUT.PASSWORD}` | blank if `AUTH_DISABLED`; stored as a secret |

## Troubleshooting

- **No data (direct NUT).** Confirm the UPS name (`upsc -l <host>`), that `upsd` listens on the LAN and
  allows the proxy IP, and that port 3493 is open. If upsd requires auth to read, set the user/password.
- **No data (PeaNUT).** Check `{$PEANUT.URL}` and port, the UPS name against `/api/v1/devices`, and the
  PeaNUT credentials if auth is enabled.
- Change any macro later in **host settings** and use **Discover now**.

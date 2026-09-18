# Home Assistant

The **Home Assistant** class monitors the Home Assistant **platform** over its REST API with a
long-lived access token: API reachability and the Core / Supervisor / OS versions. It watches the
platform's health, not the individual entities/sensors inside it.

## 1. Create a long-lived access token

In Home Assistant: **Profile → Security → Long-lived access tokens → Create token**. Copy it - it is
shown once.

## 2. Add the device in Argus

**Add device → Home Assistant**, then fill in:

| Field | Macro | Notes |
|---|---|---|
| Base URL | `{$HASS.URL}` | required, auto-filled to `http://<host>:8123` - adjust if you use HTTPS or a different port |
| Long-lived access token | `{$HASS.TOKEN}` | required, stored as a secret |

Change either later in **host settings → Home Assistant options** (the token field shows "unchanged"
and only overwrites when you type a new one).

## What it monitors

- **API up / ready** - whether the REST API answers with the token (HIGH alert if it is down or not
  ready).
- **Core version** - the running Home Assistant Core version.
- **Supervisor version** and **OS version** - on installs that have them (Home Assistant OS or
  Supervised), read from their update entities; blank on a Container / Core-only install. The entities
  read are set by `{$HASS.SUPERVISOR.ENTITY}` and `{$HASS.OS.ENTITY}` (sensible defaults; override only
  if yours are named differently).

## Troubleshooting

- **API down but the box pings.** Check the URL and port, that you used the right scheme (http vs
  https), and that the token is valid and not revoked. A reverse proxy in front of Home Assistant must
  pass the `Authorization` header through.

# Home Assistant

The **Home Assistant** class monitors the Home Assistant **platform** over its REST API with a
long-lived access token: API reachability, version, integration and entity counts, and the number of
unavailable entities. It watches the platform's health, not the individual entities/sensors inside it.

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

- **API up** - whether the REST API answers with the token.
- **Version** - the running Home Assistant version.
- **Integrations** loaded.
- **Entity count**, and **unavailable entities** (a rising count of unavailable entities often flags a
  failing integration or offline device).

## Troubleshooting

- **API down but the box pings.** Check the URL and port, that you used the right scheme (http vs
  https), and that the token is valid and not revoked. A reverse proxy in front of Home Assistant must
  pass the `Authorization` header through.

# DNS (AdGuard Home + generic DNS server)

Two related classes cover DNS monitoring. Both can run a **real resolve check** (the site proxy or core
runs `dns-resolver.py` to resolve names against the server and records success / time / resolved IP);
**AdGuard Home** adds its own filtering statistics on top from the admin API.

| Class | Monitors |
|---|---|
| **DNS server** | per-name resolve success, resolve time, and resolved IP for the names you list |
| **AdGuard Home** | the resolve check above, plus AdGuard's own stats: service up/down, protection enabled, version, queries and blocked queries (today's counters), block rate, and average processing time |

## DNS server (any resolver)

Use this for Pi-hole, Microsoft DNS, a Ubiquiti resolver, a plain BIND/Unbound box - anything that
answers on `:53`.

1. **Add device → DNS server**, give the server's IP or DNS name.
2. Set **Names to resolve** (`{$DNS.RESOLVE.NAMES}`) to a comma-separated list, e.g.
   `example.com,cloudflare.com`. Each name becomes its own resolve sensor (success, time, IP), grouped
   under the server.

A name that stops resolving raises a **HIGH** alert. A slow resolve raises a **warning** at or above
`{$DNS.RTT.WARN}` (default 0.5 s) and a **high-severity** alert at or above `{$DNS.RTT.HIGH}` (default
1 s). The query port defaults to 53 (`{$DNS.PORT}`). The resolve runs from the
proxy/core, so it needs nothing installed on the DNS server itself.

## AdGuard Home

1. **Add device → AdGuard Home**, then fill in:

   | Field | Macro | Notes |
   |---|---|---|
   | Admin URL | `{$ADGUARD.URL}` | required, auto-filled to `http://<host>` - adjust the port (default `:3000`) |
   | Admin username | `{$ADGUARD.USER}` | blank if the admin UI has no login |
   | Admin password | `{$ADGUARD.PASSWORD}` | blank if no login; stored as a secret |
   | Names to resolve | `{$DNS.RESOLVE.NAMES}` | optional, same as the DNS server class |

2. AdGuard's filtering stats are read from its admin API, so the URL and (if set) admin credentials
   must let the proxy reach the admin interface.

### The daily query bars

AdGuard's total / blocked query counters render as a **daily stacked bar chart** (queries with the
blocked portion highlighted), reconstructed from AdGuard's own per-day statistics rather than the
rolling window totals, so each bar is a true local calendar day. Block rate is the blocked share of
that day.

## Troubleshooting

- **Resolve sensors missing.** Confirm `{$DNS.RESOLVE.NAMES}` is set and the proxy can reach the server
  on `:53`. Change the list in **host settings** and use **Discover now**.
- **AdGuard stats empty but resolve works.** The admin API call is failing - check `{$ADGUARD.URL}`
  (including the port), and the admin username/password if the UI has a login.

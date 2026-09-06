import { useEffect, useRef, useState, Fragment, type FormEvent, type ReactNode, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import { registerPasskey, loginWithPasskey } from './webauthn'
import { Button, Card, Field, Banner, Badge, CopyButton, Switch, Select, Skeleton, EmptyState } from './ui'
import { useConfirm, usePrompt, useAlert } from './dialog'
import { useToast } from './toast'

type Me = { email: string; name: string; surname: string; role: string; mfa_enabled?: boolean; landing?: 'overview' | 'errors'; advanced?: boolean }
type User = { id: number; email: string; name: string; surname: string; role: string; mfa_enabled?: boolean; passkeys?: number; disabled?: boolean }
type Passkey = { id: string; name: string; created: string; last_used: string | null }
type Host = { id: string; name: string; problems: number; severity: number; state: string; paused: boolean; hidden: boolean; paused_until?: number; hidden_until?: number; groups: string[]; proxy_id?: string }
type Group = { id: string; name: string; hosts: number }
type DeviceClass = { id: string; label: string; family: string; pattern: string; iface: string; offers_http: boolean }
type SnmpCfg = { version: number; community: string; bulk: number; security_name: string; security_level: number; auth_protocol: number; auth_passphrase: string; priv_protocol: number; priv_passphrase: string; context_name: string }
type Iface = { interfaceid?: string; type: number; useip: number; ip: string; dns: string; port: string; snmp?: SnmpCfg; inherit?: boolean }
type HostCfg = { hostid: string; host: string; name: string; monitored_by: number; proxy_id?: string; proxy_name?: string; proxy_default?: SnmpCfg; interfaces: Iface[] }
type Proxy = { id: string; name: string; last_access: number; online: boolean; mode: string; enrolled_at?: number; version?: string; target?: string; latest?: string; selfupdate?: boolean; update_status?: string; last_checkin?: number; updater_version?: string; updater_latest?: string; updater_status?: string; break_glass?: boolean; break_glass_user?: string; sec_updates?: number; reboot_required?: boolean; os_reported_at?: number; os_version?: string }
type SearchHit = { type: 'host' | 'sensor' | 'group'; label: string; sub: string; host_id?: string; item_id?: string; group?: string }
type Channel = { id: number; type: string; name: string; enabled: boolean; sites: string[]; min_severity: number; config: Record<string, string>; last_sent_at?: number; last_error?: string; last_error_at?: number; sent_count?: number }
// Zabbix severities the notifier can act on (it never alerts below Warning). Used by the channel editor.
const SEVERITIES: { v: number; label: string }[] = [
  { v: 2, label: 'Warning & up' },
  { v: 3, label: 'Average & up' },
  { v: 4, label: 'High & up' },
  { v: 5, label: 'Disaster only' },
]
type SensorItem = { id: string; name: string; key: string; last_value: string; units: string; last_clock: number; supported: boolean; numeric: boolean; paused: boolean; hidden: boolean; paused_until?: number; hidden_until?: number; category?: string; label?: string; priority: number }
type Problem = { event_id: string; name: string; severity: number; state: string; acknowledged: boolean; ack_until?: number; item_ids: string[] }
type TriggerHost = { id: string; name: string }
type Trigger = { id: string; description: string; severity: number; enabled: boolean; problem: boolean; since: number; hosts: TriggerHost[]; sensors: string[] }
type SensorRow = { host_id: string; host_name: string; item_id: string; name: string; label?: string; category?: string; value: string; units: string; last_clock: number; state: string; numeric: boolean; supported: boolean; priority: number; severity: number; reason?: string; since?: number; event_ids: string[] }
type SeriesPoint = { t: number; v?: number; min?: number; avg?: number; max?: number }
type Series = { name: string; units: string; kind: 'history' | 'trend'; points: SeriesPoint[] }

const RANGES = ['2h', '2d', '1M', '3M', '6M', '1Y']

const stateColor: Record<string, string> = { ok: 'var(--ok)', warning: 'var(--warn)', error: 'var(--err)' }
const stateRank: Record<string, number> = { ok: 0, warning: 1, error: 2 }
// Census/summary state → CSS colour var and label (six buckets, incl. paused/hidden/acked).
const STATE_VAR: Record<string, string> = { ok: 'var(--ok)', warning: 'var(--warn)', error: 'var(--err)', acked: 'var(--acked)', paused: 'var(--paused)', hidden: 'var(--hidden)' }
const STATE_LABEL: Record<string, string> = { ok: 'OK', warning: 'Warning', error: 'Error', acked: 'Acknowledged', paused: 'Paused', hidden: 'Hidden' }
const PAUSED_BLUE = 'var(--paused)'
const HIDDEN_GREY = 'var(--hidden)'

// Zabbix trigger severity (0..5) -> label + colour. Colours echo Zabbix's own severity palette so
// they read as familiar to anyone who knows Zabbix. Used by the Priority column.
const SEVERITY: { label: string; color: string }[] = [
  { label: 'Not classified', color: 'var(--muted)' },
  { label: 'Information', color: '#7499ff' },
  { label: 'Warning', color: '#ffc039' },
  { label: 'Average', color: '#ffa059' },
  { label: 'High', color: '#e97659' },
  { label: 'Disaster', color: '#e45959' },
]
function sevInfo(sev: number) { return SEVERITY[sev] ?? SEVERITY[0] }

// PriorityStars shows a sensor's PRTG-style display priority (1..5) as five stars. When canEdit,
// clicking a star sets that priority (admin/helpdesk); otherwise it's read-only. Clicks never bubble
// to the row (which would open the chart).
function PriorityStars({ value, canEdit, onSet }: { value: number; canEdit: boolean; onSet?: (p: number) => void }) {
  const stars = [1, 2, 3, 4, 5]
  return (
    <span className={'prio' + (canEdit ? ' editable' : '')} title={`Priority ${value} of 5`} onClick={(e) => e.stopPropagation()}>
      {stars.map((n) => canEdit
        ? <button key={n} type="button" className={'prio-star' + (n <= value ? ' on' : '')} aria-label={`Set priority ${n}`} onClick={(e) => { e.stopPropagation(); onSet?.(n) }}>★</button>
        : <span key={n} className={'prio-star' + (n <= value ? ' on' : '')}>★</span>)}
      {/* Compact "★4" twin for dense/narrow layouts (the phone sensor table); CSS swaps it in for the five stars. */}
      <span className="prio-compact" aria-hidden="true"><span className="prio-star on">★</span>{value}</span>
    </span>
  )
}

// healthColor: acknowledged problems get the dedicated "acknowledged" colour (washed red),
// otherwise the state colour. Keeps an acked sensor visibly flagged rather than clearing it.
function healthColor(state: string, acked: boolean): string {
  return acked ? 'var(--acked)' : (stateColor[state] || 'var(--muted)')
}

// dotColor: paused (blue) and hidden (grey) override the health colour.
function dotColor(paused: boolean, hidden: boolean, state: string): string {
  if (paused) return PAUSED_BLUE
  if (hidden) return HIDDEN_GREY
  return stateColor[state] || '#777'
}

const DURATIONS: { label: string; seconds: number | null | 'custom' }[] = [
  { label: '1 hour', seconds: 3600 },
  { label: '8 hours', seconds: 28800 },
  { label: '1 day', seconds: 86400 },
  { label: '1 week', seconds: 604800 },
  { label: 'Indefinitely', seconds: null },
  { label: 'Custom…', seconds: 'custom' },
]

const pad2 = (n: number) => String(n).padStart(2, '0')
// toLocalInput formats an epoch (ms) as a datetime-local value in the browser's local time.
function toLocalInput(ms: number): string {
  const d = new Date(ms)
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}T${pad2(d.getHours())}:${pad2(d.getMinutes())}`
}

// DurationButton is an action button that opens a duration menu; onPick gets seconds (null =
// indefinite). "Custom…" reveals a date/time picker to suppress until a chosen moment.
function DurationButton({ label, onPick, disabled, borderColor }: { label: string; onPick: (seconds: number | null) => void; disabled?: boolean; borderColor?: string }) {
  const [open, setOpen] = useState(false)
  const [custom, setCustom] = useState(false)
  const [val, setVal] = useState('')
  function close() { setOpen(false); setCustom(false) }
  function pickPreset(s: number | null | 'custom') {
    if (s === 'custom') { setVal(toLocalInput(Date.now() + 3600_000)); setCustom(true); return }
    close(); onPick(s)
  }
  function confirmCustom() {
    const t = new Date(val).getTime()
    const secs = Math.round((t - Date.now()) / 1000)
    close()
    if (isFinite(t) && secs > 0) onPick(secs)
  }
  return (
    <span style={{ position: 'relative', display: 'inline-block' }}>
      <Button variant="ghost" className="compact" onClick={(e) => { e.stopPropagation(); setCustom(false); setOpen((o) => !o) }} disabled={disabled} style={{ borderColor: borderColor || 'var(--border)' }}>{label}</Button>
      {open && (
        <>
          <div onClick={(e) => { e.stopPropagation(); close() }} style={{ position: 'fixed', inset: 0, zIndex: 20 }} />
          <div onClick={(e) => e.stopPropagation()} style={{ position: 'absolute', top: '100%', right: 0, marginTop: 4, zIndex: 21, background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, minWidth: custom ? 240 : 140, boxShadow: '0 8px 24px rgba(0,0,0,0.45)', overflow: 'hidden' }}>
            {!custom && DURATIONS.map((d) => (
              <div key={d.label} onClick={(e) => { e.stopPropagation(); pickPreset(d.seconds) }} style={{ padding: '0.4rem 0.7rem', cursor: 'pointer', fontSize: '0.8rem', whiteSpace: 'nowrap' }}>{d.label}</div>
            ))}
            {custom && (
              <div style={{ padding: '0.6rem' }}>
                <div style={{ fontSize: '0.78rem', color: 'var(--muted)', marginBottom: '0.35rem' }}>Suppress until:</div>
                <input className="input" type="datetime-local" value={val} min={toLocalInput(Date.now())} onChange={(e) => setVal(e.target.value)} style={{ width: '100%', marginBottom: '0.5rem' }} />
                <div style={{ display: 'flex', gap: '0.4rem', justifyContent: 'flex-end' }}>
                  <Button variant="ghost" className="compact" onClick={(e) => { e.stopPropagation(); setCustom(false) }}>Back</Button>
                  <Button variant="primary" className="compact" onClick={(e) => { e.stopPropagation(); confirmCustom() }}>Set</Button>
                </div>
              </div>
            )}
          </div>
        </>
      )}
    </span>
  )
}

// untilLabel formats a suppression expiry: "until Aug 12, 14:30", or "no expiry" when absent.
function untilLabel(u?: number): string {
  if (!u) return 'no expiry'
  return `until ${new Date(u * 1000).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}`
}

// relSpan renders a number of seconds as the compact "54s" / "3m" / "2h" / "5d".
function relSpan(s: number): string {
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}
// relTime renders a unix time relative to now: "3m ago" for the past, "in 22h" for the future (token
// expiries), "never" when unset. A future time used to clamp to "0s ago", which read as already expired.
function relTime(unix: number): string {
  if (!unix) return 'never'
  const s = Math.floor(Date.now() / 1000) - unix
  return s < 0 ? `in ${relSpan(-s)}` : `${relSpan(s)} ago`
}

// roundNum rounds to 2 decimals for |v|>=1 and 4 for small values (so sub-second timings
// don't collapse to 0), stripping trailing zeros.
function roundNum(n: number): string {
  if (Number.isInteger(n)) return String(n)
  const decimals = Math.abs(n) >= 1 ? 2 : 4
  return String(parseFloat(n.toFixed(decimals)))
}

const BYTE_UNITS = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
const BIT_UNITS = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps']

// scaleBy reduces n by `base` until it fits a unit, returning [value, unit].
function scaleBy(n: number, base: number, units: string[]): [string, string] {
  let v = n, i = 0
  while (Math.abs(v) >= base && i < units.length - 1) { v /= base; i++ }
  return [i === 0 ? String(Math.round(v)) : String(parseFloat(v.toFixed(2))), units[i]]
}

// fmtDuration renders a number of seconds as e.g. "1d 4h 14m".
function fmtDuration(sec: number): string {
  sec = Math.max(0, Math.floor(sec))
  const d = Math.floor(sec / 86400); sec %= 86400
  const h = Math.floor(sec / 3600); sec %= 3600
  const m = Math.floor(sec / 60)
  const parts: string[] = []
  if (d) parts.push(`${d}d`)
  if (h) parts.push(`${h}h`)
  if (m || parts.length === 0) parts.push(`${m}m`)
  return parts.join(' ')
}

// scaledUnit reports whether a unit gets special scaling/formatting (so the chart axis and
// legend format it, and the "(unit)" suffix is dropped since the value already carries it).
function scaledUnit(units: string): boolean {
  return units === 'B' || units === 'Bps' || units === 'bps' || units === 'uptime' || units === 's'
}

// scaleSeconds renders a value in seconds at a human-friendly magnitude: sub-second latencies as
// ms / µs / ns, otherwise seconds. (Long-running durations should carry the 'uptime' unit instead.)
function scaleSeconds(n: number): [string, string] {
  const a = Math.abs(n)
  if (a === 0) return ['0', 's']
  if (a < 1e-6) return [roundNum(n * 1e9), 'ns']
  if (a < 1e-3) return [roundNum(n * 1e6), 'µs']
  if (a < 1) return [roundNum(n * 1e3), 'ms']
  return [roundNum(n), 's']
}

// fmtNumParts formats a numeric reading into [value, unit], scaling byte/bit units, seconds, and
// rendering uptime as a duration.
function fmtNumParts(n: number, units: string): [string, string] {
  if (units === 'B') return scaleBy(n, 1024, BYTE_UNITS)
  if (units === 'Bps') { const [v, u] = scaleBy(n, 1024, BYTE_UNITS); return [v, u + 'ps'] }
  if (units === 'bps') return scaleBy(n, 1000, BIT_UNITS)
  if (units === 'uptime') return [fmtDuration(n), '']
  if (units === 's') return scaleSeconds(n)
  return [roundNum(n), units || '']
}

function fmtNum(n: number, units: string): string {
  const [v, u] = fmtNumParts(n, units)
  return u ? `${v} ${u}` : v
}

// readingParts formats a raw stored value into [display, unit]; non-numeric values (text,
// checksums) are returned untouched with no unit.
function readingParts(raw: string, units: string): [string, string] {
  const t = (raw ?? '').trim()
  if (t === '') return ['-', '']
  const n = Number(t)
  if (!isFinite(n)) return [raw, '']
  return fmtNumParts(n, units)
}

// lastVal returns the most recent non-null value of a uPlot data series.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function lastVal(u: any, sidx: number): number | null {
  const arr = u.data[sidx]
  for (let i = arr.length - 1; i >= 0; i--) if (arr[i] != null) return arr[i]
  return null
}

const ROLES = ['admin', 'helpdesk', 'viewer']

async function errText(res: Response | null, fallback: string) {
  if (!res) return fallback
  const j = await res.json().catch(() => ({}))
  return (j && j.error) || fallback
}

type EnrollTokenRow = { id: number; proxy_name: string; site: string; status: string; created_at: number; expires_at: number }
type CreatedToken = { id: number; token: string; proxy_name: string; site: string; expires_at: number; enroll_url: string; core_host: string }

// Cross-component refresh signal: a mutation (ack / pause / hide) fires this so the shell's
// status summary and any listening view reload immediately instead of waiting for the 30s poll.
const refreshBus = new Set<() => void>()
function onDataRefresh(fn: () => void): () => void { refreshBus.add(fn); return () => { refreshBus.delete(fn) } }
function fireDataRefresh(): void { refreshBus.forEach((f) => f()) }

// Spark draws a tiny inline sparkline from a compact recent value series (from /api/spark). width is
// the drawing resolution (the dense monitoring tree keeps the compact 84px); fill makes the SVG scale
// to its cell's width, so the roomy fixed-width Trend column in the overview/status lists gets a big,
// column-filling trace at any screen size.
function Spark({ values, color, width = 84, fill = false }: { values?: number[]; color: string; width?: number; fill?: boolean }) {
  if (!values || values.length < 2) return <span style={{ color: 'var(--faint)', fontSize: 12 }}>-</span>
  const w = width, h = 20
  let min = values[0], max = values[0]
  for (const v of values) { if (v < min) min = v; if (v > max) max = v }
  const rng = max - min || 1
  const px = (i: number) => (i / (values.length - 1)) * (w - 2) + 1
  const py = (v: number) => h - 2 - ((v - min) / rng) * (h - 4)
  let d = ''
  values.forEach((v, i) => { d += (i ? 'L' : 'M') + px(i).toFixed(1) + ' ' + py(v).toFixed(1) + ' ' })
  const area = `M1 ${h - 1} ${d.replace('M', 'L').trim()} L${w - 1} ${h - 1} Z`
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" style={{ display: 'block', width: fill ? '100%' : undefined }}>
      <path d={area} fill={color} opacity={0.13} />
      <path d={d.trim()} fill="none" stroke={color} strokeWidth={1.4} />
      <circle cx={w - 1} cy={py(values[values.length - 1])} r={1.8} fill={color} />
    </svg>
  )
}

// useSparks fetches compact recent series for a set of item ids (batched), refreshing every 60s.
function useSparks(itemIds: string[]): Record<string, number[]> {
  const [map, setMap] = useState<Record<string, number[]>>({})
  const [tick, setTick] = useState(0)
  const key = itemIds.slice().sort().join(',')
  useEffect(() => { const t = setInterval(() => setTick((x) => x + 1), 60000); return () => clearInterval(t) }, [])
  useEffect(() => {
    if (!key) { setMap({}); return }
    let cancelled = false
    fetch(`/api/spark?items=${encodeURIComponent(key)}&range=2h`)
      .then((r) => (r.ok ? r.json() : {}))
      .then((m) => { if (!cancelled) setMap(m || {}) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [key, tick])
  return map
}

export default function App() {
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)
  const [passkeysAvailable, setPasskeysAvailable] = useState(false)
  const [passwordReset, setPasswordReset] = useState(false)
  const [probeEnroll, setProbeEnroll] = useState(false)
  // A password-reset link (?reset=…) shows the set-new-password screen, signed in or not.
  const [resetToken, setResetToken] = useState<string | null>(() => new URLSearchParams(window.location.search).get('reset'))
  // True only when the shell mounts right after a sign-in (not on an authenticated reload), so the
  // login -> app transition fades in instead of hard-cutting.
  const [justLoggedIn, setJustLoggedIn] = useState(false)

  useEffect(() => {
    fetch('/api/me').then((r) => (r.ok ? r.json() : null)).then(setMe).catch(() => setMe(null)).finally(() => setLoading(false))
    // Passkeys require the server to be configured for WebAuthn AND a secure context
    // (HTTPS or localhost) - over a private IP on plain HTTP they can't be used.
    fetch('/api/features').then((r) => r.json()).then((f) => { setPasskeysAvailable(!!f.passkeys && window.isSecureContext); setPasswordReset(!!f.password_reset); setProbeEnroll(!!f.probe_enroll) }).catch(() => {})
  }, [])

  if (resetToken) return <ResetPassword token={resetToken} onDone={() => { window.history.replaceState({}, '', window.location.pathname); setResetToken(null) }} />
  // Neutral loader during the initial /api/me check - deliberately NOT the branded Frame, so an
  // authenticated refresh doesn't flash the login-page chrome before the app mounts.
  if (loading) return <div style={{ minHeight: '100dvh', display: 'grid', placeItems: 'center', color: 'var(--faint)' }}>Loading…</div>
  if (!me) return <Login onSuccess={(m) => { setJustLoggedIn(true); setMe(m) }} passkeysAvailable={passkeysAvailable} passwordReset={passwordReset} />
  return <AppShell me={me} onMe={setMe} onLogout={() => { setJustLoggedIn(false); setMe(null) }} passkeysAvailable={passkeysAvailable} probeEnroll={probeEnroll} enter={justLoggedIn} />
}

function Frame({ children }: { children: ReactNode }) {
  return (
    <main style={{ minHeight: '100dvh', display: 'flex', flexDirection: 'column', alignItems: 'center', padding: 'clamp(2.5rem, 9vh, 7rem) 1.25rem 2.5rem' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 22 }}>
        <img src="/argus-logo.png" alt="Argus" width={64} height={64} />
        <div>
          <h1 style={{ margin: 0, lineHeight: 1.1 }}>Argus</h1>
          <p style={{ color: 'var(--muted)', margin: '2px 0 0' }}>Monitoring cockpit</p>
        </div>
      </div>
      <div style={{ width: '100%', maxWidth: 380 }}>{children}</div>
    </main>
  )
}

function Login({ onSuccess, passkeysAvailable, passwordReset }: { onSuccess: (m: Me) => void; passkeysAvailable: boolean; passwordReset: boolean }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [forgot, setForgot] = useState(false)

  async function passkeyLogin() {
    setBusy(true); setError(null)
    try {
      onSuccess(await loginWithPasskey())
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : 'Passkey login failed')
    } finally { setBusy(false) }
  }
  // When the account has MFA, the password step returns a short-lived token and we
  // switch to the code step instead of signing straight in.
  const [mfaToken, setMfaToken] = useState<string | null>(null)
  const [code, setCode] = useState('')

  async function submitPassword(e: FormEvent) {
    e.preventDefault()
    setBusy(true); setError(null)
    try {
      const res = await fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email, password }) })
      if (!res.ok) { setError('Invalid email or password'); return }
      const data = await res.json()
      if (data.mfa_required) { setMfaToken(data.mfa_token); return }
      onSuccess(data)
    } catch { setError('Could not reach the server') } finally { setBusy(false) }
  }

  async function submitCode(e: FormEvent) {
    e.preventDefault()
    setBusy(true); setError(null)
    try {
      const res = await fetch('/api/login/totp', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ mfa_token: mfaToken, code }) })
      if (!res.ok) { setError(await errText(res, 'Invalid code')); return }
      onSuccess(await res.json())
    } catch { setError('Could not reach the server') } finally { setBusy(false) }
  }

  if (forgot) return <ForgotPassword initialEmail={email} onBack={() => setForgot(false)} />

  if (mfaToken) {
    return (
      <Frame>
        <Card style={{ maxWidth: 380, marginTop: '1.5rem' }} title="Two-factor authentication" note="Enter the 6-digit code from your authenticator, or a recovery code.">
          <form onSubmit={submitCode}>
            {/* Hidden username so password managers (Bitwarden) treat this as a login
                form and offer to autofill the one-time-code field. */}
            <input
              type="text"
              name="username"
              autoComplete="username"
              value={email}
              readOnly
              tabIndex={-1}
              aria-hidden="true"
              style={{ position: 'absolute', width: 1, height: 1, opacity: 0, pointerEvents: 'none' }}
            />
            <input
              className="input"
              style={{ width: '100%', marginBottom: '1rem', letterSpacing: '0.15em' }}
              value={code}
              onChange={(e) => setCode(e.target.value)}
              autoComplete="one-time-code"
              inputMode="numeric"
              name="otp"
              id="otp"
              placeholder="123456"
              autoFocus
              required
            />
            <Banner variant="error">{error}</Banner>
            <Button type="submit" variant="primary" block disabled={busy}>{busy ? 'Verifying…' : 'Verify'}</Button>
          </form>
          <Button variant="ghost" block style={{ marginTop: '0.6rem' }} onClick={() => { setMfaToken(null); setCode(''); setError(null) }}>Back</Button>
        </Card>
      </Frame>
    )
  }

  return (
    <Frame>
      <Card style={{ maxWidth: 380, marginTop: '1.5rem' }} title="Sign in">
        <form onSubmit={submitPassword}>
          <Field label="Email" type="email" value={email} autoComplete="username" onChange={(e) => setEmail(e.target.value)} required />
          <Field label="Password" type="password" value={password} autoComplete="current-password" onChange={(e) => setPassword(e.target.value)} required />
          <Banner variant="error">{error}</Banner>
          <Button type="submit" variant="primary" block disabled={busy}>{busy ? 'Signing in…' : 'Sign in'}</Button>
        </form>
        {passwordReset && (
          <div style={{ textAlign: 'center', marginTop: '0.7rem' }}>
            <button type="button" onClick={() => { setForgot(true); setError(null) }} style={{ background: 'none', border: 'none', color: 'var(--accent)', cursor: 'pointer', fontSize: '0.85rem', padding: 0 }}>Forgot password?</button>
          </div>
        )}
        {passkeysAvailable && (
          <>
            <div style={{ textAlign: 'center', color: 'var(--faint)', margin: '0.9rem 0 0.6rem', fontSize: '0.85rem' }}>or</div>
            <Button variant="ghost" block onClick={passkeyLogin} disabled={busy}>Sign in with a passkey</Button>
          </>
        )}
      </Card>
    </Frame>
  )
}

function ForgotPassword({ initialEmail, onBack }: { initialEmail: string; onBack: () => void }) {
  const [email, setEmail] = useState(initialEmail)
  const [sent, setSent] = useState(false)
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault(); setBusy(true)
    try {
      await fetch('/api/password-reset/request', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email }) }).catch(() => {})
      setSent(true)
    } finally { setBusy(false) }
  }

  return (
    <Frame>
      <Card style={{ maxWidth: 380, marginTop: '1.5rem' }} title="Reset your password">
        {sent ? (
          <>
            <p style={{ color: 'var(--muted)', marginTop: 0 }}>If an account exists for that email, a reset link is on its way. It's valid for 1 hour - check your spam folder if it doesn't arrive.</p>
            <Button variant="primary" block onClick={onBack}>Back to sign in</Button>
          </>
        ) : (
          <form onSubmit={submit}>
            <p style={{ color: 'var(--muted)', marginTop: 0 }}>Enter your account email and we'll send a reset link.</p>
            <Field label="Email" type="email" value={email} autoComplete="username" onChange={(e) => setEmail(e.target.value)} required autoFocus />
            <Button type="submit" variant="primary" block disabled={busy}>{busy ? 'Sending…' : 'Send reset link'}</Button>
            <Button type="button" variant="ghost" block style={{ marginTop: '0.6rem' }} onClick={onBack}>Back</Button>
          </form>
        )}
      </Card>
    </Frame>
  )
}

function ResetPassword({ token, onDone }: { token: string; onDone: () => void }) {
  const [pw, setPw] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [done, setDone] = useState(false)
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault(); setError(null)
    if (pw !== confirm) { setError('The passwords do not match.'); return }
    if (pw.length < 8) { setError('Password must be at least 8 characters.'); return }
    setBusy(true)
    try {
      const res = await fetch('/api/password-reset/confirm', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token, new_password: pw }) })
      if (!res.ok) { setError(await errText(res, 'Could not reset password')); return }
      setDone(true)
    } catch { setError('Could not reach the server') } finally { setBusy(false) }
  }

  return (
    <Frame>
      <Card style={{ maxWidth: 380, marginTop: '1.5rem' }} title="Set a new password">
        {done ? (
          <>
            <Banner variant="success">Your password has been updated, and other sessions were signed out. If you use two-factor, you'll still need your code to sign in.</Banner>
            <Button variant="primary" block onClick={onDone}>Go to sign in</Button>
          </>
        ) : (
          <form onSubmit={submit}>
            {/* Hidden username so password managers save this against the account. */}
            <input type="text" name="username" autoComplete="username" tabIndex={-1} aria-hidden="true" readOnly value="" style={{ position: 'absolute', width: 1, height: 1, opacity: 0, pointerEvents: 'none' }} />
            <Field label="New password (min 8)" type="password" value={pw} autoComplete="new-password" onChange={(e) => setPw(e.target.value)} required minLength={8} autoFocus />
            <Field label="Confirm new password" type="password" value={confirm} autoComplete="new-password" onChange={(e) => setConfirm(e.target.value)} required />
            <Banner variant="error">{error}</Banner>
            <Button type="submit" variant="primary" block disabled={busy}>{busy ? 'Updating…' : 'Update password'}</Button>
          </form>
        )}
      </Card>
    </Frame>
  )
}

type View = 'overview' | 'triggers' | 'monitoring' | 'notifications' | 'probes' | 'users' | 'settings' | 'account' | 'list'
const VIEW_TITLES: Record<View, [string, string]> = {
  overview: ['Overview', 'What needs attention right now'],
  triggers: ['Triggers', 'Alert rules - firing, or all by host'],
  monitoring: ['Monitoring', 'Sites, hosts and sensors'],
  notifications: ['Notifications', 'Alert routing and channels'],
  probes: ['Probes', 'Site probe enrollment'],
  users: ['Users', 'Accounts and access'],
  settings: ['Settings', 'System configuration'],
  account: ['Account', 'Your security settings'],
  list: ['Sensors', 'Filtered across all sites'],
}

const ic = {
  overview: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><g transform="translate(0 4)"><path d="M3 12a9 9 0 0 1 18 0" /><path d="M12 12l4-2" /><circle cx="12" cy="12" r="1.6" fill="currentColor" stroke="none" /></g></svg>,
  triggers: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><path d="M3 12h4l2-6 4 12 2-6h6" /></svg>,
  monitoring: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round"><rect x="9" y="3" width="6" height="5" rx="1.5" /><rect x="3" y="16" width="6" height="5" rx="1.5" /><rect x="15" y="16" width="6" height="5" rx="1.5" /><path d="M12 8v4" /><path d="M6 16v-2a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v2" /></svg>,
  notifications: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9" /><path d="M13.7 21a2 2 0 0 1-3.4 0" /></svg>,
  probes: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="12" r="2" /><path d="M16.2 7.8a6 6 0 0 1 0 8.4M7.8 16.2a6 6 0 0 1 0-8.4M19 5a10 10 0 0 1 0 14M5 19A10 10 0 0 1 5 5" /></svg>,
  users: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="9" cy="8" r="3.2" /><path d="M3.5 20a5.5 5.5 0 0 1 11 0" /><path d="M16 5.2a3.2 3.2 0 0 1 0 6M17 14.5a5.5 5.5 0 0 1 3.5 5.5" /></svg>,
  settings: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" /></svg>,
  account: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="8" r="3.4" /><path d="M5 20a7 7 0 0 1 14 0" /></svg>,
  logout: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M15 12H3M9 6l-6 6 6 6M15 4h4a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-4" /></svg>,
  err: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="9" /><path d="M15 9l-6 6M9 9l6 6" /></svg>,
  warn: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z" /><path d="M12 9.5v4M12 17h.01" /></svg>,
  acked: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M21 15a2 2 0 0 1-2 2H8l-4 4V5a2 2 0 0 1 2-2h13a2 2 0 0 1 2 2z" /><path d="M8.5 10.3l2.4 2.4 4.6-4.6" /></svg>,
  ok: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.6"><path d="M20 6 9 17l-5-5" /></svg>,
  paused: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"><rect x="6" y="5" width="4" height="14" rx="1" /><rect x="14" y="5" width="4" height="14" rx="1" /></svg>,
  hidden: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"><path d="M2 12s3.5-7 10-7 10 7 10 7a17 17 0 0 1-2.2 2.9M3 3l18 18M9.5 9.5a3 3 0 0 0 4.2 4.2" /></svg>,
}
// Sensor state -> its status-chip icon (for the empty states of the filtered lists).
const STATE_ICON: Record<string, ReactNode> = { ok: ic.ok, warning: ic.warn, error: ic.err, acked: ic.acked, paused: ic.paused, hidden: ic.hidden }

function useTheme(): ['dark' | 'light', () => void] {
  const [theme, setTheme] = useState<'dark' | 'light'>(() => {
    try { const s = localStorage.getItem('argus-theme'); if (s === 'dark' || s === 'light') return s } catch { /* ignore */ }
    return window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
  })
  const first = useRef(true)
  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    try { localStorage.setItem('argus-theme', theme) } catch { /* ignore */ }
    // Force a repaint so text under composited layers (the blurred top bar) re-resolves the
    // CSS variables immediately instead of keeping the previous theme's colours until reflow.
    if (first.current) { first.current = false; return }
    const b = document.body; b.style.display = 'none'; void b.offsetHeight; b.style.display = ''
  }, [theme])
  return [theme, () => setTheme((t) => (t === 'dark' ? 'light' : 'dark'))]
}

// --- URL <-> navigation state -------------------------------------------------
// The active view (and its parameters) is mirrored in the address bar so a reload,
// bookmark, shared link, or Back/Forward restores the exact screen - instead of always
// resetting to Overview. Overview is the canonical bare URL; other views carry ?view=…
// (list adds &filter=…, monitoring adds &host=…&item=… when a host/sensor is open).
const NAV_VIEWS: View[] = ['overview', 'triggers', 'monitoring', 'notifications', 'probes', 'users', 'settings', 'account', 'list']
type NavState = { view: View; filter: string; host?: string; item?: string; group?: string }

function parseNav(): NavState {
  const p = new URLSearchParams(window.location.search)
  const host = p.get('host') || undefined
  const item = p.get('item') || undefined
  const group = p.get('group') || undefined
  const raw = p.get('view')
  // Fall back to monitoring for a legacy ?host=&item= (or ?group=) link that predates ?view=.
  const view: View = raw && (NAV_VIEWS as string[]).includes(raw) ? (raw as View) : (host || group) ? 'monitoring' : 'overview'
  return { view, filter: p.get('filter') || 'error', host, item, group }
}

function buildNav(s: NavState): string {
  const p = new URLSearchParams()
  if (s.view !== 'overview') p.set('view', s.view)
  if (s.view === 'list') p.set('filter', s.filter)
  // Host/sensor and group focus are mutually exclusive; host is the more specific, so it wins.
  if (s.view === 'monitoring') {
    if (s.host) { p.set('host', s.host); if (s.item) p.set('item', s.item) }
    else if (s.group) p.set('group', s.group)
  }
  const qs = p.toString()
  return window.location.pathname + (qs ? '?' + qs : '')
}

type VersionInfo = { version: string; latest?: string; update_available: boolean; dev_update?: boolean; dev_target?: string; status: string; checked_at?: number; check_error?: string }
type UpdateState = {
  self_update_enabled: boolean
  state: string // idle | requested | running | success | failed
  target?: string
  from?: string
  message?: string
  requested_by?: string
  updater_version?: string // the core's argus-updater sidecar version
  updater_pending?: boolean // a sidecar self-update is queued
}

// VersionAbout shows the running build at the top of Settings, with a verdict badge so an admin can
// tell at a glance whether this instance is on the newest release:
//   current     -> green "latest" tick (a clean release tag equal to the newest published release)
//   development -> neutral "development build" tag (a :testing/git-describe build ahead of its tag)
//   outdated    -> amber "update available" pill + a "What's new" changelog disclosure + (when the
//                  argus-updater sidecar is wired up) a one-click "Update now" button
// The one-click update is performed by the argus-updater sidecar (which holds the Docker socket); the
// core just drops a request and polls /api/update/state, showing a running / success / failure banner.
type OSStatus = {
  core: { available: boolean; sec_updates: number; reboot_required: boolean; reported_at: number; os?: string }
  reboot_window: { mode: string; weekday: number; hour: number; minute: number }
}
const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

// OSUpdates is the Settings card for OS patching & lifecycle (DESIGN §14c). The Debian OS under the
// core VM and every probe VM patches itself locally (unattended-upgrades, security suite only) - Argus
// never triggers apt remotely. This card shows the core's own patch status and lets an admin schedule
// the core's reboot (a pet that must not bounce unannounced), which a host timer honours locally.
function OSUpdates() {
  const toast = useToast()
  const [os, setOs] = useState<OSStatus | null>(null)
  const [mode, setMode] = useState('notify')
  const [weekday, setWeekday] = useState(0)
  const [time, setTime] = useState('03:00')
  const [busy, setBusy] = useState(false)

  const timeStr = (h: number, m: number) => `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`
  const load = () => fetch('/api/os/status').then((r) => (r.ok ? r.json() : null)).then((d: OSStatus | null) => {
    if (!d) return
    setOs(d)
    setMode(d.reboot_window.mode)
    setWeekday(d.reboot_window.weekday)
    setTime(timeStr(d.reboot_window.hour, d.reboot_window.minute))
  }).catch(() => {})
  useEffect(() => { load() }, [])

  const dirty = !!os && (mode !== os.reboot_window.mode || (mode === 'auto' && (weekday !== os.reboot_window.weekday || time !== timeStr(os.reboot_window.hour, os.reboot_window.minute))))
  const save = async () => {
    const [h, m] = time.split(':').map((n) => parseInt(n, 10))
    const body = mode === 'auto' ? { mode, weekday, hour: h || 0, minute: m || 0 } : { mode: 'notify' }
    setBusy(true)
    try {
      const res = await fetch('/api/os/reboot-window', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      if (!res.ok) { toast.error(await errText(res, 'Could not save the reboot window')); return }
      toast.success('Reboot window saved.'); await load()
    } finally { setBusy(false) }
  }

  const c = os?.core
  const sec = c ? c.sec_updates : -1
  return (
    <section className="set-card">
      <h3>OS updates</h3>
      <p className="set-note">The Debian OS under the core and probe VMs patches itself locally — security updates only, applied automatically. Argus reports status and schedules the core's reboot; it never runs apt remotely (there's no clean rollback). Per-probe status is on the <strong>Probes</strong> page.</p>

      <div className="set-row">
        <div className="set-head"><span className="complabel">Core</span></div>
        {!c?.available ? (
          <p className="set-hint">The core's OS patch status isn't wired up yet. Run the host reporter from <span className="mono">deploy/core/setup-core.sh</span> and share the self-update dir with the core container.</p>
        ) : (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
            {c.os && <span className="mono">{c.os}</span>}
            {c.reboot_required && <span className="tag avail" title="The core needs a reboot to finish applying updates — schedule it below">reboot needed</span>}
            {sec > 0 && <span className="tag avail">{sec} security update{sec === 1 ? '' : 's'}</span>}
            {sec === 0 && !c.reboot_required && <span className="tag online">patched</span>}
            {sec < 0 && !c.reboot_required && <span className="mono" style={{ color: 'var(--faint)' }}>count unknown</span>}
            {c.reported_at > 0 && <span className="set-hint" style={{ margin: 0 }}>reported {relTime(c.reported_at)}</span>}
          </div>
        )}
      </div>

      <div className="set-row" style={{ marginBottom: 0 }}>
        <div className="set-head"><span className="complabel">Core reboot window</span></div>
        <p className="set-hint" style={{ marginTop: 0 }}>Security patches apply automatically, but the core hosts the database and Zabbix, so its <strong>reboot</strong> is never unattended by default. Probe VMs reboot themselves in a weekly ~03:00 window.</p>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          <select className="input" value={mode} onChange={(e) => setMode(e.target.value)} style={{ maxWidth: 320 }}>
            <option value="notify">Notify only — never reboot automatically</option>
            <option value="auto">Auto-reboot weekly when needed</option>
          </select>
          {mode === 'auto' && <>
            <select className="input" value={weekday} onChange={(e) => setWeekday(parseInt(e.target.value, 10))} style={{ maxWidth: 160 }}>
              {WEEKDAYS.map((d, i) => <option key={i} value={i}>{d}</option>)}
            </select>
            <input className="input" type="time" value={time} onChange={(e) => setTime(e.target.value)} style={{ maxWidth: 130 }} />
          </>}
          <Button variant="default" onClick={save} disabled={busy || !dirty}>{busy ? 'Saving…' : 'Save'}</Button>
        </div>
        {mode === 'auto' && <p className="set-hint" style={{ marginBottom: 0 }}>The core reboots only when an update requires it, on <strong>{WEEKDAYS[weekday]}</strong> at <strong>{time}</strong> (local). Take a hypervisor snapshot as your safety net.</p>}
      </div>
    </section>
  )
}

function VersionAbout() {
  const confirm = useConfirm()
  const [v, setV] = useState<VersionInfo | null>(null)
  const [notes, setNotes] = useState<string | null>(null)
  const [upd, setUpd] = useState<UpdateState | null>(null)
  const [busy, setBusy] = useState(false)
  const [checking, setChecking] = useState(false)
  const [checkedMsg, setCheckedMsg] = useState('')
  const [targets, setTargets] = useState<{ channels: string[]; releases: string[] } | null>(null)
  const [switchTo, setSwitchTo] = useState('')
  const [err, setErr] = useState('')
  useEffect(() => { fetch('/api/version').then((r) => (r.ok ? r.json() : null)).then(setV).catch(() => {}) }, [])
  useEffect(() => { fetch('/api/version/tags').then((r) => (r.ok ? r.json() : null)).then((d) => { if (d) setTargets(d) }).catch(() => {}) }, [])
  // Poll the self-update state so the button + banner track the sidecar (and reconnect after the brief
  // restart an update causes - a failed fetch keeps the last state rather than clearing it).
  useEffect(() => {
    const refresh = () => fetch('/api/update/state').then((r) => (r.ok ? r.json() : null)).then((d) => { if (d) setUpd(d) }).catch(() => {})
    refresh()
    const t = setInterval(refresh, 5000)
    return () => clearInterval(t)
  }, [])
  const refreshUpd = () => fetch('/api/update/state').then((r) => (r.ok ? r.json() : null)).then((d) => { if (d) setUpd(d) }).catch(() => {})
  const running = v && (v.version || 'development build')
  const loadNotes = () => {
    if (notes !== null) return
    fetch('/api/version/notes').then((r) => (r.ok ? r.json() : null)).then((d) => setNotes((d && d.notes) || '')).catch(() => setNotes(''))
  }
  const active = upd != null && (upd.state === 'requested' || upd.state === 'running')
  const updateSidecar = async () => {
    if (!(await confirm({ title: 'Update the updater sidecar', message: 'Recreate the argus-updater sidecar onto the latest version? It rolls back if the new one fails. The core is not affected.', confirmLabel: 'Update sidecar' }))) return
    setErr('')
    fetch('/api/update/updater', { method: 'POST' })
      .then(async (r) => { if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'could not queue the sidecar update') })
      .then(refreshUpd)
      .catch((e) => setErr(String(e.message || e)))
  }
  const startUpdate = () => {
    setBusy(true); setErr('')
    fetch('/api/update/start', { method: 'POST' })
      .then(async (r) => { if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'could not start the update') })
      .then(refreshUpd)
      .catch((e) => setErr(String(e.message || e)))
      .finally(() => setBusy(false))
  }
  const dismiss = () => fetch('/api/update/dismiss', { method: 'POST' }).then(refreshUpd).catch(() => {})
  // Deliberately switch the core to a chosen channel/version (bypassing the in-place channel-preserve).
  const doSwitch = async () => {
    if (!switchTo) return
    const isVer = /^v?\d+\.\d+\.\d+$/.test(switchTo)
    const msg = isVer
      ? `Switch Argus to ${switchTo}? This pins the core to that exact version - it won't track a channel until you switch back to latest or testing. The core will pull the image and restart briefly.`
      : `Switch Argus to the "${switchTo}" channel? The core will pull that image and restart briefly.`
    if (!(await confirm({ title: 'Switch version', message: msg, confirmLabel: 'Switch' }))) return
    setBusy(true); setErr('')
    fetch('/api/update/start', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ target: switchTo }) })
      .then(async (r) => { if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || 'could not start the switch') })
      .then(refreshUpd)
      .catch((e) => setErr(String(e.message || e)))
      .finally(() => setBusy(false))
  }
  // Force an immediate GHCR re-check (the automatic check is nightly), then reflect the fresh verdict.
  const checkNow = () => {
    setChecking(true); setCheckedMsg(''); setErr('')
    fetch('/api/version/check', { method: 'POST' })
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('check failed'))))
      .then((d: VersionInfo) => { setV(d); setNotes(null); setCheckedMsg(!d.check_error && !d.update_available ? "You're on the latest available build." : '') })
      .catch(() => setErr('Update check failed'))
      .finally(() => setChecking(false))
  }
  // After a successful update the running SPA is still the OLD bundle (and shows the old version):
  // clear the finished job, then reload to pull the new frontend + version. Dismiss-then-reload so
  // the success banner doesn't reappear on the fresh load.
  const reloadNow = () => fetch('/api/update/dismiss', { method: 'POST' }).catch(() => {}).finally(() => window.location.reload())
  return (
    <section className="set-card">
      <h3>About</h3>
      <p className="set-note">The Argus components running on this instance — the core app, and its updater sidecar when wired up — and whether newer versions are available.</p>
      <div className="set-row" style={{ marginBottom: v && v.update_available ? undefined : 0 }}>
        <div className="set-head">
          <span className="complabel">Core</span>
          {v && <Button variant="default" style={{ marginLeft: 'auto' }} onClick={checkNow} disabled={checking}>{checking ? 'Checking…' : 'Check for updates'}</Button>}
        </div>
        {!v ? <span className="set-hint">Checking…</span> : (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
            <span className="mono">{running}</span>
            {v.update_available && !v.dev_update && <span className="vtag upd">↑ {v.latest} available</span>}
            {v.dev_update && <span className="vtag upd">↑ {v.dev_target || 'new testing build'}</span>}
            {v.status === 'current' && !v.check_error && <span className="vtag ok">latest</span>}
            {v.status === 'development' && <span className="vtag dev">development build</span>}
            {v.check_error && !v.update_available && <span className="vtag dev" title="The last update check couldn't reach the registry — the verdict may be out of date">check failed</span>}
          </div>
        )}
      </div>
      {v?.check_error && <p className="set-hint" style={{ margin: '0 0 8px', color: 'var(--warn)' }}>{v.check_error} to check for updates — {v.checked_at ? `showing the result from ${relTime(v.checked_at)}` : 'no successful check yet'}. Retry in a moment.</p>}
      {checkedMsg && <p className="set-hint" style={{ margin: '0 0 8px' }}>{checkedMsg}</p>}

      {/* Update progress / outcome banner (driven by the argus-updater sidecar). */}
      {upd && upd.state === 'requested' && <Banner variant="info">Update to {upd.target} queued - waiting for the updater to pick it up…</Banner>}
      {upd && upd.state === 'running' && <Banner variant="info">Updating to {upd.target}… {upd.message ? `(${upd.message})` : ''} Argus will restart briefly.</Banner>}
      {upd && upd.state === 'success' && (
        <Banner variant="success">Updated to {upd.target}. Reload to finish loading the new version.</Banner>
      )}
      {upd && upd.state === 'failed' && (
        <Banner variant="error">Update to {upd.target} failed: {upd.message || 'unknown error'}. The previous version was kept. <button type="button" className="linkbtn" onClick={dismiss}>Dismiss</button></Banner>
      )}
      {err && <Banner variant="error">{err}</Banner>}

      {upd && upd.state === 'success' ? (
        // The update landed; the running SPA is still the old bundle. Replace the "Update" button with
        // a Reload button so the only action offered is the one that actually finishes the update.
        <div className="set-row" style={{ marginBottom: 0 }}>
          <Button variant="success" onClick={reloadNow}>Reload to finish updating</Button>
        </div>
      ) : v && v.update_available && (
        <>
          {v.dev_update ? (
            <p className="set-row set-hint" style={{ marginBottom: 8 }}>A newer <span className="mono">:testing</span> build{v.dev_target ? <> — <span className="mono">{v.dev_target}</span></> : ''} has been published (unreleased changes past {running}). Updating re-pulls the testing channel in place.</p>
          ) : (
            <details className="set-row" onToggle={loadNotes}>
              <summary className="set-hint" style={{ cursor: 'pointer' }}>What's new in {v.latest}</summary>
              {notes === null ? <p className="set-hint">Loading…</p>
                : notes === '' ? <p className="set-hint">Release notes unavailable.</p>
                : <pre className="release-notes">{notes}</pre>}
            </details>
          )}
          <div className="set-row" style={{ marginBottom: 0 }}>
            {upd && upd.self_update_enabled ? (
              <Button variant="primary" onClick={startUpdate} disabled={busy || active}>
                {active ? 'Updating…' : v.dev_update ? `Update to ${v.dev_target || 'the latest testing build'}` : `Update to ${v.latest}`}
              </Button>
            ) : (
              <p className="set-hint">Self-update isn't configured on this instance. Pull the new image and redeploy, or add the <span className="mono">argus-updater</span> sidecar to enable one-click updates (see the README).</p>
            )}
          </div>
        </>
      )}

      {/* Deliberate channel / version switch (latest <-> testing <-> a pinned release). */}
      {upd && upd.self_update_enabled && upd.state !== 'success' && targets && (
        <details className="set-row" style={{ marginBottom: 0 }}>
          <summary className="set-hint" style={{ cursor: 'pointer' }}>Change channel or version</summary>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginTop: 8, flexWrap: 'wrap' }}>
            <select className="input" value={switchTo} onChange={(e) => setSwitchTo(e.target.value)} style={{ maxWidth: 240 }}>
              <option value="">Select a target…</option>
              <optgroup label="Channels">
                <option value="latest">latest — stable releases</option>
                <option value="testing">testing — main, unreleased</option>
              </optgroup>
              {targets.releases.length > 0 && (
                <optgroup label="Recent releases">
                  {targets.releases.map((t) => <option key={t} value={t}>{t}</option>)}
                </optgroup>
              )}
            </select>
            <Button variant="default" onClick={doSwitch} disabled={busy || active || !switchTo}>Switch</Button>
          </div>
          <p className="set-hint" style={{ marginTop: 6 }}>Switches the running image to the selected channel or version. Picking a specific version pins the core — it won't track a channel until you switch back to <span className="mono">latest</span> or <span className="mono">testing</span>.</p>
        </details>
      )}

      {/* The argus-updater sidecar - same row layout as Core, so the two components read in parallel. */}
      {upd && upd.self_update_enabled && (
        <div className="set-row" style={{ marginTop: 14, paddingTop: 14, borderTop: '1px solid var(--border)', marginBottom: 0 }}>
          <div className="set-head">
            <span className="complabel">Updater sidecar</span>
            {upd.updater_pending
              ? <span className="vtag upd" style={{ marginLeft: 'auto' }} title="A sidecar self-update is queued; it applies on the sidecar's next poll">update queued</span>
              : <Button variant="default" style={{ marginLeft: 'auto' }} onClick={updateSidecar}>Update sidecar</Button>}
          </div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
            <span className="mono" title="Version of the argus-updater container that performs the updates above">{upd.updater_version || '—'}</span>
          </div>
          <p className="set-hint" style={{ marginTop: 6 }}>Holds the Docker socket and performs the core updates above. <strong>Update sidecar</strong> recreates it onto the latest image (rolling back on failure); the core keeps running throughout.</p>
        </div>
      )}
    </section>
  )
}

function AppShell({ me, onMe, onLogout, passkeysAvailable, probeEnroll, enter }: { me: Me; onMe: (m: Me) => void; onLogout: () => void; passkeysAvailable: boolean; probeEnroll: boolean; enter?: boolean }) {
  // Admin-only views can't be restored from a shared/stale URL by a non-admin.
  const clampView = (v: View): View => ((v === 'users' || v === 'settings') && me.role !== 'admin' ? 'overview' : v)
  // A fresh visit to the bare "/" (no query) honours the user's landing preference; any deep
  // link (?view=…, ?host=…, ?reset=… already handled) is respected as-is.
  const initialNav = (): NavState => {
    if (!window.location.search && me.landing === 'errors') return { view: 'list', filter: 'error' }
    return parseNav()
  }
  const [view, setView] = useState<View>(() => clampView(initialNav().view))
  const [collapsed, setCollapsed] = useState(() => { try { return localStorage.getItem('argus-collapsed') === '1' } catch { return false } })
  const [navOpen, setNavOpen] = useState(false) // mobile drawer
  const [menuOpen, setMenuOpen] = useState(false)
  const [theme, toggleTheme] = useTheme()
  const [sensors, setSensors] = useState<SensorRow[]>([])
  // False until the first /api/sensors response: the lists show a skeleton instead of flashing "All clear".
  const [sensorsLoaded, setSensorsLoaded] = useState(false)
  const [listFilter, setListFilter] = useState<string>(() => initialNav().filter)
  const canPause = me.role === 'admin' || me.role === 'helpdesk'

  useEffect(() => {
    const load = () => fetch('/api/sensors').then((r) => (r.ok ? r.json() : [])).then((s) => { setSensors(s || []); setSensorsLoaded(true) }).catch(() => setSensorsLoaded(true))
    load(); const t = setInterval(load, 30000); const off = onDataRefresh(load); return () => { clearInterval(t); off() }
  }, [])
  // Remember the desktop sidebar collapsed/expanded choice across reloads.
  useEffect(() => { try { localStorage.setItem('argus-collapsed', collapsed ? '1' : '0') } catch { /* ignore */ } }, [collapsed])
  const cnt = (st: string) => sensors.filter((s) => s.state === st).length
  const errN = cnt('error'), warnN = cnt('warning'), ackN = cnt('acked'), pausedN = cnt('paused'), hiddenN = cnt('hidden'), okN = cnt('ok')

  // Deep-link target: Overview / lists / a shared URL ask the tree to open a host (and optionally
  // a sensor's chart). Seeded from the URL so a reload restores the open host/sensor.
  const [treeTarget, setTreeTarget] = useState<{ hostId?: string; itemId?: string; itemName?: string; groupPath?: string; n: number } | null>(() => {
    const s = parseNav()
    if (s.view !== 'monitoring') return null
    if (s.host) return { hostId: s.host, itemId: s.item, n: 0 }
    if (s.group) return { groupPath: s.group, n: 0 }
    return null
  })
  const navN = useRef(0)
  // Bumped when the Monitoring tab is clicked, so MonitoringView resets its drill-down to the root
  // even when it's already the active view (no remount would otherwise happen).
  const [monHome, setMonHome] = useState(0)
  const [searchOpen, setSearchOpen] = useState(false)

  // Push a new history entry for a top-level navigation (tab switch, deep-link jump).
  function pushNav(v: View, opts?: { host?: string; item?: string; filter?: string }) {
    window.history.pushState({}, '', buildNav({ view: v, filter: opts?.filter ?? listFilter, host: opts?.host, item: opts?.item }))
  }
  function goHost(hostId: string) { navN.current += 1; setTreeTarget({ hostId, n: navN.current }); setView('monitoring'); pushNav('monitoring', { host: hostId }); setMenuOpen(false); setNavOpen(false) }
  function goSensor(hostId: string, itemId: string, itemName?: string) { navN.current += 1; setTreeTarget({ hostId, itemId, itemName, n: navN.current }); setView('monitoring'); pushNav('monitoring', { host: hostId, item: itemId }); setMenuOpen(false); setNavOpen(false) }
  function openList(st: string) { setListFilter(st); setView('list'); pushNav('list', { filter: st }); setMenuOpen(false); setNavOpen(false) }
  function goGroup(path: string) { navN.current += 1; setTreeTarget({ groupPath: path, n: navN.current }); setView('monitoring'); window.history.pushState({}, '', buildNav({ view: 'monitoring', filter: listFilter, group: path })); setMenuOpen(false); setNavOpen(false) }
  // Dispatch a quick-switcher hit to the right navigation.
  function goSearch(r: SearchHit) {
    if (r.type === 'sensor' && r.host_id && r.item_id) goSensor(r.host_id, r.item_id, r.label)
    else if (r.type === 'group' && r.group) goGroup(r.group)
    else if (r.host_id) goHost(r.host_id)
    setSearchOpen(false)
  }

  // In-tree drilldown (expand a host, open a chart) refines the URL in place - replaceState so the
  // Back button steps between screens, not every accordion toggle.
  // An explicit drill (group/host/sensor name, breadcrumb) pushes a history entry so Back/Forward step
  // through the drill levels; inline accordion toggles (expanding a host card or a sensor row) replace,
  // to keep those out of history.
  function onTreeNav(hostId: string | null, itemId: string | null, group?: string | null, push?: boolean) {
    const url = buildNav({ view: 'monitoring', filter: listFilter, host: hostId || undefined, item: itemId || undefined, group: group || undefined })
    if (push) window.history.pushState({}, '', url)
    else window.history.replaceState({}, '', url)
  }

  // The header ☰ opens the drawer on mobile, and collapses the rail on desktop.
  function toggleNav() {
    if (window.matchMedia('(max-width: 768px)').matches) { setCollapsed(false); setNavOpen((o) => !o) }
    else setCollapsed((c) => !c)
  }

  // Keep the address bar and app state in sync: canonicalize the initial URL, and respond to
  // Back/Forward (popstate) by restoring the view the URL describes.
  useEffect(() => {
    // Reflect the resolved initial view (which may come from the landing preference) in the URL,
    // so a bare "/" that lands on Errors becomes ?view=list&filter=error and Back/Forward is sane.
    const s = initialNav()
    window.history.replaceState({}, '', buildNav({ ...s, view: clampView(s.view) }))
    const onPop = () => {
      const n = parseNav()
      setView(clampView(n.view)); setListFilter(n.filter); setMenuOpen(false); setNavOpen(false)
      if (n.view === 'monitoring' && n.host) { navN.current += 1; setTreeTarget({ hostId: n.host, itemId: n.item, n: navN.current }) }
      else if (n.view === 'monitoring' && n.group) { navN.current += 1; setTreeTarget({ groupPath: n.group, n: navN.current }) }
      else if (n.view === 'monitoring') { setTreeTarget(null); setMonHome((m) => m + 1) } // stepped back to the tree root
      else setTreeTarget(null)
    }
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Ctrl/Cmd-K opens the global quick-switcher from anywhere.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.altKey && (e.key === 'k' || e.key === 'K')) {
        e.preventDefault(); setSearchOpen(true)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  async function logout() { await fetch('/api/logout', { method: 'POST' }).catch(() => {}); onLogout() }
  function goto(v: View) { setTreeTarget(null); if (v === 'monitoring') setMonHome((n) => n + 1); setView(v); pushNav(v); setMenuOpen(false); setNavOpen(false) }

  // Running version for the sidebar footer (the full About card lives in Settings). Fetched once.
  const [ver, setVer] = useState<VersionInfo | null>(null)
  useEffect(() => { fetch('/api/version').then((r) => (r.ok ? r.json() : null)).then((v) => { if (v) setVer(v) }).catch(() => {}) }, [])

  // title doubles as the tooltip for the collapsed (icon-only) rail.
  const nav = (id: View, label: string, opts?: { count?: number; soon?: boolean }) => (
    <button className={'nav' + (view === id ? ' active' : '')} title={label} onClick={() => goto(id)}>
      {ic[id as keyof typeof ic]}
      <span className="lbl">{label}</span>
      {opts?.count ? <span className="count txt-err">{opts.count}</span> : null}
      {opts?.soon ? <span className="soon">Soon</span> : null}
    </button>
  )
  const chip = (st: string, icon: ReactNode, color: string, n: number, label: string) => (
    <button className={'stat' + (view === 'list' && listFilter === st ? ' on' : '')} title={label} onClick={() => openList(st)}>
      <span className="si" style={{ color }}>{icon}</span>{n}
    </button>
  )

  const [title, sub] = view === 'list' ? [`${STATE_LABEL[listFilter]} sensors`, 'Filtered across all sites'] : VIEW_TITLES[view]
  return (
    <div className={'app-shell' + (collapsed ? ' collapsed' : '') + (navOpen ? ' nav-open' : '') + (enter ? ' app-enter' : '')}>
      {navOpen && <div className="nav-backdrop" onClick={() => setNavOpen(false)} />}
      {searchOpen && <SearchPalette onClose={() => setSearchOpen(false)} onPick={goSearch} />}
      <aside className="sidebar">
        <div className="brand">
          <img className="brand-logo" src="/argus-logo.png" alt="" width={30} height={30} />
          <div><div className="word">ARGUS</div><div className="sub">Monitoring</div></div>
        </div>
        <div className="navlabel">Watch</div>
        {nav('overview', 'Overview', { count: errN })}
        {nav('triggers', 'Triggers')}
        {nav('monitoring', 'Monitoring')}
        <div className="navlabel">Configure</div>
        {nav('notifications', 'Notifications')}
        {nav('probes', 'Probes')}
        {me.role === 'admin' && <><div className="navlabel">Admin</div>{nav('users', 'Users')}{nav('settings', 'Settings')}</>}
        <div className="side-foot">
          {ver && (
            <button type="button" className={'side-ver' + (ver.update_available ? ' upd' : '')} disabled={me.role !== 'admin'}
              title={ver.update_available ? `Update available${ver.latest ? `: ${ver.latest}` : ''} — open Settings` : `Argus ${ver.version || 'development build'}`}
              onClick={() => goto('settings')}>
              <span className={'vtag ' + (ver.update_available ? 'upd' : ver.status === 'current' ? 'ok' : 'dev')}>{ver.version || 'dev'}</span>
              {ver.update_available && <span className="side-ver-txt">update available</span>}
            </button>
          )}
          <div className="kebab-wrap" style={{ display: 'block' }}>
            <button className="userbtn" onClick={() => setMenuOpen((o) => !o)}>
              <div className="avatar">{(me.name?.[0] || me.email[0] || '?').toUpperCase()}{(me.surname?.[0] || '').toUpperCase()}</div>
              <div className="who"><div className="em">{me.email}</div><div className="ro">{me.role}</div></div>
              <svg className="car" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M6 15l6-6 6 6" /></svg>
            </button>
            {menuOpen && (
              <>
                <div onClick={() => setMenuOpen(false)} style={{ position: 'fixed', inset: 0, zIndex: 30 }} />
                <div className="menu up" style={{ left: 0, right: 'auto', minWidth: 196, zIndex: 31 }}>
                  <div className="mlabel">Signed in as {me.role}</div>
                  <button onClick={() => goto('account')}>{ic.account}Account settings</button>
                  <div className="sep" />
                  <button className="danger" onClick={logout}>{ic.logout}Log out</button>
                </div>
              </>
            )}
          </div>
        </div>
      </aside>

      <div className="main">
        <div className="topbar">
          <button className="iconbtn" title="Toggle sidebar" aria-label="Toggle sidebar" onClick={toggleNav}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"><path d="M3.5 6h17M3.5 12h17M3.5 18h17" /></svg>
          </button>
          <button className="iconbtn" title="Search (Ctrl-K)" aria-label="Search" onClick={() => setSearchOpen(true)}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"><circle cx="11" cy="11" r="7" /><path d="M21 21l-4.3-4.3" /></svg>
          </button>
          <div><h1>{title}</h1><div className="sub">{sub}</div></div>
          <div className="summary">
            {chip('ok', ic.ok, 'var(--ok)', okN, 'OK')}
            {chip('warning', ic.warn, 'var(--warn)', warnN, 'Warnings')}
            {chip('error', ic.err, 'var(--err)', errN, 'Errors')}
            {chip('acked', ic.acked, 'var(--acked)', ackN, 'Acknowledged')}
            <span className="statdiv" />
            {chip('paused', ic.paused, 'var(--paused)', pausedN, 'Paused')}
            {chip('hidden', ic.hidden, 'var(--hidden)', hiddenN, 'Hidden')}
          </div>
        </div>
        <div className="content view-enter" key={`${view}:${listFilter}`}>
          {view === 'overview' && <StatusListView filter="attention" sensors={sensors} loading={!sensorsLoaded} canPause={canPause} goHost={goHost} goSensor={goSensor} onBack={() => {}} />}
          {view === 'triggers' && <TriggersView goHost={goHost} />}
          {view === 'list' && <StatusListView filter={listFilter} sensors={sensors} loading={!sensorsLoaded} canPause={canPause} goHost={goHost} goSensor={goSensor} onBack={() => goto('overview')} />}
          {view === 'monitoring' && <MonitoringView role={me.role} target={treeTarget} homeSignal={monHome} onNavigate={onTreeNav} advanced={!!me.advanced} />}
          {view === 'notifications' && <NotificationsView />}
          {view === 'probes' && <ProbesView role={me.role} enroll={probeEnroll} />}
          {view === 'users' && me.role === 'admin' && <UsersView />}
          {view === 'settings' && me.role === 'admin' && <SettingsView me={me} onMe={onMe} />}
          {view === 'account' && <AccountView me={me} onMe={onMe} passkeysAvailable={passkeysAvailable} theme={theme} toggleTheme={toggleTheme} />}
        </div>
      </div>
    </div>
  )
}

type SettingItem = {
  key: string; label: string; group: string; type: string; secret: boolean; min?: number; hint: string
  env: string; value: string; source: string; locked: boolean; has_value: boolean
}

function SettingsView({ me, onMe }: { me: Me; onMe: (m: Me) => void }) {
  const toast = useToast()
  const [items, setItems] = useState<SettingItem[] | null>(null)
  const [edits, setEdits] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [advBusy, setAdvBusy] = useState(false)
  const [zbx, setZbx] = useState<{ reachable: boolean; version?: string; error?: string } | null>(null)

  // Advanced mode is a per-user preference (saved on the admin's own account, like the landing page),
  // NOT a server-wide setting - enabling it never changes what anyone else sees.
  async function setAdvanced(next: boolean) {
    setAdvBusy(true)
    try {
      const res = await fetch('/api/me/preferences', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ advanced: next }) })
      if (!res.ok) { toast.error(await errText(res, 'Could not save preference')); return }
      onMe(await res.json()); toast.success(`Advanced mode ${next ? 'enabled' : 'disabled'}.`)
    } catch { toast.error('Could not save preference') } finally { setAdvBusy(false) }
  }

  function load() {
    fetch('/api/settings').then((r) => r.json()).then((s) => { setItems(s || []); setEdits({}) }).catch(() => toast.error('Failed to load settings'))
  }
  function checkHealth() {
    fetch('/api/health').then((r) => r.json()).then((h) => setZbx(h.zabbix)).catch(() => setZbx(null))
  }
  useEffect(() => { load(); checkHealth() }, [])

  const dirty = Object.keys(edits).length > 0
  const setEdit = (k: string, v: string) => setEdits((e) => ({ ...e, [k]: v }))

  async function save(e?: FormEvent) {
    e?.preventDefault(); setBusy(true)
    try {
      const res = await fetch('/api/settings', { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ values: edits }) })
      if (!res.ok) { toast.error(await errText(res, 'Could not save settings')); return }
      setItems(await res.json()); setEdits({}); toast.success('Settings saved and applied.'); checkHealth()
    } finally { setBusy(false) }
  }

  const field = (it: SettingItem) => {
    const editing = it.key in edits
    const cur = editing ? edits[it.key] : it.secret ? '' : it.value
    const ph = it.secret ? (it.has_value ? '•••••••• (unchanged)' : 'not set') : ''
    return (
      <label className="set-row" key={it.key}>
        <div className="set-head">
          <span className="flabel">{it.label}</span>
          {it.locked ? <span className="envpill" title={`Set via ${it.env}`}>via env</span>
            : it.source === 'default' && !editing ? <span className="set-src">default</span> : null}
        </div>
        <input
          className="input"
          type={it.secret ? 'password' : it.type === 'int' ? 'number' : 'text'}
          value={it.locked ? (it.secret ? '' : it.value) : cur}
          placeholder={it.locked && it.secret ? '•••••••• (managed by environment)' : ph}
          disabled={it.locked || busy}
          autoComplete={it.secret ? 'new-password' : 'off'}
          min={it.type === 'int' ? (it.min ?? 1) : undefined}
          onChange={(e) => setEdit(it.key, e.target.value)}
        />
        <span className="set-hint">{it.locked ? `Managed via ${it.env} - unset that variable to edit here.` : it.hint}</span>
      </label>
    )
  }

  const groups: { name: string; title: string; note?: string }[] = [
    { name: 'Connection', title: 'Zabbix connection', note: 'Where Argus reads monitoring data from.' },
    { name: 'General', title: 'General', note: 'Timezone and the external URL used in notification links.' },
    { name: 'Security', title: 'Login rate limiting', note: 'Brute-force protection thresholds.' },
    { name: 'Sessions', title: 'Sessions', note: 'How long a sign-in stays valid. Changes take effect immediately, including for existing sessions: lowering the max length can sign users out on their next request.' },
    { name: 'Probe enrollment', title: 'Probe enrollment', note: 'The address new probes are told to dial for the Zabbix server (:10051).' },
  ]

  return (
    <div className="panel">
      <div className="phead">
        <h2>Settings</h2>
        <span className="hint">Admin only</span>
        <div className="tools">
          <button className="btn primary" disabled={!dirty || busy} onClick={() => save()}>{busy ? 'Saving…' : 'Save changes'}</button>
        </div>
      </div>

      <form onSubmit={save} className="set-body">
        {/* Running version + update check, at the top so it's the first thing an admin sees. */}
        <VersionAbout />
        {/* OS patching & lifecycle (DESIGN §14c): core patch status + the operator-scheduled reboot window. */}
        <OSUpdates />
        {/* Advanced mode - a per-user preference (saved on this admin's account), kept here so only an
            admin can turn it on, and only for their own view. Theme is likewise a per-device preference,
            but lives in Account (reachable by every role) rather than this admin-only server-settings tab. */}
        <section className="set-card">
          <h3>Interface</h3>
          <p className="set-note">Personal to your account - other users aren't affected.</p>
          <div className="set-row set-toggle">
            <div className="set-head"><span className="flabel">Advanced mode</span></div>
            <Switch checked={!!me.advanced} disabled={advBusy} onChange={setAdvanced} label={me.advanced ? 'On' : 'Off'} />
            <span className="set-hint">Reveals power-user controls in the monitoring tree: the “All sensors” view and hidden-group management (hide groups / show hidden).</span>
          </div>
        </section>
        {items === null ? <Skeleton rows={4} cols={2} /> : groups.map((g) => {
          const gi = items.filter((it) => it.group === g.name)
          if (gi.length === 0) return null
          return (
            <section className="set-card" key={g.name}>
              <h3>{g.title}</h3>
              {g.note && <p className="set-note">{g.note}</p>}
              {g.name === 'Connection' && zbx && (
                <div className={'zbx-status ' + (zbx.reachable ? 'ok' : 'bad')}>
                  {zbx.reachable ? `Connected - Zabbix ${zbx.version}` : `Not reachable${zbx.error ? ': ' + zbx.error : ''}`}
                </div>
              )}
              {gi.map(field)}
            </section>
          )
        })}
        {/* Native submit so Enter works; the header button submits too. */}
        <button type="submit" style={{ display: 'none' }} aria-hidden />
      </form>
    </div>
  )
}

// SearchPalette is the Ctrl-K global quick-switcher: type to search hosts, sensors and groups;
// arrow keys + Enter to jump. Results come from GET /api/search (debounced, latest-wins).
const SEARCH_ICON: Record<SearchHit['type'], ReactNode> = {
  host: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><rect x="3" y="4" width="18" height="12" rx="2" /><path d="M8 20h8M12 16v4" /></svg>,
  sensor: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M3 12h4l2-6 4 12 2-6h6" /></svg>,
  group: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" /></svg>,
}
function SearchPalette({ onClose, onPick }: { onClose: () => void; onPick: (r: SearchHit) => void }) {
  const [q, setQ] = useState('')
  const [results, setResults] = useState<SearchHit[]>([])
  const [active, setActive] = useState(0)
  const [loading, setLoading] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => { inputRef.current?.focus() }, [])

  useEffect(() => {
    const term = q.trim()
    if (!term) { setResults([]); setLoading(false); return }
    setLoading(true)
    const ctrl = new AbortController()
    const t = setTimeout(async () => {
      try {
        const res = await fetch(`/api/search?q=${encodeURIComponent(term)}`, { signal: ctrl.signal })
        if (!res.ok) { setResults([]); return }
        const data: SearchHit[] = await res.json()
        setResults(data); setActive(0)
      } catch { /* aborted or network: ignore */ }
      finally { setLoading(false) }
    }, 180)
    return () => { clearTimeout(t); ctrl.abort() }
  }, [q])

  function onKey(e: ReactKeyboardEvent<HTMLInputElement>) {
    if (e.key === 'ArrowDown') { e.preventDefault(); setActive((i) => Math.min(i + 1, results.length - 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setActive((i) => Math.max(i - 1, 0)) }
    else if (e.key === 'Enter') { e.preventDefault(); if (results[active]) onPick(results[active]) }
    else if (e.key === 'Escape') { e.preventDefault(); onClose() }
  }

  const typeLabel: Record<SearchHit['type'], string> = { host: 'Host', sensor: 'Sensor', group: 'Group' }
  return (
    <div className="cmdk-overlay" onMouseDown={onClose}>
      <div className="cmdk" onMouseDown={(e) => e.stopPropagation()}>
        <div className="cmdk-head">
          <svg className="cmdk-search" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="11" cy="11" r="7" /><path d="M21 21l-4.3-4.3" /></svg>
          <input ref={inputRef} className="cmdk-input" placeholder="Search hosts, sensors, groups…" value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={onKey} />
          <kbd className="cmdk-esc">Esc</kbd>
        </div>
        {q.trim() && (
          <div className="cmdk-list">
            {results.map((r, i) => (
              <button
                key={r.type + (r.item_id || r.host_id || r.group || i)}
                className={'cmdk-item' + (i === active ? ' active' : '')}
                onMouseEnter={() => setActive(i)}
                onClick={() => onPick(r)}
              >
                <span className="cmdk-ic">{SEARCH_ICON[r.type]}</span>
                <span className="cmdk-label">{r.label}</span>
                {r.sub && <span className="cmdk-sub">{r.sub}</span>}
                <span className="cmdk-kind">{typeLabel[r.type]}</span>
              </button>
            ))}
            {!results.length && <div className="cmdk-empty">{loading ? 'Searching…' : 'No matches'}</div>}
          </div>
        )}
      </div>
    </div>
  )
}

const CH_META: Record<string, { c: string; l: string; label: string }> = {
  discord: { c: '#5865F2', l: 'D', label: 'Discord' },
  telegram: { c: '#229ED9', l: 'T', label: 'Telegram' },
  email: { c: '#6b7686', l: '@', label: 'Email' },
}
type ChField = { key: string; label: string; ph?: string; type?: string; opt?: boolean }
const CH_FIELDS: Record<string, ChField[]> = {
  discord: [{ key: 'webhook_url', label: 'Webhook URL', ph: 'https://discord.com/api/webhooks/…' }],
  telegram: [
    { key: 'bot_token', label: 'Bot token', ph: '123456:ABC-DEF…' },
    { key: 'chat_id', label: 'Chat ID', ph: '-1001234567890' },
    { key: 'thread_id', label: 'Topic ID', ph: 'forum topic, optional', opt: true },
  ],
  email: [
    { key: 'host', label: 'SMTP host', ph: 'smtp.example.com' },
    { key: 'port', label: 'Port', ph: '587' },
    { key: 'from', label: 'From address', ph: 'argus@example.com' },
    { key: 'to', label: 'To (comma-separated)', ph: 'you@example.com' },
    { key: 'username', label: 'Username', ph: 'optional', opt: true },
    { key: 'password', label: 'Password', ph: 'optional', type: 'password', opt: true },
  ],
}

// SitePicker is a multi-select for a channel's site scope: a compact dropdown that summarizes the
// selection and opens a scrollable, filterable, indented checklist of host-groups. Groups are
// '/'-hierarchical, so selecting a root (mybz) covers its subgroups (which then show as inherited).
// Empty selection ("All sites") means every site. Used by the admin and personal channel editors.
function SitePicker({ options, value, onChange }: { options: string[]; value: string[]; onChange: (v: string[]) => void }) {
  const [open, setOpen] = useState(false)
  const [q, setQ] = useState('')
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false) }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', onDoc)
    document.addEventListener('keydown', onKey)
    return () => { document.removeEventListener('mousedown', onDoc); document.removeEventListener('keydown', onKey) }
  }, [open])

  // Selecting a root (mybz) covers its subgroups (mybz/Network): coveredBy finds a selected ancestor,
  // and toggling a group drops any now-redundant descendants already in the selection.
  const coveredBy = (p: string) => value.find((s) => p.startsWith(s + '/'))
  const toggle = (p: string) => {
    if (value.includes(p)) { onChange(value.filter((x) => x !== p)); return }
    onChange(value.filter((x) => !x.startsWith(p + '/')).concat(p))
  }
  const all = value.length === 0
  const summary = all ? 'All sites' : value.length <= 2 ? value.join(', ') : `${value.length} sites selected`
  const needle = q.trim().toLowerCase()
  const shown = needle ? options.filter((o) => o.toLowerCase().includes(needle)) : options

  return (
    <div className="msel" ref={ref}>
      <button type="button" className="msel-btn" onClick={() => setOpen((o) => !o)} aria-haspopup="listbox" aria-expanded={open}>
        <span className="msel-sum">{summary}</span>
        <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 6l4 4 4-4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" /></svg>
      </button>
      {open && (
        <div className="msel-pop" role="listbox" aria-multiselectable="true">
          {options.length > 8 && (
            <input className="input msel-search" placeholder="Filter sites…" value={q} onChange={(e) => setQ(e.target.value)} autoFocus />
          )}
          <div className="msel-list">
            <button type="button" role="option" aria-selected={all} className={'msel-opt' + (all ? ' on' : '')} onClick={() => onChange([])}>
              <span className="msel-check">{all ? '✓' : ''}</span>All sites
            </button>
            {shown.map((s) => {
              const on = value.includes(s)
              const parent = on ? undefined : coveredBy(s)
              const covered = !!parent
              const depth = needle ? 0 : s.split('/').length - 1
              const label = needle ? s : s.slice(s.lastIndexOf('/') + 1)
              return (
                <button type="button" role="option" aria-selected={on || covered} key={s}
                  className={'msel-opt' + (on ? ' on' : '') + (covered ? ' covered' : '')}
                  style={{ paddingLeft: 9 + depth * 16 }} title={covered ? `Included via ${parent}` : s}
                  onClick={() => { if (!covered) toggle(s) }}>
                  <span className="msel-check">{on || covered ? '✓' : ''}</span>{label}
                </button>
              )
            })}
            {shown.length === 0 && <div className="msel-empty">No matching sites</div>}
          </div>
        </div>
      )}
    </div>
  )
}

// sitesLabel summarizes a channel's site scope for its card (empty = all sites).
function sitesLabel(sites?: string[]): string {
  if (!sites || sites.length === 0) return 'All sites'
  if (sites.length <= 2) return sites.join(', ')
  return `${sites.length} sites`
}

function NotificationsView() {
  const confirm = useConfirm()
  const toast = useToast()
  const [channels, setChannels] = useState<Channel[] | null>(null)
  const [sites, setSites] = useState<string[]>([])
  const [editing, setEditing] = useState<Channel | 'new' | null>(null)
  const [busy, setBusy] = useState<number | null>(null)

  function load() {
    fetch('/api/notify/channels').then((r) => r.json()).then((c) => setChannels(c || [])).catch(() => toast.error('Failed to load channels'))
  }
  useEffect(() => {
    load()
    fetch('/api/notify/sites').then((r) => r.json()).then((s) => setSites(s || [])).catch(() => {})
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  async function toggle(c: Channel) {
    const res = await fetch(`/api/notify/channels/${c.id}/enabled`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled: !c.enabled }) })
    if (!res.ok) { toast.error(await errText(res, 'Could not update channel')); return }
    load()
  }
  async function test(c: Channel) {
    setBusy(c.id)
    try {
      const res = await fetch(`/api/notify/channels/${c.id}/test`, { method: 'POST' })
      if (!res.ok) { toast.error(`Test failed for ${c.name}: ` + await errText(res, 'delivery error')); return }
      toast.success(`Test notification sent to ${c.name}.`)
    } finally { setBusy(null); load() }
  }
  async function del(c: Channel) {
    if (!(await confirm({ title: 'Delete channel', message: `Delete channel “${c.name}”? Alerts will stop routing here.`, confirmLabel: 'Delete', danger: true }))) return
    const res = await fetch(`/api/notify/channels/${c.id}`, { method: 'DELETE' })
    if (!res.ok) { toast.error(await errText(res, 'Could not delete channel')); return }
    toast.success(`Channel “${c.name}” deleted.`)
    load()
  }

  return (
    <div className="panel">
      <div className="phead">
        <h2>Notifications</h2>
        <span className="hint">{channels ? `${channels.length} channel${channels.length === 1 ? '' : 's'}` : '…'}</span>
        <div className="tools"><button className="btn primary" onClick={() => setEditing('new')}>+ Add channel</button></div>
      </div>
      <p className="panel-intro">
        Problems route to the channels below - globally or per site, each with its own severity floor (Warning by default). Acknowledged, paused and hidden items stay quiet; a recovery notice follows when things clear.
      </p>

      {editing && (
        <ChannelEditor
          initial={editing === 'new' ? null : editing}
          sites={sites}
          onCancel={() => setEditing(null)}
          onSaved={() => { setEditing(null); toast.success('Channel saved.'); load() }}
          onError={(m) => { if (m) toast.error(m) }}
        />
      )}

      {channels === null && <Skeleton rows={2} cols={3} />}
      {channels && channels.length === 0 && !editing && (
        <EmptyState icon={ic.notifications} title="No channels yet" text="Add a Discord webhook, a Telegram bot, or an email target to start receiving alerts."
          action={<Button variant="primary" onClick={() => setEditing('new')}>+ Add channel</Button>} />
      )}

      {channels && channels.length > 0 && (
        <div className="chan-grid">
          {channels.map((c) => {
            const m = CH_META[c.type] || { c: '#6b7686', l: '?', label: c.type }
            const sev = SEVERITIES.find((s) => s.v === c.min_severity)?.label || 'Warning & up'
            return (
              <div className={'chan' + (c.enabled ? '' : ' off')} key={c.id}>
                <div className="ct">
                  <span className="ci" style={{ background: m.c }}>{m.l}</span>
                  <span className="chan-name">{c.name}</span>
                  <Switch checked={c.enabled} onChange={() => toggle(c)} title={c.enabled ? 'Enabled — switch off to pause alerts to this channel' : 'Disabled — switch on to resume alerts'} />
                </div>
                <p className="chan-meta">{m.label} · {sitesLabel(c.sites)} · {sev}{c.type === 'email' && c.config?.recipients === 'users' ? ' · to all users' : ''}</p>
                <ChannelDelivery c={c} />
                <div className="chan-actions">
                  <Button disabled={busy === c.id} onClick={() => test(c)}>{busy === c.id ? 'Sending…' : 'Send test'}</Button>
                  <Kebab actions={[
                    { label: 'Edit…', icon: kbIcon.edit, onClick: () => setEditing(c) },
                    { sep: true, label: '' },
                    { label: 'Delete channel', icon: kbIcon.trash, danger: true, onClick: () => del(c) },
                  ]} />
                </div>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

// ChannelDelivery is the one-line health of a channel: when it last delivered, or the last failure and
// why. Recorded by the notifier per send (alerts and the Send test button alike).
function ChannelDelivery({ c }: { c: { last_sent_at?: number; last_error?: string; last_error_at?: number; sent_count?: number } }) {
  const failed = !!c.last_error_at && (!c.last_sent_at || c.last_error_at >= c.last_sent_at)
  if (failed) return <div className="chan-status err" title={c.last_error || ''}>Last delivery failed {relTime(c.last_error_at!)}{c.last_error ? ` · ${c.last_error}` : ''}</div>
  if (c.last_sent_at) return <div className="chan-status ok">Last sent {relTime(c.last_sent_at)}{c.sent_count ? ` · ${c.sent_count} delivered` : ''}</div>
  return <div className="chan-status">Nothing sent yet — use “Send test” to check the setup.</div>
}

function ChannelEditor({ initial, sites, onCancel, onSaved, onError }: {
  initial: Channel | null; sites: string[]; onCancel: () => void; onSaved: () => void; onError: (m: string) => void
}) {
  const [type, setType] = useState(initial?.type || 'discord')
  const [name, setName] = useState(initial?.name || '')
  const [selSites, setSelSites] = useState<string[]>(initial?.sites || [])
  const [minSev, setMinSev] = useState(initial?.min_severity || 2)
  const [enabled, setEnabled] = useState(initial ? initial.enabled : true)
  const [config, setConfig] = useState<Record<string, string>>(initial?.config || {})
  const setCfg = (k: string, v: string) => setConfig((c) => ({ ...c, [k]: v }))

  async function save(e: FormEvent) {
    e.preventDefault(); onError('')
    const body = { type, name, sites: selSites, min_severity: minSev, enabled, config }
    const url = initial ? `/api/notify/channels/${initial.id}` : '/api/notify/channels'
    const res = await fetch(url, { method: initial ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
    if (!res.ok) { onError(await errText(res, 'Could not save channel')); return }
    onSaved()
  }

  const fields = CH_FIELDS[type] || []
  return (
    <form onSubmit={save} style={{ padding: '14px 16px', borderBottom: '1px solid var(--border)', background: 'var(--elevated)', display: 'grid', gap: 12 }}>
      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', alignItems: 'flex-end' }}>
        <label style={{ display: 'grid', gap: 4 }}><span className="flabel">Type</span>
          <Select value={type} onChange={(e) => setType(e.target.value)} disabled={!!initial}>
            {Object.keys(CH_META).map((t) => <option key={t} value={t}>{CH_META[t].label}</option>)}
          </Select>
        </label>
        <label style={{ display: 'grid', gap: 4, flex: 1, minWidth: 160 }}><span className="flabel">Name</span>
          <input className="input" placeholder="e.g. Discord - site1" value={name} onChange={(e) => setName(e.target.value)} required />
        </label>
        <label style={{ display: 'grid', gap: 4 }}><span className="flabel">Severity</span>
          <Select value={minSev} onChange={(e) => setMinSev(Number(e.target.value))} title="Only problems at or above this severity reach this channel">
            {SEVERITIES.map((s) => <option key={s.v} value={s.v}>{s.label}</option>)}
          </Select>
        </label>
      </div>
      <div style={{ display: 'grid', gap: 6 }}><span className="flabel">Sites</span>
        <SitePicker options={sites} value={selSites} onChange={setSelSites} />
      </div>
      <div style={{ display: 'grid', gap: 10, gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))' }}>
        {type === 'email' && (
          <label style={{ display: 'grid', gap: 4 }}><span className="flabel">Send to</span>
            <Select value={config.recipients || 'fixed'} onChange={(e) => setCfg('recipients', e.target.value)}>
              <option value="fixed">A fixed address</option>
              <option value="users">Each user’s registered email</option>
            </Select>
          </label>
        )}
        {fields.filter((f) => !(type === 'email' && f.key === 'to' && (config.recipients || 'fixed') === 'users')).map((f) => (
          <label key={f.key} style={{ display: 'grid', gap: 4 }}><span className="flabel">{f.label}</span>
            <input className="input" type={f.type || 'text'} placeholder={f.ph} value={config[f.key] || ''} onChange={(e) => setCfg(f.key, e.target.value)} required={!f.opt} />
          </label>
        ))}
        {type === 'email' && (
          <label style={{ display: 'grid', gap: 4 }}><span className="flabel">Encryption</span>
            <Select value={config.tls || 'starttls'} onChange={(e) => setCfg('tls', e.target.value)}>
              <option value="starttls">STARTTLS (587)</option>
              <option value="tls">Implicit TLS (465)</option>
              <option value="none">None</option>
            </Select>
          </label>
        )}
      </div>
      {type === 'email' && (config.recipients || 'fixed') === 'users' && (
        <p className="set-note">Sends to every active user’s account email. The “To” field is ignored.</p>
      )}
      <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
        <Switch checked={enabled} onChange={setEnabled} label="Enabled" />
        <div style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
          <button type="button" className="btn" onClick={onCancel}>Cancel</button>
          <button type="submit" className="btn primary">{initial ? 'Save changes' : 'Add channel'}</button>
        </div>
      </div>
    </form>
  )
}

type UserChannel = { id: number; type: string; enabled: boolean; sites: string[]; min_severity: number; config: Record<string, string>; last_sent_at?: number; last_error?: string; last_error_at?: number; sent_count?: number }

// PersonalNotifyCard lets any signed-in user manage their own Telegram/Discord alert destinations,
// separate from the shared channels an admin configures in the Notifications tab. Self-service:
// everything here hits /api/me/notify/* and only ever touches the caller's own channels.
function PersonalNotifyCard() {
  const toast = useToast()
  const confirm = useConfirm()
  const [channels, setChannels] = useState<UserChannel[] | null>(null)
  const [sites, setSites] = useState<string[]>([])
  const [editing, setEditing] = useState<UserChannel | 'new' | null>(null)
  const [busy, setBusy] = useState<number | null>(null)

  function load() {
    fetch('/api/me/notify/channels').then((r) => r.json()).then((c) => setChannels(c || [])).catch(() => toast.error('Failed to load your channels'))
  }
  useEffect(() => {
    load()
    fetch('/api/me/notify/sites').then((r) => r.json()).then((s) => setSites(s || [])).catch(() => {})
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  async function toggle(c: UserChannel) {
    const res = await fetch(`/api/me/notify/channels/${c.id}/enabled`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled: !c.enabled }) })
    if (!res.ok) { toast.error(await errText(res, 'Could not update channel')); return }
    load()
  }
  async function test(c: UserChannel) {
    setBusy(c.id)
    try {
      const res = await fetch(`/api/me/notify/channels/${c.id}/test`, { method: 'POST' })
      if (!res.ok) { toast.error('Test failed: ' + await errText(res, 'delivery error')); return }
      toast.success('Test notification sent.')
    } finally { setBusy(null); load() }
  }
  async function del(c: UserChannel) {
    if (!(await confirm({ title: 'Remove channel', message: `Stop sending your alerts to this ${CH_META[c.type]?.label || c.type} destination?`, confirmLabel: 'Remove', danger: true }))) return
    const res = await fetch(`/api/me/notify/channels/${c.id}`, { method: 'DELETE' })
    if (!res.ok) { toast.error(await errText(res, 'Could not remove channel')); return }
    toast.success('Channel removed.')
    load()
  }

  return (
    <Card title="Personal notifications" note="Get alerts on your own Telegram or Discord. Only you receive these — they’re separate from the shared channels an admin manages.">
      {editing && (
        <PersonalChannelEditor
          initial={editing === 'new' ? null : editing}
          sites={sites}
          onCancel={() => setEditing(null)}
          onSaved={() => { setEditing(null); toast.success('Channel saved.'); load() }}
          onError={(m) => { if (m) toast.error(m) }}
        />
      )}
      {channels === null && <Skeleton rows={1} cols={2} />}
      {channels && channels.length === 0 && !editing && (
        <p className="set-note">No personal channels yet. Add your Telegram or Discord to get your own alerts.</p>
      )}
      {channels && channels.length > 0 && (
        <div className="chan-grid">
          {channels.map((c) => {
            const m = CH_META[c.type] || { c: '#6b7686', l: '?', label: c.type }
            const sev = SEVERITIES.find((s) => s.v === c.min_severity)?.label || 'Warning & up'
            return (
              <div className={'chan' + (c.enabled ? '' : ' off')} key={c.id}>
                <div className="ct">
                  <span className="ci" style={{ background: m.c }}>{m.l}</span>
                  <span className="chan-name">{m.label}</span>
                  <Switch checked={c.enabled} onChange={() => toggle(c)} title={c.enabled ? 'Enabled — switch off to pause your alerts here' : 'Disabled — switch on to resume'} />
                </div>
                <p className="chan-meta">{sitesLabel(c.sites)} · {sev}</p>
                <ChannelDelivery c={c} />
                <div className="chan-actions">
                  <Button disabled={busy === c.id} onClick={() => test(c)}>{busy === c.id ? 'Sending…' : 'Send test'}</Button>
                  <Kebab actions={[
                    { label: 'Edit…', icon: kbIcon.edit, onClick: () => setEditing(c) },
                    { sep: true, label: '' },
                    { label: 'Remove', icon: kbIcon.trash, danger: true, onClick: () => del(c) },
                  ]} />
                </div>
              </div>
            )
          })}
        </div>
      )}
      {!editing && (
        <div style={{ marginTop: 12 }}>
          <Button variant="primary" onClick={() => setEditing('new')}>+ Add channel</Button>
        </div>
      )}
    </Card>
  )
}

function PersonalChannelEditor({ initial, sites, onCancel, onSaved, onError }: {
  initial: UserChannel | null; sites: string[]; onCancel: () => void; onSaved: () => void; onError: (m: string) => void
}) {
  const [type, setType] = useState(initial?.type || 'telegram')
  const [selSites, setSelSites] = useState<string[]>(initial?.sites || [])
  const [minSev, setMinSev] = useState(initial?.min_severity || 2)
  const [enabled, setEnabled] = useState(initial ? initial.enabled : true)
  const [config, setConfig] = useState<Record<string, string>>(initial?.config || {})
  const setCfg = (k: string, v: string) => setConfig((c) => ({ ...c, [k]: v }))

  async function save(e: FormEvent) {
    e.preventDefault(); onError('')
    const body = { type, sites: selSites, min_severity: minSev, enabled, config }
    const url = initial ? `/api/me/notify/channels/${initial.id}` : '/api/me/notify/channels'
    const res = await fetch(url, { method: initial ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
    if (!res.ok) { onError(await errText(res, 'Could not save channel')); return }
    onSaved()
  }

  const fields = CH_FIELDS[type] || []
  const hint = type === 'telegram'
    ? 'Create your own bot with @BotFather for the token, and message the bot once so it’s allowed to reach you.'
    : 'Paste a Discord channel webhook URL (Server Settings → Integrations → Webhooks).'
  return (
    <form onSubmit={save} style={{ display: 'grid', gap: 10, marginBottom: 12, paddingBottom: 12, borderBottom: '1px solid var(--border)' }}>
      <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'flex-end' }}>
        <label style={{ display: 'grid', gap: 4 }}><span className="flabel">Type</span>
          <Select value={type} onChange={(e) => setType(e.target.value)} disabled={!!initial}>
            <option value="telegram">Telegram</option>
            <option value="discord">Discord</option>
          </Select>
        </label>
        <label style={{ display: 'grid', gap: 4 }}><span className="flabel">Severity</span>
          <Select value={minSev} onChange={(e) => setMinSev(Number(e.target.value))} title="Only problems at or above this severity reach you">
            {SEVERITIES.map((s) => <option key={s.v} value={s.v}>{s.label}</option>)}
          </Select>
        </label>
      </div>
      <div style={{ display: 'grid', gap: 6 }}><span className="flabel">Sites</span>
        <SitePicker options={sites} value={selSites} onChange={setSelSites} />
      </div>
      <div style={{ display: 'grid', gap: 10, gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))' }}>
        {fields.map((f) => (
          <label key={f.key} style={{ display: 'grid', gap: 4 }}><span className="flabel">{f.label}</span>
            <input className="input" type={f.type || 'text'} placeholder={f.ph} value={config[f.key] || ''} onChange={(e) => setCfg(f.key, e.target.value)} required={!f.opt} />
          </label>
        ))}
      </div>
      <p className="set-note">{hint}</p>
      <div style={{ display: 'flex', gap: 10, alignItems: 'center' }}>
        <Switch checked={enabled} onChange={setEnabled} label="Enabled" />
        <div style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
          <button type="button" className="btn" onClick={onCancel}>Cancel</button>
          <button type="submit" className="btn primary">{initial ? 'Save changes' : 'Add channel'}</button>
        </div>
      </div>
    </form>
  )
}

const PROBE_IMAGE = 'ghcr.io/g-guglielmi/argus-probe:latest'
const UPDATER_IMAGE = 'ghcr.io/g-guglielmi/argus-updater:latest'

function probeDockerCmd(c: CreatedToken, redeploy: boolean, selfupdate: boolean): string {
  const name = `argus-${c.proxy_name}`
  const lines: string[] = []
  // On a redeploy (a container by this name already exists), remove it first so the fresh `docker
  // run` doesn't collide on the name. The data volume is a host bind mount, so `docker rm` never
  // touches it - the enrolled certs persist and the new container skips enrollment.
  if (redeploy) lines.push(`docker rm -f ${name} ${name}-updater`)
  // The proxy container: a pure reporter, never gets the Docker socket.
  lines.push(
    `docker run -d --name ${name} --restart unless-stopped \\`,
    `  -v /docker/${name}:/var/lib/zabbix \\`,
    `  -v /docker/${name}/snmptraps:/var/lib/zabbix/snmptraps \\`,
    `  -e ARGUS_ENROLL_URL=${c.enroll_url} \\`,
    `  -e ARGUS_ENROLL_TOKEN=${c.token} \\`,
  )
  if (!c.core_host) lines.push('  -e ZBX_SERVER_HOST=<core-host-or-ip:reachable-on-10051> \\')
  lines.push(`  ${PROBE_IMAGE}`)
  // The argus-updater sidecar: the ONLY container with the socket. It recreates the proxy via the
  // Docker Engine API when Argus signals an update - so the proxy stays socket-free.
  if (selfupdate) lines.push(
    '',
    `docker run -d --name ${name}-updater --restart unless-stopped \\`,
    `  -v /var/run/docker.sock:/var/run/docker.sock \\`,
    `  -v /docker/${name}:/probe:ro \\`,
    `  -e ARGUS_UPDATER_MODE=probe-watch \\`,
    `  -e ARGUS_PROXY_CONTAINER=${name} \\`,
    `  ${UPDATER_IMAGE}`,
  )
  return lines.join('\n')
}

// probeComposeCmd emits a paste-once script that writes a .env, fetches the compose file, and
// brings up the proxy + the opt-in self-updater sidecar (Argus-coordinated auto-update).
function probeComposeCmd(c: CreatedToken): string {
  const dir = `argus-${c.proxy_name}`
  const envLines = [
    `ARGUS_PROXY_NAME=${c.proxy_name}`,
    `ARGUS_ENROLL_URL=${c.enroll_url}`,
    `ARGUS_ENROLL_TOKEN=${c.token}`,
    'ARGUS_PROBE_TAG=latest',
  ]
  if (!c.core_host) envLines.push('ZBX_SERVER_HOST=<core-host-or-ip:reachable-on-10051>')
  return [
    `mkdir -p ${dir} && cd ${dir}`,
    "cat > .env <<'EOF'",
    ...envLines,
    'EOF',
    'curl -fsSL https://raw.githubusercontent.com/g-guglielmi/argus-probe/main/deploy/probe-image/docker-compose.yml -o docker-compose.yml',
    'docker compose up -d',
  ].join('\n')
}

function ProbesView({ role, enroll }: { role: string; enroll: boolean }) {
  const confirm = useConfirm()
  const alert = useAlert()
  const [proxies, setProxies] = useState<Proxy[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tokens, setTokens] = useState<EnrollTokenRow[] | null>(null)
  const [wizardOpen, setWizardOpen] = useState(false)
  const [target, setTarget] = useState<string | null>(null)
  const [openCmd, setOpenCmd] = useState<string | null>(null) // proxy name whose update command is expanded
  const [queued, setQueued] = useState<Record<string, string>>({}) // proxy name -> queued self-update tag
  const [report, setReport] = useState<{ name: string; token: string } | null>(null) // minted check-in token to show
  const [openSnmp, setOpenSnmp] = useState<string | null>(null) // proxy name whose SNMP-defaults band is open
  const canEdit = role === 'admin' || role === 'helpdesk'
  const isAdmin = role === 'admin'

  async function triggerUpdate(p: Proxy) {
    try {
      const res = await fetch(`/api/probes/${encodeURIComponent(p.name)}/update`, { method: 'POST' })
      if (!res.ok) { alert({ title: 'Update', message: await errText(res, 'Could not queue the update'), danger: true }); return }
      const d = await res.json()
      setQueued((q) => ({ ...q, [p.name]: d.tag || 'target' }))
    } catch { alert({ title: 'Update', message: 'Could not queue the update', danger: true }) }
  }

  // Update the argus-updater sidecar itself. It recreates itself (via an ephemeral probe-recreate
  // copy) onto the latest argus-updater image at its next check-in.
  async function triggerUpdaterUpdate(p: Proxy) {
    if (!(await confirm({ title: 'Update the updater', message: `Recreate the argus-updater sidecar managing ${p.name} onto the latest version? It rolls back if the new one fails.`, confirmLabel: 'Update updater' }))) return
    try {
      const res = await fetch(`/api/probes/${encodeURIComponent(p.name)}/updater-update`, { method: 'POST' })
      if (!res.ok) { alert({ title: 'Update updater', message: await errText(res, 'Could not queue the updater update'), danger: true }); return }
      alert({ title: 'Update updater', message: 'Queued - the sidecar recreates itself on its next check-in (within ~5 min).' })
    } catch { alert({ title: 'Update updater', message: 'Could not queue the updater update', danger: true }) }
  }

  // Reveal a probe VM's break-glass console credential (admin). Fetched on demand - it's never part
  // of the /api/proxies list - and shown in an in-app dialog with copy buttons.
  async function revealBreakGlass(p: Proxy) {
    try {
      const res = await fetch(`/api/probes/${encodeURIComponent(p.name)}/break-glass`)
      if (!res.ok) { alert({ title: 'Console access', message: await errText(res, 'Could not read the credential'), danger: true }); return }
      const d = await res.json()
      alert({
        title: `Console access — ${p.name}`,
        message: (
          <div>
            <p style={{ margin: '0 0 12px', color: 'var(--muted)', fontSize: 13 }}>Break-glass login for the hypervisor console (or SSH over the VPN):</p>
            <div style={{ display: 'grid', gridTemplateColumns: 'auto 1fr auto', gap: '8px 10px', alignItems: 'center' }}>
              <span style={{ color: 'var(--muted)' }}>Username</span><span className="mono">{d.username}</span><CopyButton text={d.username} />
              <span style={{ color: 'var(--muted)' }}>Password</span><span className="mono" style={{ wordBreak: 'break-all' }}>{d.password}</span><CopyButton text={d.password} />
            </div>
          </div>
        ),
      })
    } catch { alert({ title: 'Console access', message: 'Could not read the credential', danger: true }) }
  }

  // Mint a check-in credential for a probe that predates fleet updates; shown once for the operator
  // to drop into the container as ARGUS_PROBE_TOKEN (GUI), turning on version reporting.
  async function enableReporting(p: Proxy) {
    try {
      const res = await fetch(`/api/probes/${encodeURIComponent(p.name)}/checkin-token`, { method: 'POST' })
      if (!res.ok) { alert({ title: 'Check-in token', message: await errText(res, 'Could not issue a check-in token'), danger: true }); return }
      const d = await res.json()
      setReport({ name: p.name, token: d.token })
    } catch { alert({ title: 'Check-in token', message: 'Could not issue a check-in token', danger: true }) }
  }

  const loadProxies = () => fetch('/api/proxies')
    .then(async (r) => { if (!r.ok) throw new Error(await errText(r, 'Failed to load probes')); return r.json() })
    .then((p: Proxy[]) => { setProxies(p || []); setError(null); if (p && p.length) setTarget(p[0].target ?? 'latest') })
    .catch((e) => setError(e instanceof Error ? e.message : 'Failed to load probes'))
  useEffect(() => { loadProxies(); const t = setInterval(loadProxies, 30000); return () => clearInterval(t) }, []) // eslint-disable-line react-hooks/exhaustive-deps

  function loadTokens() {
    if (!isAdmin || !enroll) return
    fetch('/api/probes/tokens').then((r) => (r.ok ? r.json() : [])).then((t) => setTokens(t || [])).catch(() => {})
  }
  useEffect(() => { loadTokens() }, [isAdmin, enroll]) // eslint-disable-line react-hooks/exhaustive-deps

  async function revoke(t: EnrollTokenRow) {
    if (!(await confirm({ title: 'Revoke token', message: `Revoke the enrollment token for ${t.proxy_name}?`, confirmLabel: 'Revoke', danger: true }))) return
    await fetch(`/api/probes/tokens/${t.id}`, { method: 'DELETE' }).catch(() => {})
    loadTokens()
  }

  // Delete a proxy from Zabbix and clean up its Argus-side records. Zabbix refuses if hosts still
  // reference it - that error is surfaced.
  async function del(p: Proxy) {
    if (!(await confirm({ title: 'Delete probe', message: `Remove “${p.name}” from Zabbix and delete its Argus records (enrollment tokens, check-in state, SNMP default)? Zabbix won't allow this while hosts are still monitored by it. Its host group is left in place.`, confirmLabel: 'Delete', danger: true }))) return
    const res = await fetch(`/api/proxies/${encodeURIComponent(p.id)}`, { method: 'DELETE' })
    if (!res.ok) { alert({ title: 'Delete probe', message: await errText(res, 'Could not delete the proxy'), danger: true }); return }
    setProxies((ps) => (ps || []).filter((x) => x.id !== p.id))
    loadTokens()
  }

  // Prune Argus records orphaned by proxies deleted directly in Zabbix (out of band).
  async function reconcile() {
    const res = await fetch('/api/proxies/reconcile', { method: 'POST' })
    if (!res.ok) { alert({ title: 'Clean up', message: await errText(res, 'Cleanup failed'), danger: true }); return }
    const d = await res.json()
    alert({ title: 'Clean up', message: d.pruned > 0 ? `Removed ${d.pruned} orphaned record${d.pruned === 1 ? '' : 's'} left by proxies deleted in Zabbix.` : 'No orphaned records — everything is in sync with Zabbix.' })
  }

  // Enrolled tokens are just noise once a probe is live (its enrollment date shows in the row
  // below), so the list keeps only what's still actionable: pending and expired tokens.
  const pendingTokens = (tokens || []).filter((t) => t.status !== 'enrolled')

  return (
    <div className="panel">
      <div className="phead">
        <h2>Site probes</h2>
        <span className="hint">{proxies ? `${proxies.length} known to the core` : '…'}</span>
        {(() => {
          const needReboot = (proxies || []).filter((p) => p.reboot_required).length
          return needReboot > 0 ? <span className="tag avail" title="These probe VMs need a reboot to finish applying OS updates; each reboots in its weekly ~03:00 window">{needReboot} need a reboot</span> : null
        })()}
        {isAdmin && <div className="tools">
          <button className="btn" onClick={reconcile} title="Prune Argus records left behind by probes deleted directly in Zabbix">Clean up</button>
          {enroll && <button className="btn primary" onClick={() => setWizardOpen(true)}>+ Add probe</button>}
        </div>}
      </div>

      {isAdmin && !enroll && (
        <p style={{ color: 'var(--muted)', fontSize: 12.5, padding: '2px 16px 0', margin: 0 }}>
          One-click enrollment is off. Mount the monitoring CA into Argus and set <code>ARGUS_CA_CERT_FILE</code> / <code>ARGUS_CA_KEY_FILE</code> (and <code>ARGUS_PROBE_CORE_HOST</code>) to enable it. Live probe status still works below.
        </p>
      )}

      {isAdmin && enroll && wizardOpen && <AddProbeWizard existingNames={(proxies || []).map((p) => p.name)} onClose={() => { setWizardOpen(false); loadTokens(); loadProxies() }} onEnrolled={() => { loadTokens(); loadProxies() }} />}

      {isAdmin && enroll && pendingTokens.length > 0 && (
        <table className="enroll">
          <thead><tr><th>Pending enrollments</th><th>Status</th><th>Expires</th><th></th></tr></thead>
          <tbody>
            {pendingTokens.map((t) => (
              <tr key={t.id}>
                <td><strong>{t.proxy_name}</strong></td>
                <td data-label="Status"><span className="tag pending">{t.status}</span></td>
                <td data-label="Expires" className="mono" style={{ color: 'var(--muted)' }}>{relTime(t.expires_at)}</td>
                <td style={{ textAlign: 'right' }}><button className="btn danger" onClick={() => revoke(t)}>{t.status === 'pending' ? 'Revoke' : 'Remove'}</button></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {isAdmin && <FleetTarget target={target} latest={(proxies || []).find((p) => p.latest)?.latest} onSaved={setTarget} />}

      <div className="enroll-scroll">
      <table className="enroll enroll-probes">
        <thead><tr><th>Probe</th><th>Health</th><th>Proxy Version</th><th>Updater Version</th><th>VM OS Version</th><th></th></tr></thead>
        <tbody>
          {error && <tr><td colSpan={6} style={{ color: 'var(--err)' }}>{error}</td></tr>}
          {!error && proxies === null && <tr><td colSpan={6} style={{ padding: 0 }}><div style={{ flex: 1, width: '100%' }}><Skeleton rows={3} cols={5} /></div></td></tr>}
          {!error && proxies && proxies.length === 0 && <tr><td colSpan={6} style={{ padding: 0 }}><div style={{ flex: 1, width: '100%' }}>
            <EmptyState icon={ic.probes} title="No probes yet" text="A probe appears here once it enrolls and checks in with the core." action={isAdmin && enroll ? <Button variant="primary" onClick={() => setWizardOpen(true)}>+ Add probe</Button> : undefined} />
          </div></td></tr>}
          {!error && proxies && proxies.map((p) => (
            <Fragment key={p.name}>
              <tr>
                <td data-label="Probe">
                  <div className="cell-stack">
                    <strong>{p.name}</strong>
                    <span className="sub-line" title={p.enrolled_at ? 'Self-enrolled via Argus' : 'No Argus enrollment on record (manually registered)'}>
                      {p.mode}{p.enrolled_at ? ` · enrolled ${new Date(p.enrolled_at * 1000).toLocaleDateString()}` : ' · manual'}
                    </span>
                  </div>
                </td>
                <td data-label="Health">
                  <div className="cell-stack">
                    {p.online ? <span className="tag online">● online</span> : <span className="tag pending">offline</span>}
                    <span className="sub-line mono" title="When the core last received data from this probe" style={{ paddingLeft: 10, color: !p.last_access ? 'var(--faint)' : (Date.now() / 1000 - p.last_access > 60 ? 'var(--warn)' : undefined) }}>{p.last_access ? relTime(p.last_access) : 'never'}</span>
                  </div>
                </td>
                <td data-label="Proxy Version">
                  <span className="vcell">
                    <span className="mono" style={{ fontWeight: 600, color: p.version ? undefined : 'var(--faint)' }} title="Zabbix proxy version running on this probe">{p.version || '-'}</span>
                    <UpdateBadge p={p} open={openCmd === p.name} onToggle={() => setOpenCmd((n) => (n === p.name ? null : p.name))} queuedTag={queued[p.name]} onSelfUpdate={triggerUpdate} canReport={isAdmin && !p.last_checkin} onEnableReporting={enableReporting} hideAuto />
                  </span>
                </td>
                <td data-label="Updater Version"><UpdaterVersionCell p={p} onUpdate={isAdmin ? triggerUpdaterUpdate : undefined} /></td>
                <td data-label="VM OS Version"><OSCell p={p} /></td>
                <td className="row-actions">
                  <ProbeRowMenu items={[
                    canEdit && p.id ? { label: 'SNMP defaults', onClick: () => setOpenSnmp((n) => (n === p.name ? null : p.name)) } : null,
                    isAdmin && p.break_glass ? { label: p.break_glass_user ? `Console (${p.break_glass_user})` : 'Console', onClick: () => revealBreakGlass(p) } : null,
                    isAdmin && p.id ? 'sep' : null,
                    isAdmin && p.id ? { label: 'Delete probe', onClick: () => del(p), danger: true } : null,
                  ]} />
                </td>
              </tr>
              {openCmd === p.name && <tr><td colSpan={6} style={{ padding: 0 }}><ProbeUpdateCommand p={p} /></td></tr>}
              {report?.name === p.name && <tr><td colSpan={6} style={{ padding: 0 }}><ReportTokenPanel token={report.token} name={p.name} onDone={() => setReport(null)} /></td></tr>}
              {openSnmp === p.name && <tr><td colSpan={6} style={{ padding: 0 }}><ProxySNMP proxyId={p.id} proxyName={p.name} onClose={() => setOpenSnmp(null)} /></td></tr>}
            </Fragment>
          ))}
        </tbody>
      </table>
      </div>
    </div>
  )
}

// probeUpdateTag maps a fleet target to the pullable image tag for a manual update.
function probeUpdateTag(target?: string): string {
  return target && target !== 'latest' ? target : 'latest'
}

// UpdateBadge shows a probe's state versus the fleet target, and (for drift) a toggle that reveals
// the one-click manual update command.
function UpdateBadge({ p, open, onToggle, queuedTag, onSelfUpdate, canReport, onEnableReporting, hideAuto }: { p: Proxy; open: boolean; onToggle: () => void; queuedTag?: string; onSelfUpdate: (p: Proxy) => void; canReport?: boolean; onEnableReporting: (p: Proxy) => void; hideAuto?: boolean }) {
  // One shared row: the button, the "→ version" chip and the auto tag stay on a single line so the
  // Update column reports an honest one-line width to the auto-sized table (a wrapping cell would
  // collapse to its widest item and let the column starve). The table's scroll wrapper handles the
  // rare too-narrow desktop instead of wrapping mid-cell.
  const wrap: React.CSSProperties = { display: 'inline-flex', alignItems: 'center', gap: 6, whiteSpace: 'nowrap' }
  // "auto" = an argus-updater sidecar manages this probe (Argus can drive updates). The sidecar's own
  // version + its self-update control live in the separate "Updater" column.
  const auto = p.selfupdate && !hideAuto ? <span className="tag" title="Managed by an argus-updater sidecar; Argus can trigger updates from here">auto</span> : null
  if (queuedTag) return <span className="tag" title={`Update to ${queuedTag} queued - the probe applies it on its next check-in (within ~5 min)`}>update queued</span>
  // A socket-enabled probe updates itself when triggered; otherwise we expand the manual command.
  const selfBtn = <button className="btn" onClick={() => onSelfUpdate(p)} title="Tell the probe to update itself to the fleet target on its next check-in">Update now</button>
  // Probes that don't check in (external/unknown) can be turned on with a minted token.
  const reportBtn = canReport
    ? <button className="btn" onClick={() => onEnableReporting(p)} title="Issue a check-in token so this probe reports its exact version to Argus">Enable reporting</button>
    : <span className="mono" style={{ color: 'var(--faint)' }} title="This probe isn't reporting its exact version to Argus (updates handled outside Argus, e.g. unRAID).">-</span>
  switch (p.update_status) {
    case 'current':
      return <span style={wrap}><span className="okquiet" title="Running the fleet target version">up to date</span>{auto}</span>
    case 'tracking':
      return <span style={wrap}><span className="tag" title="Fleet target is 'latest'; the probe converges on the newest image">tracking latest</span>{p.selfupdate ? selfBtn : null}{auto}</span>
    case 'outdated': {
      const avail = p.target === 'latest' ? p.latest : p.target
      return <span style={wrap}>{p.selfupdate ? selfBtn : <button className="btn avail" onClick={onToggle}>{open ? 'Hide' : 'Update…'}</button>}{avail ? <span className="tag avail" title="Update available">→ {avail}</span> : null}{auto}</span>
    }
    case 'external':
    default:
      return reportBtn
  }
}

// OSCell is the "OS" column: a VM probe's Debian patch status (DESIGN §14c). The OS patches itself
// (unattended-upgrades, security only) and auto-reboots in a weekly window; this only *reports*. A
// dash means no report (a container probe, or a VM that hasn't reported yet).
function OSCell({ p }: { p: Proxy }) {
  if (!p.os_reported_at) return <span className="mono" style={{ color: 'var(--faint)' }} title="No OS patch report — a container probe, or a VM probe that hasn't reported yet">-</span>
  const when = `Reported ${relTime(p.os_reported_at)}`
  const sec = typeof p.sec_updates === 'number' ? p.sec_updates : -1
  // The reporter sends the full PRETTY_NAME (e.g. "Debian GNU/Linux 13 (trixie)"); trim the "GNU/Linux"
  // filler so the cell reads "Debian 13 (trixie)".
  const osName = (p.os_version || '').replace('GNU/Linux ', '')
  const chip = p.reboot_required
    ? <span className="tag avail" title={`This VM needs a reboot to finish applying updates; it reboots in its weekly ~03:00 window. ${when}`}>reboot</span>
    : sec > 0 ? <span className="tag avail" title={`${sec} pending security update${sec === 1 ? '' : 's'}; applied automatically (security suite only). ${when}`}>{sec} security</span>
    : sec === 0 ? <span className="okquiet" title={`No pending security updates. ${when}`}>patched</span>
    : <span className="mono" style={{ color: 'var(--faint)' }} title={`Security-update count unknown. ${when}`}>?</span>
  return (
    <span className="vcell">
      {osName && <span className="mono" style={{ fontWeight: 600 }} title="Operating system reported by the VM">{osName}</span>}
      {chip}
    </span>
  )
}

// UpdaterVersionCell is the "Argus-Updater Version" column: the argus-updater sidecar's version, its
// drift vs the newest published updater, and (admin) an Update button when it's behind — the same shape
// as the proxy-version cell. A dash means no sidecar manages this probe (e.g. an unRAID-native probe).
function UpdaterVersionCell({ p, onUpdate }: { p: Proxy; onUpdate?: (p: Proxy) => void }) {
  if (!p.selfupdate) return <span className="mono" style={{ color: 'var(--faint)' }} title="No argus-updater sidecar manages this probe">-</span>
  const ver = <span className="mono" style={{ fontWeight: 600 }} title="Version of the argus-updater sidecar managing this probe">{p.updater_version || '?'}</span>
  const updateBtn = onUpdate ? <button className="btn" onClick={() => onUpdate(p)} title="Update the argus-updater sidecar to the newest version (it recreates itself)">Update</button> : null
  switch (p.updater_status) {
    case 'current':
      return <span className="vcell">{ver}<span className="okquiet" title="Running the newest published argus-updater">up to date</span></span>
    case 'outdated':
      return <span className="vcell">{ver}{p.updater_latest ? <span className="tag avail" title="A newer argus-updater has been published">→ {p.updater_latest}</span> : null}{updateBtn}</span>
    default: // unknown: version reported but GHCR not resolved yet, or version not reported
      return <span className="vcell">{ver}{updateBtn}</span>
  }
}

// ProbeRowMenu is the per-row "⋯" actions menu on the Probes table: the low-frequency, action-only
// controls (SNMP defaults, Console, Update sidecar, Delete) that used to each be a column. Portaled to
// <body> with fixed positioning so the table's horizontal scroll container can't clip it. Falsy items
// are dropped and stray separators trimmed, so the caller can pass role-gated items inline.
type ProbeMenuItem = { label: string; onClick: () => void; danger?: boolean }
function ProbeRowMenu({ items }: { items: Array<ProbeMenuItem | 'sep' | false | null | undefined> }) {
  const [open, setOpen] = useState(false)
  const btnRef = useRef<HTMLButtonElement>(null)
  const [pos, setPos] = useState<{ top?: number; bottom?: number; right: number } | null>(null)
  const list: (ProbeMenuItem | 'sep')[] = []
  for (const it of items) {
    if (!it) continue
    if (it === 'sep') { if (list.length && list[list.length - 1] !== 'sep') list.push('sep'); continue }
    list.push(it)
  }
  while (list.length && list[list.length - 1] === 'sep') list.pop()
  if (list.length === 0) return null
  const toggle = () => {
    if (!open && btnRef.current) {
      const r = btnRef.current.getBoundingClientRect()
      const right = Math.max(8, window.innerWidth - r.right)
      // Estimate the menu height and flip it above the button when there isn't room below (last row on
      // a mobile card would otherwise render off the bottom of the screen).
      const estH = list.length * 36 + 12
      const spaceBelow = window.innerHeight - r.bottom
      setPos(spaceBelow < estH && r.top > spaceBelow
        ? { bottom: Math.round(window.innerHeight - r.top) + 5, right }
        : { top: Math.round(r.bottom) + 5, right })
    }
    setOpen((o) => !o)
  }
  return (
    <div className="kebab-wrap">
      <button ref={btnRef} className={'kebab' + (open ? ' open' : '')} aria-label="Actions" title="Actions" onClick={toggle}>⋮</button>
      {open && pos && createPortal(
        <>
          <div onClick={() => setOpen(false)} style={{ position: 'fixed', inset: 0, zIndex: 59 }} />
          <div className="menu" style={{ position: 'fixed', top: pos.top ?? 'auto', bottom: pos.bottom ?? 'auto', right: pos.right, zIndex: 60 }}>
            {list.map((it, i) => it === 'sep'
              ? <div key={i} className="sep" />
              : <button key={i} className={it.danger ? 'danger' : undefined} onClick={() => { setOpen(false); it.onClick() }}>{it.label}</button>)}
          </div>
        </>, document.body)}
    </div>
  )
}

// ReportTokenPanel shows a freshly-minted check-in token once, with the single env var to add to
// the container (via the Docker/unRAID GUI) to turn on version reporting - no re-enrollment.
function ReportTokenPanel({ token, name, onDone }: { token: string; name: string; onDone: () => void }) {
  const envLine = `ARGUS_PROBE_TOKEN=${token}`
  return (
    <div style={{ padding: '12px 16px', background: 'var(--elevated)', borderBottom: '1px solid var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6, flexWrap: 'wrap' }}>
        <strong>Enable version reporting for {name}</strong>
        <span className="envpill" title="Shown once">token shown once</span>
        <CopyButton text={envLine} variant="default" style={{ marginLeft: 'auto' }} />
        <Button variant="default" onClick={onDone}>Done</Button>
      </div>
      <p style={{ color: 'var(--muted)', fontSize: 12.5, margin: '0 0 8px' }}>
        Add this environment variable to the <strong>{`argus-${name}`}</strong> container (unRAID: Edit → Add another variable) and restart it. The probe already knows the check-in URL from its enroll URL, so this token is all it needs - no re-enrollment. It's saved to the probe's volume on first boot, so you can remove the variable afterward.
      </p>
      <pre style={{ margin: 0, padding: '10px 12px', background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 8, overflowX: 'auto', fontSize: 12 }}><code>{envLine}</code></pre>
    </div>
  )
}

// ProbeUpdateCommand renders the copyable pull+restart command for a single probe (manual path).
function ProbeUpdateCommand({ p }: { p: Proxy }) {
  const cmd = `docker pull ${PROBE_IMAGE.replace(/:latest$/, '')}:${probeUpdateTag(p.target)} && docker restart argus-${p.name}`
  return (
    <div style={{ padding: '10px 16px', background: 'var(--elevated)', borderBottom: '1px solid var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6 }}>
        <span style={{ color: 'var(--muted)', fontSize: 12.5 }}>Run on {p.name}'s Docker host{p.version ? ` (currently ${p.version})` : ''}:</span>
        <CopyButton text={cmd} variant="default" style={{ marginLeft: 'auto' }} />
      </div>
      <pre style={{ margin: 0, padding: '10px 12px', background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 8, overflowX: 'auto', fontSize: 12 }}><code>{cmd}</code></pre>
    </div>
  )
}

// FleetTarget lets an admin pick the version every probe should converge on: 'latest' (rolling)
// or an exact pin like '7.0.29-r1'. The self-updater and the manual command both honour it.
function FleetTarget({ target, latest, onSaved }: { target: string | null; latest?: string; onSaved: (t: string) => void }) {
  const [editing, setEditing] = useState(false)
  const [val, setVal] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  function start() { setVal(target || 'latest'); setError(null); setEditing(true) }
  const valid = (v: string) => v === 'latest' || /^[0-9]+\.[0-9]+\.[0-9]+-r[0-9]+$/.test(v.trim())

  async function save() {
    const v = val.trim()
    if (!valid(v)) { setError('Use "latest" or a pin like 7.0.29-r1'); return }
    setBusy(true); setError(null)
    try {
      const res = await fetch('/api/probes/target', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ target: v }) })
      if (!res.ok) { setError(await errText(res, 'Could not save target')); return }
      const d = await res.json(); onSaved(d.target); setEditing(false)
    } finally { setBusy(false) }
  }

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', padding: '10px 16px', borderBottom: '1px solid var(--border)' }}>
      <span className="flabel">Fleet target version</span>
      {!editing ? (
        <>
          <code style={{ background: 'var(--elevated)', border: '1px solid var(--border)', borderRadius: 6, padding: '2px 8px' }}>{target ?? '…'}</code>
          <button className="btn" onClick={start}>Change</button>
          <span style={{ color: 'var(--muted)', fontSize: 12 }}>What every probe should run - <code>latest</code> or a pin like <code>7.0.29-r1</code>.{latest ? <> Newest published: <code>{latest}</code>.</> : null}</span>
        </>
      ) : (
        <>
          <input className="input" style={{ width: 160 }} value={val} autoFocus placeholder="latest" onChange={(e) => setVal(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') save(); if (e.key === 'Escape') setEditing(false) }} />
          <button className="btn primary" disabled={busy} onClick={save}>{busy ? 'Saving…' : 'Save'}</button>
          <button className="btn" disabled={busy} onClick={() => setEditing(false)}>Cancel</button>
          {error && <span style={{ color: 'var(--err)', fontSize: 12.5 }}>{error}</span>}
        </>
      )}
    </div>
  )
}

function slugPreview(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '')
}

function probeUnraidXml(c: CreatedToken): string {
  const name = `argus-${c.proxy_name}`
  const vol = `/mnt/user/appdata/${name}`
  const serverHost = c.core_host ? '' :
    `\n  <Config Name="Zabbix server host" Target="ZBX_SERVER_HOST" Default="" Mode="" Description="Core address the probe dials for :10051 (set if Argus didn't provide one)." Type="Variable" Display="always" Required="true" Mask="false"></Config>`
  // On unRAID, keep the native auto-update as the updater (no socket on the proxy, no sidecar app);
  // Argus shows drift + the manual update command.
  const selfUpd = ''
  return `<?xml version="1.0"?>
<Container version="2">
  <Name>${name}</Name>
  <Repository>${PROBE_IMAGE}</Repository>
  <Registry>https://github.com/g-guglielmi/argus</Registry>
  <Icon>https://raw.githubusercontent.com/g-guglielmi/argus/main/argus/web/public/argus-logo.png</Icon>
  <Network>bridge</Network>
  <Privileged>false</Privileged>
  <Overview>Self-enrolling Zabbix active proxy for Argus (site: ${c.site}). Enrolls on first boot; keep the volume persistent so the single-use token isn't re-redeemed.</Overview>
  <Category>Tools: Network:Management</Category>
  <Config Name="Enroll URL" Target="ARGUS_ENROLL_URL" Default="" Mode="" Description="Argus enrollment endpoint." Type="Variable" Display="always" Required="true" Mask="false">${c.enroll_url}</Config>
  <Config Name="Enroll Token" Target="ARGUS_ENROLL_TOKEN" Default="" Mode="" Description="Single-use enrollment token (shown once)." Type="Variable" Display="always" Required="true" Mask="true">${c.token}</Config>${serverHost}
  <Config Name="Data" Target="/var/lib/zabbix" Default="${vol}" Mode="rw" Description="Certs + SQLite spool. Persist this." Type="Path" Display="always" Required="true" Mask="false">${vol}</Config>
  <Config Name="SNMP traps" Target="/var/lib/zabbix/snmptraps" Default="${vol}/snmptraps" Mode="rw" Description="The base Zabbix image marks this path as a VOLUME; bind it into your appdata so Docker doesn't create an anonymous volume for it." Type="Path" Display="advanced" Required="false" Mask="false">${vol}/snmptraps</Config>${selfUpd}
</Container>`
}

type ProbeFmt = 'docker' | 'compose' | 'unraid' | 'vm'
// Console keyboard layouts offered for the probe VM (value = the console keymap applied to
// /etc/vconsole.conf on first boot; matters for the hypervisor console + break-glass login). US default.
const VM_KEYMAPS: [string, string][] = [
  ['us', 'US English'], ['uk', 'UK English'], ['it', 'Italian'], ['de', 'German'],
  ['fr', 'French'], ['es', 'Spanish'], ['pt-latin1', 'Portuguese'],
]
// CIDR prefix -> dotted subnet mask, for the Static IP dropdown (label shows both so "the /24 is the
// subnet mask" is self-evident). Server-side staticCIDR accepts the prefix number.
const CIDR_PREFIXES: [string, string][] = [
  ['30', '255.255.255.252'], ['29', '255.255.255.248'], ['28', '255.255.255.240'], ['27', '255.255.255.224'],
  ['26', '255.255.255.192'], ['25', '255.255.255.128'], ['24', '255.255.255.0'], ['23', '255.255.254.0'],
  ['22', '255.255.252.0'], ['21', '255.255.248.0'], ['20', '255.255.240.0'], ['16', '255.255.0.0'], ['8', '255.0.0.0'],
]
// AddProbeWizard is the guided "Add a probe" modal: name -> method + settings -> deploy -> an
// optional live wait for enrollment. The token is minted only when leaving the method step, and only
// re-minted if the name changes, so Back (to fix a misclick or change method) never wastes a token.
function AddProbeWizard({ existingNames, onClose, onEnrolled }: { existingNames: string[]; onClose: () => void; onEnrolled: () => void }) {
  const [step, setStep] = useState(1)
  const [site, setSite] = useState('')
  const [ttl, setTtl] = useState(24)
  const [advanced, setAdvanced] = useState(false)
  const [method, setMethod] = useState<ProbeFmt>('vm')
  const [selfupdate, setSelfupdate] = useState(true)
  const [keymap, setKeymap] = useState('us')
  const [staticNet, setStaticNet] = useState(false)
  const [netIp, setNetIp] = useState('')
  const [netPrefix, setNetPrefix] = useState('24')
  const [netGw, setNetGw] = useState('')
  const [netDns, setNetDns] = useState('')
  const [netDns2, setNetDns2] = useState('')
  const [created, setCreated] = useState<CreatedToken | null>(null)
  const [busy, setBusy] = useState(false)
  const [seeding, setSeeding] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [enrolled, setEnrolled] = useState<{ name: string; online: boolean } | null>(null)

  const slug = slugPreview(site)
  const proxyName = slug ? `proxy-${slug}` : ''
  const redeploy = existingNames.includes(created?.proxy_name || proxyName)

  async function mint(): Promise<CreatedToken | null> {
    setErr(null); setBusy(true)
    try {
      const res = await fetch('/api/probes/tokens', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ site, ttl_hours: ttl }) })
      if (!res.ok) { setErr(await errText(res, 'Could not create the enrollment token')); return null }
      return await res.json()
    } catch { setErr('Could not create the enrollment token'); return null }
    finally { setBusy(false) }
  }

  function next1() {
    if (!slug) { setErr('Enter a site name (letters, digits and hyphens).'); return }
    // A token minted for a different name is now stale - drop it so the next step re-mints.
    if (created && created.site !== slug) { fetch(`/api/probes/tokens/${created.id}`, { method: 'DELETE' }).catch(() => {}); setCreated(null) }
    setErr(null); setStep(2)
  }

  async function next2() {
    let c = created
    if (!c || c.site !== slug) { c = await mint(); if (!c) return; setCreated(c) }
    setErr(null); setStep(3)
  }

  async function downloadSeedISO() {
    if (!created) return
    setSeeding(true); setErr(null)
    try {
      const res = await fetch('/api/probes/seed-iso', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: created.token, enroll_url: created.enroll_url, core_host: created.core_host, keymap, name: created.proxy_name, ...(staticNet ? { static_ip: netIp, prefix: netPrefix, gateway: netGw, dns: [netDns, netDns2].map((s) => s.trim()).filter(Boolean).join(',') } : {}) }) })
      if (!res.ok) { setErr(await errText(res, 'Could not build the seed ISO')); return }
      const blob = await res.blob(); const url = URL.createObjectURL(blob)
      const a = document.createElement('a'); a.href = url; a.download = `argus-seed-${created.proxy_name}.iso`
      document.body.appendChild(a); a.click(); a.remove(); URL.revokeObjectURL(url)
    } catch { setErr('Could not build the seed ISO') } finally { setSeeding(false) }
  }

  const content = created ? (method === 'docker' ? probeDockerCmd(created, redeploy, selfupdate) : method === 'compose' ? probeComposeCmd(created) : method === 'unraid' ? probeUnraidXml(created) : '') : ''

  // Optional final step: poll until this token is redeemed, then show success.
  useEffect(() => {
    if (step !== 4 || !created) return
    let alive = true
    let timer: ReturnType<typeof setTimeout>
    const tick = async () => {
      try {
        const rows: EnrollTokenRow[] = await (await fetch('/api/probes/tokens')).json()
        const row = (rows || []).find((r) => r.id === created.id)
        if (row && row.status === 'enrolled') {
          let online = false
          try { const px: Proxy[] = await (await fetch('/api/proxies')).json(); online = (px || []).some((p) => p.name === created.proxy_name && p.online) } catch { /* ignore */ }
          if (alive) { setEnrolled({ name: created.proxy_name, online }); onEnrolled() }
          return
        }
      } catch { /* keep polling */ }
      if (alive) timer = setTimeout(tick, 3000)
    }
    timer = setTimeout(tick, 1200)
    return () => { alive = false; clearTimeout(timer) }
  }, [step, created]) // eslint-disable-line react-hooks/exhaustive-deps

  const METHODS: { id: ProbeFmt; label: string; hint: string }[] = [
    { id: 'vm', label: 'Virtual machine', hint: 'A downloadable appliance image (OVA / qcow2 / VHD).' },
    { id: 'unraid', label: 'unRAID', hint: 'A template for the unRAID Docker manager.' },
    { id: 'docker', label: 'Docker run', hint: 'One command on the site Docker host.' },
    { id: 'compose', label: 'Docker Compose', hint: 'A compose file (proxy + updater sidecar).' },
  ]

  function addAnother() { setEnrolled(null); setCreated(null); setStep(1); setSite(''); setStaticNet(false); setErr(null) }

  // Portal to <body>: the wizard renders inside `.content.view-enter`, whose transform animation makes
  // a fixed-position ancestor, so `.dlg-backdrop` (position:fixed) would size to that element instead of
  // the viewport - the dialog then can't cap at viewport height and the page scrolls. Rendering at the
  // body root keeps the backdrop viewport-relative so the pinned footer works.
  return createPortal(
    <div className="dlg-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="dlg" role="dialog" aria-modal="true" style={{ maxWidth: 560, maxHeight: 'calc(100dvh - 32px)', display: 'flex', flexDirection: 'column' }}>
        <div className="dlg-title">Add a probe{step < 4 && <span style={{ color: 'var(--faint)', fontWeight: 400, fontSize: 12 }}> &middot; step {step} of 3</span>}</div>
        {err && <div style={{ color: 'var(--err)', fontSize: 13, marginBottom: 8 }}>{err}</div>}
        <div className="dlg-scroll">


        {step === 1 && (
          <div style={{ display: 'grid', gap: 14 }}>
            <label style={{ display: 'grid', gap: 4 }}>
              <span className="flabel">Site name</span>
              <input className="input" placeholder="e.g. office" value={site} autoFocus onChange={(e) => setSite(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') next1() }} />
              <span className="set-hint">Registered in Zabbix as <strong>proxy-{slug || '<site>'}</strong>.</span>
            </label>
            <button type="button" onClick={() => setAdvanced((v) => !v)} style={{ justifySelf: 'start', background: 'none', border: 'none', color: 'var(--accent)', cursor: 'pointer', fontSize: 12.5, padding: 0 }}>{advanced ? 'Hide options' : 'Advanced'}</button>
            {advanced && (
              <label style={{ display: 'grid', gap: 4 }}>
                <span className="flabel">Enrollment token valid for</span>
                <Select value={ttl} onChange={(e) => setTtl(Number(e.target.value))}>
                  <option value={1}>1 hour</option><option value={24}>24 hours</option><option value={168}>7 days</option><option value={720}>30 days</option>
                </Select>
              </label>
            )}
          </div>
        )}

        {step === 2 && (
          <div style={{ display: 'grid', gap: 12 }}>
            <div style={{ display: 'grid', gap: 8 }}>
              {METHODS.map((m) => (
                <button key={m.id} type="button" onClick={() => setMethod(m.id)} style={{ textAlign: 'left', padding: '10px 12px', borderRadius: 8, cursor: 'pointer', border: `1px solid ${method === m.id ? 'var(--accent)' : 'var(--border)'}`, background: method === m.id ? 'color-mix(in srgb, var(--accent) 12%, transparent)' : 'var(--elevated)' }}>
                  <div style={{ fontWeight: 600, fontSize: 13.5, color: 'var(--text)' }}>{m.label}</div>
                  <div style={{ color: 'var(--muted)', fontSize: 12 }}>{m.hint}</div>
                </button>
              ))}
            </div>
            {method === 'docker' && (
              <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, color: 'var(--muted)', cursor: 'pointer' }}>
                <input type="checkbox" checked={selfupdate} onChange={(e) => setSelfupdate(e.target.checked)} />
                Add the argus-updater sidecar (lets Argus update this probe)
              </label>
            )}
            {method === 'compose' && <p style={{ color: 'var(--muted)', fontSize: 12.5, margin: 0 }}>Includes the argus-updater sidecar (two services).</p>}
            {method === 'unraid' && <p style={{ color: 'var(--muted)', fontSize: 12.5, margin: 0 }}>Uses unRAID native auto-update; Argus shows drift and a manual update command.</p>}
            {method === 'vm' && (
              <div style={{ display: 'grid', gap: 10 }}>
                <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, color: 'var(--muted)' }}>
                  Console keyboard layout
                  <Select value={keymap} onChange={(e) => setKeymap(e.target.value)}>
                    {VM_KEYMAPS.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
                  </Select>
                </label>
                <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 12.5, color: 'var(--muted)', cursor: 'pointer' }}>
                  <input type="checkbox" checked={staticNet} onChange={(e) => setStaticNet(e.target.checked)} />
                  Static IP <span style={{ color: 'var(--faint)' }}>(sites with no DHCP)</span>
                </label>
                {staticNet && (
                  <div className="wiz-net" style={{ display: 'grid', gap: 8 }}>
                    <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(0, 205px)', gap: 8 }}>
                      <input className="input" value={netIp} onChange={(e) => setNetIp(e.target.value)} placeholder="IP address (10.0.0.50)" />
                      <Select value={netPrefix} onChange={(e) => setNetPrefix(e.target.value)} title="Subnet mask">
                        {CIDR_PREFIXES.map(([p, mask]) => <option key={p} value={p}>/{p} — {mask}</option>)}
                      </Select>
                    </div>
                    <input className="input" value={netGw} onChange={(e) => setNetGw(e.target.value)} placeholder="Gateway (10.0.0.1)" />
                    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
                      <input className="input" value={netDns} onChange={(e) => setNetDns(e.target.value)} placeholder="DNS 1 (10.0.0.10)" />
                      <input className="input" value={netDns2} onChange={(e) => setNetDns2(e.target.value)} placeholder="DNS 2 (optional)" />
                    </div>
                    <span style={{ color: 'var(--faint)', fontSize: 11.5 }}>The dropdown is the subnet mask (/24 = 255.255.255.0). A second DNS is optional, for redundancy.</span>
                  </div>
                )}
              </div>
            )}
          </div>
        )}

        {step === 3 && created && (
          <div style={{ display: 'grid', gap: 10 }}>
            <p style={{ color: 'var(--muted)', fontSize: 12.5, margin: 0 }}>Deploy <strong>{created.proxy_name}</strong>. The token is single-use and expires {relTime(created.expires_at)}.{!created.core_host && ' Set the core host so it can reach :10051.'}</p>
            {method === 'vm' ? (
              <div style={{ display: 'grid', gap: 8 }}>
                <p style={{ color: 'var(--muted)', fontSize: 12.5, margin: 0, lineHeight: 1.6 }}>Import the appliance (OVA / qcow2 / VHD), then attach the seed ISO as a CD for zero-touch enrollment - or boot it (with DHCP) and open the first-boot page at its IP.</p>
                <div><Button variant="primary" onClick={downloadSeedISO} disabled={seeding}>{seeding ? 'Building the ISO...' : 'Download seed ISO'}</Button></div>
              </div>
            ) : (
              <>
                <div style={{ display: 'flex', justifyContent: 'flex-end' }}><CopyButton text={content} variant="default" /></div>
                <pre style={{ margin: 0, padding: '11px 12px', background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 8, overflowX: 'auto', fontSize: 12, lineHeight: 1.5, maxHeight: 280 }}><code>{content}</code></pre>
              </>
            )}
          </div>
        )}

        {step === 4 && (
          <div style={{ display: 'grid', gap: 10, padding: '6px 0' }}>
            {!enrolled ? (
              <>
                <div style={{ display: 'flex', alignItems: 'center', gap: 10, color: 'var(--text)', fontSize: 13.5 }}>
                  <span className="spinner" /> Waiting for <strong>{created?.proxy_name}</strong> to enrol...
                </div>
                <p style={{ color: 'var(--faint)', fontSize: 12, margin: 0 }}>You can close this - the probe will still appear in the list once it enrols.</p>
              </>
            ) : (
              <div style={{ color: 'var(--ok)', fontSize: 14, fontWeight: 600 }}>&#10003; {enrolled.name} enrolled{enrolled.online ? ' and online' : ''}.</div>
            )}
          </div>
        )}

        </div>
        <div className="dlg-foot">
          {step === 1 && <><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" onClick={next1}>Next</Button></>}
          {step === 2 && <><Button variant="ghost" onClick={() => { setErr(null); setStep(1) }}>Back</Button><Button variant="primary" onClick={next2} disabled={busy}>{busy ? 'Creating...' : 'Next'}</Button></>}
          {step === 3 && <><Button variant="ghost" onClick={() => { setErr(null); setStep(2) }}>Back</Button><Button variant="ghost" onClick={onClose}>Done</Button><Button variant="primary" onClick={() => { setErr(null); setStep(4) }}>Watch for it</Button></>}
          {step === 4 && (enrolled
            ? <><Button variant="ghost" onClick={addAnother}>Add another</Button><Button variant="primary" onClick={onClose}>Done</Button></>
            : <><Button variant="ghost" onClick={() => setStep(3)}>Back</Button><Button variant="primary" onClick={onClose}>Done</Button></>)}
        </div>
      </div>
    </div>,
    document.body,
  )
}


const kbIcon = {
  pause: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><rect x="6" y="5" width="4" height="14" rx="1" /><rect x="14" y="5" width="4" height="14" rx="1" /></svg>,
  hide: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M2 12s3.5-7 10-7 10 7 10 7" /><path d="M3 3l18 18" /><path d="M9.5 9.5a3 3 0 0 0 4.2 4.2" /></svg>,
  resume: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M7 5l12 7-12 7z" /></svg>,
  show: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z" /><circle cx="12" cy="12" r="3" /></svg>,
  ack: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M22 11.2V12a10 10 0 1 1-5.9-9.1" /><path d="M22 4 12 14.5l-3-3" /></svg>,
  edit: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M12 20h9" /><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z" /></svg>,
  folder: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" /></svg>,
  gear: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" /></svg>,
  trash: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M4 7h16M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2M6 7l1 13a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1l1-13" /></svg>,
}
// icons for the per-user kebab actions
const uIcon = {
  key: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="8" cy="15" r="4" /><path d="M10.8 12.2 20 3M17 6l2 2M14 9l2 2" /></svg>,
  shield: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z" /></svg>,
  fp: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M12 11v3M8 9a4 4 0 0 1 8 0v2a8 8 0 0 1-1 4M6 13a10 10 0 0 0 1 5M16 18a12 12 0 0 0 .8-4" /></svg>,
  ban: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="12" r="9" /><path d="M5.6 5.6l12.8 12.8" /></svg>,
  enable: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="12" r="9" /><path d="M8.5 12.5l2.5 2.5 4.5-5" /></svg>,
  trash: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M4 7h16M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2M6 7l1 13a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1l1-13" /></svg>,
}

// A kebab menu action. onClick fires immediately; onPick opens the duration submenu first and
// fires with the chosen seconds (null = indefinite). sep renders a divider.
type KAction = { label: string; icon?: ReactNode; danger?: boolean; onClick?: () => void; onPick?: (s: number | null) => void; sep?: boolean }

function Kebab({ actions, disabled, up }: { actions: KAction[]; disabled?: boolean; up?: boolean }) {
  const [open, setOpen] = useState(false)
  const [dur, setDur] = useState<KAction | null>(null)
  const [custom, setCustom] = useState(false)
  const [val, setVal] = useState('')
  const btnRef = useRef<HTMLButtonElement>(null)
  const [autoUp, setAutoUp] = useState(false)
  function close() { setOpen(false); setDur(null); setCustom(false) }
  // Open the menu upward when there isn't room below the button (the last row of a mobile card would
  // otherwise render off the bottom of the screen). Caller's `up` still forces it.
  function toggleOpen() {
    if (!open) {
      setDur(null); setCustom(false)
      const r = btnRef.current?.getBoundingClientRect()
      if (r) {
        const estH = actions.length * 36 + 24
        const spaceBelow = window.innerHeight - r.bottom
        setAutoUp(spaceBelow < estH && r.top > spaceBelow)
      }
    }
    setOpen((o) => !o)
  }
  function choose(a: KAction) { if (a.onPick) { setDur(a) } else { const fn = a.onClick; close(); fn?.() } }
  function pickPreset(s: number | null | 'custom') {
    if (s === 'custom') { setVal(toLocalInput(Date.now() + 3600_000)); setCustom(true); return }
    const fn = dur?.onPick; close(); fn?.(s)
  }
  function confirmCustom() {
    const t = new Date(val).getTime(); const secs = Math.round((t - Date.now()) / 1000); const fn = dur?.onPick
    close(); if (isFinite(t) && secs > 0) fn?.(secs)
  }
  return (
    <span className="kebab-wrap" onClick={(e) => e.stopPropagation()}>
      <button ref={btnRef} className={'kebab' + (open ? ' open' : '')} title="Actions" disabled={disabled} onClick={toggleOpen}>⋮</button>
      {open && (
        <>
          <div onClick={close} style={{ position: 'fixed', inset: 0, zIndex: 30 }} />
          <div className={'menu' + ((up || autoUp) ? ' up' : '')} style={{ zIndex: 31, minWidth: dur && custom ? 240 : 180 }} onClick={(e) => e.stopPropagation()}>
            {!dur && actions.map((a, i) => a.sep
              ? <div key={i} className="sep" />
              : <button key={i} className={a.danger ? 'danger' : ''} onClick={() => choose(a)}>{a.icon}{a.label}</button>)}
            {dur && !custom && DURATIONS.map((d) => <button key={d.label} onClick={() => pickPreset(d.seconds)}>{d.label}</button>)}
            {dur && custom && (
              <div style={{ padding: '0.4rem 0.5rem' }}>
                <div style={{ fontSize: '0.78rem', color: 'var(--muted)', marginBottom: '0.35rem' }}>{dur.label} until:</div>
                <input type="datetime-local" className="input" value={val} min={toLocalInput(Date.now())} onChange={(e) => setVal(e.target.value)} style={{ width: '100%', marginBottom: '0.5rem' }} />
                <div style={{ display: 'flex', gap: '0.4rem', justifyContent: 'flex-end' }}>
                  <button className="btn ghost" onClick={() => setCustom(false)}>Back</button>
                  <button className="btn primary" onClick={confirmCustom}>Set</button>
                </div>
              </div>
            )}
          </div>
        </>
      )}
    </span>
  )
}

// Focus is the PRTG-style drill-down state layered over the expandable tree: from the whole tree
// (root) you can narrow to a group node, a single host, or a single sensor. `path` is the full group
// path (Zabbix nests groups by name with "/", e.g. "mybz/Network"), so it can address any node in the
// hierarchy. The chevrons still expand inline for a quick peek; clicking a name drills the focus.
type Focus =
  | { level: 'root' }
  | { level: 'group'; path: string }
  | { level: 'host'; path: string; hostId: string }
  | { level: 'sensor'; path: string; hostId: string; itemId: string; itemName?: string }

// A node in the group tree. Only REAL Zabbix groups become nodes (plus a synthetic "Ungrouped" for
// hosts with no group) - there are no virtual parents: a group named "a/b" whose parent "a" isn't a
// real group renders at the top level with its full name. `name` is the display label (the path
// remainder below its real parent, or the full path at the top level); `path` is the full group name.
type GNode = { path: string; name: string; group?: Group; parentPath?: string; children: GNode[]; hosts: Host[] }
// One saved sibling ordering: the children of `scope` (a parent group path, '' for top-level roots) of
// one `kind`, listed in manual order (group paths, or host ids). Unlisted siblings fall back to alpha.
type OrderSet = { scope: string; kind: 'group' | 'host' | 'sibling'; items: string[] }

function MonitoringView({ role, target, homeSignal, onNavigate, advanced }: { role: string; target: { hostId?: string; itemId?: string; itemName?: string; groupPath?: string; n: number } | null; homeSignal: number; onNavigate: (hostId: string | null, itemId: string | null, group?: string | null, push?: boolean) => void; advanced: boolean }) {
  const confirm = useConfirm()
  const [hosts, setHosts] = useState<Host[]>([])
  const [groups, setGroups] = useState<Group[]>([])
  const [proxies, setProxies] = useState<Proxy[]>([])
  const [creating, setCreating] = useState(false) // "+ New group" inline band open
  const [addingDevice, setAddingDevice] = useState(false) // "+ Add device" inline band open (admin)
  const [classes, setClasses] = useState<DeviceClass[]>([]) // device-class catalog for the attach band
  const [gAction, setGAction] = useState<{ id: string; mode: 'rename' | 'delete' } | null>(null) // per-group rename/delete band
  const [newSubPath, setNewSubPath] = useState<string | null>(null) // group path under which a "New subgroup" band is open
  const [focus, setFocus] = useState<Focus>({ level: 'root' })
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set())
  const [openHost, setOpenHost] = useState<string | null>(null)
  const [editGroupsHost, setEditGroupsHost] = useState<string | null>(null) // host id with the "Edit groups…" band open
  const [settingsHost, setSettingsHost] = useState<string | null>(null) // host id with the "Settings…" band open
  const [showAll, setShowAll] = useState(false)
  const [busyId, setBusyId] = useState<string | null>(null)
  const [order, setOrder] = useState<OrderSet[]>([]) // saved manual sibling orderings
  const [reorder, setReorder] = useState(false)      // "Reorder" mode: show inline up/down arrows
  const [hidden, setHidden] = useState<Set<string>>(() => new Set()) // group paths hidden from the tree
  const [showHidden, setShowHidden] = useState(false) // reveal hidden groups (to manage them)
  // "All sensors" and hidden-group management are advanced-only; when advanced mode is off they stay
  // hidden and their effect is forced off, even if a stale toggle was left on.
  const showAllEff = advanced && showAll
  const showHiddenEff = advanced && showHidden
  const canPause = role === 'admin' || role === 'helpdesk'

  function load(initial = false) {
    if (initial) setLoading(true)
    fetch('/api/hosts')
      .then(async (r) => { if (!r.ok) { setError(await errText(r, 'Failed to load hosts')); return } setHosts(await r.json()); setError(null) })
      .catch(() => setError('Failed to load hosts'))
      .finally(() => { if (initial) setLoading(false) })
    fetch('/api/groups').then((r) => (r.ok ? r.json() : [])).then((g) => setGroups(g || [])).catch(() => {})
    fetch('/api/tree/order').then((r) => (r.ok ? r.json() : [])).then((o) => setOrder(o || [])).catch(() => {})
    fetch('/api/tree/hidden').then((r) => (r.ok ? r.json() : [])).then((h) => setHidden(new Set(h || []))).catch(() => {})
    fetch('/api/proxies').then((r) => (r.ok ? r.json() : [])).then((p) => setProxies(p || [])).catch(() => {})
  }
  useEffect(() => { load(true); const t = setInterval(() => load(false), 30000); const off = onDataRefresh(() => load(false)); return () => { clearInterval(t); off() } }, [])
  // Device-class catalog is static; fetch once for the "+ Add device" band.
  useEffect(() => { fetch('/api/classes').then((r) => (r.ok ? r.json() : [])).then((c) => setClasses(c || [])).catch(() => {}) }, [])

  // Group management (create/rename/delete + move a host between groups). All are admin/helpdesk-gated
  // config writes to Zabbix host groups, driven by inline bands (no browser prompts); on success we
  // reload so the tree reflects the change.
  async function createGroup(name: string) {
    name = name.trim()
    if (!name) return
    const res = await fetch('/api/groups', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }) }).catch(() => null)
    if (!res || !res.ok) { setError(await errText(res, 'Could not create the group')); return }
    setError(null); setCreating(false); setNewSubPath(null); load(); fireDataRefresh()
  }
  async function renameGroup(g: Group, name: string) {
    name = name.trim()
    if (!name || name === g.name) { setGAction(null); return }
    const res = await fetch(`/api/groups/${g.id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name }) }).catch(() => null)
    if (!res || !res.ok) { setError(await errText(res, 'Could not rename the group')); return }
    setError(null); setGAction(null); load(); fireDataRefresh()
  }
  async function deleteGroup(g: Group) {
    const res = await fetch(`/api/groups/${g.id}`, { method: 'DELETE' }).catch(() => null)
    if (!res || !res.ok) { setError(await errText(res, 'Could not delete the group')); return }
    setError(null); setGAction(null); load(); fireDataRefresh()
  }
  async function setHostGroups(hostId: string, groupIds: string[]) {
    const res = await fetch(`/api/hosts/${hostId}/groups`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ group_ids: groupIds }) }).catch(() => null)
    if (!res || !res.ok) { setError(await errText(res, 'Could not move the host')); return }
    setError(null); setEditGroupsHost(null); load(); fireDataRefresh()
    await maybeOfferProxySwitch(hostId, groupIds)
  }
  // After a group move, if the host landed in exactly one site group that matches a proxy (proxy
  // "proxy-<site>" ↔ top-level group "<site>") and it isn't already on that proxy, offer to switch its
  // "Monitored by" collector too - keeping the site=proxy=group model in sync (confirmed, not silent).
  function siteOfProxy(name: string) { return name.startsWith('proxy-') ? name.slice(6) : name }
  async function maybeOfferProxySwitch(hostId: string, groupIds: string[]) {
    const host = hosts.find((h) => h.id === hostId)
    if (!host) return
    const idToName = new Map(groups.map((g) => [g.id, g.name]))
    const topSegs = new Set(groupIds.map((id) => (idToName.get(id) || '').split('/')[0]).filter(Boolean))
    const matched = proxies.filter((p) => topSegs.has(siteOfProxy(p.name)))
    if (matched.length !== 1) return
    const target = matched[0]
    if ((host.proxy_id || '0') === target.id) return
    const curLabel = host.proxy_id && host.proxy_id !== '0' ? (proxies.find((p) => p.id === host.proxy_id)?.name || 'another proxy') : 'the server'
    if (!(await confirm({ title: 'Switch collector?', message: `${host.name} is now in the “${siteOfProxy(target.name)}” group. Also set its collector to ${target.name}? (currently ${curLabel})`, confirmLabel: 'Switch proxy' }))) return
    const r = await fetch(`/api/hosts/${hostId}/proxy`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ monitored_by: 1, proxy_id: target.id }) }).catch(() => null)
    if (!r || !r.ok) { setError(await errText(r, 'Could not switch the collector proxy')); return }
    setError(null); load(); fireDataRefresh()
  }

  // Respond to a deep-link from the Overview/lists/Triggers: drill the focus onto the target host
  // (or sensor), and expand its site + host so the tree underneath is consistent. HostItems opens
  // the sensor chart via autoOpenItem. Re-runs once hosts have loaded.
  useEffect(() => {
    if (!target) return
    // A group deep-link (?group=…) focuses the group node directly - no host needed.
    if (target.groupPath && !target.hostId) {
      const p = target.groupPath
      setCollapsed((c) => { const n = new Set(c); let a = ''; for (const seg of p.split('/')) { a = a ? a + '/' + seg : seg; n.delete(a) } return n })
      setFocus({ level: 'group', path: p })
      return
    }
    const hid = target.hostId
    if (!hid) return
    const h = hosts.find((x) => x.id === hid)
    if (!h) return
    const p = (h.groups && h.groups.length ? h.groups : ['Ungrouped'])[0]
    // Un-collapse the target group and all its ancestor paths so the host is reachable in the tree.
    setCollapsed((c) => { const n = new Set(c); let a = ''; for (const seg of p.split('/')) { a = a ? a + '/' + seg : seg; n.delete(a) } return n })
    setOpenHost(p + '::' + hid)
    setFocus(target.itemId
      ? { level: 'sensor', path: p, hostId: hid, itemId: target.itemId, itemName: target.itemName }
      : { level: 'host', path: p, hostId: hid })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [target?.n, hosts.length])

  // Clicking the Monitoring tab (even when it's already active) returns the tree to the root. The ref
  // skips the initial mount so a deep-link's focus isn't clobbered; it only fires on a real nav click.
  const lastHome = useRef(homeSignal)
  useEffect(() => {
    if (homeSignal === lastHome.current) return
    lastHome.current = homeSignal
    setFocus({ level: 'root' }); setOpenHost(null); setEditGroupsHost(null); setSettingsHost(null); setCreating(false); setGAction(null); setNewSubPath(null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [homeSignal])

  // Drill helpers - narrow the focus and refine the URL so Back steps between screens. Each level is
  // URL-persisted: group focus as ?group=<path>, host/sensor as ?host=&item=, so a reload or shared
  // link restores the same screen.
  function drillRoot() { setFocus({ level: 'root' }); onNavigate(null, null, null, true) }
  function drillGroup(path: string) { setFocus({ level: 'group', path }); onNavigate(null, null, path, true) }
  function drillHost(path: string, hostId: string) { setFocus({ level: 'host', path, hostId }); setOpenHost(path + '::' + hostId); onNavigate(hostId, null, null, true) }
  function drillSensor(path: string, hostId: string, itemId: string, itemName: string) { setFocus({ level: 'sensor', path, hostId, itemId, itemName }); onNavigate(hostId, itemId, null, true) }

  async function setHostState(h: Host, action: 'pause' | 'hide', seconds: number | null) {
    setBusyId(h.id)
    const res = await fetch(`/api/hosts/${h.id}/${action}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ duration_seconds: seconds ?? 0 }) }).catch(() => null)
    setBusyId(null)
    if (res && !res.ok) { setError(await errText(res, `Could not ${action} host`)); return }
    load(); fireDataRefresh()
  }
  async function clearHostState(h: Host, action: 'pause' | 'hide') {
    setBusyId(h.id)
    const res = await fetch(`/api/hosts/${h.id}/${action}`, { method: 'DELETE' }).catch(() => null)
    setBusyId(null)
    if (res && !res.ok) { setError(await errText(res, `Could not resume host`)); return }
    load(); fireDataRefresh()
  }

  // Build the group tree WITHOUT virtual parents: only real Zabbix groups (from /api/groups) become
  // nodes. A group nests under the real group that is the longest strict prefix of its path; a group
  // with no real ancestor sits at the top level under its full name. Hosts attach to their exact group
  // node; hosts with no group fall under a synthetic "Ungrouped" root.
  const realNames = new Set(groups.map((g) => g.name))
  function realParent(name: string): string | null {
    const segs = name.split('/')
    for (let i = segs.length - 1; i >= 1; i--) { const pre = segs.slice(0, i).join('/'); if (realNames.has(pre)) return pre }
    return null
  }
  const byPath = new Map<string, GNode>()
  const roots: GNode[] = []
  for (const g of groups) {
    const parent = realParent(g.name)
    byPath.set(g.name, { path: g.name, name: parent ? g.name.slice(parent.length + 1) : g.name, group: g, parentPath: parent || undefined, children: [], hosts: [] })
  }
  for (const n of byPath.values()) { if (n.parentPath) byPath.get(n.parentPath)!.children.push(n); else roots.push(n) }
  for (const h of hosts) {
    const gs = h.groups && h.groups.length ? h.groups : ['Ungrouped']
    for (const gp of gs) {
      let n = byPath.get(gp)
      if (!n) { n = { path: gp, name: gp, children: [], hosts: [] }; byPath.set(gp, n); roots.push(n) } // Ungrouped, or a stray group not in /api/groups
      n.hosts.push(h)
    }
  }
  // Sort a sibling set: alphabetical by default; when a manual order is saved for it, listed items take
  // that order and any unlisted (newly added) ones fall to the end, still alphabetical. Relies on a
  // stable Array.sort so the alpha pre-sort survives as the tiebreak among equal (Infinity) positions.
  const orderMap = new Map<string, string[]>()
  for (const o of order) orderMap.set(o.scope + ' ' + o.kind, o.items)
  const orderOf = (scope: string, kind: 'group' | 'host' | 'sibling') => orderMap.get(scope + ' ' + kind)
  function applyOrder<T>(items: T[], orderKey: (t: T) => string, alphaKey: (t: T) => string, ordered?: string[]): T[] {
    const base = [...items].sort((a, b) => alphaKey(a).localeCompare(alphaKey(b)))
    if (!ordered || ordered.length === 0) return base
    const pos = new Map(ordered.map((id, i) => [id, i]))
    return base.sort((a, b) => (pos.get(orderKey(a)) ?? Infinity) - (pos.get(orderKey(b)) ?? Infinity))
  }
  const orderTree = (ns: GNode[], scope: string) => {
    const sorted = applyOrder(ns, (n) => n.path, (n) => n.name, orderOf(scope, 'group'))
    ns.length = 0; ns.push(...sorted)
    for (const n of ns) { n.hosts = applyOrder(n.hosts, (h) => h.id, (h) => h.name, orderOf(n.path, 'host')); orderTree(n.children, n.path) }
  }
  orderTree(roots, '')
  // One ordered list of a parent's children — its direct hosts and its subgroups together — so a manual
  // 'sibling' order can interleave them (put a host above or below the subgroups). Base order is hosts
  // then groups (each already sorted by orderTree); a saved 'sibling' order, when present, overrides it.
  type Sibling = { host?: Host; group?: GNode; key: string }
  const mergedChildren = (hs: Host[], children: GNode[], scope: string): Sibling[] => {
    const base: Sibling[] = [
      ...hs.map((h) => ({ host: h, key: 'h:' + h.id })),
      ...children.map((n) => ({ group: n, key: 'g:' + n.path })),
    ]
    const sib = orderOf(scope, 'sibling')
    if (!sib || sib.length === 0) return base
    const pos = new Map(sib.map((k, i) => [k, i]))
    return [...base].sort((a, b) => (pos.get(a.key) ?? Infinity) - (pos.get(b.key) ?? Infinity))
  }
  // Drop hidden groups (and their whole subtree) from the tree unless we're revealing them to manage
  // them. A host that's only in hidden groups disappears with them; a host also in a visible group still
  // shows there. Mutates children arrays in place (shared with byPath, so focus views prune too).
  const pruneHidden = (ns: GNode[]) => {
    for (let i = ns.length - 1; i >= 0; i--) {
      if (!showHiddenEff && hidden.has(ns[i].path)) { ns.splice(i, 1); continue }
      pruneHidden(ns[i].children)
    }
  }
  if (hidden.size > 0) pruneHidden(roots)

  // Persist a sibling set's new order (optimistic; revert + surface the error on failure). Admin/helpdesk
  // only - the button that calls this is gated on canPause.
  async function reorderSiblings(scope: string, kind: 'group' | 'host' | 'sibling', items: string[]) {
    const prev = order
    setOrder((o) => [...o.filter((s) => !(s.scope === scope && s.kind === kind)), { scope, kind, items }])
    const res = await fetch('/api/tree/order', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ scope, kind, items }) }).catch(() => null)
    if (!res || !res.ok) { setOrder(prev); setError(await errText(res, 'Could not save the new order')) }
  }
  // Move a group node (or host) by one slot within its displayed siblings, materializing the whole set's
  // order on the first move so a previously-alphabetical set becomes explicitly ordered.
  function moveWithin(ids: string[], index: number, dir: -1 | 1, scope: string, kind: 'group' | 'host' | 'sibling') {
    const j = index + dir
    if (j < 0 || j >= ids.length) return
    const next = ids.slice();[next[index], next[j]] = [next[j], next[index]]
    reorderSiblings(scope, kind, next)
  }
  // Hide or unhide a group in the tree (Argus-local; the group stays in Zabbix). Optimistic, reverts on
  // failure. Admin-gated via the controls that call it (see the kebab, gated on advanced ⇒ admin).
  async function setGroupHidden(path: string, hide: boolean) {
    const prev = hidden
    setHidden((h) => { const n = new Set(h); hide ? n.add(path) : n.delete(path); return n })
    const res = await fetch('/api/tree/hidden', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ path, hidden: hide }) }).catch(() => null)
    if (!res || !res.ok) { setHidden(prev); setError(await errText(res, 'Could not update group visibility')) }
  }
  // Hiding is confirmed (it can tuck a group - and any hosts that live only in it - out of everyone's
  // view); unhiding is an obvious, safe one-click undo so it skips the prompt.
  async function hideGroup(node: GNode) {
    const ok = await confirm({
      title: 'Hide from tree?',
      message: `Hide “${node.path}” from the monitoring tree? The group stays in Zabbix — use “Show hidden” in the toolbar to bring it back.`,
      confirmLabel: 'Hide',
    })
    if (ok) setGroupHidden(node.path, true)
  }
  // Breadcrumb chain for a group path: walk real parents up to the top-level node.
  function crumbChain(path: string): { path: string; label: string }[] {
    const out: { path: string; label: string }[] = []
    let p: string | undefined = path
    while (p) { const n = byPath.get(p); if (!n) { out.unshift({ path: p, label: p }); break } out.unshift({ path: n.path, label: n.name }); p = n.parentPath }
    return out
  }

  // Rolled-up hosts of a node (its own + all descendants), de-duplicated - drives the node's host
  // count and worst-state dot.
  function subtreeHosts(n: GNode): Host[] {
    const seen = new Set<string>(); const out: Host[] = []
    const walk = (x: GNode) => { for (const h of x.hosts) if (!seen.has(h.id)) { seen.add(h.id); out.push(h) } for (const c of x.children) walk(c) }
    walk(n); return out
  }
  function nodeWorst(hs: Host[]): string { let s = 'ok'; for (const h of hs) if (!h.paused && !h.hidden && stateRank[h.state] > stateRank[s]) s = h.state; return s }
  function toggleNode(path: string) { setCollapsed((c) => { const n = new Set(c); n.has(path) ? n.delete(path) : n.add(path); return n }) }

  const focusHostId = focus.level === 'host' || focus.level === 'sensor' ? focus.hostId : null
  const focusItemId = focus.level === 'sensor' ? focus.itemId : null
  const focusHost = focusHostId ? hosts.find((x) => x.id === focusHostId) : undefined
  const focusHostName = focusHost?.name || focusHostId || ''
  const indent = (d: number) => 16 + d * 18

  // Up/down arrows shown in reorder mode within a sibling set (nothing when the set has <2 members, or
  // for a viewer). Clicks stop propagation so they don't toggle/drill the row they sit on.
  function orderArrows(ids: string[], index: number, scope: string, kind: 'group' | 'host' | 'sibling') {
    if (!reorder || !canPause || ids.length < 2) return null
    return (
      <span className="ord-ctrl" onClick={(e) => e.stopPropagation()}>
        <button className="ord-btn" disabled={index === 0} title="Move up" onClick={() => moveWithin(ids, index, -1, scope, kind)}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2"><path d="M6 15l6-6 6 6" /></svg>
        </button>
        <button className="ord-btn" disabled={index === ids.length - 1} title="Move down" onClick={() => moveWithin(ids, index, 1, scope, kind)}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2"><path d="M6 9l6 6 6-6" /></svg>
        </button>
      </span>
    )
  }

  // Recursive host card: head (drill on name) + optional "Edit groups…" band + expanded sensor table.
  function renderHost(h: Host, path: string, depth: number, sibIds: string[] = [h.id], index = 0) {
    const key = path + '::' + h.id
    const hopen = openHost === key || focusHostId === h.id
    return (
      <div className="host" key={key}>
        <div className="host-head" style={{ paddingLeft: indent(depth) }} onClick={() => { const next = hopen ? null : key; setOpenHost(next); onNavigate(next ? h.id : null, null) }}>
          <svg className={'chev' + (hopen ? ' open' : '')} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M9 6l6 6-6 6" /></svg>
          <span style={{ width: 9, height: 9, borderRadius: '50%', flexShrink: 0, background: dotColor(h.paused, h.hidden, h.state) }} />
          <span className="hn lnk-host" onClick={(e) => { e.stopPropagation(); drillHost(path, h.id) }}>{h.name}</span>
          {h.paused && <span className="kind" style={{ color: PAUSED_BLUE }}>· paused {untilLabel(h.paused_until)}</span>}
          {h.hidden && <span className="kind" style={{ color: HIDDEN_GREY }}>· hidden {untilLabel(h.hidden_until)}</span>}
          <div className="right">
            {!h.paused && !h.hidden && h.problems > 0 && <span style={{ color: stateColor[h.state], fontSize: 12 }}>{h.problems} problem{h.problems === 1 ? '' : 's'}</span>}
            {orderArrows(sibIds, index, path, 'sibling')}
            {canPause && !reorder && (
              <Kebab disabled={busyId === h.id} actions={[
                h.paused ? { label: 'Resume', icon: kbIcon.resume, onClick: () => clearHostState(h, 'pause') } : { label: 'Pause', icon: kbIcon.pause, onPick: (s) => setHostState(h, 'pause', s) },
                h.hidden ? { label: 'Show', icon: kbIcon.show, onClick: () => clearHostState(h, 'hide') } : { label: 'Hide', icon: kbIcon.hide, onPick: (s) => setHostState(h, 'hide', s) },
                { sep: true, label: '' },
                { label: 'Settings…', icon: kbIcon.gear, onClick: () => { setEditGroupsHost(null); setSettingsHost((cur) => (cur === h.id ? null : h.id)) } },
                { label: 'Edit groups…', icon: kbIcon.folder, onClick: () => { setSettingsHost(null); setEditGroupsHost((cur) => (cur === h.id ? null : h.id)) } },
              ]} />
            )}
          </div>
        </div>
        {settingsHost === h.id && <HostSettings hostId={h.id} canEdit={canPause} onClose={() => setSettingsHost(null)} onSaved={() => { setSettingsHost(null); load(); fireDataRefresh() }} />}
        {editGroupsHost === h.id && <GroupEditor current={h.groups || []} groups={groups} onSave={(ids) => setHostGroups(h.id, ids)} onCancel={() => setEditGroupsHost(null)} />}
        {hopen && <div className="host-body" style={{ paddingLeft: indent(depth) }}><HostItems hostId={h.id} canPause={canPause} hostPaused={h.paused} hostHidden={h.hidden} showAll={showAllEff} autoOpenItem={target && target.hostId === h.id ? target.itemId : undefined} onlyItem={focus.level === 'sensor' && focus.hostId === h.id ? focusItemId ?? undefined : undefined} onDrillSensor={(itemId, itemName) => drillSensor(path, h.id, itemId, itemName)} onItemName={(itemId, itemName) => setFocus((f) => (f.level === 'sensor' && f.itemId === itemId && !f.itemName ? { ...f, itemName } : f))} onNavigate={onNavigate} /></div>}
      </div>
    )
  }

  // Recursive group node: header (drill on name, kebab New subgroup/Rename/Delete) + inline bands +
  // this node's own direct hosts (rendered at the same indent as, and above, the child subgroups, so a
  // host that belongs to this group isn't mistaken for a member of one of its subgroups).
  function renderNode(node: GNode, depth: number, sibIds: string[] = [node.path], index = 0, scope = node.parentPath ?? '') {
    const sub = subtreeHosts(node)
    const expanded = (focus.level === 'group' && focus.path === node.path) ? true : !collapsed.has(node.path)
    const g = node.group
    const isHidden = hidden.has(node.path)
    return (
      <div className={'site' + (isHidden ? ' ghost' : '')} key={node.path}>
        <div className="site-head" style={{ paddingLeft: indent(depth) }} onClick={() => toggleNode(node.path)}>
          <svg className={'chev' + (expanded ? ' open' : '')} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M9 6l6 6-6 6" /></svg>
          <span className="name lnk-host" onClick={(e) => { e.stopPropagation(); drillGroup(node.path) }}>{node.name}</span>
          {isHidden && <span className="tag-hidden">hidden</span>}
          <span className="loc">{sub.length} host{sub.length === 1 ? '' : 's'}</span>
          <div className="right">
            <span style={{ width: 9, height: 9, borderRadius: '50%', background: stateColor[nodeWorst(sub)] || 'var(--muted)' }} />
            {orderArrows(sibIds, index, scope, 'sibling')}
            {canPause && g && !reorder && (
              <Kebab actions={[
                { label: 'New subgroup…', icon: kbIcon.folder, onClick: () => { setError(null); setGAction(null); setNewSubPath(node.path); setCollapsed((c) => { const n = new Set(c); n.delete(node.path); return n }) } },
                { label: 'Rename…', icon: kbIcon.edit, onClick: () => { setError(null); setNewSubPath(null); setGAction({ id: g.id, mode: 'rename' }) } },
                { label: 'Delete', icon: kbIcon.trash, danger: true, onClick: () => { setError(null); setNewSubPath(null); if (node.hosts.length) { setError(`Move the ${node.hosts.length} host${node.hosts.length === 1 ? '' : 's'} out of "${node.path}" before deleting it.`); return } setGAction({ id: g.id, mode: 'delete' }) } },
                // Hide/unhide is an admin, advanced-mode capability (unhiding needs "Show hidden", which
                // only appears in advanced mode) - so the hide action only shows there too.
                ...(advanced ? [
                  { sep: true, label: '' },
                  hidden.has(node.path)
                    ? { label: 'Show in tree', icon: kbIcon.show, onClick: () => setGroupHidden(node.path, false) }
                    : { label: 'Hide from tree', icon: kbIcon.hide, onClick: () => hideGroup(node) },
                ] : []),
              ]} />
            )}
          </div>
        </div>
        {g && gAction?.id === g.id && (
          gAction.mode === 'rename'
            ? <GroupNameBand initial={node.path} placeholder="Group path" confirmLabel="Rename" onConfirm={(v) => renameGroup(g, v)} onCancel={() => setGAction(null)} />
            : <div className="group-band">
                <span className="gb-msg">Delete the group “{node.path}”? This can’t be undone.</span>
                <div className="gb-foot">
                  <Button variant="ghost" onClick={() => setGAction(null)}>Cancel</Button>
                  <Button variant="danger" onClick={() => deleteGroup(g)}>Delete</Button>
                </div>
              </div>
        )}
        {newSubPath === node.path && <GroupNameBand prefix={node.path + '/'} placeholder="Subgroup name" confirmLabel="Create" onConfirm={(v) => createGroup(v)} onCancel={() => setNewSubPath(null)} />}
        {expanded && (() => {
          const kids = mergedChildren(node.hosts, node.children, node.path)
          const keys = kids.map((k) => k.key)
          return kids.map((k, i) => k.host
            ? renderHost(k.host, node.path, depth + 1, keys, i)
            : renderNode(k.group!, depth + 1, keys, i, node.path))
        })()}
      </div>
    )
  }

  // What to render: root -> all top-level nodes; group focus -> just that node's subtree; host/sensor
  // focus -> only the focused host card (the breadcrumb carries the path).
  const focusNode = focus.level === 'group' ? byPath.get(focus.path) : undefined
  const crumbs = focus.level !== 'root' ? crumbChain(focus.path) : []

  return (
    <div className="panel">
      <div className="phead">
        <h2>Sites &amp; hosts</h2>
        <span className="hint">{(showHiddenEff ? groups.length : groups.filter((g) => !hidden.has(g.name)).length)} group{groups.length === 1 ? '' : 's'}{!showHiddenEff && hidden.size > 0 ? ` (${hidden.size} hidden)` : ''} · {hosts.length} host{hosts.length === 1 ? '' : 's'}</span>
        {focus.level !== 'sensor' && (
          <div className="tools">
            {canPause && focus.level !== 'host' && !reorder && <button className="btn primary" onClick={() => { setError(null); setCreating((v) => !v) }}>+ New group</button>}
            {role === 'admin' && (focus.level === 'root' || focus.level === 'group') && !reorder && <button className="btn primary" onClick={() => { setError(null); setCreating(false); setAddingDevice((v) => !v) }}>+ Add device</button>}
            {/* Desktop: the secondary controls inline. Phone: the same actions in a ⋯ menu (plus a visible
                Done while reordering), so the toolbar stays one row on a narrow card. */}
            <span className="tools-desktop">
              {advanced && canPause && focus.level !== 'host' && hidden.size > 0 && !reorder && <button className={'btn' + (showHidden ? ' on' : '')} onClick={() => setShowHidden((v) => !v)}>{showHidden ? 'Hide hidden' : `Show hidden (${hidden.size})`}</button>}
              {canPause && focus.level !== 'host' && <button className={'btn' + (reorder ? ' on' : '')} onClick={() => { setError(null); setCreating(false); setReorder((v) => !v) }}>{reorder ? 'Done' : 'Reorder'}</button>}
              {advanced && (
                <div className="seg">
                  <button className={!showAll ? 'on' : ''} onClick={() => setShowAll(false)}>Key sensors</button>
                  <button className={showAll ? 'on' : ''} onClick={() => setShowAll(true)}>All sensors</button>
                </div>
              )}
            </span>
            {reorder && <button className="btn on tools-mobile" onClick={() => setReorder(false)}>Done</button>}
            {(canPause || advanced) && focus.level !== 'host' && (
              <span className="tools-mobile">
                <Kebab actions={[
                  ...(canPause ? [{ label: reorder ? 'Done reordering' : 'Reorder groups & hosts', onClick: () => { setError(null); setCreating(false); setReorder((v) => !v) } }] : []),
                  ...(advanced && canPause && hidden.size > 0 ? [{ label: showHidden ? 'Hide hidden groups' : `Show hidden groups (${hidden.size})`, onClick: () => setShowHidden((v) => !v) }] : []),
                  ...(advanced ? [{ label: showAll ? 'Show key sensors only' : 'Show all sensors', onClick: () => setShowAll((v) => !v) }] : []),
                ]} />
              </span>
            )}
          </div>
        )}
      </div>
      {focus.level !== 'root' && (
        <div className="crumbs">
          <span className="crumb" onClick={drillRoot}>Sites &amp; hosts</span>
          {crumbs.map((c, i) => {
            const isCur = focus.level === 'group' && i === crumbs.length - 1
            return <Fragment key={c.path}><span className="sep">/</span>{isCur ? <span className="crumb cur">{c.label}</span> : <span className="crumb" onClick={() => drillGroup(c.path)}>{c.label}</span>}</Fragment>
          })}
          {(focus.level === 'host' || focus.level === 'sensor') && <>
            <span className="sep">/</span>
            {focus.level === 'host'
              ? <span className="crumb cur">{focusHostName}</span>
              : <span className="crumb" onClick={() => drillHost(focus.path, focus.hostId)}>{focusHostName}</span>}
          </>}
          {focus.level === 'sensor' && <>
            <span className="sep">/</span>
            <span className="crumb cur">{focus.itemName || 'Sensor'}</span>
          </>}
        </div>
      )}
      {creating && <GroupNameBand placeholder="New group name (use / for nesting, e.g. mybz/Network)" confirmLabel="Create" onConfirm={(name) => createGroup(name)} onCancel={() => setCreating(false)} />}
      {addingDevice && <AddDeviceBand classes={classes} groups={groups} proxies={proxies} defaultSite={focus.level === 'group' ? focus.path : ''} onCancel={() => setAddingDevice(false)} onCreated={() => { setAddingDevice(false); setError(null); load(); fireDataRefresh() }} />}
      {loading && <Skeleton rows={5} cols={3} />}
      {error && <div style={{ padding: '0.9rem 16px', color: 'var(--err)' }}>{error}</div>}
      {!loading && !error && hosts.length === 0 && <EmptyState icon={ic.monitoring} title="No hosts yet" text="Hosts monitored in Zabbix appear here, grouped by site. If you expected some, check the Zabbix connection in Settings." />}
      <div className="tree">
        {focus.level === 'root' && (() => { const kids = mergedChildren([], roots, ''); const keys = kids.map((k) => k.key); return kids.map((k, i) => renderNode(k.group!, 0, keys, i, '')) })()}
        {focus.level === 'group' && (focusNode ? renderNode(focusNode, 0) : <div style={{ padding: '0.9rem 16px', color: 'var(--muted)' }}>This group no longer exists.</div>)}
        {(focus.level === 'host' || focus.level === 'sensor') && (focusHost ? renderHost(focusHost, focus.path, 0) : null)}
      </div>
    </div>
  )
}

// GroupNameBand is the inline name editor used for "New group" and "Rename group" (replacing the
// browser prompt): a text field with Enter-to-confirm / Esc-to-cancel and explicit buttons.
function GroupNameBand({ initial = '', prefix, placeholder, confirmLabel, onConfirm, onCancel }: { initial?: string; prefix?: string; placeholder?: string; confirmLabel: string; onConfirm: (name: string) => void | Promise<void>; onCancel: () => void }) {
  const [v, setV] = useState(initial)
  const [busy, setBusy] = useState(false)
  const submit = async () => { if (!v.trim() || busy) return; setBusy(true); await onConfirm((prefix || '') + v); setBusy(false) }
  return (
    <div className="group-band">
      {prefix && <span className="gb-prefix">{prefix}</span>}
      <input className="input" autoFocus placeholder={placeholder} value={v}
        onChange={(e) => setV(e.target.value)}
        onKeyDown={(e) => { if (e.key === 'Enter') submit(); else if (e.key === 'Escape') onCancel() }} />
      <div className="gb-foot">
        <Button variant="ghost" onClick={onCancel} disabled={busy}>Cancel</Button>
        <Button variant="primary" onClick={submit} disabled={!v.trim() || busy}>{confirmLabel}</Button>
      </div>
    </div>
  )
}

// AddDeviceBand is the inline "+ Add device" form (admin): pick a device class, name it, place it in a
// site + proxy, and (optionally) add the HTTP/HTTPS check. It POSTs /api/hosts, which creates the
// Zabbix host wired to the class's templates (Base Ping is always attached). The minimal manual-attach
// path (ROADMAP §C, phase C0) the discovery pipeline (§B) later automates.
function AddDeviceBand({ classes, groups, proxies, defaultSite, onCancel, onCreated }: { classes: DeviceClass[]; groups: Group[]; proxies: Proxy[]; defaultSite: string; onCancel: () => void; onCreated: (hostId: string) => void }) {
  const [classId, setClassId] = useState('base')
  const [name, setName] = useState('')
  const [ip, setIp] = useState('')
  const [dns, setDns] = useState('')
  const [useIp, setUseIp] = useState(true)
  const [site, setSite] = useState(defaultSite || '')
  const [proxyId, setProxyId] = useState('')
  const [http, setHttp] = useState(false)
  const [httpScheme, setHttpScheme] = useState('https')
  const [httpPort, setHttpPort] = useState('')
  const [snmpVersion, setSnmpVersion] = useState(2)
  const [community, setCommunity] = useState('public')
  const [snmpPort, setSnmpPort] = useState('161')
  const [snmpOverride, setSnmpOverride] = useState(false) // enter creds for this host instead of inheriting
  const [proxySnmp, setProxySnmp] = useState<{ set: boolean } | null>(null) // does the chosen proxy have a default?
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const cls = classes.find((c) => c.id === classId)
  const needsSnmp = cls?.iface === 'snmp'
  const offersHttp = !!cls?.offers_http
  const proxyName = proxies.find((p) => p.id === proxyId)?.name || 'the proxy'
  const canInherit = needsSnmp && proxyId !== '' && !!proxySnmp?.set
  const showSnmpFields = needsSnmp && proxySnmp !== null && (!canInherit || snmpOverride)

  // For an SNMP class, check whether the chosen proxy has an SNMP default to inherit, so the form can
  // hide the credential fields (the common case) and only ask when overriding or when none is set.
  useEffect(() => {
    if (!needsSnmp || proxyId === '') { setProxySnmp({ set: false }); return }
    setProxySnmp(null)
    fetch(`/api/proxies/${encodeURIComponent(proxyId)}/snmp`).then((r) => (r.ok ? r.json() : { set: false })).then((d) => setProxySnmp({ set: !!d.set })).catch(() => setProxySnmp({ set: false }))
  }, [needsSnmp, proxyId])

  async function submit() {
    if (busy) return
    if (!name.trim()) { setErr('A host name is required'); return }
    if (!site.trim()) { setErr('Pick a site'); return }
    if (useIp && !ip.trim()) { setErr('An IP address is required'); return }
    if (!useIp && !dns.trim()) { setErr('A DNS name is required'); return }
    if (showSnmpFields && !community.trim()) { setErr('An SNMP community is required'); return }
    setBusy(true); setErr(null)
    const body: Record<string, unknown> = { name: name.trim(), ip: ip.trim(), dns: dns.trim(), use_ip: useIp, site: site.trim(), proxy_id: proxyId, class_id: classId }
    if (offersHttp && http) { body.http = true; body.http_scheme = httpScheme; if (httpPort.trim()) body.http_port = httpPort.trim() }
    // Omit snmp to inherit the proxy default; send it only when overriding or no default exists.
    if (showSnmpFields) body.snmp = { version: snmpVersion, community: community.trim(), port: snmpPort.trim() || '161' }
    const res = await fetch('/api/hosts', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { setErr(await errText(res, 'Could not create the device')); return }
    const d = await res.json().catch(() => ({} as { id?: string }))
    onCreated(d.id || '')
  }

  const grid: CSSProperties = { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))', gap: '0.7rem' }
  return (
    <div style={{ margin: '8px 16px', padding: '12px 14px', background: 'var(--elevated)', border: '1px solid var(--border)', borderRadius: 8, display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
      {classes.length === 0 ? <Banner variant="info">Loading device classes…</Banner> : <>
        <div style={grid}>
          <Field label="Device class"><Select value={classId} onChange={(e) => setClassId(e.target.value)}>{classes.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}</Select></Field>
          <Field label="Name" placeholder="e.g. core-switch-01" value={name} onChange={(e) => setName(e.target.value)} />
          <Field label="Site"><Select value={site} onChange={(e) => setSite(e.target.value)}><option value="">Choose a site…</option>{groups.map((g) => <option key={g.id} value={g.name}>{g.name}</option>)}</Select></Field>
          <Field label="Monitored by"><Select value={proxyId} onChange={(e) => setProxyId(e.target.value)}><option value="">Core server</option>{proxies.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</Select></Field>
        </div>
        <div style={grid}>
          <Field label={useIp ? 'IP address' : 'DNS name'} placeholder={useIp ? '10.0.0.10' : 'host.example.lan'} value={useIp ? ip : dns} onChange={(e) => (useIp ? setIp(e.target.value) : setDns(e.target.value))} />
          {/* Blank label + input-height box so the toggle lines up with the address input, not its label. */}
          <Field label={' '}><div style={{ display: 'flex', alignItems: 'center', minHeight: 37 }}><Switch checked={useIp} onChange={setUseIp} label={useIp ? 'Connect by IP' : 'Connect by DNS'} /></div></Field>
        </div>
        {needsSnmp && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.55rem' }}>
            {proxySnmp === null && <span style={{ fontSize: 13, color: 'var(--muted)' }}>Checking {proxyName}'s SNMP settings…</span>}
            {canInherit && (
              <div style={{ display: 'flex', alignItems: 'center', gap: '0.9rem', flexWrap: 'wrap' }}>
                <span style={{ fontSize: 13, color: 'var(--muted)' }}>Uses {proxyName}'s SNMP settings.</span>
                <Switch checked={snmpOverride} onChange={setSnmpOverride} label="Override for this host" />
              </div>
            )}
            {proxySnmp !== null && !proxySnmp.set && (
              <span style={{ fontSize: 13, color: 'var(--muted)' }}>{proxyId === '' ? 'Hosts on the core server have' : `${proxyName} has`} no SNMP default — enter settings below{proxyId !== '' ? ', or set one in Probes to reuse it' : ''}.</span>
            )}
            {showSnmpFields && (
              <div style={grid}>
                <Field label="SNMP version"><Select value={String(snmpVersion)} onChange={(e) => setSnmpVersion(Number(e.target.value))}><option value="1">v1</option><option value="2">v2c</option></Select></Field>
                <Field label="Community" value={community} onChange={(e) => setCommunity(e.target.value)} />
                <Field label="SNMP port" value={snmpPort} onChange={(e) => setSnmpPort(e.target.value)} />
              </div>
            )}
          </div>
        )}
        {offersHttp && (
          <div style={{ display: 'flex', gap: '0.9rem', alignItems: 'center', flexWrap: 'wrap' }}>
            <Switch checked={http} onChange={setHttp} label="Also check HTTP/HTTPS" />
            {http && <>
              <Select value={httpScheme} onChange={(e) => setHttpScheme(e.target.value)} style={{ width: 'auto' }}><option value="https">HTTPS</option><option value="http">HTTP</option></Select>
              <input className="input" style={{ width: 120 }} placeholder="port (443)" value={httpPort} onChange={(e) => setHttpPort(e.target.value)} />
            </>}
          </div>
        )}
        {err && <Banner variant="error">{err}</Banner>}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <Button variant="ghost" onClick={onCancel} disabled={busy}>Cancel</Button>
          <Button variant="primary" onClick={submit} disabled={busy}>{busy ? 'Creating…' : 'Add device'}</Button>
        </div>
      </>}
    </div>
  )
}

const IFTYPE: Record<number, string> = { 1: 'Agent', 2: 'SNMP', 3: 'IPMI', 4: 'JMX' }
function blankSnmp(): SnmpCfg { return { version: 2, community: 'public', bulk: 1, security_name: '', security_level: 0, auth_protocol: 0, auth_passphrase: '', priv_protocol: 0, priv_passphrase: '', context_name: '' } }

// HostSettings is the inline band under a host row for editing its identity + interfaces (Zabbix
// host.update + hostinterface CRUD). One "Save" reconciles the whole desired state on the server.
function HostSettings({ hostId, canEdit, onClose, onSaved }: { hostId: string; canEdit: boolean; onClose: () => void; onSaved: () => void }) {
  const confirm = useConfirm()
  const [cfg, setCfg] = useState<HostCfg | null>(null)
  const [proxies, setProxies] = useState<Proxy[]>([])
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    fetch(`/api/hosts/${hostId}/config`).then((r) => (r.ok ? r.json() : Promise.reject())).then(setCfg).catch(() => setErr('Could not load host settings'))
    fetch('/api/proxies').then((r) => (r.ok ? r.json() : [])).then((p) => setProxies(p || [])).catch(() => {})
  }, [hostId])

  function patch(p: Partial<HostCfg>) { setCfg((c) => (c ? { ...c, ...p } : c)) }
  function setIface(idx: number, p: Partial<Iface>) { setCfg((c) => (c ? { ...c, interfaces: c.interfaces.map((i, n) => (n === idx ? { ...i, ...p } : i)) } : c)) }
  function setSnmp(idx: number, p: Partial<SnmpCfg>) { setCfg((c) => (c ? { ...c, interfaces: c.interfaces.map((i, n) => (n === idx ? { ...i, snmp: { ...(i.snmp || blankSnmp()), ...p } } : i)) } : c)) }
  function addIface(type: number) { setCfg((c) => (c ? { ...c, interfaces: [...c.interfaces, { type, useip: 1, ip: '', dns: '', port: type === 2 ? '161' : '10050', snmp: type === 2 ? blankSnmp() : undefined, inherit: type === 2 ? !!c.proxy_default : undefined }] } : c)) }
  async function removeIface(idx: number) {
    const it = cfg?.interfaces[idx]
    if (it?.interfaceid && !(await confirm({ title: 'Remove interface', message: 'Remove this interface? Any checks still using it will be moved to another interface on this host. If a check needs an interface of the same type (e.g. a Zabbix-agent check), the removal is refused and nothing changes.', confirmLabel: 'Remove', danger: true }))) return
    setCfg((c) => (c ? { ...c, interfaces: c.interfaces.filter((_, n) => n !== idx) } : c))
  }
  async function save() {
    if (!cfg) return
    setBusy(true); setErr(null)
    const res = await fetch(`/api/hosts/${hostId}/config`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ host: cfg.host, name: cfg.name, monitored_by: cfg.monitored_by, proxy_id: cfg.proxy_id, interfaces: cfg.interfaces }) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { setErr(await errText(res, 'Could not save host settings')); return }
    onSaved()
  }

  if (err && !cfg) return <div className="host-settings"><div style={{ color: 'var(--err)', fontSize: 13 }}>{err}</div><div className="hs-foot"><Button variant="ghost" onClick={onClose}>Close</Button></div></div>
  if (!cfg) return <div className="host-settings"><span style={{ color: 'var(--muted)', fontSize: 13 }}>Loading…</span></div>

  return (
    <div className="host-settings">
      <div className="hs-grid">
        <label className="field"><span>Visible name</span><input className="input" value={cfg.name} disabled={!canEdit} onChange={(e) => patch({ name: e.target.value })} /></label>
        <label className="field"><span>Technical name</span><input className="input" value={cfg.host} disabled={!canEdit} onChange={(e) => patch({ host: e.target.value })} /></label>
      </div>
      <div className="hs-note">Renaming the technical name is safe in Zabbix (references update automatically) — avoid it only if external scripts reference this host.</div>

      <div className="hs-mon">
        <span className="hs-monlabel">Monitored by</span>
        <div className="seg">
          <button className={cfg.monitored_by === 0 ? 'on' : ''} disabled={!canEdit} onClick={() => patch({ monitored_by: 0 })}>Server</button>
          <button className={cfg.monitored_by === 1 ? 'on' : ''} disabled={!canEdit} onClick={() => patch({ monitored_by: 1, proxy_id: cfg.proxy_id || proxies[0]?.id })}>Proxy</button>
        </div>
        {cfg.monitored_by === 1 && (
          <select className="input" value={cfg.proxy_id || ''} disabled={!canEdit} onChange={(e) => patch({ proxy_id: e.target.value })}>
            <option value="" disabled>Select a proxy…</option>
            {proxies.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
        )}
      </div>

      <div className="hs-title">Interfaces</div>
      {cfg.interfaces.length === 0 && <div style={{ color: 'var(--muted)', fontSize: 13 }}>No interfaces.</div>}
      {cfg.interfaces.map((i, idx) => (
        <div className="iface-row" key={i.interfaceid || 'new' + idx}>
          <div className="if-head">
            <span className="if-type">{IFTYPE[i.type] || 'Type ' + i.type}</span>
            <div className="seg">
              <button className={i.useip === 1 ? 'on' : ''} disabled={!canEdit} onClick={() => setIface(idx, { useip: 1 })}>IP</button>
              <button className={i.useip === 0 ? 'on' : ''} disabled={!canEdit} onClick={() => setIface(idx, { useip: 0 })}>DNS</button>
            </div>
            {canEdit && <button className="btn ghost if-remove" onClick={() => removeIface(idx)}>Remove</button>}
          </div>
          <div className="if-fields">
            <label className="field"><span>IP address</span><input className="input" value={i.ip} disabled={!canEdit} onChange={(e) => setIface(idx, { ip: e.target.value })} /></label>
            <label className="field"><span>DNS name</span><input className="input" value={i.dns} disabled={!canEdit} onChange={(e) => setIface(idx, { dns: e.target.value })} /></label>
            <label className="field"><span>Port</span><input className="input" value={i.port} disabled={!canEdit} onChange={(e) => setIface(idx, { port: e.target.value })} /></label>
          </div>
          {i.type === 2 && (
            <div className="if-snmp">
              {cfg.monitored_by === 1 && (
                <label className="field if-inherit"><span>SNMP credentials</span>
                  <div className="seg">
                    <button className={i.inherit ? 'on' : ''} disabled={!canEdit || !cfg.proxy_default} onClick={() => setIface(idx, { inherit: true })}>Inherit from {cfg.proxy_name || 'proxy'}</button>
                    <button className={!i.inherit ? 'on' : ''} disabled={!canEdit} onClick={() => setIface(idx, { inherit: false })}>Override</button>
                  </div>
                </label>
              )}
              {cfg.monitored_by === 1 && !cfg.proxy_default && <div className="if-inherit-note">No SNMP default is set for {cfg.proxy_name || 'this proxy'} yet — set one in the Probes tab (its “Defaults” button) to enable inheritance.</div>}
              {i.inherit
                ? <div className="if-inherit-note">Using {cfg.proxy_name || 'the proxy'}’s SNMP default{cfg.proxy_default ? ` (v${cfg.proxy_default.version === 2 ? '2c' : cfg.proxy_default.version}${cfg.proxy_default.version !== 3 ? `, community “${cfg.proxy_default.community}”` : ''})` : ''} — change it in the Probes tab.</div>
                : <>
              <label className="field"><span>SNMP version</span>
                <select className="input" value={i.snmp?.version ?? 2} disabled={!canEdit} onChange={(e) => setSnmp(idx, { version: Number(e.target.value) })}>
                  <option value={1}>v1</option><option value={2}>v2c</option><option value={3}>v3</option>
                </select>
              </label>
              {(i.snmp?.version ?? 2) !== 3
                ? <label className="field"><span>Community</span><input className="input" value={i.snmp?.community || ''} disabled={!canEdit} onChange={(e) => setSnmp(idx, { community: e.target.value })} /></label>
                : <>
                    <label className="field"><span>Security name</span><input className="input" value={i.snmp?.security_name || ''} disabled={!canEdit} onChange={(e) => setSnmp(idx, { security_name: e.target.value })} /></label>
                    <label className="field"><span>Security level</span>
                      <select className="input" value={i.snmp?.security_level ?? 0} disabled={!canEdit} onChange={(e) => setSnmp(idx, { security_level: Number(e.target.value) })}>
                        <option value={0}>noAuthNoPriv</option><option value={1}>authNoPriv</option><option value={2}>authPriv</option>
                      </select>
                    </label>
                    <label className="field"><span>Auth protocol</span>
                      <select className="input" value={i.snmp?.auth_protocol ?? 0} disabled={!canEdit} onChange={(e) => setSnmp(idx, { auth_protocol: Number(e.target.value) })}>
                        <option value={0}>MD5</option><option value={1}>SHA1</option><option value={3}>SHA256</option>
                      </select>
                    </label>
                    <label className="field"><span>Auth passphrase</span><input className="input" type="password" placeholder="unchanged" value={i.snmp?.auth_passphrase || ''} disabled={!canEdit} onChange={(e) => setSnmp(idx, { auth_passphrase: e.target.value })} /></label>
                    <label className="field"><span>Priv protocol</span>
                      <select className="input" value={i.snmp?.priv_protocol ?? 0} disabled={!canEdit} onChange={(e) => setSnmp(idx, { priv_protocol: Number(e.target.value) })}>
                        <option value={0}>DES</option><option value={1}>AES128</option><option value={3}>AES256</option>
                      </select>
                    </label>
                    <label className="field"><span>Priv passphrase</span><input className="input" type="password" placeholder="unchanged" value={i.snmp?.priv_passphrase || ''} disabled={!canEdit} onChange={(e) => setSnmp(idx, { priv_passphrase: e.target.value })} /></label>
                  </>}
                </>}
            </div>
          )}
        </div>
      ))}
      {canEdit && <div className="hs-add"><button className="btn" onClick={() => addIface(1)}>+ Agent interface</button><button className="btn" onClick={() => addIface(2)}>+ SNMP interface</button></div>}

      {err && <div style={{ color: 'var(--err)', fontSize: 13, marginTop: 8 }}>{err}</div>}
      <div className="hs-foot">
        <Button variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button>
        {canEdit && <Button variant="primary" onClick={save} disabled={busy || !cfg.host.trim()}>Save</Button>}
      </div>
    </div>
  )
}

// ProxySNMP is the per-proxy SNMP-defaults band in the Probes tab. Saving stores the default and
// propagates it to every host on the proxy whose SNMP interface is set to inherit.
function ProxySNMP({ proxyId, proxyName, onClose }: { proxyId: string; proxyName: string; onClose: () => void }) {
  const confirm = useConfirm()
  const toast = useToast()
  const [snmp, setSnmp] = useState<SnmpCfg | null>(null)
  const [isSet, setIsSet] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    fetch(`/api/proxies/${proxyId}/snmp`).then((r) => (r.ok ? r.json() : Promise.reject())).then((d) => { setSnmp(d.snmp); setIsSet(!!d.set) }).catch(() => setErr('Could not load the SNMP default'))
  }, [proxyId])
  function set(p: Partial<SnmpCfg>) { setSnmp((s) => (s ? { ...s, ...p } : s)) }
  async function save() {
    if (!snmp) return
    setBusy(true); setErr(null)
    const res = await fetch(`/api/proxies/${proxyId}/snmp`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(snmp) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not save the SNMP default')); return }
    const d = await res.json().catch(() => ({} as { updated?: number; overrides?: number; warning?: string }))
    setIsSet(true)
    const base = `SNMP default saved${typeof d.updated === 'number' ? ` — updated ${d.updated} inheriting host${d.updated === 1 ? '' : 's'}` : ''}${d.warning ? ` (${d.warning})` : ''}`
    toast.success(base)
    // Offer to switch existing per-host (override) SNMP interfaces on this proxy to inherit this default.
    if (d.overrides && d.overrides > 0) {
      const n = d.overrides
      if (await confirm({ title: 'Switch existing hosts to inherit?', message: `${n} SNMP interface${n === 1 ? '' : 's'} on ${proxyName}'s hosts ${n === 1 ? 'has its' : 'have their'} own credentials. Switch ${n === 1 ? 'it' : 'them'} to inherit this default too?`, confirmLabel: 'Switch to inherit' })) {
        const ar = await fetch(`/api/proxies/${proxyId}/snmp/adopt`, { method: 'POST' }).catch(() => null)
        if (!ar || !ar.ok) { toast.error(await errText(ar, 'Could not switch the overrides')); return }
        const ad = await ar.json().catch(() => ({} as { adopted?: number }))
        toast.success(`Switched ${ad.adopted ?? 0} host${ad.adopted === 1 ? '' : 's'} to inherit the default.`)
      }
    }
  }
  if (err && !snmp) return <div className="host-settings"><div style={{ color: 'var(--err)', fontSize: 13 }}>{err}</div></div>
  if (!snmp) return <div className="host-settings"><span style={{ color: 'var(--muted)', fontSize: 13 }}>Loading…</span></div>
  return (
    <div className="host-settings snmp-band">
      <div className="hs-title">SNMP default · {proxyName}</div>
      <div className="hs-note">Hosts on this proxy set to “inherit” use these credentials. Saving applies them to every inheriting host.{isSet ? '' : ' No default is set yet.'}</div>
      <div className="if-snmp" style={{ borderTop: 'none', marginTop: 4, paddingTop: 0 }}>
        <label className="field"><span>SNMP version</span>
          <select className="input" value={snmp.version} onChange={(e) => set({ version: Number(e.target.value) })}>
            <option value={1}>v1</option><option value={2}>v2c</option><option value={3}>v3</option>
          </select>
        </label>
        {snmp.version !== 3
          ? <label className="field"><span>Community</span><input className="input" value={snmp.community || ''} onChange={(e) => set({ community: e.target.value })} /></label>
          : <>
              <label className="field"><span>Security name</span><input className="input" value={snmp.security_name || ''} onChange={(e) => set({ security_name: e.target.value })} /></label>
              <label className="field"><span>Security level</span>
                <select className="input" value={snmp.security_level} onChange={(e) => set({ security_level: Number(e.target.value) })}>
                  <option value={0}>noAuthNoPriv</option><option value={1}>authNoPriv</option><option value={2}>authPriv</option>
                </select>
              </label>
              <label className="field"><span>Auth protocol</span>
                <select className="input" value={snmp.auth_protocol} onChange={(e) => set({ auth_protocol: Number(e.target.value) })}>
                  <option value={0}>MD5</option><option value={1}>SHA1</option><option value={3}>SHA256</option>
                </select>
              </label>
              <label className="field"><span>Auth passphrase</span><input className="input" type="password" placeholder="unchanged" value={snmp.auth_passphrase || ''} onChange={(e) => set({ auth_passphrase: e.target.value })} /></label>
              <label className="field"><span>Priv protocol</span>
                <select className="input" value={snmp.priv_protocol} onChange={(e) => set({ priv_protocol: Number(e.target.value) })}>
                  <option value={0}>DES</option><option value={1}>AES128</option><option value={3}>AES256</option>
                </select>
              </label>
              <label className="field"><span>Priv passphrase</span><input className="input" type="password" placeholder="unchanged" value={snmp.priv_passphrase || ''} onChange={(e) => set({ priv_passphrase: e.target.value })} /></label>
            </>}
      </div>
      {err && <div style={{ color: 'var(--err)', fontSize: 13, marginTop: 8 }}>{err}</div>}
      <div className="hs-foot">
        <Button variant="ghost" onClick={onClose} disabled={busy}>Close</Button>
        <Button variant="primary" onClick={save} disabled={busy}>Save</Button>
      </div>
    </div>
  )
}

// GroupEditor is the inline band under a host row for moving it between tree groups: a checkbox per
// group (current membership pre-checked), enforcing at least one. Save replaces the host's full group
// set. Mirrors the inline-editor idiom (ChannelEditor) rather than a modal.
function GroupEditor({ current, groups, onSave, onCancel }: { current: string[]; groups: Group[]; onSave: (ids: string[]) => Promise<void> | void; onCancel: () => void }) {
  const [sel, setSel] = useState<Set<string>>(() => new Set(groups.filter((g) => current.includes(g.name)).map((g) => g.id)))
  const [busy, setBusy] = useState(false)
  function toggle(id: string) { setSel((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n }) }
  const sorted = [...groups].sort((a, b) => a.name.localeCompare(b.name))
  return (
    <div className="group-edit">
      <div className="ge-title">Groups for this host</div>
      {sorted.length === 0
        ? <div style={{ color: 'var(--muted)', fontSize: 13 }}>No groups yet - create one first.</div>
        : <div className="ge-list">
            {sorted.map((g) => (
              <label key={g.id} className="ge-row">
                <input type="checkbox" checked={sel.has(g.id)} onChange={() => toggle(g.id)} />
                <span>{g.name}</span>
              </label>
            ))}
          </div>}
      <div className="ge-foot">
        <Button variant="ghost" onClick={onCancel} disabled={busy}>Cancel</Button>
        <Button variant="primary" disabled={sel.size === 0 || busy} onClick={async () => { setBusy(true); await onSave([...sel]); setBusy(false) }}>Save</Button>
      </div>
    </div>
  )
}

function HostItems({ hostId, canPause, hostPaused, hostHidden, showAll, autoOpenItem, onlyItem, onDrillSensor, onItemName, onNavigate }: { hostId: string; canPause: boolean; hostPaused: boolean; hostHidden: boolean; showAll: boolean; autoOpenItem?: string; onlyItem?: string; onDrillSensor?: (itemId: string, itemName: string) => void; onItemName?: (itemId: string, itemName: string) => void; onNavigate: (hostId: string | null, itemId: string | null) => void }) {
  const [items, setItems] = useState<SensorItem[] | null>(null)
  const [problems, setProblems] = useState<Problem[]>([])
  const [error, setError] = useState<string | null>(null)
  const [openItem, setOpenItem] = useState<string | null>(null)

  function loadItems(reset = true) {
    if (reset) setItems(null)
    setError(null)
    fetch(`/api/hosts/${hostId}/items${showAll ? '?all=1' : ''}`)
      .then(async (r) => { if (!r.ok) throw new Error('items'); return r.json() })
      .then((its: SensorItem[]) => setItems(its))
      .catch(() => setError('Failed to load sensors'))
  }
  useEffect(() => { loadItems() }, [hostId, showAll])

  // Open the deep-linked sensor's chart once its row is present (from an Overview sensor click).
  useEffect(() => {
    if (autoOpenItem && items && items.some((i) => i.id === autoOpenItem)) setOpenItem(autoOpenItem)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoOpenItem, items])

  // When focused on a single sensor, force its chart open and report its display name up so the
  // breadcrumb can label the crumb (needed after a reload, where only the item id survives the URL).
  useEffect(() => {
    if (!onlyItem || !items) return
    const it = items.find((i) => i.id === onlyItem)
    if (!it) return
    setOpenItem(onlyItem)
    onItemName?.(onlyItem, it.label || it.name)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [onlyItem, items])

  const [busyItem, setBusyItem] = useState<string | null>(null)
  async function setItemState(it: SensorItem, action: 'pause' | 'hide', seconds: number | null) {
    setBusyItem(it.id)
    await fetch(`/api/items/${it.id}/${action}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ duration_seconds: seconds ?? 0 }) }).catch(() => {})
    setBusyItem(null)
    loadItems(false); fireDataRefresh()
  }
  async function clearItemState(it: SensorItem, action: 'pause' | 'hide') {
    setBusyItem(it.id)
    await fetch(`/api/items/${it.id}/${action}`, { method: 'DELETE' }).catch(() => {})
    setBusyItem(null)
    loadItems(false); fireDataRefresh()
  }
  // Set a sensor's PRTG-style display priority (Argus-only, admin/helpdesk). Optimistic; reverts to
  // server truth on failure, and nudges the overview/status lists to re-sort on success.
  async function setItemPriority(it: SensorItem, priority: number) {
    if (priority === it.priority) return
    setItems((its) => (its ? its.map((x) => (x.id === it.id ? { ...x, priority } : x)) : its))
    const res = await fetch(`/api/items/${it.id}/priority`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ priority }) }).catch(() => null)
    if (!res || !res.ok) loadItems(false)
    else fireDataRefresh()
  }

  function loadProblems() {
    fetch(`/api/hosts/${hostId}/problems`).then((r) => (r.ok ? r.json() : [])).then((p) => setProblems(p || [])).catch(() => {})
  }
  useEffect(() => { loadProblems() }, [hostId])
  // Keep the expanded host's values, last-check times and problems fresh.
  useEffect(() => { const t = setInterval(() => { loadItems(false); loadProblems() }, 30000); return () => clearInterval(t) }, [hostId, showAll])

  async function ack(p: Problem, seconds: number | null) {
    await fetch(`/api/events/${p.event_id}/ack`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ duration_seconds: seconds ?? 0 }) }).catch(() => {})
    loadProblems(); loadItems(false); fireDataRefresh()
  }
  async function unack(p: Problem) {
    await fetch(`/api/events/${p.event_id}/ack`, { method: 'DELETE' }).catch(() => {})
    loadProblems(); loadItems(false); fireDataRefresh()
  }

  const sparks = useSparks((items || []).filter((i) => i.numeric && i.supported).map((i) => i.id))

  if (error) return <div style={{ color: 'var(--err)', padding: '0.4rem 0' }}>{error}</div>
  if (!items) return <Skeleton rows={3} cols={4} />

  // Map each problem-referenced item to its worst state (and whether every problem on it is
  // acknowledged, so the highlight fades).
  const itemState: Record<string, string> = {}
  const itemAcked: Record<string, boolean> = {}
  for (const p of problems) {
    for (const id of p.item_ids) {
      if (!itemState[id] || stateRank[p.state] > stateRank[itemState[id]]) itemState[id] = p.state
      if (itemAcked[id] === undefined) itemAcked[id] = true
      if (!p.acknowledged) itemAcked[id] = false
    }
  }

  return (
    <div>
      {problems.length > 0 && (
        <div style={{ border: '1px solid color-mix(in srgb, var(--err) 30%, var(--border))', background: 'color-mix(in srgb, var(--err) 7%, var(--panel))', borderRadius: 8, padding: '0.5rem 0.75rem', marginBottom: '0.5rem' }}>
          <div style={{ color: 'var(--err)', fontSize: 12, marginBottom: 4, fontWeight: 600 }}>Active problems</div>
          {problems.map((p, i) => (
            <div key={i} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.2rem 0' }}>
              <span style={{ width: 8, height: 8, borderRadius: '50%', flexShrink: 0, background: healthColor(p.state, p.acknowledged) }} />
              <span style={{ opacity: p.acknowledged ? 0.7 : 1 }}>{p.name}</span>
              <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                {p.acknowledged
                  ? <><span className="acktag">✓ acked · {untilLabel(p.ack_until)}</span><button className="btn ghost" onClick={() => unack(p)}>Unacknowledge</button></>
                  : <DurationButton label="Acknowledge" onPick={(s) => ack(p, s)} />}
              </span>
            </div>
          ))}
        </div>
      )}
      {items.length === 0
        ? <div style={{ color: 'var(--muted)', padding: '0.2rem 0 0.4rem' }}>{showAll ? 'No sensors.' : 'No recognized sensors - try “All sensors”.'}</div>
        : (
          <table className="sensors">
            <thead><tr><th>Sensor</th><th>Value</th><th>Trend</th><th>Priority</th><th style={{ textAlign: 'right' }}>Last check</th></tr></thead>
            <tbody>
              {(onlyItem ? items.filter((i) => i.id === onlyItem) : items).map((it, idx, shown) => {
                const st = itemState[it.id]
                const open = openItem === it.id
                const clickable = it.numeric && it.supported
                const label = it.label || it.name
                // A sensor inherits its host's paused/hidden state; its own toggle is locked while
                // the host controls it.
                const effPaused = it.paused || hostPaused
                const effHidden = it.hidden || hostHidden
                const newGroup = !onlyItem && !showAll && it.category && it.category !== shown[idx - 1]?.category
                const rowClass = effHidden ? 'hidden' : effPaused ? 'paused' : st ? (itemAcked[it.id] ? 'acked' : (st === 'error' ? 'err' : 'warn')) : ''
                const unacked = problems.filter((p) => p.item_ids.includes(it.id) && !p.acknowledged)
                // Pause/Hide are offered only when the host isn't already controlling that state
                // (an inherited "· host" state is cleared at the host, not per-sensor).
                const acts: KAction[] = []
                if (!hostPaused) acts.push(it.paused
                  ? { label: 'Resume', icon: kbIcon.resume, onClick: () => clearItemState(it, 'pause') }
                  : { label: 'Pause', icon: kbIcon.pause, onPick: (s) => setItemState(it, 'pause', s) })
                if (!hostHidden) acts.push(it.hidden
                  ? { label: 'Show', icon: kbIcon.show, onClick: () => clearItemState(it, 'hide') }
                  : { label: 'Hide', icon: kbIcon.hide, onPick: (s) => setItemState(it, 'hide', s) })
                const actions: KAction[] = []
                if (unacked.length) { actions.push({ label: 'Acknowledge', icon: kbIcon.ack, onPick: (s) => unacked.forEach((p) => ack(p, s)) }); if (acts.length) actions.push({ sep: true, label: '' }) }
                actions.push(...acts)
                const trendColor = st ? healthColor(st, itemAcked[it.id]) : 'var(--accent)'
                return (
                  <Fragment key={it.id}>
                    {newGroup && <tr className="cat"><td colSpan={5}>{it.category}</td></tr>}
                    <tr className={rowClass} onClick={clickable ? () => { const next = open ? null : it.id; setOpenItem(next); onNavigate(hostId, next) } : undefined} style={{ opacity: it.supported ? 1 : 0.55, cursor: clickable ? 'pointer' : 'default' }}>
                      <td className="namecell">
                        <span className={'sname' + (clickable ? ' sclick' : '')} style={{ display: 'flex', alignItems: 'center', gap: 6, opacity: effPaused || effHidden ? 0.6 : 1 }}>
                          {clickable && <span className="scaret" style={{ color: 'var(--accent)', display: 'inline-block', transition: 'transform 0.15s', transform: open ? 'rotate(90deg)' : 'none' }}>›</span>}
                          {onDrillSensor ? <span className="lnk-sensor" onClick={(e) => { e.stopPropagation(); onDrillSensor(it.id, label) }}>{label}</span> : label}
                          {effPaused && <span style={{ color: PAUSED_BLUE, fontSize: 11 }}> (paused · {hostPaused && !it.paused ? 'host' : untilLabel(it.paused_until)})</span>}
                          {effHidden && <span style={{ color: HIDDEN_GREY, fontSize: 11 }}> (hidden · {hostHidden && !it.hidden ? 'host' : untilLabel(it.hidden_until)})</span>}
                        </span>
                      </td>
                      <td className="mono val">
                        {it.supported
                          ? (() => { const [dv, du] = readingParts(it.last_value, it.units); return <span>{dv}{du ? <span className="unit"> {du}</span> : null}</span> })()
                          : <span style={{ color: 'var(--err)' }}>not supported</span>}
                      </td>
                      <td className="strend">{it.numeric && it.supported ? <Spark values={sparks[it.id]} color={trendColor} /> : null}</td>
                      <td className="prio-cell" data-label="Priority"><PriorityStars value={it.priority} canEdit={canPause} onSet={(p) => setItemPriority(it, p)} /></td>
                      <td>
                        <div className="lccell">
                          <span className="when">{relTime(it.last_clock)}</span>
                          {canPause && actions.length > 0 && <Kebab disabled={busyItem === it.id} actions={actions} />}
                        </div>
                      </td>
                    </tr>
                    {open && clickable && (
                      <tr className="chartrow"><td colSpan={5}><div className="chart-reveal"><SensorChart itemId={it.id} units={it.units} color={trendColor} /></div></td></tr>
                    )}
                  </Fragment>
                )
              })}
            </tbody>
          </table>
        )}
    </div>
  )
}

// StatusListView is the flat cross-site list opened from a top-bar status chip: every sensor in
// the chosen state, with deep-links to its host/chart and a per-row kebab.
function StatusListView({ filter, sensors, loading, canPause, goHost, goSensor, onBack }: { filter: string; sensors: SensorRow[]; loading?: boolean; canPause: boolean; goHost: (h: string) => void; goSensor: (h: string, i: string, name?: string) => void; onBack: () => void }) {
  const [busy, setBusy] = useState<string | null>(null)
  // The "attention" filter is the home Overview: every sensor that isn't OK (a PRTG-style unified list),
  // with a mode toggle. A concrete state (error/warning/…) is a top-bar status-chip drill-down.
  const attention = filter === 'attention'
  const [attMode, setAttMode] = useState<'errors' | 'both'>('errors')
  const rows = sensors.filter((s) => attention
    ? (attMode === 'errors' ? s.state === 'error' : (s.state === 'error' || s.state === 'warning' || s.state === 'acked'))
    : s.state === filter)
  // Priority leads the ordering (except the OK list, where it'd just shuffle healthy sensors); severity
  // and host/name break ties. The backend already returns them host/name-sorted as a final fallback.
  if (attention || filter !== 'ok') rows.sort((a, b) => (b.priority - a.priority) || (b.severity - a.severity) || a.host_name.localeCompare(b.host_name) || a.name.localeCompare(b.name))
  const sparks = useSparks(rows.filter((s) => s.numeric && s.supported).map((s) => s.item_id))

  async function itemAction(s: SensorRow, action: 'pause' | 'hide', seconds: number | null) {
    setBusy(s.item_id)
    await fetch(`/api/items/${s.item_id}/${action}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ duration_seconds: seconds ?? 0 }) }).catch(() => {})
    setBusy(null); fireDataRefresh()
  }
  async function clearItem(s: SensorRow, action: 'pause' | 'hide') {
    setBusy(s.item_id)
    await fetch(`/api/items/${s.item_id}/${action}`, { method: 'DELETE' }).catch(() => {})
    setBusy(null); fireDataRefresh()
  }
  async function ackEvents(s: SensorRow, seconds: number | null) {
    setBusy(s.item_id)
    for (const ev of s.event_ids) await fetch(`/api/events/${ev}/ack`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ duration_seconds: seconds ?? 0 }) }).catch(() => {})
    setBusy(null); fireDataRefresh()
  }
  async function unackEvents(s: SensorRow) {
    setBusy(s.item_id)
    for (const ev of s.event_ids) await fetch(`/api/events/${ev}/ack`, { method: 'DELETE' }).catch(() => {})
    setBusy(null); fireDataRefresh()
  }
  function actionsFor(s: SensorRow): KAction[] {
    if (s.state === 'paused') return [{ label: 'Resume', icon: kbIcon.resume, onClick: () => clearItem(s, 'pause') }, { label: 'Hide', icon: kbIcon.hide, onPick: (sec) => itemAction(s, 'hide', sec) }]
    if (s.state === 'hidden') return [{ label: 'Show', icon: kbIcon.show, onClick: () => clearItem(s, 'hide') }, { label: 'Pause', icon: kbIcon.pause, onPick: (sec) => itemAction(s, 'pause', sec) }]
    const acts: KAction[] = []
    if (s.state === 'acked' && s.event_ids.length) acts.push({ label: 'Unacknowledge', icon: kbIcon.ack, onClick: () => unackEvents(s) }, { sep: true, label: '' })
    else if ((s.state === 'error' || s.state === 'warning') && s.event_ids.length) acts.push({ label: 'Acknowledge', icon: kbIcon.ack, onPick: (sec) => ackEvents(s, sec) }, { sep: true, label: '' })
    acts.push({ label: 'Pause', icon: kbIcon.pause, onPick: (sec) => itemAction(s, 'pause', sec) }, { label: 'Hide', icon: kbIcon.hide, onPick: (sec) => itemAction(s, 'hide', sec) })
    return acts
  }
  const durCol = filter === 'paused' ? 'Paused' : filter === 'hidden' ? 'Hidden' : 'Last check'
  return (
    <div className="panel">
      <div className="phead">
        <h2>{attention ? 'Active problems' : `${STATE_LABEL[filter]} sensors`}</h2>
        <span className="hint">{rows.length} sensor{rows.length === 1 ? '' : 's'} · across all sites</span>
        <div className="tools">
          {attention
            ? <div className="seg">
                <button className={attMode === 'errors' ? 'on' : ''} onClick={() => setAttMode('errors')}>Errors</button>
                <button className={attMode === 'both' ? 'on' : ''} onClick={() => setAttMode('both')}>Errors + Warnings</button>
              </div>
            : <button className="btn ghost" onClick={onBack}>← Back to overview</button>}
        </div>
      </div>
      {loading
        ? <Skeleton rows={4} cols={5} />
        : rows.length === 0
        ? (attention
          ? <EmptyState tone="ok" icon={ic.ok} title="All clear" text={attMode === 'errors' ? 'No sensor is in error right now.' : 'No errors or warnings right now.'} />
          : <EmptyState icon={STATE_ICON[filter]} title={`No ${STATE_LABEL[filter].toLowerCase()} sensors`} text="Nothing on any site is in this state at the moment." />)
        : (
          <table className="slist slist-sensors">
            <thead><tr><th>Host</th><th className="slgrow">Sensor</th><th>Value</th><th>Trend</th><th>{durCol}</th><th className="slprio">Priority</th><th /></tr></thead>
            <tbody>
              {rows.map((s) => {
                const clickable = s.numeric && s.supported
                return (
                  <tr key={s.item_id} style={{ opacity: s.state === 'acked' ? 0.72 : 1 }}>
                    <td className="slhost" style={{ borderLeftColor: STATE_VAR[s.state] || 'var(--border)' }}><span className="lnk-host" onClick={() => goHost(s.host_id)}>{s.host_name}</span></td>
                    <td className="slgrow">
                      {clickable ? <span className="lnk-sensor" onClick={() => goSensor(s.host_id, s.item_id, s.label || s.name)}>{s.label || s.name}</span> : (s.label || s.name)}
                      {s.reason && <div className="sreason"><span style={{ color: sevInfo(s.severity).color, fontWeight: 600 }}>{sevInfo(s.severity).label}</span> · {s.reason}{s.since ? <span title={`Firing since ${new Date(s.since * 1000).toLocaleString()}`}> · {relTime(s.since)}</span> : null}</div>}
                    </td>
                    <td className="mono val" data-label="Value">{s.supported ? (() => { const [dv, du] = readingParts(s.value, s.units); return <span>{dv}{du ? <span className="unit"> {du}</span> : null}</span> })() : <span style={{ color: 'var(--err)' }}>not supported</span>}</td>
                    <td className="trend">{clickable ? <Spark values={sparks[s.item_id]} color={s.state === 'ok' ? 'var(--accent)' : (STATE_VAR[s.state] || 'var(--accent)')} width={168} fill /> : null}</td>
                    <td className="mono dur" data-label={durCol}>{relTime(s.last_clock)}</td>
                    <td className="slprio" data-label="Priority"><PriorityStars value={s.priority} canEdit={false} /></td>
                    <td className="act">{canPause && <Kebab disabled={busy === s.item_id} actions={actionsFor(s)} />}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
    </div>
  )
}

// useTriggers loads the monitored triggers (alert rules) and keeps them fresh. Shared by both
// trigger tabs; the firing tab filters problem=true, the all tab groups by host.
function useTriggers(): [Trigger[] | null, string | null] {
  const [rows, setRows] = useState<Trigger[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    function load() {
      fetch('/api/triggers')
        .then(async (r) => { if (!r.ok) { setError(await errText(r, 'Failed to load triggers')); return } setRows(await r.json()); setError(null) })
        .catch(() => setError('Failed to load triggers'))
    }
    load(); const t = setInterval(load, 30000); const off = onDataRefresh(load)
    return () => { clearInterval(t); off() }
  }, [])
  return [rows, error]
}

// SevText renders a severity as a small coloured label (the trigger tabs' lightweight severity mark).
function SevText({ sev }: { sev: number }) {
  const s = sevInfo(sev)
  return <span style={{ color: s.color, fontWeight: 600 }}>{s.label}</span>
}

// TriggersView is the alert-rules tab, with a Firing / All toggle (like the Overview's filter). Firing
// is a flat cross-host table of triggers currently in problem; All groups every monitored trigger by
// host. Both surface which sensor(s) each trigger watches, so multi-sensor triggers are visible.
function TriggersView({ goHost }: { goHost: (h: string) => void }) {
  const [rows, error] = useTriggers()
  const [mode, setMode] = useState<'firing' | 'all'>('firing')
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set())
  function toggle(id: string) { setCollapsed((c) => { const n = new Set(c); n.has(id) ? n.delete(id) : n.add(id); return n }) }

  const firing = (rows || []).filter((t) => t.problem).sort((a, b) => (b.severity - a.severity) || (a.since - b.since))
  const byHost: Record<string, { name: string; trigs: Trigger[] }> = {}
  for (const t of rows || []) for (const h of t.hosts) { (byHost[h.id] = byHost[h.id] || { name: h.name, trigs: [] }).trigs.push(t) }
  const hostIds = Object.keys(byHost).sort((a, b) => byHost[a].name.localeCompare(byHost[b].name))

  return (
    <div className="panel">
      <div className="phead">
        <h2>Triggers</h2>
        <span className="hint">{mode === 'firing' ? `${firing.length} firing` : `${(rows || []).length} trigger${(rows || []).length === 1 ? '' : 's'} · ${hostIds.length} host${hostIds.length === 1 ? '' : 's'}`} · across all sites</span>
        <div className="tools"><div className="seg">
          <button className={mode === 'firing' ? 'on' : ''} onClick={() => setMode('firing')}>Firing</button>
          <button className={mode === 'all' ? 'on' : ''} onClick={() => setMode('all')}>All</button>
        </div></div>
      </div>
      {error && <div style={{ padding: '0.9rem 16px', color: 'var(--err)' }}>{error}</div>}
      {rows === null && !error && <Skeleton rows={4} cols={5} />}

      {mode === 'firing'
        ? (rows !== null && !error && (firing.length === 0
          ? <EmptyState tone="ok" icon={ic.ok} title="No triggers firing" text="Every alert rule is quiet right now." />
          : (
            <div className="enroll-scroll">
            <table className="slist slist-trig">
              <thead><tr><th className="slgrow">Trigger</th><th>Severity</th><th>Host</th><th>Sensors</th><th>Firing</th></tr></thead>
              <tbody>
                {firing.map((t) => (
                  <tr key={t.id}>
                    <td className="slgrow" style={{ borderLeft: `3px solid ${sevInfo(t.severity).color}`, paddingLeft: 13 }}>{t.description}</td>
                    <td data-label="Severity"><SevText sev={t.severity} /></td>
                    <td data-label="Host">{t.hosts.map((h, i) => <span key={h.id}>{i ? ', ' : ''}<span className="lnk-host" onClick={() => goHost(h.id)}>{h.name}</span></span>)}</td>
                    <td className="tsensors" data-label="Sensors">{t.sensors.join(', ') || '-'}</td>
                    <td className="mono dur" data-label="Firing" title={`Firing since ${new Date(t.since * 1000).toLocaleString()}`}>{relTime(t.since)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            </div>
          )))
        : (rows !== null && !error && (hostIds.length === 0
          ? <EmptyState icon={ic.triggers} title="No triggers" text="Argus lists the alert rules (triggers) defined on your Zabbix hosts. None are visible yet." />
          : hostIds.map((hid) => {
            const h = byHost[hid]
            const open = !collapsed.has(hid)
            const nf = h.trigs.filter((t) => t.problem).length
            const trigs = [...h.trigs].sort((a, b) => (Number(b.problem) - Number(a.problem)) || (b.severity - a.severity) || a.description.localeCompare(b.description))
            return (
              <div className="site" key={hid}>
                <div className="host-head" onClick={() => toggle(hid)}>
                  <svg className={'chev' + (open ? ' open' : '')} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M9 6l6 6-6 6" /></svg>
                  <span className="hn lnk-host" onClick={(e) => { e.stopPropagation(); goHost(hid) }}>{h.name}</span>
                  <div className="right">
                    {nf > 0 && <span style={{ color: 'var(--err)', fontSize: 12 }}>{nf} firing</span>}
                    <span className="loc">{h.trigs.length} trigger{h.trigs.length === 1 ? '' : 's'}</span>
                  </div>
                </div>
                {open && (
                  <div className="host-body">
                    <table className="sensors trig-list">
                      <tbody>
                        {trigs.map((t) => (
                          <tr key={t.id}>
                            <td className="namecell"><span style={{ width: 8, height: 8, borderRadius: '50%', flexShrink: 0, display: 'inline-block', marginRight: 8, background: t.problem ? sevInfo(t.severity).color : 'var(--ok)' }} />{t.description}</td>
                            <td className="tsensors mono">{t.sensors.join(', ')}</td>
                            <td style={{ textAlign: 'right', whiteSpace: 'nowrap', color: t.problem ? sevInfo(t.severity).color : 'var(--muted)' }} title={t.problem ? `Firing since ${new Date(t.since * 1000).toLocaleString()}` : ''}>{t.problem ? <>{sevInfo(t.severity).label} · {relTime(t.since)}</> : 'OK'}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                )}
              </div>
            )
          })))}
    </div>
  )
}

type ChartColors = { line: string; fill: string; soft: string; axis: string; grid: string }

// withAlpha turns a #rrggbb token value into an rgba() with the given opacity (other formats pass through).
function withAlpha(color: string, a: number): string {
  const m = /^#([0-9a-f]{6})$/i.exec(color)
  if (!m) return color
  const n = parseInt(m[1], 16)
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`
}

// chartColors resolves the chart palette from the live theme tokens: axes and grid from the text/border
// tokens (visible on light AND dark - the old hard-coded white-alpha grid vanished on light), and the
// series in the sensor's state colour, so the big chart agrees with the row's sparkline next to it.
function chartColors(color: string): ChartColors {
  const css = getComputedStyle(document.documentElement)
  const tok = (v: string) => { const m = /^var\((--[\w-]+)\)$/.exec(v.trim()); return (m ? css.getPropertyValue(m[1]) : v).trim() }
  const line = tok(color) || '#2ea8c9'
  return { line, fill: withAlpha(line, 0.12), soft: withAlpha(line, 0.4), axis: tok('var(--faint)') || '#8a8a8a', grid: tok('var(--border)') || 'rgba(128,128,128,0.25)' }
}

// insertGaps breaks the line where sampling stopped (e.g. a paused sensor): where the time
// between two consecutive points exceeds ~1.75x the typical interval, it inserts a null so
// uPlot draws a gap instead of a straight line across the missing period.
function insertGaps(xs: number[], series: (number | null)[][]): [number[], (number | null)[][]] {
  if (xs.length < 3) return [xs, series]
  const deltas: number[] = []
  for (let i = 1; i < xs.length; i++) deltas.push(xs[i] - xs[i - 1])
  const median = [...deltas].sort((a, b) => a - b)[Math.floor(deltas.length / 2)] || 0
  if (median <= 0) return [xs, series]
  const threshold = median * 1.75
  const nx: number[] = []
  const ns: (number | null)[][] = series.map(() => [])
  for (let i = 0; i < xs.length; i++) {
    if (i > 0 && xs[i] - xs[i - 1] > threshold) {
      nx.push(xs[i - 1] + median)
      ns.forEach((s) => s.push(null))
    }
    nx.push(xs[i])
    series.forEach((s, si) => ns[si].push(s[i]))
  }
  return [nx, ns]
}

function buildPlot(data: Series, units: string, width: number, c: ChartColors): [uPlot.Options, uPlot.AlignedData] {
  const xs = data.points.map((p) => p.t)
  const grid = { stroke: c.grid, width: 1 }
  const ticks = { stroke: c.grid, width: 1 }
  const scaled = scaledUnit(units)
  // Scaled y-axis ticks (bytes/bits/uptime); otherwise default numeric.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const yValues = scaled ? ((_u: any, splits: number[]) => splits.map((v) => fmtNum(v, units))) : undefined
  const yAxis: uPlot.Axis = { stroke: c.axis, grid, ticks, size: 64, values: yValues as unknown as uPlot.Axis['values'] }
  const xAxis: uPlot.Axis = { stroke: c.axis, grid, ticks }
  // Legend cells: show the hovered point, or fall back to the latest value when idle (so the
  // legend is never blank). unitLabel is dropped for scaled units since the value carries it.
  const unitLabel = scaled ? '' : units ? ` (${units})` : ''
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const xVal = (u: any, v: number | null) => { const t = v ?? lastVal(u, 0); return t == null ? '--' : new Date(t * 1000).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const yVal = (sidx: number) => (u: any, v: number | null) => { const n = v ?? lastVal(u, sidx); return n == null ? '--' : fmtNum(n, units) }
  const base: Partial<uPlot.Options> = { width, height: 320, scales: { x: { time: true } }, axes: [xAxis, yAxis], legend: { show: true } }

  if (data.kind === 'trend') {
    const avg = data.points.map((p) => (p.avg ?? null))
    const min = data.points.map((p) => (p.min ?? null))
    const max = data.points.map((p) => (p.max ?? null))
    const opts: uPlot.Options = {
      ...base,
      series: [
        { value: xVal },
        { label: `avg${unitLabel}`, stroke: c.line, width: 1.5, value: yVal(1) },
        { label: 'min', stroke: c.soft, width: 1, value: yVal(2) },
        { label: 'max', stroke: c.soft, width: 1, value: yVal(3) },
      ],
      bands: [{ series: [3, 2], fill: c.fill }],
    } as uPlot.Options
    const [gx, [ga, gmin, gmax]] = insertGaps(xs, [avg, min, max])
    return [opts, [gx, ga, gmin, gmax] as uPlot.AlignedData]
  }

  const vs = data.points.map((p) => (p.v ?? null))
  const opts: uPlot.Options = {
    ...base,
    series: [{ value: xVal }, { label: `value${unitLabel}`, stroke: c.line, width: 1.5, fill: c.fill, value: yVal(1) }],
  } as uPlot.Options
  const [gx, [gv]] = insertGaps(xs, [vs])
  return [opts, [gx, gv] as uPlot.AlignedData]
}

function SensorChart({ itemId, units, color = 'var(--accent)' }: { itemId: string; units: string; color?: string }) {
  const [range, setRange] = useState('2h')
  const [data, setData] = useState<Series | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  // Reveal the "Loading…" text only if a fetch is genuinely slow, so a fast open doesn't flash it.
  const [showLoading, setShowLoading] = useState(false)
  const [tick, setTick] = useState(0)
  // Bumped when the theme flips (data-theme on <html>), so the chart repaints with the new tokens.
  const [themeTick, setThemeTick] = useState(0)
  const host = useRef<HTMLDivElement>(null)
  const plot = useRef<uPlot | null>(null)
  const lastKey = useRef('')

  // Refresh the open chart periodically so it stays live.
  useEffect(() => { const t = setInterval(() => setTick((x) => x + 1), 60000); return () => clearInterval(t) }, [])
  useEffect(() => {
    const mo = new MutationObserver(() => setThemeTick((x) => x + 1))
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => mo.disconnect()
  }, [])

  useEffect(() => {
    let cancelled = false
    // Show the loading state on an item/range change, but not on background refreshes.
    const key = `${itemId}|${range}`
    const fresh = lastKey.current !== key
    if (fresh) { setLoading(true); lastKey.current = key }
    // Defer the visible "Loading…" text: only surface it if the fetch outlasts the grace window,
    // so quick opens (the common case) never flash it.
    let slowTimer: ReturnType<typeof setTimeout> | undefined
    if (fresh) slowTimer = setTimeout(() => { if (!cancelled) setShowLoading(true) }, 300)
    setError(null)
    fetch(`/api/items/${itemId}/history?range=${range}`)
      .then(async (r) => { if (!r.ok) throw new Error(await errText(r, 'Failed to load history')); return r.json() })
      .then((d: Series) => { if (!cancelled) setData(d) })
      .catch((e) => { if (!cancelled) { setError(e.message || 'Failed to load history'); setData(null) } })
      .finally(() => { if (slowTimer) clearTimeout(slowTimer); if (!cancelled) { setLoading(false); setShowLoading(false) } })
    return () => { cancelled = true; if (slowTimer) clearTimeout(slowTimer) }
  }, [itemId, range, tick])

  useEffect(() => {
    if (plot.current) { plot.current.destroy(); plot.current = null }
    if (!host.current || !data || data.points.length === 0) return
    const width = host.current.clientWidth || 600
    const [opts, aligned] = buildPlot(data, units, width, chartColors(color))
    plot.current = new uPlot(opts, aligned, host.current)
    return () => { if (plot.current) { plot.current.destroy(); plot.current = null } }
  }, [data, units, color, themeTick])

  useEffect(() => {
    function onResize() { if (plot.current && host.current) plot.current.setSize({ width: host.current.clientWidth, height: 320 }) }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  return (
    <div>
      <div className="rtabs">
        {RANGES.map((rk) => (
          <button key={rk} className={'rtab' + (range === rk ? ' on' : '')} onClick={() => setRange(rk)}>{rk}</button>
        ))}
      </div>
      {showLoading && <p style={{ color: 'var(--muted)', margin: '0.3rem 0' }}>Loading…</p>}
      {error && <p style={{ color: 'var(--err)', margin: '0.3rem 0' }}>{error}</p>}
      {!loading && !error && data && data.points.length === 0 && <p style={{ color: 'var(--muted)', margin: '0.3rem 0' }}>No data in this range.</p>}
      <div ref={host} style={{ width: '100%' }} />
    </div>
  )
}

function UsersView() {
  const confirm = useConfirm()
  const prompt = usePrompt()
  const toast = useToast()
  const [users, setUsers] = useState<User[]>([])
  const [loaded, setLoaded] = useState(false)
  const [adding, setAdding] = useState(false)
  const [nu, setNu] = useState({ email: '', name: '', surname: '', role: 'viewer', password: '' })
  const usersRef = useRef<User[]>([])
  usersRef.current = users

  function load() { fetch('/api/users').then((r) => r.json()).then((u) => { setUsers(u || []); setLoaded(true) }).catch(() => toast.error('Failed to load users')) }
  useEffect(() => { load() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  async function fail(res: Response) { toast.error(await errText(res, 'Request failed')); load() }
  function edit(id: number, patch: Partial<User>) { setUsers((us) => us.map((x) => (x.id === id ? { ...x, ...patch } : x))) }

  // Persist the row's email/name/surname/role (called on blur of a field or role change).
  async function saveUser(id: number) {
    const u = usersRef.current.find((x) => x.id === id); if (!u) return
    const res = await fetch(`/api/users/${id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email: u.email, name: u.name, surname: u.surname, role: u.role }) })
    if (!res.ok) return fail(res)
    toast.success('Saved')
  }
  async function create(e: FormEvent) {
    e.preventDefault()
    const res = await fetch('/api/users', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(nu) })
    if (!res.ok) return toast.error(await errText(res, 'Request failed'))
    setNu({ email: '', name: '', surname: '', role: 'viewer', password: '' }); setAdding(false); toast.success('User created'); load()
  }
  async function resetPw(u: User) {
    const pw = await prompt({ title: 'Reset password', label: `New password for ${u.email} (min 8 characters)`, type: 'password', confirmLabel: 'Set password', required: true })
    if (!pw) return
    const res = await fetch(`/api/users/${u.id}/password`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: pw }) })
    if (!res.ok) return fail(res)
    toast.success(`Password reset for ${u.email}`)
  }
  async function resetMfa(u: User) {
    if (!(await confirm({ title: 'Remove two-factor', message: `Remove two-factor for ${u.email}? They'll sign in with just their password until they set it up again.`, confirmLabel: 'Remove', danger: true }))) return
    const res = await fetch(`/api/users/${u.id}/mfa/reset`, { method: 'POST' })
    if (!res.ok) return fail(res)
    toast.success(`Two-factor removed for ${u.email}`); load()
  }
  async function resetPasskeys(u: User) {
    if (!(await confirm({ title: 'Remove passkeys', message: `Remove all passkeys for ${u.email}?`, confirmLabel: 'Remove', danger: true }))) return
    const res = await fetch(`/api/users/${u.id}/passkeys/reset`, { method: 'POST' })
    if (!res.ok) return fail(res)
    toast.success(`Passkeys removed for ${u.email}`); load()
  }
  async function setDisabled(u: User, disabled: boolean) {
    if (disabled && !(await confirm({ title: 'Disable user', message: `Disable ${u.email}? They won't be able to sign in until re-enabled.`, confirmLabel: 'Disable', danger: true }))) return
    const res = await fetch(`/api/users/${u.id}/disabled`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ disabled }) })
    if (!res.ok) return fail(res)
    toast.success(`${u.email} ${disabled ? 'disabled' : 'enabled'}`); load()
  }
  async function del(u: User) {
    if (!(await confirm({ title: 'Remove user', message: `Remove ${u.email}? This permanently deletes the account.`, confirmLabel: 'Remove', danger: true }))) return
    const res = await fetch(`/api/users/${u.id}`, { method: 'DELETE' })
    if (!res.ok) return fail(res)
    toast.success(`${u.email} removed`); load()
  }

  function userActions(u: User): KAction[] {
    const a: KAction[] = [{ label: 'Reset password', icon: uIcon.key, onClick: () => resetPw(u) }]
    if (u.mfa_enabled) a.push({ label: 'Remove 2FA', icon: uIcon.shield, onClick: () => resetMfa(u) })
    if (u.passkeys) a.push({ label: 'Remove passkeys', icon: uIcon.fp, onClick: () => resetPasskeys(u) })
    a.push({ sep: true, label: '' })
    a.push(u.disabled
      ? { label: 'Enable user', icon: uIcon.enable, onClick: () => setDisabled(u, false) }
      : { label: 'Disable user', icon: uIcon.ban, danger: true, onClick: () => setDisabled(u, true) })
    a.push({ label: 'Remove user', icon: uIcon.trash, danger: true, onClick: () => del(u) })
    return a
  }

  return (
    <div className="panel">
      <div className="phead">
        <h2>Users</h2><span className="hint">{users.length} account{users.length === 1 ? '' : 's'}</span>
        <div className="tools"><button className="btn primary" onClick={() => setAdding((v) => !v)}>{adding ? 'Cancel' : '+ Add user'}</button></div>
      </div>

      {adding && (
        <form onSubmit={create} style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5rem', alignItems: 'center', padding: '10px 16px', borderBottom: '1px solid var(--border)', background: 'var(--elevated)' }}>
          <input className="input" type="email" placeholder="email" value={nu.email} onChange={(e) => setNu({ ...nu, email: e.target.value })} required />
          <input className="input" placeholder="name" value={nu.name} onChange={(e) => setNu({ ...nu, name: e.target.value })} />
          <input className="input" placeholder="surname" value={nu.surname} onChange={(e) => setNu({ ...nu, surname: e.target.value })} />
          <Select value={nu.role} onChange={(e) => setNu({ ...nu, role: e.target.value })}>{ROLES.map((r) => <option key={r} value={r}>{r}</option>)}</Select>
          <input className="input" type="password" placeholder="password (min 8)" value={nu.password} onChange={(e) => setNu({ ...nu, password: e.target.value })} required />
          <button type="submit" className="btn primary">Add</button>
        </form>
      )}

      {!loaded && <Skeleton rows={3} cols={6} />}
      {loaded && <table className="utable">
        <thead><tr><th style={{ width: '28%' }}>Email</th><th style={{ width: '18%' }}>Name</th><th style={{ width: '18%' }}>Surname</th><th>Role</th><th>2FA</th><th>Passkeys</th><th style={{ textAlign: 'right' }}>Manage</th></tr></thead>
        <tbody>
          {users.map((u) => (
            <tr key={u.id} style={{ opacity: u.disabled ? 0.5 : 1 }}>
              <td data-label="Email"><input className="cellinput mono" value={u.email} onChange={(e) => edit(u.id, { email: e.target.value })} onBlur={() => saveUser(u.id)} /></td>
              <td data-label="Name"><input className="cellinput" value={u.name} placeholder="Name" onChange={(e) => edit(u.id, { name: e.target.value })} onBlur={() => saveUser(u.id)} /></td>
              <td data-label="Surname"><input className="cellinput" value={u.surname} placeholder="Surname" onChange={(e) => edit(u.id, { surname: e.target.value })} onBlur={() => saveUser(u.id)} /></td>
              <td data-label="Role"><Select className="roleselect" value={u.role} onChange={(e) => { edit(u.id, { role: e.target.value }); setTimeout(() => saveUser(u.id), 0) }}>{ROLES.map((r) => <option key={r} value={r}>{r}</option>)}</Select></td>
              <td data-label="2FA">{u.mfa_enabled ? <span className="badge on">on</span> : <span className="badge off">off</span>}</td>
              <td data-label="Passkeys" className="mono">{u.passkeys || 0}</td>
              <td data-label="Manage" style={{ textAlign: 'right' }}>
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, justifyContent: 'flex-end' }}>
                  {u.disabled && <Badge tone="err">disabled</Badge>}
                  <Kebab actions={userActions(u)} />
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>}
    </div>
  )
}

function AccountView({ me, onMe, passkeysAvailable, theme, toggleTheme }: { me: Me; onMe: (m: Me) => void; passkeysAvailable: boolean; theme: 'dark' | 'light'; toggleTheme: () => void }) {
  return (
    <div style={{ display: 'grid', gap: '1rem', maxWidth: 560 }}>
      <Card title="Appearance" note={`Theme is remembered on this device. Currently ${theme}.`}>
        <Button variant="primary" onClick={toggleTheme}>Switch to {theme === 'dark' ? 'light' : 'dark'} mode</Button>
      </Card>
      <LandingCard me={me} onMe={onMe} />
      <PersonalNotifyCard />
      <PasswordCard />
      <MfaCard />
      {passkeysAvailable && <PasskeyCard />}
    </div>
  )
}

function LandingCard({ me, onMe }: { me: Me; onMe: (m: Me) => void }) {
  const toast = useToast()
  const [landing, setLanding] = useState<'overview' | 'errors'>(me.landing === 'errors' ? 'errors' : 'overview')
  const [busy, setBusy] = useState(false)

  async function choose(v: 'overview' | 'errors') {
    if (v === landing) return
    const prev = landing
    setLanding(v); setBusy(true)
    try {
      const res = await fetch('/api/me/preferences', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ landing: v }) })
      if (!res.ok) { setLanding(prev); toast.error(await errText(res, 'Could not save preference')); return }
      onMe(await res.json()); toast.success('Landing page updated.')
    } catch { setLanding(prev); toast.error('Could not save preference') }
    finally { setBusy(false) }
  }

  return (
    <Card title="Landing page" note="Which screen Argus opens on when you sign in or visit the app.">
      <Field label="Open on">
        <select value={landing} disabled={busy} onChange={(e) => choose(e.target.value as 'overview' | 'errors')}>
          <option value="overview">Overview - what needs attention right now</option>
          <option value="errors">Errors - the list of erroring sensors</option>
        </select>
      </Field>
    </Card>
  )
}

function PasswordCard() {
  const toast = useToast()
  const [cur, setCur] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState<string | null>(null)

  async function submit(e: FormEvent) {
    e.preventDefault(); setError(null)
    if (next !== confirm) { setError('The new passwords do not match.'); return }
    const res = await fetch('/api/me/password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ current_password: cur, new_password: next }) })
    if (!res.ok) { setError(await errText(res, 'Request failed')); return }
    setCur(''); setNext(''); setConfirm(''); toast.success('Password changed.')
  }

  return (
    <Card title="Change my password">
      <Banner variant="error">{error}</Banner>
      <form onSubmit={submit}>
        <Field label="Current password" type="password" value={cur} autoComplete="current-password" onChange={(e) => setCur(e.target.value)} required />
        <Field label="New password (min 8)" type="password" value={next} autoComplete="new-password" onChange={(e) => setNext(e.target.value)} required minLength={8} />
        <Field label="Confirm new password" type="password" value={confirm} autoComplete="new-password" onChange={(e) => setConfirm(e.target.value)} required />
        <Button type="submit" variant="primary">Update password</Button>
      </form>
    </Card>
  )
}

type Enrollment = { secret: string; otpauth_url: string; qr_data_uri: string }

function MfaCard() {
  const prompt = usePrompt()
  const toast = useToast()
  const [enabled, setEnabled] = useState<boolean | null>(null)
  const [remaining, setRemaining] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null)
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[] | null>(null)

  function loadStatus() {
    fetch('/api/me/mfa').then((r) => r.json()).then((d) => { setEnabled(d.enabled); setRemaining(d.recovery_codes_remaining || 0) }).catch(() => setError('Failed to load 2FA status'))
  }
  useEffect(() => { loadStatus() }, [])

  async function startSetup() {
    setError(null); setCodes(null)
    const res = await fetch('/api/me/mfa/setup', { method: 'POST' })
    if (!res.ok) { setError(await errText(res, 'Could not start setup')); return }
    setEnrollment(await res.json())
  }
  async function confirmEnable(e: FormEvent) {
    e.preventDefault(); setError(null)
    const res = await fetch('/api/me/mfa/enable', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code }) })
    if (!res.ok) { setError(await errText(res, 'Could not enable 2FA')); return }
    const d = await res.json()
    setEnrollment(null); setCode(''); setCodes(d.recovery_codes); toast.success('Two-factor is now on. Save your recovery codes.'); loadStatus()
  }
  async function disable() {
    setError(null); setCodes(null)
    const pw = await prompt({ title: 'Turn off two-factor', label: 'Confirm your password', type: 'password', confirmLabel: 'Turn off', required: true })
    if (!pw) return
    const res = await fetch('/api/me/mfa/disable', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: pw }) })
    if (!res.ok) { setError(await errText(res, 'Could not disable 2FA')); return }
    toast.success('Two-factor has been turned off.'); loadStatus()
  }
  async function regen() {
    setError(null); setCodes(null)
    const pw = await prompt({ title: 'New recovery codes', label: 'Confirm your password', type: 'password', confirmLabel: 'Generate', required: true })
    if (!pw) return
    const res = await fetch('/api/me/mfa/recovery-codes', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: pw }) })
    if (!res.ok) { setError(await errText(res, 'Could not regenerate codes')); return }
    const d = await res.json()
    setCodes(d.recovery_codes); toast.success('New recovery codes generated. The old ones no longer work.'); loadStatus()
  }

  return (
    <Card title="Two-factor authentication" note="Use an authenticator app or a password manager such as Bitwarden. Argus uses standard TOTP, so both scanning the QR and pasting the setup key work.">
      <Banner variant="error">{error}</Banner>

      {enabled === null && <p>Checking…</p>}

      {codes && <RecoveryCodes codes={codes} />}

      {enabled === false && !enrollment && !codes && (
        <Button variant="primary" onClick={startSetup}>Enable two-factor</Button>
      )}

      {enabled === false && enrollment && (
        <div>
          <p style={{ marginBottom: '0.5rem' }}>1. Scan this QR, or paste the setup key into Bitwarden:</p>
          <img src={enrollment.qr_data_uri} alt="TOTP QR code" style={{ borderRadius: 8, background: 'white', padding: 8 }} width={200} height={200} />
          <p style={{ margin: '0.75rem 0 0.25rem', color: 'var(--muted)' }}>Setup key</p>
          <code style={{ display: 'block', wordBreak: 'break-all', background: 'var(--elevated)', border: '1px solid var(--border)', borderRadius: 6, padding: '0.5rem', fontSize: '0.9rem' }}>{enrollment.secret}</code>
          <form onSubmit={confirmEnable} style={{ marginTop: '1rem' }}>
            <p style={{ marginBottom: '0.4rem' }}>2. Enter the current 6-digit code to confirm:</p>
            <input className="input" style={{ width: '100%', marginBottom: '0.75rem', letterSpacing: '0.15em' }} value={code} onChange={(e) => setCode(e.target.value)} autoComplete="one-time-code" inputMode="numeric" name="otp" placeholder="123456" required />
            <div style={{ display: 'flex', gap: '0.5rem' }}>
              <Button type="submit" variant="primary">Confirm & enable</Button>
              <Button type="button" variant="ghost" onClick={() => { setEnrollment(null); setCode(''); setError(null) }}>Cancel</Button>
            </div>
          </form>
        </div>
      )}

      {enabled === true && (
        <div>
          <p><strong className="txt-ok">On.</strong> <span style={{ color: 'var(--muted)' }}>{remaining} recovery code{remaining === 1 ? '' : 's'} remaining.</span></p>
          <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
            <Button variant="ghost" onClick={regen}>Regenerate recovery codes</Button>
            <Button variant="danger" onClick={disable}>Turn off</Button>
          </div>
        </div>
      )}
    </Card>
  )
}

function RecoveryCodes({ codes }: { codes: string[] }) {
  const text = codes.join('\n')
  function download() {
    const blob = new Blob([text + '\n'], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url; a.download = 'argus-recovery-codes.txt'; a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <div className="callout-warn">
      <p>Save these recovery codes now - each works once and they won't be shown again.</p>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: '0.25rem 1rem', fontFamily: 'monospace', fontSize: '0.95rem' }}>
        {codes.map((c) => <span key={c}>{c}</span>)}
      </div>
      <div style={{ display: 'flex', gap: '0.5rem', marginTop: '0.75rem' }}>
        <CopyButton text={text} />
        <Button variant="ghost" onClick={download}>Download</Button>
      </div>
    </div>
  )
}

function PasskeyCard() {
  const confirm = useConfirm()
  const prompt = usePrompt()
  const toast = useToast()
  const [keys, setKeys] = useState<Passkey[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  function load() { fetch('/api/me/passkeys').then((r) => r.json()).then(setKeys).catch(() => setError('Failed to load passkeys')) }
  useEffect(() => { load() }, [])

  async function add() {
    setError(null)
    const name = await prompt({ title: 'Add a passkey', label: 'Name this passkey (e.g. "Bitwarden", "Phone", "YubiKey")', initial: 'Bitwarden', confirmLabel: 'Continue' })
    if (name === null) return
    setBusy(true)
    try {
      await registerPasskey(name || 'Passkey')
      toast.success('Passkey added.'); load()
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : 'Could not add passkey')
    } finally { setBusy(false) }
  }
  async function remove(k: Passkey) {
    setError(null)
    if (!(await confirm({ title: 'Remove passkey', message: `Remove passkey "${k.name}"?`, confirmLabel: 'Remove', danger: true }))) return
    const res = await fetch(`/api/me/passkeys/${k.id}`, { method: 'DELETE' })
    if (!res.ok) { setError(await errText(res, 'Could not remove passkey')); return }
    toast.success('Passkey removed.'); load()
  }

  return (
    <Card title="Passkeys" note="Sign in without a password using a passkey stored in Bitwarden, your phone, or a security key. Passkeys work when you reach Argus through its HTTPS address.">
      <Banner variant="error">{error}</Banner>

      {keys.length === 0 && <p style={{ color: 'var(--muted)' }}>No passkeys registered yet.</p>}
      {keys.length > 0 && (
        <ul style={{ listStyle: 'none', padding: 0, margin: '0 0 1rem' }}>
          {keys.map((k) => (
            <li key={k.id} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', borderTop: '1px solid var(--border)', padding: '0.5rem 0' }}>
              <span>
                <strong>{k.name}</strong>
                <span style={{ color: 'var(--faint)', marginLeft: '0.5rem', fontSize: '0.85rem' }}>
                  added {new Date(k.created).toLocaleDateString()}
                  {k.last_used ? ` · last used ${new Date(k.last_used).toLocaleDateString()}` : ' · never used'}
                </span>
              </span>
              <Button variant="danger" onClick={() => remove(k)}>Remove</Button>
            </li>
          ))}
        </ul>
      )}
      <Button variant="primary" onClick={add} disabled={busy}>{busy ? 'Waiting for authenticator…' : 'Add a passkey'}</Button>
    </Card>
  )
}

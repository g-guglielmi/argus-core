import { useEffect, useRef, useState, Fragment, type CSSProperties, type FormEvent, type ReactNode } from 'react'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import { registerPasskey, loginWithPasskey } from './webauthn'

type Me = { email: string; name: string; surname: string; role: string; mfa_enabled?: boolean }
type User = { id: number; email: string; name: string; surname: string; role: string; mfa_enabled?: boolean; passkeys?: number }
type Health = { status: string; zabbix: { reachable: boolean; version?: string; error?: string } }
type Passkey = { id: string; name: string; created: string; last_used: string | null }
type Host = { id: string; name: string; problems: number; severity: number; state: string; paused: boolean; hidden: boolean; paused_until?: number; hidden_until?: number; groups: string[] }
type Proxy = { name: string; last_access: number; online: boolean; mode: string }
type SensorItem = { id: string; name: string; key: string; last_value: string; units: string; last_clock: number; supported: boolean; numeric: boolean; paused: boolean; hidden: boolean; paused_until?: number; hidden_until?: number; category?: string; label?: string }
type Problem = { event_id: string; name: string; severity: number; state: string; acknowledged: boolean; ack_until?: number; item_ids: string[] }
type ProblemRow = { event_id: string; name: string; host_id: string; host_name: string; severity: number; state: string; acknowledged: boolean; ack_until?: number; clock: number; item_ids: string[] }
type SensorRow = { host_id: string; host_name: string; item_id: string; name: string; label?: string; category?: string; value: string; units: string; last_clock: number; state: string; numeric: boolean; supported: boolean }
type SeriesPoint = { t: number; v?: number; min?: number; avg?: number; max?: number }
type Series = { name: string; units: string; kind: 'history' | 'trend'; points: SeriesPoint[] }

const RANGES = ['2h', '2d', '1M', '3M', '6M', '1Y']

const stateColor: Record<string, string> = { ok: 'seagreen', warning: '#d9a441', error: 'crimson' }
const stateRank: Record<string, number> = { ok: 0, warning: 1, error: 2 }
// Census/summary state → CSS colour var and label (six buckets, incl. paused/hidden/acked).
const STATE_VAR: Record<string, string> = { ok: 'var(--ok)', warning: 'var(--warn)', error: 'var(--err)', acked: 'var(--acked)', paused: 'var(--paused)', hidden: 'var(--hidden)' }
const STATE_LABEL: Record<string, string> = { ok: 'OK', warning: 'Warning', error: 'Error', acked: 'Acknowledged', paused: 'Paused', hidden: 'Hidden' }
const PAUSED_BLUE = '#4a86c5'
const HIDDEN_GREY = '#888'

// Faded health colours for acknowledged problems (PRTG-style: still visible, muted).
const stateColorFaded: Record<string, string> = { ok: '#4a6a4a', warning: '#8a7a45', error: '#8a5252' }
function healthColor(state: string, acked: boolean): string {
  return (acked ? stateColorFaded : stateColor)[state] || '#777'
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
      <button onClick={(e) => { e.stopPropagation(); setCustom(false); setOpen((o) => !o) }} disabled={disabled} style={{ ...ghost, padding: '0.1rem 0.45rem', fontSize: '0.75rem', borderColor: borderColor || 'var(--border)' }}>{label}</button>
      {open && (
        <>
          <div onClick={(e) => { e.stopPropagation(); close() }} style={{ position: 'fixed', inset: 0, zIndex: 20 }} />
          <div onClick={(e) => e.stopPropagation()} style={{ position: 'absolute', top: '100%', right: 0, marginTop: 4, zIndex: 21, background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, minWidth: custom ? 240 : 140, boxShadow: '0 8px 24px rgba(0,0,0,0.45)', overflow: 'hidden' }}>
            {!custom && DURATIONS.map((d) => (
              <div key={d.label} onClick={(e) => { e.stopPropagation(); pickPreset(d.seconds) }} style={{ padding: '0.4rem 0.7rem', cursor: 'pointer', fontSize: '0.8rem', whiteSpace: 'nowrap' }}>{d.label}</div>
            ))}
            {custom && (
              <div style={{ padding: '0.6rem' }}>
                <div style={{ fontSize: '0.78rem', color: '#aaa', marginBottom: '0.35rem' }}>Suppress until:</div>
                <input type="datetime-local" value={val} min={toLocalInput(Date.now())} onChange={(e) => setVal(e.target.value)} style={{ ...input, width: '100%', marginBottom: '0.5rem' }} />
                <div style={{ display: 'flex', gap: '0.4rem', justifyContent: 'flex-end' }}>
                  <button onClick={(e) => { e.stopPropagation(); setCustom(false) }} style={{ ...ghost, padding: '0.15rem 0.5rem', fontSize: '0.78rem' }}>Back</button>
                  <button onClick={(e) => { e.stopPropagation(); confirmCustom() }} style={{ ...btn, padding: '0.15rem 0.6rem', fontSize: '0.78rem' }}>Set</button>
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

function relTime(unix: number): string {
  if (!unix) return 'never'
  const s = Math.max(0, Math.floor(Date.now() / 1000) - unix)
  if (s < 60) return `${s}s ago`
  if (s < 3600) return `${Math.floor(s / 60)}m ago`
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`
  return `${Math.floor(s / 86400)}d ago`
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
  return units === 'B' || units === 'Bps' || units === 'bps' || units === 'uptime'
}

// fmtNumParts formats a numeric reading into [value, unit], scaling byte/bit units and
// rendering uptime as a duration.
function fmtNumParts(n: number, units: string): [string, string] {
  if (units === 'B') return scaleBy(n, 1024, BYTE_UNITS)
  if (units === 'Bps') { const [v, u] = scaleBy(n, 1024, BYTE_UNITS); return [v, u + 'ps'] }
  if (units === 'bps') return scaleBy(n, 1000, BIT_UNITS)
  if (units === 'uptime') return [fmtDuration(n), '']
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
  if (t === '') return ['—', '']
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

const card: CSSProperties = { border: '1px solid var(--border)', borderRadius: 12, padding: '1rem 1.25rem', background: 'var(--panel)', boxShadow: 'var(--shadow)' }
const input: CSSProperties = { padding: '0.5rem 0.6rem', borderRadius: 7, border: '1px solid var(--border-strong)', background: 'var(--elevated)', color: 'var(--text)', boxSizing: 'border-box' }
const btn: CSSProperties = { padding: '0.5rem 0.9rem', borderRadius: 7, border: '1px solid var(--accent)', background: 'var(--accent)', color: '#fff', cursor: 'pointer', fontWeight: 600 }
const ghost: CSSProperties = { padding: '0.4rem 0.7rem', borderRadius: 7, border: '1px solid var(--border-strong)', background: 'transparent', color: 'var(--text)', cursor: 'pointer' }

async function errText(res: Response, fallback: string) {
  const j = await res.json().catch(() => ({}))
  return (j && j.error) || fallback
}

// Cross-component refresh signal: a mutation (ack / pause / hide) fires this so the shell's
// status summary and any listening view reload immediately instead of waiting for the 30s poll.
const refreshBus = new Set<() => void>()
function onDataRefresh(fn: () => void): () => void { refreshBus.add(fn); return () => { refreshBus.delete(fn) } }
function fireDataRefresh(): void { refreshBus.forEach((f) => f()) }

export default function App() {
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)
  const [passkeysAvailable, setPasskeysAvailable] = useState(false)

  useEffect(() => {
    fetch('/api/me').then((r) => (r.ok ? r.json() : null)).then(setMe).catch(() => setMe(null)).finally(() => setLoading(false))
    // Passkeys require the server to be configured for WebAuthn AND a secure context
    // (HTTPS or localhost) — over a private IP on plain HTTP they can't be used.
    fetch('/api/features').then((r) => r.json()).then((f) => setPasskeysAvailable(!!f.passkeys && window.isSecureContext)).catch(() => {})
  }, [])

  if (loading) return <Frame><p>Loading…</p></Frame>
  if (!me) return <Login onSuccess={setMe} passkeysAvailable={passkeysAvailable} />
  return <AppShell me={me} onLogout={() => setMe(null)} passkeysAvailable={passkeysAvailable} />
}

function Frame({ children }: { children: ReactNode }) {
  return (
    <main style={{ maxWidth: 1200, margin: '2.5rem auto', padding: '0 1.25rem' }}>
      <h1 style={{ marginBottom: 0 }}>Argus</h1>
      <p style={{ color: '#888', marginTop: 4 }}>Monitoring cockpit</p>
      {children}
    </main>
  )
}

function Login({ onSuccess, passkeysAvailable }: { onSuccess: (m: Me) => void; passkeysAvailable: boolean }) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

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

  if (mfaToken) {
    return (
      <Frame>
        <section style={{ ...card, maxWidth: 380, marginTop: '1.5rem' }}>
          <h2 style={{ fontSize: '1rem', marginTop: 0 }}>Two-factor authentication</h2>
          <p style={{ color: '#aaa', marginTop: 0 }}>Enter the 6-digit code from your authenticator, or a recovery code.</p>
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
              style={{ ...input, width: '100%', marginBottom: '1rem', letterSpacing: '0.15em' }}
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
            {error && <p style={{ color: 'crimson', margin: '0 0 0.75rem' }}>{error}</p>}
            <button type="submit" disabled={busy} style={{ ...btn, width: '100%' }}>{busy ? 'Verifying…' : 'Verify'}</button>
          </form>
          <button onClick={() => { setMfaToken(null); setCode(''); setError(null) }} style={{ ...ghost, width: '100%', marginTop: '0.6rem' }}>Back</button>
        </section>
      </Frame>
    )
  }

  return (
    <Frame>
      <section style={{ ...card, maxWidth: 380, marginTop: '1.5rem' }}>
        <h2 style={{ fontSize: '1rem', marginTop: 0 }}>Sign in</h2>
        <form onSubmit={submitPassword}>
          <label style={{ display: 'block', marginBottom: '0.75rem' }}>Email
            <input style={{ ...input, width: '100%', marginTop: 4 }} type="email" value={email} autoComplete="username" onChange={(e) => setEmail(e.target.value)} required />
          </label>
          <label style={{ display: 'block', marginBottom: '1rem' }}>Password
            <input style={{ ...input, width: '100%', marginTop: 4 }} type="password" value={password} autoComplete="current-password" onChange={(e) => setPassword(e.target.value)} required />
          </label>
          {error && <p style={{ color: 'crimson', margin: '0 0 0.75rem' }}>{error}</p>}
          <button type="submit" disabled={busy} style={{ ...btn, width: '100%' }}>{busy ? 'Signing in…' : 'Sign in'}</button>
        </form>
        {passkeysAvailable && (
          <>
            <div style={{ textAlign: 'center', color: '#666', margin: '0.9rem 0 0.6rem', fontSize: '0.85rem' }}>or</div>
            <button onClick={passkeyLogin} disabled={busy} style={{ ...ghost, width: '100%' }}>Sign in with a passkey</button>
          </>
        )}
      </section>
    </Frame>
  )
}

type View = 'overview' | 'monitoring' | 'notifications' | 'probes' | 'users' | 'account' | 'list'
const VIEW_TITLES: Record<View, [string, string]> = {
  overview: ['Overview', 'What needs attention right now'],
  monitoring: ['Monitoring', 'Sites, hosts and sensors'],
  notifications: ['Notifications', 'Alert routing and channels'],
  probes: ['Probes', 'Site probe enrollment'],
  users: ['Users', 'Accounts and access'],
  account: ['Account', 'Your security settings'],
  list: ['Sensors', 'Filtered across all sites'],
}

const ic = {
  overview: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M3 12a9 9 0 0 1 18 0" /><path d="M12 12l4-2" /><circle cx="12" cy="12" r="1.6" fill="currentColor" stroke="none" /></svg>,
  monitoring: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><rect x="3" y="4" width="18" height="7" rx="2" /><rect x="3" y="13" width="18" height="7" rx="2" /><path d="M7 7.5h.01M7 16.5h.01" /></svg>,
  notifications: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9" /><path d="M13.7 21a2 2 0 0 1-3.4 0" /></svg>,
  probes: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="12" r="2" /><path d="M16.2 7.8a6 6 0 0 1 0 8.4M7.8 16.2a6 6 0 0 1 0-8.4M19 5a10 10 0 0 1 0 14M5 19A10 10 0 0 1 5 5" /></svg>,
  users: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="9" cy="8" r="3.2" /><path d="M3.5 20a5.5 5.5 0 0 1 11 0" /><path d="M16 5.2a3.2 3.2 0 0 1 0 6M17 14.5a5.5 5.5 0 0 1 3.5 5.5" /></svg>,
  account: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="8" r="3.4" /><path d="M5 20a7 7 0 0 1 14 0" /></svg>,
  logout: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M15 12H3M9 6l-6 6 6 6M15 4h4a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-4" /></svg>,
  err: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="9" /><path d="M15 9l-6 6M9 9l6 6" /></svg>,
  warn: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z" /><path d="M12 9.5v4M12 17h.01" /></svg>,
  acked: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M21 15a2 2 0 0 1-2 2H8l-4 4V5a2 2 0 0 1 2-2h13a2 2 0 0 1 2 2z" /><path d="M8.5 10.3l2.4 2.4 4.6-4.6" /></svg>,
  ok: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.6"><path d="M20 6 9 17l-5-5" /></svg>,
  paused: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"><rect x="6" y="5" width="4" height="14" rx="1" /><rect x="14" y="5" width="4" height="14" rx="1" /></svg>,
  hidden: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"><path d="M2 12s3.5-7 10-7 10 7 10 7a17 17 0 0 1-2.2 2.9M3 3l18 18M9.5 9.5a3 3 0 0 0 4.2 4.2" /></svg>,
}
const sun = <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="12" r="4.5" /><path d="M12 2v2.5M12 19.5V22M2 12h2.5M19.5 12H22M4.9 4.9l1.8 1.8M17.3 17.3l1.8 1.8M19.1 4.9l-1.8 1.8M6.7 17.3l-1.8 1.8" /></svg>
const moon = <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M20 14.5A8 8 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5Z" /></svg>

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

function AppShell({ me, onLogout, passkeysAvailable }: { me: Me; onLogout: () => void; passkeysAvailable: boolean }) {
  const [view, setView] = useState<View>('overview')
  const [collapsed, setCollapsed] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)
  const [theme, toggleTheme] = useTheme()
  const [sensors, setSensors] = useState<SensorRow[]>([])
  const [listFilter, setListFilter] = useState<string>('error')
  const canPause = me.role === 'admin' || me.role === 'helpdesk'

  useEffect(() => {
    const load = () => fetch('/api/sensors').then((r) => (r.ok ? r.json() : [])).then((s) => setSensors(s || [])).catch(() => {})
    load(); const t = setInterval(load, 30000); const off = onDataRefresh(load); return () => { clearInterval(t); off() }
  }, [])
  const cnt = (st: string) => sensors.filter((s) => s.state === st).length
  const errN = cnt('error'), warnN = cnt('warning'), ackN = cnt('acked'), pausedN = cnt('paused'), hiddenN = cnt('hidden'), okN = cnt('ok')

  // Deep-link target: Overview / lists ask the tree to open a host (and optionally a sensor's chart).
  const [treeTarget, setTreeTarget] = useState<{ hostId: string; itemId?: string; n: number } | null>(null)
  const navN = useRef(0)
  function goHost(hostId: string) { navN.current += 1; setTreeTarget({ hostId, n: navN.current }); setView('monitoring'); setMenuOpen(false) }
  function goSensor(hostId: string, itemId: string) { navN.current += 1; setTreeTarget({ hostId, itemId, n: navN.current }); setView('monitoring'); setMenuOpen(false) }
  function openList(st: string) { setListFilter(st); setView('list'); setMenuOpen(false) }

  async function logout() { await fetch('/api/logout', { method: 'POST' }).catch(() => {}); onLogout() }
  function goto(v: View) { setView(v); setMenuOpen(false) }

  const nav = (id: View, label: string, opts?: { count?: number; soon?: boolean }) => (
    <button className={'nav' + (view === id ? ' active' : '')} onClick={() => goto(id)}>
      {ic[id]}
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
    <div className={'app-shell' + (collapsed ? ' collapsed' : '')}>
      <aside className="sidebar">
        <div className="brand">
          <svg className="eye" width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><path d="M1.5 12S5 5 12 5s10.5 7 10.5 7-3.5 7-10.5 7S1.5 12 1.5 12Z" /><circle cx="12" cy="12" r="3.2" fill="currentColor" stroke="none" /></svg>
          <div><div className="word">ARGUS</div><div className="sub">Monitoring</div></div>
        </div>
        <div className="navlabel">Watch</div>
        {nav('overview', 'Overview', { count: errN })}
        {nav('monitoring', 'Monitoring')}
        <div className="navlabel">Configure</div>
        {nav('notifications', 'Notifications', { soon: true })}
        {nav('probes', 'Probes', { soon: true })}
        {me.role === 'admin' && <><div className="navlabel">Admin</div>{nav('users', 'Users')}</>}
        <div className="side-foot">
          <button className="themebtn" onClick={toggleTheme}>{theme === 'dark' ? moon : sun}<span>Theme</span></button>
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
          <button className="iconbtn" title="Toggle sidebar" aria-label="Toggle sidebar" onClick={() => setCollapsed((c) => !c)}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"><path d="M3.5 6h17M3.5 12h17M3.5 18h17" /></svg>
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
        <div className="content">
          {view === 'overview' && <OverviewView goHost={goHost} goSensor={goSensor} />}
          {view === 'list' && <StatusListView filter={listFilter} sensors={sensors} canPause={canPause} goHost={goHost} goSensor={goSensor} onBack={() => setView('overview')} />}
          {view === 'monitoring' && <MonitoringView role={me.role} target={treeTarget} />}
          {view === 'notifications' && <NotificationsView />}
          {view === 'probes' && <ProbesView />}
          {view === 'users' && me.role === 'admin' && <UsersView />}
          {view === 'account' && <AccountView passkeysAvailable={passkeysAvailable} />}
        </div>
      </div>
    </div>
  )
}

function NotificationsView() {
  return (
    <div className="panel">
      <div className="ph-hero">
        <svg className="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9" /><path d="M13.7 21a2 2 0 0 1-3.4 0" /></svg>
        <div className="soon-badge">Coming soon</div>
        <h2>Alerting &amp; notifications</h2>
        <p>Route Warning and Error events to the channels you choose — per site or globally. Acknowledged, paused, and hidden items stay quiet, and you'll get recovery notices when things clear.</p>
      </div>
      <div className="chan-grid">
        <div className="chan"><div className="ct"><span className="ci" style={{ background: '#5865F2' }}>D</span> Discord</div><p>Per-site webhooks, so each location posts to its own channel.</p><div className="st">Not configured yet</div></div>
        <div className="chan"><div className="ct"><span className="ci" style={{ background: '#229ED9' }}>T</span> Telegram</div><p>One shared bot with a topic per site for tidy threads.</p><div className="st">Not configured yet</div></div>
        <div className="chan"><div className="ct"><span className="ci" style={{ background: '#6b7686' }}>@</span> Email</div><p>SMTP delivery for a mailbox or a distribution list.</p><div className="st">Not configured yet</div></div>
      </div>
    </div>
  )
}

function ProbesView() {
  const [proxies, setProxies] = useState<Proxy[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  useEffect(() => {
    const load = () => fetch('/api/proxies')
      .then(async (r) => { if (!r.ok) throw new Error(await errText(r, 'Failed to load probes')); return r.json() })
      .then((p: Proxy[]) => { setProxies(p || []); setError(null) })
      .catch((e) => setError(e instanceof Error ? e.message : 'Failed to load probes'))
    load(); const t = setInterval(load, 30000); return () => clearInterval(t)
  }, [])
  return (
    <div className="panel">
      <div className="ph-hero">
        <svg className="ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><circle cx="12" cy="12" r="2" /><path d="M16.2 7.8a6 6 0 0 1 0 8.4M7.8 16.2a6 6 0 0 1 0-8.4M19 5a10 10 0 0 1 0 14M5 19A10 10 0 0 1 5 5" /></svg>
        <div className="soon-badge">Enrollment coming soon</div>
        <h2>Site probes</h2>
        <p>Each site collects through a probe that pushes to the core over a secure tunnel. Below is the live status of the probes the core knows about; one-click token enrollment for new sites is on the way.</p>
      </div>
      <table className="enroll">
        <thead><tr><th>Probe</th><th>Status</th><th>Last check-in</th><th>Mode</th></tr></thead>
        <tbody>
          {error && <tr><td colSpan={4} style={{ color: 'var(--err)' }}>{error}</td></tr>}
          {!error && proxies === null && <tr><td colSpan={4} style={{ color: 'var(--muted)' }}>Loading…</td></tr>}
          {!error && proxies && proxies.length === 0 && <tr><td colSpan={4} style={{ color: 'var(--muted)' }}>No probes have reported to the core yet.</td></tr>}
          {!error && proxies && proxies.map((p) => (
            <tr key={p.name}>
              <td><strong>{p.name}</strong></td>
              <td>{p.online ? <span className="tag online">● online</span> : <span className="tag pending">offline</span>}</td>
              <td className="mono" style={{ color: p.last_access ? undefined : 'var(--faint)' }}>{p.last_access ? relTime(p.last_access) : 'never'}</td>
              <td className="mono" style={{ color: 'var(--muted)' }}>{p.mode}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function OverviewView({ goHost, goSensor }: { goHost: (hostId: string) => void; goSensor: (hostId: string, itemId: string) => void }) {
  const [rows, setRows] = useState<ProblemRow[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [mode, setMode] = useState<'errors' | 'both'>('errors')

  function load() {
    fetch('/api/problems')
      .then(async (r) => { if (!r.ok) { setError(await errText(r, 'Failed to load problems')); return } setRows(await r.json()); setError(null) })
      .catch(() => setError('Failed to load problems'))
  }
  useEffect(() => { load(); const t = setInterval(load, 30000); const off = onDataRefresh(load); return () => { clearInterval(t); off() } }, [])

  async function ack(p: ProblemRow, seconds: number | null) {
    await fetch(`/api/events/${p.event_id}/ack`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ duration_seconds: seconds ?? 0 }) }).catch(() => {})
    load(); fireDataRefresh()
  }
  async function unack(p: ProblemRow) {
    await fetch(`/api/events/${p.event_id}/ack`, { method: 'DELETE' }).catch(() => {})
    load(); fireDataRefresh()
  }

  const filtered = (rows || [])
    .filter((p) => (mode === 'errors' ? p.state === 'error' && !p.acknowledged : p.state === 'error' || p.state === 'warning'))
    .sort((a, b) => (stateRank[b.state] - stateRank[a.state]) || (Number(a.acknowledged) - Number(b.acknowledged)) || (b.clock - a.clock))

  return (
    <div className="panel">
      <div className="phead">
        <h2>Active problems</h2><span className="hint">across all sites</span>
        <div className="tools">
          <div className="seg">
            <button className={mode === 'errors' ? 'on' : ''} onClick={() => setMode('errors')}>Errors</button>
            <button className={mode === 'both' ? 'on' : ''} onClick={() => setMode('both')}>Errors + Warnings</button>
          </div>
        </div>
      </div>
      {error && <div style={{ padding: '0.9rem 16px', color: 'var(--err)' }}>{error}</div>}
      {rows === null && !error && <div style={{ padding: '0.9rem 16px', color: 'var(--muted)' }}>Loading…</div>}
      {rows !== null && !error && filtered.length === 0 && <div style={{ padding: '0.9rem 16px', color: 'var(--ok)' }}>✓ All clear — nothing {mode === 'errors' ? 'in error' : 'to report'}.</div>}
      <div className="rows">
        {filtered.map((p) => {
          const c = healthColor(p.state, p.acknowledged)
          const hasItem = p.item_ids && p.item_ids.length > 0
          return (
            <div className={'row' + (p.acknowledged ? ' acked' : '')} key={p.event_id}>
              <div className="stripe" style={{ background: c }} />
              <span className="sd" style={{ background: c }} />
              <span className="hname lnk-host" onClick={() => goHost(p.host_id)}>{p.host_name}</span>
              <span className={'desc' + (hasItem ? ' lnk-sensor' : '')} onClick={hasItem ? () => goSensor(p.host_id, p.item_ids[0]) : undefined}> · {p.name}</span>
              <div className="right">
                <span className="when">{relTime(p.clock)}</span>
                {p.acknowledged
                  ? <><span className="acktag">✓ acked · {untilLabel(p.ack_until)}</span><button className="btn ghost" onClick={() => unack(p)}>Unacknowledge</button></>
                  : <DurationButton label="Acknowledge" onPick={(s) => ack(p, s)} />}
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function DashboardView() {
  const [health, setHealth] = useState<Health | null>(null)
  useEffect(() => { fetch('/api/health').then((r) => r.json()).then(setHealth).catch(() => setHealth(null)) }, [])
  return (
    <section style={card}>
      <h2 style={{ fontSize: '1rem', marginTop: 0 }}>System health</h2>
      {!health && <p>Checking…</p>}
      {health && (
        <ul style={{ lineHeight: 1.9, margin: 0, paddingLeft: '1.1rem' }}>
          <li>Backend: <strong style={{ color: 'seagreen' }}>{health.status}</strong></li>
          <li>Zabbix API:{' '}
            {health.zabbix.reachable
              ? <strong style={{ color: 'seagreen' }}>reachable (v{health.zabbix.version})</strong>
              : <strong style={{ color: 'crimson' }}>unreachable</strong>}
          </li>
          {health.zabbix.error && <li style={{ color: '#c66', listStyle: 'none', marginLeft: '-1.1rem' }}>↳ {health.zabbix.error}</li>}
        </ul>
      )}
    </section>
  )
}

const kbIcon = {
  pause: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><rect x="6" y="5" width="4" height="14" rx="1" /><rect x="14" y="5" width="4" height="14" rx="1" /></svg>,
  hide: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M2 12s3.5-7 10-7 10 7 10 7" /><path d="M3 3l18 18" /><path d="M9.5 9.5a3 3 0 0 0 4.2 4.2" /></svg>,
  resume: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M7 5l12 7-12 7z" /></svg>,
  show: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z" /><circle cx="12" cy="12" r="3" /></svg>,
  ack: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M22 11.2V12a10 10 0 1 1-5.9-9.1" /><path d="M22 4 12 14.5l-3-3" /></svg>,
}

// A kebab menu action. onClick fires immediately; onPick opens the duration submenu first and
// fires with the chosen seconds (null = indefinite). sep renders a divider.
type KAction = { label: string; icon?: ReactNode; danger?: boolean; onClick?: () => void; onPick?: (s: number | null) => void; sep?: boolean }

function Kebab({ actions, disabled, up }: { actions: KAction[]; disabled?: boolean; up?: boolean }) {
  const [open, setOpen] = useState(false)
  const [dur, setDur] = useState<KAction | null>(null)
  const [custom, setCustom] = useState(false)
  const [val, setVal] = useState('')
  function close() { setOpen(false); setDur(null); setCustom(false) }
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
      <button className={'kebab' + (open ? ' open' : '')} title="Actions" disabled={disabled} onClick={() => { if (!open) { setDur(null); setCustom(false) } setOpen((o) => !o) }}>⋮</button>
      {open && (
        <>
          <div onClick={close} style={{ position: 'fixed', inset: 0, zIndex: 30 }} />
          <div className={'menu' + (up ? ' up' : '')} style={{ zIndex: 31, minWidth: dur && custom ? 240 : 180 }} onClick={(e) => e.stopPropagation()}>
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

function MonitoringView({ role, target }: { role: string; target: { hostId: string; itemId?: string; n: number } | null }) {
  const [hosts, setHosts] = useState<Host[]>([])
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set())
  const [openHost, setOpenHost] = useState<string | null>(null)
  const [showAll, setShowAll] = useState(false)
  const [busyId, setBusyId] = useState<string | null>(null)
  const canPause = role === 'admin' || role === 'helpdesk'

  function load(initial = false) {
    if (initial) setLoading(true)
    fetch('/api/hosts')
      .then(async (r) => { if (!r.ok) { setError(await errText(r, 'Failed to load hosts')); return } setHosts(await r.json()); setError(null) })
      .catch(() => setError('Failed to load hosts'))
      .finally(() => { if (initial) setLoading(false) })
  }
  useEffect(() => { load(true); const t = setInterval(() => load(false), 30000); const off = onDataRefresh(() => load(false)); return () => { clearInterval(t); off() } }, [])

  // Respond to a deep-link from the Overview: expand the target host's site and open the host
  // (its sensor chart is opened by HostItems via autoOpenItem). Re-runs once hosts have loaded.
  useEffect(() => {
    if (!target) return
    const h = hosts.find((x) => x.id === target.hostId)
    if (!h) return
    const g = (h.groups && h.groups.length ? h.groups : ['Ungrouped'])[0]
    setCollapsed((c) => { const n = new Set(c); n.delete(g); return n })
    setOpenHost(g + '::' + target.hostId)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [target?.n, hosts.length])

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

  // Build the site tree: site = Zabbix host group. A host in several groups appears under each;
  // hosts with no group fall under "Ungrouped".
  const sites: Record<string, Host[]> = {}
  for (const h of hosts) {
    const gs = h.groups && h.groups.length ? h.groups : ['Ungrouped']
    for (const g of gs) { (sites[g] = sites[g] || []).push(h) }
  }
  const siteNames = Object.keys(sites).sort((a, b) => a.localeCompare(b))
  function siteWorst(hs: Host[]): string { let s = 'ok'; for (const h of hs) if (!h.paused && !h.hidden && stateRank[h.state] > stateRank[s]) s = h.state; return s }
  function toggleSite(name: string) { setCollapsed((c) => { const n = new Set(c); if (n.has(name)) n.delete(name); else n.add(name); return n }) }

  return (
    <div className="panel">
      <div className="phead">
        <h2>Sites &amp; hosts</h2>
        <span className="hint">{siteNames.length} group{siteNames.length === 1 ? '' : 's'} · {hosts.length} host{hosts.length === 1 ? '' : 's'}</span>
        <div className="tools">
          <div className="seg">
            <button className={!showAll ? 'on' : ''} onClick={() => setShowAll(false)}>Key sensors</button>
            <button className={showAll ? 'on' : ''} onClick={() => setShowAll(true)}>All sensors</button>
          </div>
        </div>
      </div>
      {loading && <div style={{ padding: '0.9rem 16px', color: 'var(--muted)' }}>Loading…</div>}
      {error && <div style={{ padding: '0.9rem 16px', color: 'var(--err)' }}>{error}</div>}
      {!loading && !error && hosts.length === 0 && <div style={{ padding: '0.9rem 16px', color: 'var(--muted)' }}>No hosts found.</div>}
      {siteNames.map((name) => {
        const hs = sites[name]
        const open = !collapsed.has(name)
        return (
          <div className="site" key={name}>
            <div className="site-head" onClick={() => toggleSite(name)}>
              <svg className={'chev' + (open ? ' open' : '')} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M9 6l6 6-6 6" /></svg>
              <span className="name">{name}</span>
              <span className="loc">{hs.length} host{hs.length === 1 ? '' : 's'}</span>
              <div className="right"><span style={{ width: 9, height: 9, borderRadius: '50%', background: stateColor[siteWorst(hs)] || 'var(--muted)' }} /></div>
            </div>
            {open && hs.map((h) => {
              const key = name + '::' + h.id
              const hopen = openHost === key
              return (
                <div className="host" key={key}>
                  <div className="host-head" onClick={() => setOpenHost(hopen ? null : key)}>
                    <svg className={'chev' + (hopen ? ' open' : '')} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M9 6l6 6-6 6" /></svg>
                    <span style={{ width: 9, height: 9, borderRadius: '50%', flexShrink: 0, background: dotColor(h.paused, h.hidden, h.state) }} />
                    <span className="hn">{h.name}</span>
                    {h.paused && <span className="kind" style={{ color: PAUSED_BLUE }}>· paused {untilLabel(h.paused_until)}</span>}
                    {h.hidden && <span className="kind" style={{ color: HIDDEN_GREY }}>· hidden {untilLabel(h.hidden_until)}</span>}
                    <div className="right">
                      {!h.paused && !h.hidden && h.problems > 0 && <span style={{ color: stateColor[h.state], fontSize: 12 }}>{h.problems} problem{h.problems === 1 ? '' : 's'}</span>}
                      {canPause && (
                        <Kebab disabled={busyId === h.id} actions={[
                          h.paused ? { label: 'Resume', icon: kbIcon.resume, onClick: () => clearHostState(h, 'pause') } : { label: 'Pause', icon: kbIcon.pause, onPick: (s) => setHostState(h, 'pause', s) },
                          h.hidden ? { label: 'Show', icon: kbIcon.show, onClick: () => clearHostState(h, 'hide') } : { label: 'Hide', icon: kbIcon.hide, onPick: (s) => setHostState(h, 'hide', s) },
                        ]} />
                      )}
                    </div>
                  </div>
                  {hopen && <div className="host-body"><HostItems hostId={h.id} canPause={canPause} hostPaused={h.paused} hostHidden={h.hidden} showAll={showAll} autoOpenItem={target && target.hostId === h.id ? target.itemId : undefined} /></div>}
                </div>
              )
            })}
          </div>
        )
      })}
    </div>
  )
}

function HostItems({ hostId, canPause, hostPaused, hostHidden, showAll, autoOpenItem }: { hostId: string; canPause: boolean; hostPaused: boolean; hostHidden: boolean; showAll: boolean; autoOpenItem?: string }) {
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

  if (error) return <div style={{ color: 'var(--err)', padding: '0.4rem 0' }}>{error}</div>
  if (!items) return <div style={{ color: 'var(--muted)', padding: '0.4rem 0' }}>Loading sensors…</div>

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
        ? <div style={{ color: 'var(--muted)', padding: '0.2rem 0 0.4rem' }}>{showAll ? 'No sensors.' : 'No recognized sensors — try “All sensors”.'}</div>
        : (
          <table className="sensors">
            <thead><tr><th>Sensor</th><th>Value</th><th style={{ textAlign: 'right' }}>Last check</th></tr></thead>
            <tbody>
              {items.map((it, idx) => {
                const st = itemState[it.id]
                const open = openItem === it.id
                const clickable = it.numeric && it.supported
                const label = it.label || it.name
                // A sensor inherits its host's paused/hidden state; its own toggle is locked while
                // the host controls it.
                const effPaused = it.paused || hostPaused
                const effHidden = it.hidden || hostHidden
                const newGroup = !showAll && it.category && it.category !== items[idx - 1]?.category
                const rowClass = st && !itemAcked[it.id] ? (st === 'error' ? 'err' : 'warn') : ''
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
                return (
                  <Fragment key={it.id}>
                    {newGroup && <tr className="cat"><td colSpan={3}>{it.category}</td></tr>}
                    <tr className={rowClass} onClick={clickable ? () => setOpenItem(open ? null : it.id) : undefined} style={{ opacity: it.supported ? 1 : 0.55, cursor: clickable ? 'pointer' : 'default' }}>
                      <td className="namecell">
                        <span className={'sname' + (clickable ? ' sclick' : '')} style={{ display: 'flex', alignItems: 'center', gap: 6, opacity: effPaused || effHidden ? 0.6 : 1 }}>
                          {clickable && <span style={{ color: 'var(--accent)', display: 'inline-block', transform: open ? 'rotate(90deg)' : 'none' }}>›</span>}
                          {label}
                          {effPaused && <span style={{ color: PAUSED_BLUE, fontSize: 11 }}> (paused · {hostPaused && !it.paused ? 'host' : untilLabel(it.paused_until)})</span>}
                          {effHidden && <span style={{ color: HIDDEN_GREY, fontSize: 11 }}> (hidden · {hostHidden && !it.hidden ? 'host' : untilLabel(it.hidden_until)})</span>}
                        </span>
                      </td>
                      <td className="mono val">
                        {it.supported
                          ? (() => { const [dv, du] = readingParts(it.last_value, it.units); return <span>{dv}{du ? <span className="unit"> {du}</span> : null}</span> })()
                          : <span style={{ color: 'var(--err)' }}>not supported</span>}
                      </td>
                      <td>
                        <div className="lccell">
                          <span className="when">{relTime(it.last_clock)}</span>
                          {canPause && actions.length > 0 && <Kebab disabled={busyItem === it.id} actions={actions} />}
                        </div>
                      </td>
                    </tr>
                    {open && clickable && (
                      <tr className="chartrow"><td colSpan={3}><SensorChart itemId={it.id} units={it.units} /></td></tr>
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
function StatusListView({ filter, sensors, canPause, goHost, goSensor, onBack }: { filter: string; sensors: SensorRow[]; canPause: boolean; goHost: (h: string) => void; goSensor: (h: string, i: string) => void; onBack: () => void }) {
  const [busy, setBusy] = useState<string | null>(null)
  const rows = sensors.filter((s) => s.state === filter)

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
  function actionsFor(s: SensorRow): KAction[] {
    if (s.state === 'paused') return [{ label: 'Resume', icon: kbIcon.resume, onClick: () => clearItem(s, 'pause') }, { label: 'Hide', icon: kbIcon.hide, onPick: (sec) => itemAction(s, 'hide', sec) }]
    if (s.state === 'hidden') return [{ label: 'Show', icon: kbIcon.show, onClick: () => clearItem(s, 'hide') }, { label: 'Pause', icon: kbIcon.pause, onPick: (sec) => itemAction(s, 'pause', sec) }]
    return [{ label: 'Pause', icon: kbIcon.pause, onPick: (sec) => itemAction(s, 'pause', sec) }, { label: 'Hide', icon: kbIcon.hide, onPick: (sec) => itemAction(s, 'hide', sec) }]
  }
  const durCol = filter === 'paused' ? 'Paused' : filter === 'hidden' ? 'Hidden' : 'Last check'
  return (
    <div className="panel">
      <div className="phead">
        <h2>{STATE_LABEL[filter]} sensors</h2>
        <span className="hint">{rows.length} sensor{rows.length === 1 ? '' : 's'} · across all sites</span>
        <div className="tools"><button className="btn ghost" onClick={onBack}>← Back to overview</button></div>
      </div>
      {rows.length === 0
        ? <div style={{ padding: '0.9rem 16px', color: 'var(--muted)' }}>No {STATE_LABEL[filter].toLowerCase()} sensors.</div>
        : (
          <table className="slist">
            <thead><tr><th>Host</th><th>Sensor</th><th>Value</th><th>{durCol}</th><th /></tr></thead>
            <tbody>
              {rows.map((s) => {
                const clickable = s.numeric && s.supported
                return (
                  <tr key={s.item_id}>
                    <td className="slhost" style={{ borderLeftColor: STATE_VAR[s.state] || 'var(--border)' }}><span className="lnk-host" onClick={() => goHost(s.host_id)}>{s.host_name}</span></td>
                    <td>{clickable ? <span className="lnk-sensor" onClick={() => goSensor(s.host_id, s.item_id)}>{s.label || s.name}</span> : (s.label || s.name)}</td>
                    <td className="mono val">{s.supported ? (() => { const [dv, du] = readingParts(s.value, s.units); return <span>{dv}{du ? <span className="unit"> {du}</span> : null}</span> })() : <span style={{ color: 'var(--err)' }}>not supported</span>}</td>
                    <td className="mono dur">{relTime(s.last_clock)}</td>
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

const GREEN = '#4fa06f'

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

function buildPlot(data: Series, units: string, width: number): [uPlot.Options, uPlot.AlignedData] {
  const xs = data.points.map((p) => p.t)
  const axisStroke = '#8a8a8a'
  const grid = { stroke: 'rgba(255,255,255,0.06)', width: 1 }
  const ticks = { stroke: 'rgba(255,255,255,0.06)', width: 1 }
  const scaled = scaledUnit(units)
  // Scaled y-axis ticks (bytes/bits/uptime); otherwise default numeric.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const yValues = scaled ? ((_u: any, splits: number[]) => splits.map((v) => fmtNum(v, units))) : undefined
  const yAxis: uPlot.Axis = { stroke: axisStroke, grid, ticks, size: 64, values: yValues as unknown as uPlot.Axis['values'] }
  const xAxis: uPlot.Axis = { stroke: axisStroke, grid, ticks }
  // Legend cells: show the hovered point, or fall back to the latest value when idle (so the
  // legend is never blank). unitLabel is dropped for scaled units since the value carries it.
  const unitLabel = scaled ? '' : units ? ` (${units})` : ''
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const xVal = (u: any, v: number | null) => { const t = v ?? lastVal(u, 0); return t == null ? '--' : new Date(t * 1000).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const yVal = (sidx: number) => (u: any, v: number | null) => { const n = v ?? lastVal(u, sidx); return n == null ? '--' : fmtNum(n, units) }
  const base: Partial<uPlot.Options> = { width, height: 260, scales: { x: { time: true } }, axes: [xAxis, yAxis], legend: { show: true } }

  if (data.kind === 'trend') {
    const avg = data.points.map((p) => (p.avg ?? null))
    const min = data.points.map((p) => (p.min ?? null))
    const max = data.points.map((p) => (p.max ?? null))
    const opts: uPlot.Options = {
      ...base,
      series: [
        { value: xVal },
        { label: `avg${unitLabel}`, stroke: GREEN, width: 1.5, value: yVal(1) },
        { label: 'min', stroke: 'rgba(79,160,111,0.4)', width: 1, value: yVal(2) },
        { label: 'max', stroke: 'rgba(79,160,111,0.4)', width: 1, value: yVal(3) },
      ],
      bands: [{ series: [3, 2], fill: 'rgba(79,160,111,0.12)' }],
    } as uPlot.Options
    const [gx, [ga, gmin, gmax]] = insertGaps(xs, [avg, min, max])
    return [opts, [gx, ga, gmin, gmax] as uPlot.AlignedData]
  }

  const vs = data.points.map((p) => (p.v ?? null))
  const opts: uPlot.Options = {
    ...base,
    series: [{ value: xVal }, { label: `value${unitLabel}`, stroke: GREEN, width: 1.5, fill: 'rgba(79,160,111,0.10)', value: yVal(1) }],
  } as uPlot.Options
  const [gx, [gv]] = insertGaps(xs, [vs])
  return [opts, [gx, gv] as uPlot.AlignedData]
}

function SensorChart({ itemId, units }: { itemId: string; units: string }) {
  const [range, setRange] = useState('2h')
  const [data, setData] = useState<Series | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [tick, setTick] = useState(0)
  const host = useRef<HTMLDivElement>(null)
  const plot = useRef<uPlot | null>(null)
  const lastKey = useRef('')

  // Refresh the open chart periodically so it stays live.
  useEffect(() => { const t = setInterval(() => setTick((x) => x + 1), 60000); return () => clearInterval(t) }, [])

  useEffect(() => {
    let cancelled = false
    // Show the loading state on an item/range change, but not on background refreshes.
    const key = `${itemId}|${range}`
    if (lastKey.current !== key) { setLoading(true); lastKey.current = key }
    setError(null)
    fetch(`/api/items/${itemId}/history?range=${range}`)
      .then(async (r) => { if (!r.ok) throw new Error(await errText(r, 'Failed to load history')); return r.json() })
      .then((d: Series) => { if (!cancelled) setData(d) })
      .catch((e) => { if (!cancelled) { setError(e.message || 'Failed to load history'); setData(null) } })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [itemId, range, tick])

  useEffect(() => {
    if (plot.current) { plot.current.destroy(); plot.current = null }
    if (!host.current || !data || data.points.length === 0) return
    const width = host.current.clientWidth || 600
    const [opts, aligned] = buildPlot(data, units, width)
    plot.current = new uPlot(opts, aligned, host.current)
    return () => { if (plot.current) { plot.current.destroy(); plot.current = null } }
  }, [data, units])

  useEffect(() => {
    function onResize() { if (plot.current && host.current) plot.current.setSize({ width: host.current.clientWidth, height: 260 }) }
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
      {loading && <p style={{ color: '#888', margin: '0.3rem 0' }}>Loading…</p>}
      {error && <p style={{ color: 'crimson', margin: '0.3rem 0' }}>{error}</p>}
      {!loading && !error && data && data.points.length === 0 && <p style={{ color: '#888', margin: '0.3rem 0' }}>No data in this range.</p>}
      <div ref={host} style={{ width: '100%' }} />
    </div>
  )
}

function UsersView() {
  const [users, setUsers] = useState<User[]>([])
  const [error, setError] = useState<string | null>(null)
  const [msg, setMsg] = useState<string | null>(null)
  const [nu, setNu] = useState({ email: '', name: '', surname: '', role: 'viewer', password: '' })

  function load() { fetch('/api/users').then((r) => r.json()).then(setUsers).catch(() => setError('Failed to load users')) }
  useEffect(() => { load() }, [])

  async function fail(res: Response) { setError(await errText(res, 'Request failed')) }

  async function create(e: FormEvent) {
    e.preventDefault(); setError(null); setMsg(null)
    const res = await fetch('/api/users', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(nu) })
    if (!res.ok) return fail(res)
    setNu({ email: '', name: '', surname: '', role: 'viewer', password: '' }); setMsg('User created'); load()
  }
  async function changeRole(u: User, role: string) {
    setError(null); setMsg(null)
    const res = await fetch(`/api/users/${u.id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: u.name, surname: u.surname, role }) })
    if (!res.ok) return fail(res)
    load()
  }
  async function resetPw(u: User) {
    setError(null); setMsg(null)
    const pw = window.prompt(`New password for ${u.email} (min 8 characters):`)
    if (!pw) return
    const res = await fetch(`/api/users/${u.id}/password`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: pw }) })
    if (!res.ok) return fail(res)
    setMsg(`Password reset for ${u.email}`)
  }
  async function resetMfa(u: User) {
    setError(null); setMsg(null)
    if (!window.confirm(`Reset two-factor for ${u.email}? They'll sign in with just their password until they set it up again.`)) return
    const res = await fetch(`/api/users/${u.id}/mfa/reset`, { method: 'POST' })
    if (!res.ok) return fail(res)
    setMsg(`Two-factor reset for ${u.email}`); load()
  }
  async function resetPasskeys(u: User) {
    setError(null); setMsg(null)
    if (!window.confirm(`Remove all passkeys for ${u.email}?`)) return
    const res = await fetch(`/api/users/${u.id}/passkeys/reset`, { method: 'POST' })
    if (!res.ok) return fail(res)
    setMsg(`Passkeys removed for ${u.email}`); load()
  }
  async function del(u: User) {
    setError(null); setMsg(null)
    if (!window.confirm(`Delete ${u.email}?`)) return
    const res = await fetch(`/api/users/${u.id}`, { method: 'DELETE' })
    if (!res.ok) return fail(res)
    load()
  }

  return (
    <section style={{ ...card }}>
      <h2 style={{ fontSize: '1rem', marginTop: 0 }}>Users</h2>
      {error && <p style={{ color: 'crimson' }}>{error}</p>}
      {msg && <p style={{ color: 'seagreen' }}>{msg}</p>}

      <div style={{ overflowX: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.92rem' }}>
          <thead>
            <tr style={{ textAlign: 'left', color: '#aaa' }}>
              <th style={{ padding: '0.4rem 0.5rem' }}>Email</th>
              <th style={{ padding: '0.4rem 0.5rem' }}>Name</th>
              <th style={{ padding: '0.4rem 0.5rem' }}>Role</th>
              <th style={{ padding: '0.4rem 0.5rem' }}>2FA</th>
              <th style={{ padding: '0.4rem 0.5rem' }}>Passkeys</th>
              <th style={{ padding: '0.4rem 0.5rem' }}>Actions</th>
            </tr>
          </thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id} style={{ borderTop: '1px solid #2a2a2a' }}>
                <td style={{ padding: '0.4rem 0.5rem' }}>{u.email}</td>
                <td style={{ padding: '0.4rem 0.5rem' }}>{[u.name, u.surname].filter(Boolean).join(' ') || '—'}</td>
                <td style={{ padding: '0.4rem 0.5rem' }}>
                  <select value={u.role} onChange={(e) => changeRole(u, e.target.value)} style={input}>
                    {ROLES.map((r) => <option key={r} value={r}>{r}</option>)}
                  </select>
                </td>
                <td style={{ padding: '0.4rem 0.5rem' }}>
                  {u.mfa_enabled
                    ? <span style={{ color: 'seagreen', fontWeight: 600 }}>on</span>
                    : <span style={{ color: '#777' }}>off</span>}
                </td>
                <td style={{ padding: '0.4rem 0.5rem' }}>
                  {u.passkeys ? <span style={{ color: 'seagreen', fontWeight: 600 }}>{u.passkeys}</span> : <span style={{ color: '#777' }}>0</span>}
                </td>
                <td style={{ padding: '0.4rem 0.5rem', display: 'flex', gap: '0.4rem', flexWrap: 'wrap' }}>
                  <button onClick={() => resetPw(u)} style={ghost}>Reset password</button>
                  {u.mfa_enabled && <button onClick={() => resetMfa(u)} style={ghost}>Reset 2FA</button>}
                  {!!u.passkeys && <button onClick={() => resetPasskeys(u)} style={ghost}>Reset passkeys</button>}
                  <button onClick={() => del(u)} style={{ ...ghost, borderColor: '#5a2a2a', color: '#e59' }}>Delete</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <h3 style={{ fontSize: '0.95rem', marginBottom: '0.5rem' }}>Add user</h3>
      <form onSubmit={create} style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5rem', alignItems: 'center' }}>
        <input style={input} type="email" placeholder="email" value={nu.email} onChange={(e) => setNu({ ...nu, email: e.target.value })} required />
        <input style={input} placeholder="name" value={nu.name} onChange={(e) => setNu({ ...nu, name: e.target.value })} />
        <input style={input} placeholder="surname" value={nu.surname} onChange={(e) => setNu({ ...nu, surname: e.target.value })} />
        <select style={input} value={nu.role} onChange={(e) => setNu({ ...nu, role: e.target.value })}>
          {ROLES.map((r) => <option key={r} value={r}>{r}</option>)}
        </select>
        <input style={input} type="password" placeholder="password (min 8)" value={nu.password} onChange={(e) => setNu({ ...nu, password: e.target.value })} required />
        <button type="submit" style={btn}>Add</button>
      </form>
    </section>
  )
}

function AccountView({ passkeysAvailable }: { passkeysAvailable: boolean }) {
  return (
    <div style={{ display: 'grid', gap: '1.25rem' }}>
      <PasswordCard />
      <MfaCard />
      {passkeysAvailable && <PasskeyCard />}
    </div>
  )
}

function PasswordCard() {
  const [cur, setCur] = useState('')
  const [next, setNext] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [msg, setMsg] = useState<string | null>(null)

  async function submit(e: FormEvent) {
    e.preventDefault(); setError(null); setMsg(null)
    const res = await fetch('/api/me/password', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ current_password: cur, new_password: next }) })
    if (!res.ok) { setError(await errText(res, 'Request failed')); return }
    setCur(''); setNext(''); setMsg('Password changed')
  }

  return (
    <section style={{ ...card, maxWidth: 480 }}>
      <h2 style={{ fontSize: '1rem', marginTop: 0 }}>Change my password</h2>
      {error && <p style={{ color: 'crimson' }}>{error}</p>}
      {msg && <p style={{ color: 'seagreen' }}>{msg}</p>}
      <form onSubmit={submit}>
        <label style={{ display: 'block', marginBottom: '0.75rem' }}>Current password
          <input style={{ ...input, width: '100%', marginTop: 4 }} type="password" value={cur} autoComplete="current-password" onChange={(e) => setCur(e.target.value)} required />
        </label>
        <label style={{ display: 'block', marginBottom: '1rem' }}>New password (min 8)
          <input style={{ ...input, width: '100%', marginTop: 4 }} type="password" value={next} autoComplete="new-password" onChange={(e) => setNext(e.target.value)} required />
        </label>
        <button type="submit" style={btn}>Update password</button>
      </form>
    </section>
  )
}

type Enrollment = { secret: string; otpauth_url: string; qr_data_uri: string }

function MfaCard() {
  const [enabled, setEnabled] = useState<boolean | null>(null)
  const [remaining, setRemaining] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const [msg, setMsg] = useState<string | null>(null)
  const [enrollment, setEnrollment] = useState<Enrollment | null>(null)
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[] | null>(null)

  function loadStatus() {
    fetch('/api/me/mfa').then((r) => r.json()).then((d) => { setEnabled(d.enabled); setRemaining(d.recovery_codes_remaining || 0) }).catch(() => setError('Failed to load 2FA status'))
  }
  useEffect(() => { loadStatus() }, [])

  async function startSetup() {
    setError(null); setMsg(null); setCodes(null)
    const res = await fetch('/api/me/mfa/setup', { method: 'POST' })
    if (!res.ok) { setError(await errText(res, 'Could not start setup')); return }
    setEnrollment(await res.json())
  }
  async function confirmEnable(e: FormEvent) {
    e.preventDefault(); setError(null); setMsg(null)
    const res = await fetch('/api/me/mfa/enable', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code }) })
    if (!res.ok) { setError(await errText(res, 'Could not enable 2FA')); return }
    const d = await res.json()
    setEnrollment(null); setCode(''); setCodes(d.recovery_codes); setMsg('Two-factor is now on. Save your recovery codes.'); loadStatus()
  }
  async function disable() {
    setError(null); setMsg(null); setCodes(null)
    const pw = window.prompt('Confirm your password to turn off two-factor:')
    if (!pw) return
    const res = await fetch('/api/me/mfa/disable', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: pw }) })
    if (!res.ok) { setError(await errText(res, 'Could not disable 2FA')); return }
    setMsg('Two-factor has been turned off.'); loadStatus()
  }
  async function regen() {
    setError(null); setMsg(null); setCodes(null)
    const pw = window.prompt('Confirm your password to generate new recovery codes:')
    if (!pw) return
    const res = await fetch('/api/me/mfa/recovery-codes', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: pw }) })
    if (!res.ok) { setError(await errText(res, 'Could not regenerate codes')); return }
    const d = await res.json()
    setCodes(d.recovery_codes); setMsg('New recovery codes generated. The old ones no longer work.'); loadStatus()
  }

  return (
    <section style={{ ...card, maxWidth: 480 }}>
      <h2 style={{ fontSize: '1rem', marginTop: 0 }}>Two-factor authentication</h2>
      <p style={{ color: '#aaa', marginTop: 0 }}>Use an authenticator app or a password manager such as Bitwarden. Argus uses standard TOTP, so both scanning the QR and pasting the setup key work.</p>
      {error && <p style={{ color: 'crimson' }}>{error}</p>}
      {msg && <p style={{ color: 'seagreen' }}>{msg}</p>}

      {enabled === null && <p>Checking…</p>}

      {codes && <RecoveryCodes codes={codes} />}

      {enabled === false && !enrollment && !codes && (
        <button onClick={startSetup} style={btn}>Enable two-factor</button>
      )}

      {enabled === false && enrollment && (
        <div>
          <p style={{ marginBottom: '0.5rem' }}>1. Scan this QR, or paste the setup key into Bitwarden:</p>
          <img src={enrollment.qr_data_uri} alt="TOTP QR code" style={{ borderRadius: 8, background: 'white', padding: 8 }} width={200} height={200} />
          <p style={{ margin: '0.75rem 0 0.25rem', color: '#aaa' }}>Setup key</p>
          <code style={{ display: 'block', wordBreak: 'break-all', background: 'var(--elevated)', border: '1px solid var(--border)', borderRadius: 6, padding: '0.5rem', fontSize: '0.9rem' }}>{enrollment.secret}</code>
          <form onSubmit={confirmEnable} style={{ marginTop: '1rem' }}>
            <p style={{ marginBottom: '0.4rem' }}>2. Enter the current 6-digit code to confirm:</p>
            <input style={{ ...input, width: '100%', marginBottom: '0.75rem', letterSpacing: '0.15em' }} value={code} onChange={(e) => setCode(e.target.value)} autoComplete="one-time-code" inputMode="numeric" name="otp" placeholder="123456" required />
            <div style={{ display: 'flex', gap: '0.5rem' }}>
              <button type="submit" style={btn}>Confirm & enable</button>
              <button type="button" onClick={() => { setEnrollment(null); setCode(''); setError(null) }} style={ghost}>Cancel</button>
            </div>
          </form>
        </div>
      )}

      {enabled === true && (
        <div>
          <p><strong style={{ color: 'seagreen' }}>On.</strong> <span style={{ color: '#aaa' }}>{remaining} recovery code{remaining === 1 ? '' : 's'} remaining.</span></p>
          <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
            <button onClick={regen} style={ghost}>Regenerate recovery codes</button>
            <button onClick={disable} style={{ ...ghost, borderColor: '#5a2a2a', color: '#e59' }}>Turn off</button>
          </div>
        </div>
      )}
    </section>
  )
}

function RecoveryCodes({ codes }: { codes: string[] }) {
  const text = codes.join('\n')
  const [copied, setCopied] = useState(false)
  async function copy() {
    try {
      // navigator.clipboard only exists in a secure context (HTTPS/localhost); fall
      // back to execCommand so Copy still works over plain HTTP on a private IP.
      if (navigator.clipboard && window.isSecureContext) {
        await navigator.clipboard.writeText(text)
      } else {
        const ta = document.createElement('textarea')
        ta.value = text
        ta.style.position = 'fixed'; ta.style.opacity = '0'
        document.body.appendChild(ta); ta.focus(); ta.select()
        document.execCommand('copy')
        document.body.removeChild(ta)
      }
      setCopied(true); setTimeout(() => setCopied(false), 2000)
    } catch { /* ignore */ }
  }
  function download() {
    const blob = new Blob([text + '\n'], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url; a.download = 'argus-recovery-codes.txt'; a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <div style={{ border: '1px solid #4a4a2a', background: '#1e1e14', borderRadius: 8, padding: '0.75rem 1rem', marginBottom: '1rem' }}>
      <p style={{ marginTop: 0, color: '#d9d97a' }}>Save these recovery codes now — each works once and they won't be shown again.</p>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: '0.25rem 1rem', fontFamily: 'monospace', fontSize: '0.95rem' }}>
        {codes.map((c) => <span key={c}>{c}</span>)}
      </div>
      <div style={{ display: 'flex', gap: '0.5rem', marginTop: '0.75rem' }}>
        <button onClick={copy} style={ghost}>{copied ? 'Copied!' : 'Copy'}</button>
        <button onClick={download} style={ghost}>Download</button>
      </div>
    </div>
  )
}

function PasskeyCard() {
  const [keys, setKeys] = useState<Passkey[]>([])
  const [error, setError] = useState<string | null>(null)
  const [msg, setMsg] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  function load() { fetch('/api/me/passkeys').then((r) => r.json()).then(setKeys).catch(() => setError('Failed to load passkeys')) }
  useEffect(() => { load() }, [])

  async function add() {
    setError(null); setMsg(null)
    const name = window.prompt('Name this passkey (e.g. "Bitwarden", "Phone", "YubiKey"):', 'Bitwarden')
    if (name === null) return
    setBusy(true)
    try {
      await registerPasskey(name || 'Passkey')
      setMsg('Passkey added'); load()
    } catch (e) {
      setError(e instanceof Error && e.message ? e.message : 'Could not add passkey')
    } finally { setBusy(false) }
  }
  async function remove(k: Passkey) {
    setError(null); setMsg(null)
    if (!window.confirm(`Remove passkey "${k.name}"?`)) return
    const res = await fetch(`/api/me/passkeys/${k.id}`, { method: 'DELETE' })
    if (!res.ok) { setError(await errText(res, 'Could not remove passkey')); return }
    setMsg('Passkey removed'); load()
  }

  return (
    <section style={{ ...card, maxWidth: 560 }}>
      <h2 style={{ fontSize: '1rem', marginTop: 0 }}>Passkeys</h2>
      <p style={{ color: '#aaa', marginTop: 0 }}>Sign in without a password using a passkey stored in Bitwarden, your phone, or a security key. Passkeys work when you reach Argus through its HTTPS address.</p>
      {error && <p style={{ color: 'crimson' }}>{error}</p>}
      {msg && <p style={{ color: 'seagreen' }}>{msg}</p>}

      {keys.length === 0 && <p style={{ color: '#888' }}>No passkeys registered yet.</p>}
      {keys.length > 0 && (
        <ul style={{ listStyle: 'none', padding: 0, margin: '0 0 1rem' }}>
          {keys.map((k) => (
            <li key={k.id} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', borderTop: '1px solid #2a2a2a', padding: '0.5rem 0' }}>
              <span>
                <strong>{k.name}</strong>
                <span style={{ color: '#777', marginLeft: '0.5rem', fontSize: '0.85rem' }}>
                  added {new Date(k.created).toLocaleDateString()}
                  {k.last_used ? ` · last used ${new Date(k.last_used).toLocaleDateString()}` : ' · never used'}
                </span>
              </span>
              <button onClick={() => remove(k)} style={{ ...ghost, borderColor: '#5a2a2a', color: '#e59' }}>Remove</button>
            </li>
          ))}
        </ul>
      )}
      <button onClick={add} disabled={busy} style={btn}>{busy ? 'Waiting for authenticator…' : 'Add a passkey'}</button>
    </section>
  )
}

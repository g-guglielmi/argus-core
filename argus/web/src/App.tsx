// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 g-guglielmi

import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, Fragment, type FormEvent, type ReactNode, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { createPortal } from 'react-dom'
import uPlot from 'uplot'
import 'uplot/dist/uPlot.min.css'
import { registerPasskey, loginWithPasskey } from './webauthn'
import { Button, Card, Field, Banner, Badge, CopyButton, Switch, Select, Combobox, Skeleton, EmptyState, copyToClipboard, type ComboOption } from './ui'
import { useConfirm, usePrompt, useAlert } from './dialog'
import { useToast } from './toast'

type Me = { email: string; name: string; surname: string; role: string; mfa_enabled?: boolean; landing?: 'overview' | 'errors'; advanced?: boolean; sites?: string[]; quiet?: { start: number; end: number; floor: number } }
type User = { id: number; email: string; name: string; surname: string; role: string; mfa_enabled?: boolean; passkeys?: number; disabled?: boolean; sites?: string[]; tokens?: number }
type Passkey = { id: string; name: string; created: string; last_used: string | null }
type MaintHit = { id: number; name: string; until: number }
type Host = { id: string; name: string; problems: number; severity: number; state: string; paused: boolean; hidden: boolean; paused_until?: number; hidden_until?: number; groups: string[]; proxy_id?: string; class_id?: string; icon?: string; icmp_item?: string; icmp_ms?: number; unacked?: boolean; acked?: boolean; maintenance?: MaintHit; tags?: HostTag[]; held_behind?: string }
type Group = { id: string; name: string; hosts: number }
type MacroSpec = { macro: string; label: string; hint?: string; required?: boolean; secret?: boolean; derive?: string; options?: string[]; settings_only?: boolean }
type ClassSetup = { title: string; intro?: string; steps?: string[]; command?: string; note?: string }
type DeviceClass = { id: string; label: string; family: string; pattern: string; iface: string; offers_http: boolean; internal?: boolean; icon?: string; macros?: MacroSpec[]; setup?: ClassSetup }
type SnmpCfg = { version: number; community: string; bulk: number; security_name: string; security_level: number; auth_protocol: number; auth_passphrase: string; priv_protocol: number; priv_passphrase: string; context_name: string }
type Iface = { interfaceid?: string; type: number; useip: number; ip: string; dns: string; port: string; snmp?: SnmpCfg; inherit?: boolean }
type MacroField = { macro: string; label: string; hint?: string; secret?: boolean; options?: string[]; value: string; set?: boolean }
type ThresholdField = { macro: string; label: string; unit?: string; default: string; value?: string }
type ThrRowData = { macro: string; label: string; unit?: string; default: string; value?: string }
type ThrTemplate = { template: string; label: string; every_host?: boolean; optional?: boolean; classes?: string[]; thresholds: ThrRowData[] }
type ThresholdsData = { templates: ThrTemplate[] }
// A field of an add-on. terms: an option that runs under a third party's terms (Ookla's speed test),
// chosen only once they're accepted; loaded is the value the dialog opened with (client side).
type AddOnTerms = { value: string; title: string; text: string; links: { label: string; url: string }[] }
type AddOnMacro = { macro: string; label: string; hint?: string; options?: string[]; option_labels?: Record<string, string>; show_if?: { macro: string; value: string }; terms?: AddOnTerms; value: string; loaded?: string }
type AddOnCfg = { id: string; label: string; description: string; enabled: boolean; macros?: AddOnMacro[]; services?: SaaSService[]; accepted?: string[] }
type SaaSService = { id: string; name: string; url: string; default: boolean }
type HostCfg = { hostid: string; host: string; name: string; monitored_by: number; proxy_id?: string; proxy_name?: string; proxy_default?: SnmpCfg; interfaces: Iface[]; class_id?: string; class_label?: string; macros?: MacroField[]; thresholds?: ThresholdField[]; addons?: AddOnCfg[]; vm_names?: string[]; categories?: string[]; category_order?: string[]; master?: MasterCfg; tags?: HostTag[]; own?: { asset_tag: string; location: string }; links?: LinkRow[]; class_links?: LinkRow[]; upstream?: UpstreamInfo }
// A host's master sensor: while it's down, the host's other alerts are held (item_id "" = none).
type MasterCfg = { item_id: string; default_item_id: string; custom: boolean; options: { id: string; label: string }[] }
type Proxy = { id: string; name: string; tags?: string[]; last_access: number; online: boolean; mode: string; probe_host_id?: string; probe_health?: 'ok' | 'warning' | 'error'; enrolled_at?: number; version?: string; target?: string; latest?: string; selfupdate?: boolean; scans?: boolean; sweeps?: boolean; update_status?: string; last_checkin?: number; updater_version?: string; updater_latest?: string; updater_status?: string; update_job?: ProbeJob; updater_job?: ProbeJob; break_glass?: boolean; break_glass_user?: string; sec_updates?: number; reboot_required?: boolean; os_reported_at?: number; os_version?: string; procs?: ProcRow[]; procs_pending?: boolean; procs_note?: string; procs_note_at?: number; procs_since?: number; procs_restarts?: boolean; autoscale?: string; cpu_count?: number; cpu_usable?: number; cpu_load?: number[]; cpu_peak?: number; cpu_starved?: boolean; is_vm?: boolean }
// One Zabbix process kind on a probe: what it runs, Argus's target, the busiest hour at the last
// evaluation, and a hold (put back after a raise that didn't lower its load).
// An update Argus asked a probe's sidecar for, while it is in hand: queued for the sidecar's next
// check-in, updating until the probe reports the new version, or one that didn't take.
type ProbeJob = { state: 'queued' | 'updating' | 'failed'; tag: string; at?: number }
type ProcRow = { name: string; label: string; running: number; target?: number; pinned?: boolean; peak?: number; held?: { from: number; to: number; before: number; after: number; at: number; cpus: number } }
type SearchHit = { type: 'host' | 'sensor' | 'group'; label: string; sub: string; host_id?: string; item_id?: string; group?: string }
type Channel = { id: number; type: string; name: string; enabled: boolean; sites: string[]; tags?: string[]; min_severity: number; delay_min?: number; repeat_min?: number; repeat_min_severity?: number; alerts?: boolean; system_notices?: boolean; who_to_call?: boolean; config: Record<string, string>; last_sent_at?: number; last_error?: string; last_error_at?: number; sent_count?: number }
// Zabbix severities the notifier can act on (it never alerts below Warning). Used by the channel editor.
// Alert levels a notification channel can choose (Zabbix severity floors). The app shows problems as
// warnings (Zabbix Warning) or errors (Average, High, Disaster), so these are the two choices.
const SEVERITIES: { v: number; label: string }[] = [
  { v: 2, label: 'Warnings and errors' },
  { v: 3, label: 'Errors only' },
]
type SensorItem = { id: string; name: string; key: string; last_value: string; units: string; last_clock: number; supported: boolean; numeric: boolean; paused: boolean; hidden: boolean; paused_until?: number; hidden_until?: number; category?: string; label?: string; instance?: string; channel?: string; priority: number; alertable?: boolean; alerts_off?: boolean; thr?: Thr; why?: string; updown?: boolean; note?: SensorNote }
// SensorNote is a note left on a sensor in trouble: shown and sent with it until it is OK again.
type SensorNote = { text: string; by?: string; at: number }
// A sensor's effective warning/high values, read from its own triggers (below = lower is worse).
type Thr = { warn?: number; high?: number; below?: boolean }
type Problem = { event_id: string; name: string; severity: number; state: string; acknowledged: boolean; ack_until?: number; item_ids: string[] }
type TriggerHost = { id: string; name: string }
type Trigger = { id: string; description: string; severity: number; enabled: boolean; problem: boolean; since: number; hosts: TriggerHost[]; sensors: string[] }
type SensorRow = { host_id: string; host_name: string; item_id: string; name: string; label?: string; category?: string; value: string; units: string; last_clock: number; state: string; numeric: boolean; supported: boolean; priority: number; severity: number; reason?: string; why?: string; since?: number; event_ids: string[]; synthetic?: boolean; maintenance?: MaintHit; held_by?: HeldBy; holds?: number; note?: SensorNote; call?: string }
// The master whose outage holds a sensor's alerts: the lists fold the sensor under it.
type HeldBy = { host_id: string; host_name: string; item_id: string; name: string; via?: string }
type SeriesPoint = { t: number; v?: number; min?: number; avg?: number; max?: number }
type Series = { name: string; units: string; kind: 'history' | 'trend'; points: SeriesPoint[] }

const RANGES = ['2h', '2d', '1M', '3M', '6M', '1Y']
// "Today so far" sawtooth counters (reset at midnight, rise through the day - AdGuard's own
// per-day stats) render as a daily stacked-bar chart instead of lines: a day's bar is simply the
// day's PEAK reading. Day-scale ranges only: no 2h/2d (a day chart has 0-2 bars there), a
// dedicated 7d as the default. An item whose key base is listed here opts its whole channel group
// into bar mode; the convention is a ".today" key suffix (the /api/daily endpoint keys on it too).
const RANGES_BARS = ['7d', '1M', '3M', '6M', '1Y']
// A sensor measured by runs (the speed test, every 1 to 24 hours) opens on a week: two hours would
// usually hold no run at all.
const RUN_RANGES = ['2d', '7d', '1M', '3M', '6M', '1Y']
const DAYS_BY_RANGE: Record<string, number> = { '7d': 7, '1M': 30, '3M': 90, '6M': 180, '1Y': 365 }
const BAR_COUNTER_KEYS = new Set(['adguard.queries.today', 'adguard.blocked.today'])
// Daily-ratio sensors (block rate: resets at midnight, converges through the day) bar-chart too,
// but a day's bar is its CLOSING reading, not its peak (the intraday max is small-sample noise).
const BAR_RATE_KEYS = new Set(['adguard.block_pct'])

const stateColor: Record<string, string> = { ok: 'var(--ok)', warning: 'var(--warn)', error: 'var(--err)' }
const stateRank: Record<string, number> = { ok: 0, warning: 1, error: 2 }
// Census/summary state → CSS colour var and label (six buckets, incl. paused/hidden/acked).
const STATE_VAR: Record<string, string> = { ok: 'var(--ok)', warning: 'var(--warn)', error: 'var(--err)', acked: 'var(--acked)', paused: 'var(--paused)', hidden: 'var(--hidden)' }
// Readable names for the values of a choice setting.
const OPTION_LABEL: Record<string, string> = {
  '24h': '24-hour (16:43)', '12h': '12-hour (4:43 PM)',
  restart: 'On: restart the probe to apply', 'next-restart': "On: apply at the probe's next start", off: 'Off',
  '90': '90 days', '365': '1 year', '730': '2 years',
}
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
// needsEye: a host whose dot pulses - in warning or error with something nobody has acknowledged yet
// (paused and hidden hosts never pulse; an acknowledged problem keeps a steady halo).
function needsEye(h: { paused: boolean; hidden: boolean; state: string; unacked?: boolean; maintenance?: MaintHit }): boolean {
  return !h.paused && !h.hidden && !h.maintenance && h.state !== 'ok' && !!h.unacked
}

function dotColor(paused: boolean, hidden: boolean, state: string): string {
  if (paused) return PAUSED_BLUE
  if (hidden) return HIDDEN_GREY
  return STATE_VAR[state] || '#777'
}

// hostShade is the state a host's dot, problem count and graph are coloured by: "acked" once every
// warning and error on it is acknowledged, so nothing about it still reads as an open alert.
function hostShade(h: Host): string { return h.acked ? 'acked' : h.state }

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
function DurationButton({ label, onPick, disabled, borderColor, up }: { label: string; onPick: (seconds: number | null) => void; disabled?: boolean; borderColor?: string; up?: boolean }) {
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
          <div onClick={(e) => e.stopPropagation()} style={{ position: 'absolute', ...(up ? { bottom: '100%', marginBottom: 4 } : { top: '100%', marginTop: 4 }), right: 0, zIndex: 21, background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, minWidth: custom ? 240 : 140, boxShadow: '0 8px 24px rgba(0,0,0,0.45)', overflow: 'hidden' }}>
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
  // A certificate's days left read as whole days, rounded down: 6.6 days left is "6 days", so the
  // reading never contradicts an alert for "less than 7 days" (alerts do the same).
  if (units === 'days') { const d = Math.floor(n); return [String(d), Math.abs(d) === 1 ? 'day' : 'days'] }
  return [roundNum(n), units || '']
}

function fmtNum(n: number, units: string): string {
  const [v, u] = fmtNumParts(n, units)
  return u ? `${v} ${u}` : v
}

// readingParts formats a raw stored value into [display, unit]; non-numeric values (text,
// checksums) are returned untouched with no unit.
// A reading shown with why it isn't a real one (Zabbix's error for a "not supported" sensor, or the
// reason a collector printed when it reports its target down): hover for it, or click / tap to have
// the row spell it out on a full-width line under the sensor (touch screens have no hover).
function WhyText({ why, color, onToggle, children }: { why?: string; color?: string; onToggle: () => void; children: ReactNode }) {
  if (!why) return <span style={{ color }}>{children}</span>
  return (
    <span className="why" style={{ color }} title={why} onClick={(e) => { e.stopPropagation(); onToggle() }}>
      <span className="why-mark">{children}</span>
    </span>
  )
}

// Which rows have their reason spelled out (WhyText), by row id.
function useWhyOpen(): [Record<string, boolean>, (id: string) => void] {
  const [open, setOpen] = useState<Record<string, boolean>>({})
  return [open, (id: string) => setOpen((o) => ({ ...o, [id]: !o[id] }))]
}

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
// thr colours the line by value, as the sensor's big chart does: the accent colour in the normal range,
// the warning colour past the warning value and the error colour past high (mirrored for below-is-worse
// sensors), so a spike that has passed still shows; without it the line takes the sensor's state colour.
function Spark({ values, color, width = 84, fill = false, units, thr }: { values?: number[]; color: string; width?: number; fill?: boolean; units?: string; thr?: Thr }) {
  const gid = 'spk' + useId().replace(/[^A-Za-z0-9_-]/g, '') // a plain id: url(#...) needs no escaping
  if (!values || values.length < 2) return <span style={{ color: 'var(--faint)', fontSize: 12 }}>-</span>
  const w = width, h = 20
  let min = values[0], max = values[0]
  for (const v of values) { if (v < min) min = v; if (v > max) max = v }
  // A near-constant series (a disk drifting a hundredth of a percent) would otherwise stretch its
  // sliver of range across the full height and read as a dramatic ramp. Enforce a minimum span
  // relative to the series' magnitude and center the data within it, so a flat-ish series draws flat
  // while a genuinely varying one is unchanged (its real span dominates the floor -> same mapping).
  // Percentages use the big chart's rule instead (pctRange): at least PCT_MIN_SPAN points, kept
  // inside 0-100. The relative floor alone can't flatten a near-zero percentage - 0 to 0.002 % has a
  // tiny magnitude, so its tiny floor still stretched it into a full-height spike.
  let mid = (min + max) / 2
  let rng = Math.max(max - min, Math.max(Math.abs(min), Math.abs(max), 1e-9) * 0.1)
  if (units === '%' && max - min < PCT_MIN_SPAN) {
    rng = PCT_MIN_SPAN
    mid = Math.min(Math.max(mid, PCT_MIN_SPAN / 2), 100 - PCT_MIN_SPAN / 2)
  }
  const px = (i: number) => (i / (values.length - 1)) * (w - 2) + 1
  // Half-pixel y endpoints (17.5 / 1.5): a horizontal stroke centered ON a pixel boundary (integer y)
  // is split 50/50 across two rows by antialiasing and renders dim and blurry - which made flat lines
  // (constant totals, idle disks) look washed out next to their crisp endpoint dot. Centered on a
  // half-pixel, a flat line paints one full-brightness row.
  const py = (v: number) => h - 2.5 - (((v - mid) / rng) + 0.5) * (h - 4)
  let d = ''
  values.forEach((v, i) => { d += (i ? 'L' : 'M') + px(i).toFixed(1) + ' ' + py(v).toFixed(1) + ' ' })
  const area = `M1 ${h - 1} ${d.replace('M', 'L').trim()} L${w - 1} ${h - 1} Z`
  const last = values[values.length - 1]
  // Banded by value: a vertical gradient with hard stops at each threshold's height (thrPaint's twin).
  let paint = color, dot = color, bands: { from: number; to: number; color: string }[] = []
  if (thrOn(thr)) {
    const past = (v: number, t?: number) => t != null && (thr.below ? v <= t : v >= t)
    const colorAt = (v: number) => (past(v, thr.high) ? 'var(--err)' : past(v, thr.warn) ? 'var(--warn)' : 'var(--accent)')
    const ts = [...new Set([thr.warn, thr.high].filter((x): x is number => x != null))].sort((a, b) => b - a)
    const reps = [ts[0] + 1, ...ts.slice(1).map((t, i) => (ts[i] + t) / 2), ts[ts.length - 1] - 1]
    let prev = 0
    bands = reps.map((rv, k) => {
      const end = k < ts.length ? Math.min(1, Math.max(prev, py(ts[k]) / h)) : 1
      const b = { from: prev, to: end, color: colorAt(rv) }
      prev = end
      return b
    })
    paint = `url(#${gid})`
    dot = colorAt(last)
  }
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" style={{ display: 'block', width: fill ? '100%' : undefined }}>
      {bands.length > 0 && (
        <defs>
          <linearGradient id={gid} gradientUnits="userSpaceOnUse" x1={0} y1={0} x2={0} y2={h}>
            {bands.flatMap((b, k) => [
              <stop key={k + 'a'} offset={b.from} style={{ stopColor: b.color }} />,
              <stop key={k + 'b'} offset={b.to} style={{ stopColor: b.color }} />,
            ])}
          </linearGradient>
        </defs>
      )}
      <path d={area} fill={paint} opacity={0.13} />
      <path d={d.trim()} fill="none" stroke={paint} strokeWidth={1.5} />
      <circle cx={w - 1} cy={py(last)} r={1.8} fill={dot} />
    </svg>
  )
}

// PairSpark draws two readings of one thing on one scale, the speed test's download and upload, one
// step per run: a run that didn't measure one of them leaves a gap in its line. Two lines, so no fill.
function PairSpark({ a, b, colorA, colorB, width = 168 }: { a: (number | null)[]; b: (number | null)[]; colorA: string; colorB: string; width?: number }) {
  const n = Math.max(a.length, b.length)
  const all = [...a, ...b].filter((v): v is number => v != null)
  if (n < 2 || all.length < 2) return <span style={{ color: 'var(--faint)', fontSize: 12 }}>-</span>
  const w = width, h = 20
  const min = Math.min(...all), max = Math.max(...all)
  // The same floor and half-pixel geometry as Spark, so a steady line reads flat and crisp.
  const mid = (min + max) / 2
  const rng = Math.max(max - min, Math.max(Math.abs(min), Math.abs(max), 1e-9) * 0.1)
  const px = (i: number) => (i / (n - 1)) * (w - 2) + 1
  const py = (v: number) => h - 2.5 - (((v - mid) / rng) + 0.5) * (h - 4)
  const line = (vals: (number | null)[], color: string, sw: number) => {
    let d = ''
    let on = false
    let last: [number, number] | null = null
    for (let i = 0; i < vals.length; i++) {
      const v = vals[i]
      if (v == null) { on = false; continue }
      d += (on ? 'L' : 'M') + px(i).toFixed(1) + ' ' + py(v).toFixed(1) + ' '
      on = true
      last = [px(i), py(v)]
    }
    return (
      <>
        {d && <path d={d.trim()} fill="none" stroke={color} strokeWidth={sw} />}
        {last && <circle cx={last[0]} cy={last[1]} r={1.8} fill={color} />}
      </>
    )
  }
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" style={{ display: 'block' }}>
      {line(b, colorB, 1.2)}
      {line(a, colorA, 1.5)}
    </svg>
  )
}

// useSpeedPair is the speed test's download and upload over the last week, one entry per run (null
// where a run didn't measure it), every run of the week up to 168 (hourly): the Speed row's two-line
// sparkline, one step per run, so no run's dip is skipped. lastRun (the
// newest reading's time) refetches it when a run lands.
function useSpeedPair(hostId: string, on: boolean, lastRun: number): { down: (number | null)[]; up: (number | null)[] } | null {
  const [pair, setPair] = useState<{ down: (number | null)[]; up: (number | null)[] } | null>(null)
  useEffect(() => {
    if (!on) { setPair(null); return }
    let cancelled = false
    fetch(`/api/hosts/${hostId}/speedtest/runs?range=7d`)
      .then((r) => (r.ok ? r.json() : null))
      .then((d: SpeedRuns | null) => {
        if (cancelled || !d) return
        const runs = [...d.runs].reverse() // oldest first
        const n = runs.length
        const pick = n > 168 ? Array.from({ length: 168 }, (_, i) => runs[Math.round((i * (n - 1)) / 167)]) : runs
        setPair({ down: pick.map((r) => r.down ?? null), up: pick.map((r) => r.up ?? null) })
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [hostId, on, lastRun])
  return pair
}

// BarSpark draws the counter-total mini-graph: one tiny bar per day (from /api/daily), a miniature
// of the big daily bar chart - full bar = the day's total, red share = blocked, last bar = today so
// far. A rolling total's raw sparkline is a meaningless drifting line; this shows the daily rhythm.
function BarSpark({ total, blocked, width = 168 }: { total?: number[]; blocked?: number[]; width?: number }) {
  if (!total || total.length === 0) return <span style={{ color: 'var(--faint)', fontSize: 12 }}>-</span>
  const w = width, h = 20, gap = 2
  const n = total.length
  const bw = Math.max(2, (w - (n - 1) * gap) / n)
  const max = Math.max(...total, 1e-9)
  const bars = total.map((tv, i) => {
    const x = i * (bw + gap)
    const th = Math.max(tv > 0 ? 1 : 0, (tv / max) * (h - 2))
    const bv = Math.min(blocked?.[i] ?? 0, tv)
    const bh = Math.max(bv > 0 ? 1 : 0, (bv / max) * (h - 2))
    return (
      <g key={i}>
        <rect x={x} y={h - 1 - th} width={bw} height={th} fill="#2ea8c9" opacity={0.6} />
        {bh > 0 && <rect x={x} y={h - 1 - bh} width={bw} height={bh} fill="#d64550" opacity={0.9} />}
      </g>
    )
  })
  return (
    <svg width={w} height={h} viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" style={{ display: 'block' }}>{bars}</svg>
  )
}

// useDailies fetches per-day growth buckets for counter-total items (/api/daily) - the row headline
// ("N queries · M blocked today") and the BarSpark mini bars. Bucketing runs in the viewer's zone
// (the browser's UTC offset rides along), matching the big chart's local-midnight buckets.
function useDailies(itemIds: string[]): Record<string, number[]> {
  const [map, setMap] = useState<Record<string, number[]>>({})
  const [tick, setTick] = useState(0)
  const key = itemIds.slice().sort().join(',')
  // Same 60s cadence as the open chart, so the row's "today" and the today bar can't drift apart.
  useEffect(() => { const t = setInterval(() => setTick((x) => x + 1), 60000); return () => clearInterval(t) }, [])
  useEffect(() => {
    if (!key) { setMap({}); return }
    let cancelled = false
    fetch(`/api/daily?items=${encodeURIComponent(key)}&days=7&off=${new Date().getTimezoneOffset()}`)
      .then((r) => (r.ok ? r.json() : {}))
      .then((m) => { if (!cancelled) setMap(m || {}) })
      .catch(() => {})
    return () => { cancelled = true }
  }, [key, tick])
  return map
}

// sumSparks adds two spark series element-wise for network-style groups (In + Out = total
// throughput). The two series are downsampled independently, so align them from the tail - the
// newest points are what a sparkline is for. Falls back to whichever side exists.
function sumSparks(a?: number[], b?: number[]): number[] | undefined {
  if (!a || a.length < 2) return b
  if (!b || b.length < 2) return a
  const n = Math.min(a.length, b.length)
  const out: number[] = []
  for (let i = 0; i < n; i++) out.push(a[a.length - n + i] + b[b.length - n + i])
  return out
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
  // Set when a signed-in session ends under the open app, so the login screen says why.
  const [expired, setExpired] = useState(false)
  const meRef = useRef<Me | null>(null)
  meRef.current = me

  // A session that ends while the app is open (max lifetime, idle timeout, sign-out elsewhere) makes
  // every API call return the auth middleware's 401 {"error":"unauthorized"}. The views treat that
  // like any failed load, so the shell used to stay up showing "unauthorized" / "Failed to load"
  // until a manual reload. One wrapper around fetch sends such a response back to the login screen
  // instead. Other 401s (a wrong current password, a failed sign-in step) carry their own error and
  // pass through untouched. The URL is kept, so signing in again lands on the same view.
  useEffect(() => {
    const orig = window.fetch
    window.fetch = async (input, init) => {
      const res = await orig(input, init)
      if (res.status === 401 && meRef.current) {
        const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
        const body = await res.clone().json().catch(() => null)
        if (new URL(url, window.location.href).pathname.startsWith('/api/') && body?.error === 'unauthorized') {
          meRef.current = null
          setExpired(true)
          setJustLoggedIn(false)
          setMe(null)
        }
      }
      return res
    }
    return () => { window.fetch = orig }
  }, [])

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
  if (!me) return <Login onSuccess={(m) => { setExpired(false); setJustLoggedIn(true); setMe(m) }} passkeysAvailable={passkeysAvailable} passwordReset={passwordReset} notice={expired ? 'Your session has ended. Sign in again to continue.' : null} />
  return <AppShell me={me} onMe={setMe} onLogout={() => { setExpired(false); setJustLoggedIn(false); setMe(null) }} passkeysAvailable={passkeysAvailable} probeEnroll={probeEnroll} enter={justLoggedIn} />
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

function Login({ onSuccess, passkeysAvailable, passwordReset, notice }: { onSuccess: (m: Me) => void; passkeysAvailable: boolean; passwordReset: boolean; notice?: string | null }) {
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
      // 403 = refused before the credentials were even checked (Allowed FQDNs and IPs): show why, so a
      // locked-out admin doesn't keep retrying their password.
      if (res.status === 403) { setError(await errText(res, 'This address is not allowed')); return }
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
        {!error && <Banner variant="info">{notice}</Banner>}
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

type View = 'overview' | 'triggers' | 'history' | 'monitoring' | 'inventory' | 'maintenance' | 'notifications' | 'probes' | 'discovery' | 'thresholds' | 'statuspages' | 'changes' | 'users' | 'updates' | 'settings' | 'account' | 'list'
const VIEW_TITLES: Record<View, [string, string]> = {
  overview: ['Overview', 'What needs attention right now'],
  triggers: ['Triggers', 'Alert rules - firing, or all by host'],
  history: ['History', 'What went wrong, and when'],
  monitoring: ['Monitoring', 'Sites, hosts and sensors'],
  inventory: ['Inventory', 'Models, firmware, serials and addresses'],
  maintenance: ['Maintenance', 'When alerts wait for planned work'],
  notifications: ['Notifications', 'Alert routing and channels'],
  probes: ['Probes', 'Site probe enrollment'],
  discovery: ['Discovery', 'Scan a subnet, review what answers, adopt devices'],
  thresholds: ['Thresholds', 'Fleet-wide alert defaults per template'],
  statuspages: ['Status pages', 'Read-only dashboards for a wall screen'],
  changes: ['Changes', 'Who changed what, and when'],
  users: ['Users', 'Accounts and access'],
  updates: ['Updates', "Argus, the probes and the VMs' operating systems"],
  settings: ['Settings', 'System configuration'],
  account: ['Account', 'Your security settings'],
  list: ['Sensors', 'Filtered across all sites'],
}

const ic = {
  overview: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><g transform="translate(0 4)"><path d="M3 12a9 9 0 0 1 18 0" /><path d="M12 12l4-2" /><circle cx="12" cy="12" r="1.6" fill="currentColor" stroke="none" /></g></svg>,
  triggers: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><path d="M3 12h4l2-6 4 12 2-6h6" /></svg>,
  history: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><path d="M3.5 12a8.5 8.5 0 1 0 2.5-6" /><path d="M3 4.5V8.5h4" /><path d="M12 7.5V12l3 2" /></svg>,
  monitoring: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinejoin="round"><rect x="9" y="3" width="6" height="5" rx="1.5" /><rect x="3" y="16" width="6" height="5" rx="1.5" /><rect x="15" y="16" width="6" height="5" rx="1.5" /><path d="M12 8v4" /><path d="M6 16v-2a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v2" /></svg>,
  notifications: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9" /><path d="M13.7 21a2 2 0 0 1-3.4 0" /></svg>,
  maintenance: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z" /></svg>,
  probes: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="12" r="2" /><path d="M16.2 7.8a6 6 0 0 1 0 8.4M7.8 16.2a6 6 0 0 1 0-8.4M19 5a10 10 0 0 1 0 14M5 19A10 10 0 0 1 5 5" /></svg>,
  discovery: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="12" r="9" /><circle cx="12" cy="12" r="4.6" /><path d="M12 12l5.6-5.6" /><circle cx="15.4" cy="14.6" r="1.1" fill="currentColor" stroke="none" /></svg>,
  thresholds: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round"><path d="M5 21v-6M5 11V3M12 21v-9M12 8V3M19 21v-4M19 13V3" /><circle cx="5" cy="13" r="2" /><circle cx="12" cy="6" r="2" /><circle cx="19" cy="15" r="2" /></svg>,
  statuspages: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="4" width="18" height="12" rx="2" /><path d="M8 20h8M12 16v4" /><path d="M7 12l2.5-3 2.5 2 3-4 2 2" /></svg>,
  users: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="9" cy="8" r="3.2" /><path d="M3.5 20a5.5 5.5 0 0 1 11 0" /><path d="M16 5.2a3.2 3.2 0 0 1 0 6M17 14.5a5.5 5.5 0 0 1 3.5 5.5" /></svg>,
  updates: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><path d="M20.5 12a8.5 8.5 0 1 1-2.5-6" /><path d="M20.5 3.5v4.5H16" /><path d="M12 8v7.5M9 12.5l3 3 3-3" /></svg>,
  changes: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><path d="M5 7h10M5 12h14M5 17h7" /><path d="M16 17.5l2 2 3.5-4" /></svg>,
  inventory: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"><rect x="4" y="3.5" width="16" height="17" rx="2" /><path d="M8 8.5h8M8 12.5h8M8 16.5h5" /></svg>,
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
const NAV_VIEWS: View[] = ['overview', 'triggers', 'history', 'monitoring', 'inventory', 'maintenance', 'notifications', 'probes', 'discovery', 'thresholds', 'statuspages', 'changes', 'users', 'updates', 'settings', 'account', 'list']
type NavState = { view: View; filter: string; host?: string; item?: string; group?: string; scan?: string; edit?: string }

function parseNav(): NavState {
  const p = new URLSearchParams(window.location.search)
  const host = p.get('host') || undefined
  const item = p.get('item') || undefined
  const group = p.get('group') || undefined
  const raw = p.get('view')
  // Fall back to monitoring for a legacy ?host=&item= (or ?group=) link that predates ?view=.
  const view: View = raw && (NAV_VIEWS as string[]).includes(raw) ? (raw as View) : (host || group) ? 'monitoring' : 'overview'
  return { view, filter: p.get('filter') || 'error', host, item, group, scan: p.get('scan') || undefined, edit: p.get('edit') || undefined }
}

function buildNav(s: NavState): string {
  const p = new URLSearchParams()
  if (s.view !== 'overview') p.set('view', s.view)
  if (s.view === 'list') p.set('filter', s.filter)
  // Host/sensor and group focus are mutually exclusive; host is the more specific, so it wins.
  if (s.view === 'monitoring') {
    if (s.host) { p.set('host', s.host); if (s.item) p.set('item', s.item) }
    else if (s.group) p.set('group', s.group)
    if (s.edit) p.set('edit', s.edit) // the host whose settings dialog is open
  }
  // An opened discovery scan is its own screen: deep-linkable, and Back returns to the scan list.
  if (s.view === 'discovery' && s.scan) p.set('scan', s.scan)
  const qs = p.toString()
  return window.location.pathname + (qs ? '?' + qs : '')
}

type VersionInfo = { version: string; latest?: string; update_available: boolean; dev_update?: boolean; dev_target?: string; status: string; checked_at?: number; check_error?: string; channel?: string; templates_error?: string; templates_error_at?: number }
type UpdateState = {
  self_update_enabled: boolean
  state: string // idle | requested | running | success | failed
  target?: string
  from?: string
  message?: string
  requested_by?: string
  updater_version?: string // the core's argus-updater sidecar version
  updater_latest?: string // the newest published argus-updater ("" until looked up)
  updater_status?: string // current | outdated | unknown, like the core's own verdict
  updater_pending?: boolean // a sidecar self-update is queued
  collectors?: CollectorsReport // the sidecar's word on this server's collectors
  requested_at?: string
  steps?: JobStep[] // every step of the core update so far
  sidecar?: SidecarJob // the sidecar's own update, while there is one to show
}
type JobStep = { at?: string; msg: string }
type SidecarJob = { id: string; state: 'queued' | 'running' | 'success' | 'failed' | 'unknown'; tag: string; from?: string; to?: string; message?: string; requested_by?: string; requested_at: string; finished_at?: string; steps: JobStep[] }

// fmtStepTime is a step's time of day, to the second, in the configured clock.
function fmtStepTime(at?: string): string {
  const t = at ? Date.parse(at) : NaN
  if (Number.isNaN(t)) return ''
  try { return new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: !CLOCK.h24, timeZone: CLOCK.tz }).format(new Date(t)) }
  catch { return new Date(t).toLocaleTimeString() }
}

// UpdateLog shows what an update is doing in the background, one line per step, from the moment it
// is asked for until it ends: it lives in the channel files, so a page reload doesn't hide it.
function UpdateLog({ title, state, steps, message, note, children }: { title: string; state: string; steps: JobStep[]; message?: string; note?: string; children?: ReactNode }) {
  const busy = state === 'queued' || state === 'running' || state === 'requested'
  const bad = state === 'failed' || state === 'unknown'
  const label: Record<string, string> = { queued: 'queued', requested: 'queued', running: 'in progress', success: 'done', failed: 'failed', unknown: 'no word back' }
  return (
    <div className={'upd-log ' + (bad ? 'bad' : busy ? 'busy' : 'done')} role="status" aria-live="polite">
      <div className="upd-log-head">
        {busy ? <span className="spinner" aria-hidden="true" /> : <span className="upd-log-mark" aria-hidden="true">{bad ? '!' : '\u2713'}</span>}
        <b>{title}</b>
        <span className="upd-log-state">{label[state] || state}</span>
      </div>
      <ol className="upd-log-steps">
        {steps.map((s, i) => <li key={i}><span className="mono when">{fmtStepTime(s.at)}</span><span>{s.msg}</span></li>)}
      </ol>
      {bad && message && <p className="upd-log-why">{message}</p>}
      {note && <p className="upd-log-note">{note}</p>}
      {children && <div className="upd-log-act">{children}</div>}
    </div>
  )
}
// The argus-updater copies the running image's collectors into this server's Zabbix after each core
// update (the core's Zabbix is a host package, so they can't ride the image by themselves).
type CollectorsReport = { state: 'ok' | 'failed' | 'skipped'; message?: string; at?: string; version?: string; installed?: string[]; dir?: string }

// collectorsState says whether this server's Zabbix has the running version's collectors, and what to
// do when it hasn't: the sidecar installs them, so the only fix ever needed is updating it. The note is
// only for what needs reading; the rest is in the pill's tooltip.
function collectorsState(c: CollectorsReport | undefined, running?: string): { pill: ReactNode; note: string; warn: boolean } {
  const when = c?.at ? Date.parse(c.at) / 1000 : 0
  const ago = when ? relTime(when) : ''
  const what = "The scripts this server's Zabbix runs for the hosts it monitors itself (HTTP, TCP, SSH, UPS...)."
  if (!c) return { pill: <UpdPill kind="avail" title={what}>not installed by the sidecar</UpdPill>, warn: false,
    note: "This sidecar version doesn't install them yet: update the sidecar, and it copies this version's collectors in by itself." }
  if (c.state === 'ok') {
    if (c.version && running && vv(c.version) !== vv(running)) return { pill: <UpdPill kind="busy" title={`${what} This version's are going in now.`}>updating</UpdPill>, warn: false, note: '' }
    return { pill: <UpdPill kind="ok" title={`${what} Installed from this version${ago ? `, checked ${ago}` : ''}${c.installed && c.installed.length ? `; last copied in: ${c.installed.join(', ')}` : ''}.`}>up to date</UpdPill>, warn: false, note: '' }
  }
  if (c.state === 'skipped') return { pill: <UpdPill kind="info" title={`${what} ${c.message || 'Skipped.'}${ago ? ` (${ago})` : ''}`}>not needed</UpdPill>, warn: false, note: '' }
  return { pill: <UpdPill kind="bad" title={what}>not installed</UpdPill>, warn: true, note: `The sidecar couldn't install them: ${c.message || 'no reason given'}. It tries again every 10 minutes.` }
}

type OSWindow = { mode: string; weekday: number; hour: number; minute: number }
type OSStatus = {
  core: { available: boolean; sec_updates: number; reboot_required: boolean; reported_at: number; os?: string; zbx_server?: string; zbx_candidate?: string; tz?: string; clock_sync?: boolean }
  reboot_window: OSWindow
  zbx_window: OSWindow
  fleet_zbx?: string
}
const WEEKDAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

// osPill is a VM's patch state in the same words for the core and every probe VM: a reboot it needs
// first, then security updates still pending, else patched.
function osPill(sec: number, reboot: boolean, when: string): ReactNode {
  if (reboot) return <UpdPill kind="avail" title={`Updates are installed and need a restart to take effect. ${when}`}>reboot needed</UpdPill>
  if (sec > 0) return <UpdPill kind="avail" title={`Pending; they apply by themselves (security suite only). ${when}`}>{sec} security update{sec === 1 ? '' : 's'}</UpdPill>
  if (sec === 0) return <UpdPill kind="ok" title={`No pending security updates. ${when}`}>patched</UpdPill>
  return <UpdPill kind="none" title={`The security-update count is unknown. ${when}`}>count unknown</UpdPill>
}

// osName trims the "GNU/Linux" filler off a reported PRETTY_NAME: "Debian 13 (trixie)".
function osName(s?: string): string { return (s || '').replace('GNU/Linux ', '') }

// WeeklyWindow picks when something on the core happens by itself: never (notify only), or weekly at
// a day and time on the core VM's clock. Shared by the core's reboot and its Zabbix minor updates.
function WeeklyWindow({ autoLabel, mode, weekday, time, onMode, onWeekday, onTime }: {
  autoLabel: string; mode: string; weekday: number; time: string
  onMode: (m: string) => void; onWeekday: (d: number) => void; onTime: (t: string) => void
}) {
  return (
    <>
      <select className="input" value={mode} onChange={(e) => onMode(e.target.value)} style={{ width: 200 }} aria-label="When">
        <option value="notify">Notify only</option>
        <option value="auto">{autoLabel}</option>
      </select>
      {mode === 'auto' && <>
        <select className="input" value={weekday} onChange={(e) => onWeekday(parseInt(e.target.value, 10))} style={{ width: 130 }} aria-label="Day">
          {WEEKDAYS.map((d, i) => <option key={i} value={i}>{d}</option>)}
        </select>
        <input className="input" type="time" value={time} onChange={(e) => onTime(e.target.value)} style={{ width: 120 }} aria-label="Time" />
      </>}
    </>
  )
}

// windowText is a saved window as a policy row shows it.
function windowText(w?: OSWindow): string {
  if (!w) return '…'
  return w.mode === 'auto' ? `${WEEKDAYS[w.weekday]} ${fmtHM24(w.hour * 60 + w.minute)}` : 'notify only'
}

// OSUpdatesCard is the Updates page's Operating systems section (DESIGN section 14c). The Debian under
// the core VM and every probe VM patches itself locally (unattended-upgrades, security suite only);
// Argus never runs apt remotely. It shows where each VM stands and schedules the core's reboot and its
// Zabbix minor updates (a pet that must not bounce unannounced), which host timers honour locally.
function OSUpdatesCard({ proxies, vmLatest }: { proxies: Proxy[] | null; vmLatest: string }) {
  const toast = useToast()
  const [os, setOs] = useState<OSStatus | null>(null)
  const [mode, setMode] = useState('notify')
  const [weekday, setWeekday] = useState(0)
  const [time, setTime] = useState('03:00')
  // The Zabbix minor-update window (same shape, its own policy).
  const [zMode, setZMode] = useState('notify')
  const [zWeekday, setZWeekday] = useState(0)
  const [zTime, setZTime] = useState('04:00')
  const [busy, setBusy] = useState(false)
  const [editR, setEditR] = useState(false)
  const [editZ, setEditZ] = useState(false)

  const timeStr = (h: number, m: number) => `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`
  const reset = (d: OSStatus) => {
    setMode(d.reboot_window.mode); setWeekday(d.reboot_window.weekday); setTime(timeStr(d.reboot_window.hour, d.reboot_window.minute))
    if (d.zbx_window) { setZMode(d.zbx_window.mode); setZWeekday(d.zbx_window.weekday); setZTime(timeStr(d.zbx_window.hour, d.zbx_window.minute)) }
  }
  const load = () => fetch('/api/os/status').then((r) => (r.ok ? r.json() : null)).then((d: OSStatus | null) => { if (d) { setOs(d); reset(d) } }).catch(() => {})
  useEffect(() => { load() }, [])

  const dirty = !!os && (mode !== os.reboot_window.mode || (mode === 'auto' && (weekday !== os.reboot_window.weekday || time !== timeStr(os.reboot_window.hour, os.reboot_window.minute))))
  const zDirty = !!os && (zMode !== os.zbx_window.mode || (zMode === 'auto' && (zWeekday !== os.zbx_window.weekday || zTime !== timeStr(os.zbx_window.hour, os.zbx_window.minute))))
  const saveWindow = async (url: string, label: string, m: string, wd: number, t: string, done: () => void) => {
    const [h, mi] = t.split(':').map((n) => parseInt(n, 10))
    const body = m === 'auto' ? { mode: m, weekday: wd, hour: h || 0, minute: mi || 0 } : { mode: 'notify' }
    setBusy(true)
    try {
      const res = await fetch(url, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      if (!res.ok) { toast.error(await errText(res, `Could not save the ${label}`)); return }
      toast.success(`${label[0].toUpperCase()}${label.slice(1)} saved.`); await load(); done()
    } finally { setBusy(false) }
  }
  const cancel = (close: () => void) => { if (os) reset(os); close() }

  const c = os?.core
  const reported = c?.available && c.reported_at > 0 ? `Reported ${relTime(c.reported_at)}.` : ''
  const vms = (proxies || []).filter((p) => p.os_reported_at)
  const zPill = !c?.zbx_server ? <UpdPill kind="none">not reporting</UpdPill>
    : c.zbx_candidate && c.zbx_candidate !== c.zbx_server ? <UpdPill kind="avail" title="A minor update is available from the Zabbix repo: it applies in the Core Zabbix updates window, or by hand">{c.zbx_candidate} available</UpdPill>
    : <UpdPill kind="ok" title={os?.fleet_zbx && os.fleet_zbx !== c.zbx_server ? `The probes run ${os.fleet_zbx}.` : undefined}>up to date</UpdPill>
  return (
    <section className="set-card">
      <div className="upd-head"><h3>Operating systems</h3></div>
      <p className="set-note">The Debian under the core and probe VMs patches itself: security updates only, applied automatically. Argus never runs apt remotely, since there's no clean rollback. A probe VM reboots by itself in a weekly window around 03:00 when an update needs it; the core hosts the database and Zabbix, so its reboot and its Zabbix minor updates wait for the windows below. A container probe patches with its Docker host and isn't listed.</p>
      {!os ? <Skeleton rows={3} cols={2} /> : <>
        <UpdPolicy label="Core reboot" value={windowText(os.reboot_window)}
          title={os.reboot_window.mode === 'auto' ? "The core reboots only when an update needs it, in this window (the core VM's local time)" : 'Argus flags "reboot needed" and leaves the reboot to you'}
          onEdit={() => setEditR(true)}
          editor={editR ? <>
            <WeeklyWindow autoLabel="Auto-reboot weekly" mode={mode} weekday={weekday} time={time} onMode={setMode} onWeekday={setWeekday} onTime={setTime} />
            <Button variant="primary" onClick={() => saveWindow('/api/os/reboot-window', 'reboot window', mode, weekday, time, () => setEditR(false))} disabled={busy || !dirty}>{busy ? 'Saving…' : 'Save'}</Button>
            <Button variant="default" onClick={() => cancel(() => setEditR(false))} disabled={busy}>Cancel</Button>
          </> : undefined}
          below={editR ? <p className="set-hint">{mode === 'auto'
            ? <>The core reboots only when an update needs it, on <strong>{WEEKDAYS[weekday]}</strong> at <strong>{time}</strong> (the core VM's local time). Take a hypervisor snapshot as your safety net.</>
            : 'Notify only: Argus flags "reboot needed" and leaves the reboot to you.'}</p> : undefined} />

        {/* Core Zabbix minor updates (same major only). The Zabbix apt repo is pinned per major line
            and the host applier double-guards on the major.minor prefix: a major upgrade is always a
            planned manual event (database migration, TimescaleDB compatibility). */}
        <UpdPolicy label="Core Zabbix updates" value={windowText(os.zbx_window)}
          title={os.zbx_window.mode === 'auto' ? "Pending zabbix-* minors apply in this window (the core VM's local time), then zabbix-server restarts" : 'Argus shows when a minor is available and leaves applying it to you'}
          onEdit={() => setEditZ(true)}
          editor={editZ ? <>
            <WeeklyWindow autoLabel="Auto-update weekly" mode={zMode} weekday={zWeekday} time={zTime} onMode={setZMode} onWeekday={setZWeekday} onTime={setZTime} />
            <Button variant="primary" onClick={() => saveWindow('/api/os/zbx-window', 'update window', zMode, zWeekday, zTime, () => setEditZ(false))} disabled={busy || !zDirty}>{busy ? 'Saving…' : 'Save'}</Button>
            <Button variant="default" onClick={() => cancel(() => setEditZ(false))} disabled={busy}>Cancel</Button>
          </> : undefined}
          below={editZ ? <p className="set-hint">{zMode === 'auto'
            ? <>Pending zabbix-* minors apply on <strong>{WEEKDAYS[zWeekday]}</strong> at <strong>{zTime}</strong> (the core VM's local time), then zabbix-server restarts: a seconds-long blip the probes buffer through. Major upgrades are never automated: they migrate the database.</>
            : 'Notify only: Argus shows when a minor is available and leaves applying it to you. Major upgrades are never automated: they migrate the database.'}</p> : undefined} />

        <UpdGroup label="Core VM" sub="this server">
          <UpdPart label="OS">{c?.available && c.os && <UpdVer v={osName(c.os)} title="The operating system the core VM reports" />}{c?.available ? osPill(c.sec_updates, c.reboot_required, reported) : <UpdPill kind="none">not reporting</UpdPill>}</UpdPart>
          {!c?.available && <UpdBelow><p className="set-hint">The core's OS patch status isn't wired up yet. Run the host reporter from <span className="mono">deploy/core/setup-core.sh</span> and share the self-update dir with the core container.</p></UpdBelow>}
          <UpdPart label="Zabbix">{c?.zbx_server && <UpdVer v={c.zbx_server} title="The Zabbix server version on the core" />}{zPill}</UpdPart>
          {c?.available && !c.zbx_server && <UpdBelow><p className="set-hint">The host reporter predates Zabbix version reporting: re-run <span className="mono">deploy/core/setup-core-patching.sh</span> from the repo to enable it.</p></UpdBelow>}
        </UpdGroup>
      </>}

      {vms.map((p) => (
        <UpdGroup key={p.name} label={p.name} sub="probe VM">
          <UpdPart label="OS"><UpdVer v={osName(p.os_version)} title="The operating system this VM reports" />{osPill(typeof p.sec_updates === 'number' ? p.sec_updates : -1, !!p.reboot_required, `Reported ${relTime(p.os_reported_at!)}.`)}</UpdPart>
        </UpdGroup>
      ))}
      {proxies && vms.length === 0 && <UpdGroup label="Probe VMs"><UpdPart label=""><UpdPill kind="none">none reporting</UpdPill></UpdPart></UpdGroup>}

      <UpdGroup label="Probe VM image" sub="for new probes">
        <UpdPart label="Image">{vmLatest
          ? <><UpdVer v={vv(vmLatest)} title="The newest probe appliance release" /><UpdPill kind="info" title="What Add probe installs for a new probe VM. A VM already running doesn't need it: its probe updates through its sidecar, and its OS patches itself.">newest</UpdPill></>
          : <UpdPill kind="none">not looked up yet</UpdPill>}</UpdPart>
      </UpdGroup>
    </section>
  )
}

type Retention = {
  available: boolean; error?: string
  history_days: number; history_override: boolean
  trend_days: number; trend_override: boolean
  compression_available: boolean; compression: boolean; compress_after_days: number
  min_history_days: number; min_trend_days: number
}

// DataRetention edits how long Zabbix keeps sensor data (its global housekeeping settings), so an
// admin no longer has to open the Zabbix frontend for it. Raw history feeds the 2h/2d chart tabs and
// hourly trends 7d-1Y, which sets the floors the server enforces. Shortening a period deletes data at
// the next housekeeper run, so a save that shortens anything asks first.
function DataRetention() {
  const toast = useToast()
  const confirm = useConfirm()
  const [r, setR] = useState<Retention | null>(null)
  const [hist, setHist] = useState('')
  const [trend, setTrend] = useState('')
  const [comp, setComp] = useState(false)
  const [compAfter, setCompAfter] = useState('')
  const [busy, setBusy] = useState(false)

  const apply = (d: Retention) => {
    setR(d); setHist(String(d.history_days)); setTrend(String(d.trend_days))
    setComp(d.compression); setCompAfter(String(d.compress_after_days || 7))
  }
  useEffect(() => { fetch('/api/settings/retention').then((res) => (res.ok ? res.json() : null)).then((d: Retention | null) => { if (d) apply(d) }).catch(() => {}) }, [])

  const h = parseInt(hist, 10), t = parseInt(trend, 10), ca = parseInt(compAfter, 10)
  const compDirty = !!r?.compression_available && (comp !== r.compression || (comp && ca !== r.compress_after_days))
  const dirty = !!r?.available && (h !== r.history_days || t !== r.trend_days || compDirty || !r.history_override || !r.trend_override)
  const shorter = !!r && ((h < r.history_days) || (t < r.trend_days))

  async function save() {
    if (!r) return
    if (shorter && !(await confirm({
      title: 'Shorten data retention',
      message: 'Zabbix deletes data older than the new period at its next housekeeping run (within the hour). The deleted history and trends cannot be recovered.',
      confirmLabel: 'Shorten and delete', danger: true,
    }))) return
    setBusy(true)
    try {
      const res = await fetch('/api/settings/retention', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ history_days: h, trend_days: t, compression: r.compression_available && comp, compress_after_days: r.compression_available && comp ? ca : 0 }) })
      if (!res.ok) { toast.error(await errText(res, 'Could not save data retention')); return }
      apply(await res.json()); toast.success('Data retention saved.')
    } finally { setBusy(false) }
  }

  return (
    <form className="set-card" onSubmit={(e) => { e.preventDefault(); if (dirty && !busy) save() }}>
      <h3>Data retention</h3>
      <p className="set-note">How long Zabbix keeps sensor data. Raw history feeds the 2h and 2d chart tabs; hourly trends (min / avg / max) feed 7d through 1Y. Zabbix deletes older data by itself, and longer periods mean a bigger database.</p>
      {!r ? <Skeleton rows={2} cols={2} /> : !r.available ? <p className="set-hint">{r.error}</p> : (
        <>
          {!(r.history_override && r.trend_override) && (
            <p className="set-hint" style={{ marginTop: 0 }}>Each item currently keeps its own period (Zabbix's per-item setting). Saving applies the periods below to every item.</p>
          )}
          <label className="set-row">
            <div className="set-head"><span className="flabel">History (days)</span></div>
            <input className="input" type="number" min={r.min_history_days} value={hist} disabled={busy} onChange={(e) => setHist(e.target.value)} />
            <span className="set-hint">Every raw reading. At least {r.min_history_days} days, so the 2d tab stays full; the installer default is 30.</span>
          </label>
          <label className="set-row">
            <div className="set-head"><span className="flabel">Trends (days)</span></div>
            <input className="input" type="number" min={r.min_trend_days} value={trend} disabled={busy} onChange={(e) => setTrend(e.target.value)} />
            <span className="set-hint" style={t < 365 ? { color: 'var(--warn)' } : undefined}>
              {t < 365 ? 'Under a year, the 1Y tab won\'t reach back a full year.' : 'Hourly min / avg / max per sensor. The installer default is 730 (two years).'}
            </span>
          </label>
          <div className="set-row set-toggle">
            <div className="set-head"><span className="flabel">Compression</span></div>
            {r.compression_available ? (
              <>
                <Switch checked={comp} disabled={busy} onChange={setComp} label={comp ? 'On' : 'Off'} />
                <span className="set-hint">TimescaleDB compresses older data in place, which shrinks the database a lot. Compressed data stays readable.</span>
              </>
            ) : <span className="set-hint">Needs TimescaleDB, which this Zabbix database doesn't use. Retention still works without it.</span>}
          </div>
          {r.compression_available && comp && (
            <label className="set-row">
              <div className="set-head"><span className="flabel">Compress after (days)</span></div>
              <input className="input" type="number" min={7} value={compAfter} disabled={busy} onChange={(e) => setCompAfter(e.target.value)} />
              <span className="set-hint">At least 7 days (Zabbix's minimum). The installer default is 7.</span>
            </label>
          )}
          <div className="set-row set-actions">
            <button type="submit" className="btn primary" disabled={!dirty || busy}>{busy ? 'Saving…' : 'Save'}</button>
          </div>
        </>
      )}
    </form>
  )
}

type BackupRemote = { type: '' | 'smb' | 'nfs' | 'rsync' | 's3'; share?: string; username?: string; domain?: string; version?: string; export?: string; options?: string; target?: string; port?: number; endpoint?: string; region?: string; bucket?: string; access_key?: string; path?: string }
type BackupConfig = { enabled: boolean; hour: number; minute: number; keep: number; history: boolean; remote: BackupRemote }
type BackupStatus = {
  at: number; configured: boolean; running?: boolean; last_ok_at?: number; local_dir?: string; free_bytes?: number; next_due_at?: number
  last_run?: { at: number; ok: boolean; error?: string; archive?: string; size?: number; duration_s?: number; trigger?: string; warning?: string }
  local?: { name: string; size: number; at: number }[]
  remote?: { type: string; ok: boolean; at: number; error?: string; files?: number }
  test?: { at: number; ok: boolean; error?: string }
  busy?: 'test' | 'backup'
  activity?: { at: number; what: 'check' | 'backup'; why?: string; ok: boolean; text: string; took_s?: number }[]
}
type BackupView = { config: BackupConfig; has_passphrase: boolean; has_smb_password: boolean; has_s3_secret: boolean; ssh_public_key?: string; local_dir: string; status: BackupStatus | null; pending?: string; channel: boolean }
const REMOTE_LABEL: Record<string, string> = { '': 'None: keep them on the core VM only', smb: 'SMB share (Windows, NAS)', nfs: 'NFS export', rsync: 'rsync over SSH', s3: 'S3 bucket (or compatible)' }

// BackupsCard sets the core's backups up and shows how they went (DESIGN section 14e): the core VM
// archives Argus's database, the Zabbix database and its configuration, keys and certificates, keeps
// the newest on the VM and exports them encrypted.
// failedWhy joins a failure's reason to "failed": "failed while mounting ...: why" when the host says at
// which step, "failed: why" otherwise; the sentence gets its own full stop.
function failedWhy(err?: string): string {
  const e = (err || '').replace(/\.+$/, '')
  return e.startsWith('while ') ? ' ' + e : ': ' + e
}

function BackupsCard() {
  const toast = useToast()
  const [v, setV] = useState<BackupView | null>(null)
  const [cfg, setCfg] = useState<BackupConfig | null>(null)
  const [time, setTime] = useState('02:30')
  const [pass, setPass] = useState('')
  const [smbPass, setSmbPass] = useState('')
  const [s3Secret, setS3Secret] = useState('')
  const [busy, setBusy] = useState(false)
  const [dirty, setDirty] = useState(false)

  const apply = (d: BackupView, keepEdits = false) => {
    setV(d)
    if (!keepEdits) {
      setCfg(d.config); setTime(fmtHM24(d.config.hour * 60 + d.config.minute)); setPass(''); setSmbPass(''); setS3Secret(''); setDirty(false)
    }
  }
  const load = (keepEdits = false) => fetch('/api/backup').then((r) => (r.ok ? r.json() : null)).then((d: BackupView | null) => { if (d) apply(d, keepEdits) }).catch(() => {})
  useEffect(() => { load() }, []) // eslint-disable-line react-hooks/exhaustive-deps
  // Follow a run or a test while the host works on it, and for a while after asking, so a result that
  // lands between two looks is still shown.
  const [followUntil, setFollowUntil] = useState(0)
  const active = !!v && (!!v.pending || !!v.status?.running || !!v.status?.busy || Date.now() < followUntil)
  useEffect(() => {
    if (!active) return
    const t = setInterval(() => load(true), 4000)
    return () => clearInterval(t)
  }, [active]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!v || !cfg) return <section className="set-card"><h3>Backups</h3><Skeleton rows={3} cols={2} /></section>
  const set = (patch: Partial<BackupConfig>) => { setCfg({ ...cfg, ...patch }); setDirty(true) }
  const setR = (patch: Partial<BackupRemote>) => { setCfg({ ...cfg, remote: { ...cfg.remote, ...patch } }); setDirty(true) }
  const r = cfg.remote
  const st = v.status

  async function save(e?: FormEvent) {
    e?.preventDefault()
    if (!cfg) return
    const [hh, mm] = time.split(':').map(Number)
    setBusy(true)
    try {
      const res = await fetch('/api/backup', { method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ config: { ...cfg, hour: hh || 0, minute: mm || 0, keep: Number(cfg.keep), remote: { ...cfg.remote, port: Number(cfg.remote.port) || 0 } }, passphrase: pass, smb_password: smbPass, s3_secret: s3Secret }) })
      if (!res.ok) { toast.error(await errText(res, 'Could not save the backup settings')); return }
      apply(await res.json()); toast.success('Backup settings saved.')
    } catch { toast.error('Could not save the backup settings') } finally { setBusy(false) }
  }
  async function request(kind: 'backup' | 'test') {
    const res = await fetch('/api/backup/run', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ kind }) })
    if (!res.ok) { toast.error(await errText(res, 'Could not reach the host')); return }
    apply(await res.json(), true)
    setFollowUntil(Date.now() + 20000)
    toast.success(kind === 'backup' ? 'The core starts a backup in a few seconds.' : 'The core checks the target in a few seconds.')
  }
  async function newKey() {
    const res = await fetch('/api/backup/ssh-key', { method: 'POST' })
    if (!res.ok) { toast.error(await errText(res, 'Could not create a new key')); return }
    apply(await res.json(), true); toast.success('New key created: put it on the target in place of the old one.')
  }

  const lr = st?.last_run
  const field = (label: string, value: string | number | undefined, on: (v: string) => void, ph: string, hint?: string, type = 'text') => (
    <label className="set-row">
      <div className="set-head"><span className="flabel">{label}</span></div>
      <input className="input" type={type} value={value ?? ''} placeholder={ph} disabled={busy} onChange={(e) => on(e.target.value)} autoComplete="off" />
      {hint && <span className="set-hint">{hint}</span>}
    </label>
  )
  const secret = (label: string, has: boolean, value: string, on: (v: string) => void, hint: string) => (
    <label className="set-row">
      <div className="set-head"><span className="flabel">{label}</span></div>
      <input className="input" type="password" value={value} placeholder={has ? '•••••••• (unchanged)' : 'not set'} disabled={busy} autoComplete="new-password" onChange={(e) => { on(e.target.value); setDirty(true) }} />
      <span className="set-hint">{hint}</span>
    </label>
  )

  return (
    <form className="set-card" onSubmit={save}>
      <h3>Backups</h3>
      <p className="set-note">The core VM backs itself up: Argus&apos;s database, the Zabbix database{cfg.history ? ' with its metric history' : ''}, and the configuration, keys and certificates a new core needs to take over. Archives stay in <span className="mono">{v.local_dir}</span> on the VM; export them to keep a copy off it. Restoring is one command on a new core: see the guide, docs/backup-and-restore.md.</p>

      {!v.channel ? <p className="set-hint" style={{ color: 'var(--warn)' }}>This Argus has no shared folder with its host (ARGUS_UPDATE_DIR), so it can't reach the backup tool. The core appliance VM has one.</p>
        : !st ? <p className="set-hint" style={{ color: 'var(--warn)' }}>The core host hasn&apos;t reported yet. The core VM image has the backup tool built in; on an existing core, copy deploy/core/host to the VM and run <span className="mono">sudo ./install-backup.sh</span> once. Installed already? Then it reports to another folder than the one Argus reads: see &quot;The core host hasn&apos;t reported&quot; in docs/backup-and-restore.md.</p>
          : (
            <div className="set-row bstatus">
              <div className="set-head"><span className="flabel">Status</span>
                {st.running || st.busy === 'backup' || v.pending === 'backup' ? <span className="tag online">backing up…</span>
                  : !lr ? <span className="set-src">no backup yet</span>
                    : lr.ok ? <span className="tag online">ok</span> : <span className="tag avail">failed</span>}
              </div>
              <span className="set-hint">
                {lr ? <>{lr.ok ? `Last backup ${fmtWhen(lr.at)}, ${fmtNum(lr.size || 0, 'B')}, in ${fmtDuration(lr.duration_s || 0)}.` : `The last backup (${fmtWhen(lr.at)}) failed: ${lr.error}.`}{!lr.ok && st.last_ok_at ? ` Last good one ${fmtWhen(st.last_ok_at)}.` : ''}</> : 'No backup has run yet.'}
                {' '}{st.local && st.local.length > 0 ? `${st.local.length} on the VM` : ''}{st.free_bytes ? `, ${fmtNum(st.free_bytes, 'B')} free there.` : '.'}
                {cfg.enabled && st.next_due_at ? ` Next ${fmtWhen(st.next_due_at)}.` : ''}
              </span>
              {lr?.warning && <span className="set-hint" style={{ color: 'var(--warn)' }}>{lr.warning}.</span>}
              {st.remote && <span className="set-hint" style={st.remote.ok ? undefined : { color: 'var(--warn)' }}>{st.remote.ok ? `Exported ${fmtWhen(st.remote.at)}: ${st.remote.files || 0} on the target.` : `Export failed ${fmtWhen(st.remote.at)}${failedWhy(st.remote.error)}.`}{!st.remote.ok && st.test?.ok && st.test.at > st.remote.at ? ` The target works again (checked ${fmtWhen(st.test.at)}): the next backup exports.` : ''}</span>}
              {st.test && <span className="set-hint" style={st.test.ok ? undefined : { color: 'var(--warn)' }}>{st.test.ok ? `Target checked ${fmtWhen(st.test.at)}: it works.` : `Target check ${fmtWhen(st.test.at)} failed${failedWhy(st.test.error)}.`}</span>}
              {st.activity && st.activity.length > 0 && (
                <details className="bactivity">
                  <summary>Recent activity</summary>
                  <ul>
                    {st.activity.slice(0, 10).map((e, i) => (
                      <li key={i} className={e.ok ? undefined : 'bad'}>
                        <span className="mono">{fmtWhen(e.at)}</span> {e.what === 'check' ? 'Target check' : 'Backup'}{e.why ? ` (${e.why})` : ''}: {e.text}{e.what === 'check' && e.took_s ? ` (${e.took_s < 60 ? e.took_s + 's' : fmtDuration(e.took_s)})` : ''}.
                      </li>
                    ))}
                  </ul>
                </details>
              )}
              <span className="set-hint">
                <button type="button" className="btn" style={{ padding: '2px 10px' }} disabled={active || dirty} title={dirty ? 'Save first' : undefined} onClick={() => request('backup')}>Back up now</button>
                {r.type && <button type="button" className="btn" style={{ padding: '2px 10px', marginLeft: 6 }} disabled={active || dirty} title={dirty ? 'Save first' : undefined} onClick={() => request('test')}>{v.pending === 'test' || st.busy === 'test' ? 'Checking…' : 'Check the target'}</button>}
              </span>
            </div>
          )}

      <div className="set-row set-toggle">
        <div className="set-head"><span className="flabel">Daily backup</span></div>
        <Switch checked={cfg.enabled} disabled={busy} onChange={(on) => set({ enabled: on })} label={cfg.enabled ? 'On' : 'Off'} />
        <span className="set-hint">Once a day at the time below (Argus timezone). Turning it on runs the first one within 15 minutes.</span>
      </div>
      <div className="bgrid">
        <label className="set-row"><div className="set-head"><span className="flabel">At</span></div>
          <input className="input" type="time" value={time} disabled={busy} onChange={(e) => { setTime(e.target.value); setDirty(true) }} /></label>
        <label className="set-row"><div className="set-head"><span className="flabel">Keep</span></div>
          <input className="input" type="number" min={1} max={90} value={cfg.keep} disabled={busy} onChange={(e) => set({ keep: Number(e.target.value) })} />
          <span className="set-hint">The newest this many, on the VM and on the target.</span></label>
      </div>
      <div className="set-row set-toggle">
        <div className="set-head"><span className="flabel">Metric history</span></div>
        <Switch checked={cfg.history} disabled={busy} onChange={(on) => set({ history: on })} label={cfg.history ? 'Included' : 'Settings only'} />
        <span className="set-hint">{cfg.history ? 'Everything, charts included: the biggest part of an archive.' : 'Only the Zabbix settings (hosts, templates, triggers, users): small, but charts start empty after a restore.'}</span>
      </div>
      {secret('Encryption passphrase', v.has_passphrase, pass, setPass, 'Every archive is encrypted with it (gpg, AES-256), and exporting needs one. Keep it in your password manager: without it no archive can be opened, and Argus never shows it again. At least 12 characters.')}

      <label className="set-row">
        <div className="set-head"><span className="flabel">Export to</span></div>
        <Select value={r.type} disabled={busy} onChange={(e) => setR({ type: e.target.value as BackupRemote['type'] })}>
          {Object.entries(REMOTE_LABEL).map(([k, l]) => <option key={k} value={k}>{l}</option>)}
        </Select>
        {r.type && <span className="set-hint">Each run copies the new archive there and keeps this core&apos;s newest {cfg.keep}; other files are left alone.</span>}
      </label>
      {r.type === 'smb' && <>
        {field('Share', r.share, (x) => setR({ share: x }), '//nas.example.lan/backups')}
        {field('Folder', r.path, (x) => setR({ path: x }), 'argus', 'Inside the share; created when missing.')}
        <div className="bgrid">
          {field('User', r.username, (x) => setR({ username: x }), 'backup (empty = guest)')}
          {field('Domain', r.domain, (x) => setR({ domain: x }), 'optional')}
        </div>
        {secret('Password', v.has_smb_password, smbPass, setSmbPass, 'Handed to the mount through a file only root can read, never on a command line.')}
        <label className="set-row"><div className="set-head"><span className="flabel">SMB version</span></div>
          <Select value={r.version || ''} disabled={busy} onChange={(e) => setR({ version: e.target.value })}>
            {['', '3.1.1', '3.0', '2.1', '2.0', '1.0'].map((x) => <option key={x} value={x}>{x || 'Negotiate'}</option>)}
          </Select></label>
      </>}
      {r.type === 'nfs' && <>
        {field('Export', r.export, (x) => setR({ export: x }), 'nas.example.lan:/volume1/backups')}
        {field('Folder', r.path, (x) => setR({ path: x }), 'argus', 'Inside the export; created when missing.')}
        {field('Mount options', r.options, (x) => setR({ options: x }), 'soft,timeo=150,retrans=3', 'Empty uses these. The core must be allowed to write there (the export\'s client list, root squashing).')}
      </>}
      {r.type === 'rsync' && <>
        <div className="bgrid">
          {field('Target', r.target, (x) => setR({ target: x }), 'backup@nas.example.lan:/volume1/argus')}
          {field('SSH port', r.port || '', (x) => setR({ port: Number(x) || 0 }), '22', undefined, 'number')}
        </div>
        <div className="set-row">
          <div className="set-head"><span className="flabel">Key for the target</span></div>
          {v.ssh_public_key ? <>
            <textarea className="input mono bkey" readOnly value={v.ssh_public_key} rows={2} />
            <span className="set-hint">Add this line to <span className="mono">~/.ssh/authorized_keys</span> of that user on the target. <CopyButton text={v.ssh_public_key} label="Copy" /> <button type="button" className="btn" style={{ padding: '2px 10px' }} onClick={newKey}>New key</button></span>
          </> : <span className="set-hint">Save once and Argus creates a key pair; its public half shows here.</span>}
        </div>
      </>}
      {r.type === 's3' && <>
        {field('Endpoint', r.endpoint, (x) => setR({ endpoint: x }), 'https://s3.eu-central-1.amazonaws.com', 'Any S3-compatible service: AWS, Backblaze B2, Wasabi, Cloudflare R2, MinIO.')}
        <div className="bgrid">
          {field('Bucket', r.bucket, (x) => setR({ bucket: x }), 'argus-backups', 'It must exist already.')}
          {field('Region', r.region, (x) => setR({ region: x }), 'eu-central-1')}
        </div>
        {field('Folder', r.path, (x) => setR({ path: x }), 'core', 'The key prefix inside the bucket.')}
        {field('Access key', r.access_key, (x) => setR({ access_key: x }), 'AKIA…')}
        {secret('Secret key', v.has_s3_secret, s3Secret, setS3Secret, 'A key limited to this bucket (list, read, write, delete) is all it needs.')}
      </>}
      <div className="set-row set-actions">
        <button type="submit" className="btn primary" disabled={!dirty || busy}>{busy ? 'Saving…' : 'Save'}</button>
      </div>
    </form>
  )
}

// --- Updates ---------------------------------------------------------------------------------------
// The Updates page (admin) gathers every update in one place: Argus and its updater sidecar, the
// probes and theirs, and the operating systems under the VMs. Every component reads the same way, in
// a row or in a table cell: its version, one status pill (UpdPill) and its Update button. An update in
// hand is a pill too, with its step log underneath where the updater reports steps.

type PillKind = 'ok' | 'avail' | 'busy' | 'bad' | 'info' | 'none'

// UpdPill is a component's status word: a green tick when it's up to date, amber when there's
// something to do, a spinner while an update is in hand, red when one failed, neutral otherwise. The
// Probes page's version cells use it too, so both pages say it the same way.
function UpdPill({ kind, title, children }: { kind: PillKind; title?: string; children: ReactNode }) {
  if (kind === 'ok') return <span className="okquiet" title={title}>{children}</span>
  if (kind === 'none') return <span className="upd-none" title={title}>{children}</span>
  const cls = kind === 'avail' ? 'tag avail' : kind === 'busy' ? 'tag pending jobtag' : kind === 'bad' ? 'tag err' : 'tag'
  return <span className={cls} title={title}>{kind === 'busy' && <span className="spinner sm" aria-hidden="true" />}{children}</span>
}

// vv shows a semver version (the core's, an updater's) with its "v", however it was reported.
function vv(s?: string): string { return s ? 'v' + s.replace(/^v/, '') : '' }

// UpdVer is a component's running version, at the same weight everywhere.
function UpdVer({ v, title }: { v?: string; title?: string }) {
  return <span className="mono upd-ver" title={title} style={v ? undefined : { color: 'var(--faint)' }}>{v || '-'}</span>
}

// The Updates page lays every section out the same way, in rows. A group is one machine or setting:
// its label (and a sub-line) on the left, then one line per part (the part's name, its version, pill
// and action), and under a part what it needs to say: its update log, a hint, a disclosure.
function UpdGroup({ label, sub, subWarn, children }: { label: string; sub?: string; subWarn?: boolean; children: ReactNode }) {
  return (
    <div className="upd-group">
      <div className="upd-unit">
        <span className="complabel">{label}</span>
        {sub && <span className="sub-line" style={subWarn ? { color: 'var(--warn)' } : undefined}>{sub}</span>}
      </div>
      <div className="upd-parts">{children}</div>
    </div>
  )
}

// UpdPart is one line of a group: the part's name, then its version, pill and action.
function UpdPart({ label, children }: { label: string; children: ReactNode }) {
  return <><span className="upd-sub">{label}</span><span className="vcell">{children}</span></>
}

// UpdBelow sits under a part, aligned with its version.
function UpdBelow({ children }: { children: ReactNode }) {
  return <div className="upd-below">{children}</div>
}

// UpdPolicy is a setting of a section (the core's channel, the fleet target, a maintenance window): a
// group of its own whose value reads like a version, changed in place.
function UpdPolicy({ label, value, title, onEdit, editor, below }: { label: string; value: string; title?: string; onEdit?: () => void; editor?: ReactNode; below?: ReactNode }) {
  return (
    <UpdGroup label={label}>
      <UpdPart label="">{editor || <><UpdVer v={value} title={title} />{onEdit && <button className="btn" onClick={onEdit}>Change</button>}</>}</UpdPart>
      {below && <UpdBelow>{below}</UpdBelow>}
    </UpdGroup>
  )
}

// logTitle heads an update's step log the same way for every component.
function logTitle(state: string, to?: string): string {
  const t = to ? ` to ${to}` : ''
  if (state === 'success') return `Updated${t}`
  if (state === 'failed') return `Update${t} failed`
  if (state === 'unknown') return `Update${t}: no word back`
  return `Updating${t}`
}

// jobInHand: a probe update queued or under way (one that didn't take is no longer in hand).
function jobInHand(j?: ProbeJob): boolean { return !!j && j.state !== 'failed' }

// jobPill is a probe update in hand, on the Updates and the Probes page alike: queued for the
// sidecar's next check-in, updating until the new version reports in, or failed (it didn't take).
function jobPill(job: ProbeJob, what: string): ReactNode {
  const to = job.tag === 'latest' ? 'the latest version' : job.tag
  if (job.state === 'queued') return <UpdPill kind="busy" title={`The update of the ${what} to ${to} is queued: its sidecar picks it up at its next check-in, within a minute.`}>update queued</UpdPill>
  if (job.state === 'updating') return <UpdPill kind="busy" title={`Handed to the sidecar${job.at ? ` ${relTime(job.at)}` : ''}: it pulls ${to} and recreates the ${what}, rolling back if the new one doesn't start healthy. This reads as up to date once the new version reports in.`}>updating{job.at ? ` · ${relTime(job.at)}` : ''}</UpdPill>
  return <UpdPill kind="bad" title={`The update to ${to} didn't take: 20 minutes after it was handed out, the ${what} still ran the old version (the updater rolls back one that doesn't start healthy; its log says why). Update again to retry.`}>update failed</UpdPill>
}

// probeTo is the version a probe updates to: the fleet target, or the newest published one when the
// target is latest.
function probeTo(p: Proxy): string { return (!p.target || p.target === 'latest' ? p.latest : p.target) || 'latest' }

// proxyState is where a probe's proxy stands against the fleet target, and how it can update: through
// its sidecar ('self'), by hand on a probe without one ('manual'), or not now ('').
function proxyState(p: Proxy): { pill: ReactNode; can: 'self' | 'manual' | '' } {
  const job = p.update_job
  if (job && jobInHand(job)) return { pill: jobPill(job, 'probe'), can: '' }
  if (!p.update_status || p.update_status === 'external') return { pill: <UpdPill kind="none" title="This probe doesn't report its version to Argus (it's updated outside Argus, like an unRAID app). An admin can turn reporting on from its row on the Probes page.">not reporting</UpdPill>, can: '' }
  const can = p.selfupdate ? 'self' : 'manual'
  if (job) return { pill: jobPill(job, 'probe'), can }
  if (p.update_status === 'outdated') return { pill: <UpdPill kind="avail">{probeTo(p)} available</UpdPill>, can }
  if (p.update_status === 'tracking') return { pill: <UpdPill kind="info" title="The fleet target is latest and the newest published version isn't known yet: Check for updates looks it up">tracking latest</UpdPill>, can: p.selfupdate ? 'self' : '' }
  return { pill: <UpdPill kind="ok" title="Running the fleet target version">up to date</UpdPill>, can: '' }
}

// updaterState is where a probe's argus-updater sidecar stands against the newest published one.
function updaterState(p: Proxy): { pill: ReactNode; can: boolean } {
  if (!p.selfupdate) return { pill: <UpdPill kind="none" title="No argus-updater sidecar manages this probe: it's updated outside Argus">no sidecar</UpdPill>, can: false }
  const job = p.updater_job
  if (job && jobInHand(job)) return { pill: jobPill(job, 'sidecar'), can: false }
  if (job) return { pill: jobPill(job, 'sidecar'), can: true }
  if (p.updater_status === 'outdated') return { pill: <UpdPill kind="avail">{p.updater_latest ? `${vv(p.updater_latest)} available` : 'update available'}</UpdPill>, can: true }
  if (p.updater_status === 'current') return { pill: <UpdPill kind="ok" title="Running the newest published argus-updater">up to date</UpdPill>, can: false }
  return { pill: <UpdPill kind="info" title="The newest published argus-updater isn't known yet: Check for updates looks it up">newest unknown</UpdPill>, can: true }
}

// CoreUpdates is the Argus core section: the core's channel, then the core, the argus-updater sidecar
// that installs its updates, and the collectors that sidecar copies into this server's Zabbix.
function CoreUpdates({ v, upd, onChanged }: { v: VersionInfo | null; upd: UpdateState | null; onChanged: () => void }) {
  if (!v || !upd) return <section className="set-card"><h3>Argus core</h3><Skeleton rows={3} cols={2} /></section>
  return <CoreRows v={v} upd={upd} onChanged={onChanged} />
}

function CoreRows({ v, upd, onChanged }: { v: VersionInfo; upd: UpdateState; onChanged: () => void }) {
  const confirm = useConfirm()
  const [notes, setNotes] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [targets, setTargets] = useState<{ channels: string[]; releases: string[] } | null>(null)
  const [switching, setSwitching] = useState(false)
  const [switchTo, setSwitchTo] = useState('')
  const [err, setErr] = useState('')
  useEffect(() => { fetch('/api/version/tags').then((r) => (r.ok ? r.json() : null)).then((d) => { if (d) setTargets(d) }).catch(() => {}) }, [])
  // A check that finds a newer release has other notes to show.
  useEffect(() => { setNotes(null) }, [v.latest])
  const loadNotes = () => {
    if (notes !== null) return
    fetch('/api/version/notes').then((r) => (r.ok ? r.json() : null)).then((d) => setNotes((d && d.notes) || '')).catch(() => setNotes(''))
  }

  const self = upd.self_update_enabled
  const active = upd.state === 'requested' || upd.state === 'running'
  const running = v.version ? vv(v.version) : 'local build'
  const coreTo = v.dev_update ? (v.dev_target || 'the newest testing build') : (v.latest || 'the newest release')
  const post = (url: string, body: unknown, fail: string) => {
    setErr('')
    return fetch(url, { method: 'POST', ...(body ? { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) } : {}) })
      .then(async (r) => { if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error || fail) })
      .then(onChanged)
      .catch((e) => setErr(String(e.message || e)))
  }
  const updateCore = async () => {
    if (!(await confirm({ title: 'Update Argus', message: `Update Argus from ${running} to ${coreTo}? The sidecar pulls the new image and recreates the core, which is away for a few seconds; it keeps the old version if the new one doesn't start healthy.`, confirmLabel: `Update to ${coreTo}` }))) return
    setBusy(true); await post('/api/update/start', null, 'could not start the update'); setBusy(false)
  }
  // Deliberately switch the core to a chosen channel or version (bypassing the in-place channel-preserve).
  const doSwitch = async () => {
    if (!switchTo) return
    const isVer = /^v?\d+\.\d+\.\d+$/.test(switchTo)
    const msg = isVer
      ? `Switch Argus to ${switchTo}? This pins the core to that exact version: it won't track a channel until you switch back to latest or testing. The core pulls the image and restarts briefly.`
      : `Switch Argus to the "${switchTo}" channel? The core pulls that image and restarts briefly.`
    if (!(await confirm({ title: 'Switch version', message: msg, confirmLabel: 'Switch' }))) return
    setBusy(true); await post('/api/update/start', { target: switchTo }, 'could not start the switch'); setBusy(false)
    setSwitching(false); setSwitchTo('')
  }
  const dismiss = () => fetch('/api/update/dismiss', { method: 'POST' }).then(onChanged).catch(() => {})
  // After an update the running page is still the OLD bundle: close the finished job, then reload for
  // the new frontend (closing first, so the success log doesn't come back on the fresh load).
  const reloadNow = () => fetch('/api/update/dismiss', { method: 'POST' }).catch(() => {}).finally(() => window.location.reload())

  let corePill: ReactNode = null
  if (upd.state === 'requested') corePill = <UpdPill kind="busy">update queued</UpdPill>
  else if (upd.state === 'running') corePill = <UpdPill kind="busy">updating</UpdPill>
  else if (upd.state === 'success') corePill = <UpdPill kind="ok">updated</UpdPill>
  else if (upd.state === 'failed') corePill = <UpdPill kind="bad">update failed</UpdPill>
  else if (v.update_available) corePill = <UpdPill kind="avail">{coreTo} available</UpdPill>
  else if (v.check_error) corePill = <UpdPill kind="info" title="The last check couldn't reach the registry: the verdict may be out of date">check failed</UpdPill>
  else if (v.status === 'development') corePill = <UpdPill kind="info" title="A build from main, ahead of the newest release">development build</UpdPill>
  else if (v.status === 'current') corePill = <UpdPill kind="ok">up to date</UpdPill>
  const coreAction = upd.state === 'success' ? <Button variant="success" onClick={reloadNow}>Reload to finish</Button>
    : v.update_available && self && !active ? <button className="btn" onClick={updateCore} disabled={busy}>Update</button> : null
  // The core update, step by step: who asked, then what the sidecar reported (a sidecar from before
  // 0.2.12 reports only its latest step).
  const coreSteps: JobStep[] = upd.state !== 'idle' ? [
    { at: upd.requested_at, msg: `queued by ${upd.requested_by || 'an admin'}: update to ${upd.target} (the sidecar looks for it every 10 seconds)` },
    ...(upd.steps && upd.steps.length ? upd.steps : upd.message && upd.state !== 'requested' ? [{ msg: upd.message }] : []),
  ] : []
  // What the update brings. The testing channel is digest-based and usually lands on an unreleased
  // build with no notes; right after a cut, though, its tip IS the release and the notes exist.
  const norm = (s?: string) => (s || '').replace(/^v/, '')
  const devIsRelease = !!(v.dev_update && v.dev_target && v.latest && norm(v.dev_target) === norm(v.latest))
  const whatsNew = (
    <details className="upd-more" onToggle={loadNotes}>
      <summary>What's new in {v.latest}</summary>
      {notes === null ? <p className="set-hint">Loading…</p> : notes === '' ? <p className="set-hint">Release notes unavailable.</p> : <pre className="release-notes">{notes}</pre>}
    </details>
  )
  const brings = !v.update_available || upd.state === 'success' ? null
    : !self ? <p className="set-hint">Self-update isn't set up here: pull the new image and redeploy, or add the <span className="mono">argus-updater</span> sidecar for one-click updates (see the README).</p>
    : !v.dev_update ? whatsNew
    : devIsRelease ? <><p className="set-hint">The <span className="mono">:testing</span> channel is at the <span className="mono">{v.dev_target}</span> release now; updating re-pulls the testing channel in place.</p>{whatsNew}</>
    : <p className="set-hint">A newer <span className="mono">:testing</span> build is out: changes on top of the build you run, not in a release yet. Updating re-pulls the testing channel in place.</p>

  const side = upd.sidecar
  const sideBusy = !!side && (side.state === 'queued' || side.state === 'running')
  const sideLatest = vv(upd.updater_latest)
  const updateSidecar = async () => {
    const to = sideLatest || 'the newest version'
    if (!(await confirm({ title: 'Update the sidecar', message: `Update the argus-updater sidecar from ${vv(upd.updater_version) || 'its version'} to ${to}? It recreates itself onto the new image and rolls back if the new one fails. The core keeps running.`, confirmLabel: `Update to ${to}` }))) return
    await post('/api/update/updater', null, 'could not queue the sidecar update')
  }
  const dismissSidecar = () => fetch('/api/update/updater/dismiss', { method: 'POST' }).then(onChanged).catch(() => {})
  const sidePill = !self ? <UpdPill kind="none" title="Without it the core updates by hand: pull the new image and redeploy">not set up</UpdPill>
    : side?.state === 'queued' ? <UpdPill kind="busy">update queued</UpdPill>
    : side?.state === 'running' ? <UpdPill kind="busy">updating</UpdPill>
    : side?.state === 'failed' ? <UpdPill kind="bad">update failed</UpdPill>
    : side?.state === 'unknown' ? <UpdPill kind="info" title="The sidecar hasn't said how its update went">no word back</UpdPill>
    : upd.updater_status === 'outdated' ? <UpdPill kind="avail">{sideLatest ? `${sideLatest} available` : 'update available'}</UpdPill>
    : upd.updater_status === 'current' ? <UpdPill kind="ok">up to date</UpdPill>
    : <UpdPill kind="info" title="The newest published argus-updater isn't known yet: Check for updates looks it up">newest unknown</UpdPill>
  const sideAction = self && !sideBusy && upd.updater_status === 'outdated' ? <button className="btn" onClick={updateSidecar}>Update</button> : null
  const coll = collectorsState(upd.collectors, v.version)

  const channel = v.channel || ''
  const channelText = channel === 'latest' || channel === 'testing' ? channel : channel ? `${vv(channel)} (pinned)` : 'unknown'
  const channelTitle = channel === 'latest' ? 'Stable releases' : channel === 'testing' ? 'Builds from main, ahead of the releases'
    : channel ? 'Pinned to this version: it stays there until you switch back to latest or testing' : undefined

  return (
    <section className="set-card">
      <div className="upd-head"><h3>Argus core</h3></div>
      <p className="set-note">The Argus app on this server, the argus-updater sidecar that installs its updates (it holds the Docker socket and rolls an update back if the new version doesn't start healthy), and the collectors it copies into this server's Zabbix. Argus looks for a newer release every night.</p>

      {self && (
        <UpdPolicy label="Channel" value={channelText} title={channelTitle}
          onEdit={targets && upd.state !== 'success' && !active ? () => setSwitching(true) : undefined}
          editor={switching && targets ? <>
            <select className="input" value={switchTo} onChange={(e) => setSwitchTo(e.target.value)} style={{ width: 240 }} aria-label="Switch to">
              <option value="">Select a target…</option>
              <optgroup label="Channels">
                <option value="latest">latest - stable releases</option>
                <option value="testing">testing - main, unreleased</option>
              </optgroup>
              {targets.releases.length > 0 && (
                <optgroup label="Recent releases">
                  {targets.releases.map((t) => <option key={t} value={t}>{t}</option>)}
                </optgroup>
              )}
            </select>
            <Button variant="primary" onClick={doSwitch} disabled={busy || active || !switchTo}>Switch</Button>
            <Button variant="default" onClick={() => { setSwitching(false); setSwitchTo('') }} disabled={busy}>Cancel</Button>
          </> : undefined}
          below={switching ? <p className="set-hint">Switches the running image to the chosen channel or version. A version pins the core: it stays there until you switch back to <span className="mono">latest</span> or <span className="mono">testing</span>.</p> : undefined} />
      )}

      <UpdGroup label="Argus" sub="this server">
        <UpdPart label="Argus"><UpdVer v={running} title="The Argus version this server runs" />{corePill}{coreAction}</UpdPart>
        {v.check_error && <UpdBelow><p className="set-hint" style={{ color: 'var(--warn)' }}>{v.check_error} to check for updates: {v.checked_at ? `showing the result from ${relTime(v.checked_at)}` : 'no check has worked yet'}. Try again in a moment.</p></UpdBelow>}
        {v.templates_error && <UpdBelow><p className="set-hint" style={{ color: 'var(--warn)' }}>Updating the device-class templates in Zabbix failed{v.templates_error_at ? ` ${relTime(v.templates_error_at)}` : ''}: {v.templates_error}. Until it goes through, the sensors this version adds don't exist yet; Argus tries again every 15 minutes.</p></UpdBelow>}
        {upd.state !== 'idle' && (
          <UpdBelow>
            <UpdateLog title={logTitle(upd.state, upd.target)} state={upd.state} steps={coreSteps}
              message={upd.state === 'failed' ? `${upd.message || 'no reason given'}. The previous version was kept.` : undefined}
              note={upd.state === 'running' ? 'Argus restarts briefly near the end; this page reconnects by itself.' : upd.state === 'success' ? 'Reload to load the new version.' : undefined}>
              {upd.state === 'failed' && <Button variant="ghost" onClick={dismiss}>Close</Button>}
            </UpdateLog>
          </UpdBelow>
        )}
        {brings && <UpdBelow>{brings}</UpdBelow>}
        {err && <UpdBelow><Banner variant="error">{err}</Banner></UpdBelow>}

        <UpdPart label="Sidecar">{self && <UpdVer v={vv(upd.updater_version)} title="The argus-updater container that installs the core's updates" />}{sidePill}{sideAction}</UpdPart>
        {!self && <UpdBelow><p className="set-hint">Without the argus-updater sidecar the core updates by hand: pull the new image and redeploy. With it, Argus updates in one click and keeps this server's collectors in step with each update (see the README).</p></UpdBelow>}
        {side && (
          <UpdBelow>
            <UpdateLog title={logTitle(side.state, side.to || (side.tag && side.tag !== 'latest' ? side.tag : ''))} state={side.state} steps={side.steps} message={side.message}>
              {!sideBusy && <Button variant="ghost" onClick={dismissSidecar}>Close</Button>}
            </UpdateLog>
          </UpdBelow>
        )}

        {self && <UpdPart label="Collectors">{upd.collectors?.version && <UpdVer v={vv(upd.collectors.version)} title="The Argus version they were installed from" />}{coll.pill}</UpdPart>}
        {self && coll.note && <UpdBelow><p className="set-hint" style={coll.warn ? { color: 'var(--warn)' } : undefined}>{coll.note}</p></UpdBelow>}
      </UpdGroup>
    </section>
  )
}

// FleetTarget is the version every probe converges on: 'latest' (rolling) or an exact pin like
// '7.0.29-r1'. The sidecars and the manual command both honour it.
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
    <UpdPolicy label="Fleet target" value={target ?? '…'}
      title={`What every probe runs: latest, or a pin like 7.0.29-r1.${latest ? ` The newest published is ${latest}.` : ''}`}
      onEdit={start}
      editor={editing ? <>
        <input className="input mono" style={{ width: 160 }} value={val} autoFocus placeholder="latest" aria-label="Fleet target version" onChange={(e) => setVal(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') save(); if (e.key === 'Escape') setEditing(false) }} />
        <Button variant="primary" disabled={busy} onClick={save}>{busy ? 'Saving…' : 'Save'}</Button>
        <Button variant="default" disabled={busy} onClick={() => setEditing(false)}>Cancel</Button>
      </> : undefined}
      below={error ? <p className="set-hint" style={{ color: 'var(--err)' }}>{error}</p>
        : editing ? <p className="set-hint">What every probe runs: <span className="mono">latest</span>, or a pin like <span className="mono">7.0.29-r1</span>.{latest ? <> The newest published is <span className="mono">{latest}</span>.</> : null}</p> : undefined} />
  )
}

// ProbeUpdates is the Probes section: the fleet target, then each probe's proxy and argus-updater
// sidecar with their Update buttons. A sidecar takes what is queued for it at its next check-in.
function ProbeUpdates({ proxies, error, target, onTarget, onChanged }: { proxies: Proxy[] | null; error: string; target: string | null; onTarget: (t: string) => void; onChanged: () => void }) {
  const confirm = useConfirm()
  const toast = useToast()
  const [openCmd, setOpenCmd] = useState<string | null>(null) // probe whose manual update command is open
  const list = proxies || []
  const behind = list.filter((p) => p.selfupdate && p.update_status === 'outdated' && !jobInHand(p.update_job))
  const behindUpd = list.filter((p) => p.selfupdate && p.updater_status === 'outdated' && !jobInHand(p.updater_job))
  const n = behind.length + behindUpd.length
  const queue = async (p: Proxy, what: 'probe' | 'updater'): Promise<string> => {
    const res = await fetch(`/api/probes/${encodeURIComponent(p.name)}/${what === 'probe' ? 'update' : 'updater-update'}`, { method: 'POST' }).catch(() => null)
    if (!res) return 'Argus could not be reached'
    return res.ok ? '' : errText(res, 'could not queue the update')
  }
  async function updateProbe(p: Proxy) {
    const to = probeTo(p)
    if (!(await confirm({ title: `Update ${p.name}`, message: `Update the probe ${p.name} from ${p.version || 'its version'} to ${to}? Its sidecar pulls it at its next check-in, within a minute, and rolls back if the new one doesn't start healthy. The proxy is away for a few seconds while it restarts.`, confirmLabel: `Update to ${to}` }))) return
    const e = await queue(p, 'probe')
    if (e) toast.error(e)
    onChanged()
  }
  async function updateUpdater(p: Proxy) {
    const to = vv(p.updater_latest) || 'the newest version'
    if (!(await confirm({ title: `Update the sidecar of ${p.name}`, message: `Update the argus-updater sidecar of ${p.name} from ${vv(p.updater_version) || 'its version'} to ${to}? It recreates itself at its next check-in and rolls back if the new one fails. The probe keeps running.`, confirmLabel: `Update to ${to}` }))) return
    const e = await queue(p, 'updater')
    if (e) toast.error(e)
    onChanged()
  }
  // Update all queues every update that is behind. A sidecar asked for both gets them one per
  // check-in, the proxy first (its own update replaces it, so it never runs alongside the other).
  async function updateAll() {
    const plural = (k: number, w: string) => `${k} ${w}${k === 1 ? '' : 's'}`
    const updTo = vv(behindUpd.find((p) => p.updater_latest)?.updater_latest)
    const what = [
      behind.length ? `${plural(behind.length, 'probe')} to ${probeTo(behind[0])}` : '',
      behindUpd.length ? `${plural(behindUpd.length, 'probe sidecar')}${updTo ? ` to ${updTo}` : ''}` : '',
    ].filter(Boolean).join(' and ')
    if (!(await confirm({ title: 'Update all probes', message: `Update ${what}? Each sidecar takes its updates at its next check-in, within a minute, one at a time with the proxy first, and rolls back one that doesn't start healthy.`, confirmLabel: `Update ${n}` }))) return
    const errs = (await Promise.all([...behind.map((p) => queue(p, 'probe')), ...behindUpd.map((p) => queue(p, 'updater'))])).filter(Boolean)
    if (errs.length) toast.error(`${errs.length} of ${n} couldn't be queued: ${errs[0]}`)
    else toast.success(`${plural(n, 'update')} queued.`)
    onChanged()
  }

  return (
    <section className="set-card">
      <div className="upd-head">
        <h3>Probes</h3>
        {n > 0 && <Button variant="primary" onClick={updateAll} title="Queue an update for every probe and sidecar that is behind">Update all ({n})</Button>}
      </div>
      <p className="set-note">The Zabbix proxy each site runs, and the argus-updater sidecar that updates it. A sidecar takes a queued update at its next check-in, within a minute, and rolls back one that doesn't start healthy. Argus looks for newer versions every 3 hours.</p>
      <FleetTarget target={target} latest={list.find((p) => p.latest)?.latest} onSaved={onTarget} />
      {error ? <UpdGroup label="Probes"><UpdPart label=""><span className="set-hint" style={{ color: 'var(--err)' }}>{error}</span></UpdPart></UpdGroup>
        : proxies === null ? <Skeleton rows={3} cols={3} />
        : list.length === 0 ? <UpdGroup label="Probes"><UpdPart label=""><UpdPill kind="none">none yet: a probe shows here once it enrolls and checks in</UpdPill></UpdPart></UpdGroup>
        : list.map((p) => {
          const ps = proxyState(p), us = updaterState(p)
          return (
            <UpdGroup key={p.name} label={p.name} sub={p.online ? 'online' : 'offline: updates once back'} subWarn={!p.online}>
              <UpdPart label="Proxy">
                <UpdVer v={p.version} title="The Zabbix proxy version this probe runs" />{ps.pill}
                {ps.can === 'self' && <button className="btn" onClick={() => updateProbe(p)}>Update</button>}
                {ps.can === 'manual' && <button className="btn" onClick={() => setOpenCmd((o) => (o === p.name ? null : p.name))} title="This probe has no sidecar: show how to update it by hand">{openCmd === p.name ? 'Hide' : 'Update…'}</button>}
              </UpdPart>
              {openCmd === p.name && <UpdBelow><ProbeUpdateCommand p={p} /></UpdBelow>}
              <UpdPart label="Sidecar">
                {p.selfupdate && <UpdVer v={vv(p.updater_version) || '?'} title="The argus-updater sidecar managing this probe" />}{us.pill}
                {us.can && <button className="btn" onClick={() => updateUpdater(p)}>Update</button>}
              </UpdPart>
            </UpdGroup>
          )
        })}
    </section>
  )
}

// updatesVerdict is what a Check for updates found, in one line: what can update (in green when
// nothing can), what is already updating, and which lookups couldn't reach the registry.
function updatesVerdict(v: VersionInfo | null, pv: { probe_latest?: string; updater_latest?: string; failed?: string[] } | null, st: UpdateState | null, list: Proxy[]): { text: string; ok: boolean } {
  const plural = (k: number, w: string) => `${k} ${w}${k === 1 ? '' : 's'}`
  const failed: string[] = []
  if (!v || v.check_error) failed.push('the Argus image')
  if (!pv) failed.push('the probes')
  else failed.push(...(pv.failed || []))
  const side = st?.sidecar
  const sideBusy = !!side && (side.state === 'queued' || side.state === 'running')
  const todo: string[] = []
  if (v?.update_available) todo.push(`Argus ${v.dev_update ? (v.dev_target || 'testing build') : v.latest}`)
  if (st?.self_update_enabled && st.updater_status === 'outdated' && !sideBusy) todo.push(`the core's sidecar ${vv(st.updater_latest)}`)
  const pb = list.filter((p) => p.update_status === 'outdated' && !jobInHand(p.update_job)).length
  const ub = list.filter((p) => p.selfupdate && p.updater_status === 'outdated' && !jobInHand(p.updater_job)).length
  if (pb) todo.push(`${plural(pb, 'probe')}${pv?.probe_latest ? ` (${pv.probe_latest})` : ''}`)
  if (ub) todo.push(`${plural(ub, 'probe sidecar')}${pv?.updater_latest ? ` (${vv(pv.updater_latest)})` : ''}`)
  const going = (st && (st.state === 'requested' || st.state === 'running') ? 1 : 0) + (sideBusy ? 1 : 0)
    + list.filter((p) => jobInHand(p.update_job)).length + list.filter((p) => jobInHand(p.updater_job)).length
  const goingText = going ? ` ${going === 1 ? 'One more is' : `${going} more are`} updating already.` : ''
  if (failed.length) return { ok: false, text: `Couldn't reach the registry to check ${failed.join(', ')}; try again in a moment.${todo.length ? ` Updates available: ${todo.join(', ')}.` : ''}` }
  if (todo.length) return { ok: false, text: `Updates available: ${todo.join(', ')}.${goingText}` }
  if (going) return { ok: false, text: `${going === 1 ? 'One update is' : `${going} updates are`} under way; everything else is on the newest version.` }
  return { ok: true, text: 'Everything is on the newest version.' }
}

// UpdatesView is the Updates page (admin): one Check for updates for everything, then the core, the
// probes and the operating systems, each in its own section.
function UpdatesView() {
  const [v, setV] = useState<VersionInfo | null>(null)
  const [upd, setUpd] = useState<UpdateState | null>(null)
  const [proxies, setProxies] = useState<Proxy[] | null>(null)
  const [proxyErr, setProxyErr] = useState('')
  const [target, setTarget] = useState<string | null>(null)
  const [vmLatest, setVmLatest] = useState('')
  const [checking, setChecking] = useState(false)
  const [checkMsg, setCheckMsg] = useState<{ text: string; ok: boolean } | null>(null)
  const getJSON = (url: string) => fetch(url).then((r) => (r.ok ? r.json() : null)).catch(() => null)
  // A failed fetch keeps the last state (the core restarts during its own update).
  const loadUpd = () => getJSON('/api/update/state').then((d) => { if (d) setUpd(d) })
  const loadProxies = () => fetch('/api/proxies')
    .then(async (r) => { if (!r.ok) throw new Error(await errText(r, 'Failed to load probes')); return r.json() })
    .then((p: Proxy[]) => { setProxies(p || []); setProxyErr('') })
    .catch((e) => setProxyErr(e instanceof Error ? e.message : 'Failed to load probes'))
  useEffect(() => {
    getJSON('/api/version').then((d) => { if (d) setV(d) })
    getJSON('/api/probes/target').then((d) => { if (d && d.target) setTarget(d.target) })
    getJSON('/api/probes/vm-images').then((d) => { if (d && d.version) setVmLatest(d.version) })
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  // The core's and its sidecar's jobs every 3 s while one runs, every 15 s otherwise, so one asked for
  // in another tab still shows; the probes every 30 s, every 5 s while one of theirs is in hand.
  const coreBusy = !!upd && (upd.state === 'requested' || upd.state === 'running' || upd.sidecar?.state === 'queued' || upd.sidecar?.state === 'running')
  useEffect(() => { loadUpd(); const t = setInterval(loadUpd, coreBusy ? 3000 : 15000); return () => clearInterval(t) }, [coreBusy]) // eslint-disable-line react-hooks/exhaustive-deps
  const probeBusy = (proxies || []).some((p) => jobInHand(p.update_job) || jobInHand(p.updater_job))
  useEffect(() => { loadProxies(); const t = setInterval(loadProxies, probeBusy ? 5000 : 30000); return () => clearInterval(t) }, [probeBusy]) // eslint-disable-line react-hooks/exhaustive-deps

  // Check for updates looks up everything at once (the core's release, the probe image, both
  // updaters, the probe VM image), then reloads what it decides and says it in one line.
  async function checkAll() {
    setChecking(true); setCheckMsg(null)
    try {
      const post = (url: string) => fetch(url, { method: 'POST' }).then((r) => (r.ok ? r.json() : null)).catch(() => null)
      const [cv, pv] = await Promise.all([post('/api/version/check'), post('/api/probes/check-updates')])
      const [st, list] = await Promise.all([getJSON('/api/update/state'), getJSON('/api/proxies')])
      if (cv) setV(cv)
      if (st) setUpd(st)
      if (list) { setProxies(list); setProxyErr('') }
      if (pv && pv.vm_latest) setVmLatest(pv.vm_latest)
      setCheckMsg(updatesVerdict(cv, pv, st || upd, list || proxies || []))
    } finally { setChecking(false) }
  }

  return (
    <div className="panel">
      <div className="phead">
        <PanelTitle eyebrow="Admin">Updates</PanelTitle>
        <span className="hint">Argus looks by itself too: nightly for the core, every 3 hours for the probes</span>
        <div className="tools"><button className="btn" onClick={checkAll} disabled={checking}>{checking ? 'Checking…' : 'Check for updates'}</button></div>
      </div>
      {checkMsg && <p className={'upd-check ' + (checkMsg.ok ? 'ok' : 'warn')} role="status">{checkMsg.text}</p>}
      <div className="set-body">
        <CoreUpdates v={v} upd={upd} onChanged={loadUpd} />
        <ProbeUpdates proxies={proxies} error={proxyErr} target={target} onTarget={setTarget} onChanged={loadProxies} />
        <OSUpdatesCard proxies={proxies} vmLatest={vmLatest} />
      </div>
    </div>
  )
}

// AboutCard names the running Argus and its licence; updates have their own page.
function AboutCard({ onOpenUpdates }: { onOpenUpdates: () => void }) {
  const [v, setV] = useState<VersionInfo | null>(null)
  useEffect(() => { fetch('/api/version').then((r) => (r.ok ? r.json() : null)).then(setV).catch(() => {}) }, [])
  const pill = !v ? null
    : v.update_available ? <button type="button" className="linklike" onClick={onOpenUpdates} title="Update it on the Updates page"><UpdPill kind="avail">{v.dev_update ? (v.dev_target || 'testing build') : v.latest} available</UpdPill></button>
    : v.status === 'current' ? <UpdPill kind="ok">up to date</UpdPill>
    : v.status === 'development' ? <UpdPill kind="info">development build</UpdPill> : null
  return (
    <section className="set-card">
      <h3>About</h3>
      <p className="set-note">Updates to Argus, its updater sidecar, the probes and the VMs' operating systems are on the <button type="button" className="linkbtn" style={{ color: 'var(--accent)' }} onClick={onOpenUpdates}>Updates</button> page.</p>
      <div className="set-row">
        <div className="set-head"><span className="flabel">Version</span>{pill}</div>
        <input className="input mono" disabled value={v ? (v.version ? vv(v.version) : 'local build') : '…'} aria-label="Argus version" />
        {/* AGPL-3.0 section 13: network users must be able to reach the corresponding source. */}
        <span className="set-hint">Argus is free software under the <a href="https://www.gnu.org/licenses/agpl-3.0.html" target="_blank" rel="noopener noreferrer">GNU AGPL-3.0</a> - <a href="https://github.com/g-guglielmi/argus-core" target="_blank" rel="noopener noreferrer">source code</a>.</span>
      </div>
    </section>
  )
}

function AppShell({ me, onMe, onLogout, passkeysAvailable, probeEnroll, enter }: { me: Me; onMe: (m: Me) => void; onLogout: () => void; passkeysAvailable: boolean; probeEnroll: boolean; enter?: boolean }) {
  SCOPE.sites = me.sites || []
  // Admin-only views can't be restored from a shared/stale URL by a non-admin.
  const clampView = (v: View): View => ((v === 'users' || v === 'updates' || v === 'settings' || v === 'discovery' || v === 'thresholds' || v === 'statuspages' || v === 'changes') && me.role !== 'admin' ? 'overview' : v)
  // A fresh visit to the bare "/" (no query) honours the user's landing preference; any deep
  // link (?view=…, ?host=…, ?reset=… already handled) is respected as-is.
  const initialNav = (): NavState => {
    if (!window.location.search && me.landing === 'errors') return { view: 'list', filter: 'error' }
    return parseNav()
  }
  const [view, setView] = useState<View>(() => clampView(initialNav().view))
  // The discovery scan being reviewed ("" = the scan list). Mirrored in the URL (?scan=) so a
  // reload restores it and Back steps out of the results to the list.
  const [discScan, setDiscScan] = useState<string | null>(() => { const s = initialNav(); return s.view === 'discovery' ? s.scan || null : null })
  const [collapsed, setCollapsed] = useState(() => { try { return localStorage.getItem('argus-collapsed') === '1' } catch { return false } })
  const [navOpen, setNavOpen] = useState(false) // mobile drawer
  const [menuOpen, setMenuOpen] = useState(false)
  const [theme, toggleTheme] = useTheme()
  // The census rows of the states the open view needs, and every state's count (the pills).
  const [sensors, setSensors] = useState<SensorRow[]>([])
  const [counts, setCounts] = useState<Record<string, number>>({})
  // False until the first census answer: the lists show a skeleton instead of flashing "All clear".
  const [sensorsLoaded, setSensorsLoaded] = useState(false)
  const [sensorsAt, setSensorsAt] = useState(0) // when the server read the status data (the header clock's tooltip)
  const [sensorsNext, setSensorsNext] = useState(0) // when the next fetch runs (the header counts down to it)
  const [listFilter, setListFilter] = useState<string>(() => initialNav().filter)
  const canPause = me.role === 'admin' || me.role === 'helpdesk'
  // Rows come for the Overview's states always, plus the open drill-down's own state: the OK list is
  // most of the census, so it is fetched only while it is on screen.
  const attentionStates = ['error', 'warning', 'acked']
  const rowStates = [...attentionStates, ...(view === 'list' && !attentionStates.includes(listFilter) ? [listFilter] : [])].join(',')
  const [rowsFor, setRowsFor] = useState('') // the states the loaded rows cover

  useEffect(() => {
    let live = true
    let timer: number | undefined
    // The next fetch is timed to just after the server's next build (next_ms), so every answer is a
    // fresh one and the header's "Updated ... ago" restarts from a few seconds each time.
    const again = (ms: number) => {
      const d = Math.min(60000, Math.max(3000, ms))
      window.clearTimeout(timer); timer = window.setTimeout(load, d); setSensorsNext(Date.now() + d)
    }
    function load() {
      fetch(`/api/census?rows=${rowStates}`).then((r) => (r.ok ? r.json() : Promise.reject()))
        .then((c: { counts?: Record<string, number>; rows?: SensorRow[]; age_ms?: number; next_ms?: number }) => {
          if (!live) return
          setSensors(c.rows || []); setCounts(c.counts || {}); setRowsFor(rowStates); setSensorsLoaded(true)
          setSensorsAt(Date.now() - Math.max(0, c.age_ms || 0))
          again(c.next_ms != null ? c.next_ms + 1500 : 30000)
        })
        .catch(() => { if (live) { setSensorsLoaded(true); again(30000) } })
    }
    load(); const off = onDataRefresh(load); return () => { live = false; window.clearTimeout(timer); off() }
  }, [rowStates])
  // Remember the desktop sidebar collapsed/expanded choice across reloads.
  useEffect(() => { try { localStorage.setItem('argus-collapsed', collapsed ? '1' : '0') } catch { /* ignore */ } }, [collapsed])
  // Until the first census answers (right after a restart the server may still be reading every
  // sensor from Zabbix), the status pills show the last visit's counts, dimmed, instead of a row of
  // zeros that reads as data.
  const [lastCounts] = useState<Record<string, number> | null>(() => { try { return JSON.parse(localStorage.getItem('argus-status-counts') || 'null') } catch { return null } })
  const cnt = (st: string) => (sensorsLoaded ? (counts[st] ?? 0) : (lastCounts?.[st] ?? 0))
  const errN = cnt('error'), warnN = cnt('warning'), ackN = cnt('acked'), pausedN = cnt('paused'), hiddenN = cnt('hidden'), okN = cnt('ok')
  const heldN = sensorsLoaded ? (counts.held ?? 0) : 0 // errors and warnings a down master holds, counted apart
  useEffect(() => {
    if (!sensorsLoaded) return
    try { localStorage.setItem('argus-status-counts', JSON.stringify({ ok: okN, warning: warnN, error: errN, acked: ackN, paused: pausedN, hidden: hiddenN })) } catch { /* ignore */ }
  }, [sensorsLoaded, okN, warnN, errN, ackN, pausedN, hiddenN])

  // Deep-link target: Overview / lists / a shared URL ask the tree to open a host (and optionally
  // a sensor's chart). Seeded from the URL so a reload restores the open host/sensor.
  const [treeTarget, setTreeTarget] = useState<{ hostId?: string; itemId?: string; itemName?: string; groupPath?: string; editHost?: string; n: number } | null>(() => {
    const s = parseNav()
    if (s.view !== 'monitoring') return null
    if (s.host) return { hostId: s.host, itemId: s.item, editHost: s.edit, n: 0 }
    if (s.group) return { groupPath: s.group, editHost: s.edit, n: 0 }
    if (s.edit) return { editHost: s.edit, n: 0 }
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
  function onTreeNav(hostId: string | null, itemId: string | null, group?: string | null, push?: boolean, edit?: string | null) {
    const url = buildNav({ view: 'monitoring', filter: listFilter, host: hostId || undefined, item: itemId || undefined, group: group || undefined, edit: edit || undefined })
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
      setDiscScan(n.view === 'discovery' ? n.scan || null : null)
      if (n.view === 'monitoring' && n.host) { navN.current += 1; setTreeTarget({ hostId: n.host, itemId: n.item, editHost: n.edit, n: navN.current }) }
      else if (n.view === 'monitoring' && n.group) { navN.current += 1; setTreeTarget({ groupPath: n.group, editHost: n.edit, n: navN.current }) }
      else if (n.view === 'monitoring' && n.edit) { navN.current += 1; setTreeTarget({ editHost: n.edit, n: navN.current }) }
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
  function goto(v: View) { setTreeTarget(null); setDiscScan(null); if (v === 'monitoring') setMonHome((n) => n + 1); setView(v); pushNav(v); setMenuOpen(false); setNavOpen(false) }
  // Open (or leave, with null) a discovery scan's results - its own history entry, so Back works.
  function openDiscoveryScan(id: number | null) {
    setDiscScan(id ? String(id) : null)
    window.history.pushState({}, '', buildNav({ view: 'discovery', filter: listFilter, scan: id ? String(id) : undefined }))
  }

  // Running version for the sidebar footer (updating it is on the Updates page). This poll is also
  // the stale-bundle guard: an already-loaded SPA never re-fetches its own index.html, so after the core
  // is updated the tab keeps running the OLD JS until a full reload. The version endpoint is cheap
  // (a compile-time build id + cached latest, no network), so we poll it and, when the running build id
  // changes from the one this tab first saw, offer a reload. buildinfo.Version is "" only for un-stamped
  // local dev builds (can't tell those apart), so the detector arms only once we've seen a concrete id.
  const [ver, setVer] = useState<VersionInfo | null>(null)
  const [updateReady, setUpdateReady] = useState(false)
  // A short window scrolls the sidebar's list: keep the current page's item in sight, again when the
  // phone drawer opens and once the version row (which shortens the list) arrives.
  const hasVer = !!ver
  useEffect(() => { document.querySelector('.side-nav .nav.active')?.scrollIntoView({ block: 'nearest' }) }, [view, navOpen, hasVer])
  const bootVer = useRef<string | null>(null)
  useEffect(() => {
    let stop = false
    const check = () => fetch('/api/version').then((r) => (r.ok ? r.json() : null)).then((v: VersionInfo | null) => {
      if (stop || !v) return
      setVer(v)
      if (bootVer.current === null) bootVer.current = v.version || ''
      else if (bootVer.current && v.version && v.version !== bootVer.current) setUpdateReady(true)
    }).catch(() => {})
    check(); const t = setInterval(check, 60000); return () => { stop = true; clearInterval(t) }
  }, [])

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
    <button className={'stat' + (view === 'list' && listFilter === st ? ' on' : '') + (sensorsLoaded ? '' : ' stale')} title={sensorsLoaded ? label : label + ' (loading…)'} onClick={() => openList(st)}>
      <span className="si" style={{ color }}>{icon}</span>{sensorsLoaded || lastCounts ? n : '…'}
    </button>
  )

  const [title, sub] = view === 'list' ? [`${STATE_LABEL[listFilter]} sensors`, 'Filtered across all sites']
    : view === 'discovery' && discScan ? ['Discovery · scan results', 'Review what the scan found, adopt or ignore it']
    : VIEW_TITLES[view]
  return (
    <div className={'app-shell' + (collapsed ? ' collapsed' : '') + (navOpen ? ' nav-open' : '') + (enter ? ' app-enter' : '')}>
      {navOpen && <div className="nav-backdrop" onClick={() => setNavOpen(false)} />}
      {searchOpen && <SearchPalette onClose={() => setSearchOpen(false)} onPick={goSearch} />}
      <aside className="sidebar">
        <div className="brand">
          <img className="brand-logo" src="/argus-logo.png" alt="" width={30} height={30} />
          <div><div className="word">ARGUS</div><div className="sub">Monitoring</div></div>
        </div>
        {/* The list scrolls between the logo and the account button when the window is too short for it. */}
        <nav className="side-nav">
        <div className="navlabel">Watch</div>
        {nav('overview', 'Overview', { count: errN })}
        {nav('triggers', 'Triggers')}
        {nav('monitoring', 'Monitoring')}
        {nav('inventory', 'Inventory')}
        {nav('history', 'History')}
        <div className="navlabel">Configure</div>
        {nav('probes', 'Probes')}
        {me.role === 'admin' && nav('discovery', 'Discovery')}
        {nav('maintenance', 'Maintenance')}
        {nav('notifications', 'Notifications')}
        {me.role === 'admin' && <><div className="navlabel">Admin</div>{nav('statuspages', 'Status pages')}{nav('thresholds', 'Thresholds')}{nav('changes', 'Changes')}{nav('users', 'Users')}{nav('updates', 'Updates')}{nav('settings', 'Settings')}</>}
        </nav>
        <div className="side-foot">
          {ver && (
            <button type="button" className={'side-ver' + (ver.update_available ? ' upd' : '')} disabled={me.role !== 'admin'}
              title={ver.update_available ? `Update available${ver.latest ? `: ${ver.latest}` : ''} - open Updates` : `Argus ${ver.version || 'development build'}`}
              onClick={() => goto('updates')}>
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
        {updateReady && (
          <div className="updbar" role="status">
            <span>A new version of Argus is available - reload to load the latest.</span>
            <Button variant="primary" onClick={() => window.location.reload()}>Reload</Button>
          </div>
        )}
        <div className="topbar">
          <button className="iconbtn" title="Toggle sidebar" aria-label="Toggle sidebar" onClick={toggleNav}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"><path d="M3.5 6h17M3.5 12h17M3.5 18h17" /></svg>
          </button>
          <button className="iconbtn" title="Search (Ctrl-K)" aria-label="Search" onClick={() => setSearchOpen(true)}>
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9"><circle cx="11" cy="11" r="7" /><path d="M21 21l-4.3-4.3" /></svg>
          </button>
          <div><h1>{title}</h1><div className="sub">{sub}</div></div>
          <div className="summary">
            {chip('error', ic.err, 'var(--err)', errN, heldN ? `Errors (${heldN} more held by a master sensor that is down)` : 'Errors')}
            {chip('acked', ic.acked, 'var(--acked)', ackN, 'Acknowledged')}
            {chip('warning', ic.warn, 'var(--warn)', warnN, 'Warnings')}
            {chip('ok', ic.ok, 'var(--ok)', okN, 'OK')}
            <span className="statdiv" />
            {chip('paused', ic.paused, 'var(--paused)', pausedN, 'Paused')}
            {chip('hidden', ic.hidden, 'var(--hidden)', hiddenN, 'Hidden')}
          </div>
          <HeaderClock updatedAt={sensorsAt} nextAt={sensorsNext} />
        </div>
        <div className="content view-enter" key={`${view}:${listFilter}`}>
          {view === 'overview' && <StatusListView filter="attention" sensors={sensors} loading={!sensorsLoaded} canPause={canPause} goHost={goHost} goSensor={goSensor} onBack={() => {}} />}
          {view === 'triggers' && <TriggersView goHost={goHost} />}
          {view === 'history' && <HistoryView goHost={goHost} />}
          {view === 'inventory' && <InventoryView goHost={goHost} />}
          {view === 'list' && <StatusListView filter={listFilter} sensors={sensors} loading={!sensorsLoaded || !rowsFor.split(',').includes(listFilter)} canPause={canPause} goHost={goHost} goSensor={goSensor} onBack={() => goto('overview')} />}
          {view === 'monitoring' && <MonitoringView role={me.role} target={treeTarget} homeSignal={monHome} onNavigate={onTreeNav} advanced={!!me.advanced} />}
          {view === 'maintenance' && <MaintenanceView canEdit={me.role === 'admin' || me.role === 'helpdesk'} />}
          {view === 'notifications' && <NotificationsView />}
          {view === 'probes' && <ProbesView role={me.role} enroll={probeEnroll} goHost={goHost} goUpdates={me.role === 'admin' ? () => goto('updates') : undefined} />}
          {view === 'discovery' && me.role === 'admin' && <DiscoveryView scanId={discScan} onOpenScan={openDiscoveryScan} />}
          {view === 'thresholds' && me.role === 'admin' && <ThresholdsView />}
          {view === 'statuspages' && me.role === 'admin' && <StatusPagesView />}
          {view === 'changes' && me.role === 'admin' && <ChangesView goHost={goHost} />}
          {view === 'users' && me.role === 'admin' && <UsersView />}
          {view === 'updates' && me.role === 'admin' && <UpdatesView />}
          {view === 'settings' && me.role === 'admin' && <SettingsView me={me} onMe={onMe} onOpenUpdates={() => goto('updates')} />}
          {view === 'account' && <AccountView me={me} onMe={onMe} passkeysAvailable={passkeysAvailable} theme={theme} toggleTheme={toggleTheme} />}
        </div>
      </div>
    </div>
  )
}

type SettingItem = {
  key: string; label: string; group: string; type: string; secret: boolean; min?: number; options?: string[]; hint: string
  env: string; value: string; source: string; locked: boolean; has_value: boolean
}

// HeaderClock is the top bar's clock, in Argus's timezone and time format (Settings -> General), with
// a countdown to the next refresh of the status data behind the pills (the fetch is timed to the
// server's next build, so the count runs to a fresh answer); the data's age is in the tooltip. Its
// own 1 s tick, so the shell doesn't re-render every second.
function HeaderClock({ updatedAt, nextAt }: { updatedAt: number; nextAt: number }) {
  const [cfg, setCfg] = useState<{ tz?: string; h24: boolean }>({ h24: true })
  const [, setTick] = useState(0)
  useEffect(() => {
    const load = () => fetch('/api/features').then((r) => r.json()).then((f) => {
      CLOCK.tz = f.timezone || undefined; CLOCK.h24 = f.clock_24h !== false
      setCfg({ tz: CLOCK.tz, h24: CLOCK.h24 })
    }).catch(() => {})
    load()
    const cfgT = window.setInterval(load, 5 * 60 * 1000) // picks up a Settings change without a reload
    const t = window.setInterval(() => setTick((n) => n + 1), 1000)
    return () => { clearInterval(cfgT); clearInterval(t) }
  }, [])
  let time = ''
  try { time = new Intl.DateTimeFormat(undefined, { timeZone: cfg.tz, hour: '2-digit', minute: '2-digit', hour12: !cfg.h24 }).format(new Date()) }
  catch { time = new Intl.DateTimeFormat(undefined, { hour: '2-digit', minute: '2-digit', hour12: !cfg.h24 }).format(new Date()) }
  const ago = updatedAt ? Math.max(0, Math.floor((Date.now() - updatedAt) / 1000)) : -1
  const left = nextAt ? Math.max(0, Math.ceil((nextAt - Date.now()) / 1000)) : -1
  const agoText = left < 0 ? 'Loading…' : left > 0 ? `Refresh in ${left}s` : 'Refreshing…'
  const ageTip = ago < 0 ? '' : ago < 60 ? `Data read ${ago}s ago` : `Data read ${Math.floor(ago / 60)}m ago`
  return (
    <div className="hclock" title={[cfg.tz ? `Time in ${cfg.tz}` : '', ageTip].filter(Boolean).join(' · ') || undefined}>
      <div className="hclock-t">{time}</div>
      <div className="hclock-u">{agoText}</div>
    </div>
  )
}

// VMClock is a live wall-clock in the core VM's timezone: the current instant is the same
// everywhere, so formatting "now" in the VM's IANA zone IS the VM's local time (and the sync
// pill next to it vouches that the VM's own clock agrees). Isolated so the 1s tick never
// re-renders the whole settings form.
function VMClock({ tz, clock24 = true }: { tz: string; clock24?: boolean }) {
  const [, setTick] = useState(0)
  useEffect(() => {
    const t = window.setInterval(() => setTick((n) => n + 1), 1000)
    return () => clearInterval(t)
  }, [])
  let text = ''
  try {
    if (tz) text = new Intl.DateTimeFormat(undefined, { timeZone: tz, weekday: 'short', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: !clock24 }).format(new Date())
  } catch { /* unknown zone name: fall back to showing just the zone */ }
  // Just the time: the zone itself is the Timezone field right above (user's call - redundant).
  return <input className="input" disabled value={text || tz} aria-label="Core VM local time" />
}

function SettingsView({ me, onMe, onOpenUpdates }: { me: Me; onMe: (m: Me) => void; onOpenUpdates: () => void }) {
  const toast = useToast()
  const [items, setItems] = useState<SettingItem[] | null>(null)
  const [edits, setEdits] = useState<Record<string, string>>({})
  const [busyGroup, setBusyGroup] = useState<string | null>(null)
  const [advBusy, setAdvBusy] = useState(false)
  const [zbx, setZbx] = useState<{ reachable: boolean; version?: string; error?: string } | null>(null)
  // The core VM's time status (zone + NTP sync), shown under the General group's Timezone field
  // since that setting is what drives the VM's clock.
  const [coreTime, setCoreTime] = useState<OSStatus['core'] | null>(null)

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
  useEffect(() => {
    load(); checkHealth()
    fetch('/api/os/status').then((r) => (r.ok ? r.json() : null)).then((d: OSStatus | null) => { if (d?.core.available) setCoreTime(d.core) }).catch(() => {})
  }, [])

  const setEdit = (k: string, v: string) => setEdits((e) => ({ ...e, [k]: v }))
  const groupKeys = (name: string) => new Set((items || []).filter((it) => it.group === name).map((it) => it.key))

  // Each settings card saves on its own: only that card's fields are sent, and unsaved edits in the
  // other cards stay as they are.
  async function saveGroup(g: { name: string; title: string }, e?: FormEvent) {
    e?.preventDefault()
    const keys = groupKeys(g.name)
    const values = Object.fromEntries(Object.entries(edits).filter(([k]) => keys.has(k)))
    if (Object.keys(values).length === 0) return
    setBusyGroup(g.name)
    try {
      const res = await fetch('/api/settings', { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ values }) })
      if (!res.ok) { toast.error(await errText(res, `Could not save ${g.title}`)); return }
      setItems(await res.json())
      setEdits((cur) => Object.fromEntries(Object.entries(cur).filter(([k]) => !keys.has(k))))
      toast.success(`${g.title} saved and applied.`)
      if (g.name === 'Connection') checkHealth()
    } finally { setBusyGroup(null) }
  }

  const field = (it: SettingItem, busy: boolean) => {
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
        {it.options && it.options.length > 0 ? (
          <Select value={it.locked ? it.value : cur} disabled={it.locked || busy} onChange={(e) => setEdit(it.key, e.target.value)}>
            {it.options.map((o) => <option key={o} value={o}>{OPTION_LABEL[o] || o}</option>)}
          </Select>
        ) : <input
          className="input"
          type={it.secret ? 'password' : it.type === 'int' ? 'number' : 'text'}
          value={it.locked ? (it.secret ? '' : it.value) : cur}
          placeholder={it.locked && it.secret ? '•••••••• (managed by environment)' : ph}
          disabled={it.locked || busy}
          autoComplete={it.secret ? 'new-password' : 'off'}
          min={it.type === 'int' ? (it.min ?? 1) : undefined}
          onChange={(e) => setEdit(it.key, e.target.value)}
        />}
        <span className="set-hint">{it.locked ? `Managed via ${it.env} - unset that variable to edit here.` : it.hint}</span>
      </label>
    )
  }

  const groups: { name: string; title: string; note?: string }[] = [
    { name: 'Connection', title: 'Zabbix connection', note: 'Where Argus reads monitoring data from.' },
    { name: 'General', title: 'General', note: 'Timezone and the external URL used in notification links.' },
    { name: 'Alerting', title: 'Alerting', note: 'When a problem turns into a notification. Per-channel escalation and reminders are set on each channel in Notifications.' },
    { name: 'Watchdog', title: 'Heartbeat', note: "Nothing inside Argus can tell you Argus itself has stopped. Point this at an outside monitor and it alerts you when the pings stop: the VM is down, Zabbix stopped taking data, the alert loop stalled or every alert channel is failing." },
    { name: 'Security', title: 'Login rate limiting', note: 'Brute-force protection thresholds.' },
    { name: 'Sessions', title: 'Sessions', note: 'How long a sign-in stays valid. Changes take effect immediately, including for existing sessions: lowering the max length can sign users out on their next request.' },
    { name: 'Access', title: 'Allowed FQDNs and IPs', note: "The addresses people type in the browser's address bar to open Argus, like monitoring.example.com or 10.0.0.10. With a list set, Argus refuses API requests for any other address and changes coming from other sites, which blocks DNS-rebinding and cross-site attacks." },
    { name: 'Proxy', title: 'Reverse proxy', note: "Which proxies in front of Argus it believes about who is connecting (X-Forwarded-For) and which address and scheme they used (X-Forwarded-Host / -Proto). That feeds the login rate limit, status pages' allowed networks and Allowed FQDNs and IPs. List your proxies' addresses when there's more than one, like NetScaler in front of HAProxy." },
    { name: 'Probes', title: 'Probes', note: 'The address probes dial for the Zabbix server (:10051), and how Argus sizes their Zabbix processes.' },
    { name: 'Changes', title: 'Change log', note: 'How long Argus keeps who changed what (Admin, Changes).' },
  ]

  return (
    <div className="panel">
      <div className="phead">
        <PanelTitle eyebrow="Admin">Settings</PanelTitle>
        <span className="hint">each section saves on its own</span>
      </div>

      <div className="set-body">
        {/* The running version and its licence; updating it, the probes and the VMs is on Updates. */}
        <AboutCard onOpenUpdates={onOpenUpdates} />
        {/* The core server's own SNMP default (stored under proxy id "0"): a server setting, not a probe's. */}
        <section className="set-card">
          <h3>Core SNMP default</h3>
          <p className="set-note">The SNMP credentials of the core server itself: hosts it monitors (Monitored by: Server) that inherit them, and its own discovery scans, use these. Each probe has its own default, under Probes.</p>
          <ProxySNMP proxyId="0" proxyName="the core server" embedded />
        </section>
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
          const busy = busyGroup === g.name
          const gDirty = gi.some((it) => it.key in edits)
          return (
            // A form per card, so Enter in a field saves just this card.
            <form className="set-card" key={g.name} onSubmit={(e) => saveGroup(g, e)}>
              <h3>{g.title}</h3>
              {g.note && <p className="set-note">{g.note}</p>}
              {g.name === 'Connection' && zbx && (
                <div className={'zbx-status ' + (zbx.reachable ? 'ok' : 'bad')}>
                  {zbx.reachable ? `Connected - Zabbix ${zbx.version}` : `Not reachable${zbx.error ? ': ' + zbx.error : ''}`}
                </div>
              )}
              {gi.map((it) => field(it, busy))}
              {g.name === 'Access' && <AllowedHostsStatus items={items} edits={edits} onUse={(v) => setEdit('allowed_hosts', v)} />}
              {g.name === 'Watchdog' && <HeartbeatStatus configured={!!items.find((i) => i.key === 'heartbeat_url')?.value} />}
              {/* The core VM's clock follows the Timezone field above (mirrored through the
                  update-dir channel, applied by a host timer via timedatectl) - so its live
                  state belongs right here, styled like the fields around it. */}
              {g.name === 'General' && coreTime && (coreTime.tz || coreTime.clock_sync !== undefined) && (
                <div className="set-row" style={{ marginBottom: 0 }}>
                  <div className="set-head">
                    <span className="flabel">Core VM clock</span>
                    {coreTime.clock_sync === true && <span className="tag online" title="systemd-timesyncd reports the clock as NTP-synchronized">clock synced</span>}
                    {coreTime.clock_sync === false && <span className="tag avail" title="The VM clock is NOT NTP-synchronized - timestamps will drift; check systemd-timesyncd on the core">clock NOT synced</span>}
                  </div>
                  <VMClock tz={coreTime.tz || ''} clock24={(items?.find((i) => i.key === 'time_format')?.value || '24h') !== '12h'} />
                  <span className="set-hint">Live time in the VM's timezone (the Timezone above, applied by a host timer via timedatectl); the core's reboot and Zabbix update windows, on the Updates page, run on this clock.</span>
                </div>
              )}
              {gi.some((it) => !it.locked) && (
                <div className="set-row set-actions">
                  <button type="submit" className="btn primary" disabled={!gDirty || busy}>{busy ? 'Saving…' : 'Save'}</button>
                </div>
              )}
            </form>
          )
        })}
        {/* Tags: labels across sites, on hosts and probes, for the tree's filter and channel routing. */}
        <TagsCard />
        {/* Device links: buttons every host of some classes gets, filled in from the host. */}
        <LinksCard />
        {/* Zabbix housekeeping (history / trends / compression), saved through its own endpoint. */}
        <DataRetention />
        {/* The core host's own backups (backup.go, deploy/core/host/argus-backup). */}
        <BackupsCard />
      </div>
    </div>
  )
}

type Heartbeat = { configured: boolean; status: { at?: number; ok_at?: number; held?: string; error?: string; status?: number } }

// HeartbeatStatus sits under the Heartbeat URL: the last check (a ping sent, held because Argus isn't
// healthy, or refused by the monitor) and a Send-now button to try a new URL at once.
function HeartbeatStatus({ configured }: { configured: boolean }) {
  const toast = useToast()
  const [hb, setHb] = useState<Heartbeat | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    const load = () => fetch('/api/settings/heartbeat').then((r) => (r.ok ? r.json() : null)).then(setHb).catch(() => {})
    load()
    const t = window.setInterval(load, 30000)
    return () => window.clearInterval(t)
  }, [configured])
  async function sendNow() {
    setBusy(true)
    try {
      const res = await fetch('/api/settings/heartbeat', { method: 'POST' })
      if (!res.ok) { toast.error(await errText(res, 'Could not send the heartbeat')); return }
      const next: Heartbeat = await res.json()
      setHb(next)
      if (next.status.held) toast.error('Held: ' + next.status.held)
      else if (next.status.error) toast.error(next.status.error)
      else toast.success('The monitor took the ping.')
    } catch { toast.error('Could not send the heartbeat') } finally { setBusy(false) }
  }
  if (!configured || !hb) return null
  const st = hb.status
  let tag: ReactNode = <span className="set-src">waiting</span>
  let line = 'The first ping goes out within a minute.'
  if (st.at) {
    if (st.held) { tag = <span className="tag avail">held</span>; line = `Not sent at ${fmtWhen(st.at)}: ${st.held}.` }
    else if (st.error) { tag = <span className="tag avail">failing</span>; line = `${st.error} at ${fmtWhen(st.at)}.` }
    else { tag = <span className="tag online">pinging</span>; line = `Last ping ${fmtWhen(st.at)}, accepted${st.status ? ` (HTTP ${st.status})` : ''}.` }
    if ((st.held || st.error) && st.ok_at) line += ` Last accepted ping ${fmtWhen(st.ok_at)}.`
  }
  return (
    <div className="set-row" style={{ marginBottom: 0 }}>
      <div className="set-head">
        <span className="flabel">Status</span>
        {tag}
      </div>
      <span className="set-hint">
        {line}{' '}
        <button type="button" className="btn" style={{ padding: '2px 10px', marginLeft: 6 }} disabled={busy} onClick={sendNow}>{busy ? 'Sending…' : 'Send now'}</button>
      </span>
    </div>
  )
}

// AllowedHostsStatus sits under the Allowed FQDNs and IPs field: whether the check is on, the address this
// browser is using (always kept working: the server refuses a save that would lock it out), and,
// while the list is empty, a one-click suggestion built from the Public URL host plus this address.
function AllowedHostsStatus({ items, edits, onUse }: { items: SettingItem[]; edits: Record<string, string>; onUse: (v: string) => void }) {
  const it = items.find((i) => i.key === 'allowed_hosts')
  if (!it) return null
  const value = ('allowed_hosts' in edits ? edits.allowed_hosts : it.value).trim()
  const on = value !== '' && value !== '*'
  const here = window.location.hostname.replace(/^\[|\]$/g, '').toLowerCase()
  const loopback = here === 'localhost' || here.endsWith('.localhost') || here === '::1' || /^127\./.test(here)
  let pubHost = ''
  try { const pu = items.find((i) => i.key === 'public_url')?.value; if (pu) pubHost = new URL(pu).hostname.toLowerCase() } catch { /* no Public URL */ }
  const suggestion = [...new Set([pubHost, loopback ? '' : here].filter(Boolean))].join(', ')
  return (
    <div className="set-row" style={{ marginBottom: 0 }}>
      <div className="set-head">
        <span className="flabel">Status</span>
        {on ? <span className="tag online">on</span> : <span className="set-src">off</span>}
      </div>
      <span className="set-hint">
        {on ? 'Requests for any address outside the list are refused. ' : 'Any address is accepted. '}
        You're using <span className="mono">{here}</span>{loopback ? ' (localhost, always allowed)' : ''}.
      </span>
      {!on && suggestion && !it.locked && (
        <span className="set-hint">
          Suggested list: <span className="mono">{suggestion}</span>{' '}
          <button type="button" className="btn" style={{ padding: '2px 10px', marginLeft: 6 }} onClick={() => onUse(suggestion)}>Use this</button>
        </span>
      )}
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
  teams: { c: '#4B53BC', l: 'Tm', label: 'Microsoft Teams' },
  slack: { c: '#4A154B', l: 'S', label: 'Slack' },
  discord: { c: '#5865F2', l: 'D', label: 'Discord' },
  telegram: { c: '#229ED9', l: 'T', label: 'Telegram' },
  email: { c: '#6b7686', l: '@', label: 'Email' },
  ntfy: { c: '#338574', l: 'n', label: 'ntfy' },
  gotify: { c: '#2C7CD1', l: 'G', label: 'Gotify' },
  pushover: { c: '#249DF1', l: 'P', label: 'Pushover' },
  webhook: { c: '#6b7686', l: '{}', label: 'Webhook' },
}
// The types a personal channel can be: public services only (a webhook or Gotify usually lives on the
// core's own network, which a personal channel can't reach), as the server's notify.PersonalTypes.
const PERSONAL_TYPES = ['telegram', 'discord', 'teams', 'slack', 'ntfy', 'pushover']
// A `secret` field is write-only: the server never sends its value back, only `<key>_set`, and a
// blank submit keeps the stored one.
type ChField = { key: string; label: string; ph?: string; type?: string; opt?: boolean; secret?: boolean }
const CH_FIELDS: Record<string, ChField[]> = {
  discord: [{ key: 'webhook_url', label: 'Webhook URL', ph: 'https://discord.com/api/webhooks/…', type: 'password', secret: true }],
  telegram: [
    { key: 'bot_token', label: 'Bot token', ph: '123456:ABC-DEF…', type: 'password', secret: true },
    { key: 'chat_id', label: 'Chat ID', ph: '-1001234567890' },
    { key: 'thread_id', label: 'Topic ID', ph: 'forum topic, optional', opt: true },
  ],
  teams: [{ key: 'webhook_url', label: 'Workflow URL', ph: 'https://….logic.azure.com/workflows/…', type: 'password', secret: true }],
  slack: [{ key: 'webhook_url', label: 'Webhook URL', ph: 'https://hooks.slack.com/services/…', type: 'password', secret: true }],
  ntfy: [
    { key: 'server', label: 'Server', ph: 'https://ntfy.sh', opt: true },
    { key: 'topic', label: 'Topic', ph: 'argus-alerts-x7k2', secret: true },
    { key: 'token', label: 'Access token', ph: 'optional', type: 'password', opt: true, secret: true },
  ],
  gotify: [
    { key: 'server', label: 'Server', ph: 'https://gotify.example.lan' },
    { key: 'token', label: 'Application token', ph: 'Apps → Create application', type: 'password', secret: true },
  ],
  pushover: [
    { key: 'user_key', label: 'User key', ph: 'from your Pushover dashboard', type: 'password', secret: true },
    { key: 'token', label: 'Application API token', ph: 'from an application you create', type: 'password', secret: true },
    { key: 'device', label: 'Device', ph: 'optional, blank = all your devices', opt: true },
  ],
  webhook: [
    { key: 'webhook_url', label: 'URL', ph: 'https://n8n.example.lan/webhook/…', type: 'password', secret: true },
    { key: 'auth_header', label: 'Authorization header', ph: 'optional, e.g. Bearer …', type: 'password', opt: true, secret: true },
  ],
  email: [
    { key: 'host', label: 'SMTP host', ph: 'smtp.example.com' },
    { key: 'port', label: 'Port', ph: '587' },
    { key: 'from', label: 'From address', ph: 'argus@example.com' },
    { key: 'to', label: 'To (comma-separated)', ph: 'you@example.com' },
    { key: 'username', label: 'Username', ph: 'optional', opt: true },
    { key: 'password', label: 'Password', ph: 'optional', type: 'password', opt: true, secret: true },
  ],
}

// CH_HINT is how to get a type's settings, under its fields (both editors).
const CH_HINT: Record<string, string> = {
  teams: 'In the Teams channel, open Workflows, pick the “Send webhook alerts to a channel” template and paste the URL it gives you.',
  slack: 'Add an incoming webhook for the channel (api.slack.com/apps → your app → Incoming Webhooks) and paste its URL.',
  ntfy: 'Subscribe to the topic in the ntfy app. On the public ntfy.sh anyone who knows a topic can read it, so pick one that’s hard to guess, or protect it with a token.',
  gotify: 'The Gotify server’s address and an application token (Gotify → Apps → Create application).',
  pushover: 'Your user key is on the Pushover dashboard; the API token comes from an application you create there. Pushover messages carry the graph too.',
  webhook: 'Argus POSTs each alert as JSON to this URL: kind, state, severity, host, site, the sensor, its reading, the links, and the whole message as plain text in “text”.',
}

// chanFieldProps renders one channel field: a stored secret shows "unchanged" and isn't required.
function chanFieldProps(f: ChField, config: Record<string, string>) {
  const stored = !!f.secret && config[f.key + '_set'] === 'true'
  return { type: f.type || 'text', placeholder: stored ? 'unchanged' : f.ph, required: !f.opt && !stored, autoComplete: f.secret ? 'new-password' : undefined }
}

// SitePicker is a multi-select for a channel's site scope: a compact dropdown that summarizes the
// selection and opens a scrollable, filterable, indented checklist of host-groups. Groups are
// '/'-hierarchical, so selecting a root (site1) covers its subgroups (which then show as inherited).
// Empty selection ("All sites") means every site. Used by the admin and personal channel editors.
function SitePicker({ options, value, onChange, allLabel = 'All sites', labelOf, noAll, placeholder, noun = 'sites' }: { options: string[]; value: string[]; onChange: (v: string[]) => void; allLabel?: string; labelOf?: (v: string) => string; noAll?: boolean; placeholder?: string; noun?: string }) {
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

  // Selecting a root (site1) covers its subgroups (site1/Network): coveredBy finds a selected ancestor,
  // and toggling a group drops any now-redundant descendants already in the selection.
  const coveredBy = (p: string) => value.find((s) => p.startsWith(s + '/'))
  const toggle = (p: string) => {
    if (value.includes(p)) { onChange(value.filter((x) => x !== p)); return }
    onChange(value.filter((x) => !x.startsWith(p + '/')).concat(p))
  }
  const all = value.length === 0
  const name = (v: string) => (labelOf ? labelOf(v) : v)
  const summary = all ? (noAll ? (placeholder || 'None') : allLabel) : value.length <= 2 ? value.map(name).join(', ') : `${value.length} selected`
  const needle = q.trim().toLowerCase()
  const shown = needle ? options.filter((o) => name(o).toLowerCase().includes(needle)) : options

  return (
    <div className="msel" ref={ref}>
      <button type="button" className="msel-btn" onClick={() => setOpen((o) => !o)} aria-haspopup="listbox" aria-expanded={open}>
        <span className="msel-sum">{summary}</span>
        <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true"><path d="M4 6l4 4 4-4" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" /></svg>
      </button>
      {open && (
        <div className="msel-pop" role="listbox" aria-multiselectable="true">
          {options.length > 8 && (
            <input className="input msel-search" placeholder={`Filter ${noun}…`} value={q} onChange={(e) => setQ(e.target.value)} autoFocus />
          )}
          <div className="msel-list">
            {!noAll && (
              <button type="button" role="option" aria-selected={all} className={'msel-opt' + (all ? ' on' : '')} onClick={() => onChange([])}>
                <span className="msel-check">{all ? '✓' : ''}</span>{allLabel}
              </button>
            )}
            {shown.map((s) => {
              const on = value.includes(s)
              const parent = on ? undefined : coveredBy(s)
              const covered = !!parent
              const depth = needle || labelOf ? 0 : s.split('/').length - 1
              const label = labelOf ? labelOf(s) : needle ? s : s.slice(s.lastIndexOf('/') + 1)
              return (
                <button type="button" role="option" aria-selected={on || covered} key={s}
                  className={'msel-opt' + (on ? ' on' : '') + (covered ? ' covered' : '')}
                  style={{ paddingLeft: 9 + depth * 16 }} title={covered ? `Included via ${parent}` : s}
                  onClick={() => { if (!covered) toggle(s) }}>
                  <span className="msel-check">{on || covered ? '✓' : ''}</span>{label}
                </button>
              )
            })}
            {shown.length === 0 && <div className="msel-empty">No matching {noun}</div>}
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
        <PanelTitle eyebrow="Configure">Notifications</PanelTitle>
        <span className="hint">{channels ? `${channels.length} channel${channels.length === 1 ? '' : 's'}` : '…'}</span>
        <div className="tools"><button className="btn primary" onClick={() => setEditing('new')}>+ Add channel</button></div>
      </div>
      <p className="panel-intro">
        Problems route to the channels below, globally or per site, each getting warnings and errors or errors only. A channel can also wait before it's told (escalation) and repeat the alert until someone acknowledges it. Acknowledging stops both and tells the channels that got the alert. Paused and hidden items stay quiet, and a recovery notice follows when things clear.
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
        <EmptyState icon={ic.notifications} title="No channels yet" text="Add Teams, Slack, Discord, Telegram, email, a phone push service or a webhook to start receiving alerts."
          action={<Button variant="primary" onClick={() => setEditing('new')}>+ Add channel</Button>} />
      )}

      {channels && channels.length > 0 && (
        <div className="chan-grid">
          {channels.map((c) => {
            const m = CH_META[c.type] || { c: '#6b7686', l: '?', label: c.type }
            const sev = SEVERITIES.find((s) => s.v === c.min_severity)?.label || 'Warnings and errors'
            return (
              <div className={'chan' + (c.enabled ? '' : ' off')} key={c.id}>
                <div className="ct">
                  <span className="ci" style={{ background: m.c }}>{m.l}</span>
                  <span className="chan-name">{c.name}</span>
                  <Switch checked={c.enabled} onChange={() => toggle(c)} title={c.enabled ? 'Enabled - switch off to pause alerts to this channel' : 'Disabled - switch on to resume alerts'} />
                </div>
                <p className="chan-meta">{m.label} · {sitesLabel(c.sites)} · {c.alerts === false ? 'No alerts' : sev}{c.type === 'email' && c.config?.recipients === 'users' ? ' · to all users' : ''}{c.alerts === false ? '' : timingLabel(c)}{c.system_notices ? ' · system notices' : ''}</p>
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

// Escalation and reminder choices for a channel, in minutes (0 = at once / off). A stored value outside
// the list (set through the API) is kept as an extra option, so opening the editor never changes it.
const DELAY_CHOICES = [0, 5, 15, 30, 60, 120, 240]
const REPEAT_CHOICES = [0, 15, 30, 60, 120, 240, 720, 1440]

function fmtMinutes(m: number): string {
  if (m < 60) return `${m} min`
  const h = Math.floor(m / 60), r = m % 60
  return r ? `${h} h ${r} min` : `${h} h`
}

// timingLabel is the channel card's escalation summary, appended to its meta line ("" when immediate
// with no reminders, which is how every channel behaved before escalation existed).
function timingLabel(c: { min_severity: number; delay_min?: number; repeat_min?: number; repeat_min_severity?: number; who_to_call?: boolean; alerts?: boolean }): string {
  let out = ''
  if (c.delay_min) out += ` · after ${fmtMinutes(c.delay_min)}`
  if (c.repeat_min) {
    out += ` · reminds every ${fmtMinutes(c.repeat_min)}`
    const rs = c.repeat_min_severity || 2
    if (rs > c.min_severity) out += ` (${SEVERITIES.find((s) => s.v === rs)?.label || ''})`
  }
  if (c.who_to_call && c.alerts !== false) out += ' · says who to call'
  return out
}

function EscalationSection({ delay, repeat, remSev, onDelay, onRepeat, onRemSev, who }: {
  delay: number; repeat: number; remSev: number; onDelay: (m: number) => void; onRepeat: (m: number) => void; onRemSev: (s: number) => void; who: string
}) {
  const opts = (list: number[], cur: number) => (list.includes(cur) ? list : [...list, cur].sort((a, b) => a - b))
  return (
    <ChanSection title="Escalation and reminders"
      note={`${who} ${who === 'You' ? 'hear' : 'hears'} only of problems still open and unacknowledged after "Notify after". Reminders repeat an open problem until someone acknowledges it.`}>
      <div className="chan-row chan-row-3">
        <label className="chan-field"><span className="flabel">Notify after</span>
          <Select value={delay} onChange={(e) => onDelay(Number(e.target.value))}>
            {opts(DELAY_CHOICES, delay).map((m) => <option key={m} value={m}>{m ? fmtMinutes(m) : 'Immediately'}</option>)}
          </Select>
        </label>
        <label className="chan-field"><span className="flabel">Remind every</span>
          <Select value={repeat} onChange={(e) => onRepeat(Number(e.target.value))}>
            {opts(REPEAT_CHOICES, repeat).map((m) => <option key={m} value={m}>{m ? fmtMinutes(m) : 'Off'}</option>)}
          </Select>
        </label>
        <label className="chan-field"><span className="flabel">Remind for</span>
          <Select value={remSev} onChange={(e) => onRemSev(Number(e.target.value))} disabled={!repeat} title="Only problems at or above this severity are repeated; lower ones are still alerted once">
            {SEVERITIES.map((s) => <option key={s.v} value={s.v}>{s.label}</option>)}
          </Select>
        </label>
      </div>
    </ChanSection>
  )
}

// NoticesSwitch turns on Argus's own system notices for a channel (off by default).
// CallSwitch is a channel's "Who to call": its alerts carry the site's internet line and contact.
function CallSwitch({ checked, onChange }: { checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="chan-field">
      <Switch checked={checked} onChange={onChange} label="Who to call" />
      <span className="set-note" style={{ margin: 0 }}>Adds who to call from the site's info: on an alert about an internet line, its provider, circuit and support number; on every alert, the site's first contact. Whoever reads it on the phone can call straight away.</span>
    </div>
  )
}

function NoticesSwitch({ checked, onChange }: { checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="chan-field">
      <Switch checked={checked} onChange={onChange} label="System notices" />
      <span className="set-note" style={{ margin: 0 }}>Argus's own news, sent once each: a new Argus release and self-update results, probes or updaters behind, pending OS updates or reboots, a Zabbix update for the core, finished discovery scans, and a channel that keeps failing.</span>
    </div>
  )
}

function ChanSection({ title, note, children }: { title: string; note?: string; children: ReactNode }) {
  return (
    <section className="chan-sec">
      <div className="chan-sec-h">{title}</div>
      {note && <p className="set-note" style={{ margin: 0 }}>{note}</p>}
      {children}
    </section>
  )
}

// ChannelDialog is the modal shell shared by the shared-channel and personal-channel editors: title,
// scrolling sections, and a pinned footer with the Enabled switch and the actions. Escape and a click
// on the backdrop cancel, like the other Argus dialogs.
function ChannelDialog({ title, submitLabel, enabled, setEnabled, onCancel, onSubmit, children }: {
  title: string; submitLabel: string; enabled: boolean; setEnabled: (v: boolean) => void
  onCancel: () => void; onSubmit: (e: FormEvent) => void; children: ReactNode
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onCancel() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onCancel])
  return createPortal(
    <div className="dlg-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onCancel() }}>
      <form className="dlg" role="dialog" aria-modal="true" onSubmit={onSubmit}
        style={{ maxWidth: 'min(680px, 94vw)', maxHeight: 'calc(100dvh - 32px)', display: 'flex', flexDirection: 'column' }}>
        <div className="dlg-title">{title}</div>
        <div className="dlg-scroll"><div className="chan-form">{children}</div></div>
        <div className="chan-dlg-foot">
          <Switch checked={enabled} onChange={setEnabled} label="Enabled" />
          <div style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
            <button type="button" className="btn" onClick={onCancel}>Cancel</button>
            <button type="submit" className="btn primary">{submitLabel}</button>
          </div>
        </div>
      </form>
    </div>,
    document.body,
  )
}

// ChannelDelivery is the one-line health of a channel: when it last delivered, or the last failure and
// why. Recorded by the notifier per send (alerts and the Send test button alike).
function ChannelDelivery({ c }: { c: { last_sent_at?: number; last_error?: string; last_error_at?: number; sent_count?: number } }) {
  const failed = !!c.last_error_at && (!c.last_sent_at || c.last_error_at >= c.last_sent_at)
  if (failed) return <div className="chan-status err" title={c.last_error || ''}>Last delivery failed {relTime(c.last_error_at!)}{c.last_error ? ` · ${c.last_error}` : ''}</div>
  if (c.last_sent_at) return <div className="chan-status ok">Last sent {relTime(c.last_sent_at)}{c.sent_count ? ` · ${c.sent_count} delivered` : ''}</div>
  return <div className="chan-status">Nothing sent yet - use “Send test” to check the setup.</div>
}

function ChannelEditor({ initial, sites, onCancel, onSaved, onError }: {
  initial: Channel | null; sites: string[]; onCancel: () => void; onSaved: () => void; onError: (m: string) => void
}) {
  const [type, setType] = useState(initial?.type || 'discord')
  const [name, setName] = useState(initial?.name || '')
  const [selSites, setSelSites] = useState<string[]>(initial?.sites || [])
  const [selTags, setSelTags] = useState<string[]>(initial?.tags || [])
  const [allTags] = useTags()
  const [minSev, setMinSev] = useState(initial?.min_severity || 2)
  const [delayMin, setDelayMin] = useState(initial?.delay_min || 0)
  const [repeatMin, setRepeatMin] = useState(initial?.repeat_min || 0)
  const [remSev, setRemSev] = useState(initial?.repeat_min_severity || 2)
  const [alerts, setAlerts] = useState(initial ? initial.alerts !== false : true)
  const [notices, setNotices] = useState(!!initial?.system_notices)
  const [call, setCall] = useState(!!initial?.who_to_call)
  const [enabled, setEnabled] = useState(initial ? initial.enabled : true)
  const [config, setConfig] = useState<Record<string, string>>(initial?.config || {})
  const setCfg = (k: string, v: string) => setConfig((c) => ({ ...c, [k]: v }))

  async function save(e: FormEvent) {
    e.preventDefault(); onError('')
    const body = { type, name, sites: selSites, tags: selTags, min_severity: minSev, delay_min: delayMin, repeat_min: repeatMin, repeat_min_severity: remSev, alerts, system_notices: notices, who_to_call: alerts && call, enabled, config }
    const url = initial ? `/api/notify/channels/${initial.id}` : '/api/notify/channels'
    const res = await fetch(url, { method: initial ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
    if (!res.ok) { onError(await errText(res, 'Could not save channel')); return }
    onSaved()
  }

  const fields = CH_FIELDS[type] || []
  return (
    <ChannelDialog title={initial ? `Edit ${initial.name}` : 'Add channel'} submitLabel={initial ? 'Save changes' : 'Add channel'}
      enabled={enabled} setEnabled={setEnabled} onCancel={onCancel} onSubmit={save}>
      <ChanSection title="Channel">
        <div className="chan-row">
          <label className="chan-field"><span className="flabel">Type</span>
            <Select value={type} onChange={(e) => setType(e.target.value)} disabled={!!initial}>
              {Object.keys(CH_META).map((t) => <option key={t} value={t}>{CH_META[t].label}</option>)}
            </Select>
          </label>
          <label className="chan-field chan-wide"><span className="flabel">Name</span>
            <input className="input" placeholder={`e.g. ${CH_META[type]?.label || 'Discord'} - site1`} value={name} onChange={(e) => setName(e.target.value)} required />
          </label>
        </div>
        <div className="chan-row">
          {type === 'email' && (
            <label className="chan-field"><span className="flabel">Send to</span>
              <Select value={config.recipients || 'fixed'} onChange={(e) => setCfg('recipients', e.target.value)}>
                <option value="fixed">A fixed address</option>
                <option value="users">Each user’s registered email</option>
              </Select>
            </label>
          )}
          {fields.filter((f) => !(type === 'email' && f.key === 'to' && (config.recipients || 'fixed') === 'users')).map((f) => (
            <label key={f.key} className={'chan-field' + (fields.length === 1 ? ' chan-full' : '')}><span className="flabel">{f.label}</span>
              <input className="input" {...chanFieldProps(f, config)} value={config[f.key] || ''} onChange={(e) => setCfg(f.key, e.target.value)} />
            </label>
          ))}
          {type === 'email' && (
            <label className="chan-field"><span className="flabel">Encryption</span>
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
        {CH_HINT[type] && <p className="set-note">{CH_HINT[type]}</p>}
      </ChanSection>
      <ChanSection title="What it receives">
        <div className="chan-row">
          <div className="chan-field chan-wide"><span className="flabel">Sites</span>
            <SitePicker options={sites} value={selSites} onChange={setSelSites} />
          </div>
          <label className="chan-field"><span className="flabel">Alerts</span>
            <Select value={alerts ? minSev : 0} onChange={(e) => { const v = Number(e.target.value); setAlerts(v !== 0); if (v) setMinSev(v) }} title="Only problems at or above this severity reach this channel">
              {SEVERITIES.map((s) => <option key={s.v} value={s.v}>{s.label}</option>)}
              <option value={0}>None</option>
            </Select>
          </label>
        </div>
        {(allTags.length > 0 || selTags.length > 0) && (
          <div className="chan-row">
            <div className="chan-field chan-wide"><span className="flabel">Tags</span>
              <TagPicker tags={allTags} value={selTags} onChange={setSelTags} allLabel="Any tag" />
              <span className="set-note" style={{ margin: 0 }}>Only hosts with one of these tags, their own or from their probe. Any tag = every host.</span>
            </div>
          </div>
        )}
        {alerts && <CallSwitch checked={call} onChange={setCall} />}
        <NoticesSwitch checked={notices} onChange={setNotices} />
        {!alerts && !notices && <p className="set-note txt-err" style={{ margin: 0 }}>Turn on alerts, system notices, or both.</p>}
      </ChanSection>
      {alerts && <EscalationSection delay={delayMin} repeat={repeatMin} remSev={remSev} onDelay={setDelayMin} onRepeat={setRepeatMin} onRemSev={setRemSev} who="This channel" />}
    </ChannelDialog>
  )
}

type UserChannel = { id: number; type: string; enabled: boolean; sites: string[]; tags?: string[]; min_severity: number; delay_min?: number; repeat_min?: number; repeat_min_severity?: number; alerts?: boolean; system_notices?: boolean; who_to_call?: boolean; config: Record<string, string>; last_sent_at?: number; last_error?: string; last_error_at?: number; sent_count?: number }

// PersonalNotifyCard lets any signed-in user manage their own alert destinations (PERSONAL_TYPES),
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
    <Card title="Personal notifications" note="Get alerts on your own Telegram, Discord, Teams, Slack, ntfy or Pushover. Only you receive these - they’re separate from the shared channels an admin manages.">
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
        <p className="set-note">No personal channels yet. Add your own Telegram, Discord, Teams, Slack, ntfy or Pushover to get your own alerts.</p>
      )}
      {channels && channels.length > 0 && (
        <div className="chan-grid">
          {channels.map((c) => {
            const m = CH_META[c.type] || { c: '#6b7686', l: '?', label: c.type }
            const sev = SEVERITIES.find((s) => s.v === c.min_severity)?.label || 'Warnings and errors'
            return (
              <div className={'chan' + (c.enabled ? '' : ' off')} key={c.id}>
                <div className="ct">
                  <span className="ci" style={{ background: m.c }}>{m.l}</span>
                  <span className="chan-name">{m.label}</span>
                  <Switch checked={c.enabled} onChange={() => toggle(c)} title={c.enabled ? 'Enabled - switch off to pause your alerts here' : 'Disabled - switch on to resume'} />
                </div>
                <p className="chan-meta">{sitesLabel(c.sites)} · {c.alerts === false ? 'No alerts' : sev}{c.alerts === false ? '' : timingLabel(c)}{c.system_notices ? ' · system notices' : ''}</p>
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
  const [selTags, setSelTags] = useState<string[]>(initial?.tags || [])
  const [allTags] = useTags()
  const [minSev, setMinSev] = useState(initial?.min_severity || 2)
  const [delayMin, setDelayMin] = useState(initial?.delay_min || 0)
  const [repeatMin, setRepeatMin] = useState(initial?.repeat_min || 0)
  const [remSev, setRemSev] = useState(initial?.repeat_min_severity || 2)
  const [alerts, setAlerts] = useState(initial ? initial.alerts !== false : true)
  const [notices, setNotices] = useState(!!initial?.system_notices)
  const [call, setCall] = useState(!!initial?.who_to_call)
  const [enabled, setEnabled] = useState(initial ? initial.enabled : true)
  const [config, setConfig] = useState<Record<string, string>>(initial?.config || {})
  const setCfg = (k: string, v: string) => setConfig((c) => ({ ...c, [k]: v }))

  async function save(e: FormEvent) {
    e.preventDefault(); onError('')
    const body = { type, sites: selSites, tags: selTags, min_severity: minSev, delay_min: delayMin, repeat_min: repeatMin, repeat_min_severity: remSev, alerts, system_notices: notices, who_to_call: alerts && call, enabled, config }
    const url = initial ? `/api/me/notify/channels/${initial.id}` : '/api/me/notify/channels'
    const res = await fetch(url, { method: initial ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
    if (!res.ok) { onError(await errText(res, 'Could not save channel')); return }
    onSaved()
  }

  const fields = CH_FIELDS[type] || []
  const hint = type === 'telegram'
    ? 'Create your own bot with @BotFather for the token, and message the bot once so it’s allowed to reach you.'
    : type === 'discord' ? 'Paste a Discord channel webhook URL (Server Settings → Integrations → Webhooks).'
    : type === 'ntfy' ? CH_HINT.ntfy + ' A personal channel can only use a server on the internet (blank = ntfy.sh).'
    : CH_HINT[type] || ''
  return (
    <ChannelDialog title={initial ? `Edit your ${CH_META[initial.type]?.label || initial.type} channel` : 'Add a personal channel'} submitLabel={initial ? 'Save changes' : 'Add channel'}
      enabled={enabled} setEnabled={setEnabled} onCancel={onCancel} onSubmit={save}>
      <ChanSection title="Channel">
        <div className="chan-row">
          <label className="chan-field"><span className="flabel">Type</span>
            <Select value={type} onChange={(e) => setType(e.target.value)} disabled={!!initial}>
              {PERSONAL_TYPES.map((t) => <option key={t} value={t}>{CH_META[t].label}</option>)}
            </Select>
          </label>
        </div>
        <div className="chan-row">
          {fields.map((f) => (
            <label key={f.key} className={'chan-field' + (fields.length === 1 ? ' chan-full' : '')}><span className="flabel">{f.label}</span>
              <input className="input" {...chanFieldProps(f, config)} value={config[f.key] || ''} onChange={(e) => setCfg(f.key, e.target.value)} />
            </label>
          ))}
        </div>
        <p className="set-note">{hint}</p>
      </ChanSection>
      <ChanSection title="What you receive">
        <div className="chan-row">
          <div className="chan-field chan-wide"><span className="flabel">Sites</span>
            <SitePicker options={sites} value={selSites} onChange={setSelSites} allLabel={SCOPE.sites.length ? 'All your sites' : 'All sites'} />
          </div>
          <label className="chan-field"><span className="flabel">Alerts</span>
            <Select value={alerts ? minSev : 0} onChange={(e) => { const v = Number(e.target.value); setAlerts(v !== 0); if (v) setMinSev(v) }} title="Only problems at or above this severity reach you">
              {SEVERITIES.map((s) => <option key={s.v} value={s.v}>{s.label}</option>)}
              <option value={0}>None</option>
            </Select>
          </label>
        </div>
        {(allTags.length > 0 || selTags.length > 0) && (
          <div className="chan-row">
            <div className="chan-field chan-wide"><span className="flabel">Tags</span>
              <TagPicker tags={allTags} value={selTags} onChange={setSelTags} allLabel="Any tag" />
              <span className="set-note" style={{ margin: 0 }}>Only hosts with one of these tags, their own or from their probe. Any tag = every host.</span>
            </div>
          </div>
        )}
        {alerts && <CallSwitch checked={call} onChange={setCall} />}
        <NoticesSwitch checked={notices} onChange={setNotices} />
        {!alerts && !notices && <p className="set-note txt-err" style={{ margin: 0 }}>Turn on alerts, system notices, or both.</p>}
      </ChanSection>
      {alerts && <EscalationSection delay={delayMin} repeat={repeatMin} remSev={remSev} onDelay={setDelayMin} onRepeat={setRepeatMin} onRemSev={setRemSev} who="You" />}
    </ChannelDialog>
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

// Sentinel for the Core-server SNMP-defaults band in openSnmp (otherwise keyed by probe name).
// The core's default is stored under proxy id "0" and is what core-monitored hosts inherit and
// core-run discovery scans fingerprint with.

function ProbesView({ role, enroll, goHost, goUpdates }: { role: string; enroll: boolean; goHost: (hostId: string) => void; goUpdates?: () => void }) {
  const confirm = useConfirm()
  const alert = useAlert()
  const [proxies, setProxies] = useState<Proxy[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tokens, setTokens] = useState<EnrollTokenRow[] | null>(null)
  const [wizardOpen, setWizardOpen] = useState(false)
  const [report, setReport] = useState<{ name: string; token: string } | null>(null) // minted check-in token to show
  const [openSnmp, setOpenSnmp] = useState<string | null>(null) // proxy name whose SNMP-defaults band is open
  const [openProcs, setOpenProcs] = useState<string | null>(null) // proxy name whose Zabbix-processes band is open
  const canEdit = role === 'admin' || role === 'helpdesk'
  const isAdmin = role === 'admin'
  const toast = useToast()
  const [allTags, reloadTags] = useTags()
  const [tagsFor, setTagsFor] = useState<Proxy | null>(null) // the probe whose tags dialog is open
  const [probeTags, setProbeTags] = useState<string[]>([])
  const [tagsBusy, setTagsBusy] = useState(false)
  const [tagsReason, setTagsReason] = useState('')
  const tagColor = (n: string) => allTags.find((t) => t.name === n)?.color || '#8b8d98'
  async function saveProbeTags() {
    if (!tagsFor) return
    setTagsBusy(true)
    const res = await fetch(`/api/proxies/${tagsFor.id}/tags`, { method: 'PUT', headers: { 'Content-Type': 'application/json', ...reasonHeader(tagsReason) }, body: JSON.stringify({ tags: probeTags }) }).catch(() => null)
    setTagsBusy(false)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not save the tags')); return }
    setTagsFor(null); setTagsReason(''); loadProxies(); reloadTags(); fireDataRefresh()
  }

  // The version cells say where each probe stands, in the Updates page's words. Updating is done
  // there, so a pill with something to do links to it (for an admin; helpdesk sees the same words).
  const toUpdates = (st: { pill: ReactNode; can: string | boolean }) => st.can && goUpdates
    ? <button type="button" className="linklike" onClick={goUpdates} title="Update it on the Updates page">{st.pill}</button> : st.pill

  // Reveal a probe VM's break-glass console credential (admin). Fetched on demand - it's never part
  // of the /api/proxies list - and shown in an in-app dialog with copy buttons.
  async function revealBreakGlass(p: Proxy) {
    try {
      const res = await fetch(`/api/probes/${encodeURIComponent(p.name)}/break-glass`)
      if (!res.ok) { alert({ title: 'Console access', message: await errText(res, 'Could not read the credential'), danger: true }); return }
      const d = await res.json()
      alert({
        title: `Console access - ${p.name}`,
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
    .then((p: Proxy[]) => { setProxies(p || []); setError(null) })
    .catch((e) => setError(e instanceof Error ? e.message : 'Failed to load probes'))
  useEffect(() => { loadProxies(); const t = setInterval(loadProxies, 30000); return () => clearInterval(t) }, []) // eslint-disable-line react-hooks/exhaustive-deps
  // While an update is queued or under way, look every 5 s, so its row moves on as it happens.
  const jobBusy = (proxies || []).some((p) => [p.update_job, p.updater_job].some((j) => j && j.state !== 'failed'))
  useEffect(() => { if (!jobBusy) return; const t = setInterval(loadProxies, 5000); return () => clearInterval(t) }, [jobBusy]) // eslint-disable-line react-hooks/exhaustive-deps

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
    if (!(await confirm({ title: 'Delete probe', message: `Remove “${p.name}” from Zabbix and delete its Argus records (enrollment tokens, check-in state, SNMP default)? Its Probe health host is deleted with it; Zabbix won't allow this while other hosts are still monitored by it. Its host group is left in place.`, confirmLabel: 'Delete', danger: true }))) return
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
    alert({ title: 'Clean up', message: d.pruned > 0 ? `Removed ${d.pruned} orphaned record${d.pruned === 1 ? '' : 's'} left by proxies deleted in Zabbix.` : 'No orphaned records - everything is in sync with Zabbix.' })
  }

  // Enrolled tokens are just noise once a probe is live (its enrollment date shows in the row
  // below), so the list keeps only what's still actionable: pending and expired tokens.
  const pendingTokens = (tokens || []).filter((t) => t.status !== 'enrolled')

  return (
    <div className="panel">
      <div className="phead">
        <PanelTitle eyebrow="Configure">Site probes</PanelTitle>
        <span className="hint">{proxies ? `${proxies.length} known to the core` : '…'}</span>
        {(() => {
          const needReboot = (proxies || []).filter((p) => p.reboot_required).length
          return needReboot > 0 ? <span className="tag avail" title="These probe VMs need a reboot to finish applying OS updates; each reboots in its weekly ~03:00 window">{needReboot === 1 ? '1 needs' : `${needReboot} need`} a reboot</span> : null
        })()}
        {canEdit && <div className="tools">
          {isAdmin && <button className="btn" onClick={reconcile} title="Prune Argus records left behind by probes deleted directly in Zabbix">Clean up</button>}
          {isAdmin && enroll && <button className="btn primary" onClick={() => setWizardOpen(true)}>+ Add probe</button>}
        </div>}
      </div>

      {isAdmin && !enroll && (
        <p style={{ color: 'var(--muted)', fontSize: 12.5, padding: '2px 16px 0', margin: 0 }}>
          One-click enrollment is off. Mount the monitoring CA into Argus and set <code>ARGUS_CA_CERT_FILE</code> / <code>ARGUS_CA_KEY_FILE</code> (and <code>ARGUS_PROBE_CORE_HOST</code>) to enable it. Live probe status still works below.
        </p>
      )}

      {tagsFor && (
        <BulkDialog title={`Tags · ${tagsFor.name}`} note="Every host this probe monitors carries these tags, including hosts added later. A host shows them beside its own and can't remove them; a host moved to another probe takes that probe's instead." busy={tagsBusy} applyLabel="Save" reason={tagsReason} setReason={setTagsReason} onClose={() => setTagsFor(null)} onApply={saveProbeTags}>
          <div className="hs-mon hs-tags"><span className="hs-monlabel">Tags</span><TagsEditor all={allTags} value={probeTags} onChange={setProbeTags} /></div>
        </BulkDialog>
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
                    {((p.tags && p.tags.length > 0) || (isAdmin && allTags.length > 0)) && (
                      <span className="probe-tags">
                        <TagList tags={(p.tags || []).map((n) => ({ name: n, color: tagColor(n) }))} />
                        {isAdmin && <button type="button" className="linkbtn" onClick={() => { setTagsFor(p); setProbeTags(p.tags || []) }}>{p.tags && p.tags.length ? 'Edit tags' : '+ Tags'}</button>}
                      </span>
                    )}
                  </div>
                </td>
                <td data-label="Health">
                  <div className="cell-stack">
                    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                      {p.online ? <span className="tag online">● online</span> : <span className="tag pending">offline</span>}
                      {/* The probe's own health sensors (queue, backlog, caches, process load) live on its
                          Argus-managed Probe host in the site group; its worst open problem shows here. */}
                      {p.probe_host_id && (
                        <button type="button" className="linklike" onClick={() => goHost(p.probe_host_id!)} title="Open this probe's health sensors in Monitoring">
                          {p.probe_health === 'error' ? <span className="tag err">health: error</span>
                            : p.probe_health === 'warning' ? <span className="tag avail">health: warning</span>
                            : <span className="tag online">health: ok</span>}
                        </button>
                      )}
                      {(p.cpu_starved || (p.procs || []).some((r) => r.held)) && (
                        <button type="button" className="linklike" onClick={() => setOpenProcs(p.name)} title="Argus stopped adding processes on this probe: see Processes">
                          <span className="tag err">short on CPU?</span>
                        </button>
                      )}
                      {p.procs_pending && (
                        <button type="button" className="linklike" onClick={() => setOpenProcs(p.name)} title={p.procs_note ? `Argus changed the process counts: ${p.procs_note}` : 'Argus changed the process counts'}>
                          <span className="tag avail">{p.autoscale === 'restart' && p.procs_restarts ? 'processes: restart pending' : 'processes: next start'}</span>
                        </button>
                      )}
                    </span>
                    <span className="sub-line mono" title="When the core last received data from this probe" style={{ paddingLeft: 10, color: !p.last_access ? 'var(--faint)' : (Date.now() / 1000 - p.last_access > 60 ? 'var(--warn)' : undefined) }}>{p.last_access ? relTime(p.last_access) : 'never'}</span>
                  </div>
                </td>
                <td data-label="Proxy Version">
                  <span className="vcell">
                    <UpdVer v={p.version} title="Zabbix proxy version running on this probe" />
                    {/* Probes that don't check in (updated outside Argus) can report with a minted token. */}
                    {isAdmin && !p.last_checkin && (!p.update_status || p.update_status === 'external')
                      ? <button className="btn" onClick={() => enableReporting(p)} title="Issue a check-in token so this probe reports its exact version to Argus">Enable reporting</button>
                      : toUpdates(proxyState(p))}
                  </span>
                </td>
                <td data-label="Updater Version">
                  <span className="vcell">{p.selfupdate && <UpdVer v={vv(p.updater_version) || '?'} title="Version of the argus-updater sidecar managing this probe" />}{toUpdates(updaterState(p))}</span>
                </td>
                <td data-label="VM OS Version"><OSCell p={p} /></td>
                <td className="row-actions">
                  <ProbeRowMenu items={[
                    canEdit && p.id ? { label: 'SNMP defaults', onClick: () => setOpenSnmp((n) => (n === p.name ? null : p.name)) } : null,
                    { label: 'Processes', onClick: () => setOpenProcs((n) => (n === p.name ? null : p.name)) },
                    isAdmin && p.break_glass ? { label: p.break_glass_user ? `Console (${p.break_glass_user})` : 'Console', onClick: () => revealBreakGlass(p) } : null,
                    isAdmin && p.id ? 'sep' : null,
                    isAdmin && p.id ? { label: 'Delete probe', onClick: () => del(p), danger: true } : null,
                  ]} />
                </td>
              </tr>
              {report?.name === p.name && <tr><td colSpan={6} style={{ padding: 0 }}><ReportTokenPanel token={report.token} name={p.name} onDone={() => setReport(null)} /></td></tr>}
              {openSnmp === p.name && <tr><td colSpan={6} style={{ padding: 0 }}><ProxySNMP proxyId={p.id} proxyName={p.name} onClose={() => setOpenSnmp(null)} /></td></tr>}
              {openProcs === p.name && <tr><td colSpan={6} style={{ padding: 0 }}><ProbeProcesses p={p} isAdmin={isAdmin} onChanged={loadProxies} onClose={() => setOpenProcs(null)} /></td></tr>}
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

// OSCell is the "OS" column: a VM probe's Debian patch status (DESIGN §14c). The OS patches itself
// (unattended-upgrades, security only) and auto-reboots in a weekly window; this only *reports*. A
// dash means no report (a container probe, or a VM that hasn't reported yet).
function OSCell({ p }: { p: Proxy }) {
  if (!p.os_reported_at) return <span className="mono" style={{ color: 'var(--faint)' }} title="No OS patch report - a container probe, or a VM probe that hasn't reported yet">-</span>
  return (
    <span className="vcell">
      {p.os_version && <UpdVer v={osName(p.os_version)} title="Operating system reported by the VM" />}
      {osPill(typeof p.sec_updates === 'number' ? p.sec_updates : -1, !!p.reboot_required, `Reported ${relTime(p.os_reported_at)}.`)}
    </span>
  )
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

// ProbeProcesses is the inline band listing a probe's Zabbix process counts: what each kind runs, how
// busy its busiest hour was at Argus's last evaluation, and the count Argus wants (autoscale.go).
function ProbeProcesses({ p, isAdmin, onChanged, onClose }: { p: Proxy; isAdmin: boolean; onChanged: () => void; onClose: () => void }) {
  const toast = useToast()
  const [busy, setBusy] = useState(false)
  const rows = p.procs || []
  const held = rows.filter((r) => r.held)
  const fmtLoad = (v: number) => (Math.round(v * 100) / 100).toString()
  async function release() {
    setBusy(true)
    try {
      const r = await fetch(`/api/probes/${encodeURIComponent(p.name)}/procs/release`, { method: 'POST' })
      if (!r.ok) { toast.error((await r.json().catch(() => ({}))).error || 'Could not release the holds'); return }
      toast.success('Argus judges these counts again at its next evaluation.')
      onChanged()
    } finally { setBusy(false) }
  }
  const settling = p.procs_since ? Date.now() / 1000 - p.procs_since < 6 * 3600 : false
  const pending = p.procs_pending
    ? (p.autoscale === 'restart' && p.procs_restarts
      ? 'A change is waiting: the updater restarts the probe to apply it within a few minutes (a few seconds of downtime; collected data is kept).'
      : "A change is waiting: it applies when the probe next starts (it has no updater sidecar that can restart it, or autoscaling applies at the next start).")
    : ''
  return (
    <div className="host-settings">
      <div className="hs-title">Zabbix processes · {p.name}</div>
      <div className="hs-note">
        {p.autoscale === 'off'
          ? 'Autoscaling is off (Settings, Probes): these counts stay as they are.'
          : 'Argus sizes each kind from its busiest hour over the last day: over 60% raises the count (aiming for 50%), under 20% lowers it, never below the image default. Counts set on the container are left alone.'}
      </div>
      {p.cpu_count ? (
        <div className="hs-note">
          CPU: {p.cpu_usable && p.cpu_usable !== p.cpu_count ? `${p.cpu_usable} usable of ${p.cpu_count}` : `${p.cpu_count}`} · load {p.cpu_load ? p.cpu_load.map(fmtLoad).join(' / ') : '-'} (1 / 5 / 15 min)
          {p.cpu_peak != null ? ` · busiest hour ${fmtLoad(p.cpu_peak)} per CPU` : ''}
          {!p.is_vm ? ' · a container sees its whole host, so this load includes the other containers there' : ''}
        </div>
      ) : null}
      {p.cpu_starved && (
        <div className="hs-note" style={{ color: 'var(--err)' }}>
          The busiest hour kept every CPU busy, so Argus adds no processes: more of them would only queue for the CPU. {p.is_vm ? 'Give the VM more vCPUs, or split the site across two probes.' : 'The Docker host may be short on CPU, or the site needs a second probe.'}
        </div>
      )}
      {held.length > 0 && (
        <div className="hs-note" style={{ color: 'var(--err)' }}>
          Raising {held.map((r) => r.label.toLowerCase()).join(', ')} didn't lower their load, so Argus put them back and holds them. The limit is likely the CPU (or the site grew meanwhile). Argus tries again when the CPU count changes{isAdmin ? ', or now:' : '.'}
          {isAdmin && <> <Button variant="default" onClick={release} disabled={busy}>Try again</Button></>}
        </div>
      )}
      {rows.length === 0
        ? <div className="hs-note">This probe doesn't report its process counts yet. It needs a probe image from 7.0.31-r8 on.</div>
        : (
          <table className="sensors" style={{ marginTop: 6 }}>
            <thead><tr><th>Process</th><th>Running</th><th>Busiest hour</th><th>Argus</th></tr></thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.name}>
                  <td title={r.name}>{r.label}</td>
                  <td className="mono">{r.running}</td>
                  <td className="mono">{r.peak != null ? `${Math.round(r.peak)}%` : <span style={{ color: 'var(--faint)' }}>-</span>}</td>
                  <td>
                    {r.pinned
                      ? <span className="tag" title={`Set on the container (ZBX_${r.name.toUpperCase()}): Argus leaves it alone`}>set on container</span>
                      : r.held && (!r.target || r.target === r.running)
                      ? <span className="tag err" title={`Raised ${r.held.from} to ${r.held.to} ${relTime(r.held.at)}: busiest hour ${Math.round(r.held.before)}% before, ${Math.round(r.held.after)}% after. Put back and held.`}>held at {r.running}</span>
                      : r.target && r.target !== r.running
                      ? <span className="tag avail">→ {r.target}</span>
                      : <span className="okquiet">keep</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      {p.procs_note && <div className="hs-note" style={{ marginTop: 8 }}>Last change {p.procs_note_at ? relTime(p.procs_note_at) : ''}: {p.procs_note}.</div>}
      {pending && <div className="hs-note">{pending}</div>}
      {!pending && settling && rows.length > 0 && p.autoscale !== 'off' && <div className="hs-note">These counts started {relTime(p.procs_since!)}. Argus judges them once they have run for 6 hours, and checks that every raise lowered the load (it puts back one that didn't).</div>}
      <div className="hs-foot"><Button variant="ghost" onClick={onClose}>Close</Button></div>
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
    <div className="upd-cmd">
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6 }}>
        <span className="set-hint">{p.name} has no argus-updater sidecar, so it updates by hand. On its Docker host{p.version ? ` (it runs ${p.version})` : ''}:</span>
        <CopyButton text={cmd} variant="default" style={{ marginLeft: 'auto' }} />
      </div>
      <pre style={{ margin: 0, padding: '10px 12px', background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 8, overflowX: 'auto', fontSize: 12 }}><code>{cmd}</code></pre>
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
  // Newest probe-vm appliance + its OVA/qcow2/VHD download links, resolved server-side from GitHub
  // Releases so the deploy step can offer direct downloads instead of sending the user to GitHub.
  const [vmInfo, setVmInfo] = useState<{ version: string; page: string; images: { name: string; label: string; url: string; size: number }[] } | null>(null)
  useEffect(() => { fetch('/api/probes/vm-images').then((r) => (r.ok ? r.json() : null)).then((v) => { if (v) setVmInfo(v) }).catch(() => {}) }, [])

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
      <div className="dlg" role="dialog" aria-modal="true" style={{ maxWidth: 'min(800px, 94vw)', maxHeight: 'calc(100dvh - 32px)', display: 'flex', flexDirection: 'column' }}>
        <div className="dlg-title">Add a probe{step < 4 && <span style={{ color: 'var(--faint)', fontWeight: 400, fontSize: 12 }}> &middot; step {step} of 3</span>}</div>
        {err && <div style={{ color: 'var(--err)', fontSize: 13, marginBottom: 8 }}>{err}</div>}
        <div className="dlg-scroll">


        {step === 1 && (
          <div style={{ display: 'grid', gap: 14 }}>
            <label style={{ display: 'grid', gap: 4 }}>
              <span className="flabel">Site name</span>
              <input className="input" placeholder="e.g. site1" value={site} autoFocus onChange={(e) => setSite(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') next1() }} />
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
                        {CIDR_PREFIXES.map(([p, mask]) => <option key={p} value={p}>/{p} - {mask}</option>)}
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
              <div style={{ display: 'grid', gap: 15 }}>
                {/* Step 1 - pick ONE appliance for your hypervisor. */}
                <div style={{ display: 'grid', gap: 7 }}>
                  <div style={{ fontSize: 12.5, fontWeight: 600 }}>1 · Virtual appliance <span style={{ color: 'var(--accent)', fontWeight: 400 }}>{vmInfo?.version ? <>· {vmInfo.version} </> : null}- download <strong>one</strong> for your hypervisor</span></div>
                  {vmInfo && vmInfo.images.length > 0 ? (
                    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
                      {vmInfo.images.map((img) => (
                        <a key={img.name} href={img.url} target="_blank" rel="noopener noreferrer" download title={`Download ${img.name}`}
                          style={{ display: 'inline-flex', alignItems: 'center', gap: 6, padding: '7px 12px', border: '1px solid var(--border)', borderRadius: 8, background: 'var(--elevated)', color: 'var(--text)', fontSize: 12.5, fontWeight: 600, textDecoration: 'none' }}>
                          {img.label}<span style={{ color: 'var(--faint)', fontWeight: 400 }}>· {img.size >= 1073741824 ? `${(img.size / 1073741824).toFixed(1)} GB` : `${Math.round(img.size / 1048576)} MB`}</span>
                        </a>
                      ))}
                    </div>
                  ) : (
                    <a href="https://github.com/g-guglielmi/argus-probe/releases" target="_blank" rel="noopener noreferrer" style={{ fontSize: 12.5 }}>Download the appliance from GitHub releases →</a>
                  )}
                </div>
                {/* Step 2 - the seed ISO: required, one ISO works with any of the three formats. */}
                <div style={{ display: 'grid', gap: 7 }}>
                  <div style={{ fontSize: 12.5, fontWeight: 600 }}>2 · Seed ISO <span style={{ color: 'var(--accent)' }}>· required for zero-touch provisioning and static IP</span></div>
                  <p style={{ color: 'var(--muted)', fontSize: 12.5, margin: 0, lineHeight: 1.55 }}>Attach it to the VM as a CD/DVD before first boot - it carries this probe's token and network settings, and the <strong>same ISO works with any</strong> of the three formats above. (No ISO? Boot the appliance on DHCP and finish at its first-boot page instead.)</p>
                  <div><Button variant="primary" onClick={downloadSeedISO} disabled={seeding}>{seeding ? 'Building the ISO...' : 'Download seed ISO'}</Button></div>
                </div>
              </div>
            ) : (
              <>
                {method === 'unraid' ? (
                  <div style={{ display: 'grid', gap: 6 }}>
                    <div style={{ fontSize: 12.5, fontWeight: 600 }}>Add this as a container template on unRAID <span style={{ color: 'var(--accent)', fontWeight: 400 }}>· self-enrolls on first start</span></div>
                    <ol style={{ margin: 0, paddingLeft: '1.15rem', color: 'var(--muted)', fontSize: 12.5, lineHeight: 1.6, display: 'grid', gap: 3 }}>
                      <li>Copy the template below (the Copy button on the right).</li>
                      <li>On the unRAID flash, save it as <code style={{ background: 'var(--panel)', padding: '1px 4px', borderRadius: 4 }}>config/plugins/dockerMan/templates-user/argus-{created.proxy_name}.xml</code> (edit it over the flash share, or from the terminal).</li>
                      <li>In the <strong>Docker</strong> tab, click <strong>Add Container</strong>, pick <strong>argus-{created.proxy_name}</strong> from the <em>Template</em> list at the top, then <strong>Apply</strong>.</li>
                    </ol>
                    <p style={{ color: 'var(--faint)', fontSize: 11.5, margin: 0, lineHeight: 1.5 }}>The template already carries the enrollment token, core host and paths, so it enrolls itself on first start. Adjust the data path if you don't use <code style={{ background: 'var(--panel)', padding: '1px 4px', borderRadius: 4 }}>/mnt/user/appdata</code>. The token is single-use and expires, so deploy it before then.</p>
                  </div>
                ) : method === 'compose' ? (
                  <div style={{ display: 'grid', gap: 6 }}>
                    <div style={{ fontSize: 12.5, fontWeight: 600 }}>Deploy with Docker Compose <span style={{ color: 'var(--accent)', fontWeight: 400 }}>· self-enrolls on first start</span></div>
                    <ol style={{ margin: 0, paddingLeft: '1.15rem', color: 'var(--muted)', fontSize: 12.5, lineHeight: 1.6, display: 'grid', gap: 3 }}>
                      <li>Save the file below as <code style={{ background: 'var(--panel)', padding: '1px 4px', borderRadius: 4 }}>compose.yaml</code> on the host where you want the probe.</li>
                      <li>In that folder, run <code style={{ background: 'var(--panel)', padding: '1px 4px', borderRadius: 4 }}>docker compose up -d</code>.</li>
                    </ol>
                    <p style={{ color: 'var(--faint)', fontSize: 11.5, margin: 0, lineHeight: 1.5 }}>It brings up the proxy and its updater sidecar, which enroll on first start. The token is single-use and expires, so deploy it before then.</p>
                  </div>
                ) : (
                  <div style={{ display: 'grid', gap: 6 }}>
                    <div style={{ fontSize: 12.5, fontWeight: 600 }}>Run the container on your Docker host <span style={{ color: 'var(--accent)', fontWeight: 400 }}>· self-enrolls on first start</span></div>
                    <ol style={{ margin: 0, paddingLeft: '1.15rem', color: 'var(--muted)', fontSize: 12.5, lineHeight: 1.6, display: 'grid', gap: 3 }}>
                      <li>On any host with Docker, paste and run the command below (the Copy button on the right).</li>
                      <li>It creates the <code style={{ background: 'var(--panel)', padding: '1px 4px', borderRadius: 4 }}>argus-{created.proxy_name}</code> container, which enrolls itself on first start.</li>
                    </ol>
                    <p style={{ color: 'var(--faint)', fontSize: 11.5, margin: 0, lineHeight: 1.5 }}>Adjust the data path if you don't want the probe's state under the default. The token is single-use and expires, so run it before then.</p>
                  </div>
                )}
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


// NoteLine shows a sensor's note under it: the text, then who left it and when.
function NoteLine({ note, label }: { note: SensorNote; label?: string }) {
  return (
    <div className="snote" title="A note on this sensor: it goes out with its alerts and clears itself once the sensor is OK again">
      {kbIcon.edit}
      <span className="snote-t">{label ? <b>{label}: </b> : null}{note.text}</span>
      <span className="snote-by">{note.by ? `${note.by} \u00b7 ` : ''}{relTime(note.at)}</span>
    </div>
  )
}

// useNoteEditor writes the note on one or more sensors (a group's channels in trouble), asking for it
// in an in-app dialog: an empty one takes it off. removeNotes takes it off without asking.
function useNoteEditor() {
  const prompt = usePrompt()
  const toast = useToast()
  const save = async (keys: string[], text: string) => {
    for (const k of keys) {
      const res = await fetch(`/api/sensors/${encodeURIComponent(k)}/note`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ text }) }).catch(() => null)
      if (!res || !res.ok) { toast.error(res ? await errText(res, 'Could not save the note') : 'Could not save the note'); return false }
    }
    return true
  }
  const edit = async (keys: string[], sensor: string, current?: SensorNote) => {
    const text = await prompt({
      title: current ? 'Edit note' : 'Add note',
      message: `On ${sensor}. It shows with the sensor here and on the status pages, goes out with its alerts, reminders and RESOLVED, and clears itself once the sensor is OK again.`,
      label: 'Note', initial: current?.text || '', placeholder: 'ISP ticket 4471 open, technician on site at 14:00', confirmLabel: 'Save',
    })
    if (text === null) return
    if (await save(keys, text)) { toast.success(text.trim() ? 'Note saved.' : 'Note removed.'); fireDataRefresh() }
  }
  const remove = async (keys: string[]) => {
    if (await save(keys, '')) { toast.success('Note removed.'); fireDataRefresh() }
  }
  return { edit, remove }
}

// noteActions are the ⋯ menu items for a sensor's note: add one, or edit and remove the one there.
function noteActions(notes: { edit: (k: string[], s: string, c?: SensorNote) => void; remove: (k: string[]) => void }, keys: string[], sensor: string, current?: SensorNote): KAction[] {
  if (keys.length === 0) return []
  const out: KAction[] = [{ label: current ? 'Edit note' : 'Add note', icon: kbIcon.edit, onClick: () => notes.edit(keys, sensor, current) }]
  if (current) out.push({ label: 'Remove note', icon: kbIcon.trash, onClick: () => notes.remove(keys) })
  return out
}

const kbIcon = {
  pause: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><rect x="6" y="5" width="4" height="14" rx="1" /><rect x="14" y="5" width="4" height="14" rx="1" /></svg>,
  hide: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M2 12s3.5-7 10-7 10 7 10 7" /><path d="M3 3l18 18" /><path d="M9.5 9.5a3 3 0 0 0 4.2 4.2" /></svg>,
  resume: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M7 5l12 7-12 7z" /></svg>,
  show: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12Z" /><circle cx="12" cy="12" r="3" /></svg>,
  ack: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M22 11.2V12a10 10 0 1 1-5.9-9.1" /><path d="M22 4 12 14.5l-3-3" /></svg>,
  edit: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M12 20h9" /><path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4z" /></svg>,
  mute: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M13.7 21a2 2 0 0 1-3.4 0" /><path d="M18.6 13A18 18 0 0 1 18 8" /><path d="M6.3 6.3A5.9 5.9 0 0 0 6 8c0 7-3 9-3 9h14" /><path d="M18 8a6 6 0 0 0-9.3-5" /><path d="M2 2l20 20" /></svg>,
  unmute: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9" /><path d="M13.7 21a2 2 0 0 1-3.4 0" /></svg>,
  folder: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" /></svg>,
  folderOpen: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M3 8V6.2a1.8 1.8 0 0 1 1.8-1.8h3.9l2 2h6.5A1.8 1.8 0 0 1 20 8.2V9" /><path d="M3 9.2h17.8l-1.9 8.2a1.8 1.8 0 0 1-1.8 1.4H6.4a1.8 1.8 0 0 1-1.8-1.4z" /></svg>,
  gear: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="12" r="3" /><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" /></svg>,
  trash: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M4 7h16M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2M6 7l1 13a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1l1-13" /></svg>,
  discover: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><circle cx="12" cy="12" r="8.5" /><path d="M12 12l5-5" /><circle cx="12" cy="12" r="1.4" fill="currentColor" stroke="none" /><path d="M20.5 12a8.5 8.5 0 0 0-2.5-6" opacity="0.5" /></svg>,
}

// devIcon: the per-host tree glyph, keyed by the `icon` name the backend resolves from a host's device
// class (or a best-effort name guess for the unclassified fleet). Same hand-drawn stroke style as
// kbIcon. `hostGlyph` falls back to a generic device for any unknown name.
const devIcon: Record<string, JSX.Element> = {
  probe: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><circle cx="12" cy="12" r="2" /><path d="M16.2 7.8a6 6 0 0 1 0 8.4M7.8 16.2a6 6 0 0 1 0-8.4M19 5a10 10 0 0 1 0 14M5 19A10 10 0 0 1 5 5" /></svg>,
  device: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><rect x="3" y="4" width="18" height="12.5" rx="2" /><path d="M8.5 20.5h7M12 16.5v4" /></svg>,
  server: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><rect x="3" y="4" width="18" height="7" rx="1.6" /><rect x="3" y="13" width="18" height="7" rx="1.6" /><path d="M6.6 7.5h.01M6.6 16.5h.01M10 7.5h4M10 16.5h4" /></svg>,
  switch: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><rect x="2.5" y="7.5" width="19" height="9" rx="1.6" /><path d="M6 10.5h2.4M6 16.5v1.7M9.6 16.5v1.7M13.2 16.5v1.7M16.8 16.5v1.7" /></svg>,
  shield: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><path d="M12 3.2l7.5 2.7v5.6c0 4.3-3.2 7.4-7.5 8.8-4.3-1.4-7.5-4.5-7.5-8.8V5.9z" /></svg>,
  router: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><rect x="3" y="12.5" width="18" height="6.6" rx="1.6" /><path d="M6.6 15.8h.01M17.4 15.8h.01M12 12.5V8m0 0l-2.4 2.2M12 8l2.4 2.2" /></svg>,
  wifi: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><path d="M4.2 10.4a11 11 0 0 1 15.6 0M7.4 13.6a6.5 6.5 0 0 1 9.2 0" /><circle cx="12" cy="17.4" r="1.2" /></svg>,
  nas: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><ellipse cx="12" cy="6.2" rx="6.8" ry="2.6" /><path d="M5.2 6.2v11.6c0 1.4 3 2.6 6.8 2.6s6.8-1.2 6.8-2.6V6.2M5.2 12c0 1.4 3 2.6 6.8 2.6s6.8-1.2 6.8-2.6" /></svg>,
  cloud: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><path d="M7.4 18.5a4.3 4.3 0 0 1-.4-8.6 5.1 5.1 0 0 1 9.8-1.1 3.8 3.8 0 0 1 .4 7.6z" /></svg>,
  globe: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><circle cx="12" cy="12" r="8.4" /><path d="M3.6 12h16.8M12 3.6c2.5 2.4 2.5 14.4 0 16.8M12 3.6c-2.5 2.4-2.5 14.4 0 16.8" /></svg>,
  battery: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><rect x="3" y="8" width="15" height="8.5" rx="1.6" /><path d="M21 11.2v2.6" /><path d="M11 10.2l-2 3.2h2.6l-2 3.2" /></svg>,
  home: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><path d="M3.5 11.5 12 4l8.5 7.5" /><path d="M5.5 10v9.5h13V10" /><path d="M10 19.5v-5.5h4v5.5" /></svg>,
  vm: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7"><rect x="3" y="4.5" width="18" height="15" rx="2" /><rect x="6" y="7.5" width="5.2" height="4" rx="0.8" /><rect x="12.8" y="7.5" width="5.2" height="4" rx="0.8" /><rect x="6" y="13.5" width="5.2" height="4" rx="0.8" /></svg>,
}
const hostGlyph = (name?: string): JSX.Element => devIcon[name || 'device'] || devIcon.device

// Device classes Argus creates and manages itself (the per-site Probe health host): their class
// can't be changed from host settings.
const ARGUS_MANAGED_CLASSES = new Set(['probe'])

// fmtLatency renders an ICMP response time (milliseconds) compactly for the host row.
function fmtLatency(ms: number): string {
  if (ms >= 100) return Math.round(ms) + ' ms'
  if (ms >= 10) return ms.toFixed(1) + ' ms'
  return ms.toFixed(2) + ' ms'
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
  const menuRef = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ top: number; right: number } | null>(null)
  const [moved, setMoved] = useState(0) // bumped by a scroll or resize, to place the menu again
  function close() { setOpen(false); setDur(null); setCustom(false); setPos(null) }
  function toggleOpen() {
    if (open) { close(); return }
    setDur(null); setCustom(false); setPos(null); setOpen(true)
  }
  // The menu is portaled to <body> with fixed positioning, so a parent that clips its overflow (a host
  // card, a table's scroll wrapper) can't cut it off. Measured once it renders, and again when its
  // content changes (the duration list is taller than the actions): it opens below the button, above
  // when there's more room there (or the caller asks), and stays on screen.
  useLayoutEffect(() => {
    if (!open || !btnRef.current || !menuRef.current) return
    const r = btnRef.current.getBoundingClientRect()
    const h = menuRef.current.offsetHeight
    const gap = 5, margin = 8
    const below = window.innerHeight - r.bottom - gap - margin
    const above = r.top - gap - margin
    const goUp = up ? above >= h || above > below : h > below && above > below
    const top = Math.max(margin, Math.min(goUp ? r.top - gap - h : r.bottom + gap, window.innerHeight - margin - h))
    const right = Math.max(margin, window.innerWidth - r.right)
    setPos((p) => (p && p.top === top && p.right === right ? p : { top, right }))
  }, [open, dur, custom, actions.length, up, moved])
  // A fixed menu doesn't move with the page: a scroll or resize places it again by its button, and a
  // scroll that takes the button off screen closes it.
  useEffect(() => {
    if (!open) return
    const onScroll = (e: Event) => {
      if (e.target instanceof Node && menuRef.current?.contains(e.target)) return // the menu's own list
      const r = btnRef.current?.getBoundingClientRect()
      if (!r || r.bottom < 0 || r.top > window.innerHeight) close()
      else setMoved((n) => n + 1)
    }
    const onResize = () => setMoved((n) => n + 1)
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('resize', onResize)
    return () => { window.removeEventListener('scroll', onScroll, true); window.removeEventListener('resize', onResize) }
  }, [open])
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
      {open && createPortal(
        <>
          <div onClick={close} style={{ position: 'fixed', inset: 0, zIndex: 59 }} />
          <div ref={menuRef} className="menu" onClick={(e) => e.stopPropagation()}
            style={{ position: 'fixed', top: pos ? pos.top : 0, right: pos ? pos.right : 0, bottom: 'auto', visibility: pos ? 'visible' : 'hidden', zIndex: 60,
              minWidth: dur && custom ? 240 : 180, maxHeight: 'calc(100vh - 16px)', overflowY: 'auto' }}>
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
        </>, document.body)}
    </span>
  )
}

// Focus is the PRTG-style drill-down state layered over the expandable tree: from the whole tree
// (root) you can narrow to a group node, a single host, or a single sensor. `path` is the full group
// path (Zabbix nests groups by name with "/", e.g. "site1/Network"), so it can address any node in the
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

function MonitoringView({ role, target, homeSignal, onNavigate, advanced }: { role: string; target: { hostId?: string; itemId?: string; itemName?: string; groupPath?: string; editHost?: string; n: number } | null; homeSignal: number; onNavigate: (hostId: string | null, itemId: string | null, group?: string | null, push?: boolean, edit?: string | null) => void; advanced: boolean }) {
  const confirm = useConfirm()
  const [hosts, setHosts] = useState<Host[]>([])
  const [groups, setGroups] = useState<Group[]>([])
  const [proxies, setProxies] = useState<Proxy[]>([])
  const [creating, setCreating] = useState(false) // "+ New group" inline band open
  const [addingDevice, setAddingDevice] = useState(false) // "+ Add device" inline band open (admin)
  const [siteEdit, setSiteEdit] = useState(false) // the site info editor (siteinfo.go)
  const [classes, setClasses] = useState<DeviceClass[]>([]) // device-class catalog for the attach band
  const [gAction, setGAction] = useState<{ id: string; mode: 'rename' | 'delete' } | null>(null) // per-group rename/delete band
  const [newSubPath, setNewSubPath] = useState<string | null>(null) // group path under which a "New subgroup" band is open
  const [focus, setFocus] = useState<Focus>({ level: 'root' })
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  // Tree collapse/expand state persists for the browser session (sessionStorage): remembered across
  // reloads and navigation, cleared when the tab/session ends - not forever. The first visit of a
  // session starts fully collapsed (default applied once the group list arrives, below).
  const [collapsed, setCollapsed] = useState<Set<string>>(() => { try { const s = sessionStorage.getItem('argus.tree.collapsed'); if (s) return new Set<string>(JSON.parse(s)) } catch { /* ignore */ } return new Set() })
  const collapseReady = useRef<boolean>((() => { try { return sessionStorage.getItem('argus.tree.collapsed') != null } catch { return false } })())
  const [openHost, setOpenHost] = useState<string | null>(() => { try { return sessionStorage.getItem('argus.tree.openhost') } catch { return null } })
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
  const toast = useToast()
  // Tags (the toolbar's filter) and the selection for bulk actions.
  const [tagList, reloadTags] = useTags()
  const [tagFilter, setTagFilter] = useState<string[]>([])
  const [selecting, setSelecting] = useState(false)
  const [sel, setSel] = useState<Set<string>>(() => new Set())
  const [bulk, setBulk] = useState<null | 'groups' | 'probe' | 'tags' | 'thresholds' | 'maint'>(null)
  const [bulkBusy, setBulkBusy] = useState(false)
  const [bulkReason, setBulkReason] = useState('')
  const [maintSites, setMaintSites] = useState<string[]>([])
  function toggleSel(ids: string[], on: boolean) { setSel((cur) => { const n = new Set(cur); for (const id of ids) { if (on) n.add(id); else n.delete(id) } return n }) }
  function endSelecting() { setSelecting(false); setSel(new Set()); setBulk(null); setBulkReason('') }
  // runBulk acts on the selected hosts; the ones that worked leave the selection, the failed stay.
  async function runBulk(action: string, extra: object, what: string) {
    setBulkBusy(true)
    const ids = [...sel]
    const r = await postBulk('/api/bulk/hosts', { action, host_ids: ids, ...extra }, bulkReason)
    setBulkBusy(false)
    bulkToast(toast, r, what, ['host', 'hosts'])
    if (typeof r === 'string') return
    const failed = new Set(r.failed.map((f) => f.id))
    setSel(new Set(ids.filter((id) => failed.has(id))))
    if (!failed.size) { setBulk(null); setBulkReason('') }
    load(); reloadTags(); fireDataRefresh()
  }
  // Per-host ICMP latency sparklines: batch the icmppingsec item ids from the host list, auto-refreshed.
  const icmpSparks = useSparks(hosts.map((h) => h.icmp_item || '').filter(Boolean))

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
  // First session visit (nothing persisted yet): start with every group collapsed. Runs once, after
  // the group list first arrives; later refreshes are ignored (collapseReady is latched).
  useEffect(() => {
    if (collapseReady.current || groups.length === 0) return
    setCollapsed(new Set(groups.map((g) => g.name)))
    collapseReady.current = true
  }, [groups])
  // Persist the collapse + open-host state for the session (skip the transient empty set before the
  // default above has been applied).
  useEffect(() => { if (collapseReady.current) { try { sessionStorage.setItem('argus.tree.collapsed', JSON.stringify([...collapsed])) } catch { /* ignore */ } } }, [collapsed])
  useEffect(() => { try { if (openHost == null) sessionStorage.removeItem('argus.tree.openhost'); else sessionStorage.setItem('argus.tree.openhost', openHost) } catch { /* ignore */ } }, [openHost])
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
  // the sensor chart via autoOpenItem. hosts.length is a dep so a link that arrived before the hosts
  // loaded still applies once they do - but each deep-link is LATCHED on its nav id (target.n), so an
  // incidental hosts reload (Add device, the 30s poll) never re-applies a now-stale target and yanks
  // the user out of a group they've since drilled into by hand (drilling updates the URL, not target).
  const appliedTarget = useRef<number | undefined>(undefined)
  useEffect(() => {
    if (!target || appliedTarget.current === target.n) return
    // A group deep-link (?group=…) focuses the group node directly - no host needed.
    if (target.groupPath && !target.hostId) {
      const p = target.groupPath
      setCollapsed((c) => { const n = new Set(c); let a = ''; for (const seg of p.split('/')) { a = a ? a + '/' + seg : seg; n.delete(a) } return n })
      setFocus({ level: 'group', path: p })
      setSettingsHost(target.editHost || null)
      appliedTarget.current = target.n
      return
    }
    const hid = target.hostId
    if (!hid) { setSettingsHost(target.editHost || null); appliedTarget.current = target.n; return }
    const h = hosts.find((x) => x.id === hid)
    if (!h) return // host not loaded yet - retry when hosts arrive (don't latch until it's applied)
    const p = (h.groups && h.groups.length ? h.groups : ['Ungrouped'])[0]
    // Un-collapse the target group and all its ancestor paths so the host is reachable in the tree.
    setCollapsed((c) => { const n = new Set(c); let a = ''; for (const seg of p.split('/')) { a = a ? a + '/' + seg : seg; n.delete(a) } return n })
    setOpenHost(p + '::' + hid)
    setFocus(target.itemId
      ? { level: 'sensor', path: p, hostId: hid, itemId: target.itemId, itemName: target.itemName }
      : { level: 'host', path: p, hostId: hid })
    setSettingsHost(target.editHost || null)
    appliedTarget.current = target.n
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
  // The host-settings dialog is URL-backed (&edit=<hostid>) on top of the current focus params, so
  // a reload restores it and Back closes it. Any drill (the calls below carry no edit) drops it.
  function navEdit(edit: string | null) {
    const host = focus.level === 'host' || focus.level === 'sensor' ? focus.hostId : null
    const item = focus.level === 'sensor' ? focus.itemId : null
    const group = focus.level === 'group' ? focus.path : null
    onNavigate(host, item ?? null, group, true, edit)
  }
  function openSettings(hostId: string) { setEditGroupsHost(null); setSettingsHost(hostId); navEdit(hostId) }
  function closeSettings() { setSettingsHost(null); navEdit(null) }

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
  // Run the host's LLD rules now (Zabbix "execute now"), so new per-instance sensors - disks, NICs,
  // ports - appear after one sweep instead of at the rules' next scheduled run.
  async function discoverNow(h: Host) {
    setBusyId(h.id)
    const res = await fetch(`/api/hosts/${h.id}/discover`, { method: 'POST' }).catch(() => null)
    setBusyId(null)
    if (!res) { toast.error('Could not start discovery'); return }
    if (!res.ok) { toast.error(await errText(res, 'Could not start discovery')); return }
    const d = await res.json().catch(() => ({ triggered: 0 }))
    toast.success(d.triggered > 0
      ? `Discovery started (${d.triggered} rule${d.triggered === 1 ? '' : 's'}) - new sensors appear after the sweep`
      : 'This host has no discovery rules')
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
  const treeHosts = tagFilter.length ? hosts.filter((h) => (h.tags || []).some((t) => tagFilter.includes(t.name))) : hosts
  for (const h of treeHosts) {
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
  // A site's Probe health host is pinned first by default: ahead of the alphabetical order, and ahead
  // of a saved order that doesn't list it yet. Once an admin moves it, the saved order wins.
  const isProbeHost = (h?: Host) => h?.class_id === 'probe'
  function applyOrder<T>(items: T[], orderKey: (t: T) => string, alphaKey: (t: T) => string, ordered?: string[], pinned?: (t: T) => boolean): T[] {
    const pin = (t: T) => (pinned && pinned(t) ? 0 : 1)
    const base = [...items].sort((a, b) => pin(a) - pin(b) || alphaKey(a).localeCompare(alphaKey(b)))
    if (!ordered || ordered.length === 0) return base
    const pos = new Map(ordered.map((id, i) => [id, i]))
    const at = (t: T) => pos.get(orderKey(t)) ?? (pin(t) === 0 ? -1 : Infinity)
    return base.sort((a, b) => at(a) - at(b))
  }
  const orderTree = (ns: GNode[], scope: string) => {
    const sorted = applyOrder(ns, (n) => n.path, (n) => n.name, orderOf(scope, 'group'))
    ns.length = 0; ns.push(...sorted)
    for (const n of ns) { n.hosts = applyOrder(n.hosts, (h) => h.id, (h) => h.name, orderOf(n.path, 'host'), isProbeHost); orderTree(n.children, n.path) }
  }
  orderTree(roots, '')
  // One ordered list of a parent's children - its direct hosts and its subgroups together - so a manual
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
    const at = (x: Sibling) => pos.get(x.key) ?? (isProbeHost(x.host) ? -1 : Infinity)
    return [...base].sort((a, b) => at(a) - at(b))
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
  // With a tag filter on, a group with none of its hosts left drops out of the tree.
  const pruneEmpty = (ns: GNode[]): boolean => {
    for (let i = ns.length - 1; i >= 0; i--) { const keep = pruneEmpty(ns[i].children) || ns[i].hosts.length > 0; if (!keep) ns.splice(i, 1) }
    return ns.length > 0
  }
  if (tagFilter.length) pruneEmpty(roots)

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
      message: `Hide “${node.path}” from the monitoring tree? The group stays in Zabbix - use “Show hidden” in the toolbar to bring it back.`,
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
  // nodeShade colours a group's dot: the worst of what nobody has acknowledged, else "acked" while an
  // acknowledged problem is all that's left.
  function nodeShade(hs: Host[]): string {
    const live = hs.filter((h) => !h.paused && !h.hidden)
    const worst = nodeWorst(live.filter((h) => !h.acked))
    return worst === 'ok' && live.some((h) => h.acked) ? 'acked' : worst
  }
  function toggleNode(path: string) { setCollapsed((c) => { const n = new Set(c); n.has(path) ? n.delete(path) : n.add(path); return n }) }

  const focusHostId = focus.level === 'host' || focus.level === 'sensor' ? focus.hostId : null
  const focusItemId = focus.level === 'sensor' ? focus.itemId : null
  const focusHost = focusHostId ? hosts.find((x) => x.id === focusHostId) : undefined
  const focusHostName = focusHost?.name || focusHostId || ''
  const indent = (d: number) => 16 + d * 18
  // Vertical indent guides: one full-height hairline per ancestor level, overlaid (absolute) so they
  // don't shift the row's own padding. A row at depth d draws guides at the left edge of each shallower
  // level, connecting a group header to the rows nested under it.
  const guides = (d: number) => Array.from({ length: d }, (_, i) => <span key={i} className="tguide" style={{ left: 16 + i * 18 }} />)

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
    const shade = hostShade(h)
    return (
      <div className="host" key={key}>
        <div className={'host-head' + (selecting && sel.has(h.id) ? ' selected' : '')} style={{ paddingLeft: indent(depth) }} onClick={() => { const next = hopen ? null : key; setOpenHost(next); onNavigate(next ? h.id : null, null) }}>
          {guides(depth)}
          <div className="c-name">
            {selecting && <input type="checkbox" className="tsel" checked={sel.has(h.id)} onClick={(e) => e.stopPropagation()} onChange={(e) => toggleSel([h.id], e.target.checked)} aria-label={`Select ${h.name}`} />}
            <svg className={'chev' + (hopen ? ' open' : '')} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M9 6l6 6-6 6" /></svg>
            <span className="dev-ico" title={h.class_id || undefined}>{hostGlyph(h.icon)}<span className={'dev-badge' + (needsEye(h) ? ' pulse' : '')} style={{ '--dot': dotColor(h.paused, h.hidden, shade) } as CSSProperties} /></span>
            <span className="hn lnk-host" onClick={(e) => { e.stopPropagation(); drillHost(path, h.id) }}>{h.name}</span>
            {h.paused && <span className="kind" style={{ color: PAUSED_BLUE }}>· paused {untilLabel(h.paused_until)}</span>}
            {h.hidden && <span className="kind" style={{ color: HIDDEN_GREY }}>· hidden {untilLabel(h.hidden_until)}</span>}
            {h.maintenance && <span className="kind maint" title={`${h.maintenance.name}: alerts wait until ${fmtWhen(h.maintenance.until)}`}>· maintenance</span>}
            {h.held_behind && <span className="kind held" title={`${h.held_behind} is down: this host's alerts wait until it is back`}>· held: behind {h.held_behind}</span>}
            {!h.paused && !h.hidden && h.problems > 0 && <span className={'probpill' + (shade === 'acked' ? ' acked' : h.state === 'warning' ? ' warn' : '')} title={`${h.problems} problem${h.problems === 1 ? '' : 's'}${shade === 'acked' ? ', acknowledged' : ''}`}>{h.problems}</span>}
            <TagList tags={h.tags} />
          </div>
          <div className="c-graph">
            {h.icmp_item && icmpSparks[h.icmp_item] && icmpSparks[h.icmp_item].length > 1 && (
              <span className="hspark"><Spark values={icmpSparks[h.icmp_item]} color={shade === 'ok' ? 'var(--accent)' : (STATE_VAR[shade] || 'var(--accent)')} width={168} /></span>
            )}
          </div>
          <div className="c-val" title="ICMP response time">
            {reorder ? orderArrows(sibIds, index, path, 'sibling') : (typeof h.icmp_ms === 'number' ? <span className="hms">{fmtLatency(h.icmp_ms)}</span> : null)}
          </div>
          <div className="c-act">
            {canPause && !reorder && (
              <Kebab disabled={busyId === h.id} actions={[
                h.paused ? { label: 'Resume', icon: kbIcon.resume, onClick: () => clearHostState(h, 'pause') } : { label: 'Pause', icon: kbIcon.pause, onPick: (s) => setHostState(h, 'pause', s) },
                h.hidden ? { label: 'Show', icon: kbIcon.show, onClick: () => clearHostState(h, 'hide') } : { label: 'Hide', icon: kbIcon.hide, onPick: (s) => setHostState(h, 'hide', s) },
                { sep: true, label: '' },
                { label: 'Settings…', icon: kbIcon.gear, onClick: () => openSettings(h.id) },
                { label: 'Edit groups…', icon: kbIcon.folder, onClick: () => { setSettingsHost(null); setEditGroupsHost((cur) => (cur === h.id ? null : h.id)) } },
                { label: 'Discover now', icon: kbIcon.discover, onClick: () => discoverNow(h) },
              ]} />
            )}
          </div>
        </div>
        {editGroupsHost === h.id && <GroupEditor current={h.groups || []} groups={groups} onSave={(ids) => setHostGroups(h.id, ids)} onCancel={() => setEditGroupsHost(null)} />}
        {hopen && <div className="host-body" style={{ paddingLeft: indent(depth) }}><HostItems hostId={h.id} canPause={canPause} hostPaused={h.paused} hostHidden={h.hidden} maintenance={h.maintenance} showAll={showAllEff} autoOpenItem={target && target.hostId === h.id ? target.itemId : undefined} onlyItem={focus.level === 'sensor' && focus.hostId === h.id ? focusItemId ?? undefined : undefined} onDrillSensor={(itemId, itemName) => drillSensor(path, h.id, itemId, itemName)} onItemName={(itemId, itemName) => setFocus((f) => (f.level === 'sensor' && f.itemId === itemId && !f.itemName ? { ...f, itemName } : f))} onNavigate={onNavigate} onOpenSettings={canPause ? () => openSettings(h.id) : undefined} heldBehind={h.held_behind} /></div>}
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
          {guides(depth)}
          <div className="c-name">
            {selecting && sub.length > 0 && <input type="checkbox" className="tsel" checked={sub.every((x) => sel.has(x.id))} onClick={(e) => e.stopPropagation()} onChange={(e) => toggleSel(sub.map((x) => x.id), e.target.checked)} aria-label={`Select every host in ${node.path}`} />}
            <svg className={'chev' + (expanded ? ' open' : '')} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M9 6l6 6-6 6" /></svg>
            <span className="fold-ico">{expanded ? kbIcon.folderOpen : kbIcon.folder}</span>
            <span className="name lnk-host" onClick={(e) => { e.stopPropagation(); drillGroup(node.path) }}>{node.name}</span>
            {isHidden && <span className="tag-hidden">hidden</span>}
            <span className="loc">{sub.length} host{sub.length === 1 ? '' : 's'}</span>
          </div>
          <div className="c-graph" />
          <div className="c-val">
            {reorder ? orderArrows(sibIds, index, scope, 'sibling') : <span className={'grpdot' + (sub.some(needsEye) ? ' pulse' : '')} style={{ '--dot': STATE_VAR[nodeShade(sub)] || 'var(--muted)' } as CSSProperties} />}
          </div>
          <div className="c-act">
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
  const focusSite = focus.level === 'group' && !focus.path.includes('/') ? focus.path : ''

  return (
    <div className="panel">
      <div className="phead">
        <PanelTitle eyebrow="Watch">Sites &amp; hosts</PanelTitle>
        <span className="hint">{(showHiddenEff ? groups.length : groups.filter((g) => !hidden.has(g.name)).length)} group{groups.length === 1 ? '' : 's'}{!showHiddenEff && hidden.size > 0 ? ` (${hidden.size} hidden)` : ''} · {hosts.length} host{hosts.length === 1 ? '' : 's'}</span>
        {focus.level !== 'sensor' && (
          <div className="tools">
            {canPause && focus.level !== 'host' && !reorder && <button className="btn primary" onClick={() => { setError(null); setCreating((v) => !v) }}>+ New group</button>}
            {role === 'admin' && (focus.level === 'root' || focus.level === 'group') && !reorder && <button className="btn primary" onClick={() => { setError(null); setCreating(false); setAddingDevice((v) => !v) }}>+ Add device</button>}
            {/* Desktop: the secondary controls inline. Phone: the same actions in a ⋯ menu (plus a visible
                Done while reordering), so the toolbar stays one row on a narrow card. */}
            <span className="tools-desktop">
              {focus.level !== 'host' && tagList.length > 0 && !reorder && <TagPicker tags={tagList} value={tagFilter} onChange={setTagFilter} allLabel="All tags" />}
              {canPause && focus.level !== 'host' && !reorder && <button className={'btn' + (selecting ? ' on' : '')} onClick={() => (selecting ? endSelecting() : setSelecting(true))}>{selecting ? 'Done' : 'Select'}</button>}
              {focusSite && canPause && !reorder && <button className="btn" onClick={() => setSiteEdit(true)}>Edit site info</button>}
              {focus.level !== 'host' && !reorder && <button className="btn" onClick={() => exportHosts(treeHosts, proxies)}>Export CSV</button>}
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
            {focus.level !== 'host' && (
              <span className="tools-mobile">
                <Kebab actions={[
                  ...(canPause ? [{ label: selecting ? 'Done selecting' : 'Select hosts', onClick: () => (selecting ? endSelecting() : setSelecting(true)) }] : []),
                  ...(focusSite && canPause ? [{ label: 'Edit site info', onClick: () => setSiteEdit(true) }] : []),
                  { label: 'Export CSV', onClick: () => exportHosts(treeHosts, proxies) },
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
      {creating && <GroupNameBand
        prefix={focus.level === 'group' ? focus.path + '/' : undefined}
        placeholder={focus.level === 'group' ? 'Subgroup name (use / for deeper nesting)' : 'New group name (use / for nesting, e.g. site1/Network)'}
        confirmLabel="Create" onConfirm={(name) => createGroup(name)} onCancel={() => setCreating(false)} />}
      {addingDevice && <AddDeviceBand classes={classes} groups={groups} proxies={proxies} defaultSite={focus.level === 'group' ? focus.path : ''} onCancel={() => setAddingDevice(false)} onCreated={() => { setAddingDevice(false); setError(null); load(); fireDataRefresh() }} />}
      {settingsHost && <HostSettingsModal hostId={settingsHost} hostName={hosts.find((h) => h.id === settingsHost)?.name} canEdit={canPause} isAdmin={role === 'admin'} onClose={closeSettings} onSaved={() => { closeSettings(); load(); fireDataRefresh() }} />}
      {focusSite && <SitePanel site={focusSite} canEdit={canPause} editing={siteEdit} onEditDone={() => setSiteEdit(false)} />}
      {loading && <Skeleton rows={5} cols={3} />}
      {error && <div style={{ padding: '0.9rem 16px', color: 'var(--err)' }}>{error}</div>}
      {!loading && !error && hosts.length === 0 && <EmptyState icon={ic.monitoring} title="No hosts yet" text="Hosts monitored in Zabbix appear here, grouped by site. If you expected some, check the Zabbix connection in Settings." />}
      <div className="tree">
        {focus.level === 'root' && (() => { const kids = mergedChildren([], roots, ''); const keys = kids.map((k) => k.key); return kids.map((k, i) => renderNode(k.group!, 0, keys, i, '')) })()}
        {focus.level === 'group' && (focusNode ? renderNode(focusNode, 0) : <div style={{ padding: '0.9rem 16px', color: 'var(--muted)' }}>This group no longer exists.</div>)}
        {(focus.level === 'host' || focus.level === 'sensor') && (focusHost ? renderHost(focusHost, focus.path, 0) : null)}
      </div>
      {selecting && sel.size > 0 && (() => {
        const picked = hosts.filter((h) => sel.has(h.id))
        const oneClass = picked.length > 0 && picked.every((h) => h.class_id && h.class_id === picked[0].class_id)
        return (
          <BulkBar count={sel.size} noun={['host', 'hosts']} onClear={() => setSel(new Set())}>
            <DurationButton up label="Acknowledge" disabled={bulkBusy} onPick={(sec) => runBulk('ack', { duration_seconds: sec ?? 0 }, 'Acknowledged the problems of')} />
            {picked.some((h) => !h.paused) && <DurationButton up label="Pause" disabled={bulkBusy} onPick={(sec) => runBulk('pause', { duration_seconds: sec ?? 0 }, 'Paused')} />}
            {picked.some((h) => h.paused) && <Button variant="ghost" className="compact" disabled={bulkBusy} onClick={() => runBulk('resume', {}, 'Resumed')}>Resume</Button>}
            {picked.some((h) => !h.hidden) && <DurationButton up label="Hide" disabled={bulkBusy} onPick={(sec) => runBulk('hide', { duration_seconds: sec ?? 0 }, 'Hid')} />}
            {picked.some((h) => h.hidden) && <Button variant="ghost" className="compact" disabled={bulkBusy} onClick={() => runBulk('show', {}, 'Showed again')}>Show</Button>}
            <Button variant="ghost" className="compact" disabled={bulkBusy} onClick={() => { fetch('/api/me/notify/sites').then((r) => (r.ok ? r.json() : [])).then((x) => setMaintSites(x || [])).catch(() => {}); setBulk('maint') }}>Maintenance…</Button>
            <Button variant="ghost" className="compact" disabled={bulkBusy} onClick={() => setBulk('groups')}>Move to group…</Button>
            <Button variant="ghost" className="compact" disabled={bulkBusy} onClick={() => setBulk('probe')}>Probe…</Button>
            <Button variant="ghost" className="compact" disabled={bulkBusy} onClick={() => setBulk('tags')}>Tags…</Button>
            <Button variant="ghost" className="compact" disabled={bulkBusy || !oneClass} title={oneClass ? undefined : 'Thresholds are set on hosts of one class at a time'} onClick={() => setBulk('thresholds')}>Thresholds…</Button>
          </BulkBar>
        )
      })()}
      {bulk === 'groups' && <BulkGroupsDialog groups={groups} count={sel.size} busy={bulkBusy} reason={bulkReason} setReason={setBulkReason} onClose={() => setBulk(null)} onApply={(ids) => runBulk('groups', { group_ids: ids }, 'Moved')} />}
      {bulk === 'probe' && <BulkProbeDialog proxies={proxies} count={sel.size} busy={bulkBusy} reason={bulkReason} setReason={setBulkReason} onClose={() => setBulk(null)} onApply={(by, pid) => runBulk('probe', { monitored_by: by, proxy_id: pid }, 'Moved')} />}
      {bulk === 'tags' && <BulkTagsDialog tags={tagList} count={sel.size} busy={bulkBusy} reason={bulkReason} setReason={setBulkReason} onClose={() => setBulk(null)} onApply={(add, remove) => runBulk('tags', { add, remove }, 'Changed the tags of')} />}
      {bulk === 'thresholds' && sel.size > 0 && <BulkThresholdsDialog hostId={[...sel][0]} count={sel.size} busy={bulkBusy} reason={bulkReason} setReason={setBulkReason} onClose={() => setBulk(null)} onApply={(macros) => runBulk('thresholds', { macros }, 'Changed thresholds on')} />}
      {bulk === 'maint' && <MaintenanceDialog initial={null} sites={maintSites} hosts={hosts} presetHosts={[...sel]} onCancel={() => setBulk(null)} onSaved={() => { setBulk(null); toast.success('Maintenance window created'); load(); fireDataRefresh() }} />}
    </div>
  )
}

// BulkGroupsDialog puts the selected hosts in the groups picked (replacing the ones they're in).
function BulkGroupsDialog({ groups, count, busy, reason, setReason, onApply, onClose }: { groups: Group[]; count: number; busy: boolean; reason: string; setReason: (v: string) => void; onApply: (ids: string[]) => void; onClose: () => void }) {
  const [pick, setPick] = useState<string[]>([])
  const byName = new Map(groups.map((g) => [g.name, g.id]))
  return (
    <BulkDialog title={`Move ${count} host${count === 1 ? '' : 's'}`} note="They leave the groups they're in now and go into the ones picked here." busy={busy} applyLabel="Move" canApply={pick.length > 0} reason={reason} setReason={setReason} onClose={onClose} onApply={() => onApply(pick.map((n) => byName.get(n)!).filter(Boolean))}>
      <div className="chan-field"><span className="flabel">Groups</span><SitePicker options={groups.map((g) => g.name).sort()} value={pick} onChange={setPick} noAll placeholder="Pick groups" noun="groups" /></div>
    </BulkDialog>
  )
}

// BulkProbeDialog moves the selected hosts to the core server or a probe.
function BulkProbeDialog({ proxies, count, busy, reason, setReason, onApply, onClose }: { proxies: Proxy[]; count: number; busy: boolean; reason: string; setReason: (v: string) => void; onApply: (monitoredBy: number, proxyId: string) => void; onClose: () => void }) {
  const [by, setBy] = useState(proxies.length ? 1 : 0)
  const [pid, setPid] = useState(proxies[0]?.id || '')
  return (
    <BulkDialog title={`Monitor ${count} host${count === 1 ? '' : 's'} from…`} busy={busy} applyLabel="Move" canApply={by === 0 || !!pid} reason={reason} setReason={setReason} onClose={onClose} onApply={() => onApply(by, by ? pid : '')}>
      <div className="hs-mon">
        <span className="hs-monlabel">Monitored by</span>
        <div className="seg"><button className={by === 0 ? 'on' : ''} onClick={() => setBy(0)}>Server</button><button className={by === 1 ? 'on' : ''} onClick={() => setBy(1)} disabled={!proxies.length}>Probe</button></div>
        {by === 1 && <Select value={pid} onChange={(e) => setPid(e.target.value)}>{proxies.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</Select>}
      </div>
    </BulkDialog>
  )
}

// BulkTagsDialog adds tags to the selected hosts and takes others off.
function BulkTagsDialog({ tags, count, busy, reason, setReason, onApply, onClose }: { tags: TagInfo[]; count: number; busy: boolean; reason: string; setReason: (v: string) => void; onApply: (add: string[], remove: string[]) => void; onClose: () => void }) {
  const [add, setAdd] = useState<string[]>([])
  const [remove, setRemove] = useState<string[]>([])
  return (
    <BulkDialog title={`Tags of ${count} host${count === 1 ? '' : 's'}`} note={tags.length ? 'Tags a host gets from its probe stay: change those on the Probes page.' : 'No tags yet: make them in Settings, Tags.'} busy={busy} applyLabel="Apply" canApply={add.length + remove.length > 0} reason={reason} setReason={setReason} onClose={onClose} onApply={() => onApply(add, remove)}>
      <div className="chan-row">
        <div className="chan-field chan-wide"><span className="flabel">Add</span><TagPicker tags={tags.filter((t) => !remove.includes(t.name))} value={add} onChange={setAdd} noAll placeholder="None" /></div>
        <div className="chan-field chan-wide"><span className="flabel">Remove</span><TagPicker tags={tags.filter((t) => !add.includes(t.name))} value={remove} onChange={setRemove} noAll placeholder="None" /></div>
      </div>
    </BulkDialog>
  )
}

// BulkThresholdsDialog sets per-host thresholds on hosts of one class: a filled field sets it on each,
// "Use the default" takes their own value off, the rest stay as they are.
function BulkThresholdsDialog({ hostId, count, busy, reason, setReason, onApply, onClose }: { hostId: string; count: number; busy: boolean; reason: string; setReason: (v: string) => void; onApply: (macros: Record<string, string>) => void; onClose: () => void }) {
  const [fields, setFields] = useState<ThresholdField[] | null>(null)
  const [vals, setVals] = useState<Record<string, string>>({})
  useEffect(() => { fetch(`/api/hosts/${hostId}/config`).then((r) => (r.ok ? r.json() : null)).then((c: HostCfg | null) => setFields(c?.thresholds || [])).catch(() => setFields([])) }, [hostId])
  const set = Object.keys(vals).length
  return (
    <BulkDialog title={`Thresholds of ${count} host${count === 1 ? '' : 's'}`} note="Fill only what should change; empty fields stay as each host has them." busy={busy} applyLabel="Apply" canApply={set > 0} reason={reason} setReason={setReason} onClose={onClose} onApply={() => onApply(vals)}>
      {fields === null ? <span className="muted">Loading…</span> : fields.length === 0 ? <span className="muted">This class has no thresholds to set.</span> : (
        <div className="thr-rows">
          {fields.map((f) => (
            <div className="thr-row" key={f.macro}>
              <span className="thr-row-label">{f.label}{f.unit ? ` (${f.unit})` : ''}</span>
              <div className="thr-row-input">
                <input className="input" inputMode="decimal" placeholder={vals[f.macro] === '' ? `default ${f.default}${f.unit || ''}` : 'unchanged'} value={vals[f.macro] ?? ''}
                  onChange={(e) => { const v = e.target.value; setVals((cur) => { const n = { ...cur }; if (v.trim() === '') delete n[f.macro]; else n[f.macro] = v; return n }) }} />
                <button type="button" className={'btn ghost thr-reset' + (vals[f.macro] === '' ? ' on' : '')} onClick={() => setVals((cur) => { const n = { ...cur }; if (n[f.macro] === '') delete n[f.macro]; else n[f.macro] = ''; return n })}>{vals[f.macro] === '' ? 'Back to unchanged' : 'Use the default'}</button>
              </div>
              <span className="thr-row-def">{vals[f.macro] === '' ? `Each host goes back to the default of ${f.default}${f.unit || ''}` : `Default ${f.default}${f.unit || ''}`}</span>
            </div>
          ))}
        </div>
      )}
    </BulkDialog>
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
  const [macroVals, setMacroVals] = useState<Record<string, string>>({}) // class-declared per-host macros
  const [macroTouched, setMacroTouched] = useState<Set<string>>(new Set()) // macros the user edited (stop auto-deriving them)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)

  const cls = classes.find((c) => c.id === classId)
  // Settings-only macros (like XCP-NG's ignored-VMs list) need discovered data to pick from, so the
  // wizard skips them - they appear in host settings once the host exists.
  const classMacros = (cls?.macros || []).filter((ms) => !ms.settings_only)
  // Prerequisite steps some classes carry (e.g. Ugreen needs a Zabbix agent container on the NAS):
  // fill {host} in the copyable command with the device name the user is typing.
  const setupCmd = cls?.setup?.command ? cls.setup.command.replace('{host}', name.trim() || '<device-name>') : ''
  // Alphabetical by label, with "Ping only" (base) pinned first as the universal default.
  const classOptions = useMemo(() => classes.filter((c) => !c.internal).map((c) => ({ value: c.id, label: c.label }))
    .sort((a, b) => (a.value === 'base' ? -1 : b.value === 'base' ? 1 : a.label.localeCompare(b.label))), [classes])
  const needsSnmp = cls?.iface === 'snmp'
  const offersHttp = !!cls?.offers_http
  const proxyName = proxyId === '' ? 'the core server' : (proxies.find((p) => p.id === proxyId)?.name || 'the proxy')
  const canInherit = needsSnmp && !!proxySnmp?.set
  const showSnmpFields = needsSnmp && proxySnmp !== null && (!canInherit || snmpOverride)

  // For an SNMP class, check whether the chosen collector has an SNMP default to inherit, so the form
  // can hide the credential fields (the common case) and only ask when overriding or when none is set.
  // The core server's own default lives under proxy id "0" (Settings, Core SNMP default).
  useEffect(() => {
    if (!needsSnmp) { setProxySnmp({ set: false }); return }
    setProxySnmp(null)
    const id = proxyId === '' ? '0' : proxyId
    fetch(`/api/proxies/${encodeURIComponent(id)}/snmp`).then((r) => (r.ok ? r.json() : { set: false })).then((d) => setProxySnmp({ set: !!d.set })).catch(() => setProxySnmp({ set: false }))
  }, [needsSnmp, proxyId])

  // Changing the class starts its macros fresh (so a derived URL is re-derived, not carried over).
  useEffect(() => { setMacroTouched(new Set()) }, [classId])
  // Auto-fill a host-addressed URL macro (AdGuard/HA admin URLs are usually just the host) from the
  // IP/DNS the form already has, until the user edits that field. Classes opt in via MacroSpec.derive
  // ("http://{host}"); a controller URL that differs from the device (UniFi) sets no derive.
  const hostAddr = (useIp ? ip : dns).trim()
  useEffect(() => {
    setMacroVals((v) => {
      let next = v
      for (const ms of classMacros) {
        if (!ms.derive || macroTouched.has(ms.macro)) continue
        const val = hostAddr ? ms.derive.replace('{host}', hostAddr) : ''
        if ((next[ms.macro] || '') !== val) { if (next === v) next = { ...v }; next[ms.macro] = val }
      }
      return next
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hostAddr, classId, macroTouched])

  // Escape closes the modal (matches the backdrop click and Cancel), unless a submit is in flight.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape' && !busy) onCancel() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onCancel, busy])

  async function submit() {
    if (busy) return
    if (!name.trim()) { setErr('A host name is required'); return }
    if (!site.trim()) { setErr('Pick a site'); return }
    if (useIp && !ip.trim()) { setErr('An IP address is required'); return }
    if (!useIp && !dns.trim()) { setErr('A DNS name is required'); return }
    if (showSnmpFields && !community.trim()) { setErr('An SNMP community is required'); return }
    for (const ms of classMacros) {
      if (ms.required && !(macroVals[ms.macro] || '').trim()) { setErr(`${ms.label} is required`); return }
    }
    setBusy(true); setErr(null)
    const body: Record<string, unknown> = { name: name.trim(), ip: ip.trim(), dns: dns.trim(), use_ip: useIp, site: site.trim(), proxy_id: proxyId, class_id: classId }
    if (classMacros.length) {
      const m: Record<string, string> = {}
      for (const ms of classMacros) { const v = (macroVals[ms.macro] || '').trim(); if (v) m[ms.macro] = v }
      if (Object.keys(m).length) body.macros = m
    }
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
  return createPortal(
    <div className="dlg-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget && !busy) onCancel() }}>
      <div className="dlg" role="dialog" aria-modal="true" style={{ maxWidth: 'min(980px, 94vw)', maxHeight: 'calc(100dvh - 32px)', display: 'flex', flexDirection: 'column' }}>
        <div className="dlg-title">Add a device</div>
        <div className="dlg-scroll">
          {classes.length === 0 ? <Banner variant="info">Loading device classes…</Banner> : <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
        <div style={grid}>
          <Field label="Device class"><Combobox value={classId} onChange={setClassId} options={classOptions} placeholder="Search device classes…" /></Field>
          <Field label="Name" placeholder="e.g. core-switch-01" value={name} onChange={(e) => setName(e.target.value)} />
          <Field label="Site"><Select value={site} onChange={(e) => setSite(e.target.value)}><option value="">Choose a site…</option>{groups.map((g) => <option key={g.id} value={g.name}>{g.name}</option>)}</Select></Field>
          <Field label="Monitored by"><Select value={proxyId} onChange={(e) => setProxyId(e.target.value)}><option value="">Core server</option>{proxies.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}</Select></Field>
        </div>
        <div style={grid}>
          <Field label={useIp ? 'IP address' : 'DNS name'} placeholder={useIp ? '10.0.0.10' : 'host.example.lan'} value={useIp ? ip : dns} onChange={(e) => (useIp ? setIp(e.target.value) : setDns(e.target.value))} />
          {/* Blank label + input-height box so the toggle lines up with the address input, not its label. */}
          <Field label={' '}><div style={{ display: 'flex', alignItems: 'center', minHeight: 37 }}><Switch checked={useIp} onChange={setUseIp} label={useIp ? 'Connect by IP' : 'Connect by DNS'} /></div></Field>
        </div>
        {cls?.setup && (
          <div style={{ border: '1px solid var(--border)', borderRadius: 8, padding: '0.7rem 0.8rem', background: 'var(--elevated)', display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
            <div style={{ fontWeight: 600, fontSize: 13 }}>{cls.setup.title}</div>
            {cls.setup.intro && <div style={{ fontSize: 12.5, color: 'var(--muted)', lineHeight: 1.45 }}>{cls.setup.intro}</div>}
            {cls.setup.steps && cls.setup.steps.length > 0 && (
              <ol style={{ margin: 0, paddingLeft: '1.15rem', fontSize: 12.5, lineHeight: 1.45, display: 'flex', flexDirection: 'column', gap: '0.35rem' }}>
                {cls.setup.steps.map((s, i) => <li key={i}>{s}</li>)}
              </ol>
            )}
            {setupCmd && (
              <div style={{ position: 'relative' }}>
                <pre style={{ margin: 0, padding: '0.6rem 2.2rem 0.6rem 0.7rem', background: 'var(--panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12, overflowX: 'auto', whiteSpace: 'pre' }}>{setupCmd}</pre>
                <div style={{ position: 'absolute', top: 6, right: 6 }}><CopyButton text={setupCmd} /></div>
              </div>
            )}
            {cls.setup.note && <div style={{ fontSize: 12, color: 'var(--muted)', lineHeight: 1.45 }}>{cls.setup.note}</div>}
          </div>
        )}
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
              <span style={{ fontSize: 13, color: 'var(--muted)' }}>{proxyName} has no SNMP default - enter settings below, or set one {proxyId === '' ? 'in Settings (Core SNMP default)' : 'in Probes'} to reuse it.</span>
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
        {classMacros.length > 0 && (
          <div style={grid}>
            {classMacros.map((ms) => (
              ms.options && ms.options.length > 0 ? (
                // Fixed value set (e.g. XCP-NG's VM-monitoring mode): a select, where blank keeps
                // the template default (the hint names it).
                <Field key={ms.macro} label={ms.label + (ms.required ? '' : ' (optional)')}>
                  <Select value={macroVals[ms.macro] || ''}
                    onChange={(e) => { const val = e.target.value; setMacroTouched((t) => (t.has(ms.macro) ? t : new Set(t).add(ms.macro))); setMacroVals((v) => ({ ...v, [ms.macro]: val })) }}>
                    <option value="">{ms.hint ? `default (${ms.hint})` : 'template default'}</option>
                    {ms.options.map((o) => <option key={o} value={o}>{o}</option>)}
                  </Select>
                </Field>
              ) : (
                <Field key={ms.macro} label={ms.label + (ms.required ? '' : ' (optional)')} type={ms.secret ? 'password' : 'text'}
                  placeholder={ms.hint} value={macroVals[ms.macro] || ''}
                  onChange={(e) => { const val = e.target.value; setMacroTouched((t) => (t.has(ms.macro) ? t : new Set(t).add(ms.macro))); setMacroVals((v) => ({ ...v, [ms.macro]: val })) }} />
              )
            ))}
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
          </div>}
        </div>
        <div className="dlg-foot">
          <Button variant="ghost" onClick={onCancel} disabled={busy}>Cancel</Button>
          <Button variant="primary" onClick={submit} disabled={busy || classes.length === 0}>{busy ? 'Creating…' : 'Add device'}</Button>
        </div>
      </div>
    </div>,
    document.body,
  )
}

// --- Network auto-discovery (§B): the Discovery tab. An admin points a probe at a subnet; the scan
// job rides the probe's check-in channel (picked up within a minute), the probe's scanner reports
// raw fingerprints, and the review table below adopts (via the ordinary POST /api/hosts, tagged
// discovered) or ignores what it found. Admin-only (gated in the shell nav + clampView).
type CertReport = { fingerprint: string; subject: string; issuer: string; not_after: string }
type DiscoveryJobRow = { id: number; proxy_name: string; kind?: string; controller_id?: number; controller_name?: string; cidr: string; state: string; error?: string; certificate?: CertReport; requested_by?: string; created_at: number; completed_at?: number; found?: number; new?: number }
type DiscoveryHTTP = { port: number; scheme: string; status: number; server?: string; title?: string }
type DiscoveryUnifi = { name?: string; mac?: string; model?: string; type?: string; state?: number; version?: string; site?: string; site_desc?: string }
type DiscoveryUnifiClient = { name?: string; hostname?: string; wired?: boolean }
type DiscoveryResultRow = { id: number; ip: string; mac?: string; rdns?: string; tcp: number[]; sysdescr?: string; sysobjectid?: string; sysname?: string; http?: DiscoveryHTTP; dns?: boolean; ssh?: string; unifi?: DiscoveryUnifi; unifi_client?: DiscoveryUnifiClient; suggested_class?: string; state: string; host_id?: string; monitored_id?: string; monitored_name?: string }
type DiscRowCfg = { name: string; classId: string; http: boolean; httpScheme: string; httpPort: string; macros: Record<string, string>; site?: string }
type UnifiCtlRow = { id: number; name: string; url: string; has_key: boolean; sites: string[]; tls_mode: 'verify' | 'pin' | 'ignore'; fingerprint: string }
type CtlForm = { id: number; name: string; url: string; key: string; sites: string[]; tls: 'verify' | 'pin' | 'ignore'; fingerprint: string }
const emptyCtlForm = (): CtlForm => ({ id: 0, name: '', url: '', key: '', sites: [], tls: 'verify', fingerprint: '' })
const ctlToForm = (c: UnifiCtlRow): CtlForm => ({ id: c.id, name: c.name, url: c.url, key: '', sites: c.sites || [], tls: c.tls_mode || 'verify', fingerprint: c.fingerprint || '' })
// A pinned certificate reads as groups of four, like a fingerprint does elsewhere.
const fpGroups = (fp: string) => (fp || '').replace(/(.{4})/g, '$1 ').trim()
// The certificate facts a pin dialog shows, one per line, with the intro and the consequence around them.
function certMessage(intro: string, c: { subject?: string; issuer?: string; not_after?: string; fingerprint: string }, outro: string) {
  const when = c.not_after ? (isNaN(Date.parse(c.not_after)) ? c.not_after : new Date(c.not_after).toLocaleDateString()) : ''
  const row = (k: string, v: ReactNode) => <div><span style={{ color: 'var(--faint)', display: 'inline-block', minWidth: 88 }}>{k}</span>{v || '(not readable)'}</div>
  return (
    <div style={{ display: 'grid', gap: 8 }}>
      <div>{intro}</div>
      <div style={{ fontSize: 12.5, lineHeight: 1.6 }}>
        {row('Subject', c.subject)}
        {row('Issuer', c.issuer)}
        {row('Valid until', when)}
        {row('SHA-256', <span className="mono" style={{ wordBreak: 'break-all' }}>{fpGroups(c.fingerprint)}</span>)}
      </div>
      <div>{outro}</div>
    </div>
  )
}
// The four controller macros the adopt path fills server-side for a sweep-adopted UniFi device
// (the API key never travels through the browser) - the review UI shows them as auto-filled.
const UNIFI_AUTOFILL = ['{$UNIFI.URL}', '{$UNIFI.KEY}', '{$UNIFI.MAC}', '{$UNIFI.SITE}']

// DiscDialog is the Discovery tab's action dialog (new scan / sweep / controllers): the landing
// page stays a clean history, each action opens wizard-style. z-index sits BELOW the confirm
// provider's (100) so a nested confirm - e.g. deleting a controller - paints on top.
function DiscDialog({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])
  return createPortal(
    <div className="dlg-backdrop" style={{ zIndex: 90 }} onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="dlg" role="dialog" aria-modal="true" style={{ maxWidth: 'min(520px, 94vw)' }}>
        <div className="dlg-title">{title}</div>
        {children}
      </div>
    </div>,
    document.body,
  )
}

// --- Import: hosts from a spreadsheet or from PRTG (importhosts.go, importprtg.go) ---

type ImportRow = { row: number; ref?: string; name: string; address: string; class: string; site: string; probe: string; tags: string; asset_tag: string; location: string; mac: string; macros: Record<string, string>; hint?: string }
type ImportCheck = { row: number; status: 'ready' | 'skip' | 'fix'; problems?: { field: string; msg: string }[]; note?: string; class_id?: string; class?: string; probe?: string; missing?: MacroSpec[]; from_unifi?: boolean }
type ImportCounts = { ready: number; skip: number; fix: number }
type ImportJob = { id: string; source: string; total: number; done: number; created: number; skipped: number; failed: { row: number; name: string; error: string }[]; running: boolean }
type PRTGDevice = { ref: string; name: string; address: string; probe: string; groups: string[]; tags: string[]; sensors?: string[]; class: string; hint: string }
type PRTGTree = { version?: string; probes: { name: string; devices: number }[]; groups: number; tags: string[]; devices: PRTGDevice[] }
type ProbeMap = Record<string, { probe: string; site: string }>

const EMPTY_IMPORT_ROW: Omit<ImportRow, 'row'> = { name: '', address: '', class: '', site: '', probe: '', tags: '', asset_tag: '', location: '', mac: '', macros: {} }

// parseCSV reads a spreadsheet's CSV: quoted cells, and the separator Excel used (comma, semicolon
// or tab), read from the header line.
function parseCSV(text: string): string[][] {
  text = text.replace(/^\uFEFF/, '')
  const first = text.split(/\r?\n/, 1)[0] || ''
  const delim = [';', '\t'].reduce((best, d) => (first.split(d).length > first.split(best).length ? d : best), ',')
  const rows: string[][] = []
  let row: string[] = []
  let cell = ''
  let quoted = false
  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    if (quoted) {
      if (c === '"') { if (text[i + 1] === '"') { cell += '"'; i++ } else quoted = false } else cell += c
      continue
    }
    if (c === '"') { quoted = true; continue }
    if (c === delim) { row.push(cell); cell = ''; continue }
    if (c === '\n' || c === '\r') {
      if (c === '\r' && text[i + 1] === '\n') i++
      row.push(cell); rows.push(row); row = []; cell = ''
      continue
    }
    cell += c
  }
  if (cell !== '' || row.length) { row.push(cell); rows.push(row) }
  return rows.filter((r) => r.some((c) => c.trim() !== ''))
}

const IMPORT_COLS: Record<string, keyof ImportRow> = {
  name: 'name', host: 'name', hostname: 'name', device: 'name', devicename: 'name',
  address: 'address', ip: 'address', ipaddress: 'address', dns: 'address', dnsname: 'address',
  class: 'class', deviceclass: 'class', type: 'class',
  site: 'site', group: 'site', hostgroup: 'site',
  probe: 'probe', proxy: 'probe', monitoredby: 'probe',
  tags: 'tags', tag: 'tags', assettag: 'asset_tag', asset: 'asset_tag', location: 'location', mac: 'mac', macaddress: 'mac',
}

// rowsFromCSV makes import rows of a CSV: known columns by their header (in any case or spacing), a
// class input by its macro name (NUT.UPS or {$NUT.UPS}); other columns are listed as ignored.
function rowsFromCSV(text: string): { rows: ImportRow[]; ignored: string[] } {
  const t = parseCSV(text)
  if (t.length === 0) return { rows: [], ignored: [] }
  const head = t[0].map((h) => h.trim())
  const cols = head.map((h) => {
    const k = h.toLowerCase().replace(/[^a-z0-9]/g, '')
    if (IMPORT_COLS[k]) return { field: IMPORT_COLS[k] }
    if (/^\{?\$?[A-Za-z0-9_]+(\.[A-Za-z0-9_]+)+\}?$/.test(h)) return { macro: h }
    return null
  })
  const rows = t.slice(1).map((cells, i) => {
    const r: ImportRow = { row: i + 2, ...EMPTY_IMPORT_ROW, macros: {} }
    cols.forEach((c, j) => {
      const v = (cells[j] || '').trim()
      if (!c || !v) return
      if (c.field) (r as unknown as Record<string, string>)[c.field] = v
      else if (c.macro) r.macros[c.macro] = v
    })
    return r
  })
  return { rows, ignored: head.filter((h, j) => !cols[j] && h) }
}

// readTextFile reads a file as UTF-8, or as Excel's Windows encoding when it isn't UTF-8.
async function readTextFile(f: File): Promise<string> {
  const buf = await f.arrayBuffer()
  const text = new TextDecoder('utf-8').decode(buf)
  return text.includes('\uFFFD') ? new TextDecoder('windows-1252').decode(buf) : text
}

function importTemplate() {
  downloadCSV('argus-import-template.csv', ['name', 'address', 'class', 'site', 'probe', 'tags', 'asset tag', 'location', 'mac', 'NUT.UPS'], [
    ['sw-site4-core', '10.0.4.2', 'UniFi Switch', 'site4/Network', 'proxy-site4', 'critical', 'IT-0101', 'Comms room', '00:00:5e:00:53:10', ''],
    ['nas-site4', '10.0.4.30', 'Ugreen (Zabbix agent)', 'site4', '', 'customer-a', '', 'Rack, U4', '', ''],
    ['ups-site4', '10.0.4.50', 'UPS (NUT)', 'site4', '', '', '', '', '', 'ups'],
  ])
}

// prtgAutoMap pairs each PRTG probe with the Argus probe whose site its name carries ("Site 1 probe"
// and proxy-site1), and the Local Probe with the core server.
function prtgAutoMap(tree: PRTGTree, proxies: Proxy[]): ProbeMap {
  const norm = (v: string) => v.toLowerCase().replace(/[^a-z0-9]/g, '')
  const out: ProbeMap = {}
  for (const p of tree.probes) {
    const n = norm(p.name)
    const hit = proxies.find((x) => { const site = norm(x.name.replace(/^proxy-/, '')); return site.length >= 3 && n.includes(site) })
    out[p.name] = hit ? { probe: hit.name, site: hit.name.replace(/^proxy-/, '') } : { probe: /local/i.test(p.name) ? 'server' : '', site: '' }
  }
  return out
}

// ImportDialog imports hosts from a spreadsheet or from PRTG: Argus checks every row first, shows
// what it makes of it, lets bad cells be fixed in place, and creates nothing until Import.
function ImportDialog({ proxies, classes, onClose }: { proxies: Proxy[]; classes: DeviceClass[]; onClose: () => void }) {
  const toast = useToast()
  const [src, setSrc] = useState<'csv' | 'prtg'>('csv')
  const [rows, setRows] = useState<ImportRow[]>([])
  const [file, setFile] = useState<{ name: string; ignored: string[] } | null>(null)
  const [checks, setChecks] = useState<Record<number, ImportCheck>>({})
  const [counts, setCounts] = useState<ImportCounts | null>(null)
  const [checking, setChecking] = useState(false)
  const [show, setShow] = useState<'all' | 'fix' | 'skip'>('all')
  const [reason, setReason] = useState('')
  const [job, setJob] = useState<ImportJob | null>(null)
  const [err, setErr] = useState('')
  const [drag, setDrag] = useState(false)
  // PRTG
  const [prtgURL, setPrtgURL] = useState('')
  const [prtgKey, setPrtgKey] = useState('')
  const [prtgInsecure, setPrtgInsecure] = useState(false)
  const [tree, setTree] = useState<PRTGTree | null>(null)
  const [reading, setReading] = useState(false)
  const [pmap, setPmap] = useState<ProbeMap>({})
  const [keepGroups, setKeepGroups] = useState(true)
  const [bringTags, setBringTags] = useState(true)
  const edits = useRef<Record<number, Partial<ImportRow>>>({})
  const fileInput = useRef<HTMLInputElement>(null)
  const seq = useRef(0)
  const pickable = classes.filter((c) => !c.internal).sort((a, b) => a.label.localeCompare(b.label))

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape' && !job?.running) onClose() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose, job])

  // Every change re-checks the rows (after a pause in typing).
  useEffect(() => {
    if (rows.length === 0) { setChecks({}); setCounts(null); return }
    const n = ++seq.current
    setChecking(true)
    const t = setTimeout(async () => {
      const res = await fetch('/api/import/check', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ rows }) }).catch(() => null)
      if (n !== seq.current) return
      setChecking(false)
      if (!res || !res.ok) { setErr(await errText(res, 'Could not check the rows')); return }
      const d: { rows: ImportCheck[]; counts: ImportCounts } = await res.json()
      setErr('')
      setChecks(Object.fromEntries(d.rows.map((c) => [c.row, c])))
      setCounts(d.counts)
    }, 450)
    return () => clearTimeout(t)
  }, [rows])

  // PRTG: the rows follow the probe mapping, the group and tag choices, and the cells fixed by hand.
  useEffect(() => {
    if (!tree) return
    setRows(tree.devices.map((d, i) => {
      const m = pmap[d.probe] || { probe: '', site: '' }
      const site = m.site ? m.site + (keepGroups && d.groups.length ? '/' + d.groups.join('/') : '') : ''
      const r: ImportRow = { row: i + 1, ref: d.ref, ...EMPTY_IMPORT_ROW, macros: {}, name: d.name, address: d.address, class: d.class, site, probe: m.probe, tags: bringTags ? d.tags.join(',') : '', hint: d.hint }
      return { ...r, ...edits.current[r.row] }
    }))
  }, [tree, pmap, keepGroups, bringTags])

  // Follow a running import.
  useEffect(() => {
    if (!job?.running) return
    const t = setInterval(async () => {
      const res = await fetch(`/api/import/${job.id}`).catch(() => null)
      if (res && res.ok) setJob(await res.json())
    }, 1000)
    return () => clearInterval(t)
  }, [job?.id, job?.running]) // eslint-disable-line react-hooks/exhaustive-deps

  function edit(row: number, patch: Partial<ImportRow>) {
    edits.current[row] = { ...edits.current[row], ...patch }
    setRows((rs) => rs.map((r) => (r.row === row ? { ...r, ...patch } : r)))
  }
  function setMacro(r: ImportRow, macro: string, v: string) { edit(r.row, { macros: { ...r.macros, [macro]: v } }) }

  async function pickFile(f: File | undefined) {
    if (!f) return
    const { rows: rs, ignored } = rowsFromCSV(await readTextFile(f))
    edits.current = {}
    setFile({ name: f.name, ignored })
    setRows(rs)
    if (rs.length === 0) setErr('No rows in this file: the first line names the columns, one device per line after it.')
  }
  async function readPRTG() {
    setReading(true); setErr('')
    const res = await fetch('/api/import/prtg', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ url: prtgURL, key: prtgKey, insecure: prtgInsecure }) }).catch(() => null)
    setReading(false)
    if (!res || !res.ok) { setErr(await errText(res, 'Could not read PRTG')); return }
    const t: PRTGTree = await res.json()
    edits.current = {}
    setPmap(prtgAutoMap(t, proxies))
    setTree(t)
  }
  function switchSrc(v: 'csv' | 'prtg') {
    if (v === src) return
    setSrc(v); setRows([]); setTree(null); setFile(null); setErr(''); edits.current = {}
  }
  async function run() {
    const res = await fetch('/api/import/run', { method: 'POST', headers: { 'Content-Type': 'application/json', ...reasonHeader(reason) }, body: JSON.stringify({ rows, source: src === 'prtg' ? 'PRTG' : file?.name || 'a spreadsheet' }) }).catch(() => null)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not start the import')); return }
    setJob(await res.json())
  }

  const probeOptions = [{ v: 'server', l: 'Server (the core)' }, ...proxies.map((p) => ({ v: p.name, l: p.name }))]
  const listed = rows.filter((r) => { const st = checks[r.row]?.status; return show === 'all' || (show === 'fix' ? st === 'fix' : st === 'skip') })
  const shown = listed.slice(0, 500)
  const needPick = tree ? rows.filter((r) => !r.class).length : 0

  const cell = (r: ImportRow, field: 'name' | 'address' | 'site', ck?: ImportCheck) => {
    const bad = ck?.problems?.some((p) => p.field === field)
    if (!bad) return <span className={field === 'address' ? 'mono' : undefined}>{r[field] || <span className="muted">-</span>}</span>
    return <input className="input cellfix" value={r[field]} onChange={(e) => edit(r.row, { [field]: e.target.value })} aria-label={`${field} of row ${r.row}`} />
  }
  const classCell = (r: ImportRow, ck?: ImportCheck) => {
    const bad = ck?.problems?.some((p) => p.field === 'class')
    if (!bad && src === 'csv') return <span>{ck?.class || r.class}</span>
    const value = ck?.class_id || pickable.find((c) => c.id === r.class || c.label.toLowerCase() === r.class.toLowerCase())?.id || ''
    return (
      <>
        <Select className={'cellfix' + (bad ? ' bad' : '')} value={value} onChange={(e) => edit(r.row, { class: e.target.value })} aria-label={`Class of row ${r.row}`}>
          <option value="">{r.class && !value ? `${r.class}: pick a class` : 'Pick a class'}</option>
          {pickable.map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
        </Select>
        {r.hint && <div className="sreason">{r.hint}</div>}
      </>
    )
  }
  const probeCell = (r: ImportRow, ck?: ImportCheck) => {
    const bad = ck?.problems?.some((p) => p.field === 'probe')
    if (!bad) return <span>{ck?.probe || r.probe || <span className="muted">-</span>}</span>
    return (
      <Select className="cellfix bad" value="" onChange={(e) => edit(r.row, { probe: e.target.value })} aria-label={`Probe of row ${r.row}`}>
        <option value="">{r.probe ? `${r.probe}: pick a probe` : 'Pick a probe'}</option>
        {probeOptions.map((o) => <option key={o.v} value={o.v}>{o.l}</option>)}
      </Select>
    )
  }
  const checkCell = (r: ImportRow, ck?: ImportCheck) => {
    if (!ck) return <span className="muted">{checking ? 'checking…' : ''}</span>
    if (ck.status === 'skip') return <span className="upd-none">{ck.note}: skipped</span>
    if (ck.status === 'ready') return <span className="okquiet">ready{ck.note ? ` · ${ck.note}` : ''}{ck.from_unifi ? ' · UniFi settings from the saved controller' : ''}</span>
    return (
      <div className="imp-fix">
        {(ck.problems || []).filter((p) => !p.field.startsWith('macro:')).map((p, i) => <span key={i} className="tag err">{p.msg}</span>)}
        {(ck.missing || []).map((m) => (
          <input key={m.macro} className="input cellfix" type={m.secret ? 'password' : 'text'} placeholder={m.label} value={r.macros[m.macro] || ''} onChange={(e) => setMacro(r, m.macro, e.target.value)} aria-label={`${m.label} for row ${r.row}`} title={m.hint} />
        ))}
      </div>
    )
  }

  const table = rows.length > 0 && (
    <>
      <div className="imp-strip">
        {counts ? <>
          <span className="okquiet">{counts.ready} ready</span>
          {counts.skip > 0 && <span className="upd-none">{counts.skip} skipped</span>}
          {counts.fix > 0 && <span className="tag err">{counts.fix} to fix</span>}
        </> : <span className="muted">Checking {rows.length} rows…</span>}
        {counts && (counts.fix > 0 || counts.skip > 0) && (
          <div className="seg" style={{ marginLeft: 'auto' }}>
            <button type="button" className={show === 'all' ? 'on' : ''} onClick={() => setShow('all')}>All</button>
            {counts.fix > 0 && <button type="button" className={show === 'fix' ? 'on' : ''} onClick={() => setShow('fix')}>To fix</button>}
            {counts.skip > 0 && <button type="button" className={show === 'skip' ? 'on' : ''} onClick={() => setShow('skip')}>Skipped</button>}
          </div>
        )}
      </div>
      <div className="imp-scroll">
        <table className="slist imp-table">
          <thead><tr><th>{src === 'prtg' ? 'PRTG id' : 'Row'}</th><th>Name</th><th>Address</th><th>Class</th><th>{src === 'prtg' ? 'Group' : 'Site'}</th><th>Probe</th><th>Check</th></tr></thead>
          <tbody>
            {shown.map((r) => {
              const ck = checks[r.row]
              return (
                <tr key={r.row} className={ck?.status === 'fix' ? 'bad' : ck?.status === 'skip' ? 'skip' : undefined}>
                  <td className="mono imp-n" data-label={src === 'prtg' ? 'PRTG id' : 'Row'}>{src === 'prtg' ? r.ref : r.row}</td>
                  <td data-label="Name">{cell(r, 'name', ck)}</td>
                  <td data-label="Address">{cell(r, 'address', ck)}</td>
                  <td data-label="Class">{classCell(r, ck)}</td>
                  <td data-label={src === 'prtg' ? 'Group' : 'Site'}>{cell(r, 'site', ck)}</td>
                  <td data-label="Probe">{probeCell(r, ck)}</td>
                  <td className="imp-check">{checkCell(r, ck)}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
        {listed.length > shown.length && <div className="imp-more">The first {shown.length} of {listed.length} rows are listed; all of them are imported.</div>}
      </div>
    </>
  )

  const csvSource = (
    <>
      <div className="hs-note">One device per row, with the columns <span className="mono">name, address, class, site, probe</span> and, if you like, <span className="mono">tags, asset tag, location, mac</span>. A class's own inputs go in a column named after them (<span className="mono">NUT.UPS</span>); UniFi devices get theirs from a saved controller. An empty probe means the site's probe. The upstream device comes from the UniFi controller afterwards. Argus checks every row first; nothing is created until you press Import.</div>
      <div className={'imp-drop' + (drag ? ' over' : '')} onDragOver={(e) => { e.preventDefault(); setDrag(true) }} onDragLeave={() => setDrag(false)} onDrop={(e) => { e.preventDefault(); setDrag(false); pickFile(e.dataTransfer.files?.[0]) }}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6"><path d="M14 3H6a1 1 0 0 0-1 1v16a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V8z" /><path d="M14 3v5h5M8.5 13h7M8.5 16.5h7" /></svg>
        {file ? <span><b>{file.name}</b> <span className="sub-line">· {rows.length} row{rows.length === 1 ? '' : 's'} read{file.ignored.length ? ` · ignored columns: ${file.ignored.join(', ')}` : ''}</span></span> : <span className="muted">Drop a CSV file here, or choose one.</span>}
        <span className="imp-drop-act">
          <Button onClick={() => fileInput.current?.click()}>{file ? 'Choose another file' : 'Choose a file'}</Button>
          <Button variant="ghost" onClick={importTemplate}>Download a template</Button>
        </span>
        <input ref={fileInput} type="file" accept=".csv,text/csv" hidden onChange={(e) => { pickFile(e.target.files?.[0]); e.target.value = '' }} />
      </div>
    </>
  )

  const prtgSource = (
    <>
      <div className="hs-note">Argus reads the device tree from PRTG with an API key (read access is enough): probes, groups, devices, their addresses and tags. Nothing changes in PRTG and the key isn't kept. PRTG's sensors aren't copied: each device gets the Argus class that fits it, with its own sensors and thresholds.</div>
      <div className="hs-grid">
        <label className="chan-field"><span className="flabel">PRTG address</span><input className="input" placeholder="https://prtg.example.lan" value={prtgURL} onChange={(e) => setPrtgURL(e.target.value)} /></label>
        <label className="chan-field"><span className="flabel">API key</span><input className="input" type="password" autoComplete="off" value={prtgKey} onChange={(e) => setPrtgKey(e.target.value)} /></label>
      </div>
      <div className="imp-strip">
        <Switch checked={prtgInsecure} onChange={setPrtgInsecure} label="Accept a self-signed certificate" />
        <Button onClick={readPRTG} disabled={reading || !prtgURL.trim() || !prtgKey.trim()}>{reading ? 'Reading…' : tree ? 'Read again' : 'Read from PRTG'}</Button>
        {tree && <span className="okquiet">read {tree.devices.length} devices in {tree.probes.length} probe{tree.probes.length === 1 ? '' : 's'} and {tree.groups} group{tree.groups === 1 ? '' : 's'}{tree.version ? ` · PRTG ${tree.version}` : ''}</span>}
      </div>
      {tree && (
        <div className="info-rows imp-map">
          <InfoRow label="Probes">
            {tree.probes.map((p) => {
              const m = pmap[p.name] || { probe: '', site: '' }
              const setM = (v: Partial<{ probe: string; site: string }>) => setPmap((x) => ({ ...x, [p.name]: { ...m, ...v } }))
              return (
                <InfoLine key={p.name} k={`${p.name} · ${p.devices}`}>
                  <Select value={m.probe} onChange={(e) => { const v = e.target.value; setM({ probe: v, site: m.site || (v !== 'server' ? v.replace(/^proxy-/, '') : '') }) }} aria-label={`Argus probe for ${p.name}`} className={m.probe ? undefined : 'bad'}>
                    <option value="">Choose a probe</option>
                    {probeOptions.map((o) => <option key={o.v} value={o.v}>{o.l}</option>)}
                  </Select>
                  <input className={'input imp-site' + (m.site ? '' : ' bad')} placeholder="site (group)" value={m.site} onChange={(e) => setM({ site: e.target.value })} aria-label={`Site for ${p.name}`} />
                </InfoLine>
              )
            })}
          </InfoRow>
          <InfoRow label="Groups">
            <InfoLine>
              <div className="seg"><button type="button" className={keepGroups ? 'on' : ''} onClick={() => setKeepGroups(true)}>Keep PRTG's groups</button><button type="button" className={!keepGroups ? 'on' : ''} onClick={() => setKeepGroups(false)}>One group per probe</button></div>
              <span className="sub-line">{keepGroups ? `${tree.groups} groups, made under each probe's site` : "every device in its probe's site"}</span>
            </InfoLine>
          </InfoRow>
          <InfoRow label="Tags">
            <InfoLine>
              <Switch checked={bringTags} onChange={setBringTags} label="Bring the device tags" />
              <span className="sub-line">{tree.tags.length ? `${tree.tags.length} tags, e.g. ${tree.tags.slice(0, 3).join(', ')}` : 'no device tags in PRTG'}</span>
            </InfoLine>
          </InfoRow>
          <InfoRow label="Class">
            <InfoLine><span className="v">Guessed from each device's PRTG sensors (and from a saved UniFi controller)</span>{needPick > 0 && <span className="tag avail">{needPick} need a pick</span>}</InfoLine>
          </InfoRow>
        </div>
      )}
    </>
  )

  const done = job && !job.running
  return createPortal(
    <div className="dlg-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget && !job?.running) onClose() }}>
      <div className="dlg imp-dlg" role="dialog" aria-modal="true">
        <div className="dlg-title">Import devices</div>
        <div className="dlg-scroll"><div className="host-settings in-dlg">
          {!job && <>
            <div className="hs-mon">
              <span className="hs-monlabel">From</span>
              <div className="seg">
                <button type="button" className={src === 'csv' ? 'on' : ''} onClick={() => switchSrc('csv')}>A spreadsheet (CSV)</button>
                <button type="button" className={src === 'prtg' ? 'on' : ''} onClick={() => switchSrc('prtg')}>PRTG</button>
              </div>
            </div>
            {src === 'csv' ? csvSource : prtgSource}
            {err && <div className="imp-err">{err}</div>}
            {table}
            <div className="hs-foot">
              <span className="sub-line imp-foot-note">{counts && counts.fix > 0 ? `Fix the ${counts.fix} row${counts.fix === 1 ? '' : 's'} here${src === 'csv' ? ' or in the file' : ''}, or import the ${counts.ready} now and the rest later.` : counts && counts.ready > 0 ? 'Each host gets its class, its probe and its tags; the upstream device comes from the UniFi controller.' : ''}</span>
              <ReasonInput value={reason} onChange={setReason} />
              <Button variant="ghost" onClick={onClose}>Cancel</Button>
              <Button variant="primary" onClick={run} disabled={!counts || counts.ready === 0 || checking}>{counts && counts.ready > 0 ? `Import ${counts.ready} device${counts.ready === 1 ? '' : 's'}` : 'Import'}</Button>
            </div>
          </>}
          {job && <>
            <div className="imp-progress">
              <div className="imp-bar"><span style={{ width: `${job.total ? Math.round((job.done / job.total) * 100) : 0}%` }} /></div>
              <div>{job.running ? `Importing ${job.done} of ${job.total}…` : `Imported ${job.created} of ${job.total}${job.skipped ? `, ${job.skipped} left out` : ''}.`} {job.running && <span className="sub-line">You can close this: the import goes on, and Changes records it when it ends.</span>}</div>
            </div>
            {job.failed.length > 0 && (
              <div className="imp-failed">
                <div className="site-ed-h">Not imported</div>
                {job.failed.map((f) => <div key={f.row} className="sreason"><b>{f.name}</b> (row {f.row}): {f.error}</div>)}
              </div>
            )}
            <div className="hs-foot">
              <Button variant={done ? 'primary' : 'ghost'} onClick={onClose}>{done ? 'Done' : 'Close'}</Button>
            </div>
          </>}
        </div></div>
      </div>
    </div>,
    document.body,
  )
}

function DiscoveryView({ scanId, onOpenScan }: { scanId: string | null; onOpenScan: (id: number | null) => void }) {
  const [importing, setImporting] = useState(false) // the Import dialog (spreadsheet or PRTG)
  const [proxies, setProxies] = useState<Proxy[] | null>(null)
  const [groups, setGroups] = useState<Group[]>([])
  const [classes, setClasses] = useState<DeviceClass[]>([])
  const [jobs, setJobs] = useState<DiscoveryJobRow[] | null>(null)
  const [job, setJob] = useState<DiscoveryJobRow | null>(null)
  const [results, setResults] = useState<DiscoveryResultRow[]>([])
  // scan form
  const [proxyId, setProxyId] = useState('')
  const [cidr, setCidr] = useState('')
  const [community, setCommunity] = useState('') // blank = the probe's SNMP default
  const [site, setSite] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  // UniFi sweep form + saved controllers
  const confirm = useConfirm()
  const toast = useToast()
  const [ctls, setCtls] = useState<UnifiCtlRow[] | null>(null)
  const [sweepCtl, setSweepCtl] = useState('')
  const [sweepFrom, setSweepFrom] = useState('')
  const [sweepErr, setSweepErr] = useState<string | null>(null)
  const [sweepBusy, setSweepBusy] = useState(false)
  const [ctlForm, setCtlForm] = useState<CtlForm | null>(null)
  const [ctlBusy, setCtlBusy] = useState(false)
  const [siteNames, setSiteNames] = useState<string[]>([]) // for a controller's site scope
  const [certProbe, setCertProbe] = useState('') // which probe to ask for a controller's certificate
  const [certAsking, setCertAsking] = useState(false)
  const [certOffer, setCertOffer] = useState(false) // shown after "Argus can't reach this controller"
  useEffect(() => { fetch('/api/notify/sites').then((r) => r.json()).then((s) => setSiteNames(s || [])).catch(() => {}) }, [])
  // Which action dialog is open - the landing page itself is just the scan history. 'new' is
  // the wizard's source-picker step; future discovery sources (other vendor APIs) slot in there.
  const [dlg, setDlg] = useState<null | 'new' | 'scan' | 'sweep' | 'ctls'>(null)
  // review state
  const [sel, setSel] = useState<Set<number>>(new Set())
  const [rowCfg, setRowCfg] = useState<Record<number, DiscRowCfg>>({})
  const [rowErr, setRowErr] = useState<Record<number, string>>({})
  const [open, setOpen] = useState<Set<number>>(new Set()) // rows with the settings band expanded
  const [showIgnored, setShowIgnored] = useState(false)
  const [adding, setAdding] = useState(false)

  const classOptions = useMemo(() => classes.filter((c) => !c.internal).map((c) => ({ value: c.id, label: c.label }))
    .sort((a, b) => (a.value === 'base' ? -1 : b.value === 'base' ? 1 : a.label.localeCompare(b.label))), [classes])
  const siteOfProxy = (name: string) => (name.startsWith('proxy-') ? name.slice(6) : name)
  // Non-settings-only per-host macros a class collects at attach time (settings-only ones need
  // discovered data and live in host settings afterwards - same rule as the Add-device form).
  const attachMacros = (classId: string) => (classes.find((c) => c.id === classId)?.macros || []).filter((ms) => !ms.settings_only)
  const deriveMacros = (classId: string, ip: string): Record<string, string> => {
    const out: Record<string, string> = {}
    for (const ms of attachMacros(classId)) if (ms.derive) out[ms.macro] = ms.derive.replace('{host}', ip)
    return out
  }
  const seedRow = (r: DiscoveryResultRow): DiscRowCfg => {
    const classId = r.suggested_class && classes.some((c) => c.id === r.suggested_class) ? r.suggested_class : 'base'
    // HTTP add-on pre-ticked (and scheme/port pre-filled) from what the scan actually saw; the
    // band still offers it for any web-capable class, scan facts or not.
    const httpPort = r.http && r.http.port !== 443 && r.http.port !== 80 ? String(r.http.port) : ''
    // Some resolvers answer a PTR lookup with the IP itself - a numeric first label would seed a
    // useless name like "10", so only a real hostname-shaped rDNS contributes.
    const rdnsName = r.rdns && !/^\d+$/.test(r.rdns.split('.')[0]) ? r.rdns.split('.')[0] : ''
    // Name preference: the controller's device name, then its client-table alias/hostname (a
    // naming hint for non-UniFi hosts), then the wire facts.
    const clientName = r.unifi_client?.name || r.unifi_client?.hostname || ''
    return { name: r.unifi?.name || clientName || r.sysname || rdnsName || r.ip, classId, http: !!r.http, httpScheme: r.http?.scheme || 'https', httpPort, macros: deriveMacros(classId, r.ip) }
  }
  // Sweep-adopted UniFi devices get their controller macros injected server-side at adopt time,
  // so the review UI must neither warn about nor require them. The MAC only counts when the row
  // actually carries one (backfilled from the controller since scanner r16 / core enrichment) -
  // otherwise it stays an editable required field instead of a disabled empty one.
  const autoFilled = (r: DiscoveryResultRow, classId: string, macro: string) => {
    if (!r.unifi || !classId.startsWith('unifi-') || !UNIFI_AUTOFILL.includes(macro)) return false
    return macro !== '{$UNIFI.MAC}' || !!(r.unifi.mac || r.mac)
  }

  function applyJob(d: { job: DiscoveryJobRow; results: DiscoveryResultRow[] }) {
    setJob(d.job)
    setResults(d.results || [])
  }
  async function loadJob(id: number) {
    const r = await fetch(`/api/discovery/jobs/${id}`).catch(() => null)
    if (!r || !r.ok) return
    const d = await r.json().catch(() => null)
    if (d && d.job) applyJob({ job: d.job, results: d.results || [] })
  }
  const loadJobs = () => fetch('/api/discovery/jobs?limit=10').then((r) => (r.ok ? r.json() : [])).then((j: DiscoveryJobRow[]) => setJobs(j || [])).catch(() => setJobs([]))
  const loadCtls = () => fetch('/api/discovery/controllers').then((r) => (r.ok ? r.json() : [])).then((c: UnifiCtlRow[]) => setCtls(c || [])).catch(() => setCtls([]))

  useEffect(() => {
    fetch('/api/proxies').then((r) => (r.ok ? r.json() : [])).then((p) => setProxies(p || [])).catch(() => setProxies([]))
    fetch('/api/groups').then((r) => (r.ok ? r.json() : [])).then((g) => setGroups(g || [])).catch(() => {})
    fetch('/api/classes').then((r) => (r.ok ? r.json() : [])).then((c) => setClasses(c || [])).catch(() => {})
    void loadCtls()
    void loadJobs()
    // Keep the scan list fresh (queued scans start, running ones finish) without a manual reload.
    const t = window.setInterval(() => { void loadJobs() }, 15000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  // The opened scan is owned by the URL (?scan=, held by AppShell): load it when it changes, clear
  // everything when leaving back to the list. The adopt-site resets per scan, so a site2 scan never
  // inherits the site picked for an earlier scan of another site - the effect below re-defaults it from
  // THIS scan's source.
  useEffect(() => {
    setSel(new Set()); setRowErr({}); setOpen(new Set()); setSite(''); setDlg(null)
    if (!scanId) { setJob(null); setResults([]); return }
    void loadJob(Number(scanId))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scanId])
  // Seed each result row's adopt config (name / suggested class / derived macros) exactly once,
  // and only once the class catalog is in (the suggestion needs it). Rows the admin already edited
  // are never re-seeded; result ids are globally unique, so stale entries from an older job are inert.
  useEffect(() => {
    if (!classes.length || !results.length) return
    setRowCfg((prev) => {
      let next = prev
      for (const r of results) {
        if (next[r.id]) continue
        if (next === prev) next = { ...prev }
        next[r.id] = seedRow(r)
      }
      return next
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [classes, results])

  // Default the adopt-site from the scanning probe once a job is in view (a core scan carries no
  // site of its own, and any site the admin already picked wins).
  useEffect(() => {
    if (!job || site) return
    const s = siteOfProxy(job.proxy_name || '')
    if (s && groups.some((g) => g.name === s)) setSite(s)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [job?.id, groups.length])

  // While a scan is pending/dispatched, poll it (self-rescheduling so slow responses never overlap).
  const jobId = job?.id, jobState = job?.state
  useEffect(() => {
    if (!jobId || (jobState !== 'pending' && jobState !== 'dispatched')) return
    let alive = true
    let timer = 0
    const tick = async () => {
      const r = await fetch(`/api/discovery/jobs/${jobId}`).catch(() => null)
      if (!alive) return
      if (r && r.ok) {
        const d = await r.json().catch(() => null)
        if (!alive) return
        if (d && d.job) {
          applyJob({ job: d.job, results: d.results || [] })
          if (d.job.state === 'pending' || d.job.state === 'dispatched') timer = window.setTimeout(tick, 3000)
          else void loadJobs()
          return
        }
      }
      timer = window.setTimeout(tick, 3000)
    }
    timer = window.setTimeout(tick, 1500)
    return () => { alive = false; clearTimeout(timer) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId, jobState])

  async function start() {
    if (busy) return
    // 'core' = scan from the Argus server itself (in-process Go scanner, proxy_id ""); anything
    // else is a probe id and the job rides that probe's check-in channel.
    const isCore = proxyId === 'core'
    const p = (proxies || []).find((x) => x.id === proxyId)
    if (!isCore && !p) { setErr('Pick where to scan from - the core server or a probe'); return }
    if (!cidr.trim()) { setErr('Enter a subnet to scan, e.g. 10.0.0.0/24'); return }
    setBusy(true); setErr(null)
    const body: Record<string, unknown> = { proxy_id: isCore ? '' : proxyId, cidr: cidr.trim() }
    if (community.trim()) body.snmp = { version: 2, community: community.trim() }
    const res = await fetch('/api/discovery/jobs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { setErr(await errText(res, 'Could not start the scan')); return }
    const d = await res.json().catch(() => ({} as { id?: number }))
    setDlg(null)
    void loadJobs()
    // Jump straight into the new scan's screen - it shows the progress and then the results.
    if (d.id) onOpenScan(d.id)
  }

  async function startSweep() {
    if (sweepBusy) return
    if (!sweepCtl) { setSweepErr('Pick a saved controller to sweep'); return }
    const isCore = sweepFrom === 'core'
    if (!isCore && !(proxies || []).some((x) => x.id === sweepFrom)) { setSweepErr('Pick where to sweep from - the core server or a probe'); return }
    setSweepBusy(true); setSweepErr(null)
    const body = { kind: 'unifi', controller_id: Number(sweepCtl), proxy_id: isCore ? '' : sweepFrom }
    const res = await fetch('/api/discovery/jobs', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null)
    setSweepBusy(false)
    if (!res || !res.ok) { setSweepErr(await errText(res, 'Could not start the sweep')); return }
    const d = await res.json().catch(() => ({} as { id?: number }))
    setDlg(null)
    void loadJobs()
    if (d.id) onOpenScan(d.id)
  }

  async function saveCtl(override?: Partial<CtlForm>) {
    if (!ctlForm || ctlBusy) return
    const f = { ...ctlForm, ...(override || {}) }
    setCtlBusy(true); setSweepErr(null)
    const body = { id: f.id, name: f.name.trim(), url: f.url.trim(), api_key: f.key.trim(), sites: f.sites, tls_mode: f.tls, fingerprint: f.fingerprint.trim() }
    const res = await fetch('/api/discovery/controllers', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null)
    setCtlBusy(false)
    if (res && res.status === 409) {
      // The certificate needs a decision: self-signed (pin it?) or unreachable from the core.
      const d = await res.json().catch(() => ({})) as { error?: string; unreachable?: boolean; certificate?: { fingerprint: string; subject: string; issuer: string; not_after: string } }
      if (d.certificate) {
        const c = d.certificate
        const ok = await confirm({
          title: 'Pin this certificate?',
          message: certMessage("The controller presented a certificate that isn't trusted by the system roots (usually a console's own self-signed one).", c, 'Pin it and every request will require exactly this certificate; a change later fails loudly.'),
          confirmLabel: 'Pin and save',
        })
        if (ok) { setCtlForm({ ...f, tls: 'pin', fingerprint: c.fingerprint }); await saveCtl({ ...f, tls: 'pin', fingerprint: c.fingerprint }); return }
        setSweepErr('Not saved. Choose "Pin" with a fingerprint, or "Ignore" for this controller.')
        return
      }
      setSweepErr((d.error || 'Argus cannot reach this controller to check its certificate.') + ' Ask a probe of that site to read it below, paste its SHA-256 fingerprint under "Pin", or choose "Ignore".')
      setCertOffer(true)
      if (!certProbe) {
        const inSite = (proxies || []).find((p) => p.sweeps && f.sites.some((s) => p.name === 'proxy-' + s || p.name === s))
        setCertProbe(inSite?.id || (proxies || []).find((p) => p.sweeps)?.id || '')
      }
      return
    }
    if (!res || !res.ok) { setSweepErr(await errText(res, 'Could not save the controller')); return }
    setCtlForm(null); setCertOffer(false)
    void loadCtls()
  }

  // The controller is out of the core's reach: a probe reads its certificate over the check-in
  // channel (a "cert" job: no key travels), and the same pin dialog follows.
  async function askProbeForCertificate() {
    if (!ctlForm || !certProbe || certAsking) return
    setCertAsking(true); setSweepErr(null)
    try {
      const res = await fetch('/api/discovery/certificate', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ url: ctlForm.url.trim(), proxy_id: certProbe }) })
      if (!res.ok) { setSweepErr(await errText(res, 'Could not ask the probe')); return }
      const { job_id } = await res.json() as { job_id: number }
      const deadline = Date.now() + 3 * 60 * 1000
      let job: DiscoveryJobRow | null = null
      while (Date.now() < deadline) {
        await new Promise((r) => setTimeout(r, 2000))
        const jr = await fetch(`/api/discovery/jobs/${job_id}`).catch(() => null)
        if (!jr || !jr.ok) continue
        const d = await jr.json() as { job?: DiscoveryJobRow } & DiscoveryJobRow
        job = (d.job || d) as DiscoveryJobRow
        if (job.state === 'done' || job.state === 'failed') break
      }
      if (!job || (job.state !== 'done' && job.state !== 'failed')) { setSweepErr('The probe has not answered yet (it picks jobs up at check-in, once a minute). Try again in a moment.'); return }
      if (!job.certificate) { setSweepErr(job.error ? `The probe could not read the certificate: ${job.error}` : 'The probe answered without a certificate; it needs the latest probe image.'); return }
      const c = job.certificate
      const ok = await confirm({
        title: 'Pin this certificate?',
        message: certMessage(`Probe ${job.proxy_name} reached the controller and saw this certificate.`, c, 'Pin it and every request will require exactly this certificate; a change later fails loudly.'),
        confirmLabel: 'Pin and save',
      })
      if (!ok) return
      setCtlForm({ ...ctlForm, tls: 'pin', fingerprint: c.fingerprint })
      await saveCtl({ ...ctlForm, tls: 'pin', fingerprint: c.fingerprint })
    } catch { setSweepErr('Could not ask the probe') } finally { setCertAsking(false) }
  }

  // A sweep refused by the certificate check reports what it saw: pin it for that controller here.
  async function pinFromJob(j: DiscoveryJobRow) {
    const c = j.certificate; const ctl = (ctls || []).find((x) => x.id === j.controller_id)
    if (!c || !ctl) return
    const ok = await confirm({
      title: `Pin the certificate of ${ctl.name}?`,
      message: certMessage('The sweep was refused because this certificate is not trusted yet.', c, 'Future sweeps and scans will require exactly this certificate.'),
      confirmLabel: 'Pin',
    })
    if (!ok) return
    const body = { id: ctl.id, name: ctl.name, url: ctl.url, api_key: '', sites: ctl.sites || [], tls_mode: 'pin', fingerprint: c.fingerprint }
    const res = await fetch('/api/discovery/controllers', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null)
    if (!res || !res.ok) { setErr(await errText(res, 'Could not pin the certificate')); return }
    toast.success(`Certificate pinned for ${ctl.name}. Run the sweep again.`)
    void loadCtls()
  }

  async function deleteCtl(c: UnifiCtlRow) {
    if (!(await confirm({ message: `Delete the saved controller "${c.name}"? Devices already adopted keep working - only future sweeps lose it.`, danger: true }))) return
    const res = await fetch(`/api/discovery/controllers/${c.id}`, { method: 'DELETE' }).catch(() => null)
    if (!res || !res.ok) { setSweepErr(await errText(res, 'Could not delete the controller')); return }
    if (sweepCtl === String(c.id)) setSweepCtl('')
    void loadCtls()
  }

  async function deleteJob(j: DiscoveryJobRow) {
    const label = j.kind === 'unifi' ? `the sweep of ${j.controller_name || 'the controller'}` : `the scan of ${j.cidr}`
    if (!(await confirm({ message: `Delete ${label} and its results? Devices ignored only in this scan will show as new next time they are found.`, danger: true }))) return
    const res = await fetch(`/api/discovery/jobs/${j.id}`, { method: 'DELETE' }).catch(() => null)
    if (!res || !res.ok) { setErr(await errText(res, 'Could not delete the scan')); return }
    if (job?.id === j.id) onOpenScan(null)
    void loadJobs()
  }

  const selectable = (r: DiscoveryResultRow) => r.state === 'new' && !r.monitored_id
  const visible = results.filter((r) => showIgnored || r.state !== 'ignored')
  const selectableIds = visible.filter(selectable).map((r) => r.id)
  const allSelected = selectableIds.length > 0 && selectableIds.every((id) => sel.has(id))
  const toggle = (id: number) => setSel((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n })
  const toggleAll = () => setSel(allSelected ? new Set<number>() : new Set(selectableIds))
  const toggleOpen = (id: number) => setOpen((s) => { const n = new Set(s); if (n.has(id)) n.delete(id); else n.add(id); return n })
  const setCfg = (id: number, patch: Partial<DiscRowCfg>) => setRowCfg((c) => ({ ...c, [id]: { ...c[id], ...patch } }))
  const setRowClass = (id: number, classId: string, ip: string) => setRowCfg((c) => {
    const cur = c[id]
    // A class switch re-derives that class's URL-style macros but keeps anything already typed.
    return { ...c, [id]: { ...cur, classId, macros: { ...deriveMacros(classId, ip), ...Object.fromEntries(Object.entries(cur.macros).filter(([, v]) => v !== '')) } } }
  })

  async function addSelected() {
    if (adding) return
    const jobProxyId = (proxies || []).find((p) => p.name === job?.proxy_name)?.id || ''
    setAdding(true); setErr(null)
    const errs: Record<number, string> = { ...rowErr }
    const adopted: Record<number, string> = {} // result id -> host id
    for (const id of Array.from(sel)) {
      const r = results.find((x) => x.id === id)
      const cfg = rowCfg[id]
      if (!r || !cfg || !selectable(r)) continue
      const cls = classes.find((c) => c.id === cfg.classId)
      let bad = ''
      const macros: Record<string, string> = {}
      for (const ms of attachMacros(cfg.classId)) {
        const v = (cfg.macros[ms.macro] || '').trim()
        if (v) macros[ms.macro] = v
        else if (ms.required && !autoFilled(r, cfg.classId, ms.macro)) bad = `${ms.label} is required - open the row's settings`
      }
      const siteFor = (cfg.site || site).trim()
      if (!siteFor) bad = 'pick a site - in the toolbar for all selected, or per device in the row settings'
      if (!cfg.name.trim()) bad = 'a name is required'
      if (bad) { errs[id] = bad; setOpen((s) => new Set(s).add(id)); continue }
      const body: Record<string, unknown> = { name: cfg.name.trim(), ip: r.ip, use_ip: true, site: siteFor, proxy_id: jobProxyId, class_id: cfg.classId, discovery_result_id: id }
      if (Object.keys(macros).length) body.macros = macros
      if (cls?.offers_http && cfg.http) {
        body.http = true; body.http_scheme = cfg.httpScheme || 'https'
        if (cfg.httpPort.trim()) body.http_port = cfg.httpPort.trim()
      }
      const res = await fetch('/api/hosts', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null)
      if (!res || !res.ok) { errs[id] = await errText(res, 'could not create the device'); continue }
      const d = await res.json().catch(() => ({} as { id?: string }))
      adopted[id] = d.id || ''
      delete errs[id]
    }
    setRowErr(errs)
    setAdding(false)
    if (Object.keys(adopted).length) {
      setResults((rs) => rs.map((r) => (adopted[r.id] !== undefined ? { ...r, state: 'added', host_id: adopted[r.id], monitored_id: adopted[r.id], monitored_name: rowCfg[r.id]?.name || r.ip } : r)))
      setSel((s) => { const n = new Set(s); for (const id of Object.keys(adopted)) n.delete(Number(id)); return n })
      fireDataRefresh()
    }
  }

  async function setState(ids: number[], state: 'ignored' | 'new') {
    if (!ids.length) return
    const res = await fetch('/api/discovery/results/state', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ids, state }) }).catch(() => null)
    if (!res || !res.ok) { setErr(await errText(res, 'Could not update the results')); return }
    setResults((rs) => rs.map((r) => (ids.includes(r.id) && r.state !== 'added' ? { ...r, state } : r)))
    setSel((s) => { const n = new Set(s); for (const id of ids) n.delete(id); return n })
  }

  const scanCapable = (proxies || []).filter((p) => p.scans)
  const sweepCapable = (proxies || []).filter((p) => p.sweeps)
  const running = jobState === 'pending' || jobState === 'dispatched'
  const isSweep = job?.kind === 'unifi'
  const jobLabel = job ? (job.kind === 'unifi' ? (job.controller_name || 'UniFi controller') : job.cidr) : ''
  const grid: CSSProperties = { display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))', gap: '0.7rem' }
  const portTag = (p: number) => (p === 22 ? 'SSH' : p === 445 ? 'SMB' : p === 3493 ? 'NUT' : p === 443 ? 'HTTPS' : p === 80 ? 'HTTP' : p === 10050 ? 'Agent' : p === 53 ? ':53' : `:${p}`)
  const facts = (r: DiscoveryResultRow): string[] => {
    if (r.unifi) {
      // Controller facts lead (exact model, site, link state); an enriched scan row keeps its
      // wire facts after them - a pure sweep row has none.
      const f: string[] = []
      if (r.unifi.model) f.push(r.unifi.model)
      if (r.unifi.site && r.unifi.site !== 'default') f.push(r.unifi.site_desc || r.unifi.site)
      f.push(r.unifi.state === 1 ? 'online' : 'offline')
      for (const p of r.tcp) f.push(portTag(p))
      return f
    }
    const f: string[] = []
    // A client-table match is a naming hint, not gear: one pill says where the name came from.
    if (r.unifi_client) f.push(r.unifi_client.wired ? 'wired client' : 'Wi-Fi client')
    if (r.sysdescr || r.sysname) f.push('SNMP')
    if (r.dns) f.push('DNS')
    for (const p of r.tcp) f.push(portTag(p))
    return f
  }
  const stateTag = (r: DiscoveryResultRow) => {
    if (r.state === 'added') return <span className="tag online">added</span>
    if (r.monitored_id) return <span className="tag online" title={r.monitored_name || undefined}>monitored</span>
    if (r.state === 'ignored') return <span className="tag">ignored</span>
    return <span className="tag pending">new</span>
  }

  return (
    <>
      {/* --- list screen: the scan history IS the page; New scan / UniFi sweep / Manage
          controllers open as dialogs (wizard-style, per user feedback) --- */}
      {!job && <>
      <section className="panel">
      <div className="phead">
        <PanelTitle eyebrow="Configure">Network discovery</PanelTitle>
        <span className="hint">find devices, review what answered, adopt into monitoring · kept for 30 days</span>
        <div className="tools">
          <Button onClick={() => { setCtlForm((ctls?.length || 0) === 0 ? emptyCtlForm() : null); setDlg('ctls') }}>Discovery settings</Button>
          <Button onClick={() => setImporting(true)} disabled={proxies === null}>Import</Button>
          <Button variant="primary" onClick={() => setDlg('new')} disabled={proxies === null || ctls === null}>+ New scan</Button>
        </div>
      </div>
      {importing && proxies && <ImportDialog proxies={proxies} classes={classes} onClose={() => setImporting(false)} />}
      {/* One concise nudge, only while no controller is saved - configuring them first makes
          every later discovery identify UniFi gear exactly. */}
      {ctls !== null && ctls.length === 0 && (
        <p className="panel-intro">Tip: save your UniFi controllers first (<b>Discovery settings</b>). Discovery matches what it finds against them, so UniFi devices come back exactly identified, ready to adopt with their settings pre-filled.</p>
      )}
      {err && <div style={{ padding: '10px 1rem 0' }}><Banner variant="error">{err}</Banner></div>}
      {jobs === null && <div style={{ padding: '10px 1rem 1rem' }}><Skeleton rows={2} cols={5} /></div>}
      {jobs !== null && jobs.length === 0 && <p style={{ color: 'var(--muted)', fontSize: 13, padding: '10px 1rem 1rem', margin: 0 }}>No discoveries yet - start a subnet scan with <b>+ New scan</b>, or pull a controller's devices with <b>UniFi sweep</b>.</p>}
      {jobs !== null && jobs.length > 0 && (
        <div className="enroll-scroll">
          <table className="enroll enroll-discovery">
            <thead><tr><th>When</th><th>Source</th><th>Target</th><th>Found</th><th>Status</th><th>By</th><th style={{ width: 34 }} /></tr></thead>
            <tbody>
              {jobs.map((j) => (
                <tr key={j.id} className="disc-scan-row" title={j.error || undefined}
                  onClick={() => onOpenScan(j.id)}>
                  <td data-label="When" className="mono" style={{ color: 'var(--muted)' }}>{relTime(j.created_at)}</td>
                  <td data-label="Source">{j.proxy_name || 'Core server'}</td>
                  <td data-label="Target" className={j.kind === 'unifi' ? undefined : 'mono'}>{j.kind === 'unifi' ? `UniFi sweep · ${j.controller_name || '?'}` : j.cidr}</td>
                  <td data-label="Found">{j.state === 'done' ? `${j.found ?? 0} device${(j.found ?? 0) === 1 ? '' : 's'} · ${j.new ?? 0} new` : '-'}</td>
                  <td data-label="Status"><span className={'tag' + (j.state === 'done' ? ' online' : j.state === 'failed' ? '' : ' pending')}>{j.state === 'dispatched' ? (j.kind === 'unifi' ? 'sweeping' : 'scanning') : j.state === 'pending' ? 'queued' : j.state}</span></td>
                  <td data-label="By" style={{ color: 'var(--muted)' }}>{j.requested_by || '-'}</td>
                  <td data-label="" onClick={(e) => e.stopPropagation()}>
                    <button className="iconbtn" title="Delete this scan" aria-label="Delete this scan" onClick={() => void deleteJob(j)}>
                      <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M4 7h16M9 7V5a1 1 0 0 1 1-1h4a1 1 0 0 1 1 1v2M6 7l1 13a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1l1-13" /></svg>
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      </section>

      {/* Step 1 of the wizard: pick the discovery source (probe-wizard option-card idiom).
          Future vendor APIs become more cards here, not more page chrome. */}
      {dlg === 'new' && (
        <DiscDialog title="New scan" onClose={() => setDlg(null)}>
          <p className="dlg-msg">How should Argus look for devices?</p>
          <div style={{ display: 'grid', gap: 8 }}>
            <button type="button" onClick={() => setDlg('scan')} style={{ textAlign: 'left', padding: '10px 12px', borderRadius: 8, cursor: 'pointer', border: '1px solid var(--border)', background: 'var(--elevated)' }}>
              <div style={{ fontWeight: 600, fontSize: 13.5, color: 'var(--text)' }}>Subnet scan</div>
              <div style={{ color: 'var(--muted)', fontSize: 12 }}>Probe an IP range from the core or a probe - finds anything that answers, vendor or not.</div>
            </button>
            <button type="button" onClick={() => setDlg('sweep')} style={{ textAlign: 'left', padding: '10px 12px', borderRadius: 8, cursor: 'pointer', border: '1px solid var(--border)', background: 'var(--elevated)' }}>
              <div style={{ fontWeight: 600, fontSize: 13.5, color: 'var(--text)' }}>UniFi controller sweep</div>
              <div style={{ color: 'var(--muted)', fontSize: 12 }}>Ask a saved controller for every device it manages, across all its sites - no wire scan, settings pre-filled.</div>
            </button>
          </div>
          <div className="dlg-foot">
            <Button variant="ghost" onClick={() => setDlg(null)}>Cancel</Button>
          </div>
        </DiscDialog>
      )}

      {dlg === 'scan' && (
        <DiscDialog title="New subnet scan" onClose={() => setDlg(null)}>
          <p className="dlg-msg">Probes every address in the range from the collector you pick and suggests a device class for whatever answers.</p>
          <Field label="Scan from">
            <Select value={proxyId} onChange={(e) => setProxyId(e.target.value)}>
              <option value="">Choose…</option>
              <option value="core">Core server</option>
              {(proxies || []).map((p) => <option key={p.id} value={p.id} disabled={!p.scans}>{p.name}{p.scans ? '' : ' (needs probe update)'}</option>)}
            </Select>
          </Field>
          <Field label="Subnet" placeholder="10.0.0.0/24" value={cidr} onChange={(e) => setCidr(e.target.value)} />
          <Field label="SNMP community (optional)" placeholder={proxyId === 'core' ? "core's SNMP default" : "probe's SNMP default"} value={community} onChange={(e) => setCommunity(e.target.value)} />
          {proxies !== null && scanCapable.length === 0 && (
            <Banner variant="info">None of your probes has reported the network-scan capability yet - it ships with the latest probe image (and needs check-in enabled). You can still scan from the core server.</Banner>
          )}
          {err && <Banner variant="error">{err}</Banner>}
          <div className="dlg-foot">
            <Button variant="ghost" onClick={() => setDlg('new')}>‹ Back</Button>
            <Button variant="primary" onClick={start} disabled={busy}>{busy ? 'Starting…' : 'Start scan'}</Button>
          </div>
        </DiscDialog>
      )}

      {dlg === 'sweep' && (
        <DiscDialog title="UniFi controller sweep" onClose={() => setDlg(null)}>
          <p className="dlg-msg">Asks a saved controller for every device it manages, across all its sites - no wire scan. Devices adopted from a sweep arrive with their UniFi settings pre-filled, the API key included.</p>
          {(ctls?.length || 0) === 0 ? (
            <>
              <p style={{ color: 'var(--muted)', fontSize: 13, margin: 0 }}>No controllers saved yet.</p>
              <div className="dlg-foot">
                <Button variant="ghost" onClick={() => setDlg('new')}>‹ Back</Button>
                <Button variant="primary" onClick={() => { setCtlForm(emptyCtlForm()); setDlg('ctls') }}>Add a controller</Button>
              </div>
            </>
          ) : (
            <>
              <Field label="Controller">
                <Select value={sweepCtl} onChange={(e) => setSweepCtl(e.target.value)}>
                  <option value="">Choose…</option>
                  {(ctls || []).map((c) => <option key={c.id} value={String(c.id)}>{c.name}</option>)}
                </Select>
              </Field>
              <Field label="Sweep from">
                <Select value={sweepFrom} onChange={(e) => setSweepFrom(e.target.value)}>
                  <option value="">Choose…</option>
                  <option value="core">Core server</option>
                  {(proxies || []).map((p) => <option key={p.id} value={p.id} disabled={!p.sweeps}>{p.name}{p.sweeps ? '' : ' (needs probe update)'}</option>)}
                </Select>
              </Field>
              {proxies !== null && sweepCapable.length === 0 && (
                <Banner variant="info">None of your probes has reported the UniFi-sweep capability yet - it ships with the latest probe image. You can still sweep from the core server if it can reach the controller.</Banner>
              )}
              {sweepErr && <Banner variant="error">{sweepErr}</Banner>}
              <div className="dlg-foot">
                <Button variant="ghost" onClick={() => setDlg('new')}>‹ Back</Button>
                <Button variant="primary" onClick={startSweep} disabled={sweepBusy}>{sweepBusy ? 'Starting…' : 'Start sweep'}</Button>
              </div>
            </>
          )}
        </DiscDialog>
      )}

      {dlg === 'ctls' && (
        <DiscDialog title="Discovery settings" onClose={() => setDlg(null)}>
          <div style={{ fontWeight: 600, fontSize: 13.5, margin: '2px 0 6px' }}>UniFi controllers</div>
          <p className="dlg-msg">Saved once, used by both discovery paths: sweeps import a controller's devices directly, and subnet scans match their results against it. The API key comes from UniFi Network → Settings → Control Plane → Integrations and never leaves the server.</p>
          {(ctls || []).map((c) => (
            <div key={c.id} style={{ display: 'flex', alignItems: 'center', gap: '0.6rem', flexWrap: 'wrap', fontSize: 13, marginBottom: 8 }}>
              <b>{c.name}</b>
              <span className="mono" style={{ color: 'var(--muted)' }}>{c.url}</span>
              <span style={{ color: 'var(--faint)', fontSize: 12 }}>{c.sites && c.sites.length ? c.sites.join(', ') : 'all sites'} · {c.tls_mode === 'pin' ? 'pinned certificate' : c.tls_mode === 'ignore' ? 'certificate ignored' : 'certificate verified'}</span>
              <span style={{ flex: 1 }} />
              <Button variant="ghost" onClick={() => setCtlForm(ctlToForm(c))}>Edit</Button>
              <Button variant="ghost" onClick={() => void deleteCtl(c)}>Delete</Button>
            </div>
          ))}
          {ctlForm === null && <div style={{ marginTop: 4 }}><Button onClick={() => setCtlForm(emptyCtlForm())}>+ Add controller</Button></div>}
          {ctlForm !== null && (
            <>
              <Field label="Name" placeholder="site1" value={ctlForm.name} onChange={(e) => setCtlForm({ ...ctlForm, name: e.target.value })} />
              <Field label="Controller URL" placeholder="https://unifi.example.lan:11443" value={ctlForm.url} onChange={(e) => setCtlForm({ ...ctlForm, url: e.target.value })} />
              <Field label={ctlForm.id ? 'API key (blank = keep current)' : 'API key'} type="password" placeholder="from Control Plane → Integrations" value={ctlForm.key} onChange={(e) => setCtlForm({ ...ctlForm, key: e.target.value })} />
              <label className="field"><span>Sites</span>
                <SitePicker options={siteNames} value={ctlForm.sites} onChange={(v) => setCtlForm({ ...ctlForm, sites: v })} />
              </label>
              <p className="set-note">Which probes may use this controller (its API key travels to them for scans and sweeps). Leave empty for every site.</p>
              <label className="field"><span>Certificate</span>
                <Select value={ctlForm.tls} onChange={(e) => setCtlForm({ ...ctlForm, tls: e.target.value as CtlForm['tls'] })}>
                  <option value="verify">Verify (system roots; self-signed consoles are offered for pinning)</option>
                  <option value="pin">Pin a fingerprint</option>
                  <option value="ignore">Ignore the certificate</option>
                </Select>
              </label>
              {ctlForm.tls === 'pin' && (
                <Field label="Certificate SHA-256 fingerprint" placeholder="64 hex characters, as the console or a browser shows it" value={ctlForm.fingerprint} onChange={(e) => setCtlForm({ ...ctlForm, fingerprint: e.target.value })} />
              )}
              {ctlForm.tls === 'ignore' && <p className="set-note">The API key is then sent to whatever answers at this address. Only for a network you trust end to end.</p>}
              {certOffer && ctlForm.tls === 'verify' && (
                <div style={{ display: 'grid', gap: 6, padding: '8px 10px', border: '1px solid var(--border)', borderRadius: 8, marginBottom: 12 }}>
                  <div style={{ fontSize: 12.5, fontWeight: 600 }}>Ask a probe for the certificate</div>
                  <p className="set-note" style={{ margin: 0 }}>A probe of that site connects to the controller and reports the certificate it presents (no API key travels). You then decide whether to pin it.</p>
                  <div style={{ display: 'flex', gap: '0.6rem', alignItems: 'center', flexWrap: 'wrap' }}>
                    <Select value={certProbe} onChange={(e) => setCertProbe(e.target.value)}>
                      <option value="">Choose a probe…</option>
                      {(proxies || []).map((p) => <option key={p.id} value={p.id} disabled={!p.sweeps}>{p.name}{p.sweeps ? '' : ' (needs probe update)'}</option>)}
                    </Select>
                    <Button onClick={() => void askProbeForCertificate()} disabled={!certProbe || certAsking}>{certAsking ? 'Asking the probe…' : 'Ask the probe'}</Button>
                  </div>
                </div>
              )}
              <div style={{ display: 'flex', gap: '0.6rem' }}>
                <Button variant="primary" onClick={() => void saveCtl()} disabled={ctlBusy}>{ctlBusy ? 'Saving…' : ctlForm.id ? 'Save changes' : 'Add controller'}</Button>
                <Button variant="ghost" onClick={() => setCtlForm(null)}>Cancel</Button>
              </div>
            </>
          )}
          {sweepErr && <Banner variant="error">{sweepErr}</Banner>}
          <div className="dlg-foot">
            <Button variant="ghost" onClick={() => setDlg(null)}>Close</Button>
          </div>
        </DiscDialog>
      )}
      </>}

      {/* --- scan screen: an opened scan replaces the whole page; the topbar + URL reflect it
          (?scan=) and "Back to scans" (or the browser's Back) returns to the list. --- */}
      {job && <section className="panel">
        <div className="phead" style={job.state === 'done' ? { borderBottom: 'none' } : undefined}>
          <PanelTitle eyebrow={`Configure · discovery · ${job.proxy_name || 'Core server'}`}>{isSweep ? 'Sweep results' : 'Scan results'} · {jobLabel}</PanelTitle>
          <span className="hint">{job.state === 'done' ? `${results.length} device${results.length === 1 ? '' : 's'} found · ${relTime(job.completed_at || job.created_at)}` : `started ${relTime(job.created_at)}`}</span>
          <div className="tools">
            <Button variant="ghost" onClick={() => void deleteJob(job)}>Delete</Button>
            <Button variant="ghost" onClick={() => onOpenScan(null)}>‹ Back to scans</Button>
          </div>
        </div>
      {err && <div style={{ padding: '8px 1rem 0' }}><Banner variant="error">{err}</Banner></div>}
      {running && (
        <div style={{ display: 'flex', alignItems: 'center', gap: '0.6rem', fontSize: 13, color: 'var(--muted)', padding: '14px 1rem' }}>
          <span className="spinner" aria-hidden="true" />
          {jobState === 'pending' && job.proxy_name ? `Queued for ${job.proxy_name} - it picks jobs up at check-in, one at a time…`
            : isSweep ? `${job.proxy_name || 'The core server'} is asking ${jobLabel} for its devices… this only takes a moment.`
            : `${job.proxy_name || 'The core server'} is scanning ${job.cidr}… this can take a few minutes.`}
        </div>
      )}
      {job.state === 'failed' && <div style={{ padding: '14px 1rem', display: 'grid', gap: 8 }}>
        <Banner variant="error">{isSweep ? 'Sweep of' : 'Scan of'} {jobLabel} failed: {job.error || 'unknown error'}</Banner>
        {isSweep && job.certificate && job.controller_id ? (
          <div style={{ display: 'flex', gap: '0.6rem', alignItems: 'center', flexWrap: 'wrap', fontSize: 13 }}>
            <span style={{ color: 'var(--muted)' }}>The controller presented a certificate with SHA-256 <span className="mono">{fpGroups(job.certificate.fingerprint)}</span>.</span>
            <Button onClick={() => void pinFromJob(job)}>Pin it for this controller</Button>
          </div>
        ) : null}
      </div>}

      {job.state === 'done' && (
        <>
          {/* The header + adopt toolbar read as ONE block: no border under the title, one border
              under the toolbar (a line only above the toolbar looked lopsided). */}
          {job.error && <div style={{ padding: '0 1rem' }}><Banner variant="info">{job.error}</Banner></div>}
          <div className="disc-actions" style={{ display: 'flex', alignItems: 'center', gap: '0.6rem', flexWrap: 'wrap', padding: '0 1rem 0.7rem', borderBottom: '1px solid var(--border)' }}>
            <Button variant="primary" onClick={addSelected} disabled={sel.size === 0 || adding}>{adding ? 'Adding…' : `Add ${sel.size || ''} selected`.replace('  ', ' ')}</Button>
            <span style={{ fontSize: 13, color: 'var(--muted)' }}>into</span>
            <Select value={site} onChange={(e) => setSite(e.target.value)} style={{ width: 'auto', minWidth: 150 }} title="The site the selected devices join - override per device in its row settings">
              <option value="">Choose a site…</option>
              {groups.map((g) => <option key={g.id} value={g.name}>{g.name}</option>)}
            </Select>
            <Button variant="ghost" onClick={() => setState(Array.from(sel), 'ignored')} disabled={sel.size === 0 || adding}>Ignore selected</Button>
            <span style={{ flex: 1 }} />
            {results.some((r) => r.state === 'ignored') && <Switch checked={showIgnored} onChange={setShowIgnored} label="Show ignored" />}
          </div>
          <div className="enroll-scroll">
            <table className="enroll enroll-discovery">
              <thead><tr>
                <th style={{ width: 34 }}><input type="checkbox" checked={allSelected} onChange={toggleAll} aria-label="Select all" disabled={selectableIds.length === 0} /></th>
                <th>Device</th><th>Found</th><th>Class</th><th>Status</th>
              </tr></thead>
              <tbody>
                {visible.length === 0 && <tr><td colSpan={5} style={{ padding: 0 }}><div style={{ flex: 1, width: '100%' }}><EmptyState icon={ic.discovery} title={isSweep ? 'No devices' : 'Nothing answered'} text={isSweep ? `${jobLabel} reported no adopted devices with an IP address.` : `No live devices in ${job.cidr}. Try a different range, or check the SNMP community.`} /></div></td></tr>}
                {visible.map((r) => {
                  const cfg = rowCfg[r.id]
                  const canPick = selectable(r)
                  const cls = classes.find((c) => c.id === cfg?.classId)
                  const hasBand = canPick // the band always at least offers the per-device site override
                  const fp = facts(r)
                  return (
                    <Fragment key={r.id}>
                      <tr className={canPick ? '' : 'disc-muted'}>
                        <td data-label="">{canPick ? <input type="checkbox" checked={sel.has(r.id)} onChange={() => toggle(r.id)} aria-label={`Select ${r.ip}`} /> : null}</td>
                        <td data-label="Device">
                          <div className="cell-stack">
                            {canPick && cfg
                              ? <input className="input disc-name" value={cfg.name} onChange={(e) => setCfg(r.id, { name: e.target.value })} />
                              : <span>{r.monitored_name || cfg?.name || r.sysname || r.rdns || r.ip}</span>}
                            <span className="sub-line">{r.ip}{r.mac ? ` · ${r.mac}` : ''}{r.rdns && r.rdns !== r.ip ? ` · ${r.rdns}` : ''}</span>
                          </div>
                        </td>
                        <td data-label="Found">
                          <div className="cell-stack">
                            <span className="disc-facts">{fp.length ? fp.map((f) => <span key={f} className="tag">{f}</span>) : <span style={{ color: 'var(--faint)' }}>ping only</span>}</span>
                            {(() => {
                              const sub = r.unifi ? (r.unifi.version ? `firmware ${r.unifi.version}` : '') : (r.http?.title || r.sysdescr || r.ssh || '')
                              return sub ? <span className="sub-line" title={r.unifi ? sub : (r.sysdescr || r.http?.title || r.ssh)}>{sub.slice(0, 80)}</span> : null
                            })()}
                          </div>
                        </td>
                        <td data-label="Class">
                          {canPick && cfg ? (
                            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                              <div style={{ minWidth: 180, flex: 1 }}><Combobox value={cfg.classId} onChange={(v) => setRowClass(r.id, v, r.ip)} options={classOptions} placeholder="Class…" /></div>
                              {(() => {
                                // Proactive nudge: this class can't be adopted until its required
                                // fields are filled (Add also hard-blocks the row with an error).
                                const missing = attachMacros(cfg.classId).filter((ms) => ms.required && !autoFilled(r, cfg.classId, ms.macro) && !(cfg.macros[ms.macro] || '').trim())
                                return missing.length > 0 ? (
                                  <button className="iconbtn disc-warn" title={`This class still needs: ${missing.map((m) => m.label).join(', ')} - click to fill them in`}
                                    aria-label="Missing required settings" onClick={() => setOpen((s) => new Set(s).add(r.id))}>
                                    <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2"><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z" /><path d="M12 9.5v4M12 17h.01" /></svg>
                                  </button>
                                ) : null
                              })()}
                              {hasBand && (
                                <button className="iconbtn" title="Device settings" aria-label="Device settings" onClick={() => toggleOpen(r.id)}>
                                  <svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="2"><path d={open.has(r.id) ? 'M6 15l6-6 6 6' : 'M6 9l6 6 6-6'} /></svg>
                                </button>
                              )}
                            </div>
                          ) : <span style={{ color: 'var(--muted)' }}>{classes.find((c) => c.id === (r.state === 'added' ? cfg?.classId : r.suggested_class))?.label || '-'}</span>}
                        </td>
                        <td data-label="Status">
                          <div className="vcell">
                            {stateTag(r)}
                            {r.state === 'ignored' && <Button variant="ghost" onClick={() => setState([r.id], 'new')}>Unignore</Button>}
                            {rowErr[r.id] && <span className="txt-err" style={{ fontSize: 12 }}>{rowErr[r.id]}</span>}
                          </div>
                        </td>
                      </tr>
                      {canPick && cfg && open.has(r.id) && (
                        <tr className="disc-band-row"><td colSpan={5} style={{ padding: 0 }}>
                          <div className="disc-band">
                            <div style={grid}>
                              <Field label="Site (this device)">
                                <Select value={cfg.site || ''} onChange={(e) => setCfg(r.id, { site: e.target.value })}>
                                  <option value="">{site ? `same as the toolbar (${site})` : 'same as the toolbar'}</option>
                                  {groups.map((g) => <option key={g.id} value={g.name}>{g.name}</option>)}
                                </Select>
                              </Field>
                            </div>
                            {attachMacros(cfg.classId).length > 0 && (
                              <div style={grid}>
                                {attachMacros(cfg.classId).map((ms) => (
                                  autoFilled(r, cfg.classId, ms.macro) ? (
                                    // Sweep-adopted rows: the controller macros land server-side at
                                    // adopt time. URL/KEY/MAC are fixed facts (the key never reaches
                                    // the browser); the site stays overridable.
                                    ms.macro === '{$UNIFI.SITE}' ? (
                                      <Field key={ms.macro} label={ms.label + ' (optional)'} placeholder={`${r.unifi?.site || 'default'} - from the controller`}
                                        value={cfg.macros[ms.macro] || ''} onChange={(e) => setCfg(r.id, { macros: { ...cfg.macros, [ms.macro]: e.target.value } })} />
                                    ) : (
                                      <Field key={ms.macro} label={ms.label} value={ms.macro === '{$UNIFI.MAC}' ? (r.unifi?.mac || r.mac || '') : ''}
                                        placeholder="auto-filled from the controller" disabled readOnly />
                                    )
                                  ) : ms.options && ms.options.length > 0 ? (
                                    <Field key={ms.macro} label={ms.label + (ms.required ? '' : ' (optional)')}>
                                      <Select value={cfg.macros[ms.macro] || ''} onChange={(e) => setCfg(r.id, { macros: { ...cfg.macros, [ms.macro]: e.target.value } })}>
                                        <option value="">{ms.hint ? `default (${ms.hint})` : 'template default'}</option>
                                        {ms.options.map((o) => <option key={o} value={o}>{o}</option>)}
                                      </Select>
                                    </Field>
                                  ) : (
                                    <Field key={ms.macro} label={ms.label + (ms.required ? '' : ' (optional)')} type={ms.secret ? 'password' : 'text'}
                                      placeholder={ms.hint} value={cfg.macros[ms.macro] || ''}
                                      onChange={(e) => setCfg(r.id, { macros: { ...cfg.macros, [ms.macro]: e.target.value } })} />
                                  )
                                ))}
                              </div>
                            )}
                            {cls?.offers_http && (
                              <div style={{ display: 'flex', gap: '0.9rem', alignItems: 'center', flexWrap: 'wrap' }}>
                                <Switch checked={cfg.http} onChange={(v) => setCfg(r.id, { http: v })} label="Also check HTTP/HTTPS" />
                                {cfg.http && <>
                                  <Select value={cfg.httpScheme} onChange={(e) => setCfg(r.id, { httpScheme: e.target.value })} style={{ width: 'auto' }}><option value="https">HTTPS</option><option value="http">HTTP</option></Select>
                                  <input className="input" style={{ width: 120 }} placeholder="port (443)" value={cfg.httpPort} onChange={(e) => setCfg(r.id, { httpPort: e.target.value })} />
                                  {r.http && <span style={{ fontSize: 12, color: 'var(--muted)' }}>scan saw {r.http.scheme.toUpperCase()} on :{r.http.port}</span>}
                                </>}
                              </div>
                            )}
                          </div>
                        </td></tr>
                      )}
                    </Fragment>
                  )
                })}
              </tbody>
            </table>
          </div>
        </>
      )}
      </section>}

    </>
  )
}

const IFTYPE: Record<number, string> = { 1: 'Agent', 2: 'SNMP', 3: 'IPMI', 4: 'JMX' }
function blankSnmp(): SnmpCfg { return { version: 2, community: 'public', bulk: 1, security_name: '', security_level: 0, auth_protocol: 0, auth_passphrase: '', priv_protocol: 0, priv_passphrase: '', context_name: '' } }

// ClassChanger swaps a host's device class in place (no delete/recreate): pick a new class, supply its
// required macros (+ SNMP creds if the host needs a new SNMP interface), confirm, POST it, reload.
function ClassChanger({ hostId, currentClassId, currentClassLabel, onChanged }: { hostId: string; currentClassId?: string; currentClassLabel?: string; onChanged: () => void }) {
  const confirm = useConfirm()
  const [open, setOpen] = useState(false)
  const [classes, setClasses] = useState<DeviceClass[]>([])
  const [sel, setSel] = useState('')
  const [macros, setMacros] = useState<Record<string, string>>({})
  const [snmpOn, setSnmpOn] = useState(false)
  const [snmp, setSnmp] = useState({ version: 2, community: '', port: '' })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  useEffect(() => { if (open && classes.length === 0) fetch('/api/classes').then((r) => (r.ok ? r.json() : [])).then((c) => setClasses(c || [])).catch(() => {}) }, [open, classes.length])
  const cls = classes.find((c) => c.id === sel)
  const macroSpecs = (cls?.macros || []).filter((m) => !m.settings_only)
  async function apply() {
    if (!cls) return
    if (!(await confirm({ title: 'Change class', message: `Switch this host to "${cls.label}"? Sensors from templates that are only in the current class are removed; history for any template shared with the new class is kept.`, confirmLabel: 'Change class' }))) return
    setBusy(true); setErr('')
    const body: Record<string, unknown> = { class_id: sel, macros }
    if (cls.iface === 'snmp' && snmpOn) body.snmp = { version: snmp.version, community: snmp.community, port: snmp.port }
    const res = await fetch(`/api/hosts/${hostId}/class`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { setErr(await errText(res, 'Could not change class')); return }
    setOpen(false); onChanged()
  }
  return (
    <div className="hs-mon">
      <span className="hs-monlabel">Class</span>
      {!open ? (
        <>
          <span style={{ fontSize: 13 }}>{currentClassLabel || currentClassId || 'None'}</span>
          {ARGUS_MANAGED_CLASSES.has(currentClassId || '')
            ? <span className="set-hint" style={{ margin: 0 }}>managed by Argus</span>
            : <button className="btn" onClick={() => { setSel(''); setMacros({}); setErr(''); setOpen(true) }}>Change class</button>}
        </>
      ) : (
        <div style={{ flex: 1, minWidth: 0 }}>
          <Select value={sel} onChange={(e) => { setSel(e.target.value); setMacros({}) }}>
            <option value="">Select a class…</option>
            {classes.filter((c) => c.id !== currentClassId && !c.internal).map((c) => <option key={c.id} value={c.id}>{c.label}</option>)}
          </Select>
          {macroSpecs.length > 0 && (
            <div className="hs-grid" style={{ marginTop: 10 }}>
              {macroSpecs.map((m) => (
                <label className="field" key={m.macro}>
                  <span>{m.label}{m.required ? ' *' : ''}</span>
                  {m.options && m.options.length > 0
                    ? <Select value={macros[m.macro] || ''} onChange={(e) => setMacros((v) => ({ ...v, [m.macro]: e.target.value }))}><option value="">{m.hint ? `default (${m.hint})` : 'default'}</option>{m.options.map((o) => <option key={o} value={o}>{o}</option>)}</Select>
                    : <input className="input" type={m.secret ? 'password' : 'text'} placeholder={m.hint || ''} value={macros[m.macro] || ''} onChange={(e) => setMacros((v) => ({ ...v, [m.macro]: e.target.value }))} />}
                </label>
              ))}
            </div>
          )}
          {cls?.iface === 'snmp' && (
            <label className="hs-note" style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 10 }}>
              <input type="checkbox" checked={snmpOn} onChange={(e) => setSnmpOn(e.target.checked)} />
              <span>Enter SNMP credentials (only needed if this host has no SNMP interface yet and its proxy has no SNMP default)</span>
            </label>
          )}
          {cls?.iface === 'snmp' && snmpOn && (
            <div className="hs-grid" style={{ marginTop: 8 }}>
              <label className="field"><span>SNMP version</span><Select value={snmp.version} onChange={(e) => setSnmp((s) => ({ ...s, version: Number(e.target.value) }))}><option value={1}>v1</option><option value={2}>v2c</option></Select></label>
              <label className="field"><span>Community</span><input className="input" value={snmp.community} onChange={(e) => setSnmp((s) => ({ ...s, community: e.target.value }))} /></label>
              <label className="field"><span>Port</span><input className="input" placeholder="161" value={snmp.port} onChange={(e) => setSnmp((s) => ({ ...s, port: e.target.value }))} /></label>
            </div>
          )}
          {err && <div className="txt-err" style={{ fontSize: 13, marginTop: 8 }}>{err}</div>}
          <div style={{ display: 'flex', gap: 8, marginTop: 10 }}>
            <Button variant="primary" onClick={apply} disabled={busy || !sel}>Apply class change</Button>
            <Button variant="ghost" onClick={() => setOpen(false)} disabled={busy}>Cancel</Button>
          </div>
        </div>
      )}
    </div>
  )
}

// HostSettingsModal hosts the settings editor in a portaled dialog: navigating the tree or
// switching views can never leave a stale settings band behind (the old inline band survived a
// drill back to the root) - the dialog is closed first (Escape / backdrop / Cancel / Back, since
// the open dialog is URL-backed via &edit=).
// ThrRow is one global threshold-default field: edit the fleet-wide value (blank/placeholder = the
// factory default), save on blur, and Reset back to the factory value. Saves via PUT /api/thresholds/default.
function ThrRow({ template, row, onSaved, reason = '' }: { template: string; row: ThrRowData; onSaved: (template: string, macro: string, value: string) => void; reason?: string }) {
  const [val, setVal] = useState(row.value || '')
  const [status, setStatus] = useState<'idle' | 'saving' | 'saved' | 'err'>('idle')
  const [err, setErr] = useState('')
  useEffect(() => { setVal(row.value || '') }, [row.value])
  async function commit(next: string) {
    next = next.trim()
    if (next === (row.value || '')) { setStatus('idle'); return }
    setStatus('saving'); setErr('')
    const res = await fetch('/api/thresholds/default', { method: 'PUT', headers: { 'Content-Type': 'application/json', ...reasonHeader(reason) }, body: JSON.stringify({ template, macro: row.macro, value: next }) }).catch(() => null)
    if (!res || !res.ok) { setStatus('err'); setErr(await errText(res, 'Save failed')); return }
    setStatus('saved'); onSaved(template, row.macro, next); setTimeout(() => setStatus('idle'), 1500)
  }
  const overridden = (row.value || '') !== ''
  return (
    <div className="thr-row">
      <span className="thr-row-label">{row.label}{row.unit ? ` (${row.unit})` : ''}</span>
      <div className="thr-row-input">
        <input className="input" value={val} inputMode="decimal" placeholder={`default ${row.default}${row.unit || ''}`}
          onChange={(e) => setVal(e.target.value)} onBlur={() => commit(val)}
          onKeyDown={(e) => { if (e.key === 'Enter') (e.target as HTMLInputElement).blur() }} />
        {overridden && <button className="btn ghost thr-reset" onClick={() => { setVal(''); commit('') }}>Reset</button>}
        {status === 'saving' && <span className="thr-row-def">Saving…</span>}
        {status === 'saved' && <span className="thr-row-def txt-ok">Saved</span>}
      </div>
      {status === 'err' ? <span className="thr-row-def txt-err">{err}</span>
        : overridden ? <span className="thr-row-def">Overrides the default of {row.default}{row.unit || ''}</span>
        : <span className="thr-row-def">Factory default</span>}
    </div>
  )
}

// thrScopeText describes who a template's thresholds reach, for the list row + dialog header.
function thrScopeText(t: ThrTemplate): string {
  if (t.every_host) return 'Applies to every device'
  if (t.optional) return 'Optional add-on (enabled per host)'
  return t.classes && t.classes.length > 0 ? 'Used by: ' + t.classes.join(', ') : ''
}

// ThresholdDialog edits one template's fleet-wide threshold defaults. Each field saves on blur (ThrRow);
// closing refreshes the list so the "N customized" counts update.
function ThresholdDialog({ tpl, onClose, onSaved }: { tpl: ThrTemplate; onClose: () => void; onSaved: (template: string, macro: string, value: string) => void }) {
  const [reason, setReason] = useState('')
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])
  return createPortal(
    <div className="dlg-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="dlg" role="dialog" aria-modal="true" style={{ maxWidth: 'min(760px, 94vw)', maxHeight: 'calc(100dvh - 32px)', display: 'flex', flexDirection: 'column' }}>
        <div className="dlg-title">{tpl.label} · thresholds</div>
        <div className="dlg-scroll">
          <p className="set-note" style={{ marginTop: 0 }}>{thrScopeText(tpl)}. These are the fleet-wide defaults - every host using this template inherits them unless overridden in its own settings. Blank a field (or Reset) to use the factory default.</p>
          <div className="thr-reason"><ReasonInput value={reason} onChange={setReason} /></div>
          <div className="thr-rows">
            {tpl.thresholds.map((r) => <ThrRow key={r.macro} template={tpl.template} row={r} onSaved={onSaved} reason={reason} />)}
          </div>
        </div>
        <div className="hs-foot"><Button variant="ghost" onClick={onClose}>Done</Button></div>
      </div>
    </div>,
    document.body,
  )
}

type StatusNote = { text: string; style: 'info' | 'warning' | 'problem'; at: number; until?: number; by?: string }
type StatusPage = { id: number; name: string; sites: string[]; allow_cidrs: string; expires_at: number; created_at: number; created_by: string; last_viewed_at: number; has_link?: boolean; note?: StatusNote }
const NOTE_STYLES: { v: StatusNote['style']; label: string; color: string }[] = [
  { v: 'info', label: 'Info', color: 'var(--accent)' }, { v: 'warning', label: 'Warning', color: 'var(--warn)' }, { v: 'problem', label: 'Problem', color: 'var(--err)' },
]
// How long a new note shows, in hours (0 = until it's removed).
const NOTE_FOR_HOURS = [0, 1, 4, 12, 24, 72, 168]

// Expiry choices for a status page link, in days (0 = never).
const STATUS_EXPIRY_DAYS = [0, 1, 7, 30, 90, 365]

// statusLinkURL makes a status link absolute: the server returns one only when a Public URL is set.
function statusLinkURL(link: string): string {
  return link.startsWith('/') ? window.location.origin + link : link
}

// StatusPagesView manages the read-only status pages a wall screen opens with a secret link (admin).
// The link is shown once, right after creating a page or giving it a new link.
type MaintWindow = { id: number; name: string; sites: string[]; host_ids: string[]; kind: 'once' | 'daily' | 'weekly' | 'monthly'; start_at?: number; minute: number; weekdays?: number; month_day?: number; duration_min: number; enabled: boolean; created_by?: string; active?: boolean; until?: number; next_start?: number }
const WEEKDAYS_SHORT = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
const WEEK_ORDER = [1, 2, 3, 4, 5, 6, 0] // Monday first

function fmtHM(minute: number): string {
  const h = Math.floor(minute / 60), m = minute % 60
  if (CLOCK.h24) return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`
  return `${h % 12 || 12}:${String(m).padStart(2, '0')} ${h < 12 ? 'AM' : 'PM'}`
}
function fmtDurMin(min: number): string {
  const d = Math.floor(min / 1440), h = Math.floor((min % 1440) / 60), m = min % 60
  return [d ? `${d} d` : '', h ? `${h} h` : '', m ? `${m} min` : ''].filter(Boolean).join(' ') || '0 min'
}
// scheduleText words when a window runs: "Mon, Wed at 02:00 for 2 h".
function scheduleText(w: MaintWindow): string {
  const dur = ` for ${fmtDurMin(w.duration_min)}`
  if (w.kind === 'once') return `${w.start_at ? fmtWhen(w.start_at) : 'once'}${dur}`
  const at = ` at ${fmtHM(w.minute)}`
  if (w.kind === 'daily') return `Every day${at}${dur}`
  if (w.kind === 'weekly') {
    const days = WEEK_ORDER.filter((d) => ((w.weekdays || 0) & (1 << d)) !== 0).map((d) => WEEKDAYS_SHORT[d])
    return `${days.length === 7 ? 'Every day' : days.join(', ')}${at}${dur}`
  }
  return `${w.month_day === -1 ? 'Last day of the month' : `Day ${w.month_day} of the month`}${at}${dur}`
}

// MaintenanceView lists the maintenance windows and edits them (admin, helpdesk): when some hosts'
// alerts wait for planned work (maintenance.go).
function MaintenanceView({ canEdit }: { canEdit: boolean }) {
  const toast = useToast()
  const confirm = useConfirm()
  const [wins, setWins] = useState<MaintWindow[] | null>(null)
  const [sites, setSites] = useState<string[]>([])
  const [hosts, setHosts] = useState<Host[]>([])
  const [editing, setEditing] = useState<MaintWindow | 'new' | null>(null)
  function load() {
    fetch('/api/maintenance').then((r) => (r.ok ? r.json() : Promise.reject(r))).then((w) => setWins(w || [])).catch(() => { setWins([]); toast.error('Failed to load the maintenance windows') })
  }
  useEffect(() => {
    load()
    fetch('/api/me/notify/sites').then((r) => (r.ok ? r.json() : [])).then((s) => setSites(s || [])).catch(() => {})
    fetch('/api/hosts').then((r) => (r.ok ? r.json() : [])).then((h) => setHosts(h || [])).catch(() => {})
  }, []) // eslint-disable-line react-hooks/exhaustive-deps
  const hostName = (id: string) => hosts.find((h) => h.id === id)?.name || `host ${id}`
  const targets = (w: MaintWindow) => [...w.sites, ...w.host_ids.map(hostName)].join(', ')

  async function del(w: MaintWindow) {
    if (!(await confirm({ title: 'Delete window', message: `Delete "${w.name}"? Its hosts' alerts stop waiting for it.`, confirmLabel: 'Delete', danger: true }))) return
    const res = await fetch(`/api/maintenance/${w.id}`, { method: 'DELETE' })
    if (!res.ok) { toast.error(await errText(res, 'Could not delete the window')); return }
    toast.success('Window deleted'); load()
  }
  async function toggle(w: MaintWindow, enabled: boolean) {
    const res = await fetch(`/api/maintenance/${w.id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...w, enabled }) })
    if (!res.ok) { toast.error(await errText(res, 'Could not save the window')); return }
    load()
  }

  return (
    <div className="panel">
      <div className="phead">
        <PanelTitle eyebrow="Configure">Maintenance windows</PanelTitle>
        <span className="hint">times in the Argus timezone{CLOCK.tz ? ` (${CLOCK.tz})` : ''}</span>
        {canEdit && <div className="tools"><button className="btn primary" onClick={() => setEditing('new')}>+ Add window</button></div>}
      </div>
      <p className="set-note" style={{ padding: '10px 16px 0', margin: 0 }}>
        During a window its hosts keep collecting and their problems stay on screen, but nobody is alerted about them. When it ends, whatever is still wrong is alerted.
      </p>
      {wins === null ? <Skeleton rows={3} cols={4} /> : wins.length === 0 ? (
        <div className="empty-state" style={{ padding: '28px 16px' }}>No maintenance windows yet.{canEdit ? ' Add one for a nightly backup, a monthly parity check or a patch night.' : ''}</div>
      ) : (
        <table className="utable mtable">
          <thead><tr><th style={{ width: '22%' }}>Name</th><th>Covers</th><th>When</th><th>Status</th>{canEdit && <th style={{ textAlign: 'right' }}>Manage</th>}</tr></thead>
          <tbody>
            {wins.map((w) => (
              <tr key={w.id} style={{ opacity: w.enabled ? 1 : 0.55 }}>
                <td data-label="Name"><b>{w.name}</b></td>
                <td data-label="Covers" className="mwrap">{targets(w) || '-'}</td>
                <td data-label="When" className="mwrap">{scheduleText(w)}</td>
                <td data-label="Status">{!w.enabled ? <span className="set-src">off</span>
                  : w.active ? <span className="tag avail" title="Alerts for its hosts are waiting">on until {fmtWhen(w.until || 0)}</span>
                    : w.next_start ? <span className="okquiet">next {fmtWhen(w.next_start)}</span> : <span className="set-src">over</span>}</td>
                {canEdit && (
                  <td data-label="Manage" style={{ textAlign: 'right' }}>
                    <Kebab actions={[
                      { label: 'Edit…', icon: kbIcon.gear, onClick: () => setEditing(w) },
                      w.enabled ? { label: 'Turn off', icon: kbIcon.pause, onClick: () => toggle(w, false) } : { label: 'Turn on', icon: kbIcon.resume, onClick: () => toggle(w, true) },
                      { sep: true, label: '' },
                      { label: 'Delete', icon: uIcon.trash, danger: true, onClick: () => del(w) },
                    ]} />
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {editing && <MaintenanceDialog initial={editing === 'new' ? null : editing} sites={sites} hosts={hosts}
        onCancel={() => setEditing(null)} onSaved={() => { setEditing(null); toast.success('Window saved'); load() }} />}
    </div>
  )
}

// MaintenanceDialog adds or edits one window.
function MaintenanceDialog({ initial, sites, hosts, onCancel, onSaved, presetHosts }: { initial: MaintWindow | null; sites: string[]; hosts: Host[]; onCancel: () => void; onSaved: () => void; presetHosts?: string[] }) {
  const toast = useToast()
  const [name, setName] = useState(initial?.name || '')
  const [selSites, setSelSites] = useState<string[]>(initial?.sites || [])
  const [selHosts, setSelHosts] = useState<string[]>(initial?.host_ids || presetHosts || [])
  const [kind, setKind] = useState<MaintWindow['kind']>(initial?.kind || (presetHosts ? 'once' : 'weekly'))
  const [time, setTime] = useState(fmtHM24(initial?.minute ?? 120))
  const [weekdays, setWeekdays] = useState(initial?.weekdays || (1 << 0))
  const [monthDay, setMonthDay] = useState(initial?.month_day || 1)
  const [onceAt, setOnceAt] = useState(() => { const d = new Date(((initial?.start_at) || (Math.ceil(Date.now() / 3600000) * 3600)) * 1000); d.setMinutes(d.getMinutes() - d.getTimezoneOffset()); return d.toISOString().slice(0, 16) })
  const [hours, setHours] = useState(Math.floor((initial?.duration_min ?? 120) / 60))
  const [mins, setMins] = useState((initial?.duration_min ?? 120) % 60)
  const [enabled, setEnabled] = useState(initial ? initial.enabled : true)
  const [busy, setBusy] = useState(false)

  async function save(e: FormEvent) {
    e.preventDefault()
    const [hh, mm] = time.split(':').map(Number)
    const body = {
      name, sites: selSites, host_ids: selHosts, kind, enabled,
      minute: (hh || 0) * 60 + (mm || 0), weekdays: kind === 'weekly' ? weekdays : 0, month_day: kind === 'monthly' ? monthDay : 0,
      start_at: kind === 'once' ? Math.floor(new Date(onceAt).getTime() / 1000) : 0,
      duration_min: Number(hours) * 60 + Number(mins),
    }
    setBusy(true)
    try {
      const res = await fetch(initial ? `/api/maintenance/${initial.id}` : '/api/maintenance', { method: initial ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      if (!res.ok) { toast.error(await errText(res, 'Could not save the window')); return }
      onSaved()
    } catch { toast.error('Could not save the window') } finally { setBusy(false) }
  }

  return (
    <ChannelDialog title={initial ? 'Edit maintenance window' : 'Add a maintenance window'} submitLabel={busy ? 'Saving…' : initial ? 'Save changes' : 'Add window'}
      enabled={enabled} setEnabled={setEnabled} onCancel={onCancel} onSubmit={save}>
      <ChanSection title="Window">
        <div className="chan-row">
          <label className="chan-field chan-full"><span className="flabel">Name</span>
            <input className="input" value={name} maxLength={80} placeholder="Nightly backups" onChange={(e) => setName(e.target.value)} required />
          </label>
        </div>
        <div className="chan-row">
          <div className="chan-field chan-wide"><span className="flabel">Sites</span>
            <SitePicker options={sites} value={selSites} onChange={setSelSites} noAll placeholder="No site" />
          </div>
          <div className="chan-field chan-wide"><span className="flabel">Hosts</span>
            <SitePicker options={hosts.map((h) => h.id)} labelOf={(id) => hosts.find((h) => h.id === id)?.name || id} value={selHosts} onChange={setSelHosts} noAll placeholder="No single host" />
          </div>
        </div>
        <p className="set-note">A site covers every host in it and below it; add single hosts on top.</p>
      </ChanSection>
      <ChanSection title="When">
        <div className="chan-row">
          <label className="chan-field"><span className="flabel">Repeats</span>
            <Select value={kind} onChange={(e) => setKind(e.target.value as MaintWindow['kind'])}>
              <option value="once">Once</option>
              <option value="daily">Every day</option>
              <option value="weekly">Every week</option>
              <option value="monthly">Every month</option>
            </Select>
          </label>
          {kind === 'once'
            ? <label className="chan-field"><span className="flabel">Starts</span><input className="input" type="datetime-local" value={onceAt} onChange={(e) => setOnceAt(e.target.value)} required /></label>
            : <label className="chan-field"><span className="flabel">Starts at</span><input className="input" type="time" value={time} onChange={(e) => setTime(e.target.value)} required /></label>}
          <div className="chan-field"><span className="flabel">Lasts</span>
            <span className="mdur"><input className="input" type="number" min={0} max={168} value={hours} onChange={(e) => setHours(Number(e.target.value))} /> h <input className="input" type="number" min={0} max={59} step={5} value={mins} onChange={(e) => setMins(Number(e.target.value))} /> min</span>
          </div>
        </div>
        {kind === 'weekly' && (
          <div className="chan-row"><div className="chan-field chan-full"><span className="flabel">On</span>
            <div className="wdays">{WEEK_ORDER.map((d) => {
              const on = (weekdays & (1 << d)) !== 0
              return <button type="button" key={d} className={'wday' + (on ? ' on' : '')} aria-pressed={on} onClick={() => setWeekdays(weekdays ^ (1 << d))}>{WEEKDAYS_SHORT[d]}</button>
            })}</div>
          </div></div>
        )}
        {kind === 'monthly' && (
          <div className="chan-row"><label className="chan-field"><span className="flabel">On day</span>
            <Select value={monthDay} onChange={(e) => setMonthDay(Number(e.target.value))}>
              {Array.from({ length: 31 }, (_, i) => i + 1).map((d) => <option key={d} value={d}>{d}</option>)}
              <option value={-1}>Last day</option>
            </Select>
          </label></div>
        )}
        <p className="set-note">{kind === 'once' ? 'In your browser\'s time.' : `In the Argus timezone${CLOCK.tz ? ` (${CLOCK.tz})` : ''}. A window can run past midnight.`}</p>
      </ChanSection>
    </ChannelDialog>
  )
}

// fmtHM24 is a minute of the day as an <input type=time> value ("02:00").
function fmtHM24(minute: number): string { return `${String(Math.floor(minute / 60)).padStart(2, '0')}:${String(minute % 60).padStart(2, '0')}` }

function StatusPagesView() {
  const toast = useToast()
  const confirm = useConfirm()
  const [pages, setPages] = useState<StatusPage[] | null>(null)
  const [sites, setSites] = useState<string[]>([])
  const [editing, setEditing] = useState<StatusPage | 'new' | null>(null)
  const [reveal, setReveal] = useState<{ name: string; link: string } | null>(null)
  const [noting, setNoting] = useState<StatusPage | null>(null)
  function load() { fetch('/api/status-pages').then((r) => (r.ok ? r.json() : Promise.reject())).then((p) => setPages(p || [])).catch(() => toast.error('Could not load status pages')) }
  useEffect(() => {
    load()
    fetch('/api/notify/sites').then((r) => r.json()).then((s) => setSites(s || [])).catch(() => {})
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  async function rotate(p: StatusPage) {
    if (!(await confirm({ title: 'New link', message: `Make a new link for “${p.name}”? The current link stops working at once, so screens using it need the new one.`, confirmLabel: 'Make a new link', danger: true }))) return
    const res = await fetch(`/api/status-pages/${p.id}/rotate`, { method: 'POST' })
    if (!res.ok) { toast.error(await errText(res, 'Could not make a new link')); return }
    const j = await res.json()
    setReveal({ name: p.name, link: statusLinkURL(j.link) })
  }
  async function showLink(p: StatusPage) {
    const res = await fetch(`/api/status-pages/${p.id}/link`)
    if (!res.ok) { toast.error(await errText(res, 'Could not get the link')); return }
    const j = await res.json()
    setReveal({ name: p.name, link: statusLinkURL(j.link) })
  }
  async function del(p: StatusPage) {
    if (!(await confirm({ title: 'Delete status page', message: `Delete “${p.name}”? Its link stops working.`, confirmLabel: 'Delete', danger: true }))) return
    const res = await fetch(`/api/status-pages/${p.id}`, { method: 'DELETE' })
    if (!res.ok) { toast.error(await errText(res, 'Could not delete the status page')); return }
    toast.success(`Status page “${p.name}” deleted.`)
    load()
  }

  return (
    <div className="panel">
      <div className="phead">
        <PanelTitle eyebrow="Admin">Status pages</PanelTitle>
        <span className="hint">{pages ? `${pages.length} page${pages.length === 1 ? '' : 's'}` : '…'}</span>
        <div className="tools"><button className="btn primary" onClick={() => setEditing('new')}>+ Add status page</button></div>
      </div>
      <p className="panel-intro">
        A status page is a read-only dashboard for a wall screen, opened with a secret link instead of a login. It shows the sites you choose, host by host, with the sensors that need attention, and refreshes every 30 seconds. Anyone with the link can see it, so keep it on the screen it's for: limit it to your networks, give it an expiry, or make a new link to shut the old one out. Pin a note on a page to tell whoever is looking what's going on; a page also shows the maintenance windows of its hosts, in progress and coming up.
      </p>
      {pages === null && <Skeleton rows={2} cols={3} />}
      {pages && pages.length === 0 && (
        <EmptyState icon={ic.statuspages} title="No status pages yet" text="Add one to put your sites on a wall screen without signing in there."
          action={<Button variant="primary" onClick={() => setEditing('new')}>+ Add status page</Button>} />
      )}
      {pages && pages.length > 0 && (
        <div className="chan-grid">
          {pages.map((p) => {
            const expired = p.expires_at > 0 && p.expires_at * 1000 <= Date.now()
            return (
              <div className={'chan' + (expired ? ' off' : '')} key={p.id}>
                <div className="ct">
                  <span className="ci" style={{ background: 'var(--accent)' }}>{ic.statuspages}</span>
                  <span className="chan-name">{p.name}</span>
                </div>
                <p className="chan-meta">{sitesLabel(p.sites)} · {p.allow_cidrs ? `only ${p.allow_cidrs}` : 'any network'} · {p.expires_at ? (expired ? 'expired' : `expires ${new Date(p.expires_at * 1000).toLocaleDateString()}`) : 'never expires'}</p>
                <div className="chan-status">{p.last_viewed_at ? `Last viewed ${relTime(p.last_viewed_at)}` : 'Not opened yet'}</div>
                {p.note && (
                  <div className="sp-note" style={{ '--c': NOTE_STYLES.find((s) => s.v === p.note!.style)?.color } as CSSProperties} title={p.note.text}>
                    <span className="sp-note-t">{p.note.text}</span>
                    <span className="sp-note-w">{p.note.until ? `until ${fmtWhen(p.note.until)}` : 'until removed'}</span>
                  </div>
                )}
                <div className="chan-actions">
                  {p.has_link ? <Button onClick={() => showLink(p)}>Show link</Button> : <Button onClick={() => rotate(p)}>New link</Button>}
                  <Button variant="ghost" onClick={() => setNoting(p)}>{p.note ? 'Note' : 'Add note'}</Button>
                  <Kebab actions={[
                    { label: 'Edit…', icon: kbIcon.edit, onClick: () => setEditing(p) },
                    ...(p.has_link ? [{ label: 'New link…', icon: kbIcon.edit, onClick: () => rotate(p) }] : []),
                    { sep: true, label: '' },
                    { label: 'Delete', icon: kbIcon.trash, danger: true, onClick: () => del(p) },
                  ]} />
                </div>
              </div>
            )
          })}
        </div>
      )}
      {editing && (
        <StatusPageDialog initial={editing === 'new' ? null : editing} sites={sites} onCancel={() => setEditing(null)}
          onSaved={(p, link) => { setEditing(null); load(); if (link) setReveal({ name: p.name, link: statusLinkURL(link) }); else toast.success('Status page saved.') }} />
      )}
      {reveal && <StatusLinkDialog name={reveal.name} link={reveal.link} onClose={() => setReveal(null)} />}
      {noting && <StatusNoteDialog page={noting} onClose={(changed) => { setNoting(null); if (changed) load() }} />}
    </div>
  )
}

// StatusNoteDialog pins a note on a status page for whoever is looking at it ("site3's internet is
// down, the ISP has a ticket open"), in a style and for a time, or takes the current one down.
function StatusNoteDialog({ page, onClose }: { page: StatusPage; onClose: (changed: boolean) => void }) {
  const toast = useToast()
  const cur = page.note
  const [text, setText] = useState(cur?.text || '')
  const [style, setStyle] = useState<StatusNote['style']>(cur?.style || 'info')
  // How long: keep the current end, or a fresh one from now.
  const [hours, setHours] = useState<number>(cur ? -1 : 0)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(false) }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])
  async function save(e: FormEvent) {
    e.preventDefault()
    const until = hours === -1 ? (cur?.until || 0) : hours === 0 ? 0 : Math.floor(Date.now() / 1000) + hours * 3600
    setBusy(true)
    const res = await fetch(`/api/status-pages/${page.id}/note`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ text, style, until }) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not save the note')); return }
    toast.success('Note on the page.'); onClose(true)
  }
  async function remove() {
    setBusy(true)
    const res = await fetch(`/api/status-pages/${page.id}/note`, { method: 'DELETE' }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not remove the note')); return }
    toast.success('Note removed.'); onClose(true)
  }
  const left = 500 - text.length
  return createPortal(
    <div className="dlg-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(false) }}>
      <form className="dlg" role="dialog" aria-modal="true" onSubmit={save} style={{ maxWidth: 'min(600px, 94vw)' }}>
        <div className="dlg-title">Note on {page.name}</div>
        <p className="dlg-msg">A note shows at the top of the page, for whoever is looking at it: what's going on, what's being done about it, when it's expected back.{cur?.by ? ` Posted by ${cur.by} ${relTime(cur.at)}.` : ''}</p>
        <div className="chan-form" style={{ marginTop: 10 }}>
          <label className="chan-field"><span className="flabel">Note</span>
            <textarea className="input" rows={4} maxLength={500} value={text} placeholder="e.g. The internet at site3 is down. The ISP has a ticket open; next update by 15:00." onChange={(e) => setText(e.target.value)} style={{ resize: 'vertical', minHeight: 84 }} />
            <span className="flabel" style={{ textAlign: 'right', marginTop: 4, color: left < 50 ? 'var(--warn)' : undefined }}>{left} left</span>
          </label>
          <div className="chan-row">
            <label className="chan-field"><span className="flabel">Style</span>
              <Select value={style} onChange={(e) => setStyle(e.target.value as StatusNote['style'])}>{NOTE_STYLES.map((s) => <option key={s.v} value={s.v}>{s.label}</option>)}</Select>
            </label>
            <label className="chan-field"><span className="flabel">Shows</span>
              <Select value={hours} onChange={(e) => setHours(Number(e.target.value))}>
                {cur && <option value={-1}>{cur.until ? `Keep: until ${fmtWhen(cur.until)}` : 'Keep: until removed'}</option>}
                {NOTE_FOR_HOURS.map((h) => <option key={h} value={h}>{h === 0 ? 'Until I remove it' : h < 24 ? `For ${h} hour${h === 1 ? '' : 's'}` : `For ${h / 24} day${h === 24 ? '' : 's'}`}</option>)}
              </Select>
            </label>
          </div>
        </div>
        <div className="dlg-foot">
          {cur && <Button variant="danger" disabled={busy} onClick={remove} style={{ marginRight: 'auto' }}>Remove note</Button>}
          <Button onClick={() => onClose(false)}>Cancel</Button>
          <Button variant="primary" type="submit" disabled={busy || !text.trim()}>{cur ? 'Save note' : 'Put on the page'}</Button>
        </div>
      </form>
    </div>,
    document.body,
  )
}

function StatusPageDialog({ initial, sites, onCancel, onSaved }: {
  initial: StatusPage | null; sites: string[]; onCancel: () => void; onSaved: (p: StatusPage, link?: string) => void
}) {
  const [name, setName] = useState(initial?.name || '')
  const [selSites, setSelSites] = useState<string[]>(initial?.sites || [])
  const [cidrs, setCidrs] = useState(initial?.allow_cidrs || '')
  // Expiry: keep an existing date as is unless another choice is picked.
  const [expiry, setExpiry] = useState<number>(initial && initial.expires_at ? -1 : 0)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onCancel() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onCancel])
  async function save(e: FormEvent) {
    e.preventDefault(); setErr('')
    const expires_at = expiry === -1 ? (initial?.expires_at || 0) : expiry === 0 ? 0 : Math.floor(Date.now() / 1000) + expiry * 86400
    setBusy(true)
    const res = await fetch(initial ? `/api/status-pages/${initial.id}` : '/api/status-pages', {
      method: initial ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name, sites: selSites, allow_cidrs: cidrs, expires_at }),
    }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { setErr(await errText(res, 'Could not save the status page')); return }
    const j = await res.json()
    if (initial) onSaved(j as StatusPage)
    else onSaved(j.page as StatusPage, j.link as string)
  }
  return createPortal(
    <div className="dlg-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onCancel() }}>
      <form className="dlg" role="dialog" aria-modal="true" onSubmit={save} style={{ maxWidth: 'min(640px, 94vw)', maxHeight: 'calc(100dvh - 32px)', display: 'flex', flexDirection: 'column' }}>
        <div className="dlg-title">{initial ? `Edit ${initial.name}` : 'Add status page'}</div>
        <div className="dlg-scroll"><div className="chan-form">
          <ChanSection title="Page">
            <label className="chan-field"><span className="flabel">Name</span>
              <input className="input" placeholder="e.g. Rack room screen" value={name} onChange={(e) => setName(e.target.value)} required />
            </label>
            <div className="chan-field"><span className="flabel">Sites</span>
              <SitePicker options={sites} value={selSites} onChange={setSelSites} />
            </div>
          </ChanSection>
          <ChanSection title="Who can open it" note="The link works from anywhere unless you list the networks it may be opened from. It can also expire.">
            <label className="chan-field"><span className="flabel">Allowed networks</span>
              <input className="input" placeholder="e.g. 10.0.0.0/24, 192.168.1.20 (empty = any network)" value={cidrs} onChange={(e) => setCidrs(e.target.value)} />
            </label>
            <label className="chan-field" style={{ maxWidth: 260 }}><span className="flabel">Link expires</span>
              <Select value={expiry} onChange={(e) => setExpiry(Number(e.target.value))}>
                {initial && initial.expires_at > 0 && <option value={-1}>Keep: {new Date(initial.expires_at * 1000).toLocaleDateString()}</option>}
                {STATUS_EXPIRY_DAYS.map((d) => <option key={d} value={d}>{d === 0 ? 'Never' : d === 1 ? 'In 1 day' : d === 365 ? 'In 1 year' : `In ${d} days`}</option>)}
              </Select>
            </label>
          </ChanSection>
          {err && <p className="set-note txt-err" style={{ margin: 0 }}>{err}</p>}
        </div></div>
        <div className="chan-dlg-foot">
          <div style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
            <button type="button" className="btn" onClick={onCancel}>Cancel</button>
            <button type="submit" className="btn primary" disabled={busy || !name.trim()}>{initial ? 'Save changes' : 'Add status page'}</button>
          </div>
        </div>
      </form>
    </div>,
    document.body,
  )
}

// StatusLinkDialog shows a status page's link once: Argus keeps only a hash of it.
function StatusLinkDialog({ name, link, onClose }: { name: string; link: string; onClose: () => void }) {
  return createPortal(
    <div className="dlg-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="dlg" role="dialog" aria-modal="true" style={{ maxWidth: 'min(640px, 94vw)' }}>
        <div className="dlg-title">Link for {name}</div>
        <p className="dlg-msg">Open this link on the screen. Once opened, the screen remembers it and the address bar shows a plain /status. You can copy it again later with Show link; New link replaces it and shuts the old one out.</p>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginTop: 12 }}>
          <input className="input mono" readOnly value={link} onFocus={(e) => e.target.select()} style={{ flex: 1, minWidth: 0 }} />
          <CopyButton text={link} />
        </div>
        <div className="dlg-foot"><Button variant="primary" onClick={onClose}>Done</Button></div>
      </div>
    </div>,
    document.body,
  )
}

// ThresholdsView (§D): a list of monitoring templates; clicking one opens a dialog to edit its
// fleet-wide threshold defaults. Per-device overrides live in each host's settings dialog. Admin-only.
function ThresholdsView() {
  const [data, setData] = useState<ThresholdsData | null>(null)
  const [err, setErr] = useState('')
  const [open, setOpen] = useState<string | null>(null)
  function load() { fetch('/api/thresholds').then((r) => (r.ok ? r.json() : Promise.reject())).then((d: ThresholdsData) => { setData(d); setErr('') }).catch(() => setErr('Could not load thresholds')) }
  useEffect(load, [])
  function onRowSaved(template: string, macro: string, value: string) {
    setData((d) => d ? { ...d, templates: d.templates.map((t) => t.template === template ? { ...t, thresholds: t.thresholds.map((r) => r.macro === macro ? { ...r, value } : r) } : t) } : d)
  }
  if (err && !data) return <div className="txt-err" style={{ fontSize: 13 }}>{err}</div>
  if (!data) return <div style={{ color: 'var(--muted)', fontSize: 13 }}>Loading…</div>
  const active = open ? data.templates.find((t) => t.template === open) : null
  return (
    <div className="thr-view">
      <p className="set-note">Fleet-wide alert thresholds, grouped by the monitoring template that carries them. Click a template to edit its defaults - a change applies to every host using it. Override for a single device in that host's settings.</p>
      <div className="thr-list">
        {data.templates.map((t) => {
          const overrides = t.thresholds.filter((r) => r.value).length
          return (
            <button className="thr-list-row" key={t.template} onClick={() => setOpen(t.template)}>
              <span className="thr-list-main">
                <span className="thr-list-title">{t.label}</span>
                <span className="thr-list-scope">{thrScopeText(t)}</span>
              </span>
              <span className="thr-list-meta">
                <span>{t.thresholds.length} threshold{t.thresholds.length === 1 ? '' : 's'}{overrides ? ` · ${overrides} customized` : ''}</span>
                <span className="thr-list-caret">›</span>
              </span>
            </button>
          )
        })}
      </div>
      {active && <ThresholdDialog tpl={active} onClose={() => { setOpen(null); load() }} onSaved={onRowSaved} />}
    </div>
  )
}

// The HTTP add-on's URL list ({$HTTP.URLS}) as rows: each URL with its own certificate check and the
// text its page must (or must not) contain, written back as "url#tls=...&text=..." entries, the way
// argus_http.py reads them (the older "url#text" / "url#!text" entries are read too). A row always
// names its certificate check: one stored without it follows the add-on's, so it shows that one.
type UrlRow = { url: string; tls: '' | 'verify' | 'self-signed' | 'ignore'; text: string; absent: boolean }
const URL_MODES = ['verify', 'self-signed', 'ignore'] as const
function decodeSafe(s: string): string { try { return decodeURIComponent(s) } catch { return s } }
function parseUrlList(v: string): UrlRow[] {
  return (v || '').split(/[,\s]+/).filter(Boolean).map((e) => {
    const i = e.indexOf('#')
    const r: UrlRow = { url: i < 0 ? e : e.slice(0, i), tls: '', text: '', absent: false }
    const frag = i < 0 ? '' : e.slice(i + 1)
    if (frag && !frag.includes('=')) { r.absent = frag.startsWith('!'); r.text = decodeSafe(r.absent ? frag.slice(1) : frag) }
    else if (frag) {
      for (const p of frag.split('&')) {
        const j = p.indexOf('='), k = j < 0 ? p : p.slice(0, j), val = decodeSafe(j < 0 ? '' : p.slice(j + 1))
        if (k === 'tls' && (URL_MODES as readonly string[]).includes(val)) r.tls = val as UrlRow['tls']
        else if (k === 'text' || k === 'notext') { r.text = val; r.absent = k === 'notext' }
      }
    }
    return r
  })
}
function serializeUrlList(rows: UrlRow[]): string {
  return rows.filter((r) => r.url.trim()).map((r) => {
    const opts: string[] = []
    if (r.tls) opts.push('tls=' + r.tls)
    if (r.text.trim()) opts.push((r.absent ? 'notext=' : 'text=') + encodeURIComponent(r.text.trim()))
    return r.url.trim() + (opts.length ? '#' + opts.join('&') : '')
  }).join(', ')
}
// urlRowProblem is what's wrong with a typed URL ("" when fine): the checks Argus and the collector make.
function urlRowProblem(u: string): string {
  const s = u.trim()
  if (!s) return ''
  if (/[\s,]/.test(s)) return 'One URL per row, without spaces or commas.'
  if (s.includes('#')) return 'Put the text the page must have in the text field, not after #.'
  if (!/^[A-Za-z0-9._~:/?[\]@!&'()*+;=%-]+$/.test(s)) return 'It has characters a URL can’t have (quotes, $ or a backslash).'
  if (s.startsWith('/')) return ''
  try {
    const x = new URL(s.includes('://') ? s : 'https://' + s)
    if (x.protocol !== 'http:' && x.protocol !== 'https:') return 'Only http and https URLs can be checked.'
    if (x.username || x.password) return 'A user name or password in the URL isn’t supported.'
  } catch { return 'This isn’t a URL (https://portal.example.com/app), a host (10.0.0.20:8443) or a path (/login).' }
  return ''
}
// httpFieldUsed says whether one of the HTTP add-on's other fields applies to the URL list as it
// stands, so host settings show only those: a blank list checks the host itself on all of them;
// otherwise Scheme serves hosts and paths without one, Port paths, and Certificate nothing (each row
// sets its own).
function httpFieldUsed(macro: string, urls: string): boolean {
  const rows = parseUrlList(urls)
  if (rows.length === 0) return true
  if (macro === '{$HTTP.SCHEME}') return rows.some((r) => !r.url.includes('://'))
  if (macro === '{$HTTP.PORT}') return rows.some((r) => r.url.startsWith('/'))
  return macro !== '{$HTTP.TLS.VERIFY}'
}
// The Common SaaS add-on's list ({$SAAS.URLS}): "address#name=Name" entries, comma separated.
type SaaSRow = { url: string; name: string }
const SAAS_MAX = 16

function parseSaaSList(v: string): SaaSRow[] {
  return v.split(/[,\s]+/).filter(Boolean).map((e) => {
    const [url, frag = ''] = e.split('#', 2)
    let name = ''
    for (const part of frag.split('&')) {
      const [k, val = ''] = part.split('=', 2)
      if (k === 'name') { try { name = decodeURIComponent(val) } catch { name = val } }
    }
    return { url, name }
  })
}

function serializeSaaSList(rows: SaaSRow[]): string {
  return rows.filter((r) => r.url.trim()).map((r) => r.url.trim() + (r.name.trim() ? '#name=' + encodeURIComponent(r.name.trim()) : '')).join(', ')
}

// SaaSEditor picks the services the Common SaaS add-on checks: the catalog's as a checklist, and any
// other address with a name of its own.
function SaaSEditor({ value, services, onChange, disabled }: { value: string; services: SaaSService[]; onChange: (v: string) => void; disabled?: boolean }) {
  const [rows, setRows] = useState<SaaSRow[]>(() => parseSaaSList(value))
  const known = new Set(services.map((x) => x.url))
  const custom = rows.map((r, i) => ({ r, i })).filter(({ r }) => !known.has(r.url))
  const full = rows.length >= SAAS_MAX
  function update(next: SaaSRow[]) { setRows(next); onChange(serializeSaaSList(next)) }
  function toggle(sv: SaaSService, on: boolean) {
    if (on) update([...rows, { url: sv.url, name: sv.name }])
    else update(rows.filter((r) => r.url !== sv.url))
  }
  return (
    <div className="saas-ed">
      <div className="saas-list">
        {services.map((sv) => {
          const on = rows.some((r) => r.url === sv.url)
          return (
            <label key={sv.id} className={'saas-item' + (on ? ' on' : '')} title={sv.url}>
              <input type="checkbox" checked={on} disabled={disabled || (!on && full)} onChange={(e) => toggle(sv, e.target.checked)} />
              {sv.name}
            </label>
          )
        })}
      </div>
      {custom.map(({ r, i }) => (
        <div className="saas-custom" key={i}>
          <input className="input" value={r.name} placeholder="Name, e.g. Our ERP" maxLength={60} disabled={disabled} aria-label="Service name"
            onChange={(e) => update(rows.map((x, n) => (n === i ? { ...x, name: e.target.value } : x)))} />
          <input className={'input' + (r.url && urlRowProblem(r.url) ? ' bad' : '')} value={r.url} placeholder="https://erp.example.com/health" disabled={disabled} aria-label="Service address"
            onChange={(e) => update(rows.map((x, n) => (n === i ? { ...x, url: e.target.value } : x)))} />
          <Button variant="ghost" className="compact" disabled={disabled} onClick={() => update(rows.filter((_, n) => n !== i))}>Remove</Button>
        </div>
      ))}
      <div className="saas-foot">
        <Button variant="ghost" className="compact" disabled={disabled || full} onClick={() => update([...rows, { url: '', name: '' }])}>+ Another service</Button>
        <span className="sub-line">{rows.length} of {SAAS_MAX}. Any answer short of a server error counts: a login page still says the service is there.</span>
      </div>
    </div>
  )
}

function HttpUrlsEditor({ value, tlsDefault, onChange, disabled }: { value: string; tlsDefault: string; onChange: (v: string) => void; disabled?: boolean }) {
  const def = ((URL_MODES as readonly string[]).includes(tlsDefault) ? tlsDefault : 'verify') as UrlRow['tls']
  const [rows, setRows] = useState<UrlRow[]>(() => parseUrlList(value).map((r) => ({ ...r, tls: r.tls || def })))
  function update(next: UrlRow[]) { setRows(next); onChange(serializeUrlList(next)) }
  function set(i: number, p: Partial<UrlRow>) { update(rows.map((r, n) => (n === i ? { ...r, ...p } : r))) }
  return (
    <div className="url-rows">
      {rows.length === 0 && <div className="hs-note" style={{ margin: '0 0 6px' }}>No URLs: the host itself is checked, on the scheme, port and certificate check below.</div>}
      {rows.map((r, i) => {
        const prob = urlRowProblem(r.url)
        return (
          <div className="url-row" key={i}>
            <input className={'input url-url' + (prob ? ' bad' : '')} value={r.url} disabled={disabled} aria-label="URL"
              placeholder="https://portal.example.com/app, 10.0.0.20:8443 or /login" onChange={(e) => set(i, { url: e.target.value })} />
            <Select value={r.tls} disabled={disabled} aria-label="Certificate" onChange={(e) => set(i, { tls: e.target.value as UrlRow['tls'] })}>
              {URL_MODES.map((m) => <option key={m} value={m}>Certificate: {m}</option>)}
            </Select>
            <Select value={r.absent ? 'notext' : 'text'} disabled={disabled} aria-label="Page text" onChange={(e) => set(i, { absent: e.target.value === 'notext' })}>
              <option value="text">Page contains</option>
              <option value="notext">Page doesn’t contain</option>
            </Select>
            <input className="input" value={r.text} disabled={disabled} placeholder="text (optional)" aria-label="Text" onChange={(e) => set(i, { text: e.target.value })} />
            {!disabled && <button type="button" className="btn ghost url-del" aria-label="Remove this URL" title="Remove" onClick={() => update(rows.filter((_, n) => n !== i))}>✕</button>}
            {prob && <div className="url-err">{prob}</div>}
          </div>
        )
      })}
      {!disabled && rows.length < 16 && <div className="hs-add"><Button onClick={() => update([...rows, { url: '', tls: def, text: '', absent: false }])}>+ Add URL</Button></div>}
    </div>
  )
}

type PushSensor = { id: number; host_id: string; name: string; late_secs: number; missed_secs: number; created_at: number; created_by?: string; last_at?: number; last_ok: boolean; last_msg?: string; runs: number; url?: string }

// A push sensor's times are entered as a number and a unit; PUSH_UNITS are the units offered.
const PUSH_UNITS: { s: number; label: string }[] = [{ s: 60, label: 'minutes' }, { s: 3600, label: 'hours' }, { s: 86400, label: 'days' }]
function pushParts(secs: number): [string, number] {
  for (const u of [...PUSH_UNITS].reverse()) if (secs >= u.s && secs % u.s === 0) return [String(secs / u.s), u.s]
  return [String(Math.round(secs / 60)), 60]
}

// pushExamples are the calls a job makes at the end of its run, for Linux (curl) and Windows
// (PowerShell). A run that went wrong sends status=fail; msg is optional and shows as the reason.
function pushExamples(url: string): string {
  return [
    '# Linux, macOS, a NAS (curl): at the end of the job',
    `curl -fsS -m 10 --retry 3 "${url}?status=ok&msg=Backup+done"`,
    '# ...or when it went wrong',
    `curl -fsS -m 10 --retry 3 "${url}?status=fail&msg=Backup+failed"`,
    '',
    '# Windows (PowerShell)',
    `Invoke-RestMethod -Method Post -Uri "${url}" -Body @{ status = 'ok'; msg = 'Backup done' }`,
  ].join('\n')
}

// PushSensorsSection lists a host's push sensors (jobs that report their runs to Argus) with each
// one's last run, and lets admins and helpdesk add, edit, re-key and delete them. It saves each
// change at once, apart from the host settings' Save.
// HsSection is one foldable section of host settings. Folded, its title row says what the section
// holds (summary; warn colours it when something there needs a look), so a host left at its defaults
// reads as a short list. The title opens and closes it.
function HsSection({ title, summary, warn, open, onToggle, children }: { title: string; summary?: string; warn?: boolean; open: boolean; onToggle: () => void; children: ReactNode }) {
  return (
    <section className="hs-sec">
      <button type="button" className="hs-title hs-toggle" aria-expanded={open} onClick={onToggle}>
        <svg className={'chev' + (open ? ' open' : '')} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" aria-hidden="true"><path d="M9 6l6 6-6 6" /></svg>
        <span className="hs-toggle-txt">
          <span>{title}</span>
          {!open && summary && <span className={'hs-toggle-sum' + (warn ? ' warn' : '')}>{summary}</span>}
        </span>
      </button>
      {open && <div className="hs-sec-body">{children}</div>}
    </section>
  )
}
// hsShort shortens a value for a folded section's summary.
function hsShort(s: string): string { return s.length > 40 ? s.slice(0, 39) + '…' : s }

function PushSensorsSection({ hostId, canEdit, open, onToggle }: { hostId: string; canEdit: boolean; open: boolean; onToggle: () => void }) {
  const toast = useToast()
  const confirm = useConfirm()
  const [list, setList] = useState<PushSensor[] | null>(null)
  const [editing, setEditing] = useState<PushSensor | 'new' | null>(null)
  const [shown, setShown] = useState<number | null>(null)
  function load() {
    fetch(`/api/hosts/${hostId}/push`).then((r) => (r.ok ? r.json() : Promise.reject())).then((l: PushSensor[]) => setList(l || [])).catch(() => setList([]))
  }
  useEffect(load, [hostId]) // eslint-disable-line react-hooks/exhaustive-deps
  const abs = (u?: string) => (!u ? '' : u.startsWith('/') ? window.location.origin + u : u)

  async function rotate(p: PushSensor) {
    if (!(await confirm({ title: 'New URL', message: `Give ${p.name} a new URL? The current one stops working at once, so update the job to call the new one.`, confirmLabel: 'New URL', danger: true }))) return
    const res = await fetch(`/api/push-sensors/${p.id}/rotate`, { method: 'POST' })
    if (!res.ok) { toast.error(await errText(res, 'Could not change the URL')); return }
    toast.success('New URL made. Update the job to call it.'); setShown(p.id); load()
  }
  async function del(p: PushSensor) {
    if (!(await confirm({ title: 'Delete push sensor', message: `Delete ${p.name}? Its URL stops working at once, and its sensors stop and are removed with their history within the hour.`, confirmLabel: 'Delete', danger: true }))) return
    const res = await fetch(`/api/push-sensors/${p.id}`, { method: 'DELETE' })
    if (!res.ok) { toast.error(await errText(res, 'Could not delete the push sensor')); return }
    toast.success('Push sensor deleted.'); load()
  }

  if (list === null) return null
  if (!canEdit && list.length === 0) return null
  const failed = list.filter((p) => p.last_at && !p.last_ok)
  return (
    <HsSection title="Push sensors" open={open} onToggle={onToggle} warn={failed.length > 0}
      summary={list.length === 0 ? 'none' : list.map((p) => p.name + (p.last_at && !p.last_ok ? ' (failed)' : '')).join(', ')}>
      <div className="hs-note" style={{ margin: '0 0 10px' }}>Jobs that report to Argus when they run: a backup, a cron job, a scheduled task. Each has its own URL the job calls at the end, with <span className="mono">status=ok</span> or <span className="mono">status=fail</span> and an optional <span className="mono">msg</span>. A failed run is an error; no run for longer than the late time is a warning, longer than the missed time an error. The sensors show up within a minute. Changes here are saved at once.</div>
      {list.map((p) => (
        <div className="push-row" key={p.id}>
          <div className="push-head">
            <span className={'sdot' + (p.last_at && !p.last_ok ? ' pulse' : '')} style={{ '--dot': !p.last_at ? 'var(--faint)' : p.last_ok ? 'var(--ok)' : 'var(--err)' } as CSSProperties} />
            <b className="push-name">{p.name}</b>
            <span className="push-meta">
              {!p.last_at ? 'No run yet' : `${p.last_ok ? 'OK' : 'Failed'} ${relTime(p.last_at)}`}{p.last_at && p.last_msg ? `: ${p.last_msg}` : ''}
            </span>
            {canEdit && (
              <span className="push-actions">
                <Button variant="ghost" onClick={() => setShown(shown === p.id ? null : p.id)}>{shown === p.id ? 'Hide URL' : 'URL'}</Button>
                <Kebab actions={[
                  { label: 'Edit…', icon: kbIcon.edit, onClick: () => setEditing(p) },
                  { label: 'New URL…', onClick: () => rotate(p) },
                  { sep: true, label: '' },
                  { label: 'Delete', icon: kbIcon.trash, danger: true, onClick: () => del(p) },
                ]} />
              </span>
            )}
          </div>
          <div className="push-sub">Late after {fmtDuration(p.late_secs)}, missed after {fmtDuration(p.missed_secs)} · {p.runs} run{p.runs === 1 ? '' : 's'} reported</div>
          {shown === p.id && p.url && (
            <div className="push-url">
              <div className="push-urlline"><span className="mono">{abs(p.url)}</span><CopyButton text={abs(p.url)} /></div>
              <pre className="push-pre"><code>{pushExamples(abs(p.url))}</code></pre>
              <div className="push-sub">Anyone with this URL can report runs for this job, so keep it in the job's settings, not in a shared document. <b>New URL</b> replaces it.</div>
            </div>
          )}
          {editing !== 'new' && editing?.id === p.id && <PushEditor hostId={hostId} initial={p} onDone={(saved) => { setEditing(null); if (saved) load() }} />}
        </div>
      ))}
      {editing === 'new' && (
        <div className="push-row">
          <b className="push-name">New push sensor</b>
          <PushEditor hostId={hostId} initial={null} onDone={(saved) => { setEditing(null); if (saved) { setShown(saved.id); load() } }} />
        </div>
      )}
      {canEdit && editing !== 'new' && <div className="hs-add"><Button onClick={() => setEditing('new')}>+ Add push sensor</Button></div>}
    </HsSection>
  )
}

function PushEditor({ hostId, initial, onDone }: { hostId: string; initial: PushSensor | null; onDone: (saved: PushSensor | null) => void }) {
  const toast = useToast()
  const [name, setName] = useState(initial?.name || '')
  const [late, setLate] = useState(pushParts(initial?.late_secs || 25 * 3600))
  const [missed, setMissed] = useState(pushParts(initial?.missed_secs || 49 * 3600))
  const [busy, setBusy] = useState(false)
  async function save() {
    setBusy(true)
    const body = { name, late_secs: Math.round(Number(late[0]) * late[1]), missed_secs: Math.round(Number(missed[0]) * missed[1]) }
    const res = await fetch(initial ? `/api/push-sensors/${initial.id}` : `/api/hosts/${hostId}/push`, { method: initial ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not save the push sensor')); return }
    toast.success(initial ? 'Push sensor saved.' : 'Push sensor added. Copy its URL into the job.')
    onDone(await res.json())
  }
  const dur = (v: [string, number], set: (v: [string, number]) => void) => (
    <span className="push-dur">
      <input className="input" type="text" inputMode="numeric" value={v[0]} onChange={(e) => set([e.target.value, v[1]])} />
      <Select value={v[1]} onChange={(e) => set([v[0], Number(e.target.value)])}>{PUSH_UNITS.map((u) => <option key={u.s} value={u.s}>{u.label}</option>)}</Select>
    </span>
  )
  return (
    <div className="push-edit">
      <div className="hs-grid">
        <label className="field"><span>Name</span><input className="input" value={name} placeholder="e.g. Nightly backup" maxLength={64} onChange={(e) => setName(e.target.value)} /></label>
        <div />
        <label className="field"><span>Warning when no run for</span>{dur(late, setLate)}</label>
        <label className="field"><span>Error when no run for</span>{dur(missed, setMissed)}</label>
      </div>
      <div className="hs-note">For a daily job, 25 and 49 hours give it an hour of slack before each.</div>
      <div className="hs-add">
        <Button variant="primary" disabled={busy || !name.trim()} onClick={save}>{busy ? 'Saving…' : initial ? 'Save push sensor' : 'Add push sensor'}</Button>
        <Button variant="ghost" onClick={() => onDone(null)}>Cancel</Button>
      </div>
    </div>
  )
}

function HostSettingsModal({ hostId, hostName, canEdit, isAdmin, onClose, onSaved }: { hostId: string; hostName?: string; canEdit: boolean; isAdmin?: boolean; onClose: () => void; onSaved: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])
  return createPortal(
    <div className="dlg-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="dlg" role="dialog" aria-modal="true" style={{ maxWidth: 'min(980px, 94vw)', maxHeight: 'calc(100dvh - 32px)', display: 'flex', flexDirection: 'column' }}>
        <div className="dlg-title">Host settings{hostName ? ` · ${hostName}` : ''}</div>
        <div className="dlg-scroll"><HostSettings hostId={hostId} canEdit={canEdit} isAdmin={isAdmin} onClose={onClose} onSaved={onSaved} inDialog /></div>
      </div>
    </div>,
    document.body,
  )
}

// HostSettings is the editor for a host's identity + interfaces (Zabbix
// host.update + hostinterface CRUD). One "Save" reconciles the whole desired state on the server.
function HostSettings({ hostId, canEdit, isAdmin, onClose, onSaved, inDialog }: { hostId: string; canEdit: boolean; isAdmin?: boolean; onClose: () => void; onSaved: () => void; inDialog?: boolean }) {
  const rootCls = 'host-settings' + (inDialog ? ' in-dlg' : '')
  const confirm = useConfirm()
  const toast = useToast()
  const [cfg, setCfg] = useState<HostCfg | null>(null)
  const [proxies, setProxies] = useState<Proxy[]>([])
  const [err, setErr] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [customOrder, setCustomOrder] = useState(false)
  // Every section starts folded (most hosts keep their defaults); openSecs holds the ones opened.
  const [openSecs, setOpenSecs] = useState<Set<string>>(() => new Set())
  const sec = (id: string) => ({ open: openSecs.has(id), onToggle: () => setOpenSecs((s) => { const n = new Set(s); if (!n.delete(id)) n.add(id); return n }) })
  const [masterChoice, setMasterChoice] = useState('default') // 'default' | 'none' | a sensor id
  const [reason, setReason] = useState('') // why, for the change log (optional)
  const [allTags] = useTags()
  const [ownTags, setOwnTags] = useState<string[]>([])
  const [own, setOwn] = useState<{ asset_tag: string; location: string }>({ asset_tag: '', location: '' })
  const [upMode, setUpMode] = useState<'auto' | 'manual' | 'none'>('auto')
  const [upHost, setUpHost] = useState('')
  const [allHosts, setAllHosts] = useState<Host[]>([])
  const [links, setLinks] = useState<LinkRow[]>([])
  function loadCfg() {
    fetch(`/api/hosts/${hostId}/config`).then((r) => (r.ok ? r.json() : Promise.reject())).then((d: HostCfg) => {
      setCfg({ ...d, addons: d.addons?.map((a) => ({ ...a, macros: a.macros?.map((m) => ({ ...m, loaded: m.value })) })) })
      setCustomOrder(!!(d.category_order && d.category_order.length))
      setOwnTags((d.tags || []).filter((t) => !t.from).map((t) => t.name))
      setOwn(d.own || { asset_tag: '', location: '' })
      setUpMode(d.upstream?.mode || 'auto'); setUpHost(d.upstream?.manual_host || '')
      setLinks((d.links || []).map((l) => ({ label: l.label, url: l.url })))
      setMasterChoice(!d.master || !d.master.custom ? 'default' : d.master.item_id || 'none')
    }).catch(() => setErr('Could not load host settings'))
  }
  useEffect(() => {
    loadCfg()
    fetch('/api/proxies').then((r) => (r.ok ? r.json() : [])).then((p) => setProxies(p || [])).catch(() => {})
    fetch('/api/hosts').then((r) => (r.ok ? r.json() : [])).then((h) => setAllHosts(h || [])).catch(() => {})
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hostId])

  function patch(p: Partial<HostCfg>) { setCfg((c) => (c ? { ...c, ...p } : c)) }
  function setIface(idx: number, p: Partial<Iface>) { setCfg((c) => (c ? { ...c, interfaces: c.interfaces.map((i, n) => (n === idx ? { ...i, ...p } : i)) } : c)) }
  function setSnmp(idx: number, p: Partial<SnmpCfg>) { setCfg((c) => (c ? { ...c, interfaces: c.interfaces.map((i, n) => (n === idx ? { ...i, snmp: { ...(i.snmp || blankSnmp()), ...p } } : i)) } : c)) }
  function setMacro(macro: string, value: string) { setCfg((c) => (c ? { ...c, macros: (c.macros || []).map((m) => (m.macro === macro ? { ...m, value } : m)) } : c)) }
  function setThreshold(macro: string, value: string) { setCfg((c) => (c ? { ...c, thresholds: (c.thresholds || []).map((t) => (t.macro === macro ? { ...t, value } : t)) } : c)) }
  function setAddon(id: string, p: Partial<AddOnCfg>) { setCfg((c) => (c ? { ...c, addons: (c.addons || []).map((a) => (a.id === id ? { ...a, ...p } : a)) } : c)) }
  function setAddonMacro(id: string, macro: string, value: string) { setCfg((c) => (c ? { ...c, addons: (c.addons || []).map((a) => (a.id === id ? { ...a, macros: (a.macros || []).map((m) => (m.macro === macro ? { ...m, value } : m)) } : a)) } : c)) }
  // An option under a third party's terms (Ookla's speed test) is chosen only once they're accepted
  // here; the save carries the acceptance and the change log records who accepted them.
  async function chooseAddonOption(a: AddOnCfg, m: AddOnMacro, value: string) {
    const t = m.terms
    if (t && value === t.value && m.loaded !== t.value && !(a.accepted || []).includes(m.macro)) {
      const ok = await confirm({
        title: t.title, confirmLabel: 'Accept and use it',
        message: (
          <div className="terms-msg">
            <p>{t.text}</p>
            <p>{t.links.map((l, i) => <span key={l.url}>{i > 0 && ' · '}<a href={l.url} target="_blank" rel="noreferrer">{l.label}</a></span>)}</p>
          </div>
        ),
      })
      if (!ok) return
      setAddon(a.id, { accepted: [...(a.accepted || []), m.macro] })
    }
    setAddonMacro(a.id, m.macro, value)
  }
  function moveCategory(idx: number, dir: -1 | 1) {
    setCfg((c) => {
      if (!c || !c.categories) return c
      const cats = [...c.categories]
      const j = idx + dir
      if (j < 0 || j >= cats.length) return c
      ;[cats[idx], cats[j]] = [cats[j], cats[idx]]
      return { ...c, categories: cats }
    })
  }
  function addIface(type: number) { setCfg((c) => (c ? { ...c, interfaces: [...c.interfaces, { type, useip: 1, ip: '', dns: '', port: type === 2 ? '161' : '10050', snmp: type === 2 ? blankSnmp() : undefined, inherit: type === 2 ? !!c.proxy_default : undefined }] } : c)) }
  async function removeIface(idx: number) {
    const it = cfg?.interfaces[idx]
    if (it?.interfaceid && !(await confirm({ title: 'Remove interface', message: 'Remove this interface? Any checks still using it will be moved to another interface on this host. If a check needs an interface of the same type (e.g. a Zabbix-agent check), the removal is refused and nothing changes.', confirmLabel: 'Remove', danger: true }))) return
    setCfg((c) => (c ? { ...c, interfaces: c.interfaces.filter((_, n) => n !== idx) } : c))
  }
  async function save() {
    if (!cfg) return
    setBusy(true); setErr(null)
    // Class options and per-host threshold overrides share the one macro map the server applies (only
    // the class's own macros + its known thresholds are ever touched; a blank field reverts to default).
    const pairs = [...(cfg.macros || []).map((m) => [m.macro, m.value] as const), ...(cfg.thresholds || []).map((t) => [t.macro, t.value || ''] as const)]
    const macros = pairs.length > 0 ? Object.fromEntries(pairs) : undefined
    // Per-host sensor order: send the current list when "custom" is on, else [] to clear the override.
    const category_order = cfg.categories && cfg.categories.length > 0 ? (customOrder ? cfg.categories : []) : undefined
    const addons = cfg.addons ? Object.fromEntries(cfg.addons.map((a) => [a.id, { enabled: a.enabled, macros: Object.fromEntries((a.macros || []).map((m) => [m.macro, m.value])), accepted: a.accepted }])) : undefined
    const res = await fetch(`/api/hosts/${hostId}/config`, { method: 'PATCH', headers: { 'Content-Type': 'application/json', ...reasonHeader(reason) }, body: JSON.stringify({ host: cfg.host, name: cfg.name, monitored_by: cfg.monitored_by, proxy_id: cfg.proxy_id, interfaces: cfg.interfaces, macros, category_order, addons, master: cfg.master ? masterChoice : undefined, tags: ownTags, own, upstream: { mode: upMode, host_id: upMode === 'manual' ? upHost : '' }, links: links.filter((l) => l.label.trim() || l.url.trim()) }) }).catch(() => null)
    setBusy(false)
    // The error also pops up: the dialog is long, and its line by the Save button may be scrolled away.
    if (!res || !res.ok) { const m = await errText(res, 'Could not save host settings'); setErr(m); toast.error(m); return }
    onSaved()
  }

  if (err && !cfg) return <div className={rootCls}><div style={{ color: 'var(--err)', fontSize: 13 }}>{err}</div><div className="hs-foot"><Button variant="ghost" onClick={onClose}>Close</Button></div></div>
  if (!cfg) return <div className={rootCls}><span style={{ color: 'var(--muted)', fontSize: 13 }}>Loading…</span></div>

  return (
    <div className={rootCls}>
      <div className="hs-grid">
        <label className="field"><span>Visible name</span><input className="input" value={cfg.name} disabled={!canEdit} onChange={(e) => patch({ name: e.target.value })} /></label>
        <label className="field"><span>Technical name</span><input className="input" value={cfg.host} disabled={!canEdit} onChange={(e) => patch({ host: e.target.value })} /></label>
      </div>
      <div className="hs-note">Renaming the technical name is safe in Zabbix (references update automatically) - avoid it only if external scripts reference this host.</div>

      <div className="hs-mon">
        <span className="hs-monlabel">Monitored by</span>
        <div className="seg">
          <button className={cfg.monitored_by === 0 ? 'on' : ''} disabled={!canEdit} onClick={() => patch({ monitored_by: 0 })}>Server</button>
          {/* "0" is the server-monitored sentinel, not a proxy id - flipping to Proxy must land on
              a real proxy or the select silently shows one the state doesn't hold. */}
          <button className={cfg.monitored_by === 1 ? 'on' : ''} disabled={!canEdit} onClick={() => patch({ monitored_by: 1, proxy_id: cfg.proxy_id && cfg.proxy_id !== '0' ? cfg.proxy_id : proxies[0]?.id })}>Proxy</button>
        </div>
        {cfg.monitored_by === 1 && (
          <select className="input" value={cfg.proxy_id || ''} disabled={!canEdit} onChange={(e) => patch({ proxy_id: e.target.value })}>
            <option value="" disabled>Select a proxy…</option>
            {proxies.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
          </select>
        )}
      </div>

      {isAdmin && cfg.class_id && <ClassChanger hostId={hostId} currentClassId={cfg.class_id} currentClassLabel={cfg.class_label} onChanged={loadCfg} />}
      <div className="hs-mon hs-tags">
        <span className="hs-monlabel">Tags</span>
        <TagsEditor all={allTags} value={ownTags} onChange={setOwnTags} inherited={cfg.tags} disabled={!canEdit} />
      </div>

      <HsSection title="Interfaces" {...sec('ifaces')} summary={cfg.interfaces.length === 0 ? 'none'
        : cfg.interfaces.map((i) => `${IFTYPE[i.type] || 'Type ' + i.type} ${((i.useip === 1 ? i.ip : i.dns) || '(no address)') + (i.port ? ':' + i.port : '')}`).join(', ')}>
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
              {(cfg.monitored_by === 0 || cfg.monitored_by === 1) && (
                <label className="field if-inherit"><span>SNMP credentials</span>
                  <div className="seg">
                    <button className={i.inherit ? 'on' : ''} disabled={!canEdit || !cfg.proxy_default} onClick={() => setIface(idx, { inherit: true })}>Inherit from {cfg.proxy_name || (cfg.monitored_by === 0 ? 'the core server' : 'proxy')}</button>
                    <button className={!i.inherit ? 'on' : ''} disabled={!canEdit} onClick={() => setIface(idx, { inherit: false })}>Override</button>
                  </div>
                </label>
              )}
              {(cfg.monitored_by === 0 || cfg.monitored_by === 1) && !cfg.proxy_default && <div className="if-inherit-note">No SNMP default is set for {cfg.proxy_name || (cfg.monitored_by === 0 ? 'the core server' : 'this proxy')} yet - set one {cfg.monitored_by === 0 ? 'in Settings (Core SNMP default)' : 'in the Probes tab (its “SNMP defaults”)'} to enable inheritance.</div>}
              {i.inherit
                ? <div className="if-inherit-note">Using {cfg.proxy_name || (cfg.monitored_by === 0 ? 'the core server' : 'the proxy')}’s SNMP default{cfg.proxy_default ? ` (v${cfg.proxy_default.version === 2 ? '2c' : cfg.proxy_default.version}${cfg.proxy_default.version !== 3 ? `, community “${cfg.proxy_default.community}”` : ''})` : ''} - change it {cfg.monitored_by === 0 ? 'in Settings (Core SNMP default)' : 'in the Probes tab'}.</div>
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
      </HsSection>

      {cfg.macros && cfg.macros.length > 0 && (() => {
        const own = cfg.macros.filter((m) => m.value.trim() || (m.secret && m.set))
        const said = (m: MacroField) => m.macro === '{$XCP.VM.IGNORE}' ? `${m.value.split(',').filter((s) => s.trim()).length} VMs left out`
          : `${m.label} ${m.secret ? 'set' : hsShort(m.value.trim())}`
        return (
        <HsSection title={cfg.class_label ? cfg.class_label + ' options' : 'Monitoring options'} {...sec('class')}
          summary={own.length === 0 ? 'all at the defaults' : own.map(said).join(', ')}>
          {/* Intro line ABOVE the fields: rendered after the grid it strands below the tallest
              column (the XCP-NG VM checklist) and reads as an unaligned orphan. */}
          <div className="hs-note" style={{ margin: '0 0 10px' }}>These tune the class monitoring for this host. Leave a field blank to use the template default; changes take effect on the next discovery cycle.</div>
          <div className="hs-grid">
            {cfg.macros.map((m) => {
              // The XCP-NG ignored-VMs macro renders as a checklist of the discovered VMs (plus any
              // name already ignored, so it can be re-enabled even after its sensors aged out).
              // Checked = monitored; unchecked names are stored comma-separated in the macro. With
              // nothing to pick from (VM monitoring off, or nothing discovered yet) the field is
              // hidden entirely - a raw names input would be noise.
              if (m.macro === '{$XCP.VM.IGNORE}') {
                const ignored = m.value.split(',').map((s) => s.trim()).filter(Boolean)
                const vmNames = Array.from(new Set([...(cfg.vm_names || []), ...ignored])).sort()
                if (vmNames.length === 0) return null
                return (
                  <div className="field" key={m.macro}>
                    <span>{m.label}</span>
                    <div className="vm-list">
                      {vmNames.map((n) => {
                        const on = !ignored.includes(n)
                        return (
                          <label key={n} style={canEdit ? undefined : { cursor: 'default' }}>
                            <input type="checkbox" checked={on} disabled={!canEdit}
                              onChange={() => { const next = on ? [...ignored, n] : ignored.filter((x) => x !== n); setMacro(m.macro, next.join(',')) }} />
                            <span className={on ? undefined : 'off'}>{n}</span>
                          </label>
                        )
                      })}
                    </div>
                    <span style={{ marginTop: 5 }}>Unchecked VMs are excluded from the sensors and the VM counts.</span>
                  </div>
                )
                // Nothing discovered yet (VM monitoring off): fall through to the plain text input.
              }
              return (
                <label className="field" key={m.macro}>
                  <span>{m.label}</span>
                  {m.options && m.options.length > 0 ? (
                    // Fixed value set: a select, where blank keeps the template default.
                    <Select value={m.value} disabled={!canEdit} onChange={(e) => setMacro(m.macro, e.target.value)}>
                      <option value="">{m.hint ? `default (${m.hint})` : 'template default'}</option>
                      {m.options.map((o) => <option key={o} value={o}>{o}</option>)}
                    </Select>
                  ) : (
                    <input className="input" type={m.secret ? 'password' : 'text'} placeholder={m.secret && m.set ? 'unchanged' : (m.hint || '')} value={m.value} disabled={!canEdit} onChange={(e) => setMacro(m.macro, e.target.value)} />
                  )}
                  {m.hint && !(m.options && m.options.length > 0) && <span style={{ color: 'var(--muted)', fontSize: 11, marginTop: 3 }}>Example: {m.hint}</span>}
                </label>
              )
            })}
          </div>
        </HsSection>
        )
      })()}

      {cfg.addons && cfg.addons.length > 0 && (() => {
        const on = cfg.addons.filter((x) => x.enabled)
        const urls = (x: AddOnCfg) => parseUrlList(x.macros?.find((m) => m.macro === '{$HTTP.URLS}')?.value || '')
        const said = (x: AddOnCfg) => {
          if (x.id === 'http') { const n = urls(x).length; return `${x.label}: ${n === 0 ? 'the host itself' : n === 1 ? '1 URL' : n + ' URLs'}` }
          if (x.id === 'saas') { const n = parseSaaSList(x.macros?.find((m) => m.macro === '{$SAAS.URLS}')?.value || '').length; return `${x.label}: ${n === 1 ? '1 service' : n + ' services'}` }
          if (x.id === 'speedtest') {
            const eng = x.macros?.find((m) => m.macro === '{$SPEEDTEST.ENGINE}')
            return `${x.label}: ${eng ? eng.option_labels?.[eng.value] || eng.value : 'Cloudflare'}, every ${x.macros?.find((m) => m.macro === '{$SPEEDTEST.INTERVAL}')?.value || '6h'}`
          }
          const v = (x.macros?.[0]?.value || '').trim()
          return v ? `${x.label}: ${hsShort(v)}` : x.label
        }
        const bad = on.some((x) => x.id === 'http' && urls(x).some((r) => urlRowProblem(r.url)))
        return (
        <HsSection title="Add-ons" {...sec('addons')} warn={bad}
          summary={(on.length === 0 ? 'none on' : on.map(said).join(', ')) + (bad ? '; a URL has a problem' : '')}>
          <div className="hs-note" style={{ margin: '0 0 10px' }}>Optional Argus checks you can layer on this host. Turn one on and set its options; turning it off removes its sensors.</div>
          {cfg.addons.map((a) => (
            <div key={a.id} style={{ marginBottom: 10 }}>
              <label className="hs-note" style={{ display: 'flex', alignItems: 'center', gap: 8, margin: '0 0 6px', cursor: canEdit ? 'pointer' : 'default' }}>
                <input type="checkbox" checked={a.enabled} disabled={!canEdit} onChange={(e) => setAddon(a.id, { enabled: e.target.checked })} />
                <span><b style={{ color: 'var(--text)' }}>{a.label}</b> - {a.description}</span>
              </label>
              {a.enabled && a.macros && a.macros.length > 0 && (
                <div className="hs-grid">
                  {a.macros.filter((m) => {
                    if (m.show_if && (a.macros!.find((x) => x.macro === m.show_if!.macro)?.value || '') !== m.show_if.value) return false // another engine's option
                    const urls = a.macros!.find((x) => x.macro === '{$HTTP.URLS}')
                    return !urls || httpFieldUsed(m.macro, urls.value)
                  }).map((m) => m.macro === '{$SAAS.URLS}' ? (
                    <div className="field" key={m.macro} style={{ gridColumn: '1 / -1' }}>
                      <span>{m.label}</span>
                      <SaaSEditor value={m.value} services={a.services || []} disabled={!canEdit} onChange={(v) => setAddonMacro(a.id, m.macro, v)} />
                    </div>
                  ) : m.macro === '{$HTTP.URLS}' ? (
                    <div className="field" key={m.macro} style={{ gridColumn: '1 / -1' }}>
                      <span>{m.label}</span>
                      <HttpUrlsEditor value={m.value} disabled={!canEdit} onChange={(v) => setAddonMacro(a.id, m.macro, v)}
                        tlsDefault={a.macros!.find((x) => x.macro === '{$HTTP.TLS.VERIFY}')?.value || 'verify'} />
                    </div>
                  ) : (
                    <label className="field" key={m.macro}>
                      <span>{m.label}</span>
                      {m.options && m.options.length > 0
                        ? <Select value={m.value} disabled={!canEdit} onChange={(e) => chooseAddonOption(a, m, e.target.value)}>{m.options.map((o) => <option key={o} value={o}>{m.option_labels?.[o] || o}</option>)}</Select>
                        : <input className="input" value={m.value} placeholder={m.hint || ''} disabled={!canEdit} onChange={(e) => setAddonMacro(a.id, m.macro, e.target.value)} />}
                    </label>
                  ))}
                </div>
              )}
            </div>
          ))}
        </HsSection>
        )
      })()}

      {cfg.class_id !== 'probe' && <PushSensorsSection hostId={hostId} canEdit={canEdit} {...sec('push')} />}

      {cfg.master && (
        <HsSection title="Master sensor" {...sec('master')}
          summary={masterChoice === 'default'
            ? `Default: ${cfg.master.default_item_id ? cfg.master.options.find((o) => o.id === cfg.master!.default_item_id)?.label || 'ping' : 'none'}`
            : masterChoice === 'none' ? 'None' : cfg.master.options.find((o) => o.id === masterChoice)?.label || 'a sensor'}>
          <div className="hs-note" style={{ margin: '0 0 10px' }}>While this sensor is down, the host's other sensors don't send notifications, so an unreachable device alerts once instead of once per sensor. The held alerts go out if they're still open once it's back.{cfg.class_id === 'probe' ? " This probe's reporting sensor also holds the alerts of every device at its site while the probe is unreachable." : ''}</div>
          <label className="field" style={{ maxWidth: 420 }}>
            <span>Master</span>
            <Select value={masterChoice} disabled={!canEdit} onChange={(e) => setMasterChoice(e.target.value)}>
              <option value="default">{cfg.master.default_item_id
                ? `Default: ${cfg.master.options.find((o) => o.id === cfg.master!.default_item_id)?.label || 'ping'}`
                : 'Default: none (no ping sensor)'}</option>
              <option value="none">None: never hold this host's alerts</option>
              {cfg.master.options.filter((o) => o.id !== cfg.master!.default_item_id).map((o) => <option key={o.id} value={o.id}>{o.label}</option>)}
            </Select>
          </label>
        </HsSection>
      )}

      {cfg.thresholds && cfg.thresholds.length > 0 && (() => {
        const own = cfg.thresholds.filter((t) => (t.value || '').trim())
        return (
          <HsSection title="Thresholds" {...sec('thr')} summary={own.length === 0 ? 'all at the defaults'
            : `${own.length} set for this host: ` + own.map((t) => `${t.label} ${t.value!.trim()}${t.unit || ''}`).join(', ')}>
            <div className="hs-note" style={{ margin: '0 0 10px' }}>Per-host overrides. Leave a field blank to use the current default (shown in the field). Set a number to override it for this host only; fleet-wide defaults live in the Thresholds screen.</div>
            <div className="hs-grid">
              {cfg.thresholds.map((t) => (
                <label className="field" key={t.macro}>
                  <span>{t.label}{t.unit ? ` (${t.unit})` : ''}</span>
                  <input className="input" type="text" inputMode="decimal" placeholder={t.default ? `default ${t.default}${t.unit || ''}` : 'default'} value={t.value || ''} disabled={!canEdit} onChange={(e) => setThreshold(t.macro, e.target.value)} />
                </label>
              ))}
            </div>
          </HsSection>
        )
      })()}

      {cfg.categories && cfg.categories.length > 1 && (
        <HsSection title="Sensor order" {...sec('order')} summary={customOrder ? 'custom: ' + cfg.categories.join(', ') : 'the default order'}>
          <div className="hs-note" style={{ margin: '0 0 10px' }}>The order sensor categories read on this host. Off follows the class or built-in default order.</div>
          <label className="hs-note" style={{ display: 'flex', alignItems: 'center', gap: 8, margin: '0 0 10px', cursor: canEdit ? 'pointer' : 'default' }}>
            <input type="checkbox" checked={customOrder} disabled={!canEdit} onChange={(e) => setCustomOrder(e.target.checked)} />
            <span>Custom order for this host</span>
          </label>
          {customOrder && (
            <ol className="cat-order">
              {cfg.categories.map((c, i) => (
                <li key={c}>
                  <span className="cat-name">{c}</span>
                  <span className="cat-move">
                    <button className="btn ghost" disabled={!canEdit || i === 0} onClick={() => moveCategory(i, -1)} aria-label="Move up">↑</button>
                    <button className="btn ghost" disabled={!canEdit || i === cfg.categories!.length - 1} onClick={() => moveCategory(i, 1)} aria-label="Move down">↓</button>
                  </span>
                </li>
              ))}
            </ol>
          )}
        </HsSection>
      )}

      <HsSection title="Upstream device" {...sec('upstream')} summary={upMode === 'none' ? 'none' : upMode === 'manual' ? `chosen by hand: ${allHosts.find((x) => x.id === upHost)?.name || 'pick a host'}` : cfg.upstream?.auto ? `${cfg.upstream.auto.name}${cfg.upstream.auto.port ? ` port ${cfg.upstream.auto.port}` : ''} (from the UniFi controller)` : "from the UniFi controller: it doesn't list this host"}>
        <div className="hs-mon">
          <span className="hs-monlabel">Upstream</span>
          <div className="seg">
            <button type="button" className={upMode === 'auto' ? 'on' : ''} disabled={!canEdit} onClick={() => setUpMode('auto')}>From the controller</button>
            <button type="button" className={upMode === 'manual' ? 'on' : ''} disabled={!canEdit} onClick={() => setUpMode('manual')}>Choose a host</button>
            <button type="button" className={upMode === 'none' ? 'on' : ''} disabled={!canEdit} onClick={() => setUpMode('none')}>None</button>
          </div>
          {upMode === 'manual' && (
            <Select value={upHost} disabled={!canEdit} onChange={(e) => setUpHost(e.target.value)} aria-label="Upstream host">
              <option value="">Pick a host…</option>
              {allHosts.filter((x) => x.id !== hostId).sort((a, b) => a.name.localeCompare(b.name)).map((x) => <option key={x.id} value={x.id}>{x.name}{x.groups?.[0] ? ` · ${x.groups[0]}` : ''}</option>)}
            </Select>
          )}
        </div>
        {upMode === 'auto' && cfg.upstream && (cfg.upstream.path.length > 0 && cfg.upstream.source !== 'manual'
          ? <div className="info-line" style={{ padding: '2px 0 6px' }}><PathView hops={cfg.upstream.path} /></div>
          : <div className="hs-note">{cfg.upstream.why ? `No answer from the UniFi controller. ${cfg.upstream.why}` : "The UniFi controller doesn't list this host (it knows its own devices, and the switch port of each wired client it sees). Choose a host if you know where it is plugged in."}</div>)}
        <div className="hs-note">The default is the UniFi controller's answer when it knows this host, otherwise none. While the upstream device is down, this host's alerts wait: Argus can't reach it through that device anyway, and the device's own alert says so. Whatever is still wrong after it's back is alerted.</div>
      </HsSection>

      <HsSection title="Device facts" {...sec('facts')} summary={[own.asset_tag && `asset ${own.asset_tag}`, own.location].filter(Boolean).join(' · ') || 'no asset tag or location'}>
        <div className="hs-grid">
          <label className="field"><span>Asset tag</span><input className="input" maxLength={64} disabled={!canEdit} value={own.asset_tag} onChange={(e) => setOwn({ ...own, asset_tag: e.target.value })} /></label>
          <label className="field"><span>Location</span><input className="input" maxLength={120} disabled={!canEdit} placeholder="e.g. Floor 2, comms cupboard" value={own.location} onChange={(e) => setOwn({ ...own, location: e.target.value })} /></label>
        </div>
        <div className="hs-note">The model, serial, firmware and MAC are read from the device; these two are yours. They show on the Device tab and in the Inventory.</div>
      </HsSection>

      <HsSection title="Links" {...sec('links')} summary={[...(cfg.class_links || []).map((l) => l.label), ...links.filter((l) => l.label.trim()).map((l) => l.label)].join(', ') || 'none'}>
        {(cfg.class_links || []).length > 0 && (
          <div className="link-line"><span className="flabel">From the class</span><LinkButtons links={cfg.class_links || []} /></div>
        )}
        {links.map((l, i) => (
          <div className="link-edit" key={i}>
            <input className="input" placeholder="Label" maxLength={40} disabled={!canEdit} value={l.label} onChange={(e) => setLinks((ls) => ls.map((x, j) => (j === i ? { ...x, label: e.target.value } : x)))} aria-label="Link label" />
            <input className="input mono" placeholder="https://… or ssh://…, may use {ip}, {name}, {mac}" disabled={!canEdit} value={l.url} onChange={(e) => setLinks((ls) => ls.map((x, j) => (j === i ? { ...x, url: e.target.value } : x)))} aria-label="Link address" />
            {canEdit && <Button variant="ghost" onClick={() => setLinks((ls) => ls.filter((_, j) => j !== i))}>Remove</Button>}
          </div>
        ))}
        {canEdit && <div><Button variant="ghost" className="compact" onClick={() => setLinks((ls) => [...ls, { label: '', url: '' }])}>+ Add link</Button></div>}
        <div className="hs-note">A link is a button on this host's Device tab. Links for every host of a class are set in Settings, Device links.</div>
      </HsSection>

      {err && <div style={{ color: 'var(--err)', fontSize: 13, marginTop: 8 }}>{err}</div>}
      <div className="hs-foot">
        {canEdit && <ReasonInput value={reason} onChange={setReason} />}
        <Button variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button>
        {canEdit && <Button variant="primary" onClick={save} disabled={busy || !cfg.host.trim()}>Save</Button>}
      </div>
    </div>
  )
}

// ProxySNMP is the per-proxy SNMP-defaults band in the Probes tab. Saving stores the default and
// propagates it to every host on the proxy whose SNMP interface is set to inherit.
// embedded: a section of its own page (Settings, for the core) rather than a band under a probe row:
// no title or Close of its own.
function ProxySNMP({ proxyId, proxyName, onClose, embedded }: { proxyId: string; proxyName: string; onClose?: () => void; embedded?: boolean }) {
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
    const base = `SNMP default saved${typeof d.updated === 'number' ? ` - updated ${d.updated} inheriting host${d.updated === 1 ? '' : 's'}` : ''}${d.warning ? ` (${d.warning})` : ''}`
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
  const root = 'host-settings ' + (embedded ? 'snmp-embedded' : 'snmp-band')
  if (err && !snmp) return <div className={root}><div style={{ color: 'var(--err)', fontSize: 13 }}>{err}</div></div>
  if (!snmp) return <div className={root}><span style={{ color: 'var(--muted)', fontSize: 13 }}>Loading…</span></div>
  return (
    <div className={root}>
      {!embedded && <div className="hs-title">SNMP default · {proxyName}</div>}
      <div className="hs-note">{embedded ? 'Hosts set to “inherit” use these credentials.' : 'Hosts on this proxy set to “inherit” use these credentials.'} Saving applies them to every inheriting host.{isSet ? '' : ' No default is set yet.'}</div>
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
        {onClose && <Button variant="ghost" onClick={onClose} disabled={busy}>Close</Button>}
        <Button variant="primary" onClick={save} disabled={busy}>Save</Button>
      </div>
    </div>
  )
}

// GroupEditor is the inline band under a host row for moving it between tree groups: a checkbox per
// group (current membership pre-checked), enforcing at least one. Save replaces the host's full group
// set. An inline band rather than a modal: it edits one row in place.
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

// --- Uptime and incident history (uptime.go, incidents.go) --------------------------------------

// Argus's timezone and time format, for absolute times; HeaderClock keeps it current.
const CLOCK: { tz?: string; h24: boolean } = { h24: true }

// The signed-in user's sites (empty = every site), for the labels that name what a list covers. The
// server already filters everything to them; AppShell keeps this current.
const SCOPE: { sites: string[] } = { sites: [] }
function watchEyebrow(): string { return SCOPE.sites.length ? `Watch · ${sitesLabel(SCOPE.sites)}` : 'Watch · all sites' }

// fmtWhen renders a unix time in Argus's timezone: "29 Sep, 14:05".
function fmtWhen(unix: number): string {
  const opts: Intl.DateTimeFormatOptions = { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', hour12: !CLOCK.h24, timeZone: CLOCK.tz }
  try { return new Intl.DateTimeFormat(undefined, opts).format(new Date(unix * 1000)) }
  catch { delete opts.timeZone; return new Intl.DateTimeFormat(undefined, opts).format(new Date(unix * 1000)) }
}

type Availability = { item_id: string; label?: string; uptime_24h: number | null; uptime_7d: number | null; uptime_30d: number | null; checks: [number, number][]; days: { day: string; pct: number | null }[] }

// fmtUptime never rounds up to 100%: one missed check in a month still reads 99.99%.
function fmtUptime(v: number | null | undefined): string {
  if (v == null) return '-'
  if (v >= 100) return '100%'
  return String(parseFloat((Math.floor(v * 100) / 100).toFixed(2))) + '%'
}
function uptimeColor(v: number | null | undefined): string {
  if (v == null) return 'var(--faint)'
  if (v >= 99.9) return 'var(--ok)'
  if (v >= 99) return 'var(--warn)'
  return 'var(--err)'
}

function useAvailability(url: string): Availability | null {
  const [a, setA] = useState<Availability | null>(null)
  useEffect(() => {
    let live = true
    const load = () => fetch(url).then((r) => (r.ok ? r.json() : null)).then((v) => { if (live) setA(v) }).catch(() => {})
    load()
    const t = window.setInterval(load, 60000)
    return () => { live = false; clearInterval(t) }
  }, [url])
  return a
}

// UptimeChecks is the strip of the last checks, one tick each, newest on the right.
function UptimeChecks({ checks }: { checks: [number, number][] }) {
  if (!checks.length) return null
  return (
    <span className="upchecks" role="img" aria-label={`Last ${checks.length} checks: ${checks.filter((c) => !c[1]).length} down`}>
      {checks.map(([t, up]) => <span key={t} className={up ? 'up' : 'down'} title={`${fmtWhen(t)} · ${up ? 'up' : 'down'}`} />)}
    </span>
  )
}

function UptimeFigures({ a }: { a: Availability }) {
  const figs: [string, number | null][] = [['24h', a.uptime_24h], ['7 days', a.uptime_7d], ['30 days', a.uptime_30d]]
  return <>{figs.map(([l, v]) => (
    <span key={l} className="upfig"><span className="upfig-l">{l}</span><span className="upfig-v" style={{ color: uptimeColor(v) }}>{fmtUptime(v)}</span></span>
  ))}</>
}

// HostUptime is the band at the top of a host card: the host's uptime and its last checks, measured
// on its master sensor when that is an up/down one, else its ping.
function HostUptime({ hostId }: { hostId: string }) {
  const a = useAvailability(`/api/hosts/${hostId}/availability`)
  if (!a || !a.item_id) return null
  return (
    <div className="upband">
      <span className="upband-l" title={a.label ? `Measured on ${a.label}` : undefined}>Uptime</span>
      <UptimeFigures a={a} />
      <UptimeChecks checks={a.checks} />
    </div>
  )
}

// AvailabilityPanel is an up/down sensor's uptime in its chart reveal: the figures, the last checks,
// and one bar per day for the last 30 days.
function AvailabilityPanel({ itemId }: { itemId: string }) {
  const a = useAvailability(`/api/items/${itemId}/availability`)
  if (!a) return <div className="upanel"><span className="upanel-l">Loading uptime…</span></div>
  return (
    <div className="upanel">
      <div className="upanel-figs"><span className="upband-l">Uptime</span><UptimeFigures a={a} /></div>
      <div className="upanel-row"><span className="upanel-l">Last {a.checks.length} checks</span><UptimeChecks checks={a.checks} /></div>
      <div className="upanel-row"><span className="upanel-l">Last 30 days</span>
        <span className="updays" role="img" aria-label="Uptime per day, last 30 days">
          {a.days.map((d) => <span key={d.day} style={{ background: uptimeColor(d.pct) }} title={`${d.day} · ${d.pct == null ? 'no data' : fmtUptime(d.pct)}`} />)}
        </span>
      </div>
    </div>
  )
}

type Incident = { event_id: string; host_id: string; host_name: string; site?: string; item_id?: string; sensor?: string; name: string; severity: number; start: number; end?: number; ack_by?: string; ack_note?: string; reason?: string; argus?: boolean; note?: string; note_by?: string; hidden?: boolean }

// IncidentRows lists incidents newest first; the host column only in the fleet-wide list.
function IncidentRows({ rows, goHost }: { rows: Incident[]; goHost: ((h: string) => void) | null }) {
  const now = Math.floor(Date.now() / 1000)
  return (
    <div className="enroll-scroll">
      <table className={'slist slist-inc' + (goHost ? '' : ' nohost')}>
        <thead><tr><th className="slgrow">What happened</th>{goHost && <th>Host</th>}<th>Started</th><th>Duration</th><th>Acknowledged</th></tr></thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.event_id + ':' + r.start} className={r.hidden ? 'inc-hidden' : undefined}>
              <td className="slgrow" style={{ borderLeft: `3px solid ${sevInfo(r.severity).color}`, paddingLeft: 13 }}>
                <div className="inc-name">{r.name}{r.hidden && <span className="tag-hidden" title="Its sensor (or host) is hidden">hidden</span>}</div>
                <div className="sreason"><SevText sev={r.severity} />{r.sensor && r.sensor !== r.name ? <> · {r.sensor}</> : null}</div>
                {r.reason && <div className="sreason inc-why">{r.reason}</div>}
                {r.note && <div className="sreason inc-note" title="The note on the sensor during this incident">Note: {r.note}{r.note_by ? ` (${r.note_by})` : ''}</div>}
              </td>
              {goHost && <td data-label="Host"><span><span className="lnk-host" onClick={() => goHost(r.host_id)}>{r.host_name}</span>{r.site ? <span className="inc-site"> · {r.site}</span> : null}</span></td>}
              <td className="mono" data-label="Started">{fmtWhen(r.start)}</td>
              <td data-label="Duration">{r.end ? <span className="mono">{fmtDuration(r.end - r.start)}</span> : <span className="tag inc-open">ongoing · {fmtDuration(now - r.start)}</span>}</td>
              <td data-label="Acknowledged" title={r.ack_note || undefined}>{r.ack_by || <span className="muted">-</span>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// useIncidents loads an incident list and how many incidents of hidden sensors it left out (or, with
// hidden=1 in the URL, flagged).
function useIncidents(url: string): [Incident[] | null, string, number] {
  const [rows, setRows] = useState<Incident[] | null>(null)
  const [err, setErr] = useState('')
  const [hidden, setHidden] = useState(0)
  useEffect(() => {
    let live = true
    setRows(null); setErr('')
    const load = () => fetch(url).then(async (r) => { if (!r.ok) throw new Error('incidents'); return r.json() })
      .then((d) => { if (live) { setRows(d.incidents || []); setHidden(d.hidden || 0); setErr('') } })
      .catch(() => { if (live) setErr('Could not load the incident history') })
    load()
    const t = window.setInterval(load, 60000)
    return () => { live = false; clearInterval(t) }
  }, [url])
  return [rows, err, hidden]
}

// HiddenToggle offers the incidents of hidden sensors that a list left out, and hides them again.
function HiddenToggle({ n, shown, onToggle, lead = true }: { n: number; shown: boolean; onToggle: () => void; lead?: boolean }) {
  if (n === 0) return null
  return <>{lead ? ' · ' : null}<button type="button" className="linkbtn hidden-toggle" onClick={(e) => { e.stopPropagation(); onToggle() }}>{shown ? 'leave out' : 'show'} {n} from hidden sensors</button></>
}

// --- The host's tabs: Device, Journal, Inventory ---

type HostTab = 'sensors' | 'device' | 'history' | 'journal' | 'changes'
type LinkRow = { id?: number; label: string; url: string; from?: string }
type DeviceFacts = { model?: string; serial?: string; firmware?: string; os?: string; ip?: string; mac?: string; read_at?: number; from?: string; upgrade?: string }
type Hop = { host_id: string; name: string; port?: string; down?: boolean }
type UpstreamInfo = { mode: 'auto' | 'manual' | 'none'; manual_host?: string; auto?: Hop; path: Hop[]; behind: { id: string; name: string }[]; behind_all: number; source?: string; why?: string }
type DeviceInfo = { facts: DeviceFacts; own: { asset_tag: string; location: string }; class?: string; links: LinkRow[]; tags: HostTag[]; used_by: { groups: string[]; probe: string; status_pages: string[]; maintenance: string[]; channels: string[] }; upstream: UpstreamInfo; site?: SiteInfo }
type SiteContact = { role: string; name: string; phone: string; email: string }
type SiteLine = { name: string; host_id: string; key: string; provider: string; circuit: string; phone: string; note: string; host_name?: string; sensor?: string; state?: string }
type LineChoice = { host_id: string; key: string; label: string; group: string }
type SiteInfo = { site: string; address: string; note: string; contacts: SiteContact[]; lines: SiteLine[]; updated_at?: number; choices?: LineChoice[] }
type JournalRow = { id: number; kind: 'info' | 'warning' | 'problem'; text: string; by: string; at: number; mine?: boolean; can_delete?: boolean }

// CopyValue is a value with a small copy button after it.
function CopyValue({ value, mono = true }: { value: string; mono?: boolean }) {
  const [done, setDone] = useState(false)
  return (
    <span className="copyval">
      <span className={mono ? 'mono' : undefined}>{value}</span>
      <button type="button" className="copy-ic" title={done ? 'Copied' : 'Copy'} aria-label={`Copy ${value}`} onClick={async (e) => { e.stopPropagation(); if (await copyToClipboard(value)) { setDone(true); setTimeout(() => setDone(false), 1500) } }}>
        {done
          ? <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2"><path d="M5 12.5l4.5 4.5L19 7" /></svg>
          : <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><rect x="9" y="9" width="11" height="11" rx="2" /><path d="M5 15V6a2 2 0 0 1 2-2h9" /></svg>}
      </button>
    </span>
  )
}

// LinkButtons opens a host's links: web pages in a new tab, remote sessions (ssh:// rdp://) in their app.
function LinkButtons({ links }: { links: LinkRow[] }) {
  if (!links.length) return null
  return (
    <span className="linkbtns">
      {links.map((l, i) => (
        <a key={i} className="btn compact" href={l.url} target={/^https?:/i.test(l.url) ? '_blank' : undefined} rel="noopener noreferrer" title={l.url}>
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5" /></svg>{l.label}
        </a>
      ))}
    </span>
  )
}

// useHostCounts reads what the host's tab labels count: incidents in 30 days, journal entries, changes.
function useHostCounts(hostId: string, tick: number): { history?: number; journal?: number; changes?: number; changesMore?: boolean } {
  const [c, setC] = useState<{ history?: number; journal?: number; changes?: number; changesMore?: boolean }>({})
  useEffect(() => {
    let live = true
    fetch(`/api/hosts/${hostId}/incidents?days=30`).then((r) => (r.ok ? r.json() : null)).then((d) => { if (live && d) setC((x) => ({ ...x, history: (d.incidents || []).length })) }).catch(() => {})
    fetch(`/api/hosts/${hostId}/journal`).then((r) => (r.ok ? r.json() : null)).then((d) => { if (live && d) setC((x) => ({ ...x, journal: d.length })) }).catch(() => {})
    fetch(`/api/hosts/${hostId}/changes`).then((r) => (r.ok ? r.json() : null)).then((d) => { if (live && d) setC((x) => ({ ...x, changes: (d.changes || []).length, changesMore: !!d.more })) }).catch(() => {})
    return () => { live = false }
  }, [hostId, tick])
  return c
}

// HostTabs is the row of a host's tabs, each with what it counts.
function HostTabs({ hostId, tab, setTab, sensors, tick }: { hostId: string; tab: HostTab; setTab: (t: HostTab) => void; sensors: number; tick: number }) {
  const c = useHostCounts(hostId, tick)
  const T: [HostTab, string, number | string | undefined][] = [
    ['sensors', 'Sensors', sensors], ['device', 'Device', undefined], ['history', 'History', c.history],
    ['journal', 'Journal', c.journal], ['changes', 'Changes', c.changes === undefined ? undefined : `${c.changes}${c.changesMore ? '+' : ''}`],
  ]
  return (
    <div className="rtabs host-tabs" role="tablist" aria-label="Host">
      {T.map(([id, label, n]) => (
        <button key={id} type="button" role="tab" aria-selected={tab === id} className={'rtab' + (tab === id ? ' on' : '')} onClick={(e) => { e.stopPropagation(); setTab(id) }}>
          {label}{n !== undefined && n !== 0 && n !== '0' ? <span className="n">{n}</span> : null}
        </button>
      ))}
    </div>
  )
}

function HostTabBody({ hostId, tab, canEdit, onOpenSettings, onChanged }: { hostId: string; tab: HostTab; canEdit: boolean; onOpenSettings?: () => void; onChanged: () => void }) {
  if (tab === 'device') return <DeviceTab hostId={hostId} onOpenSettings={onOpenSettings} />
  if (tab === 'history') return <HostIncidents hostId={hostId} goHost={null} asTab />
  if (tab === 'journal') return <JournalTab hostId={hostId} canEdit={canEdit} onChanged={onChanged} />
  if (tab === 'changes') return <HostChanges hostId={hostId} asTab />
  return null
}

// PathView draws a host's upstream path, top first: gw-site1 › sw-core port 24 › this host. A hop that
// is down is red.
function PathView({ hops }: { hops: Hop[] }) {
  return (
    <span className="path">
      {hops.map((h, i) => (
        <Fragment key={h.host_id}>
          {i > 0 && <span className="path-arrow" aria-hidden="true">›</span>}
          <span className={'path-hop' + (h.down ? ' down' : '') + (i === hops.length - 1 ? ' here' : '')}>
            <span className="sdot" style={{ '--dot': h.down ? 'var(--err)' : 'var(--ok)' } as CSSProperties} />
            {h.name}{h.port ? <span className="path-port"> port {h.port}</span> : null}
          </span>
        </Fragment>
      ))}
    </span>
  )
}

const PHONE_IC = <svg className="call-ic" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true"><path d="M5 4h4l2 5-2.5 1.5a11 11 0 0 0 5 5L15 13l5 2v4a2 2 0 0 1-2 2A16 16 0 0 1 3 6a2 2 0 0 1 2-2z" /></svg>

// LineState is an internet line's state, read from its sensor: a quiet tick when up, red when down.
function LineState({ l }: { l: SiteLine }) {
  if (l.state === 'up') return <span className="okquiet" title={l.sensor ? `${l.sensor} on ${l.host_name}` : undefined}>up</span>
  if (l.state === 'down') return <span className="tag err" title={l.sensor ? `${l.sensor} on ${l.host_name}` : undefined}>down</span>
  return null
}

// SiteLines are a site's info as lines: address, contacts, internet lines. The Device tab shows them in
// one row; the site's page in a row each (part).
function SiteLines({ info, part }: { info: SiteInfo; part?: 'address' | 'contacts' | 'lines' }) {
  return (
    <>
      {(!part || part === 'address') && (info.address || info.note) && (
        <InfoLine k={part ? undefined : 'Address'}><span className="v">{info.address}</span>{info.note && <span className="sub-line">{info.address ? '· ' : ''}{info.note}</span>}</InfoLine>
      )}
      {(!part || part === 'contacts') && info.contacts.map((c, i) => (
        <InfoLine key={'c' + i} k={c.role || 'Contact'}>
          {c.name && <span className="v">{c.name}</span>}
          {c.phone && <CopyValue value={c.phone} />}
          {c.email && <a className="sub-line" href={`mailto:${c.email}`}>{c.email}</a>}
        </InfoLine>
      ))}
      {(!part || part === 'lines') && info.lines.map((l, i) => (
        <InfoLine key={'l' + i} k={l.name || 'Internet'}>
          <span className="v">{[l.provider, l.note].filter(Boolean).join(' · ') || '-'}</span>
          {l.circuit && <><span className="sub-line">circuit</span><CopyValue value={l.circuit} /></>}
          {l.phone && <><span className="sub-line">support</span><CopyValue value={l.phone} /></>}
          <LineState l={l} />
        </InfoLine>
      ))}
    </>
  )
}

// SitePanel is the top of a site's page in Monitoring: its address, who to call and its internet lines.
function SitePanel({ site, canEdit, editing, onEditDone }: { site: string; canEdit: boolean; editing: boolean; onEditDone: () => void }) {
  const [info, setInfo] = useState<SiteInfo | null>(null)
  const load = () => fetch(`/api/sites/${encodeURIComponent(site)}/info`).then((r) => (r.ok ? r.json() : null)).then((x) => setInfo(x)).catch(() => setInfo(null))
  useEffect(() => { setInfo(null); load() }, [site]) // eslint-disable-line react-hooks/exhaustive-deps
  if (!info) return editing ? <SiteInfoDialog site={site} onClose={onEditDone} onSaved={() => { onEditDone(); load() }} /> : null
  const empty = !info.address && !info.note && info.contacts.length === 0 && info.lines.length === 0
  return (
    <>
      {empty
        ? (canEdit ? <div className="site-empty">No site info yet: add the address, who to call and the internet lines with <b>Edit site info</b>. Alerts can then say who to call.</div> : null)
        : (
          <div className="info-rows site-info">
            {(info.address || info.note) && <InfoRow label="Address"><SiteLines info={info} part="address" /></InfoRow>}
            {info.contacts.length > 0 && <InfoRow label="Contacts"><SiteLines info={info} part="contacts" /></InfoRow>}
            {info.lines.length > 0 && <InfoRow label="Internet"><SiteLines info={info} part="lines" /></InfoRow>}
          </div>
        )}
      {editing && <SiteInfoDialog site={site} onClose={onEditDone} onSaved={() => { onEditDone(); load() }} />}
    </>
  )
}

const NO_CONTACT: SiteContact = { role: '', name: '', phone: '', email: '' }
const NO_LINE: SiteLine = { name: '', host_id: '', key: '', provider: '', circuit: '', phone: '', note: '' }

// SiteInfoDialog edits a site's info: the address, the contacts and the internet lines, each line tied
// to the sensor that measures it.
function SiteInfoDialog({ site, onClose, onSaved }: { site: string; onClose: () => void; onSaved: () => void }) {
  const toast = useToast()
  const [address, setAddress] = useState('')
  const [note, setNote] = useState('')
  const [contacts, setContacts] = useState<SiteContact[]>([])
  const [lines, setLines] = useState<SiteLine[]>([])
  const [choices, setChoices] = useState<LineChoice[]>([])
  const [loaded, setLoaded] = useState(false)
  const [busy, setBusy] = useState(false)
  const [reason, setReason] = useState('')
  useEffect(() => {
    fetch(`/api/sites/${encodeURIComponent(site)}/info?choices=1`).then((r) => (r.ok ? r.json() : null)).then((x: SiteInfo | null) => {
      if (x) {
        setAddress(x.address); setNote(x.note); setContacts(x.contacts); setChoices(x.choices || [])
        setLines(x.lines.map((l) => ({ name: l.name, host_id: l.host_id, key: l.key, provider: l.provider, circuit: l.circuit, phone: l.phone, note: l.note })))
      }
      setLoaded(true)
    }).catch(() => setLoaded(true))
  }, [site])
  const options: ComboOption[] = [{ value: '', label: 'Not tied to a sensor' }, ...choices.map((c) => ({ value: c.host_id + '|' + c.key, label: c.label, hint: c.key ? 'WAN sensor' : 'any alert on this host' }))]
  const setContact = (i: number, v: Partial<SiteContact>) => setContacts((cs) => cs.map((c, j) => (j === i ? { ...c, ...v } : c)))
  const setLine = (i: number, v: Partial<SiteLine>) => setLines((ls) => ls.map((l, j) => (j === i ? { ...l, ...v } : l)))
  function tie(i: number, v: string) {
    const [host_id, key] = v ? v.split('|') : ['', '']
    const wan = /^unifi\.wan\.[a-z]+\[(.+)\]$/.exec(key || '')
    setLines((ls) => ls.map((l, j) => (j === i ? { ...l, host_id, key: key || '', name: l.name || (wan ? `WAN ${wan[1]}` : l.name) } : l)))
  }
  async function save() {
    setBusy(true)
    const res = await fetch(`/api/sites/${encodeURIComponent(site)}/info`, { method: 'PUT', headers: { 'Content-Type': 'application/json', ...reasonHeader(reason) }, body: JSON.stringify({ address, note, contacts, lines }) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not save the site info')); return }
    toast.success('Site info saved')
    onSaved()
  }
  return (
    <BulkDialog title={`Site info · ${site}`} wide busy={busy || !loaded} applyLabel="Save" reason={reason} setReason={setReason} onClose={onClose} onApply={save}>
      <div className="hs-grid">
        <label className="chan-field"><span className="flabel">Address</span><input className="input" maxLength={200} value={address} onChange={(e) => setAddress(e.target.value)} placeholder="1 Example Street, Example City" /></label>
        <label className="chan-field"><span className="flabel">Note</span><input className="input" maxLength={200} value={note} onChange={(e) => setNote(e.target.value)} placeholder="Reception opens 08:00-18:00" /></label>
      </div>
      <div className="site-ed-h">Contacts</div>
      <div className="hs-note">Who to call about this site. The first one goes in the alerts of a channel with Who to call on.</div>
      {contacts.map((c, i) => (
        <div className="site-contact-ed" key={i}>
          <input className="input" maxLength={200} placeholder="Role, e.g. On-site IT" value={c.role} onChange={(e) => setContact(i, { role: e.target.value })} aria-label="Role" />
          <input className="input" maxLength={200} placeholder="Name" value={c.name} onChange={(e) => setContact(i, { name: e.target.value })} aria-label="Name" />
          <input className="input" maxLength={200} placeholder="Phone" value={c.phone} onChange={(e) => setContact(i, { phone: e.target.value })} aria-label="Phone" />
          <input className="input" maxLength={200} placeholder="Email" value={c.email} onChange={(e) => setContact(i, { email: e.target.value })} aria-label="Email" />
          <Button variant="ghost" onClick={() => setContacts((cs) => cs.filter((_, j) => j !== i))}>Remove</Button>
        </div>
      ))}
      {contacts.length < 20 && <div><Button variant="ghost" className="compact" onClick={() => setContacts((cs) => [...cs, { ...NO_CONTACT }])}>+ Add contact</Button></div>}
      <div className="site-ed-h">Internet lines</div>
      <div className="hs-note">The provider, circuit and support number of each line. Tie it to the sensor that measures it (a UniFi gateway's WAN, or a host such as the provider's modem), so an alert on it says who to call; when the site's probe stops reporting, the alert lists every line.</div>
      {lines.map((l, i) => (
        <div className="site-line-ed" key={i}>
          <div className="chan-field site-line-tie"><span className="flabel">Measured by</span><Combobox value={l.host_id ? l.host_id + '|' + l.key : ''} onChange={(v) => tie(i, v)} options={options} placeholder="Search sensors and hosts…" /></div>
          <label className="chan-field"><span className="flabel">Name</span><input className="input" maxLength={200} placeholder="WAN 1" value={l.name} onChange={(e) => setLine(i, { name: e.target.value })} /></label>
          <label className="chan-field"><span className="flabel">Provider</span><input className="input" maxLength={200} placeholder="Example Fiber" value={l.provider} onChange={(e) => setLine(i, { provider: e.target.value })} /></label>
          <label className="chan-field"><span className="flabel">Circuit / contract</span><input className="input" maxLength={200} placeholder="EXF-000123" value={l.circuit} onChange={(e) => setLine(i, { circuit: e.target.value })} /></label>
          <label className="chan-field"><span className="flabel">Support phone</span><input className="input" maxLength={200} placeholder="+1 555 0100" value={l.phone} onChange={(e) => setLine(i, { phone: e.target.value })} /></label>
          <label className="chan-field"><span className="flabel">Note</span><input className="input" maxLength={200} placeholder="1 Gbps, LTE backup" value={l.note} onChange={(e) => setLine(i, { note: e.target.value })} /></label>
          <div className="site-line-act"><Button variant="ghost" className="compact" onClick={() => setLines((ls) => ls.filter((_, j) => j !== i))}>Remove</Button></div>
        </div>
      ))}
      {lines.length < 10 && <div><Button variant="ghost" className="compact" onClick={() => setLines((ls) => [...ls, { ...NO_LINE }])}>+ Add line</Button></div>}
    </BulkDialog>
  )
}

// InfoRows is the label-and-lines layout the Device tab and the site info share (the Updates rows).
function InfoRow({ label, children }: { label: string; children: ReactNode }) {
  return <div className="info-row"><span className="complabel">{label}</span><div className="info-lines">{children}</div></div>
}
function InfoLine({ k, children }: { k?: string; children: ReactNode }) {
  return <div className="info-line">{k && <span className="k">{k}</span>}{children}</div>
}

// DeviceTab shows what Argus knows about the device: facts read from it (copyable), the ones only a
// person knows, its links, tags, and what uses it.
function DeviceTab({ hostId, onOpenSettings }: { hostId: string; onOpenSettings?: () => void }) {
  const [d, setD] = useState<DeviceInfo | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    let live = true
    fetch(`/api/hosts/${hostId}/device`).then(async (r) => { if (!r.ok) throw new Error(await errText(r, 'Could not load the device')); return r.json() })
      .then((x) => { if (live) setD(x) }).catch((e) => { if (live) setErr(e instanceof Error ? e.message : 'Could not load the device') })
    return () => { live = false }
  }, [hostId])
  if (err) return <div className="tab-empty txt-err">{err}</div>
  if (!d) return <Skeleton rows={4} cols={2} />
  const f = d.facts
  const any = f.model || f.serial || f.firmware || f.os || f.ip || f.mac
  const u = d.used_by
  return (
    <div className="info-rows">
      <InfoRow label="Device">
        {d.class && <InfoLine k="Class"><span className="v">{d.class}</span></InfoLine>}
        {f.model && <InfoLine k="Model"><span className="v">{f.model}</span></InfoLine>}
        {f.firmware && <InfoLine k="Firmware"><span className="v mono">{f.firmware}</span>{f.upgrade && <span className="tag avail" title="The UniFi controller offers this firmware for the device">update available · {f.upgrade}</span>}</InfoLine>}
        {f.os && <InfoLine k="OS"><span className="v">{f.os}</span></InfoLine>}
        {f.serial && <InfoLine k="Serial"><CopyValue value={f.serial} /></InfoLine>}
        {f.ip && <InfoLine k="IP"><CopyValue value={f.ip} /></InfoLine>}
        {f.mac && <InfoLine k="MAC"><CopyValue value={f.mac} /></InfoLine>}
        {!any && <InfoLine><span className="muted">Nothing read from this device yet: its class doesn't report a model or firmware.</span></InfoLine>}
        {f.from && <InfoLine><span className="sub-line">Read from {f.from}{f.read_at ? ` ${relTime(f.read_at)}` : ''}</span></InfoLine>}
      </InfoRow>
      <InfoRow label="Your fields">
        <InfoLine k="Asset tag">{d.own.asset_tag ? <CopyValue value={d.own.asset_tag} /> : <span className="muted">-</span>}</InfoLine>
        <InfoLine k="Location"><span className="v">{d.own.location || <span className="muted">-</span>}</span></InfoLine>
        {onOpenSettings && <InfoLine><button type="button" className="linkbtn" onClick={onOpenSettings}>Edit in settings</button></InfoLine>}
      </InfoRow>
      <InfoRow label="Path">
        {d.upstream.path.length > 0
          ? <InfoLine><PathView hops={d.upstream.path} /><span className="sub-line">{d.upstream.source === 'manual' ? 'set by hand' : 'from the UniFi controller'}</span></InfoLine>
          : <InfoLine><span className="muted">{d.upstream.mode === 'none' ? 'No upstream device: set to none in its settings.' : `No upstream device known. ${d.upstream.why || "The UniFi controller doesn't list this host. Pick one in its settings."}`}</span></InfoLine>}
        {d.upstream.behind.length > 0 && <InfoLine k="Behind it"><span className="v">{d.upstream.behind.map((b) => b.name).join(', ')}{d.upstream.behind_all > d.upstream.behind.length ? ` (${d.upstream.behind_all} hosts in all, further down)` : ''}</span></InfoLine>}
      </InfoRow>
      {d.site && <InfoRow label={`Site · ${d.site.site}`}><SiteLines info={d.site} /></InfoRow>}
      <InfoRow label="Links">
        {d.links.length ? <InfoLine><LinkButtons links={d.links} /></InfoLine> : <InfoLine><span className="muted">No links. Add them in this host's settings, or for its class in Settings, Device links.</span></InfoLine>}
      </InfoRow>
      <InfoRow label="Tags">
        <InfoLine>{d.tags.length ? <TagList tags={d.tags} /> : <span className="muted">No tags.</span>}</InfoLine>
      </InfoRow>
      <InfoRow label="Used by">
        <InfoLine k="Groups"><span className="v">{u.groups.join(', ') || '-'}</span></InfoLine>
        <InfoLine k="Probe"><span className="v">{u.probe || '-'}</span></InfoLine>
        {u.status_pages.length > 0 && <InfoLine k="Status pages"><span className="v">{u.status_pages.join(', ')}</span></InfoLine>}
        {u.maintenance.length > 0 && <InfoLine k="Maintenance"><span className="v">{u.maintenance.join(', ')}</span></InfoLine>}
        <InfoLine k="Alerts to"><span className="v">{u.channels.length ? u.channels.join(', ') : 'no shared channel'}</span></InfoLine>
      </InfoRow>
    </div>
  )
}

// JournalTab is the host's journal: lasting notes, newest first, each with its kind, author and date.
function JournalTab({ hostId, canEdit, onChanged }: { hostId: string; canEdit: boolean; onChanged: () => void }) {
  const toast = useToast()
  const confirm = useConfirm()
  const [rows, setRows] = useState<JournalRow[] | null>(null)
  const [text, setText] = useState('')
  const [kind, setKind] = useState<JournalRow['kind']>('info')
  const [busy, setBusy] = useState(false)
  const load = () => fetch(`/api/hosts/${hostId}/journal`).then((r) => (r.ok ? r.json() : [])).then((x) => setRows(x || [])).catch(() => setRows([]))
  useEffect(() => { load() }, [hostId]) // eslint-disable-line react-hooks/exhaustive-deps
  async function add() {
    if (!text.trim()) return
    setBusy(true)
    const res = await fetch(`/api/hosts/${hostId}/journal`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ kind, text: text.trim() }) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not add the entry')); return }
    setText(''); setKind('info'); load(); onChanged()
  }
  async function del(e: JournalRow) {
    if (!(await confirm({ title: 'Remove entry', message: 'Remove this journal entry? The change log keeps a note that it was removed.', confirmLabel: 'Remove', danger: true }))) return
    const res = await fetch(`/api/journal/${e.id}`, { method: 'DELETE' }).catch(() => null)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not remove the entry')); return }
    load(); onChanged()
  }
  if (!rows) return <Skeleton rows={3} cols={2} />
  return (
    <div className="journal">
      {canEdit && (
        <div className="journal-add">
          <input className="input" maxLength={1000} placeholder="What happened, what was done, a ticket number" value={text} onChange={(e) => setText(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') add() }} aria-label="New journal entry" />
          <div className="seg" role="radiogroup" aria-label="Kind">
            {(['info', 'warning', 'problem'] as const).map((k) => <button key={k} type="button" className={kind === k ? 'on' : ''} onClick={() => setKind(k)}>{k === 'info' ? 'Info' : k === 'warning' ? 'Warning' : 'Problem'}</button>)}
          </div>
          <Button variant="primary" onClick={add} disabled={busy || !text.trim()}>Add</Button>
        </div>
      )}
      {rows.length === 0
        ? <div className="tab-empty">No entries yet. A journal keeps what happened to this host for as long as it exists: a part replaced, a move, a ticket.</div>
        : rows.map((e) => (
          <div key={e.id} className={'jentry ' + e.kind}>
            <span className="jtext">{e.text}</span>
            <span className="jby">{e.by || 'someone'} · {fmtWhen(e.at)}{e.can_delete && <> · <button type="button" className="linkbtn" onClick={() => del(e)}>remove</button></>}</span>
          </div>
        ))}
    </div>
  )
}

type InvRow = { host_id: string; name: string; groups: string[]; probe: string; class_id?: string; class?: string; asset_tag?: string; location?: string; newest?: string; newest_from?: string } & DeviceFacts

// InventoryView lists every device with its model, firmware or OS, serial, IP and MAC, grouped by
// class, and marks one on older firmware than the newest seen on the same model.
function InventoryView({ goHost }: { goHost: (h: string) => void }) {
  const [rows, setRows] = useState<InvRow[] | null>(null)
  const [err, setErr] = useState('')
  const [hf, setHf] = useState<HostFilterVal>(NO_HOST_FILTER)
  const [q, setQ] = useState('')
  const [older, setOlder] = useState(false)
  useEffect(() => {
    let live = true
    setRows(null); setErr('')
    fetch(`/api/inventory?x=1${hostFilterQS(hf)}`).then(async (r) => { if (!r.ok) throw new Error(await errText(r, 'Could not load the inventory')); return r.json() })
      .then((x) => { if (live) setRows(x || []) }).catch((e) => { if (live) setErr(e instanceof Error ? e.message : 'Could not load the inventory') })
    return () => { live = false }
  }, [hf])
  const needle = q.trim().toLowerCase()
  const shown = (rows || []).filter((r) => (!older || r.newest) && (!needle || [r.name, r.model, r.serial, r.ip, r.mac, r.firmware, r.os, r.asset_tag, r.location, r.class].some((v) => (v || '').toLowerCase().includes(needle))))
  const behind = (rows || []).filter((r) => r.newest).length
  const groups: [string, InvRow[]][] = []
  for (const r of shown) { const k = r.class || 'Other'; const g = groups.find((x) => x[0] === k); if (g) g[1].push(r); else groups.push([k, [r]]) }
  function exportInv() {
    downloadCSV(`argus-inventory-${csvStamp()}.csv`, ['Device', 'Groups', 'Probe', 'Class', 'Model', 'Firmware', 'OS', 'Serial', 'IP', 'MAC', 'Asset tag', 'Location', 'Newer firmware seen'],
      shown.map((r) => [r.name, r.groups.join('; '), r.probe, r.class || '', r.model || '', r.firmware || '', r.os || '', r.serial || '', r.ip || '', r.mac || '', r.asset_tag || '', r.location || '', r.newest || '']))
  }
  return (
    <div className="panel">
      <div className="phead">
        <PanelTitle eyebrow={watchEyebrow()}>Inventory</PanelTitle>
        <span className="hint">{rows ? `${shown.length === rows.length ? `${rows.length} devices` : `${shown.length} of ${rows.length} devices`}${behind ? ` · ${behind} on older firmware` : ''}` : ''}</span>
        <div className="tools hist-tools">
          <input className="input hist-q" placeholder="Host, model, serial, IP or MAC" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search the inventory" />
          <ProbeGroupFilter value={hf} onChange={setHf} />
          <div className="seg">
            <button className={older ? '' : 'on'} onClick={() => setOlder(false)}>All</button>
            <button className={older ? 'on' : ''} onClick={() => setOlder(true)}>Older firmware</button>
          </div>
          <button className="btn" disabled={!shown.length} onClick={exportInv}>Export CSV</button>
        </div>
      </div>
      {err && <div style={{ padding: '0.9rem 16px', color: 'var(--err)' }}>{err}</div>}
      {rows === null && !err && <Skeleton rows={6} cols={6} />}
      {rows !== null && !err && (shown.length === 0
        ? <EmptyState icon={ic.inventory} title={rows.length ? 'No device matches' : 'No devices yet'} text={rows.length ? 'Nothing matches the search and filters.' : 'Devices appear here once hosts are monitored.'} />
        : (
          <div className="enroll-scroll">
            <table className="slist slist-inv">
              <thead><tr><th>Device</th><th>Model</th><th>Firmware / OS</th><th>Serial</th><th>IP</th><th>MAC</th></tr></thead>
              <tbody>
                {groups.map(([g, rs]) => (
                  <Fragment key={g}>
                    <tr className="cat"><td colSpan={6}>{g}<span className="cat-sub">{rs.length}{rs.some((r) => r.newest) ? ` · ${rs.filter((r) => r.newest).length} with newer firmware available` : ''}</span></td></tr>
                    {rs.map((r) => (
                      <tr key={r.host_id}>
                        <td><span className="lnk-host" onClick={() => goHost(r.host_id)}>{r.name}</span><span className="inc-site"> · {r.groups[0] || 'no group'} · {r.probe}</span></td>
                        <td data-label="Model">{r.model || <span className="muted">-</span>}</td>
                        <td data-label="Firmware / OS"><span className="inv-fw"><span className="mono">{r.firmware || r.os || '-'}</span>{r.newest && (r.newest_from === 'controller'
                          ? <span className="tag avail" title="The UniFi controller offers this firmware for the device">update available · {r.newest}</span>
                          : <span className="tag avail" title="Newer firmware runs on other devices of this model">older · newest {r.newest}</span>)}</span></td>
                        <td data-label="Serial">{r.serial ? <CopyValue value={r.serial} /> : <span className="muted">-</span>}</td>
                        <td data-label="IP">{r.ip ? <CopyValue value={r.ip} /> : <span className="muted">-</span>}</td>
                        <td data-label="MAC">{r.mac ? <CopyValue value={r.mac} /> : <span className="muted">-</span>}</td>
                      </tr>
                    ))}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
        ))}
    </div>
  )
}

type LinkTpl = { id: number; label: string; url: string; classes: string[] }

// LinksCard is the Settings section for device links: buttons every host of some classes gets.
function LinksCard() {
  const toast = useToast()
  const confirm = useConfirm()
  const [tpls, setTpls] = useState<LinkTpl[]>([])
  const [classes, setClasses] = useState<DeviceClass[]>([])
  const [editing, setEditing] = useState<number | null>(null) // a template's id, or 0 for a new one
  const [form, setForm] = useState<{ label: string; url: string; classes: string[] }>({ label: '', url: '', classes: [] })
  const [busy, setBusy] = useState(false)
  const load = () => fetch('/api/links').then((r) => (r.ok ? r.json() : [])).then((x) => setTpls(x || [])).catch(() => {})
  useEffect(() => { load(); fetch('/api/classes').then((r) => (r.ok ? r.json() : [])).then((c) => setClasses(c || [])).catch(() => {}) }, [])
  const classLabel = (id: string) => classes.find((c) => c.id === id)?.label || id
  function start(t: LinkTpl | null) { setEditing(t ? t.id : 0); setForm(t ? { label: t.label, url: t.url, classes: t.classes } : { label: '', url: 'https://{ip}', classes: [] }) }
  async function save() {
    setBusy(true)
    const res = await fetch(editing ? `/api/links/${editing}` : '/api/links', { method: editing ? 'PATCH' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(form) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not save the link')); return }
    setEditing(null); load()
  }
  async function del(t: LinkTpl) {
    if (!(await confirm({ title: 'Delete link', message: `Delete “${t.label}”? Its button goes from every host it was on.`, confirmLabel: 'Delete', danger: true }))) return
    const res = await fetch(`/api/links/${t.id}`, { method: 'DELETE' }).catch(() => null)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not delete the link')); return }
    load()
  }
  const editor = (
    <div className="link-form">
      <input className="input" placeholder="Label, e.g. Web UI" maxLength={40} value={form.label} onChange={(e) => setForm({ ...form, label: e.target.value })} aria-label="Link label" />
      <input className="input mono" placeholder="https://{ip}" value={form.url} onChange={(e) => setForm({ ...form, url: e.target.value })} aria-label="Link address" />
      <SitePicker options={classes.filter((c) => !c.internal).map((c) => c.id)} labelOf={classLabel} value={form.classes} onChange={(v) => setForm({ ...form, classes: v })} allLabel="Every class" noun="classes" />
      <span className="tag-form-act"><Button variant="ghost" onClick={() => setEditing(null)} disabled={busy}>Cancel</Button><Button variant="primary" onClick={save} disabled={busy || !form.label.trim() || !form.url.trim()}>{busy ? 'Saving…' : 'Save'}</Button></span>
    </div>
  )
  return (
    <section className="set-card">
      <h3>Device links</h3>
      <p className="set-note">Buttons on the Device tab of every host of some classes. <span className="mono">{'{ip}'}</span>, <span className="mono">{'{name}'}</span>, <span className="mono">{'{host}'}</span>, <span className="mono">{'{mac}'}</span>, <span className="mono">{'{group}'}</span> and <span className="mono">{'{macro:NAME}'}</span> (a host macro, like <span className="mono">{'{macro:UNIFI.URL}'}</span>) are filled in from the host; a host missing one doesn't get the button. A host can add its own in its settings.</p>
      <div className="tag-rows">
        {tpls.map((t) => editing === t.id ? <div key={t.id} className="tag-row editing">{editor}</div> : (
          <div key={t.id} className="tag-row">
            <span className="complabel">{t.label}</span>
            <span className="tag-row-desc"><span className="mono">{t.url}</span><span className="sub-line"> · {t.classes.length ? t.classes.map(classLabel).join(', ') : 'every class'}</span></span>
            <span className="tag-row-act"><Button variant="ghost" className="compact" onClick={() => start(t)}>Edit</Button><Kebab actions={[{ label: 'Delete', icon: kbIcon.trash, danger: true, onClick: () => del(t) }]} /></span>
          </div>
        ))}
        {tpls.length === 0 && editing !== 0 && <p className="set-hint" style={{ margin: '4px 0 8px' }}>No device links.</p>}
        {editing === 0 && <div className="tag-row editing">{editor}</div>}
      </div>
      {editing === null && <div className="set-row set-actions"><Button variant="primary" onClick={() => start(null)}>+ New link</Button></div>}
    </section>
  )
}

// --- Tags ---

type HostTag = { name: string; color: string; from?: string }
type TagInfo = { name: string; color: string; description?: string; hosts: number; probes: number }
const TAG_COLORS = ['#e5484d', '#f5a524', '#30a46c', '#3b82f6', '#8e4ec6', '#12a594', '#d6409f', '#8b8d98']

// TagChip is one tag in its colour; one that comes from the host's probe is dashed and says so.
function TagChip({ tag, onRemove }: { tag: HostTag; onRemove?: () => void }) {
  return (
    <span className={'tagchip' + (tag.from ? ' inh' : '')} style={{ '--tc': tag.color || '#8b8d98' } as CSSProperties} title={tag.from ? `From its probe, ${tag.from}: change it on the Probes page` : undefined}>
      {tag.name}
      {tag.from && <span className="tag-from">from {tag.from}</span>}
      {onRemove && <button type="button" className="tag-x" aria-label={`Remove ${tag.name}`} onClick={(e) => { e.stopPropagation(); onRemove() }}>×</button>}
    </span>
  )
}

function TagList({ tags }: { tags?: HostTag[] }) {
  if (!tags || !tags.length) return null
  return <span className="taglist">{tags.map((t) => <TagChip key={t.name + '|' + (t.from || '')} tag={t} />)}</span>
}

// useTags loads the tags (name, colour, how many hosts and probes carry them).
function useTags(): [TagInfo[], () => void] {
  const [tags, setTags] = useState<TagInfo[]>([])
  const load = () => { fetch('/api/tags').then((r) => (r.ok ? r.json() : [])).then((t) => setTags(t || [])).catch(() => {}) }
  useEffect(load, [])
  return [tags, load]
}

// TagPicker is a multi-select of tags by name.
function TagPicker({ tags, value, onChange, allLabel = 'Any tag', noAll, placeholder }: { tags: TagInfo[]; value: string[]; onChange: (v: string[]) => void; allLabel?: string; noAll?: boolean; placeholder?: string }) {
  return <SitePicker options={tags.map((t) => t.name)} labelOf={(n) => n} value={value} onChange={onChange} allLabel={allLabel} noAll={noAll} placeholder={placeholder} noun="tags" />
}

// TagsEditor is a host's or probe's own tags: chips that come off with ×, and a picker to add more.
function TagsEditor({ all, value, onChange, inherited, disabled }: { all: TagInfo[]; value: string[]; onChange: (v: string[]) => void; inherited?: HostTag[]; disabled?: boolean }) {
  const color = (n: string) => all.find((t) => t.name === n)?.color || '#8b8d98'
  const left = all.filter((t) => !value.includes(t.name))
  return (
    <span className="tags-edit">
      {value.map((n) => <TagChip key={n} tag={{ name: n, color: color(n) }} onRemove={disabled ? undefined : () => onChange(value.filter((x) => x !== n))} />)}
      {(inherited || []).filter((t) => t.from && !value.includes(t.name)).map((t) => <TagChip key={'i:' + t.name} tag={t} />)}
      {!disabled && left.length > 0 && <SitePicker options={left.map((t) => t.name)} labelOf={(n) => n} value={[]} onChange={(v) => onChange([...value, ...v.filter((x) => !value.includes(x))])} noAll placeholder="+ Add tag" noun="tags" />}
      {!disabled && all.length === 0 && <span className="set-hint">No tags yet: make them in Settings, Tags.</span>}
    </span>
  )
}

// TagsCard is the Settings section where tags are made, recoloured, renamed and deleted.
function TagsCard() {
  const toast = useToast()
  const confirm = useConfirm()
  const [tags, reload] = useTags()
  const [editing, setEditing] = useState<string | null>(null) // a tag's name, or '' for a new one
  const [form, setForm] = useState({ name: '', color: TAG_COLORS[0], description: '' })
  const [busy, setBusy] = useState(false)
  function start(t: TagInfo | null) {
    setEditing(t ? t.name : '')
    setForm(t ? { name: t.name, color: t.color, description: t.description || '' } : { name: '', color: TAG_COLORS[tags.length % TAG_COLORS.length], description: '' })
  }
  async function save() {
    setBusy(true)
    const isNew = editing === ''
    const res = await fetch(isNew ? '/api/tags' : `/api/tags/${encodeURIComponent(editing!)}`, { method: isNew ? 'POST' : 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(form) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not save the tag')); return }
    setEditing(null); reload(); fireDataRefresh()
  }
  async function del(t: TagInfo) {
    const on = [t.hosts ? `${t.hosts} host${t.hosts === 1 ? '' : 's'}` : '', t.probes ? `${t.probes} probe${t.probes === 1 ? '' : 's'}` : ''].filter(Boolean).join(' and ')
    if (!(await confirm({ title: 'Delete tag', message: `Delete “${t.name}”?${on ? ` It comes off ${on}, and channels limited to it stop using it.` : ''}`, confirmLabel: 'Delete', danger: true }))) return
    const res = await fetch(`/api/tags/${encodeURIComponent(t.name)}`, { method: 'DELETE' }).catch(() => null)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not delete the tag')); return }
    reload(); fireDataRefresh()
  }
  const editor = (
    <div className="tag-form">
      <input className="input" placeholder="Name, e.g. critical" maxLength={32} value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} aria-label="Tag name" />
      <span className="tag-swatches" role="radiogroup" aria-label="Colour">
        {TAG_COLORS.map((c) => <button key={c} type="button" role="radio" aria-checked={form.color === c} className={'swatch' + (form.color === c ? ' on' : '')} style={{ background: c }} onClick={() => setForm({ ...form, color: c })} aria-label={c} />)}
      </span>
      <input className="input tag-desc" placeholder="What it means (optional)" maxLength={120} value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} aria-label="Description" />
      <span className="tag-form-act">
        <Button variant="ghost" onClick={() => setEditing(null)} disabled={busy}>Cancel</Button>
        <Button variant="primary" onClick={save} disabled={busy || !form.name.trim()}>{busy ? 'Saving…' : 'Save'}</Button>
      </span>
    </div>
  )
  return (
    <section className="set-card">
      <h3>Tags</h3>
      <p className="set-note">Labels that cut across sites. Filter the tree by them, and limit a channel to the hosts with a tag. Put them on a host in its settings, on many from the tree's selection, or on a probe (Probes) to give them to every host it monitors.</p>
      <div className="tag-rows">
        {tags.map((t) => editing === t.name ? <div key={t.name} className="tag-row editing">{editor}</div> : (
          <div key={t.name} className="tag-row">
            <span className="tag-row-chip"><TagChip tag={{ name: t.name, color: t.color }} /></span>
            <span className="tag-row-desc">{t.description || <span className="muted">no description</span>}<span className="sub-line"> · {t.hosts} host{t.hosts === 1 ? '' : 's'}{t.probes ? ` (${t.probes} probe${t.probes === 1 ? '' : 's'})` : ''}</span></span>
            <span className="tag-row-act"><Button variant="ghost" className="compact" onClick={() => start(t)}>Edit</Button><Kebab actions={[{ label: 'Delete', icon: kbIcon.trash, danger: true, onClick: () => del(t) }]} /></span>
          </div>
        ))}
        {tags.length === 0 && editing !== '' && <p className="set-hint" style={{ margin: '4px 0 8px' }}>No tags yet.</p>}
        {editing === '' && <div className="tag-row editing">{editor}</div>}
      </div>
      {editing === null && <div className="set-row set-actions"><Button variant="primary" onClick={() => start(null)}>+ New tag</Button></div>}
    </section>
  )
}

// --- Bulk actions ---

type BulkResult = { done: number; failed: { id: string; name?: string; error: string }[] }

// BulkBar is the action bar of a list's selection, pinned to the bottom while something is picked.
function BulkBar({ count, noun, onClear, children }: { count: number; noun: [string, string]; onClear: () => void; children: ReactNode }) {
  return (
    <div className="bulkbar" role="toolbar" aria-label="Actions on the selection">
      <b>{count} {count === 1 ? noun[0] : noun[1]} selected</b>
      {children}
      <button type="button" className="linkbtn bulk-clear" onClick={onClear}>Clear</button>
    </div>
  )
}

// BulkDialog is the dialog of a bulk action that needs a choice (groups, a probe, tags, thresholds).
function BulkDialog({ title, note, busy, applyLabel, canApply = true, reason, setReason, onApply, onClose, wide, children }: { title: string; note?: ReactNode; busy: boolean; applyLabel: string; canApply?: boolean; reason: string; setReason: (v: string) => void; onApply: () => void; onClose: () => void; wide?: boolean; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])
  return createPortal(
    <div className="dlg-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) onClose() }}>
      <div className="dlg" role="dialog" aria-modal="true" style={{ maxWidth: wide ? 'min(860px, 94vw)' : 'min(620px, 94vw)', width: wide ? '100%' : undefined, maxHeight: 'calc(100dvh - 32px)', display: 'flex', flexDirection: 'column' }}>
        <div className="dlg-title">{title}</div>
        <div className="dlg-scroll"><div className="host-settings in-dlg">
          {note && <div className="hs-note">{note}</div>}
          {children}
          <div className="hs-foot">
            <ReasonInput value={reason} onChange={setReason} />
            <Button variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button>
            <Button variant="primary" onClick={onApply} disabled={busy || !canApply}>{busy ? 'Working…' : applyLabel}</Button>
          </div>
        </div></div>
      </div>
    </div>,
    document.body,
  )
}

// postBulk runs a bulk action and reads its answer: how many it did and which it couldn't, with why.
async function postBulk(url: string, body: object, reason: string): Promise<BulkResult | string> {
  const res = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json', ...reasonHeader(reason) }, body: JSON.stringify(body) }).catch(() => null)
  if (!res || !res.ok) return errText(res, 'The action failed')
  return res.json().catch(() => 'The action failed')
}

// bulkToast reports a bulk action: "Paused 3 hosts", and the ones that failed with their reason.
function bulkToast(toast: ReturnType<typeof useToast>, r: BulkResult | string, what: string, noun: [string, string]) {
  if (typeof r === 'string') { toast.error(r); return }
  const n = (k: number) => `${k} ${k === 1 ? noun[0] : noun[1]}`
  if (r.failed.length === 0) { toast.success(`${what} ${n(r.done)}`); return }
  const first = r.failed[0]
  const more = r.failed.length > 1 ? ` (and ${r.failed.length - 1} more)` : ''
  const msg = `${r.done ? `${what} ${n(r.done)}; ` : ''}${n(r.failed.length)} failed: ${first.name || first.id}: ${first.error}${more}`
  if (r.done) toast.success(msg); else toast.error(msg)
}

function exportHosts(hosts: Host[], proxies: Proxy[]) {
  const probe = (id?: string) => (!id || id === '0' ? 'Server' : proxies.find((p) => p.id === id)?.name || id)
  downloadCSV(`argus-hosts-${csvStamp()}.csv`, ['Host', 'Groups', 'Probe', 'Class', 'State', 'Problems', 'Paused', 'Hidden', 'In maintenance', 'Tags', 'Ping (ms)'],
    hosts.map((h) => [h.name, (h.groups || []).join('; '), probe(h.proxy_id), h.class_id || '', h.state, h.problems, h.paused ? 'yes' : '', h.hidden ? 'yes' : '', h.maintenance ? h.maintenance.name : '',
      (h.tags || []).map((t) => t.name).join('; '), typeof h.icmp_ms === 'number' ? Math.round(h.icmp_ms * 100) / 100 : '']))
}

function exportSensors(rows: SensorRow[], title: string) {
  downloadCSV(`argus-${title.toLowerCase().replace(/[^a-z0-9]+/g, '-')}-${csvStamp()}.csv`, ['Host', 'Sensor', 'State', 'Severity', 'Reason', 'Value', 'Units', 'Since', 'Last check', 'Note'],
    rows.map((s) => [s.host_name, s.label || s.name, s.state, s.severity ? sevInfo(s.severity).label : '', s.reason || '', s.value, s.units, s.since ? csvTime(s.since) : '', s.last_clock ? csvTime(s.last_clock) : '', s.note ? s.note.text : '']))
}

// --- Probe and group filters, CSV export (shared by the long lists) ---

type HostFilterVal = { probes: string[]; groups: string[] }
const NO_HOST_FILTER: HostFilterVal = { probes: [], groups: [] }

// hostFilterQS is the filter as query parameters: one value each, so a group name may hold a comma.
function hostFilterQS(f: HostFilterVal): string {
  return f.probes.map((p) => `&probe=${encodeURIComponent(p)}`).join('') + f.groups.map((g) => `&group=${encodeURIComponent(g)}`).join('')
}

// useFilterOptions loads what the probe and group filters offer: the probes (the core server first,
// as "Server") and every group, read once per list.
function useFilterOptions(): { probes: { id: string; name: string }[]; groups: string[] } {
  const [probes, setProbes] = useState<{ id: string; name: string }[]>([])
  const [groups, setGroups] = useState<string[]>([])
  useEffect(() => {
    fetch('/api/proxies').then((r) => (r.ok ? r.json() : [])).then((px: Proxy[]) => setProbes([{ id: '0', name: 'Server' }, ...(px || []).map((p) => ({ id: p.id, name: p.name })).sort((a, b) => a.name.localeCompare(b.name))])).catch(() => {})
    fetch('/api/groups').then((r) => (r.ok ? r.json() : [])).then((gs: Group[]) => setGroups((gs || []).map((g) => g.name).sort((a, b) => a.localeCompare(b)))).catch(() => {})
  }, [])
  return { probes, groups }
}

// ProbeGroupFilter narrows a list to the hosts of some probes and some groups (a group covers its
// subgroups): the same two dropdowns on History, Changes and Inventory.
function ProbeGroupFilter({ value, onChange }: { value: HostFilterVal; onChange: (v: HostFilterVal) => void }) {
  const { probes, groups } = useFilterOptions()
  const probeName = (id: string) => probes.find((p) => p.id === id)?.name || id
  return (
    <>
      <SitePicker options={probes.map((p) => p.id)} labelOf={probeName} value={value.probes} onChange={(v) => onChange({ ...value, probes: v })} allLabel="All probes" noun="probes" />
      <SitePicker options={groups} value={value.groups} onChange={(v) => onChange({ ...value, groups: v })} allLabel="All groups" noun="groups" />
    </>
  )
}

// downloadCSV saves rows as a CSV file the way spreadsheets open it (UTF-8 with a BOM, CRLF lines,
// every cell quoted).
function downloadCSV(name: string, header: string[], rows: (string | number | undefined | null)[][]) {
  const cell = (v: string | number | undefined | null) => `"${String(v ?? '').replace(/"/g, '""')}"`
  const text = '\ufeff' + [header, ...rows].map((r) => r.map(cell).join(',')).join('\r\n') + '\r\n'
  const url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

// csvTime is a time as a spreadsheet sorts it: 2026-10-03 14:05.
function csvTime(unix: number): string {
  const d = new Date(unix * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function csvStamp(): string {
  return csvTime(Math.floor(Date.now() / 1000)).replace(/[: ]/g, '-').slice(0, 16)
}

function exportIncidents(rows: Incident[]) {
  downloadCSV(`argus-incidents-${csvStamp()}.csv`, ['What happened', 'Severity', 'Sensor', 'Host', 'Site', 'Started', 'Ended', 'Duration (min)', 'Acknowledged by', 'Reason', 'Note'],
    rows.map((r) => [r.name, sevInfo(r.severity).label, r.sensor, r.host_name, r.site, csvTime(r.start), r.end ? csvTime(r.end) : 'ongoing', r.end ? Math.round((r.end - r.start) / 60) : '', r.ack_by, r.reason, r.note]))
}

// --- The change log ---

// reasonHeader sends the reason someone gave for a change along with it, for the change log.
function reasonHeader(reason: string): Record<string, string> {
  const r = reason.trim()
  return r ? { 'X-Argus-Reason': encodeURIComponent(r.slice(0, 200)) } : {}
}

// ReasonInput is the optional "why" beside a dialog's Save; it shows in the change log.
function ReasonInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return <input className="input reason-input" placeholder="Reason for the change log (optional)" maxLength={200} value={value} onChange={(e) => onChange(e.target.value)} aria-label="Reason for this change" />
}

type ChangeDiff = { f: string; o: string; n: string }
type ChangeRow = { id: number; at: number; actor: string; by_argus?: boolean; category: string; action: string; object?: string; detail?: string; diff?: ChangeDiff[]; reason?: string; hosts?: { id: string; name: string }[] }

const CHANGE_CATEGORIES: [string, string][] = [
  ['', 'Everything'], ['hosts', 'Hosts and sensors'], ['states', 'Alert states'], ['thresholds', 'Thresholds'], ['groups', 'Groups'],
  ['maintenance', 'Maintenance'], ['discovery', 'Discovery and imports'], ['probes', 'Probes'], ['channels', 'Alert channels'],
  ['users', 'Users and sign-in'], ['statuspages', 'Status pages'], ['updates', 'Updates'], ['settings', 'Settings'],
]

// ChangeRows lists change log entries newest first: what changed (with each value before and after,
// the detail and the reason), who and when.
function ChangeRows({ rows, goHost }: { rows: ChangeRow[]; goHost?: (h: string) => void }) {
  return (
    <div className="enroll-scroll">
      <table className="slist slist-inc slist-chg">
        <thead><tr><th className="slgrow">What changed</th><th>Who</th><th>When</th></tr></thead>
        <tbody>
          {rows.map((c) => (
            <tr key={c.id}>
              <td className="slgrow chg-cell">
                <div className="inc-name">{c.action}{c.object ? <> · <span className="chg-obj">{c.object}</span></> : null}</div>
                {goHost && c.hosts && c.hosts.length > 1 && (
                  <div className="sreason">{c.hosts.map((h, i) => <span key={h.id}>{i ? ', ' : ''}<span className="lnk-host" onClick={() => goHost(h.id)}>{h.name || h.id}</span></span>)}</div>
                )}
                {(c.diff || []).map((d, i) => (
                  <div key={i} className="chg-diff"><span className="chg-f">{d.f}</span> {d.o ? <span className="chg-old">{d.o}</span> : null}{d.o ? <span className="chg-arr" aria-label="to">→</span> : null}<span className="chg-new">{d.n || 'none'}</span></div>
                ))}
                {c.detail && <div className="sreason">{c.detail}</div>}
                {c.reason && <div className="sreason chg-why">Reason: {c.reason}</div>}
              </td>
              <td data-label="Who">{c.by_argus ? <span className="chg-argus" title="Argus did this by itself">Argus</span> : (c.actor || <span className="muted">-</span>)}</td>
              <td className="mono" data-label="When" title={new Date(c.at * 1000).toLocaleString()}>{fmtWhen(c.at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function exportChanges(rows: ChangeRow[]) {
  downloadCSV(`argus-changes-${csvStamp()}.csv`, ['When', 'Who', 'What', 'Object', 'Hosts', 'Changed', 'Detail', 'Reason'],
    rows.map((c) => [csvTime(c.at), c.by_argus ? 'Argus' : c.actor, c.action, c.object, (c.hosts || []).map((h) => h.name || h.id).join(', '),
      (c.diff || []).map((d) => `${d.f}: ${d.o ? d.o + ' -> ' : ''}${d.n || 'none'}`).join('; '), c.detail, c.reason]))
}

// useChanges loads a change log page and pages back on demand.
function useChanges(url: string | null): { rows: ChangeRow[] | null; err: string; more: boolean; keep: number; loadMore: () => void; loading: boolean } {
  const [rows, setRows] = useState<ChangeRow[] | null>(null)
  const [err, setErr] = useState('')
  const [more, setMore] = useState(false)
  const [keep, setKeep] = useState(365)
  const [loading, setLoading] = useState(false)
  useEffect(() => {
    if (!url) return
    let live = true
    setRows(null); setErr('')
    fetch(url).then(async (r) => { if (!r.ok) throw new Error('changes'); return r.json() })
      .then((d) => { if (live) { setRows(d.changes || []); setMore(!!d.more); setKeep(d.keep_days || 365) } })
      .catch(() => { if (live) setErr('Could not load the changes') })
    return () => { live = false }
  }, [url])
  const loadMore = () => {
    if (!url || !rows || !rows.length || loading) return
    setLoading(true)
    fetch(`${url}${url.includes('?') ? '&' : '?'}before=${rows[rows.length - 1].id}`).then((r) => (r.ok ? r.json() : null))
      .then((d) => { if (d) { setRows((cur) => [...(cur || []), ...(d.changes || [])]); setMore(!!d.more) } })
      .catch(() => {}).finally(() => setLoading(false))
  }
  return { rows, err, more, keep, loadMore, loading }
}

function keepText(days: number): string {
  return days % 365 === 0 ? (days === 365 ? '1 year' : `${days / 365} years`) : `${days} days`
}

// ChangesView is the admin change log: every change, who made it and when, with the values before
// and after, filtered by text, kind, probe, group and period.
function ChangesView({ goHost }: { goHost: (h: string) => void }) {
  const [q, setQ] = useState('')
  const [needle, setNeedle] = useState('')
  const [cat, setCat] = useState('')
  const [days, setDays] = useState(7)
  const [hf, setHf] = useState<HostFilterVal>(NO_HOST_FILTER)
  useEffect(() => { const t = window.setTimeout(() => setNeedle(q.trim()), 300); return () => clearTimeout(t) }, [q])
  const from = Math.floor(Date.now() / 1000 / 60) * 60 - days * 86400
  const url = `/api/changes?from=${from}${cat ? `&cat=${cat}` : ''}${needle ? `&q=${encodeURIComponent(needle)}` : ''}${hostFilterQS(hf)}`
  const { rows, err, more, keep, loadMore, loading } = useChanges(url)
  const period = days === 1 ? 'the last 24 hours' : days === 365 ? 'the last year' : `the last ${days} days`
  return (
    <div className="panel">
      <div className="phead">
        <PanelTitle eyebrow="Admin">Changes</PanelTitle>
        <span className="hint">{rows ? `${rows.length}${more ? '+' : ''} in ${period} · kept for ${keepText(keep)}` : 'who changed what'}</span>
        <div className="tools hist-tools">
          <input className="input hist-q" placeholder="Who, what, host or setting" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search the changes" />
          <Select value={cat} onChange={(e) => setCat(e.target.value)} aria-label="Kind of change" style={{ width: 'auto' }}>
            {CHANGE_CATEGORIES.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
          </Select>
          <ProbeGroupFilter value={hf} onChange={setHf} />
          <div className="seg">
            {[1, 7, 30, 365].map((d) => <button key={d} className={days === d ? 'on' : ''} onClick={() => setDays(d)}>{d === 1 ? '24h' : d === 365 ? '1y' : `${d}d`}</button>)}
          </div>
          <button className="btn" disabled={!rows || !rows.length} onClick={() => rows && exportChanges(rows)}>Export CSV</button>
        </div>
      </div>
      {err && <div style={{ padding: '0.9rem 16px', color: 'var(--err)' }}>{err}</div>}
      {rows === null && !err && <Skeleton rows={5} cols={3} />}
      {rows !== null && !err && (rows.length === 0
        ? <EmptyState icon={ic.changes} title="No changes" text={needle || cat || hf.probes.length || hf.groups.length ? 'Nothing in this period matches the filters.' : `Nobody changed anything in ${period}.`} />
        : <>
          <ChangeRows rows={rows} goHost={goHost} />
          {more && <div className="chg-more"><button className="btn" disabled={loading} onClick={loadMore}>{loading ? 'Loading…' : 'Show older changes'}</button></div>}
        </>)}
    </div>
  )
}

// HostChanges is a host's own change log, folded under its history until opened.
function HostChanges({ hostId, asTab }: { hostId: string; asTab?: boolean }) {
  const [open, setOpen] = useState(false)
  const { rows, err, more, loadMore, loading } = useChanges(`/api/hosts/${hostId}/changes`)
  if (asTab) {
    if (err) return <div className="tab-empty txt-err">{err}</div>
    if (!rows) return <Skeleton rows={3} cols={3} />
    if (rows.length === 0) return <div className="tab-empty">Nothing changed on this host yet.</div>
    return <><ChangeRows rows={rows} />{more && <div className="chg-more"><button className="btn" disabled={loading} onClick={loadMore}>{loading ? 'Loading…' : 'Show older changes'}</button></div>}</>
  }
  if (err || !rows) return null
  const last = rows[0]
  return (
    <div className="hinc">
      <button type="button" className="hinc-head" onClick={() => setOpen((o) => !o)} aria-expanded={open} disabled={rows.length === 0}>
        {rows.length > 0 && <svg className={'chev' + (open ? ' open' : '')} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M9 6l6 6-6 6" /></svg>}
        <span className="hinc-t">Changes</span>
        <span className="hinc-s">{rows.length === 0 ? 'nothing changed yet' : `${rows.length}${more ? '+' : ''} · last ${relTime(last.at)}: ${last.action.toLowerCase()}${last.by_argus ? ' by Argus' : last.actor ? ` by ${last.actor}` : ''}`}</span>
      </button>
      {open && rows.length > 0 && <>
        <ChangeRows rows={rows} />
        {more && <div className="chg-more"><button className="btn" disabled={loading} onClick={loadMore}>{loading ? 'Loading…' : 'Show older changes'}</button></div>}
      </>}
    </div>
  )
}

// PanelTitle is a panel's title with a small label above it: the sidebar section it belongs to and,
// for a list spanning sites, its scope ("Watch · all sites").
function PanelTitle({ eyebrow, children }: { eyebrow: string; children: ReactNode }) {
  return <div className="ptitle"><div className="eyebrow">{eyebrow}</div><h2>{children}</h2></div>
}

// HostIncidents is the host card's history: the last 30 days, folded until opened. With itemIds (a
// drilled-down sensor, or every channel of its group) it is that sensor's history, open from the start.
function HostIncidents({ hostId, goHost, itemIds, asTab }: { hostId: string; goHost: ((h: string) => void) | null; itemIds?: string[]; asTab?: boolean }) {
  const items = itemIds && itemIds.length ? itemIds.join(',') : ''
  const [withHidden, setWithHidden] = useState(false)
  const [rows, err, hidden] = useIncidents(`/api/hosts/${hostId}/incidents?days=30${items ? `&items=${items}` : ''}${withHidden ? '&hidden=1' : ''}`)
  const [open, setOpen] = useState(!!items)
  if (asTab) {
    if (err) return <div className="tab-empty txt-err">{err}</div>
    if (!rows) return <Skeleton rows={3} cols={4} />
    return (
      <>
        <div className="tab-head">{rows.length === 0 ? 'No incidents in the last 30 days.' : `${rows.length} incident${rows.length === 1 ? '' : 's'} in the last 30 days`}{hidden > 0 && <HiddenToggle n={hidden} shown={withHidden} onToggle={() => setWithHidden((w) => !w)} />}</div>
        {rows.length > 0 && <IncidentRows rows={rows} goHost={null} />}
      </>
    )
  }
  if (err || !rows) return null
  const live = rows.filter((r) => !r.end).length
  return (
    <div className="hinc">
      <button type="button" className="hinc-head" onClick={() => setOpen((o) => !o)} aria-expanded={open} disabled={rows.length === 0}>
        {rows.length > 0 && <svg className={'chev' + (open ? ' open' : '')} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><path d="M9 6l6 6-6 6" /></svg>}
        <span className="hinc-t">{items ? 'Sensor history' : 'History'}</span>
        <span className="hinc-s">{rows.length === 0 ? 'no incidents in the last 30 days' : `${rows.length} incident${rows.length === 1 ? '' : 's'} in the last 30 days${live ? ` · ${live} open` : ''}`}</span>
      </button>
      {/* A drilled-down sensor always shows its own incidents; the host's list leaves hidden sensors out. */}
      {!items && hidden > 0 && <span className="hinc-s hinc-hidden"><HiddenToggle n={hidden} shown={withHidden} lead={false} onToggle={() => { setWithHidden((w) => !w); setOpen(true) }} /></span>}
      {open && rows.length > 0 && <IncidentRows rows={rows} goHost={goHost} />}
    </div>
  )
}

function HostItems({ hostId, canPause, hostPaused, hostHidden, maintenance, showAll, autoOpenItem, onlyItem, onDrillSensor, onItemName, onNavigate, onOpenSettings, heldBehind }: { hostId: string; canPause: boolean; hostPaused: boolean; hostHidden: boolean; maintenance?: MaintHit; showAll: boolean; autoOpenItem?: string; onlyItem?: string; onDrillSensor?: (itemId: string, itemName: string) => void; onItemName?: (itemId: string, itemName: string) => void; onNavigate: (hostId: string | null, itemId: string | null) => void; onOpenSettings?: () => void; heldBehind?: string }) {
  // The host's tabs: its sensors (the default), its Device facts, History, Journal and Changes.
  const toast = useToast()
  const [tab, setTab] = useState<HostTab>('sensors')
  const [countTick, setCountTick] = useState(0) // bumped when a tab changes what the labels count
  const notes = useNoteEditor()
  const [items, setItems] = useState<SensorItem[] | null>(null)
  const [problems, setProblems] = useState<Problem[]>([])
  const [error, setError] = useState<string | null>(null)
  const [openItem, setOpenItem] = useState<string | null>(null)
  const [whyOpen, toggleWhy] = useWhyOpen()

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
  // Latched per target: `items` is re-fetched every 30s, and without the latch this re-fired on every
  // poll and yanked the view back to the deep-linked sensor after the user had opened another chart.
  // A channel of a multi-channel instance opens as its GROUP row (the group key drives openItem),
  // so deep links and drills that carry a member item id land on the whole group, not a lone
  // channel ripped out of it.
  const openKeyFor = (it: SensorItem) => (it.instance ? 'g:' + (it.category || '') + '|' + it.instance : it.id)
  const autoOpened = useRef<string | null>(null)
  useEffect(() => {
    if (!autoOpenItem || autoOpened.current === autoOpenItem) return
    const it = items?.find((i) => i.id === autoOpenItem)
    if (it) { setOpenItem(openKeyFor(it)); autoOpened.current = autoOpenItem }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoOpenItem, items])

  // When focused on a single sensor, force its chart open and report its display name up so the
  // breadcrumb can label the crumb (needed after a reload, where only the item id survives the URL).
  // Latched like autoOpenItem, so the 30s poll can't force a manually-collapsed chart back open.
  const onlyOpened = useRef<string | null>(null)
  useEffect(() => {
    if (!onlyItem || !items || onlyOpened.current === onlyItem) return
    const it = items.find((i) => i.id === onlyItem)
    if (!it) return
    setOpenItem(openKeyFor(it))
    onItemName?.(onlyItem, it.instance || it.label || it.name)
    onlyOpened.current = onlyItem
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
  // Mute / unmute a sensor's alerts = disable / enable its Zabbix triggers (the item keeps collecting).
  // Accepts several item ids so a whole group, or one channel, can be toggled at once.
  // Run sensors now instead of at their next interval (a sensor read from another runs that one, once).
  async function checkNow(ids: string[], what: string) {
    const res = await fetch('/api/items/check', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ item_ids: ids }) }).catch(() => null)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not check it now')); return }
    toast.success(`Checking ${what} now: the new reading arrives within a minute or so`)
    setTimeout(() => { loadItems(false); fireDataRefresh() }, 40000)
  }
  async function muteItems(ids: string[], mute: boolean) {
    if (ids.length === 0) return
    setBusyItem(ids[0])
    await Promise.all(ids.map((id) => fetch(`/api/items/${id}/mute`, { method: mute ? 'POST' : 'DELETE' }).catch(() => {})))
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
  const speedItems = (items || []).filter((i) => i.key.startsWith('speedtest.'))
  const speedPair = useSpeedPair(hostId, speedItems.some((i) => i.key === 'speedtest.down'), Math.max(0, ...speedItems.map((i) => i.last_clock || 0)))
  // Daily-resetting items (AdGuard's today counters + block rate) additionally get per-day
  // buckets, for the daily mini bars in their rows.
  const dailies = useDailies((items || []).filter((i) => { const b = i.key.replace(/\[.*$/, ''); return i.numeric && (BAR_COUNTER_KEYS.has(b) || BAR_RATE_KEYS.has(b)) }).map((i) => i.id))

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

  // Group per-instance sensors (disk mounts, NICs) into one collapsible "channel group" row, so a
  // host with many LLD sensors reads like PRTG instead of a flat wall. Curated view only; "All
  // sensors" and single-sensor focus stay flat.
  // Focusing a member of a multi-channel instance focuses the whole GROUP (grouping stays on and
  // every sibling channel is shown) - a drilled "DNS activity" or "ICMP" reads exactly like its
  // row in the full list, not like one channel ripped out of it.
  const focusItem = onlyItem && items ? items.find((i) => i.id === onlyItem) : undefined
  const focusInst = focusItem && focusItem.instance ? { cat: focusItem.category || '', inst: focusItem.instance } : undefined
  const grouped = (!onlyItem || !!focusInst) && !showAll
  type Row = { cat: string; showCat?: boolean } & ({ kind: 'item'; item: SensorItem } | { kind: 'group'; instance: string; items: SensorItem[] })
  const shownItems = onlyItem
    ? (focusInst ? items.filter((i) => (i.category || '') === focusInst.cat && i.instance === focusInst.inst) : items.filter((i) => i.id === onlyItem))
    : items
  const rows: Row[] = []
  if (!grouped) {
    shownItems.forEach((it) => rows.push({ kind: 'item', cat: it.category || '', item: it }))
  } else {
    const at = new Map<string, number>()
    for (const it of shownItems) {
      if (it.instance) {
        const k = (it.category || '') + '|' + it.instance
        const i = at.get(k)
        if (i === undefined) { at.set(k, rows.length); rows.push({ kind: 'group', cat: it.category || '', instance: it.instance, items: [it] }) }
        else { const r = rows[i]; if (r.kind === 'group') r.items.push(it) }
      } else {
        rows.push({ kind: 'item', cat: it.category || '', item: it })
      }
    }
  }
  // A one-member group is just a single sensor - but it keeps the GROUP's name, so a "WAN 2
  // quality" whose latency member has no data yet still reads like its fully-populated sibling.
  const finalRows: Row[] = rows.map((r) => (r.kind === 'group' && r.items.length === 1 ? { kind: 'item', cat: r.cat, item: { ...r.items[0], label: r.instance } } : r))
  { let prev = ''; finalRows.forEach((r) => { r.showCat = grouped && !!r.cat && r.cat !== prev; prev = r.cat || prev }) }

  // Headline reading for a collapsed group: disk shows Used %, network shows down/up, else the first.
  const reading = (it?: SensorItem): ReactNode => { if (!it || !it.supported) return null; const [dv, du] = readingParts(it.last_value, it.units); return <>{dv}{du ? <span className="unit"> {du}</span> : null}</> }
  // A group whose up/down channel reads down headlines that, with the channel's reason (why/whyId).
  function groupHeadline(cat: string, gi: SensorItem[]): { node: ReactNode; primary: SensorItem; why?: string; whyId?: string } {
    if (cat === 'Network') { const inn = gi.find((x) => x.channel === 'In'), out = gi.find((x) => x.channel === 'Out'); return { node: <span className="vpair"><span>↓ {reading(inn) ?? '-'}</span><span>↑ {reading(out) ?? '-'}</span></span>, primary: inn || gi[0] } }
    if (cat === 'Disk') { const pu = gi.find((x) => (x.channel || '').startsWith('Used %')) || gi[0]; return { node: reading(pu), primary: pu } }
    if (cat === 'Internet') {
      const dn = gi.find((x) => x.channel === 'Download'), upl = gi.find((x) => x.channel === 'Upload'), ran = gi.find((x) => x.channel === 'Ran')
      if (dn || upl) {
        if (ran && ran.last_value !== '' && Number(ran.last_value) === 0) return { node: <span style={{ color: 'var(--muted)' }}>could not run</span>, primary: dn || upl || gi[0], why: ran.why, whyId: ran.id }
        return { node: <span className="vpair"><span>↓ {reading(dn) ?? '-'}</span><span>↑ {reading(upl) ?? '-'}</span></span>, primary: dn || upl || gi[0] }
      }
      const idle = gi.find((x) => x.channel === 'Idle') || gi[0]
      return { node: reading(idle), primary: idle }
    }
    if (cat === 'Ping' || cat === 'Web' || cat === 'TCP' || cat === 'Cloud services') {
      const rt = gi.find((x) => x.channel === 'Response time') || gi[0]
      // A TCP port that isn't answering, or a URL that doesn't answer as expected, says so (its reason
      // is on the Reachable channel's row).
      const up = gi.find((x) => x.channel === 'Reachable')
      if ((cat === 'TCP' || cat === 'Web' || cat === 'Cloud services') && up && up.last_value !== '' && Number(up.last_value) === 0) return { node: <span style={{ color: 'var(--muted)' }}>{cat === 'TCP' ? 'not answering' : 'down'}</span>, primary: rt, why: up.why, whyId: up.id }
      return { node: reading(rt), primary: rt }
    }
    // A push sensor reads how long ago its job last ran; a failed last run says so, with the job's message.
    if (cat === 'Push') {
      const age = gi.find((x) => x.channel === 'Since last run'), last = gi.find((x) => x.channel === 'Last run')
      const primary = age || gi[0]
      if (last && last.last_value === 'Failed') return { node: <span style={{ color: 'var(--muted)' }}>failed</span>, primary, why: last.why, whyId: last.id }
      return { node: age && age.supported ? <>{reading(age)} ago</> : reading(primary), primary }
    }
    // A temperature group (unRAID disk temps) or CPU cores group reads as its HOTTEST/BUSIEST
    // member - the one you'd act on; that member also drives the sparkline and the chart's main line.
    if (cat === 'Temperature' || cat === 'CPU' || cat === 'Probe') {
      let hot: SensorItem | undefined
      for (const x of gi) { if (x.supported && x.numeric && x.last_value !== '' && (!hot || Number(x.last_value) > Number(hot.last_value))) hot = x }
      if (hot) return { node: reading(hot), primary: hot }
    }
    // A radio reads clients-first ("3 clients · 12 %"), with Clients as primary so the row's
    // sparkline and the chart's main line show who's on the air; utilization tags along.
    if (cat === 'Wireless') {
      const cl = gi.find((x) => x.channel === 'Clients'), ut = gi.find((x) => x.channel === 'Utilization')
      if (cl) return { node: <span>{reading(cl)} clients{ut ? <> &nbsp;·&nbsp; {reading(ut)}</> : null}</span>, primary: cl }
    }
    // A port reads like a network interface: traffic in/out, with the In channel as primary (it
    // drives the row's sparkline and the chart's main line). A down port just says so.
    if (cat === 'Ports') {
      const inn = gi.find((x) => x.channel === 'In'), out = gi.find((x) => x.channel === 'Out')
      const primary = inn || gi[0]
      const ln = gi.find((x) => x.channel === 'Link')
      if (ln && ln.last_value !== '' && Number(ln.last_value) === 0) return { node: <span style={{ color: 'var(--muted)' }}>down</span>, primary }
      return { node: <span className="vpair"><span>↓ {reading(inn) ?? '-'}</span><span>↑ {reading(out) ?? '-'}</span></span>, primary }
    }
    // A DNS name reads by what it resolves to (the IP), collapsing the pass/fail + timing channels;
    // response time drives the sparkline. A name that isn't resolving says so instead.
    if (cat === 'DNS') {
      // AdGuard's activity group reads TODAY's counts - the bucket /api/daily assigned to today's
      // LOCAL date, not the raw reading: AdGuard's stats day is UTC-aligned, so between local
      // midnight and its rollover the raw counter still carries yesterday's total; the today
      // bucket is honestly 0 then (matching the chart's empty today column). Total is primary.
      // The per-name resolve groups below read by their Resolved IP instead.
      const tot = gi.find((x) => x.channel === 'Total'), blk = gi.find((x) => x.channel === 'Blocked')
      if (tot || blk) {
        const today = (it?: SensorItem) => { const a = it && dailies[it.id]; return a && a.length ? a[a.length - 1] : undefined }
        const tq = today(tot), tb = today(blk)
        const node = tq == null
          ? <span style={{ color: 'var(--muted)' }}>…</span>
          : <span>{fmtNum(tq, '')} queries &nbsp;·&nbsp; {tb == null ? '-' : fmtNum(tb, '')} blocked <span style={{ color: 'var(--faint)', fontSize: 11 }}>today</span></span>
        return { node, primary: tot || gi[0] }
      }
      const ip = gi.find((x) => x.channel === 'Resolved IP')
      const rt = gi.find((x) => x.channel === 'Response time')
      const ok = gi.find((x) => x.channel === 'Resolves')
      const primary = rt || ip || gi[0]
      if (ok && ok.last_value !== '' && Number(ok.last_value) === 0) return { node: <span style={{ color: 'var(--muted)' }}>not resolving</span>, primary, why: ok.why, whyId: ok.id }
      return { node: reading(ip) ?? '-', primary }
    }
    return { node: reading(gi[0]), primary: gi[0] }
  }

  // The problems banner takes the worst severity nobody has acknowledged: red for errors, amber when
  // it's warnings only, the acknowledged colour once every problem is.
  const open = problems.filter((p) => !p.acknowledged)
  const probErr = open.some((p) => p.state === 'error')
  const probColor = open.length === 0 ? 'var(--acked)' : probErr ? 'var(--err)' : 'var(--warn)'
  return (
    <div>
      {!onlyItem && <HostUptime hostId={hostId} />}
      {maintenance && (
        <div className="maint-band">
          <span className="maint-ico">{ic.maintenance}</span>
          <span>In maintenance: <b>{maintenance.name}</b>, until {fmtWhen(maintenance.until)}. Alerts for this host wait meanwhile; whatever is still wrong when it ends is alerted then.</span>
        </div>
      )}
      {heldBehind && (
        <div className="held-band">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"><path d="M12 3l9 16H3z" /><path d="M12 10v4M12 17h.01" /></svg>
          <span>Behind <b>{heldBehind}</b>, which is down: Argus can't reach this host through it, so its alerts wait until {heldBehind} is back. Whatever is still wrong then is alerted.</span>
        </div>
      )}
      {!onlyItem && <HostTabs hostId={hostId} tab={tab} setTab={setTab} sensors={items.length} tick={countTick} />}
      {tab !== 'sensors' && !onlyItem && <HostTabBody hostId={hostId} tab={tab} canEdit={canPause} onOpenSettings={onOpenSettings} onChanged={() => setCountTick((t) => t + 1)} />}
      {(tab === 'sensors' || onlyItem) && problems.length > 0 && (
        <div style={{ border: `1px solid color-mix(in srgb, ${probColor} 30%, var(--border))`, background: `color-mix(in srgb, ${probColor} 7%, var(--panel))`, borderRadius: 8, padding: '0.5rem 0.75rem', marginBottom: '0.5rem' }}>
          <div style={{ color: probColor, fontSize: 12, marginBottom: 4, fontWeight: 600 }}>{open.length === 0 ? 'Acknowledged problems' : probErr ? 'Active problems' : 'Active warnings'}</div>
          {problems.map((p, i) => (
            <div key={i} style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', padding: '0.2rem 0' }}>
              <span className={'sdot' + (p.acknowledged ? '' : ' pulse')} style={{ '--dot': healthColor(p.state, p.acknowledged) } as CSSProperties} />
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
      {(tab !== 'sensors' && !onlyItem) ? null : items.length === 0
        ? <div style={{ color: 'var(--muted)', padding: '0.2rem 0 0.4rem' }}>{showAll ? 'No sensors.' : 'No recognized sensors - try “All sensors”.'}</div>
        : (
          <table className="sensors">
            <thead><tr><th>Sensor</th><th>Value</th><th>Trend</th><th>Priority</th><th style={{ textAlign: 'right' }}>Last check</th></tr></thead>
            <tbody>
              {finalRows.map((row) => {
                const catRow = row.showCat ? <tr className="cat"><td colSpan={5}>{row.cat}</td></tr> : null
                if (row.kind === 'group') {
                  const gkey = 'g:' + row.cat + '|' + row.instance
                  const open = openItem === gkey
                  const { node: headline, primary, why: gWhy, whyId: gWhyId } = groupHeadline(row.cat, row.items)
                  // A spun-down unRAID disk drops out of the temperature extend: Zabbix flags its item
                  // "not supported" but keeps the last reading. Keep such a parked drive on the chart
                  // (its last value seeds a flat hold in buildMultiPlot) instead of filtering it out.
                  let channels: GroupChan[] = row.items.filter((i) => i.numeric && (i.supported || (row.cat === 'Temperature' && i.last_value !== ''))).map((i) => {
                    if (((row.cat === 'Ping' || row.cat === 'Web' || row.cat === 'TCP' || row.cat === 'Cloud services') && i.channel === 'Reachable') || (row.cat === 'DNS' && i.channel === 'Resolves'))
                      return { id: i.id, label: 'Downtime', units: '', invert: true } // show only when unreachable / not-resolving (PRTG-style)
                    if (row.cat === 'Push' && i.channel === 'Last run') return { id: i.id, label: 'Failed', units: '', invert: true, stepped: true } // a band from a failed run to the next one
                    if (row.cat === 'Internet' && i.channel === 'Ran') return { id: i.id, label: 'Result', units: '', invert: true } // a ✕ at each run that couldn't measure
                    // A port's Speed and Link are constants - start their lines hidden (legend keeps
                    // the value; a click reveals the line). Hiding Speed also lets the bps axis
                    // range to the In/Out traffic instead of pinning at the negotiated gigabits.
                    const c: GroupChan = { id: i.id, label: i.channel || i.label || i.name, units: i.units, defaultOff: row.cat === 'Ports' && (i.channel === 'Speed' || i.channel === 'Link'), thr: i.thr }
                    // Temperature channels hold their last reading flat while the drive is parked - seed
                    // the hold from the current last value/time (see buildMultiPlot's LOCF pass).
                    if (row.cat === 'Temperature') { const sv = Number(i.last_value); c.hold = true; if (Number.isFinite(sv)) c.seedValue = sv; c.seedClock = i.last_clock }
                    if (row.cat === 'Internet' && i.channel === 'Upload') c.color = SERIES_COLORS[2] // green, as before the order was fixed
                    return c
                  })
                  // Put the primary/headline channel first so it owns the left axis + the accent colour -
                  // EXCEPT for the max-member groups (CPU cores, disk temps): their primary is "whichever
                  // member is busiest/hottest right now", and hoisting it would reshuffle the legend order
                  // and every line's colour on each refresh. Those keep their natural order (Core 1..N,
                  // drives alphabetical); the row's value and sparkline still follow the max member.
                  if (row.cat !== 'CPU' && row.cat !== 'Temperature') {
                    const pIdx = channels.findIndex((cc) => cc.id === primary.id)
                    if (pIdx > 0) channels = [channels[pIdx], ...channels.slice(0, pIdx), ...channels.slice(pIdx + 1)]
                  }
                  // Disk reads Used % -> Used -> Total (user pref: live values first, static Total last).
                  if (row.cat === 'Disk') { const rank: Record<string, number> = { 'Used %': 0, Used: 1, Total: 2 }; channels = [channels[0], ...channels.slice(1).sort((a, b) => (rank[a.label] ?? 9) - (rank[b.label] ?? 9))] }
                  // The speed test reads Download, Upload, Result; its round trips Idle, Jitter, then while busy.
                  if (row.cat === 'Internet') { const rank: Record<string, number> = { Download: 0, Upload: 1, Result: 2, Idle: 0, Jitter: 1, 'While downloading': 2, 'While uploading': 3 }; channels = [...channels].sort((a, b) => (rank[a.label] ?? 9) - (rank[b.label] ?? 9)) }
                  // Ports read In -> Out -> PoE, so In/Out carry the same colours as the network
                  // groups; the constant Speed/Link (hidden lines) trail behind.
                  if (row.cat === 'Ports') { const rank: Record<string, number> = { In: 0, Out: 1, PoE: 2, Speed: 3, Link: 4 }; channels = [channels[0], ...channels.slice(1).sort((a, b) => (rank[a.label] ?? 9) - (rank[b.label] ?? 9))] }
                  // Radios read Clients -> Utilization (clients drive the group).
                  if (row.cat === 'Wireless') { const rank: Record<string, number> = { Clients: 0, Utilization: 1 }; channels = [channels[0], ...channels.slice(1).sort((a, b) => (rank[a.label] ?? 9) - (rank[b.label] ?? 9))] }
                  // Drive temperatures read in unRAID Main-tab order (parity, data disks, then pools);
                  // within a bucket - and for plain device names (Ugreen: nvme0, nvme1, sda, sdb) - sort
                  // naturally, so it reads alphabetically with numbers in order (Disk 2 before Disk 10,
                  // nvme0 before nvme1 before sda).
                  if (row.cat === 'Temperature') {
                    const bucket = (l: string) => (/^parity/i.test(l) ? 0 : /^disk ?\d/i.test(l) ? 1 : /^cache/i.test(l) ? 2 : 3)
                    channels = [...channels].sort((a, b) => bucket(a.label) - bucket(b.label) || a.label.localeCompare(b.label, undefined, { numeric: true }))
                  }
                  // A group whose members are all counter totals charts as daily stacked bars.
                  const barGroup = row.items.length > 0 && row.items.every((i) => BAR_COUNTER_KEYS.has(i.key.replace(/\[.*$/, '')))
                  const clickable = channels.length > 0
                  const gState = row.items.reduce((w, i) => { const s = itemState[i.id]; return s && (!w || stateRank[s] > stateRank[w]) ? s : w }, '')
                  const gAcked = !row.items.some((i) => itemState[i.id] && itemAcked[i.id] === false)
                  const gPaused = hostPaused || row.items.every((i) => i.paused)
                  const gHidden = hostHidden || row.items.every((i) => i.hidden)
                  const rowClass = gHidden ? 'hidden' : gPaused ? 'paused' : gState ? (gAcked ? 'acked' : (gState === 'error' ? 'err' : 'warn')) : ''
                  const trendColor = gState ? healthColor(gState, gAcked) : 'var(--accent)'
                  const gPrio = Math.max(...row.items.map((i) => i.priority))
                  // Pause/Hide/Ack act on every channel of the instance (you pause a disk, not one metric).
                  const acts: KAction[] = []
                  if (!hostPaused) acts.push(row.items.every((i) => i.paused)
                    ? { label: 'Resume', icon: kbIcon.resume, onClick: () => row.items.forEach((i) => i.paused && clearItemState(i, 'pause')) }
                    : { label: 'Pause', icon: kbIcon.pause, onPick: (s) => row.items.forEach((i) => setItemState(i, 'pause', s)) })
                  if (!hostHidden) acts.push(row.items.every((i) => i.hidden)
                    ? { label: 'Show', icon: kbIcon.show, onClick: () => row.items.forEach((i) => i.hidden && clearItemState(i, 'hide')) }
                    : { label: 'Hide', icon: kbIcon.hide, onPick: (s) => row.items.forEach((i) => setItemState(i, 'hide', s)) })
                  // Disable/enable alerts for the whole group (all its channels' triggers). Per-channel
                  // muting is in the expanded chart panel below.
                  const gAlertable = row.items.filter((i) => i.alertable)
                  if (gAlertable.length) acts.push(gAlertable.every((i) => i.alerts_off)
                    ? { label: 'Enable alerts', icon: kbIcon.unmute, onClick: () => muteItems(gAlertable.map((i) => i.id), false) }
                    : { label: 'Disable alerts', icon: kbIcon.mute, onClick: () => muteItems(gAlertable.filter((i) => !i.alerts_off).map((i) => i.id), true) })
                  if (!gPaused) acts.push({ label: 'Check now', icon: kbIcon.discover, onClick: () => checkNow(row.items.map((i) => i.id), row.instance) })
                  const gUnacked = problems.filter((p) => !p.acknowledged && p.item_ids.some((id) => row.items.some((i) => i.id === id)))
                  // A note on a group goes on each of its channels in trouble (and comes off them all).
                  const gTrouble = row.items.filter((i) => problems.some((p) => p.item_ids.includes(i.id)))
                  const gNoted = row.items.filter((i) => i.note)
                  const gNoteKeys = [...new Set([...gTrouble, ...gNoted].map((i) => i.id))]
                  const actions: KAction[] = []
                  if (gUnacked.length) actions.push({ label: 'Acknowledge', icon: kbIcon.ack, onPick: (s) => gUnacked.forEach((p) => ack(p, s)) })
                  if (gTrouble.length || gNoted.length) actions.push(...noteActions(notes, gNoteKeys, row.instance, gNoted[0]?.note))
                  if (actions.length && acts.length) actions.push({ sep: true, label: '' })
                  actions.push(...acts)
                  return (
                    <Fragment key={gkey}>
                      {catRow}
                      <tr className={rowClass} onClick={clickable ? () => setOpenItem(open ? null : gkey) : undefined} style={{ cursor: clickable ? 'pointer' : 'default' }}>
                        <td className="namecell">
                          <span className={'sname' + (clickable ? ' sclick' : '')} style={{ display: 'flex', alignItems: 'center', gap: 6, opacity: gPaused || gHidden ? 0.6 : 1 }}>
                            {clickable && <span className="scaret" style={{ color: 'var(--accent)', display: 'inline-block', transition: 'transform 0.15s', transform: open ? 'rotate(90deg)' : 'none' }}>›</span>}
                            {/* Drill on the name, like flat sensors: the group travels as its primary channel's
                                item id (the focus/URL machinery is item-id based) and HostItems re-expands that
                                id into the whole group. */}
                            <span className="sn-label">{onDrillSensor ? <span className="lnk-sensor" onClick={(e) => { e.stopPropagation(); onDrillSensor(primary.id, row.instance) }}>{row.instance}</span> : row.instance}</span>
                            <span style={{ color: 'var(--faint)', fontSize: 11 }}> · {row.items.length} channels</span>
                            {gPaused && <span style={{ color: PAUSED_BLUE, fontSize: 11 }}> (paused)</span>}
                            {gHidden && <span style={{ color: HIDDEN_GREY, fontSize: 11 }}> (hidden)</span>}
                            {(() => { const al = row.items.filter((i) => i.alertable); if (!al.length) return null; const n = al.filter((i) => i.alerts_off).length; if (n === 0) return null; return <span style={{ color: 'var(--faint)', fontSize: 11 }}> ({n === al.length ? 'alerts off' : n + ' muted'})</span> })()}
                          </span>
                        </td>
                        <td className="mono val">{gWhy && gWhyId ? <WhyText why={gWhy} onToggle={() => toggleWhy(gWhyId)}>{headline}</WhyText> : headline ?? <span style={{ color: 'var(--muted)' }}>-</span>}</td>
                        <td className="strend">{clickable ? (() => {
                          // Counter-total groups show the daily mini bars (a miniature of the big
                          // bar chart) instead of a drifting rolling-total line.
                          if (barGroup) {
                            const tot = row.items.find((x) => x.channel === 'Total'), blk = row.items.find((x) => x.channel === 'Blocked')
                            return <BarSpark total={tot ? dailies[tot.id] : undefined} blocked={blk ? dailies[blk.id] : undefined} width={168} />
                          }
                          // The speed test's Speed row: download and upload, one step per run, on one scale.
                          if (row.cat === 'Internet' && row.instance === 'Speed' && speedPair) return <PairSpark a={speedPair.down} b={speedPair.up} colorA={trendColor} colorB={SERIES_COLORS[2]} width={168} />
                          // Traffic-style groups (anything with In + Out channels: NICs, uplinks,
                          // switch ports) spark the SUM of both directions - total throughput.
                          const gin = row.items.find((x) => x.channel === 'In'), gout = row.items.find((x) => x.channel === 'Out')
                          const vals = gin && gout ? sumSparks(sparks[gin.id], sparks[gout.id]) : sparks[primary.id]
                          // One line, the main reading's: banded by that reading's own thresholds (only a
                          // summed In + Out has none), whatever its siblings or the row's state.
                          return <Spark values={vals} color={trendColor} width={168} units={gin && gout ? undefined : primary.units} thr={gin && gout ? undefined : primary.thr} />
                        })() : null}</td>
                        <td className="prio-cell" data-label="Priority"><PriorityStars value={gPrio} canEdit={canPause} onSet={(p) => row.items.forEach((i) => setItemPriority(i, p))} /></td>
                        <td><div className="lccell"><span className="when">{relTime(Math.max(...row.items.map((x) => x.last_clock || 0)))}</span>{canPause && actions.length > 0 && <Kebab actions={actions} />}</div></td>
                      </tr>
                      {gWhy && gWhyId && whyOpen[gWhyId] && <tr className="whyrow"><td colSpan={5}><div className="why-line">{gWhy}</div></td></tr>}
                      {gNoted.length > 0 && (
                        <tr className="noterow"><td colSpan={5}>
                          {/* The same note on several channels reads once. */}
                          {gNoted.filter((i, k) => gNoted.findIndex((j) => j.note!.text === i.note!.text) === k).map((i) => (
                            <NoteLine key={i.id} note={i.note!} label={gNoted.every((j) => j.note!.text === i.note!.text) ? undefined : (i.channel || i.label || i.name)} />
                          ))}
                        </td></tr>
                      )}
                      {open && clickable && (
                        <tr className="chartrow"><td colSpan={5}><div className="chart-reveal">
                          {(() => {
                            // Per-channel alert mute: with >1 alertable channel (e.g. several drives) you
                            // can turn off the alert for just one of them; a single-channel group uses the
                            // row's "Disable alerts" action instead.
                            const al = row.items.filter((i) => i.alertable)
                            if (al.length < 2) return null
                            return (
                              <div className="chan-mute">
                                <span className="chan-mute-lbl">Alerts:</span>
                                {al.map((i) => (
                                  <button key={i.id} className={'chan-mute-btn' + (i.alerts_off ? ' off' : '')} disabled={busyItem === i.id}
                                    title={i.alerts_off ? 'Alerts off for this channel - click to enable' : 'Disable alerts for this channel'}
                                    onClick={(e) => { e.stopPropagation(); muteItems([i.id], !i.alerts_off) }}>
                                    {i.alerts_off ? kbIcon.mute : kbIcon.unmute}<span>{i.channel || i.label || i.name}</span>
                                  </button>
                                ))}
                              </div>
                            )
                          })()}
                          {(() => { const ud = row.items.find((i) => i.updown); return ud ? <AvailabilityPanel itemId={ud.id} /> : null })()}
                          <SensorGroupChart channels={channels} bars={barGroup}
                            runs={row.cat === 'Internet' ? { hostId, list: row.instance === 'Speed', lastRun: Math.max(0, ...row.items.map((i) => i.last_clock || 0)) } : undefined} />
                        </div></td></tr>
                      )}
                    </Fragment>
                  )
                }
                const it = row.item
                const st = itemState[it.id]
                const open = openItem === it.id
                const clickable = it.numeric && it.supported
                const label = it.label || it.name
                // A sensor inherits its host's paused/hidden state; its own toggle is locked while
                // the host controls it.
                const effPaused = it.paused || hostPaused
                const effHidden = it.hidden || hostHidden
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
                if (it.alertable) acts.push(it.alerts_off
                  ? { label: 'Enable alerts', icon: kbIcon.unmute, onClick: () => muteItems([it.id], false) }
                  : { label: 'Disable alerts', icon: kbIcon.mute, onClick: () => muteItems([it.id], true) })
                if (!effPaused) acts.push({ label: 'Check now', icon: kbIcon.discover, onClick: () => checkNow([it.id], label) })
                const actions: KAction[] = []
                if (unacked.length) actions.push({ label: 'Acknowledge', icon: kbIcon.ack, onPick: (s) => unacked.forEach((p) => ack(p, s)) })
                if (it.note || problems.some((p) => p.item_ids.includes(it.id))) actions.push(...noteActions(notes, [it.id], label, it.note))
                if (actions.length && acts.length) actions.push({ sep: true, label: '' })
                actions.push(...acts)
                const trendColor = st ? healthColor(st, itemAcked[it.id]) : 'var(--accent)'
                // A daily-ratio sensor (block rate) charts as daily bars and minis like the
                // counter group above it - one bar per day, the day's closing rate.
                const barRate = BAR_RATE_KEYS.has(it.key.replace(/\[.*$/, ''))
                return (
                  <Fragment key={it.id}>
                    {catRow}
                    <tr className={rowClass} onClick={clickable ? () => { const next = open ? null : it.id; setOpenItem(next); onNavigate(hostId, next) } : undefined} style={{ opacity: it.supported ? 1 : 0.55, cursor: clickable ? 'pointer' : 'default' }}>
                      <td className="namecell">
                        <span className={'sname' + (clickable ? ' sclick' : '')} style={{ display: 'flex', alignItems: 'center', gap: 6, opacity: effPaused || effHidden ? 0.6 : 1 }}>
                          {clickable && <span className="scaret" style={{ color: 'var(--accent)', display: 'inline-block', transition: 'transform 0.15s', transform: open ? 'rotate(90deg)' : 'none' }}>›</span>}
                          <span className="sn-label">{onDrillSensor ? <span className="lnk-sensor" onClick={(e) => { e.stopPropagation(); onDrillSensor(it.id, label) }}>{label}</span> : label}</span>
                          {effPaused && <span style={{ color: PAUSED_BLUE, fontSize: 11 }}> (paused · {hostPaused && !it.paused ? 'host' : untilLabel(it.paused_until)})</span>}
                          {effHidden && <span style={{ color: HIDDEN_GREY, fontSize: 11 }}> (hidden · {hostHidden && !it.hidden ? 'host' : untilLabel(it.hidden_until)})</span>}
                          {it.alerts_off && <span style={{ color: 'var(--faint)', fontSize: 11 }}> (alerts off)</span>}
                        </span>
                      </td>
                      <td className="mono val">
                        {it.supported
                          ? (() => {
                            // A daily-ratio row reads TODAY's local-day rate (the /api/daily bucket,
                            // derived from the same counters as the row above) - the raw reading is
                            // AdGuard's own UTC-day rate, which straddles local midnight.
                            const dv0 = barRate && dailies[it.id]?.length ? String(dailies[it.id][dailies[it.id].length - 1]) : it.last_value
                            const [dv, du] = readingParts(dv0, it.units); return <WhyText why={it.why} onToggle={() => toggleWhy(it.id)}>{dv}{du ? <span className="unit"> {du}</span> : null}</WhyText>
                          })()
                          : <WhyText why={it.why} color="var(--err)" onToggle={() => toggleWhy(it.id)}>not supported</WhyText>}
                      </td>
                      <td className="strend">{it.numeric && it.supported ? (barRate ? <BarSpark total={dailies[it.id]} width={168} /> : <Spark values={sparks[it.id]} color={trendColor} width={168} units={it.units} thr={it.thr} />) : null}</td>
                      <td className="prio-cell" data-label="Priority"><PriorityStars value={it.priority} canEdit={canPause} onSet={(p) => setItemPriority(it, p)} /></td>
                      <td>
                        <div className="lccell">
                          <span className="when">{relTime(it.last_clock)}</span>
                          {canPause && actions.length > 0 && <Kebab disabled={busyItem === it.id} actions={actions} />}
                        </div>
                      </td>
                    </tr>
                    {it.why && whyOpen[it.id] && <tr className="whyrow"><td colSpan={5}><div className="why-line">{it.why}</div></td></tr>}
                    {it.note && <tr className="noterow"><td colSpan={5}><NoteLine note={it.note} /></td></tr>}
                    {open && clickable && (
                      <tr className="chartrow"><td colSpan={5}><div className="chart-reveal">{it.updown ? <AvailabilityPanel itemId={it.id} /> : <SensorChart itemId={it.id} units={it.units} color={trendColor} bars={barRate} label={label} thr={it.thr} runs={it.key.startsWith('speedtest.')} />}</div></td></tr>
                    )}
                  </Fragment>
                )
              })}
            </tbody>
          </table>
        )}
      {onlyItem && <HostIncidents hostId={hostId} goHost={null} itemIds={shownItems.map((i) => i.id)} />}
    </div>
  )
}

// StatusListView is the flat cross-site list opened from a top-bar status chip: every sensor in
// the chosen state, with deep-links to its host/chart and a per-row kebab.
function StatusListView({ filter, sensors, loading, canPause, goHost, goSensor, onBack }: { filter: string; sensors: SensorRow[]; loading?: boolean; canPause: boolean; goHost: (h: string) => void; goSensor: (h: string, i: string, name?: string) => void; onBack: () => void }) {
  const [busy, setBusy] = useState<string | null>(null)
  const notes = useNoteEditor()
  const toast = useToast()
  const prompt = usePrompt()
  // The selection for bulk actions (sensor keys), kept to the rows still listed.
  const [sel, setSel] = useState<Set<string>>(() => new Set())
  const [bulkBusy, setBulkBusy] = useState(false)
  const [whyOpen, toggleWhy] = useWhyOpen()
  // The "attention" filter is the home Overview: every sensor that isn't OK (a PRTG-style unified list),
  // with a mode toggle. A concrete state (error/warning/…) is a top-bar status-chip drill-down.
  const attention = filter === 'attention'
  const [attMode, setAttMode] = useState<'errors' | 'both'>('errors')
  const [showHeld, setShowHeld] = useState(false)
  const matched = sensors.filter((s) => attention
    ? (attMode === 'errors' ? s.state === 'error' : (s.state === 'error' || s.state === 'warning' || s.state === 'acked'))
    : s.state === filter)
  // A sensor whose master is down (its ping, its collector, its site's probe) is part of that one
  // incident: the list shows the master, how many it holds, and lists the rest after it on "show".
  const held = matched.filter((s) => s.held_by)
  const main = matched.filter((s) => !s.held_by)
  // Priority leads the ordering (except the OK list, where it'd just shuffle healthy sensors); severity
  // and host/name break ties. The backend already returns them host/name-sorted as a final fallback.
  if (attention || filter !== 'ok') main.sort((a, b) => (b.priority - a.priority) || (b.severity - a.severity) || a.host_name.localeCompare(b.host_name) || a.name.localeCompare(b.name))
  const heldUnder: Record<string, SensorRow[]> = {}
  for (const s of held) (heldUnder[s.held_by!.item_id] = heldUnder[s.held_by!.item_id] || []).push(s)
  const rows: SensorRow[] = []
  for (const s of main) {
    rows.push(s)
    if (showHeld) rows.push(...(heldUnder[s.item_id] || []))
  }
  if (showHeld) { // held by a master this list doesn't show (acknowledged, or at the other severity)
    const listed = new Set(main.map((s) => s.item_id))
    for (const s of held) if (!listed.has(s.held_by!.item_id)) rows.push(s)
  }
  const heldToggle = held.length > 0 && (
    <button className="linkbtn held-toggle" onClick={() => setShowHeld(!showHeld)}>{showHeld ? 'Hide' : 'Show'} {held.length} held</button>
  )
  const sparks = useSparks(rows.filter((s) => s.numeric && s.supported).map((s) => s.item_id))
  const listed = new Set(rows.map((s) => s.item_id))
  const picked = rows.filter((s) => sel.has(s.item_id))
  function toggleSel(keys: string[], on: boolean) { setSel((cur) => { const n = new Set(cur); for (const k of keys) { if (on) n.add(k); else n.delete(k) } return n }) }
  async function runBulk(action: string, extra: object, what: string) {
    setBulkBusy(true)
    const keys = [...sel].filter((k) => listed.has(k))
    const r = await postBulk('/api/bulk/sensors', { action, keys, ...extra }, '')
    setBulkBusy(false)
    bulkToast(toast, r, what, ['sensor', 'sensors'])
    if (typeof r !== 'string') { const failed = new Set(r.failed.map((f) => f.id)); setSel(new Set(keys.filter((k) => failed.has(k)))) }
    fireDataRefresh()
  }
  async function bulkNote() {
    const text = await prompt({ title: `Note on ${picked.length} sensor${picked.length === 1 ? '' : 's'}`, message: 'Shown with each sensor and sent with its alerts until it is OK again.', placeholder: 'e.g. ISP ticket 4471 open, technician on site at 14:00', confirmLabel: 'Add note' })
    if (text && text.trim()) runBulk('note', { note: text.trim() }, 'Left a note on')
  }

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
    if (s.state === 'acked' && s.event_ids.length) acts.push({ label: 'Unacknowledge', icon: kbIcon.ack, onClick: () => unackEvents(s) })
    else if ((s.state === 'error' || s.state === 'warning') && s.event_ids.length) acts.push({ label: 'Acknowledge', icon: kbIcon.ack, onPick: (sec) => ackEvents(s, sec) })
    if (s.event_ids.length) acts.push(...noteActions(notes, [s.item_id], `${s.label || s.name} on ${s.host_name}`, s.note))
    if (acts.length) acts.push({ sep: true, label: '' })
    // An Argus-raised row (an unreachable agent) isn't a Zabbix sensor: it can be acknowledged, not paused or hidden.
    if (s.synthetic) return acts.filter((a) => !a.sep)
    acts.push({ label: 'Pause', icon: kbIcon.pause, onPick: (sec) => itemAction(s, 'pause', sec) }, { label: 'Hide', icon: kbIcon.hide, onPick: (sec) => itemAction(s, 'hide', sec) })
    return acts
  }
  const durCol = filter === 'paused' ? 'Paused' : filter === 'hidden' ? 'Hidden' : 'Last check'
  return (
    <div className="panel">
      <div className="phead">
        <PanelTitle eyebrow={watchEyebrow()}>{attention ? 'Active problems' : `${STATE_LABEL[filter]} sensors`}</PanelTitle>
        <span className="hint">{main.length} sensor{main.length === 1 ? '' : 's'}{heldToggle && <> · {heldToggle}</>}</span>
        <div className="tools">
          {attention
            ? <div className="seg">
                <button className={attMode === 'errors' ? 'on' : ''} onClick={() => setAttMode('errors')}>Errors</button>
                <button className={attMode === 'both' ? 'on' : ''} onClick={() => setAttMode('both')}>Errors + Warnings</button>
              </div>
            : <button className="btn ghost" onClick={onBack}>← Back to overview</button>}
          <button className="btn" disabled={!rows.length} onClick={() => exportSensors(rows, attention ? 'Active problems' : `${STATE_LABEL[filter]} sensors`)}>Export CSV</button>
        </div>
      </div>
      {loading
        ? <Skeleton rows={4} cols={5} />
        : rows.length === 0 && held.length > 0
        ? <EmptyState icon={STATE_ICON.paused} title="Only held sensors" text={`${held.length} sensor${held.length === 1 ? ' waits' : 's wait'} on a master sensor that is down and not in this list (acknowledged, or at another severity).`} action={heldToggle} />
        : rows.length === 0
        ? (attention
          ? <EmptyState tone="ok" icon={ic.ok} title="All clear" text={attMode === 'errors' ? 'No sensor is in error right now.' : 'No errors or warnings right now.'} />
          : <EmptyState icon={STATE_ICON[filter]} title={`No ${STATE_LABEL[filter].toLowerCase()} sensors`} text="Nothing on any site is in this state at the moment." />)
        : (
          <table className="slist slist-sensors">
            <thead><tr><th><span className="sel-cell"><input type="checkbox" className="tsel" checked={rows.length > 0 && rows.every((s) => sel.has(s.item_id))} onChange={(e) => toggleSel(rows.map((s) => s.item_id), e.target.checked)} aria-label="Select every sensor listed" />Host</span></th><th className="slgrow">Sensor</th><th>Value</th><th>Trend</th><th className="slprio">Priority</th><th>{durCol}</th><th /></tr></thead>
            <tbody>
              {rows.map((s) => {
                const clickable = s.numeric && s.supported
                const holds = (heldUnder[s.item_id] || []).length
                const h = s.held_by
                return (
                  <tr key={s.item_id} className={(h ? 'held-row' : '') + (sel.has(s.item_id) ? ' selected' : '') || undefined} style={{ opacity: s.state === 'acked' ? 0.72 : 1 }}>
                    <td className="slhost" style={{ borderLeftColor: STATE_VAR[s.state] || 'var(--border)' }}><span className="sel-cell"><input type="checkbox" className="tsel" checked={sel.has(s.item_id)} onChange={(e) => toggleSel([s.item_id], e.target.checked)} aria-label={`Select ${s.label || s.name} on ${s.host_name}`} /><span className="lnk-host" onClick={() => goHost(s.host_id)}>{s.host_name}</span></span></td>
                    <td className="slgrow">
                      <span className="sl-name">{clickable ? <span className="lnk-sensor" onClick={() => goSensor(s.host_id, s.item_id, s.label || s.name)}>{s.label || s.name}</span> : (s.label || s.name)}</span>
                      {s.reason && <div className="sreason"><span style={{ color: sevInfo(s.severity).color, fontWeight: 600 }}>{sevInfo(s.severity).label}</span> · {s.reason}{s.since ? <span title={`Firing since ${new Date(s.since * 1000).toLocaleString()}`}> · {relTime(s.since)}</span> : null}</div>}
                      {s.why && whyOpen[s.host_id + ':' + s.item_id] && <div className="sreason why-line">{s.why}</div>}
                      {s.maintenance && <div className="sreason maint-tag" title="Alerts wait until the window ends">In maintenance ({s.maintenance.name}) until {fmtWhen(s.maintenance.until)}</div>}
                      {holds > 0 && <div className="sreason held-note">Holding {holds} other sensor{holds === 1 ? '' : 's'} while it is down · <button className="linkbtn" onClick={() => setShowHeld(!showHeld)}>{showHeld ? 'hide' : 'show'}</button></div>}
                      {h && (h.via === 'upstream'
                        ? <div className="sreason held-tag" title="Its alerts wait until the device it is plugged into is back">Held: behind {h.host_name}, which is down</div>
                        : <div className="sreason held-tag" title="Its alerts wait until the master sensor is back">Held: {h.name}{h.host_id !== s.host_id ? ` on ${h.host_name}` : ''} is down</div>)}
                      {s.call && <div className="sreason call-line" title="Who to call, from the site's info">{PHONE_IC}{s.call}</div>}
                      {s.note && <NoteLine note={s.note} />}
                    </td>
                    <td className="mono val" data-label="Value">{s.supported ? (() => { const [dv, du] = readingParts(s.value, s.units); return <WhyText why={s.why} onToggle={() => toggleWhy(s.host_id + ':' + s.item_id)}>{dv}{du ? <span className="unit"> {du}</span> : null}</WhyText> })() : <WhyText why={s.why} color="var(--err)" onToggle={() => toggleWhy(s.host_id + ':' + s.item_id)}>not supported</WhyText>}</td>
                    <td className="trend">{clickable ? <Spark values={sparks[s.item_id]} color={s.state === 'ok' ? 'var(--accent)' : (STATE_VAR[s.state] || 'var(--accent)')} width={168} fill units={s.units} /> : null}</td>
                    <td className="slprio" data-label="Priority"><PriorityStars value={s.priority} canEdit={false} /></td>
                    <td className="mono dur" data-label={durCol}>{relTime(s.last_clock)}</td>
                    <td className="act">{canPause && actionsFor(s).length > 0 && <Kebab disabled={busy === s.item_id} actions={actionsFor(s)} />}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      {picked.length > 0 && (
        <BulkBar count={picked.length} noun={['sensor', 'sensors']} onClear={() => setSel(new Set())}>
          {picked.some((s) => s.event_ids.length && (s.state === 'error' || s.state === 'warning')) && <DurationButton up label="Acknowledge" disabled={bulkBusy} onPick={(sec) => runBulk('ack', { duration_seconds: sec ?? 0 }, 'Acknowledged')} />}
          {canPause && picked.some((s) => s.event_ids.length) && <Button variant="ghost" className="compact" disabled={bulkBusy} onClick={bulkNote}>Add note…</Button>}
          {canPause && picked.some((s) => !s.synthetic) && <DurationButton up label="Pause" disabled={bulkBusy} onPick={(sec) => runBulk('pause', { duration_seconds: sec ?? 0 }, 'Paused')} />}
          {canPause && picked.some((s) => !s.synthetic) && <DurationButton up label="Hide" disabled={bulkBusy} onPick={(sec) => runBulk('hide', { duration_seconds: sec ?? 0 }, 'Hid')} />}
        </BulkBar>
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
// HistoryView is the fleet-wide incident feed: what went wrong and when, newest first, with how long
// it lasted, who took it, and the reason when the device's collector gave one.
function HistoryView({ goHost }: { goHost: (h: string) => void }) {
  const [days, setDays] = useState(7)
  const [level, setLevel] = useState<'all' | 'errors'>('all')
  const [q, setQ] = useState('')
  const [withHidden, setWithHidden] = useState(false)
  const [hf, setHf] = useState<HostFilterVal>(NO_HOST_FILTER)
  const [rows, err, hidden] = useIncidents(`/api/incidents?days=${days}${withHidden ? '&hidden=1' : ''}${hostFilterQS(hf)}`)
  const needle = q.trim().toLowerCase()
  const shown = (rows || []).filter((r) => (level === 'all' || r.severity >= 3) &&
    (!needle || [r.host_name, r.site, r.sensor, r.name, r.reason].some((v) => (v || '').toLowerCase().includes(needle))))
  const live = shown.filter((r) => !r.end).length
  const period = days === 1 ? 'the last 24 hours' : `the last ${days} days`
  return (
    <div className="panel">
      <div className="phead">
        <PanelTitle eyebrow={watchEyebrow()}>Incidents</PanelTitle>
        <span className="hint">{rows ? <>{`${shown.length} in ${period}${live ? ` · ${live} still open` : ''}`}<HiddenToggle n={hidden} shown={withHidden} onToggle={() => setWithHidden((w) => !w)} /></> : ''}</span>
        <div className="tools hist-tools">
          <input className="input hist-q" placeholder="Host, sensor or reason" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Filter incidents" />
          <ProbeGroupFilter value={hf} onChange={setHf} />
          <div className="seg">
            {[1, 7, 30, 90].map((d) => <button key={d} className={days === d ? 'on' : ''} onClick={() => setDays(d)}>{d === 1 ? '24h' : `${d}d`}</button>)}
          </div>
          <div className="seg">
            <button className={level === 'all' ? 'on' : ''} onClick={() => setLevel('all')}>Errors + Warnings</button>
            <button className={level === 'errors' ? 'on' : ''} onClick={() => setLevel('errors')}>Errors</button>
          </div>
          <button className="btn" disabled={!shown.length} onClick={() => exportIncidents(shown)}>Export CSV</button>
        </div>
      </div>
      {err && <div style={{ padding: '0.9rem 16px', color: 'var(--err)' }}>{err}</div>}
      {rows === null && !err && <Skeleton rows={5} cols={5} />}
      {rows !== null && !err && (shown.length === 0
        ? <EmptyState tone="ok" icon={ic.ok} title="Nothing went wrong" text={needle ? 'No incident in this period matches the filter.' : `No incidents in ${period}.`} />
        : <IncidentRows rows={shown} goHost={goHost} />)}
    </div>
  )
}

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
        <PanelTitle eyebrow={watchEyebrow()}>Triggers</PanelTitle>
        <span className="hint">{mode === 'firing' ? `${firing.length} firing` : `${(rows || []).length} trigger${(rows || []).length === 1 ? '' : 's'} · ${hostIds.length} host${hostIds.length === 1 ? '' : 's'}`}</span>
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

type ChartColors = { line: string; fill: string; soft: string; axis: string; grid: string; warn: string; err: string }

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
  return { line, fill: withAlpha(line, 0.12), soft: withAlpha(line, 0.4), axis: tok('var(--faint)') || '#8a8a8a', grid: tok('var(--border)') || 'rgba(128,128,128,0.25)', warn: tok('var(--warn)') || '#e0a53a', err: tok('var(--err)') || '#e2564d' }
}

// thrOn reports whether a sensor has at least one numeric threshold to band its chart with.
const thrOn = (t?: Thr): t is Thr => !!t && (t.warn != null || t.high != null)

// thrPaint colours a series BY VALUE instead of by the sensor's current state: the normal line colour
// inside the normal range, the warning colour past the warning value, the error colour past high
// (mirrored for below-is-worse sensors). It is a vertical canvas gradient with hard stops at each
// threshold's pixel height, which uPlot rebuilds whenever the scale changes (zoom, resize, refresh) -
// so only the stretch of line beyond a threshold changes colour, and a past excursion stays marked
// after the alert has cleared. alpha < 1 gives the matching translucent fill / band.
function thrPaint(scaleKey: string, thr: Thr, c: ChartColors, alpha: number) {
  const tint = (col: string) => (alpha < 1 ? withAlpha(col, alpha) : col)
  const past = (v: number, t?: number) => t != null && (thr.below ? v <= t : v >= t)
  const colorAt = (v: number) => (past(v, thr.high) ? c.err : past(v, thr.warn) ? c.warn : c.line)
  // Distinct threshold values, highest first = top of the plot first (canvas y grows downward).
  const ts = [...new Set([thr.warn, thr.high].filter((x): x is number => x != null))].sort((a, b) => b - a)
  // One representative value per region: above the top line, between each pair, below the bottom.
  const reps = [ts[0] + 1, ...ts.slice(1).map((t, i) => (ts[i] + t) / 2), ts[ts.length - 1] - 1]
  return (u: uPlot) => {
    const bb = u.bbox
    // Before the first layout (legend markers, init) there is no plot box or scale yet.
    if (!bb || !bb.height) return tint(c.line)
    const offs = ts.map((t) => (u.valToPos(t, scaleKey, true) - bb.top) / bb.height)
    if (offs.some((o) => !Number.isFinite(o))) return tint(c.line)
    const g = u.ctx.createLinearGradient(0, bb.top, 0, bb.top + bb.height)
    let prev = 0
    reps.forEach((rv, k) => {
      const end = k < offs.length ? Math.min(1, Math.max(prev, offs[k])) : 1
      const col = tint(colorAt(rv))
      g.addColorStop(prev, col)
      g.addColorStop(end, col)
      prev = end
    })
    return g
  }
}

// A dashed reference line at a threshold value on one scale; series lists the chart series it belongs
// to (drawn while any of them is shown, so hiding a channel in the legend also hides its lines). text
// is the value ("85 °C") shown on the line's axis tag; ink is the tag's text colour. owner names the
// channel - used only by the in-plot fallback label, since a tag's axis side already says which
// channel's scale it is on.
// sev: 2 an error (high) threshold, 1 a warning - the error's tag wins where two would overlap.
type ThrLine = { scale: string; value: number; color: string; ink: string; series: number[]; text: string; owner?: string; sev: number }

const thrVisible = (u: uPlot, lines: ThrLine[]) => lines.filter((l) => l.series.some((i) => u.series[i]?.show))

// thrAxisOf finds the y axis that shows a scale, or null when the scale has no axis (a third unit on a
// two-axis chart). anchor is where uPlot anchors that axis's tick labels, in canvas px - the plot-side
// gutter edge shifted outward by tick length + gap (uPlot drawAxesGrid: basePos + (tickSize +
// axisGap) * shiftDir), with labels LEFT-aligned there on a right axis and RIGHT-aligned on a left one.
// A tag anchors its text on the same point, in the same font, so it lines up with the axis numbers.
function thrAxisOf(u: uPlot, scale: string): { side: number; anchor: number; font: string } | null {
  const dpr = window.devicePixelRatio || 1
  for (let i = 1; i < u.axes.length; i++) {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const a = u.axes[i] as any
    if (a.scale !== scale || a.show === false || !(a._size > 0)) continue
    const tick = a.ticks && a.ticks.show !== false ? Math.round((a.ticks.size ?? 10) * dpr) : 0
    const shift = (tick + Math.round((a.gap ?? 5) * dpr)) * (a.side === 3 ? -1 : 1)
    return { side: a.side, anchor: Math.round(a._pos * dpr) + shift, font: (a.font && a.font[0]) || thrFont(u) }
  }
  return null
}

// The tag / fallback-label font: the axis tick font, a notch smaller.
function thrFont(u: uPlot): string {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const f: string = ((u.axes[1] as any)?.font?.[0] as string) || `${11 * (window.devicePixelRatio || 1)}px system-ui, sans-serif`
  return f.replace(/(\d+(?:\.\d+)?)px/, (_m, n) => `${Math.round(Number(n) * 0.9)}px`)
}

// thrLinesHook draws the threshold reference lines UNDER the data (drawAxes runs after the grid, before
// the series). A line outside the current y range is simply not drawn - the scale is never stretched
// to fit a threshold, so a sensor far from its limits keeps a readable chart.
function thrLinesHook(lines: ThrLine[]) {
  return (u: uPlot) => {
    const { ctx, bbox } = u
    const dpr = window.devicePixelRatio || 1
    ctx.save()
    ctx.lineWidth = dpr
    ctx.setLineDash([4 * dpr, 4 * dpr])
    for (const l of thrVisible(u, lines)) {
      const y = Math.round(u.valToPos(l.value, l.scale, true)) + 0.5
      if (!Number.isFinite(y) || y < bbox.top || y > bbox.top + bbox.height) continue
      ctx.strokeStyle = withAlpha(l.color, 0.75)
      ctx.beginPath()
      ctx.moveTo(bbox.left, y)
      ctx.lineTo(bbox.left + bbox.width, y)
      ctx.stroke()
    }
    ctx.restore()
  }
}

// thrTagged is the threshold tags drawn on one scale. Each sits exactly at its line's value; one that
// would overlap a tag already placed isn't moved off its value but left out (its dashed line still
// shows): the first channel's tags go before the others', an error before a warning.
function thrTagged(u: uPlot, lines: ThrLine[], scale: string): { l: ThrLine; y: number }[] {
  const dpr = window.devicePixelRatio || 1
  const room = 18 * dpr // a tag's height and a little gap
  const { top, height } = u.bbox
  const cand = thrVisible(u, lines).filter((l) => l.scale === scale)
    .map((l) => ({ l, y: u.valToPos(l.value, scale, true) }))
    .filter((c) => Number.isFinite(c.y) && c.y >= top && c.y <= top + height)
    .sort((a, b) => (Math.min(...a.l.series) - Math.min(...b.l.series)) || (b.l.sev - a.l.sev))
  const placed: { l: ThrLine; y: number }[] = []
  for (const c of cand) if (placed.every((p) => Math.abs(p.y - c.y) >= room)) placed.push(c)
  return placed
}

// thrTagsHook labels each line with a filled tag in its colour ON ITS AXIS, at the line's height (the
// trading-chart pattern): outside the plot, so it never covers the data, and on the side of the scale
// it belongs to, so a two-axis chart reads unambiguously. A line whose scale has no axis falls back to a
// small label inside the plot's right edge. Which tags are drawn: thrTagged.
// Runs in the draw hook (after the axes and series) so the tag sits on top of the tick marks.
function thrTagsHook(lines: ThrLine[]) {
  return (u: uPlot) => {
    const { ctx, bbox } = u
    const dpr = window.devicePixelRatio || 1
    const cw = ctx.canvas.width, ch = ctx.canvas.height
    const h = 16 * dpr, padX = 5 * dpr
    ctx.save()
    for (const sc of [...new Set(lines.map((l) => l.scale))]) {
      const ax = thrAxisOf(u, sc)
      const tags = thrTagged(u, lines, sc)
      if (ax) {
        ctx.textBaseline = 'middle'
        for (const { l, y } of tags) {
          const cy = Math.min(Math.max(y, h / 2), ch - h / 2) // only kept inside the canvas
          // The tag's TEXT sits exactly where the axis numbers do (same anchor, alignment and font); the
          // coloured box pads around it, reaching over the tick marks on the plot side.
          ctx.font = ax.font
          const tw = ctx.measureText(l.text).width
          const right = ax.side === 1
          let x = right ? ax.anchor - padX : ax.anchor - tw - padX
          const w = tw + 2 * padX
          x = Math.max(0, Math.min(x, cw - w))
          ctx.fillStyle = l.color
          ctx.beginPath()
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          const c2 = ctx as any
          if (typeof c2.roundRect === 'function') c2.roundRect(x, cy - h / 2, w, h, 3 * dpr)
          else ctx.rect(x, cy - h / 2, w, h)
          ctx.fill()
          ctx.fillStyle = l.ink
          ctx.textAlign = 'left'
          ctx.fillText(l.text, x + padX, cy + 0.5 * dpr)
        }
      } else {
        // No axis for this scale: a label just above the line inside the plot.
        ctx.font = thrFont(u)
        ctx.textAlign = 'right'
        ctx.textBaseline = 'bottom'
        const lh = 13 * dpr
        for (const { l, y } of tags) {
          ctx.fillStyle = withAlpha(l.color, 0.95)
          ctx.fillText(l.owner ? `${l.owner} ${l.text}` : l.text, bbox.left + bbox.width - 6 * dpr, Math.max(y - 3 * dpr, bbox.top + lh))
        }
      }
    }
    ctx.restore()
  }
}

// addThrLines wires threshold lines into a chart: the dashed lines + axis tags (merged into any
// existing zoom/toggle hooks), and two wraps on every y axis that carries tags - its tick labels skip a
// value that would sit under a tag (a "504 ms" tick beside a "500 ms" tag), and its gutter grows when
// a tag is wider than the widest tick label, so a tag never clips at the canvas edge.
function addThrLines(opts: uPlot.Options, lines: ThrLine[]) {
  if (!lines.length) return
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const hooks: any = (opts.hooks = opts.hooks || {})
  hooks.drawAxes = [...(hooks.drawAxes || []), thrLinesHook(lines)]
  hooks.draw = [...(hooks.draw || []), thrTagsHook(lines)]
  // Showing/hiding a channel in the legend adds/removes its tags, so the axis must re-run its tick
  // labels (the ones a tag covered come back when the tag goes). uPlot only recomputes axes when a
  // scale changes, and a pinned scale (ICMP's 0-100 % Loss axis) never does - force it. Deferred out
  // of the hook; `show` is only present on a real toggle (cursor focus passes opts without it).
  hooks.setSeries = [...(hooks.setSeries || []), (u: uPlot, _i: number | null, o: { show?: boolean } | undefined) => {
    if (o && o.show !== undefined) requestAnimationFrame(() => u.redraw(false, true))
  }]
  const dpr = window.devicePixelRatio || 1
  ;(opts.axes || []).forEach((ax, i) => {
    if (i === 0) return
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const a = ax as any
    const ov = a.values
    if (typeof ov === 'function') {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      a.values = (u: uPlot, splits: number[], ai: number, space: number, incr: number): any => {
        const out = ov(u, splits, ai, space, incr)
        const sc = u.axes[ai].scale as string
        const dpr = window.devicePixelRatio || 1
        const tagYs = thrTagged(u, lines, sc).map((t) => t.y / dpr)
        if (!tagYs.length || !Array.isArray(out)) return out
        // A tick within a tag's height (16px) of a drawn tag would peek out from under it - drop its label.
        return out.map((v: unknown, k: number) => (tagYs.some((ty) => Math.abs(u.valToPos(splits[k], sc) + u.bbox.top / dpr - ty) < 16) ? '' : v))
      }
    }
    // A tagged axis gets denser ticks (uPlot's default min spacing is 30px): the numbers a tag covers
    // are dropped, and on a short axis that could leave only two - this keeps a readable scale.
    if (a.space == null) a.space = 18
    const os = a.size
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    a.size = (u: uPlot, values: any, ai: number, cycle: number): number => {
      const base: number = typeof os === 'function' ? os(u, values, ai, cycle) : typeof os === 'number' ? os : 50
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const ax = u.axes[ai] as any
      const sc = ax.scale as string
      // All of the scale's lines (not only visible ones), so toggling a channel doesn't resize the gutter.
      const mine = lines.filter((l) => l.scale === sc)
      if (!mine.length || !ax.font) return base
      u.ctx.font = ax.font[0]
      const widest = Math.max(...mine.map((l) => u.ctx.measureText(l.text).width)) / dpr
      // Tag text starts where the numbers do (tick + gap out), plus its outer padding and a little slack.
      const tick = ax.ticks && ax.ticks.show !== false ? ax.ticks.size ?? 10 : 0
      return Math.max(base, Math.ceil(tick + (ax.gap ?? 5) + widest + 5 + 4))
    }
  })
}

// thrLines turns one sensor's thresholds into its reference lines (units formats the value label).
function thrLines(thr: Thr, scale: string, series: number[], c: ChartColors, units: string, owner?: string): ThrLine[] {
  const out: ThrLine[] = []
  // Tag ink: dark on the amber warning tag, white on the red error tag (legible in both themes).
  if (thr.warn != null) out.push({ scale, value: thr.warn, color: c.warn, ink: '#1b1405', series, text: fmtNum(thr.warn, units), owner, sev: 1 })
  if (thr.high != null) out.push({ scale, value: thr.high, color: c.err, ink: '#ffffff', series, text: fmtNum(thr.high, units), owner, sev: 2 })
  return out
}

// insertGaps breaks the line where sampling stopped (e.g. a paused sensor): where the time
// between two consecutive points exceeds ~1.75x the typical interval, it inserts a null so
// uPlot draws a gap instead of a straight line across the missing period.
// dropIsolated nulls out any single-point segment (a value with a gap on both sides). uPlot draws
// such lone points as dots even with points.show:false (a line needs two points), which showed up as
// a stray dot at the chart's edge next to the window-boundary/gap nulls. Lines-only stays lines-only.
function dropIsolated(series: (number | null)[][]): (number | null)[][] {
  return series.map((a) => a.map((v, i) => (v != null && (i === 0 || a[i - 1] == null) && (i === a.length - 1 || a[i + 1] == null) ? null : v)))
}

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

// Auto-size a value axis to its longest rendered label ("364d 23h 59m", "953.67 MB", ...) so text
// never clips regardless of unit - the uPlot autosize recipe: measure the formatted tick strings in
// the axis font. The stock recipe bails out with the cycle-1 size on later convergence cycles, but
// resizing the gutter changes the plot width, which can make uPlot pick FINER splits ("32.5 °C"
// where cycle 1 only saw "30 °C") that are wider than what was measured - the recurring clipped-C.
// So later cycles re-measure and only ever GROW the gutter: monotone growth still converges (it's
// bounded by the widest possible label) and can never oscillate.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function axisAutoSize(u: any, values: string[] | null, axisIdx: number, cycleNum: number): number {
  const ax = u.axes[axisIdx]
  let size = (typeof ax.ticks?.size === 'number' ? ax.ticks.size : 10) + (typeof ax.gap === 'number' ? ax.gap : 5) + 4
  const longest = (values ?? []).reduce((a, b) => (b != null && String(b).length > a.length ? String(b) : a), '')
  // +6 slack: on fractional display scaling (Windows 125/150 %) the canvas-measured width can come
  // up a few px short of what actually paints, clipping the label's outer edge at the canvas
  // boundary (visible on wide labels like "8000" on a right-side axis).
  if (longest !== '') { u.ctx.font = ax.font[0]; size += u.ctx.measureText(longest).width / (window.devicePixelRatio || 1) + 6 }
  size = Math.ceil(size)
  return cycleNum > 1 ? Math.max(ax._size, size) : size
}
const axisSize = axisAutoSize as unknown as uPlot.Axis['size']

// A percentage axis must never zoom so tight that a near-constant value renders as a dramatic ramp
// with every gridline rounding to the same label (a disk sitting at 57.3 % slowly filling). Enforce
// a minimum visible span, centered on the data and clamped to [0,100]; a series that genuinely
// varies more than the floor keeps uPlot's normal 10%-padded auto-range. % is bounded and often
// near-constant, so it gets this treatment where other units keep tight auto-range.
const PCT_MIN_SPAN = 10
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function pctRange(_u: any, dataMin: number | null, dataMax: number | null): [number, number] {
  if (dataMin == null || dataMax == null) return [0, 100]
  let lo: number, hi: number
  const span = dataMax - dataMin
  if (span >= PCT_MIN_SPAN) { const pad = span * 0.1; lo = dataMin - pad; hi = dataMax + pad }
  else { const mid = (dataMin + dataMax) / 2; lo = mid - PCT_MIN_SPAN / 2; hi = mid + PCT_MIN_SPAN / 2 }
  if (lo < 0) { hi -= lo; lo = 0 }
  if (hi > 100) { lo -= hi - 100; hi = 100 }
  return [Math.max(0, lo), Math.min(100, hi)]
}

// A certificate's days left drop a tenth of a day every couple of hours: auto-ranged, that sliver
// fills the plot and every step reads as a cliff. Days get a minimum span (DAYS_MIN_SPAN), kept at or
// above 0 unless the certificate has expired, so a short range reads flat and a long one still shows
// the countdown.
const DAYS_MIN_SPAN = 15
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function daysRange(_u: any, dataMin: number | null, dataMax: number | null): [number, number] {
  if (dataMin == null || dataMax == null) return [0, DAYS_MIN_SPAN]
  const span = dataMax - dataMin
  let lo: number, hi: number
  if (span >= DAYS_MIN_SPAN) { const pad = span * 0.1; lo = dataMin - pad; hi = dataMax + pad }
  else { const mid = (dataMin + dataMax) / 2; lo = mid - DAYS_MIN_SPAN / 2; hi = mid + DAYS_MIN_SPAN / 2 }
  if (lo < 0 && dataMin >= 0) { hi -= lo; lo = 0 }
  return [lo, hi]
}

// thr (optional) bands the line by value - see thrPaint - and adds dashed warning/high reference lines.
// runs: a sensor measured by runs (the speed test's packet loss): a dot at each run, lines only between
// runs next to each other, the axis from 0.
function buildPlot(data: Series, units: string, width: number, c: ChartColors, onZoom?: (zoomed: boolean) => void, thr?: Thr, runs?: boolean): [uPlot.Options, uPlot.AlignedData] {
  const banded = thrOn(thr)
  const lineStroke = banded ? thrPaint('y', thr, c, 1) : c.line
  const softStroke = banded ? thrPaint('y', thr, c, 0.4) : c.soft
  const areaFill = banded ? thrPaint('y', thr, c, 0.12) : c.fill
  const xs = data.points.map((p) => p.t)
  const grid = { stroke: c.grid, width: 1 }
  const ticks = { stroke: c.grid, width: 1 }
  const scaled = scaledUnit(units)
  // Unit-aware y-axis ticks for every unit ("2.2 %", "3.73 GB", "7d 4h") - uPlot's default formatter
  // drops the unit and follows the browser locale, which read as bare "2,2" on a % axis.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const yValues = (_u: any, splits: number[]) => splits.map((v) => fmtNum(v, units))
  // Single-metric charts label their axis on the RIGHT (user pref: the values sit beside "now");
  // the gutter sizes itself to the longest label via axisSize, so nothing clips.
  const yAxis: uPlot.Axis = { stroke: c.axis, grid, ticks, side: 1, size: axisSize, values: yValues as unknown as uPlot.Axis['values'] }
  const xAxis: uPlot.Axis = { stroke: c.axis, grid, ticks }
  // Legend cells: show the hovered point, or fall back to the latest value when idle (so the
  // legend is never blank). unitLabel is dropped for scaled units since the value carries it.
  const unitLabel = scaled ? '' : units ? ` (${units})` : ''
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const xVal = (u: any, v: number | null) => { const t = v ?? lastVal(u, 0); return t == null ? '--' : new Date(t * 1000).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const yVal = (sidx: number) => (u: any, v: number | null) => { const n = v ?? lastVal(u, sidx); return n == null ? '--' : fmtNum(n, units) }
  // cursor.points.show:false removes uPlot's hover marker (a small dot it parks on the line at the
  // cursor - and at the plot's top-left corner while idle). We're lines-only; the legend already shows
  // the hovered value, so the dot is pure noise. This is the real source of the long-standing "stray dot".
  const scales: uPlot.Scales = { x: { time: true } }
  if (units === '%') scales.y = { range: pctRange as unknown as uPlot.Scale['range'] }
  if (units === 'days') scales.y = { range: daysRange as unknown as uPlot.Scale['range'] }
  if (runs) scales.y = { range: zeroRange }
  const base: Partial<uPlot.Options> = { width, height: 320, scales, axes: [xAxis, yAxis], legend: { show: true }, cursor: { points: { show: false } }, ...zoomHook(onZoom, xs.length ? [xs[0], xs[xs.length - 1]] : undefined) }

  // Uptime is a monotonic counter - min ≈ avg ≈ max, so its band is meaningless; fall through to a
  // single line (drawn from avg on trend ranges).
  if (data.kind === 'trend' && units !== 'uptime' && !runs) {
    const avg = data.points.map((p) => (p.avg ?? null))
    const min = data.points.map((p) => (p.min ?? null))
    const max = data.points.map((p) => (p.max ?? null))
    const opts: uPlot.Options = {
      ...base,
      series: [
        { value: xVal },
        { label: `avg${unitLabel}`, stroke: lineStroke, width: 1.5, points: { show: false, size: 0 }, value: yVal(1) },
        { label: 'min', stroke: softStroke, width: 1, points: { show: false, size: 0 }, value: yVal(2) },
        { label: 'max', stroke: softStroke, width: 1, points: { show: false, size: 0 }, value: yVal(3) },
      ],
      bands: [{ series: [3, 2], fill: areaFill }],
    } as uPlot.Options
    if (banded) addThrLines(opts, thrLines(thr, 'y', [1, 2, 3], c, units))
    const [gx, gy] = insertGaps(xs, [avg, min, max])
    const [ga, gmin, gmax] = dropIsolated(gy)
    return [opts, [gx, ga, gmin, gmax] as uPlot.AlignedData]
  }

  const vs = data.points.map((p) => (p.v ?? p.avg ?? null))
  const opts: uPlot.Options = {
    ...base,
    series: [{ value: xVal }, { label: `value${unitLabel}`, stroke: lineStroke, width: 1.5, fill: areaFill, points: runs ? { show: true, size: 6, width: 1, fill: c.line, stroke: c.line } : { show: false, size: 0 }, value: yVal(1) }],
  } as uPlot.Options
  if (banded) addThrLines(opts, thrLines(thr, 'y', [1], c, units))
  if (runs) return [opts, [xs, vs] as uPlot.AlignedData] // every run is a dot, a lone one too
  const [gx, gy] = insertGaps(xs, [vs])
  const [gv] = dropIsolated(gy)
  return [opts, [gx, gv] as uPlot.AlignedData]
}

// bars switches to the daily bar mode for a single daily-ratio sensor (block rate): one bar per
// day from /api/daily, with the day-scale range tabs; label names the legend there.
// thr bands the line by value (only the stretch past a threshold takes the warning/error colour), so a
// banded chart keeps the accent base colour instead of painting the whole line in the sensor's state.
function SensorChart({ itemId, units, color = 'var(--accent)', bars, label, thr, runs }: { itemId: string; units: string; color?: string; bars?: boolean; label?: string; thr?: Thr; runs?: boolean }) {
  const banded = !bars && thrOn(thr)
  // The items list is re-fetched on every poll (a new thr object each time) - key the rebuild on the
  // values, not the object, so the chart doesn't redraw for nothing.
  const thrKey = banded ? `${thr.warn}|${thr.high}|${thr.below ? 1 : 0}` : ''
  const [range, setRange] = useState(bars || runs ? '7d' : '2h')
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
  const zoomedRef = useRef(false) // true while the user has zoomed in; pauses auto-refresh
  const lastKey = useRef('')

  // Refresh the open chart periodically so it stays live.
  useEffect(() => { const t = setInterval(() => { if (!zoomedRef.current) setTick((x) => x + 1) }, 60000); return () => clearInterval(t) }, [])
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
    const get = (rk: string) => fetch(`/api/items/${itemId}/history?range=${rk}${runs ? '&runs=1' : ''}`)
      .then(async (r) => { if (!r.ok) throw new Error(await errText(r, 'Failed to load history')); return r.json() as Promise<Series> })
    // Bar mode: the day-scale ranges are trend-backed and trends lag the still-open hour - merge a
    // Bar mode consumes /api/daily - the SAME buckets the row's value and mini bars read - shaped
    // into a one-point-per-day Series (bucket i = local date today − (n−1−i), at noon).
    ;(bars
      ? fetch(`/api/daily?items=${encodeURIComponent(itemId)}&days=${DAYS_BY_RANGE[range] || 7}&off=${new Date().getTimezoneOffset()}`)
        .then(async (r) => { if (!r.ok) throw new Error(await errText(r, 'Failed to load history')); return r.json() })
        .then((m: Record<string, number[]>) => {
          const vals = m[itemId] || []
          return { name: label || '', units, kind: 'history', points: vals.map((v, i) => ({ t: i, v })) } as Series
        })
      : get(range))
      .then((d: Series) => { if (!cancelled) setData(d) })
      .catch((e) => { if (!cancelled) { setError(e.message || 'Failed to load history'); setData(null) } })
      .finally(() => { if (slowTimer) clearTimeout(slowTimer); if (!cancelled) { setLoading(false); setShowLoading(false) } })
    return () => { cancelled = true; if (slowTimer) clearTimeout(slowTimer) }
  }, [itemId, range, tick, bars, runs])

  useEffect(() => {
    if (plot.current) { plot.current.destroy(); plot.current = null }
    if (!host.current || !data || data.points.length === 0) return
    const width = host.current.clientWidth || 600
    const [opts, aligned] = bars
      ? buildBarPlot([{ label: data.name || 'value', units, values: data.points.map((p) => p.v ?? 0) }], width, chartColors(color), (z) => { zoomedRef.current = z })
      : buildPlot(data, units, width, chartColors(banded ? 'var(--accent)' : color), (z) => { zoomedRef.current = z }, banded ? thr : undefined, runs)
    plot.current = new uPlot(opts, aligned, host.current)
    colorLegendChecks(plot.current)
    return () => { if (plot.current) { plot.current.destroy(); plot.current = null } }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, units, color, themeTick, bars, thrKey, runs])

  useEffect(() => {
    function onResize() { if (plot.current && host.current) plot.current.setSize({ width: host.current.clientWidth, height: 320 }) }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  return (
    <div>
      <div className="rtabs">
        {(bars ? RANGES_BARS : runs ? RUN_RANGES : RANGES).map((rk) => (
          <button key={rk} className={'rtab' + (range === rk ? ' on' : '')} onClick={() => setRange(rk)}>{rk}</button>
        ))}
        {!bars && !loading && (() => { const b = rateTotal(data, units); return b != null ? <RateTotals totals={[{ label: label || '', bytes: b }]} range={range} /> : null })()}
      </div>
      {showLoading && <p style={{ color: 'var(--muted)', margin: '0.3rem 0' }}>Loading…</p>}
      {error && <p style={{ color: 'var(--err)', margin: '0.3rem 0' }}>{error}</p>}
      {!loading && !error && data && data.points.length === 0 && <p style={{ color: 'var(--muted)', margin: '0.3rem 0' }}>{runs ? 'No run in this range.' : 'No data in this range.'}</p>}
      <div ref={host} style={{ width: '100%' }} />
    </div>
  )
}

// Distinct series colours for the multi-channel graph (mid-tones that read in light and dark).
// Channel line colours (by series index). Amber-gold sits at index 1 so the ICMP group's second
// channel (Loss) is clearly apart from the red downtime band and the cyan primary (orange read too
// close to red, purple clashed with it). Ordered so no 2- or 3-channel group gets two similar lines.
// 6th is a violet, not a second yellow-green: a 6-channel group (e.g. an unRAID array with two
// cache drives) otherwise gave Disk 1 (gold) and Cache 2 (olive) near-identical lines.
const SERIES_COLORS = ['#2ea8c9', '#e0b53a', '#3aa856', '#e0803a', '#c9564f', '#9b6fd6']
// Lookback window per range key (seconds), so the group graph can pin its x-axis to the window.
const RANGE_SECS: Record<string, number> = { '2h': 7200, '2d': 172800, '7d': 604800, '1M': 2592000, '3M': 7776000, '6M': 15552000, '1Y': 31536000 }
// What each range key spans, spelled out for the traffic totals ("over 2 days").
const RANGE_WORDS: Record<string, string> = { '2h': '2 hours', '2d': '2 days', '7d': '7 days', '1M': '30 days', '3M': '90 days', '6M': '180 days', '1Y': '365 days' }

// rateTotal is how many bytes a rate sensor (bits or bytes per second: an interface's traffic, a VM's
// disk reads) moved over a loaded series, or null for any other unit. Hourly trends count their
// average for the hour; raw history counts each reading until the next one, and across a gap in the
// data only for one usual poll interval (what happened in the gap isn't known).
function rateTotal(d: Series | null, units: string): number | null {
  const perByte = units === 'bps' ? 8 : units === 'Bps' || units === 'B/s' ? 1 : 0
  if (!d || !perByte || d.points.length === 0) return null
  let sum = 0
  if (d.kind === 'trend') {
    d.points.forEach((p) => { if (p.avg != null && isFinite(p.avg)) sum += p.avg * 3600 })
  } else {
    const pts = d.points.filter((p) => p.v != null && isFinite(p.v)).sort((a, b) => a.t - b.t)
    const gaps: number[] = []
    for (let i = 1; i < pts.length; i++) gaps.push(pts[i].t - pts[i - 1].t)
    const usual = gaps.length ? [...gaps].sort((a, b) => a - b)[Math.floor(gaps.length / 2)] : 60
    pts.forEach((p, i) => { const dt = i < gaps.length ? gaps[i] : usual; sum += (p.v as number) * Math.min(dt, 2 * usual) })
  }
  return sum / perByte
}

// RateTotals is the line beside the range tabs of a traffic chart: "In 1.24 TB · Out 301 GB over 30 days".
function RateTotals({ totals, range }: { totals: { label: string; bytes: number }[]; range: string }) {
  if (totals.length === 0) return null
  const one = totals.length === 1
  return (
    <span className="rtotal" title="Total moved in the chart's range (zooming doesn't change it)">
      {totals.map((t, i) => <span key={t.label + i}>{i > 0 && ' · '}{!one && <span className="rtotal-l">{t.label} </span>}{fmtNum(t.bytes, 'B')}</span>)}
      {` over ${RANGE_WORDS[range] || range}`}
    </span>
  )
}

// Downtime channel (inverted reachability): a red band that only rises when the target is unreachable.
const DOWNTIME_STROKE = '#d64550'
const DOWNTIME_FILL = 'rgba(214, 69, 80, 0.30)'

// invert turns a reachable (1=up) channel into downtime (spikes to 1 when down), drawn as a red band.
// thr draws the channel's warning/high as dashed reference lines (group lines keep their own colours).
// stepped draws the band from a failed run to the next one (a push job stays failed until it next
// succeeds); color pins a channel's colour regardless of its place in the legend.
type GroupChan = { id: string; label: string; units: string; invert?: boolean; defaultOff?: boolean; hold?: boolean; seedValue?: number; seedClock?: number; thr?: Thr; stepped?: boolean; color?: string }

// zeroRange starts an axis at 0 with a little headroom: a run's speed against nothing, so a drop reads
// as one.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const zeroRange = ((_u: any, _min: number | null, max: number | null) => [0, max != null && max > 0 ? max * 1.1 : 1]) as unknown as uPlot.Scale['range']

// colorLegendChecks tints each legend row's check (the ::after from theme.css) with that series'
// colour, by copying the marker's border colour into a --mk custom property uPlot doesn't expose.
function colorLegendChecks(u: uPlot) {
  // Clear the swatch fill (a filled series like downtime otherwise gets a solid box while the rest are
  // outlines) so every swatch is a consistent coloured outline + coloured check.
  u.root.querySelectorAll('.u-legend .u-marker').forEach((m) => { const el = m as HTMLElement; el.style.setProperty('--mk', getComputedStyle(el).borderTopColor || '#fff'); el.style.background = 'transparent' })
}

// buildMultiPlot overlays several channels on one uPlot: timestamps are unioned, each distinct unit
// gets its own scale (axes drawn for the first two, left/right), and the legend lists every channel
// with its live value and toggles it on click. xrange pins the x-axis to the requested window.
// mode.runs is a sensor measured by runs (the speed test): its channels share each run's timestamp, so
// nothing is bucketed; every run is a dot, lines join only runs next to each other, a failed run is a
// red ✕ at its time (mode.reasons says why on hover) and the axes start at 0.
function buildMultiPlot(series: { label: string; units: string; points: { t: number; v: number | null; lo?: number | null; hi?: number | null }[]; downtime?: boolean; off?: boolean; hold?: boolean; seedValue?: number; seedClock?: number; thr?: Thr; stepped?: boolean; color?: string }[], width: number, c: ChartColors, xrange?: [number, number], onZoom?: (zoomed: boolean) => void, onToggle?: (label: string, show: boolean) => void, mode?: { runs?: boolean; reasons?: Map<number, string> }): [uPlot.Options, uPlot.AlignedData] {
  const runs = !!mode?.runs
  // Bucket timestamps to the typical sampling interval so channels sampled at slightly offset clocks
  // land on the same x (else the line renders as dots) while a genuine gap still breaks the line.
  const deltas: number[] = []
  series.forEach((s) => { const ts = s.points.map((p) => p.t).sort((a, b) => a - b); for (let k = 1; k < ts.length; k++) deltas.push(ts[k] - ts[k - 1]) })
  deltas.sort((a, b) => a - b)
  const bucket = runs ? 1 : deltas.length ? Math.max(1, deltas[Math.floor(deltas.length / 2)]) : 60
  const round = (t: number) => Math.round(t / bucket) * bucket
  const tset = new Set<number>()
  series.forEach((s) => s.points.forEach((p) => tset.add(round(p.t))))
  // Extend the axis to the window edges, but ONLY where an edge lies outside the data. Adding a
  // boundary x that falls *inside* the data range inserts a null column mid-line, which uPlot then
  // paints as a stray dot at the plot edge - and the window 'from' lands inside the data whenever the
  // last poll lags 'now' (the usual case). With no data, still span the window.
  if (xrange) {
    let dmin = Infinity, dmax = -Infinity
    tset.forEach((t) => { if (t < dmin) dmin = t; if (t > dmax) dmax = t })
    if (!isFinite(dmin) || xrange[0] < dmin) tset.add(xrange[0])
    if (!isFinite(dmax) || xrange[1] > dmax) tset.add(xrange[1])
  }
  const xs = [...tset].sort((a, b) => a - b)
  const xi = new Map(xs.map((t, i) => [t, i]))
  const ys = series.map((s) => { const a: (number | null)[] = new Array(xs.length).fill(null); s.points.forEach((p) => { const i = xi.get(round(p.t)); if (i !== undefined) a[i] = p.v }); return a })
  // Held channels (parked unRAID disks): carry the last reading forward as a flat line instead of
  // gapping. Fill each empty slot with the previous value; seed the leading run from the channel's
  // last known value when the drive was already parked before the window opened (seedClock < from).
  const from0 = xrange ? xrange[0] : (xs.length ? xs[0] : 0)
  series.forEach((s, si) => {
    if (!s.hold) return
    let carry: number | null = s.seedValue != null && s.seedClock != null && s.seedClock < from0 ? s.seedValue : null
    for (let j = 0; j < xs.length; j++) {
      if (ys[si][j] == null) ys[si][j] = carry
      else carry = ys[si][j]
    }
  })
  // Axes go to the first two units; the Downtime band's 0-1 scale comes last, so a real second unit
  // (a URL's certificate days beside its response time) gets the right axis, not the band.
  const units = [...new Set([...series.filter((s) => !s.downtime), ...series.filter((s) => s.downtime)].map((s) => s.units))]
  const scaleKey = (u: string) => 'y' + units.indexOf(u)
  const grid = { stroke: c.grid, width: 1 }
  const ticks = { stroke: c.grid, width: 1 }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  // Day labels round to the nearest day (a reading rounds down), so each one names its gridline.
  const yv = (u: string) => ((_up: any, splits: number[]) => splits.map((v) => fmtNum(u === 'days' ? Math.round(v) : v, u))) as unknown as uPlot.Axis['values']
  // Loss beside Downtime (the ping chart) pins the % scale to exactly 0-100 (applied to scaleCfg
  // below): a pinned scale never re-ranges, which makes it the ideal GRID OWNER (user's idea).
  const hasDown = series.some((s) => s.downtime)
  const pctFixed = hasDown && units.includes('%')
  const axes: uPlot.Axis[] = [{ stroke: c.axis, grid, ticks }]
  if (pctFixed && units[0] !== '%' && units.length > 1) {
    // Fixed shared grid: the pinned 0-100 % scale defines the gridlines - 0/20/40/60/80/100 %, at
    // constant fractions of the plot height, so the grid NEVER moves across refreshes or hosts. The
    // primary (left) axis keeps its auto-fitted range but places its labels ON those fixed lines.
    const FR = [0, 0.2, 0.4, 0.6, 0.8, 1]
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const fracSplits = ((u: any) => { const l = u.scales[scaleKey(units[0])]; return l && l.min != null && l.max != null ? FR.map((f) => l.min + f * (l.max - l.min)) : FR }) as unknown as uPlot.Axis['splits']
    axes.push({ scale: scaleKey(units[0]), stroke: c.axis, grid, ticks, size: axisSize, values: yv(units[0]), splits: fracSplits })
    axes.push({ scale: scaleKey('%'), side: 1, stroke: c.axis, grid: { show: false }, ticks, size: axisSize, values: yv('%'), splits: (() => FR.map((f) => f * 100)) as unknown as uPlot.Axis['splits'] })
  } else {
    // A single-scale group (network In/Out) reads like a single-metric chart: axis on the right.
    axes.push({ scale: scaleKey(units[0]), stroke: c.axis, grid, ticks, size: axisSize, values: yv(units[0]), ...(units.length === 1 ? { side: 1 } : {}) })
    // No pinned scale: the primary owns the grid and the right axis rides ITS gridlines - the same
    // fractional heights mapped into the right scale, recomputed every draw (u.axes[1]._splits).
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const rightSplits = ((u: any, _ax: number, smin: number, smax: number) => {
      const ls: number[] = (u.axes[1] && u.axes[1]._splits) || []
      const l = u.scales[scaleKey(units[0])]
      if (!ls.length || l == null || l.min == null || l.max == null || l.max === l.min) return [smin, smax]
      // Label precision follows the scale span; rounding shifts a label by well under a pixel.
      const dp = smax - smin >= 20 ? 1 : smax - smin >= 2 ? 10 : 100
      return ls.map((v: number) => Math.round((smin + ((v - l.min) / (l.max - l.min)) * (smax - smin)) * dp) / dp)
    }) as unknown as uPlot.Axis['splits']
    // The downtime / failed band's 0-1 scale never gets an axis of its own: its numbers mean nothing.
    if (units.length > 1 && series.some((s) => !s.downtime && s.units === units[1])) axes.push({ scale: scaleKey(units[1]), side: 1, stroke: c.axis, grid: { show: false }, ticks, size: axisSize, values: yv(units[1]), splits: rightSplits })
  }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const xVal = (u: any, v: number | null) => { const t = v ?? lastVal(u, 0); return t == null ? '--' : new Date(t * 1000).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' }) }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const scaleCfg: Record<string, any> = { x: { time: true } }
  const uplotSeries: uPlot.Series[] = [{ value: xVal }]
  series.forEach((s, i) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const val = (u: any, v: number | null, _si: number, idx: number | null) => {
      // Idle, the legend shows the latest reading; on a run that didn't measure this, it says so.
      const n = v ?? (runs && idx != null ? null : lastVal(u, i + 1))
      if (n == null) return '--'
      if (!s.downtime) return fmtNum(n, s.units)
      if (!runs) return n > 0 ? 'down' : 'up'
      if (!(n > 0)) return 'ok'
      const why = idx != null ? mode?.reasons?.get(u.data[0][idx]) : undefined
      return why ? `failed: ${why.length > 140 ? why.slice(0, 139) + '…' : why}` : 'failed'
    }
    if (s.downtime) scaleCfg[scaleKey(s.units)] = { range: [0, 1] } // pin the status band to the bottom
    const color = s.downtime ? DOWNTIME_STROKE : s.color || SERIES_COLORS[i % SERIES_COLORS.length]
    // The first (primary) channel is drawn a touch heavier so it reads as the main field.
    uplotSeries.push({
      label: s.label,
      show: !s.off, // constants (a port's Speed/Link) start hidden - the legend keeps the value, a click reveals the line
      stroke: color,
      fill: s.downtime && !runs ? DOWNTIME_FILL : undefined,
      width: s.downtime ? 1 : i === 0 ? 2 : 1.5,
      // Lines-only, consistent across ranges/zoom (uPlot else shows dots on sparse data); runs are dots.
      points: runs && !s.downtime ? { show: true, size: 6, width: 1, fill: color, stroke: color } : { show: false, size: 0 },
      // A run's result is drawn by the ✕ markers below, not as a band; a push job's failure holds until its next run.
      ...(s.downtime && runs ? { paths: () => null } : s.downtime && s.stepped ? { paths: uPlot.paths.stepped!({ align: 1 }) } : {}),
      scale: scaleKey(s.units),
      value: val,
    } as uPlot.Series)
  })
  // Loss rides with Downtime on the ping chart: pin its % scale to exactly 0-100 (no auto headroom),
  // so "100 % lost" and downtime's "down" (a pinned 0-1 scale) peak at the same height, and 0 % sits
  // on the bottom edge with the "up" line. Other %-scales (disk, memory) keep uPlot's auto-range zoom.
  if (pctFixed) scaleCfg[scaleKey('%')] = { range: [0, 100] }
  // A non-pinned % scale (disk Used %, memory %, radio utilization) gets a minimum span so a
  // near-constant percentage reads flat instead of a full-height ramp with identical gridlines.
  else if (units.includes('%')) scaleCfg[scaleKey('%')] = { range: pctRange }
  // A certificate's days left: a minimum span, so the slow countdown doesn't read as steps.
  if (units.includes('days')) scaleCfg[scaleKey('days')] = { range: daysRange }
  // Runs: a speed or a round trip against 0, not zoomed to the spread between runs.
  if (runs) units.forEach((u) => { if (!scaleCfg[scaleKey(u)]) scaleCfg[scaleKey(u)] = { range: zeroRange } })
  // Primary channel min/max envelope (a shaded band), when it carries trend min/max - long ranges
  // only; short ranges are raw history (no min/max), so the band simply doesn't appear there.
  const p0 = series[0]
  // The MAIN channel - the primary, when it is the only channel on its unit (ICMP response time, a
  // disk's Used %) - is banded by value like a single-sensor chart: its line, fill and envelope turn
  // the warning / error colour only past its own thresholds. Peer groups (drive temps, CPU cores,
  // In/Out) keep their identity colours - a gold channel would be indistinguishable from "warning".
  const mainThr = p0 && !p0.downtime && series.filter((s) => s.units === p0.units).length === 1 && thrOn(p0.thr) ? p0.thr : undefined
  const paint = (alpha: number, plain: string) => (mainThr ? thrPaint(scaleKey(p0.units), mainThr, c, alpha) : plain)
  if (mainThr) (uplotSeries[1] as uPlot.Series).stroke = paint(1, c.line)
  const extraYs: (number | null)[][] = []
  const bands: uPlot.Band[] = []
  if (p0 && !runs && !p0.downtime && !p0.off && p0.points.some((p) => p.lo != null && p.hi != null)) {
    const lo: (number | null)[] = new Array(xs.length).fill(null)
    const hi: (number | null)[] = new Array(xs.length).fill(null)
    p0.points.forEach((p) => { const i = xi.get(round(p.t)); if (i !== undefined) { lo[i] = p.lo ?? null; hi[i] = p.hi ?? null } })
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const softVal = (_u: any, v: number | null) => (v == null ? '--' : fmtNum(v, p0.units))
    const minIdx = uplotSeries.length // series-array index (x is at 0)
    uplotSeries.push({ label: 'min', stroke: paint(0.4, c.soft), width: 0.75, points: { show: false, size: 0 }, scale: scaleKey(p0.units), value: softVal } as uPlot.Series)
    uplotSeries.push({ label: 'max', stroke: paint(0.4, c.soft), width: 0.75, points: { show: false, size: 0 }, scale: scaleKey(p0.units), value: softVal } as uPlot.Series)
    extraYs.push(lo, hi)
    bands.push({ series: [minIdx + 1, minIdx], fill: paint(0.12, c.fill) }) // fill between max and min
  }
  // Match the single-sensor look: shade under the primary channel - except on trend ranges (the
  // min/max band already shades around the line), for downtime, and when siblings share the primary's
  // unit (network In/Out are peers on one scale - shading just one of them reads as favouritism).
  if (p0 && !runs && !p0.downtime && !bands.length && series.filter((s) => s.units === p0.units).length === 1) (uplotSeries[1] as uPlot.Series).fill = paint(0.12, c.fill)
  // cursor.points.show:false removes uPlot's hover marker dot (see buildPlot) - the real "stray dot".
  const opts = { width, height: 320, scales: scaleCfg, axes, series: uplotSeries, legend: { show: true }, cursor: { points: { show: false } }, bands, ...zoomHook(onZoom, xrange, onToggle) } as uPlot.Options
  // Threshold reference lines per channel, merged where channels share one (all array drives at
  // 40/45 draw a single pair; mixed HDD + SSD groups get both pairs). A line shows while any of its
  // channels is visible, so toggling a drive off in the legend drops lines only it had.
  const merged = new Map<string, ThrLine>()
  series.forEach((s, i) => {
    if (s.downtime || !thrOn(s.thr)) return
    thrLines(s.thr, scaleKey(s.units), [i + 1], c, s.units, s.label).forEach((l) => {
      const k = `${l.scale}|${l.value}|${l.color}`
      const m = merged.get(k)
      // Shared by several channels (every HDD at 40 °C): the value alone says it; no single owner.
      if (m) { m.series.push(i + 1); m.owner = undefined }
      else merged.set(k, l)
    })
  })
  addThrLines(opts, [...merged.values()])
  if (runs) {
    // A failed run: a dashed red line and a ✕ at its time, while its Result channel is shown.
    const fi = series.findIndex((s) => s.downtime) + 1
    if (fi > 0) {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const hooks: any = (opts.hooks = opts.hooks || {})
      hooks.draw = [...(hooks.draw || []), (u: uPlot) => {
        if (!u.series[fi].show) return
        const fy = u.data[fi] as (number | null)[]
        const { left, top, width: bw, height } = u.bbox
        const pr = uPlot.pxRatio
        const ctx = u.ctx
        ctx.save()
        ctx.strokeStyle = DOWNTIME_STROKE
        ctx.fillStyle = DOWNTIME_STROKE
        ctx.lineWidth = 2 * pr
        ctx.globalAlpha = 0.85
        ctx.setLineDash([3 * pr, 3 * pr])
        ctx.font = `700 ${13 * pr}px sans-serif`
        ctx.textAlign = 'center'
        for (let j = 0; j < fy.length; j++) {
          const v = fy[j]
          if (v == null || !(v > 0)) continue
          const x = u.valToPos(u.data[0][j], 'x', true)
          if (x < left || x > left + bw) continue
          ctx.beginPath(); ctx.moveTo(x, top); ctx.lineTo(x, top + height); ctx.stroke()
          ctx.fillText('✕', x, top + height - 4 * pr)
        }
        ctx.restore()
      }]
    }
    return [opts, [xs, ...ys] as uPlot.AlignedData] // every run is a dot; a failed run is the only break
  }
  // insertGaps breaks the line where sampling actually stopped (a real outage) instead of drawing a
  // straight segment across it; bucketing above keeps offset-but-regular channels connected.
  const [gx, gy] = insertGaps(xs, [...ys, ...extraYs])
  return [opts, [gx, ...dropIsolated(gy)] as uPlot.AlignedData]
}

// buildBarPlot renders /api/daily's pre-bucketed values - the SAME buckets the row headline and
// mini bars read, so the chart can never disagree with them - as one bar per LOCAL calendar day
// (bucket i is the local date today − (n−1−i); the last bucket is today so far, live). Channels
// are nested (each a subset of the one before: blocked ⊆ total), so bars simply overlay - the
// full bar is the first channel, later ones paint their share on top from the baseline.
function buildBarPlot(series: { label: string; units: string; values: number[] }[], width: number, c: ChartColors, onZoom?: (zoomed: boolean) => void, onToggle?: (label: string, show: boolean) => void): [uPlot.Options, uPlot.AlignedData] {
  const n = Math.max(0, ...series.map((s) => s.values.length))
  // Local-midnight day math via Date (DST-correct; t - t%86400 would give UTC midnight).
  const dayAt = (i: number) => { const d = new Date(); d.setHours(0, 0, 0, 0); d.setDate(d.getDate() - (n - 1 - i)); return Math.round(d.getTime() / 1000) }
  // Trim the leading all-zero run (a young device on a 1Y window would otherwise squish its few
  // real bars against the right edge); zero days inside the data stay as honest zero bars.
  let first = 0
  while (first < n - 1 && !series.some((s) => (s.values[first] ?? 0) > 0)) first++
  const idxs: number[] = []
  for (let i = first; i < n; i++) idxs.push(i)
  const deltas = series.map((s) => idxs.map((i) => s.values[i] ?? 0))
  // A nested channel can't exceed its parent (guards rounding/desync at the edges).
  for (let i = 1; i < deltas.length; i++) deltas[i] = deltas[i].map((v, j) => Math.min(v, deltas[i - 1][j]))
  // Bars center on noon; pad the x axis to whole days on both sides (null edge columns) so the
  // first and last bars sit inside the plot, not clipped.
  const x0 = n ? dayAt(first) : Math.round(new Date().setHours(0, 0, 0, 0) / 1000)
  const x1 = n ? dayAt(n - 1) + 86400 : x0 + 86400
  const xs = [x0, ...idxs.map((i) => dayAt(i) + 43200), x1]
  const cols: (number | null)[][] = deltas.map((ch) => [null, ...ch, null])
  const grid = { stroke: c.grid, width: 1 }
  const ticks = { stroke: c.grid, width: 1 }
  const units = series[0]?.units || ''
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const yValues = (_u: any, splits: number[]) => splits.map((v) => fmtNum(v, units))
  // Cursor/idle x reads as a day, not a minute. Idle falls back to the LAST BAR's day - lastVal(u,0)
  // would return the padding edge column (tomorrow's midnight), a day that hasn't happened.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const lastBarX = (u: any) => { for (let j = u.data[0].length - 1; j >= 0; j--) if (u.data.some((s: (number | null)[], si: number) => si > 0 && s[j] != null)) return u.data[0][j]; return null }
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const xVal = (u: any, v: number | null) => { const t = v ?? lastBarX(u); return t == null ? '--' : new Date(t * 1000).toLocaleDateString([], { month: 'short', day: 'numeric' }) }
  const barsPath = uPlot.paths.bars!({ size: [0.85, 100] })
  const uplotSeries: uPlot.Series[] = [{ value: xVal }]
  series.forEach((s, i) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const val = (u: any, v: number | null) => { const n = v ?? lastVal(u, i + 1); return n == null ? '--' : fmtNum(n, s.units) }
    // The nested share reads as "what got stopped" - red; the full bar keeps the primary blue.
    const color = s.label === 'Blocked' ? DOWNTIME_STROKE : SERIES_COLORS[i % SERIES_COLORS.length]
    uplotSeries.push({ label: s.label, stroke: color, fill: withAlpha(color, i === 0 ? 0.5 : 0.85), width: 1, paths: barsPath, points: { show: false, size: 0 }, value: val } as uPlot.Series)
  })
  // Bars sit on zero: floor the y range at 0 with a little headroom (uPlot's auto-range would
  // otherwise lift the floor to the smallest bar and make every day look near-identical).
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const yRange = ((_u: any, _min: number | null, max: number | null) => [0, Math.max(1, max ?? 1) * 1.05]) as unknown as uPlot.Scale['range']
  const scales: uPlot.Scales = { x: { time: true }, y: { range: yRange } }
  // Day-or-coarser tick increments only: uPlot's default time incrs happily pick 12h ticks on a 7d
  // window, which reads as "12am / 12pm" clutter between daily bars.
  const DAY_INCRS = [86400, 172800, 259200, 604800, 1209600, 2592000, 5184000, 7776000, 15552000, 31536000]
  const axes: uPlot.Axis[] = [
    { stroke: c.axis, grid, ticks, incrs: DAY_INCRS },
    { stroke: c.axis, grid, ticks, side: 1, size: axisSize, values: yValues as unknown as uPlot.Axis['values'] },
  ]
  const opts = { width, height: 320, scales, axes, series: uplotSeries, legend: { show: true }, cursor: { points: { show: false } }, ...zoomHook(onZoom, [x0, x1], onToggle) } as uPlot.Options
  return [opts, [xs, ...cols] as uPlot.AlignedData]
}

// zoomHook reports (via onZoom) whether the x view is narrower than the full window - so the caller
// can pause auto-refresh while the user is zoomed in. uPlot fires setScale on init (full = not
// zoomed), on a drag-zoom, and on a double-click reset. onToggle (optional) reports legend
// show/hide clicks so the caller can persist them across the chart's frequent rebuilds.
function zoomHook(onZoom: ((z: boolean) => void) | undefined, xrange: [number, number] | undefined, onToggle?: (label: string, show: boolean) => void): Partial<uPlot.Options> {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const hooks: any = {}
  if (onZoom && xrange) {
    const full = xrange[1] - xrange[0]
    hooks.setScale = [(u: uPlot, key: string) => { if (key === 'x' && u.scales.x.min != null && u.scales.x.max != null) onZoom(u.scales.x.max - u.scales.x.min < full * 0.985) }]
  }
  if (onToggle) {
    // Fires only on a real show change (a legend click); cursor focus passes opts without `show`.
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    hooks.setSeries = [(u: uPlot, i: number | null, o: any) => { if (i != null && o && o.show !== undefined) { const l = u.series[i]?.label; if (typeof l === 'string') onToggle(l, !!o.show) } }]
  }
  return Object.keys(hooks).length ? { hooks } : {}
}

// SensorGroupChart overlays the channels of one instance (a disk mount, a NIC) in a single graph with
// a click-to-toggle legend - the PRTG "sensor with channels" view. Long ranges use each channel's avg.
// bars switches to the daily stacked-bar mode (counter totals) with its own day-scale range tabs.
// runs: a sensor measured by runs (the speed test): its own ranges and chart (buildMultiPlot's runs
// mode), and with list the runs under the chart, whose reasons the failed runs show on hover.
function SensorGroupChart({ channels, bars, runs }: { channels: GroupChan[]; bars?: boolean; runs?: { hostId: string; list?: boolean; lastRun?: number } }) {
  const runsOn = !!runs, runsHost = runs?.hostId || '', runsList = !!runs?.list
  const [range, setRange] = useState(bars || runsOn ? '7d' : '2h')
  const [runList, setRunList] = useState<SpeedRuns | null>(null)
  const [series, setSeries] = useState<{ label: string; units: string; points: { t: number; v: number | null; lo?: number | null; hi?: number | null }[]; values?: number[]; downtime?: boolean; off?: boolean; hold?: boolean; seedValue?: number; seedClock?: number; thr?: Thr; total?: number | null; stepped?: boolean; color?: string }[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)
  const [themeTick, setThemeTick] = useState(0)
  const host = useRef<HTMLDivElement>(null)
  const plot = useRef<uPlot | null>(null)
  const zoomedRef = useRef(false) // true while the user has zoomed in; pauses auto-refresh
  // The chart is destroyed+rebuilt on every auto-refresh and range change, and uPlot's legend
  // show/hide state lives inside the instance - so without this it would reset each time. Remember
  // the user's per-channel toggles (by label) and re-apply them after each rebuild.
  const showRef = useRef<Record<string, boolean>>({})
  const key = channels.map((c) => c.id).join(',')

  useEffect(() => { const t = setInterval(() => { if (!zoomedRef.current) setTick((x) => x + 1) }, 60000); return () => clearInterval(t) }, [])
  useEffect(() => {
    const mo = new MutationObserver(() => setThemeTick((x) => x + 1))
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => mo.disconnect()
  }, [])

  useEffect(() => {
    let cancelled = false
    setError(null)
    // Bar mode consumes /api/daily - the SAME buckets the row headline and mini bars read, so the
    // chart can never disagree with them. Line mode fetches each channel's history as before.
    if (bars) {
      const ids = channels.map((ch) => ch.id).join(',')
      fetch(`/api/daily?items=${encodeURIComponent(ids)}&days=${DAYS_BY_RANGE[range] || 7}&off=${new Date().getTimezoneOffset()}`)
        .then((r) => (r.ok ? r.json() : Promise.reject(new Error('failed'))))
        .then((m: Record<string, number[]>) => {
          if (cancelled) return
          setSeries(channels.map((ch) => ({ label: ch.label, units: ch.units, points: [], values: m[ch.id] || [] })))
        })
        .catch(() => { if (!cancelled) setError('Failed to load history') })
      return () => { cancelled = true }
    }
    const list: Promise<SpeedRuns | null> = runsList
      ? fetch(`/api/hosts/${runsHost}/speedtest/runs?range=${range}`).then((r) => (r.ok ? r.json() : null)).catch(() => null)
      : Promise.resolve(null)
    list.then((l) => { if (!cancelled) setRunList(l) })
    Promise.all(channels.map((ch) =>
      fetch(`/api/items/${ch.id}/history?range=${range}${runsOn ? '&runs=1' : ''}`).then((r) => (r.ok ? r.json() : null)).then((d: Series | null) => ({
        label: ch.label, units: ch.units, downtime: !!ch.invert, off: !!ch.defaultOff, hold: !!ch.hold, seedValue: ch.seedValue, seedClock: ch.seedClock, thr: ch.thr, stepped: !!ch.stepped, color: ch.color,
        total: ch.invert ? null : rateTotal(d, ch.units),
        // invert reachability into downtime: up (>0) -> 0, down -> 1. lo/hi carry the trend min/max
        // (present only on long ranges) so the primary channel can draw a shaded envelope.
        points: d ? d.points.map((p) => { let v = p.v ?? p.avg ?? null; if (ch.invert && v != null) v = v > 0 ? 0 : 1; return { t: p.t, v, lo: ch.invert ? null : (p.min ?? null), hi: ch.invert ? null : (p.max ?? null) } }) : [] as { t: number; v: number | null; lo?: number | null; hi?: number | null }[],
      })).catch(() => ({ label: ch.label, units: ch.units, downtime: !!ch.invert, hold: !!ch.hold, seedValue: ch.seedValue, seedClock: ch.seedClock, thr: ch.thr, stepped: !!ch.stepped, color: ch.color, points: [] as { t: number; v: number | null; lo?: number | null; hi?: number | null }[] }))
    )).then((res) => { if (!cancelled) setSeries(res) }).catch(() => { if (!cancelled) setError('Failed to load history') })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, range, tick, bars, runsOn, runsHost, runsList])

  useEffect(() => {
    if (plot.current) { plot.current.destroy(); plot.current = null }
    if (!host.current || !series || !series.some((s) => s.points.length > 0 || (s.values && s.values.length > 0) || (s.hold && s.seedValue != null))) return
    const width = host.current.clientWidth || 600
    const to = Math.floor(Date.now() / 1000)
    const from = to - (RANGE_SECS[range] || 7200)
    const onToggle = (label: string, show: boolean) => { showRef.current[label] = show }
    const [opts, aligned] = bars
      ? buildBarPlot(series.map((s) => ({ label: s.label, units: s.units, values: s.values || [] })), width, chartColors('var(--accent)'), (z) => { zoomedRef.current = z }, onToggle)
      : buildMultiPlot(series, width, chartColors('var(--accent)'), [from, to], (z) => { zoomedRef.current = z }, onToggle,
        runsOn ? { runs: true, reasons: new Map((runList?.runs || []).filter((r) => !r.ok && r.error).map((r) => [r.t, r.error as string])) } : undefined)
    plot.current = new uPlot(opts, aligned, host.current)
    // Re-apply the user's remembered show/hide choices (they survive refresh + range change).
    const sv = showRef.current
    plot.current.series.forEach((s, i) => { if (i === 0) return; const l = s.label; if (typeof l === 'string' && sv[l] !== undefined && sv[l] !== s.show) plot.current!.setSeries(i, { show: sv[l] }) })
    colorLegendChecks(plot.current)
    return () => { if (plot.current) { plot.current.destroy(); plot.current = null } }
  }, [series, themeTick, range, bars, runsOn, runList])

  useEffect(() => {
    function onResize() { if (plot.current && host.current) plot.current.setSize({ width: host.current.clientWidth, height: 320 }) }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const empty = series && !series.some((s) => s.points.length > 0 || (s.values && s.values.length > 0) || (s.hold && s.seedValue != null))
  const totals = bars || !series ? [] : series.filter((s) => s.total != null && s.points.length > 0).map((s) => ({ label: s.label, bytes: s.total as number }))
  return (
    <div>
      <div className="rtabs">
        {(bars ? RANGES_BARS : runsOn ? RUN_RANGES : RANGES).map((rk) => <button key={rk} className={'rtab' + (range === rk ? ' on' : '')} onClick={() => setRange(rk)}>{rk}</button>)}
        <RateTotals totals={totals} range={range} />
      </div>
      {error && <p style={{ color: 'var(--err)', margin: '0.3rem 0' }}>{error}</p>}
      {empty && <p style={{ color: 'var(--muted)', margin: '0.3rem 0' }}>{runsOn ? `No run in this range${runs?.lastRun ? `: the last one was ${relTime(runs.lastRun)}` : ''}.` : 'No data in this range.'}</p>}
      <div ref={host} style={{ width: '100%' }} />
      {runsList && <SpeedRunsList data={runList} />}
    </div>
  )
}

// SpeedRun is one speed test run (speeds Mbps, round trips seconds, as the template stores them).
type SpeedRun = { t: number; ok: boolean; error?: string; down?: number; up?: number; latency?: number; jitter?: number; loaded_down?: number; loaded_up?: number; loss?: number; site?: string }
type SpeedRuns = { interval: string; kind: string; runs: SpeedRun[] }

// SpeedRunsList is the speed test's runs under its Speed chart, newest first, over the chart's range:
// what each run measured and, for one that couldn't, why. Export CSV saves every run in the range.
function SpeedRunsList({ data }: { data: SpeedRuns | null }) {
  const [all, setAll] = useState(false)
  if (!data || data.runs.length === 0) return null
  const SHOW = 7
  const runs = data.runs
  const shown = all ? runs : runs.slice(0, SHOW)
  const hasLoss = runs.some((r) => r.loss != null)
  const val = (n: number | undefined, u: string) => (n == null ? '--' : fmtNum(n, u))
  const ms = (n: number | undefined) => (n == null ? '' : Math.round(n * 100000) / 100)
  const when = (t: number) => new Date(t * 1000).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  const every = data.interval ? data.interval.replace(/^(\d+)([smhdw])$/, '$1 $2') : ''
  const exportRuns = () => downloadCSV(`argus-speedtest-runs-${csvStamp()}.csv`,
    ['Ran', 'Download (Mbps)', 'Upload (Mbps)', 'Latency (ms)', 'Jitter (ms)', 'While downloading (ms)', 'While uploading (ms)', 'Packet loss (%)', 'Test site', 'Result', 'Reason'],
    runs.map((r) => [csvTime(r.t), r.down, r.up, ms(r.latency), ms(r.jitter), ms(r.loaded_down), ms(r.loaded_up), r.loss, r.site, r.ok ? 'ok' : 'failed', r.error]))
  return (
    <div className="runs-list">
      <div className="runs-head">
        <span className="runs-title">Runs</span>
        <span className="runs-sub">{every ? `every ${every}, ` : ''}newest first{data.kind === 'trend' ? '; hourly averages, without the reason and test site (kept 90 days)' : ''}</span>
        <span className="runs-acts">
          {runs.length > SHOW && <button className="btn" onClick={() => setAll((a) => !a)}>{all ? 'Show fewer' : `Show all ${runs.length}`}</button>}
          <button className="btn" onClick={exportRuns}>Export CSV</button>
        </span>
      </div>
      <div className="runs-scroll">
        <table className="runs-table">
          <thead><tr><th>Ran</th><th>Download</th><th>Upload</th><th>Latency</th><th>Jitter</th><th>While busy ↓ / ↑</th>{hasLoss && <th>Packet loss</th>}<th>Test site</th><th>Result</th></tr></thead>
          <tbody>
            {shown.map((r) => (
              <tr key={r.t}>
                <td>{when(r.t)}</td>
                <td className="num">{val(r.down, 'Mbps')}</td>
                <td className="num">{val(r.up, 'Mbps')}</td>
                <td>{val(r.latency, 's')}</td>
                <td>{val(r.jitter, 's')}</td>
                <td>{val(r.loaded_down, 's')} / {val(r.loaded_up, 's')}</td>
                {hasLoss && <td>{val(r.loss, '%')}</td>}
                <td>{r.site || '--'}</td>
                <td className="runs-result">{r.ok ? <span className="runs-ok">ok</span> : <><span className="runs-fail">failed</span>{r.error && <span className="runs-why"> {r.error}</span>}</>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
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
  const [nu, setNu] = useState<{ email: string; name: string; surname: string; role: string; password: string; sites: string[] }>({ email: '', name: '', surname: '', role: 'viewer', password: '', sites: [] })
  // The sites an account can be limited to (the same picker as a channel's).
  const [siteOptions, setSiteOptions] = useState<string[]>([])
  useEffect(() => { fetch('/api/notify/sites').then((r) => (r.ok ? r.json() : [])).then((s) => setSiteOptions(s || [])).catch(() => {}) }, [])
  const usersRef = useRef<User[]>([])
  usersRef.current = users

  function load() { fetch('/api/users').then((r) => r.json()).then((u) => { setUsers(u || []); setLoaded(true) }).catch(() => toast.error('Failed to load users')) }
  useEffect(() => { load() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  async function fail(res: Response) { toast.error(await errText(res, 'Request failed')); load() }
  function edit(id: number, patch: Partial<User>) { setUsers((us) => us.map((x) => (x.id === id ? { ...x, ...patch } : x))) }

  // Persist the row's email/name/surname/role/sites (called on blur of a field, a role or sites change).
  async function saveUser(id: number) {
    const u = usersRef.current.find((x) => x.id === id); if (!u) return
    const res = await fetch(`/api/users/${id}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ email: u.email, name: u.name, surname: u.surname, role: u.role, sites: u.sites || [] }) })
    if (!res.ok) return fail(res)
    const saved: User = await res.json()
    edit(id, { sites: saved.sites || [] }) // an admin keeps no sites
    toast.success('Saved')
  }
  async function create(e: FormEvent) {
    e.preventDefault()
    const res = await fetch('/api/users', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(nu) })
    if (!res.ok) return toast.error(await errText(res, 'Request failed'))
    setNu({ email: '', name: '', surname: '', role: 'viewer', password: '', sites: [] }); setAdding(false); toast.success('User created'); load()
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
  async function revokeTokens(u: User) {
    if (!(await confirm({ title: 'Revoke API tokens', message: `Revoke all ${u.tokens} API token${u.tokens === 1 ? '' : 's'} of ${u.email}? Scripts using them stop working at once.`, confirmLabel: 'Revoke', danger: true }))) return
    const res = await fetch(`/api/users/${u.id}/tokens`, { method: 'DELETE' })
    if (!res.ok) return fail(res)
    toast.success(`API tokens revoked for ${u.email}`); load()
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
    if (u.tokens) a.push({ label: `Revoke API tokens (${u.tokens})`, icon: uIcon.key, onClick: () => revokeTokens(u) })
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
        <PanelTitle eyebrow="Admin">Users</PanelTitle><span className="hint">{users.length} account{users.length === 1 ? '' : 's'}</span>
        <div className="tools"><button className="btn primary" onClick={() => setAdding((v) => !v)}>{adding ? 'Cancel' : '+ Add user'}</button></div>
      </div>

      {adding && (
        <form onSubmit={create} style={{ display: 'flex', flexWrap: 'wrap', gap: '0.5rem', alignItems: 'center', padding: '10px 16px', borderBottom: '1px solid var(--border)', background: 'var(--elevated)' }}>
          <input className="input" type="email" placeholder="email" value={nu.email} onChange={(e) => setNu({ ...nu, email: e.target.value })} required />
          <input className="input" placeholder="name" value={nu.name} onChange={(e) => setNu({ ...nu, name: e.target.value })} />
          <input className="input" placeholder="surname" value={nu.surname} onChange={(e) => setNu({ ...nu, surname: e.target.value })} />
          <Select value={nu.role} onChange={(e) => setNu({ ...nu, role: e.target.value })}>{ROLES.map((r) => <option key={r} value={r}>{r}</option>)}</Select>
          {nu.role !== 'admin' && <div className="usites" title="Which sites this account sees"><SitePicker options={siteOptions} value={nu.sites} onChange={(v) => setNu({ ...nu, sites: v })} /></div>}
          <input className="input" type="password" placeholder="password (min 8)" value={nu.password} onChange={(e) => setNu({ ...nu, password: e.target.value })} required />
          <button type="submit" className="btn primary">Add</button>
        </form>
      )}

      {!loaded && <Skeleton rows={3} cols={6} />}
      {loaded && <table className="utable">
        <thead><tr><th style={{ width: '24%' }}>Email</th><th style={{ width: '14%' }}>Name</th><th style={{ width: '14%' }}>Surname</th><th>Role</th><th>Sites</th><th>2FA</th><th>Passkeys</th><th style={{ textAlign: 'right' }}>Manage</th></tr></thead>
        <tbody>
          {users.map((u) => (
            <tr key={u.id} style={{ opacity: u.disabled ? 0.5 : 1 }}>
              <td data-label="Email"><input className="cellinput mono" value={u.email} onChange={(e) => edit(u.id, { email: e.target.value })} onBlur={() => saveUser(u.id)} /></td>
              <td data-label="Name"><input className="cellinput" value={u.name} placeholder="Name" onChange={(e) => edit(u.id, { name: e.target.value })} onBlur={() => saveUser(u.id)} /></td>
              <td data-label="Surname"><input className="cellinput" value={u.surname} placeholder="Surname" onChange={(e) => edit(u.id, { surname: e.target.value })} onBlur={() => saveUser(u.id)} /></td>
              <td data-label="Role"><Select className="roleselect" value={u.role} onChange={(e) => { edit(u.id, { role: e.target.value }); setTimeout(() => saveUser(u.id), 0) }}>{ROLES.map((r) => <option key={r} value={r}>{r}</option>)}</Select></td>
              <td data-label="Sites" className="usites">{u.role === 'admin'
                ? <span className="okquiet" title="An admin always sees every site">All sites</span>
                : <SitePicker options={siteOptions} value={u.sites || []} onChange={(v) => { edit(u.id, { sites: v }); setTimeout(() => saveUser(u.id), 0) }} />}</td>
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
      <QuietHoursCard me={me} onMe={onMe} />
      <PasswordCard />
      <MfaCard />
      {passkeysAvailable && <PasskeyCard />}
      <TokensCard />
    </div>
  )
}

type APIToken = { id: number; name: string; scope: string; can: string; hint: string; created_at: number; expires_at?: number; expired?: boolean; last_used_at?: number; last_ip?: string }

const TOKEN_SCOPES: [string, string][] = [['read', 'Read only'], ['ack', 'Acknowledge, add notes'], ['maint', 'Open and close maintenance windows'], ['all', 'Everything I can do']]
const TOKEN_EXPIRY: [number, string][] = [[30, 'Expires in 30 days'], [90, 'Expires in 90 days'], [182, 'Expires in 6 months'], [365, 'Expires in 1 year'], [0, 'Never expires']]

// TokensCard is the user's personal API tokens: for scripts and other tools, acting as the user and
// narrowed to a scope; shown once when made.
function TokensCard() {
  const confirm = useConfirm()
  const toast = useToast()
  const [tokens, setTokens] = useState<APIToken[] | null>(null)
  const [name, setName] = useState('')
  const [scope, setScope] = useState('read')
  const [expires, setExpires] = useState(182)
  const [busy, setBusy] = useState(false)
  const [fresh, setFresh] = useState<{ name: string; token: string } | null>(null)
  const load = () => fetch('/api/me/tokens').then((r) => (r.ok ? r.json() : [])).then((t) => setTokens(t || [])).catch(() => setTokens([]))
  useEffect(() => { load() }, [])
  async function create() {
    setBusy(true)
    const res = await fetch('/api/me/tokens', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: name.trim(), scope, expires_days: expires }) }).catch(() => null)
    setBusy(false)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not create the token')); return }
    const d: { token: string; info: APIToken } = await res.json()
    setFresh({ name: d.info.name, token: d.token }); setName(''); load()
  }
  async function revoke(t: APIToken) {
    if (!(await confirm({ title: 'Revoke token', message: `Revoke "${t.name}"? Whatever uses it stops working at once.`, confirmLabel: 'Revoke', danger: true }))) return
    const res = await fetch(`/api/me/tokens/${t.id}`, { method: 'DELETE' }).catch(() => null)
    if (!res || !res.ok) { toast.error(await errText(res, 'Could not revoke the token')); return }
    toast.success('Token revoked'); load()
  }
  return (
    <Card title="API tokens" note="For scripts and other tools. A token acts as you, with your role and your sites, never more, and can be narrowed further. Send it as Authorization: Bearer <token>. Every change a token makes is in the change log under its name.">
      {fresh && (
        <div className="token-fresh">
          <span className="token-fresh-txt"><b>{fresh.name}: copy it now, it won't be shown again</b><CopyValue value={fresh.token} /></span>
          <Button variant="ghost" className="compact" onClick={() => setFresh(null)}>Done</Button>
        </div>
      )}
      {tokens === null ? <Skeleton rows={2} cols={2} /> : tokens.length === 0
        ? <p className="muted" style={{ margin: '0 0 12px' }}>No tokens yet.</p>
        : (
          <ul className="token-list">
            {tokens.map((t) => (
              <li key={t.id}>
                <span className="token-main">
                  <span><b>{t.name}</b> <span className="mono token-hint">…{t.hint}</span></span>
                  <span className="sub-line token-meta">
                    {t.can} · {t.expired ? <span className="txt-err">expired {fmtDay(t.expires_at!)}</span> : t.expires_at ? `expires ${fmtDay(t.expires_at)}` : 'never expires'} · {t.last_used_at ? `last used ${relTime(t.last_used_at)}${t.last_ip ? ` from ${t.last_ip}` : ''}` : 'never used'}
                  </span>
                </span>
                <Button variant="ghost" className="compact" onClick={() => revoke(t)}>Revoke</Button>
              </li>
            ))}
          </ul>
        )}
      <div className="token-form">
        <input className="input" maxLength={60} placeholder="Name, e.g. ticketing" value={name} onChange={(e) => setName(e.target.value)} aria-label="Token name" />
        <Select value={scope} onChange={(e) => setScope(e.target.value)} aria-label="What the token can do">
          {TOKEN_SCOPES.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </Select>
        <Select value={expires} onChange={(e) => setExpires(Number(e.target.value))} aria-label="When the token expires">
          {TOKEN_EXPIRY.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
        </Select>
        <Button variant="primary" onClick={create} disabled={busy || !name.trim()}>{busy ? 'Creating…' : 'Create token'}</Button>
      </div>
    </Card>
  )
}

// fmtDay is a date the way token expiries read: "Mar 29, 2027".
function fmtDay(unix: number): string {
  return new Date(unix * 1000).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

// QuietHoursCard sets the quiet hours of the user's personal channels: during them only the more
// serious problems come through; a quieter one that is still open when they end is sent then.
function QuietHoursCard({ me, onMe }: { me: Me; onMe: (m: Me) => void }) {
  const toast = useToast()
  const q = me.quiet || { start: -1, end: -1, floor: 4 }
  const [on, setOn] = useState(q.start >= 0 && q.end >= 0)
  const [from, setFrom] = useState(fmtHM24(q.start >= 0 ? q.start : 22 * 60))
  const [to, setTo] = useState(fmtHM24(q.end >= 0 ? q.end : 7 * 60))
  const [floor, setFloor] = useState(q.floor >= 3 && q.floor <= 5 ? q.floor : 4)
  const [busy, setBusy] = useState(false)
  const mins = (v: string) => { const [h, m] = v.split(':').map(Number); return (h || 0) * 60 + (m || 0) }

  async function save(next: { on: boolean; from: string; to: string; floor: number }) {
    setBusy(true)
    try {
      const quiet = next.on ? { start: mins(next.from), end: mins(next.to), floor: next.floor } : { start: -1, end: -1, floor: next.floor }
      const res = await fetch('/api/me/preferences', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ quiet }) })
      if (!res.ok) { toast.error(await errText(res, 'Could not save quiet hours')); return }
      onMe(await res.json()); toast.success(next.on ? 'Quiet hours saved.' : 'Quiet hours off.')
    } catch { toast.error('Could not save quiet hours') } finally { setBusy(false) }
  }

  return (
    <Card title="Quiet hours" note={`For your personal channels, in the Argus timezone${CLOCK.tz ? ` (${CLOCK.tz})` : ''}. Shared channels are not affected.`}>
      <div className="set-row set-toggle">
        <div className="set-head"><span className="flabel">Quiet hours</span></div>
        <Switch checked={on} disabled={busy} onChange={(v) => { setOn(v); save({ on: v, from, to, floor }) }} label={on ? 'On' : 'Off'} />
      </div>
      {on && (
        <div className="quiet-row">
          <Field label="From"><input className="input" type="time" value={from} disabled={busy} onChange={(e) => setFrom(e.target.value)} onBlur={() => save({ on, from, to, floor })} /></Field>
          <Field label="To"><input className="input" type="time" value={to} disabled={busy} onChange={(e) => setTo(e.target.value)} onBlur={() => save({ on, from, to, floor })} /></Field>
          <Field label="Still send">
            <select value={floor} disabled={busy} onChange={(e) => { const f = Number(e.target.value); setFloor(f); save({ on, from, to, floor: f }) }}>
              <option value={3}>Average and above</option>
              <option value={4}>High and above</option>
              <option value={5}>Disaster only</option>
            </select>
          </Field>
        </div>
      )}
      <p className="set-note">During quiet hours anything below that waits: if it is still open when they end, you get it then. Reminders, acknowledgements and recoveries below it are not sent meanwhile.</p>
    </Card>
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
    const pw = await prompt({ title: 'Turn off two-factor', label: 'Confirm your password', type: 'password', confirmLabel: 'Continue', required: true })
    if (!pw) return
    const code = await prompt({ title: 'Turn off two-factor', label: 'Enter the current code from your authenticator', confirmLabel: 'Turn off', required: true })
    if (!code) return
    const res = await fetch('/api/me/mfa/disable', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: pw, code }) })
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
    const pw = await prompt({ title: 'Add a passkey', label: 'Confirm your password', type: 'password', confirmLabel: 'Continue', required: true })
    if (!pw) return
    setBusy(true)
    try {
      await registerPasskey(name || 'Passkey', pw)
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

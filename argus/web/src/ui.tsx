// Shared UI primitives - the one design-system layer for the SPA. These wrap the
// token-based classes in theme.css (.btn, .card, .field, .banner, .badge) so the same
// widgets look and behave the same across every view, instead of being rebuilt ad-hoc
// with inline styles and hardcoded colors.
import { useState, type ButtonHTMLAttributes, type CSSProperties, type InputHTMLAttributes, type ReactNode } from 'react'

// copyToClipboard works over HTTPS (navigator.clipboard) and falls back to execCommand so Copy
// still works over plain HTTP on a private IP.
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard && window.isSecureContext) { await navigator.clipboard.writeText(text); return true }
    const ta = document.createElement('textarea')
    ta.value = text; ta.style.position = 'fixed'; ta.style.opacity = '0'
    document.body.appendChild(ta); ta.focus(); ta.select(); document.execCommand('copy'); document.body.removeChild(ta)
    return true
  } catch { return false }
}

type Variant = 'default' | 'primary' | 'ghost' | 'danger' | 'success'

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant; block?: boolean }

// Button maps to .btn plus a variant modifier; block adds width:100%. Extra className/style
// pass through for the few bespoke sizes (e.g. the duration picker).
export function Button({ variant = 'default', block, className, type = 'button', children, ...rest }: ButtonProps) {
  const cls = ['btn', variant !== 'default' ? variant : '', block ? 'block' : '', className].filter(Boolean).join(' ')
  return <button type={type} className={cls} {...rest}>{children}</button>
}

// Card is a panel-toned section for Account / Dashboard sub-cards, with an optional title/note.
export function Card({ title, note, children, className, style }: { title?: ReactNode; note?: ReactNode; children: ReactNode; className?: string; style?: CSSProperties }) {
  return (
    <section className={className ? `card ${className}` : 'card'} style={style}>
      {title != null && <h2 className="card-title">{title}</h2>}
      {note != null && <p className="card-note">{note}</p>}
      {children}
    </section>
  )
}

type FieldProps = InputHTMLAttributes<HTMLInputElement> & { label: ReactNode; children?: ReactNode }

// Field is a labeled control. It renders a styled <input> from the passed props, or wraps a
// custom control (select, etc.) passed as children. Styling comes from `.field` in theme.css.
export function Field({ label, children, ...input }: FieldProps) {
  return (
    <label className="field">
      <span>{label}</span>
      {children ?? <input {...input} />}
    </label>
  )
}

// Banner is a status message line, theme-aware via tokens - replaces inline crimson/seagreen text.
export function Banner({ variant = 'info', children }: { variant?: 'error' | 'success' | 'info' | 'warn'; children?: ReactNode }) {
  if (!children) return null
  return <p className={`banner ${variant}`}>{children}</p>
}

// Badge is a small pill; tone selects the on/off/err color treatment (.badge modifiers).
export function Badge({ tone, children, className, style }: { tone?: 'on' | 'off' | 'err'; children: ReactNode; className?: string; style?: CSSProperties }) {
  const cls = ['badge', tone, className].filter(Boolean).join(' ')
  return <span className={cls} style={style}>{children}</span>
}

// CopyButton copies `text` and flips its label to "Copied!" for 2s. Dedups the copy pattern.
export function CopyButton({ text, label = 'Copy', copiedLabel = 'Copied!', variant = 'ghost', className, style }: { text: string; label?: string; copiedLabel?: string; variant?: Variant; className?: string; style?: CSSProperties }) {
  const [copied, setCopied] = useState(false)
  return (
    <Button variant={variant} className={className} style={style} onClick={async () => { if (await copyToClipboard(text)) { setCopied(true); setTimeout(() => setCopied(false), 2000) } }}>
      {copied ? copiedLabel : label}
    </Button>
  )
}

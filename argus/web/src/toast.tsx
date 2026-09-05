// Transient feedback (toasts): "Saved", "Test notification sent", "Could not delete…" - shown for a few
// seconds in a corner and gone, instead of the persistent inline text lines the views used to leave behind.
// Usage: const toast = useToast(); toast.success('Channel saved.'); toast.error(msg)
// Errors linger longer (8s) and every toast can be dismissed by hand. Contextual form errors (login,
// password mismatch) stay inline where the field is - toasts are for the outcome of an action.
import { createContext, useCallback, useContext, useMemo, useRef, useState, type ReactNode } from 'react'

type Kind = 'success' | 'error' | 'info'
type Toast = { id: number; kind: Kind; message: ReactNode }

export type ToastAPI = {
  toast: (kind: Kind, message: ReactNode, opts?: { duration?: number }) => void
  success: (message: ReactNode) => void
  error: (message: ReactNode) => void
  info: (message: ReactNode) => void
}

const Ctx = createContext<ToastAPI | null>(null)

export function useToast(): ToastAPI {
  const c = useContext(Ctx)
  if (!c) throw new Error('useToast must be used within a ToastProvider')
  return c
}

const ICON: Record<Kind, ReactNode> = {
  success: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4"><path d="M20 6 9 17l-5-5" /></svg>,
  error: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="9" /><path d="M12 8v5M12 16h.01" /></svg>,
  info: <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"><circle cx="12" cy="12" r="9" /><path d="M12 11v5M12 8h.01" /></svg>,
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([])
  const seq = useRef(0)
  const dismiss = useCallback((id: number) => setItems((t) => t.filter((x) => x.id !== id)), [])
  const api = useMemo<ToastAPI>(() => {
    const toast: ToastAPI['toast'] = (kind, message, opts) => {
      if (!message) return
      const id = ++seq.current
      const duration = opts?.duration ?? (kind === 'error' ? 8000 : 4000)
      // Keep the stack short: at most four visible, oldest dropped first.
      setItems((t) => [...t.slice(-3), { id, kind, message }])
      if (duration > 0) setTimeout(() => dismiss(id), duration)
    }
    return { toast, success: (m) => toast('success', m), error: (m) => toast('error', m), info: (m) => toast('info', m) }
  }, [dismiss])

  return (
    <Ctx.Provider value={api}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className={`toast ${t.kind}`}>
            <span className="toast-ic">{ICON[t.kind]}</span>
            <span className="toast-msg">{t.message}</span>
            <button type="button" className="toast-x" aria-label="Dismiss" onClick={() => dismiss(t.id)}>×</button>
          </div>
        ))}
      </div>
    </Ctx.Provider>
  )
}

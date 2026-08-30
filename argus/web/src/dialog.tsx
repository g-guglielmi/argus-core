// In-app dialogs (confirm / prompt) so the UI never falls back to native browser popups.
// Usage: const confirm = useConfirm(); if (!(await confirm({ message: '…', danger: true }))) return
//        const prompt = usePrompt(); const v = await prompt({ label: '…', type: 'password' })
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { Button } from './ui'

type ConfirmOpts = { title?: string; message: ReactNode; confirmLabel?: string; cancelLabel?: string; danger?: boolean }
type PromptOpts = { title?: string; message?: ReactNode; label?: ReactNode; initial?: string; placeholder?: string; type?: 'text' | 'password'; confirmLabel?: string; cancelLabel?: string; required?: boolean }

type AlertOpts = { title?: string; message: ReactNode; confirmLabel?: string; danger?: boolean }

type DialogState =
  | { kind: 'confirm'; opts: ConfirmOpts; resolve: (v: boolean) => void }
  | { kind: 'prompt'; opts: PromptOpts; resolve: (v: string | null) => void }
  | { kind: 'alert'; opts: AlertOpts; resolve: () => void }

type DialogAPI = {
  confirm: (opts: ConfirmOpts) => Promise<boolean>
  prompt: (opts: PromptOpts) => Promise<string | null>
  alert: (opts: AlertOpts) => Promise<void>
}

const Ctx = createContext<DialogAPI | null>(null)

export function useConfirm() {
  const c = useContext(Ctx)
  if (!c) throw new Error('useConfirm must be used within a DialogProvider')
  return c.confirm
}
export function usePrompt() {
  const c = useContext(Ctx)
  if (!c) throw new Error('usePrompt must be used within a DialogProvider')
  return c.prompt
}
export function useAlert() {
  const c = useContext(Ctx)
  if (!c) throw new Error('useAlert must be used within a DialogProvider')
  return c.alert
}

export function DialogProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<DialogState | null>(null)
  const [value, setValue] = useState('')

  const api = useMemo<DialogAPI>(() => ({
    confirm: (opts) => new Promise<boolean>((resolve) => setState({ kind: 'confirm', opts, resolve })),
    prompt: (opts) => new Promise<string | null>((resolve) => { setValue(opts.initial ?? ''); setState({ kind: 'prompt', opts, resolve }) }),
    alert: (opts) => new Promise<void>((resolve) => setState({ kind: 'alert', opts, resolve })),
  }), [])

  const cancel = useCallback(() => {
    if (!state) return
    if (state.kind === 'confirm') state.resolve(false)
    else if (state.kind === 'prompt') state.resolve(null)
    else state.resolve()
    setState(null)
  }, [state])
  const accept = useCallback(() => {
    if (!state) return
    if (state.kind === 'confirm') state.resolve(true)
    else if (state.kind === 'prompt') state.resolve(value)
    else state.resolve()
    setState(null)
  }, [state, value])

  // Esc always cancels while a dialog is open.
  useEffect(() => {
    if (!state) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') cancel() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [state, cancel])

  const isPrompt = state?.kind === 'prompt'
  const opts = state?.opts as (ConfirmOpts & PromptOpts) | undefined
  const promptRequired = isPrompt && (state?.opts as PromptOpts).required
  const disabled = !!promptRequired && !value.trim()

  return (
    <Ctx.Provider value={api}>
      {children}
      {state && (
        <div className="dlg-backdrop" onMouseDown={(e) => { if (e.target === e.currentTarget) cancel() }}>
          <div className="dlg" role="dialog" aria-modal="true">
            {opts?.title && <div className="dlg-title">{opts.title}</div>}
            {opts?.message && <div className="dlg-msg">{opts.message}</div>}
            {isPrompt && (
              <label className="dlg-field">
                {opts?.label && <span>{opts.label}</span>}
                <input className="input" autoFocus type={(opts as PromptOpts)?.type || 'text'} value={value}
                  placeholder={(opts as PromptOpts)?.placeholder}
                  onChange={(e) => setValue(e.target.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter' && !disabled) accept() }} />
              </label>
            )}
            <div className="dlg-foot">
              {state.kind !== 'alert' && <Button variant="ghost" onClick={cancel}>{opts?.cancelLabel || 'Cancel'}</Button>}
              <Button variant={opts?.danger ? 'danger' : 'primary'} onClick={accept} disabled={disabled}>
                {opts?.confirmLabel || (isPrompt ? 'OK' : state.kind === 'alert' ? 'OK' : 'Confirm')}
              </Button>
            </div>
          </div>
        </div>
      )}
    </Ctx.Provider>
  )
}

import { useState, useEffect } from 'react'
import { CheckCircle2, AlertTriangle, X } from 'lucide-react'
import { cn } from './lib'

/** Tiny toast: a module-level emitter + a <Toaster/> that renders the stack. */
interface Toast {
  id: number
  msg: string
  type: 'success' | 'error'
}

let seq = 0
const listeners = new Set<(t: Toast[]) => void>()
let toasts: Toast[] = []

function emit() {
  for (const l of listeners) l(toasts)
}

export function toast(msg: string, type: 'success' | 'error' = 'success') {
  const t: Toast = { id: ++seq, msg, type }
  toasts = [...toasts, t]
  emit()
  setTimeout(() => {
    toasts = toasts.filter((x) => x.id !== t.id)
    emit()
  }, 4000)
}

export function Toaster() {
  const [items, setItems] = useState<Toast[]>(toasts)
  useEffect(() => {
    listeners.add(setItems)
    return () => {
      listeners.delete(setItems)
    }
  }, [])

  return (
    <div className="fixed bottom-4 right-4 z-50 flex flex-col gap-2">
      {items.map((t) => (
        <div
          key={t.id}
          className={cn(
            'flex items-center gap-2 rounded-lg border px-3 py-2 text-sm shadow-lg shadow-black/40',
            t.type === 'success'
              ? 'border-success/30 bg-[#151515] text-neutral-100'
              : 'border-destructive/40 bg-[#151515] text-neutral-100',
          )}
        >
          {t.type === 'success' ? (
            <CheckCircle2 className="h-4 w-4 text-success" />
          ) : (
            <AlertTriangle className="h-4 w-4 text-destructive" />
          )}
          <span>{t.msg}</span>
        </div>
      ))}
    </div>
  )
}

/** Icon re-export so callers don't import lucide twice. */
export { X as CloseIcon }

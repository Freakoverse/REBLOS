import type { ReactNode } from 'react'

/** A titled card wrapping a list of read-only config rows. */
export function ConfigSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="rounded-lg border border-border bg-card">
      <div className="border-b border-border px-4 py-2.5">
        <h3 className="text-sm font-semibold text-neutral-200">{title}</h3>
      </div>
      <div className="divide-y divide-border/60">{children}</div>
    </div>
  )
}

/** One read-only label / value (+ optional hint) config row. */
export function ConfigRow({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="flex items-start justify-between gap-4 px-4 py-2.5">
      <div className="min-w-0">
        <div className="text-sm text-neutral-300">{label}</div>
        {hint && <div className="text-xs text-muted-foreground">{hint}</div>}
      </div>
      <div className="shrink-0 text-right text-sm font-medium text-neutral-100">{value}</div>
    </div>
  )
}

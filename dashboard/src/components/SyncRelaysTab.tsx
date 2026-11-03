import { useCallback, useEffect, useState } from 'react'
import { Loader2, Plus, Trash2, AlertTriangle, Radio } from 'lucide-react'
import {
  listSyncRelays,
  addSyncRelay,
  setSyncRelayEnabled,
  removeSyncRelay,
  type SyncRelay,
} from '../api'
import { cn } from '../lib'
import { toast } from '../toast'

export function SyncRelaysTab() {
  const [relays, setRelays] = useState<SyncRelay[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [url, setUrl] = useState('')
  const [adding, setAdding] = useState(false)
  const [busy, setBusy] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setRelays(await listSyncRelays())
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load sync relays')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const add = async () => {
    const u = url.trim()
    if (!u) return
    setAdding(true)
    try {
      setRelays(await addSyncRelay(u))
      setUrl('')
      toast('Relay added')
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Add failed', 'error')
    } finally {
      setAdding(false)
    }
  }

  const toggle = async (r: SyncRelay) => {
    setBusy(r.url)
    try {
      await setSyncRelayEnabled(r.url, !r.enabled)
      await load()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Toggle failed', 'error')
    } finally {
      setBusy(null)
    }
  }

  const remove = async (r: SyncRelay) => {
    setBusy(r.url)
    try {
      await removeSyncRelay(r.url)
      toast(r.default ? 'Default disabled' : 'Relay removed')
      await load()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Remove failed', 'error')
    } finally {
      setBusy(null)
    }
  }

  const enabledCount = relays.filter((r) => r.enabled).length

  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        General relays this node pulls hub events and content from, in addition to each hub&apos;s own relay
        hints. Used by the durability sync to catch up on anything published while the node was down. Toggle a
        default off if you don&apos;t want it, or add your own.
      </p>

      {/* Add */}
      <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-3">
        <input
          value={url}
          onChange={(e) => setUrl(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && add()}
          placeholder="wss://relay.example.com"
          className="min-w-[260px] flex-1 rounded-lg border border-border bg-[#212121] px-3 py-2 font-mono text-sm text-white placeholder:text-muted-foreground focus:border-[#404040] focus:outline-none"
        />
        <button
          onClick={add}
          disabled={adding || !url.trim()}
          className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground hover:bg-primary-hover disabled:opacity-60"
        >
          {adding ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
          Add relay
        </button>
      </div>

      {/* List */}
      <div className="rounded-lg border border-border bg-card">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <h3 className="text-sm font-semibold text-neutral-200">
            Sync relays ({enabledCount}/{relays.length} on)
          </h3>
          {loading && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        </div>

        {error ? (
          <div className="flex items-center gap-2 px-4 py-8 text-sm text-destructive">
            <AlertTriangle className="h-4 w-4" /> {error}
          </div>
        ) : relays.length === 0 && !loading ? (
          <div className="flex flex-col items-center gap-2 px-4 py-10 text-center">
            <Radio className="h-8 w-8 text-neutral-700" />
            <p className="text-sm text-muted-foreground">No sync relays configured.</p>
          </div>
        ) : (
          <ul className="divide-y divide-border/60">
            {relays.map((r) => (
              <li key={r.url} className="flex items-center justify-between gap-3 px-4 py-2.5">
                <div className="flex min-w-0 items-center gap-2">
                  <span
                    className={cn('h-2 w-2 shrink-0 rounded-full', r.enabled ? 'bg-emerald-500' : 'bg-neutral-600')}
                  />
                  <span className={cn('truncate font-mono text-xs', r.enabled ? 'text-neutral-300' : 'text-neutral-500')}>
                    {r.url}
                  </span>
                  {r.default && (
                    <span className="shrink-0 rounded bg-[#212121] px-1.5 py-0.5 text-[10px] text-muted-foreground">
                      default
                    </span>
                  )}
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  <button
                    onClick={() => toggle(r)}
                    disabled={busy === r.url}
                    title={r.enabled ? 'Disable' : 'Enable'}
                    className={cn(
                      'relative h-5 w-9 rounded-full transition-colors disabled:opacity-50',
                      r.enabled ? 'bg-primary' : 'bg-neutral-700',
                    )}
                  >
                    <span
                      className={cn(
                        'absolute top-0.5 h-4 w-4 rounded-full bg-white transition-transform',
                        r.enabled ? 'translate-x-4' : 'translate-x-0.5',
                      )}
                    />
                  </button>
                  <button
                    onClick={() => remove(r)}
                    disabled={busy === r.url}
                    title={r.default ? 'Disable this default' : 'Remove'}
                    className="rounded-md p-1.5 text-neutral-500 transition-colors hover:bg-destructive/15 hover:text-destructive disabled:opacity-50"
                  >
                    <Trash2 className="h-4 w-4" />
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

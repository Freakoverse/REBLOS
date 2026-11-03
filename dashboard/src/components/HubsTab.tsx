import { useCallback, useEffect, useState } from 'react'
import { Loader2, Plus, Trash2, AlertTriangle, Boxes } from 'lucide-react'
import { listHubs, addHub, removeHub, type HubEntry } from '../api'
import { truncateMiddle } from '../lib'
import { toast } from '../toast'

export function HubsTab() {
  const [hubs, setHubs] = useState<HubEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [ref, setRef] = useState('')
  const [note, setNote] = useState('')
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<HubEntry | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setHubs(await listHubs())
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load hubs')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const add = async () => {
    const r = ref.trim()
    if (!r) return
    setAdding(true)
    try {
      await addHub(r, note.trim() || undefined)
      toast('Hub whitelisted')
      setRef('')
      setNote('')
      await load()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Add failed', 'error')
    } finally {
      setAdding(false)
    }
  }

  const remove = async (coordinate: string, deleteBlobs: boolean) => {
    try {
      await removeHub(coordinate, deleteBlobs)
      toast(deleteBlobs ? 'Hub removed and its blobs deleted' : 'Hub removed')
      setRemoving(null)
      await load()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Remove failed', 'error')
    }
  }

  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        Whitelist the hubs this node hosts. Paste a hub&apos;s <span className="font-mono">naddr</span> (or a
        raw <span className="font-mono">36942:&lt;pubkey&gt;:&lt;dTag&gt;</span> coordinate). The relay then
        reads that hub&apos;s member tree and accepts events only from its members, serving only its content.
      </p>

      {/* Add */}
      <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-3">
        <input
          value={ref}
          onChange={(e) => setRef(e.target.value)}
          placeholder="naddr1… or 36942:<pubkey>:<dTag>"
          className="min-w-[260px] flex-1 rounded-lg border border-border bg-[#212121] px-3 py-2 font-mono text-sm text-white placeholder:text-muted-foreground focus:border-[#404040] focus:outline-none"
        />
        <input
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="Note (optional)"
          className="min-w-[160px] flex-1 rounded-lg border border-border bg-[#212121] px-3 py-2 text-sm text-white placeholder:text-muted-foreground focus:border-[#404040] focus:outline-none"
        />
        <button
          onClick={add}
          disabled={adding || !ref.trim()}
          className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground hover:bg-primary-hover disabled:opacity-60"
        >
          {adding ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
          Add hub
        </button>
      </div>

      {/* List */}
      <div className="rounded-lg border border-border bg-card">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <h3 className="text-sm font-semibold text-neutral-200">Whitelisted hubs ({hubs.length})</h3>
          {loading && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        </div>

        {error ? (
          <div className="flex items-center gap-2 px-4 py-8 text-sm text-destructive">
            <AlertTriangle className="h-4 w-4" /> {error}
          </div>
        ) : hubs.length === 0 && !loading ? (
          <div className="flex flex-col items-center gap-2 px-4 py-10 text-center">
            <Boxes className="h-8 w-8 text-neutral-700" />
            <p className="text-sm text-muted-foreground">No hubs whitelisted yet.</p>
          </div>
        ) : (
          <ul className="divide-y divide-border/60">
            {hubs.map((h) => (
              <li key={h.coordinate} className="flex items-center justify-between gap-3 px-4 py-2.5">
                <div className="min-w-0">
                  {h.name && <div className="truncate text-sm font-medium text-neutral-100">{h.name}</div>}
                  <div className="truncate font-mono text-xs text-neutral-400">
                    {h.d} <span className="text-muted-foreground">· {truncateMiddle(h.pubkey, 10, 8)}</span>
                  </div>
                  {h.note && <div className="truncate text-xs text-muted-foreground">{h.note}</div>}
                </div>
                <div className="flex shrink-0 items-center gap-3">
                  <span
                    title={h.epoch < 0 ? 'Member tree not read yet' : `Epoch ${h.epoch}`}
                    className="rounded-md bg-[#212121] px-2 py-1 text-xs text-neutral-300"
                  >
                    {h.epoch < 0 ? 'no tree yet' : `${h.members} member${h.members === 1 ? '' : 's'}`}
                  </span>
                  <button
                    onClick={() => setRemoving(h)}
                    title="Remove"
                    className="rounded-md p-1.5 text-neutral-500 transition-colors hover:bg-destructive/15 hover:text-destructive"
                  >
                    <Trash2 className="h-4 w-4" />
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>

      {removing && (
        <RemoveConfirm hub={removing} onClose={() => setRemoving(null)} onConfirm={remove} />
      )}
    </div>
  )
}

/**
 * Confirm removing a hub, with the choice to keep or delete its blobs. Removing from
 * the whitelist stops the relay serving the hub's events, but its blobs stay
 * downloadable by hash and keep using disk, so deleting them is offered. The delete
 * removes only blobs this hub SOLELY owns (shared ones survive), and is irreversible.
 */
function RemoveConfirm({
  hub,
  onClose,
  onConfirm,
}: {
  hub: HubEntry
  onClose: () => void
  onConfirm: (coordinate: string, deleteBlobs: boolean) => Promise<void>
}) {
  const [busy, setBusy] = useState<'keep' | 'delete' | null>(null)
  const go = async (deleteBlobs: boolean) => {
    setBusy(deleteBlobs ? 'delete' : 'keep')
    try {
      await onConfirm(hub.coordinate, deleteBlobs)
    } finally {
      setBusy(null)
    }
  }
  return (
    <div className="fixed inset-0 z-40 flex items-center justify-center bg-black/60 px-4" onClick={onClose}>
      <div className="w-full max-w-sm space-y-4 rounded-xl border border-border bg-card p-5" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-start gap-3">
          <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-destructive/15">
            <Trash2 className="h-4 w-4 text-destructive" />
          </div>
          <div className="min-w-0 space-y-1">
            <h3 className="text-sm font-semibold text-neutral-100">Remove this hub?</h3>
            {hub.name && <p className="truncate text-xs font-medium text-neutral-300">{hub.name}</p>}
            <p className="break-all font-mono text-xs text-muted-foreground">{hub.d}</p>
            <p className="text-xs text-neutral-500">
              The relay stops accepting and serving this hub&apos;s events. Its blobs stay stored (and
              downloadable by hash) unless you delete them. Deleting removes only blobs this hub solely
              owns, and cannot be undone.
            </p>
          </div>
        </div>

        <div className="flex flex-col gap-2">
          <button
            onClick={() => go(false)}
            disabled={busy !== null}
            className="inline-flex items-center justify-center gap-1.5 rounded-lg border border-border px-3 py-2 text-xs font-medium text-neutral-200 hover:border-[#404040] disabled:opacity-60"
          >
            {busy === 'keep' ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}
            Remove from whitelist only (keep blobs)
          </button>
          <button
            onClick={() => go(true)}
            disabled={busy !== null}
            className="inline-flex items-center justify-center gap-1.5 rounded-lg bg-destructive px-3 py-2 text-xs font-medium text-destructive-foreground hover:opacity-90 disabled:opacity-60"
          >
            {busy === 'delete' ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
            Remove and delete its blobs
          </button>
          <button
            onClick={onClose}
            disabled={busy !== null}
            className="rounded-lg px-3 py-1.5 text-xs text-neutral-400 hover:text-neutral-200 disabled:opacity-60"
          >
            Cancel
          </button>
        </div>
      </div>
    </div>
  )
}

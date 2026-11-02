import { useCallback, useEffect, useState } from 'react'
import { Loader2, Plus, Trash2, AlertTriangle, Ban } from 'lucide-react'
import { getBlacklist, addBlacklist, removeBlacklist, type BlacklistEntry } from '../api'
import { cn, truncateMiddle, formatDate } from '../lib'
import { toast } from '../toast'

const isHash = (s: string) => /^[0-9a-f]{64}$/i.test(s.trim())

export function BlacklistPanel() {
  const [entries, setEntries] = useState<BlacklistEntry[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [hash, setHash] = useState('')
  const [reason, setReason] = useState('')
  const [adding, setAdding] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setEntries(await getBlacklist())
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load blacklist')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const add = async () => {
    const h = hash.trim().toLowerCase()
    if (!isHash(h)) {
      toast('Enter a valid 64-char sha256 hash', 'error')
      return
    }
    setAdding(true)
    try {
      await addBlacklist(h, reason.trim() || undefined)
      toast('Hash blacklisted and purged')
      setHash('')
      setReason('')
      await load()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Add failed', 'error')
    } finally {
      setAdding(false)
    }
  }

  const remove = async (h: string) => {
    try {
      await removeBlacklist(h)
      toast('Removed from blacklist')
      await load()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Remove failed', 'error')
    }
  }

  const inputCls =
    'rounded-lg border border-border bg-[#212121] px-3 py-2 text-sm text-white placeholder:text-muted-foreground focus:border-[#404040] focus:outline-none'

  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        Blacklisted hashes are purged from storage and can’t be uploaded or downloaded again — the blob
        counterpart to an event takedown. A plain delete doesn’t stick, since the same bytes can be re-uploaded.
      </p>

      {/* Add */}
      <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-3">
        <input
          value={hash}
          onChange={(e) => setHash(e.target.value)}
          placeholder="sha256 hash (64 hex chars)"
          className={cn(inputCls, 'min-w-[280px] flex-1 font-mono')}
        />
        <input
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder="Reason (optional)"
          className={cn(inputCls, 'min-w-[160px] flex-1')}
        />
        <button
          onClick={add}
          disabled={adding || !hash.trim()}
          className="inline-flex items-center gap-1.5 rounded-lg bg-destructive px-3 py-2 text-sm font-medium text-destructive-foreground hover:opacity-90 disabled:opacity-60"
        >
          {adding ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
          Blacklist
        </button>
      </div>

      {/* List */}
      <div className="rounded-lg border border-border bg-card">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <h3 className="text-sm font-semibold text-neutral-200">
            Blacklisted hashes{entries ? ` (${entries.length})` : ''}
          </h3>
          {loading && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        </div>

        {error ? (
          <div className="flex items-center gap-2 px-4 py-8 text-sm text-destructive">
            <AlertTriangle className="h-4 w-4" /> {error}
          </div>
        ) : entries && entries.length === 0 && !loading ? (
          <div className="flex flex-col items-center gap-2 px-4 py-10 text-center">
            <Ban className="h-8 w-8 text-neutral-700" />
            <p className="text-sm text-muted-foreground">Nothing blacklisted.</p>
          </div>
        ) : (
          <ul className="divide-y divide-border/60">
            {entries?.map((e) => (
              <li key={e.hash} className="flex items-center justify-between gap-3 px-4 py-2.5">
                <div className="min-w-0">
                  <div className="font-mono text-xs text-neutral-300" title={e.hash}>
                    {truncateMiddle(e.hash, 14, 10)}
                  </div>
                  <div className="text-xs text-muted-foreground">
                    {e.reason ? e.reason : <span className="text-neutral-600">no reason given</span>}
                    {e.added ? ` · ${formatDate(e.added)}` : ''}
                  </div>
                </div>
                <button
                  onClick={() => remove(e.hash)}
                  title="Remove from blacklist"
                  className="rounded-md p-1.5 text-neutral-500 transition-colors hover:bg-secondary hover:text-white"
                >
                  <Trash2 className="h-4 w-4" />
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

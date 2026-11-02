import { useCallback, useEffect, useState } from 'react'
import { Loader2, Plus, Trash2, AlertTriangle, ShieldCheck } from 'lucide-react'
import { getWhitelist, addWhitelist, removeWhitelist, type WhitelistInfo } from '../api'
import { truncateMiddle } from '../lib'
import { toast } from '../toast'

export function WhitelistTab() {
  const [info, setInfo] = useState<WhitelistInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [pubkey, setPubkey] = useState('')
  const [note, setNote] = useState('')
  const [adding, setAdding] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setInfo(await getWhitelist())
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load whitelist')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const add = async () => {
    const pk = pubkey.trim()
    if (!pk) return
    setAdding(true)
    try {
      await addWhitelist(pk, note.trim() || undefined)
      toast('Added to whitelist')
      setPubkey('')
      setNote('')
      await load()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Add failed', 'error')
    } finally {
      setAdding(false)
    }
  }

  const remove = async (pk: string) => {
    try {
      await removeWhitelist(pk)
      toast('Removed')
      await load()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Remove failed', 'error')
    }
  }

  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        Whitelisted keys upload at a raised size cap
        {info ? ` (${info.whitelisted_mb} MB vs the normal ${info.limit_mb} MB)` : ''}, and their uploaded files
        skip the download gate (no PoW), so a trusted author&apos;s files are directly downloadable
        (even from a browser). Accepts an npub or a hex pubkey.
      </p>

      {/* Add */}
      <div className="flex flex-wrap items-center gap-2 rounded-lg border border-border bg-card p-3">
        <input
          value={pubkey}
          onChange={(e) => setPubkey(e.target.value)}
          placeholder="npub1… or hex pubkey"
          className="min-w-[240px] flex-1 rounded-lg border border-border bg-[#212121] px-3 py-2 text-sm text-white placeholder:text-muted-foreground focus:border-[#404040] focus:outline-none"
        />
        <input
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="Note (optional)"
          className="min-w-[160px] flex-1 rounded-lg border border-border bg-[#212121] px-3 py-2 text-sm text-white placeholder:text-muted-foreground focus:border-[#404040] focus:outline-none"
        />
        <button
          onClick={add}
          disabled={adding || !pubkey.trim()}
          className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground hover:bg-primary-hover disabled:opacity-60"
        >
          {adding ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
          Add
        </button>
      </div>

      {/* List */}
      <div className="rounded-lg border border-border bg-card">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <h3 className="text-sm font-semibold text-neutral-200">
            Whitelisted keys{info ? ` (${info.entries.length})` : ''}
          </h3>
          {loading && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        </div>

        {error ? (
          <div className="flex items-center gap-2 px-4 py-8 text-sm text-destructive">
            <AlertTriangle className="h-4 w-4" /> {error}
          </div>
        ) : info && info.entries.length === 0 && !loading ? (
          <div className="flex flex-col items-center gap-2 px-4 py-10 text-center">
            <ShieldCheck className="h-8 w-8 text-neutral-700" />
            <p className="text-sm text-muted-foreground">No whitelisted keys yet.</p>
          </div>
        ) : (
          <ul className="divide-y divide-border/60">
            {info?.entries.map((e) => (
              <li key={e.pubkey} className="flex items-center justify-between gap-3 px-4 py-2.5">
                <div className="min-w-0">
                  <div className="font-mono text-xs text-neutral-300">{truncateMiddle(e.pubkey, 14, 10)}</div>
                  {e.note && <div className="truncate text-xs text-muted-foreground">{e.note}</div>}
                </div>
                <button
                  onClick={() => remove(e.pubkey)}
                  title="Remove"
                  className="rounded-md p-1.5 text-neutral-500 transition-colors hover:bg-destructive/15 hover:text-destructive"
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

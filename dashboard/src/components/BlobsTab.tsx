import { useCallback, useEffect, useState, type ReactNode } from 'react'
import {
  Loader2, Search, Trash2, Download, Copy, Check, ChevronLeft, ChevronRight,
  ArrowUp, ArrowDown, AlertTriangle, X, Undo2, Users,
} from 'lucide-react'
import {
  listBlobs, deleteBlob, downloadBlob, addBlacklist, releaseQuarantine, formatBytes,
  type BlobsPage, type BlobSort, type ModRef,
} from '../api'
import { npubEncode, naddrEncode } from '../nostr'
import { cn, truncateMiddle, formatDate } from '../lib'
import { toast } from '../toast'

const PER_PAGE = 50

export function BlobsTab() {
  const [data, setData] = useState<BlobsPage | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const [search, setSearch] = useState('')
  const [debounced, setDebounced] = useState('')
  const [exts, setExts] = useState<string[]>([])
  const [sort, setSort] = useState<BlobSort>('date')
  const [dir, setDir] = useState<'asc' | 'desc'>('desc')
  const [page, setPage] = useState(1)
  const [confirm, setConfirm] = useState<string | null>(null)

  useEffect(() => {
    const t = setTimeout(() => setDebounced(search.trim()), 300)
    return () => clearTimeout(t)
  }, [search])
  useEffect(() => setPage(1), [debounced, exts, sort, dir])

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setData(await listBlobs({ search: debounced, ext: exts, sort, dir, page, per: PER_PAGE }))
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load files')
    } finally {
      setLoading(false)
    }
  }, [debounced, exts, sort, dir, page])

  useEffect(() => {
    void load()
  }, [load])

  const toggleSort = (field: BlobSort) => {
    if (sort === field) setDir((d) => (d === 'asc' ? 'desc' : 'asc'))
    else {
      setSort(field)
      setDir(field === 'hash' ? 'asc' : 'desc')
    }
  }
  const toggleExt = (e: string) =>
    setExts((prev) => (prev.includes(e) ? prev.filter((x) => x !== e) : [...prev, e]))

  return (
    <div className="space-y-4">
      {/* Search + type filters */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-[220px] flex-1">
          <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search by hash…"
            className="w-full rounded-lg border border-border bg-[#212121] py-2 pl-9 pr-3 text-sm text-white placeholder:text-muted-foreground focus:border-[#404040] focus:outline-none"
          />
        </div>
        {data && data.types.length > 0 && (
          <div className="flex flex-wrap gap-1">
            {data.types.map((t) => (
              <button
                key={t}
                onClick={() => toggleExt(t)}
                className={cn(
                  'rounded-md border px-2 py-1 text-xs font-medium transition-colors',
                  exts.includes(t)
                    ? 'border-primary/40 bg-primary/10 text-primary'
                    : 'border-border text-neutral-400 hover:border-[#404040]',
                )}
              >
                .{t}
              </button>
            ))}
          </div>
        )}
      </div>

      <div className="rounded-lg border border-border bg-card">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <h3 className="text-sm font-semibold text-neutral-200">
            Stored files{data ? ` (${data.total.toLocaleString()})` : ''}
          </h3>
          {loading && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        </div>

        {error ? (
          <div className="flex items-center gap-2 px-4 py-8 text-sm text-destructive">
            <AlertTriangle className="h-4 w-4" /> {error}
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-border text-xs text-muted-foreground">
                  <SortTh label="Hash" field="hash" sort={sort} dir={dir} onSort={toggleSort} />
                  <th className="px-3 py-2 text-left font-medium">Type</th>
                  <th className="px-3 py-2 text-left font-medium">Uploader</th>
                  <SortTh label="Size" field="size" sort={sort} dir={dir} onSort={toggleSort} align="right" />
                  <SortTh label="Added" field="date" sort={sort} dir={dir} onSort={toggleSort} align="right" />
                  <th className="px-3 py-2 text-left font-medium">State</th>
                  <th className="px-3 py-2 text-left font-medium">Mod</th>
                  <th className="px-3 py-2 text-right font-medium">Actions</th>
                </tr>
              </thead>
              <tbody>
                {data?.blobs.map((b) => (
                  <tr key={b.hash} className="border-b border-border/60 last:border-0 hover:bg-[#212121]/40">
                    <td className="px-3 py-2 font-mono text-xs text-neutral-300">
                      <HashCell hash={b.hash} />
                    </td>
                    <td className="px-3 py-2 text-neutral-400">{b.ext ? `.${b.ext}` : '—'}</td>
                    <td className="px-3 py-2 font-mono text-xs">
                      <UploaderCell uploaders={b.uploaders} />
                    </td>
                    <td className="px-3 py-2 text-right tabular-nums text-neutral-300">{formatBytes(b.size)}</td>
                    <td className="px-3 py-2 text-right tabular-nums text-neutral-400">{formatDate(b.added)}</td>
                    <td className="px-3 py-2"><StateCell refs={b.refs} quarantined={b.quarantined} /></td>
                    <td className="px-3 py-2 text-neutral-400"><ModCell refs={b.refs} /></td>
                    <td className="px-3 py-2">
                      <div className="flex items-center justify-end gap-1">
                        {b.quarantined && <ReleaseButton hash={b.hash} onReleased={load} />}
                        <DownloadButton hash={b.hash} ext={b.ext} />
                        <button
                          onClick={() => setConfirm(b.hash)}
                          title="Delete"
                          className="rounded-md p-1.5 text-neutral-500 transition-colors hover:bg-destructive/15 hover:text-destructive"
                        >
                          <Trash2 className="h-4 w-4" />
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
                {data && data.blobs.length === 0 && !loading && (
                  <tr>
                    <td colSpan={8} className="px-4 py-10 text-center text-sm text-muted-foreground">
                      No files match.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        )}

        {data && data.pages > 1 && (
          <div className="flex items-center justify-between border-t border-border px-4 py-2.5 text-xs text-muted-foreground">
            <span>Page {data.page} of {data.pages}</span>
            <div className="flex gap-1">
              <button
                disabled={data.page <= 1}
                onClick={() => setPage((p) => p - 1)}
                className="rounded-md border border-border p-1 disabled:opacity-40 enabled:hover:border-[#404040]"
              >
                <ChevronLeft className="h-4 w-4" />
              </button>
              <button
                disabled={data.page >= data.pages}
                onClick={() => setPage((p) => p + 1)}
                className="rounded-md border border-border p-1 disabled:opacity-40 enabled:hover:border-[#404040]"
              >
                <ChevronRight className="h-4 w-4" />
              </button>
            </div>
          </div>
        )}
      </div>

      {confirm && (
        <DeleteConfirm
          hash={confirm}
          onClose={() => setConfirm(null)}
          onDeleted={() => {
            setConfirm(null)
            void load()
          }}
        />
      )}
    </div>
  )
}

function DownloadButton({ hash, ext }: { hash: string; ext: string }) {
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    try {
      await downloadBlob(hash, ext)
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Download failed', 'error')
    } finally {
      setBusy(false)
    }
  }
  return (
    <button
      onClick={go}
      disabled={busy}
      title="Download"
      className="rounded-md p-1.5 text-neutral-500 transition-colors hover:bg-secondary hover:text-white disabled:opacity-60"
    >
      {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Download className="h-4 w-4" />}
    </button>
  )
}

function HashCell({ hash }: { hash: string }) {
  return <Copyable full={hash} display={truncateMiddle(hash, 12, 8)} />
}

/**
 * The blob's uploaders. Shows the original (first) uploader as a copyable npub; a
 * clickable "+N" opens a modal listing every uploader, each individually copyable.
 */
function UploaderCell({ uploaders }: { uploaders?: string[] }) {
  const [open, setOpen] = useState(false)
  if (!uploaders || uploaders.length === 0) {
    return <span className="text-neutral-600" title="Uploaded before uploaders were tracked">—</span>
  }
  const npub = npubEncode(uploaders[0])
  const rest = uploaders.length - 1
  return (
    <span className="inline-flex items-center gap-1">
      <Copyable full={npub} display={truncateMiddle(npub, 10, 6)} />
      {rest > 0 && (
        <button
          onClick={() => setOpen(true)}
          title="View all uploaders"
          className="shrink-0 rounded px-1 text-neutral-500 hover:bg-secondary hover:text-neutral-200"
        >
          +{rest}
        </button>
      )}
      {open && <UploadersModal uploaders={uploaders} onClose={() => setOpen(false)} />}
    </span>
  )
}

function UploadersModal({ uploaders, onClose }: { uploaders: string[]; onClose: () => void }) {
  return (
    <div className="fixed inset-0 z-40 flex items-center justify-center bg-black/60 px-4" onClick={onClose}>
      <div className="w-full max-w-md space-y-3 rounded-xl border border-border bg-card p-5" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="inline-flex items-center gap-2 text-sm font-semibold text-neutral-100">
            <Users className="h-4 w-4 text-neutral-400" /> Uploaders ({uploaders.length})
          </h3>
          <button onClick={onClose} className="rounded-md p-1 text-neutral-500 hover:bg-secondary hover:text-white">
            <X className="h-4 w-4" />
          </button>
        </div>
        <p className="text-xs text-muted-foreground">
          Everyone who uploaded these exact bytes — the first is the original uploader.
        </p>
        <ul className="max-h-80 space-y-1 overflow-auto">
          {uploaders.map((u, i) => {
            const npub = npubEncode(u)
            return (
              <li key={u} className="flex items-center justify-between gap-2 rounded-lg border border-border/60 bg-[#1a1a1a] px-3 py-2">
                <span className="min-w-0 font-mono text-xs text-neutral-300">
                  {i === 0 && <span className="mr-1 rounded bg-secondary px-1 text-[10px] text-neutral-400">original</span>}
                  <span className="align-middle" title={npub}>{truncateMiddle(npub, 14, 10)}</span>
                </span>
                <Copyable full={npub} display="Copy" className="shrink-0 rounded-md border border-border px-2 py-1 text-xs text-neutral-400 hover:text-neutral-200" />
              </li>
            )
          })}
        </ul>
      </div>
    </div>
  )
}

function ReleaseButton({ hash, onReleased }: { hash: string; onReleased: () => void }) {
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    try {
      await releaseQuarantine(hash)
      toast('Released from quarantine')
      onReleased()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Release failed', 'error')
      setBusy(false)
    }
  }
  return (
    <button
      onClick={go}
      disabled={busy}
      title="Release from quarantine (make downloadable again)"
      className="rounded-md p-1.5 text-neutral-500 transition-colors hover:bg-success/15 hover:text-success disabled:opacity-60"
    >
      {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Undo2 className="h-4 w-4" />}
    </button>
  )
}

/**
 * Claimed = a mod stored on THIS relay references the blob. Unclaimed doesn't mean
 * orphaned — the mod may live on another relay — so the tooltip says "verify before
 * removing". Labelling only; nothing here deletes or gates anything.
 */
function StateCell({ refs, quarantined }: { refs?: ModRef[]; quarantined?: boolean }) {
  if (quarantined) {
    return (
      <span
        className="rounded-full bg-warning/15 px-2 py-0.5 text-[10px] font-medium text-warning"
        title="Withheld from download — the last uploader retracted it. Reversible: a re-upload or Release restores it."
      >
        Quarantined
      </span>
    )
  }
  if (refs && refs.length > 0) {
    return (
      <span
        className="rounded-full bg-success/15 px-2 py-0.5 text-[10px] font-medium text-success"
        title={`Referenced by ${refs.length} mod${refs.length > 1 ? 's' : ''} on this relay`}
      >
        Claimed
      </span>
    )
  }
  return (
    <span
      className="rounded-full bg-secondary px-2 py-0.5 text-[10px] font-medium text-neutral-400"
      title="No mod stored on this relay references this file. It may still belong to a mod hosted elsewhere — verify before removing."
    >
      Unclaimed
    </span>
  )
}

/**
 * The mod(s) referencing a blob. Shows the first mod's title (falling back to its
 * naddr) as a click-to-copy button that copies the naddr; "+N" marks any others,
 * with all of them listed in the hover tooltip.
 */
function ModCell({ refs }: { refs?: ModRef[] }) {
  if (!refs || refs.length === 0) return <span className="text-neutral-600">—</span>
  const first = refs[0]
  const naddr = naddrEncode(first.coord)
  const label = first.title.trim() || truncateMiddle(naddr, 12, 8)
  const rest = refs.length - 1
  const tip = refs
    .map((r) => {
      const a = naddrEncode(r.coord)
      return r.title.trim() ? `${r.title} — ${a}` : a
    })
    .join('\n')
  return (
    <span className="inline-flex max-w-[220px] items-center gap-1">
      <Copyable full={naddr} display={<span className="truncate">{label}</span>} className="max-w-[170px]" />
      {rest > 0 && (
        <span className="shrink-0 text-neutral-600" title={tip}>
          +{rest}
        </span>
      )}
    </span>
  )
}

/** Text with a click-to-copy button; copies the full value, shows `display`. */
function Copyable({ full, display, className }: { full: string; display: ReactNode; className?: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => {
        navigator.clipboard.writeText(full)
        setCopied(true)
        setTimeout(() => setCopied(false), 1500)
      }}
      className={cn('inline-flex min-w-0 items-center gap-1.5 hover:text-white', className)}
      title={copied ? 'Copied' : `Copy — ${full}`}
    >
      {display}
      {copied ? <Check className="h-3 w-3 shrink-0 text-success" /> : <Copy className="h-3 w-3 shrink-0 text-neutral-600" />}
    </button>
  )
}

function SortTh({
  label, field, sort, dir, onSort, align = 'left',
}: {
  label: string
  field: BlobSort
  sort: BlobSort
  dir: 'asc' | 'desc'
  onSort: (f: BlobSort) => void
  align?: 'left' | 'right'
}) {
  const active = sort === field
  return (
    <th className={cn('px-3 py-2 font-medium', align === 'right' ? 'text-right' : 'text-left')}>
      <button
        onClick={() => onSort(field)}
        className={cn('inline-flex items-center gap-1 hover:text-neutral-200', active && 'text-neutral-200')}
      >
        {label}
        {active && (dir === 'asc' ? <ArrowUp className="h-3 w-3" /> : <ArrowDown className="h-3 w-3" />)}
      </button>
    </th>
  )
}

function DeleteConfirm({ hash, onClose, onDeleted }: { hash: string; onClose: () => void; onDeleted: () => void }) {
  const [busy, setBusy] = useState(false)
  const [blacklist, setBlacklist] = useState(false)
  const [reason, setReason] = useState('')
  const del = async () => {
    setBusy(true)
    try {
      if (blacklist) {
        await addBlacklist(hash, reason.trim() || undefined)
        toast('File deleted and blacklisted')
      } else {
        await deleteBlob(hash)
        toast('File deleted')
      }
      onDeleted()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Delete failed', 'error')
      setBusy(false)
    }
  }
  return (
    <div className="fixed inset-0 z-40 flex items-center justify-center bg-black/60 px-4" onClick={onClose}>
      <div
        className="w-full max-w-sm space-y-4 rounded-xl border border-border bg-card p-5"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-start gap-3">
          <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-destructive/15">
            <Trash2 className="h-4 w-4 text-destructive" />
          </div>
          <div className="space-y-1">
            <h3 className="text-sm font-semibold text-neutral-100">Delete this file?</h3>
            <p className="break-all font-mono text-xs text-muted-foreground">{truncateMiddle(hash, 16, 10)}</p>
            <p className="text-xs text-neutral-500">
              This removes the blob from storage permanently. Any mod referencing it will lose the file.
            </p>
          </div>
        </div>

        {/* Optional: also blacklist so the same bytes can't be re-uploaded. */}
        <div className="space-y-2 rounded-lg border border-border/60 bg-[#1a1a1a] p-3">
          <label className="flex cursor-pointer items-start gap-2 text-xs text-neutral-300">
            <input
              type="checkbox"
              checked={blacklist}
              onChange={(e) => setBlacklist(e.target.checked)}
              className="mt-0.5 accent-[hsl(263_70%_58%)]"
            />
            <span>
              Also blacklist this hash
              <span className="block text-neutral-500">Blocks re-upload of the same bytes — the takedown sticks.</span>
            </span>
          </label>
          {blacklist && (
            <input
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Reason (optional)"
              className="w-full rounded-lg border border-border bg-[#212121] px-3 py-1.5 text-xs text-white placeholder:text-muted-foreground focus:border-[#404040] focus:outline-none"
            />
          )}
        </div>

        <div className="flex justify-end gap-2">
          <button
            onClick={onClose}
            disabled={busy}
            className="rounded-lg border border-border px-3 py-1.5 text-xs text-neutral-300 hover:border-[#404040]"
          >
            Cancel
          </button>
          <button
            onClick={del}
            disabled={busy}
            className="inline-flex items-center gap-1.5 rounded-lg bg-destructive px-3 py-1.5 text-xs font-medium text-destructive-foreground hover:opacity-90 disabled:opacity-60"
          >
            {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Trash2 className="h-3.5 w-3.5" />}
            {blacklist ? 'Delete + blacklist' : 'Delete'}
          </button>
        </div>
      </div>
    </div>
  )
}

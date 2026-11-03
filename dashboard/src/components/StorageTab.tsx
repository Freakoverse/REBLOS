import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import { Loader2, AlertTriangle, Database, ArrowRight, HardDrive } from 'lucide-react'
import { getStorage, startStorageMigration, formatBytes, type StorageInfo, type BackendDesc } from '../api'
import { toast } from '../toast'

function backendLabel(b: BackendDesc): string {
  if (b.kind === 'disk') return `disk · ${b.path || '?'}`
  const where = [b.endpoint, b.bucket].filter(Boolean).join(' / ')
  return `${b.kind}${where ? ` · ${where}` : ''}`
}

export function StorageTab() {
  const [info, setInfo] = useState<StorageInfo | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [migrating, setMigrating] = useState(false)
  const pollRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const load = useCallback(async () => {
    try {
      const s = await getStorage()
      setInfo(s)
      setError(null)
      return s
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load storage status')
      return null
    } finally {
      setLoading(false)
    }
  }, [])

  // Poll while a migration is running, then stop.
  const schedulePoll = useCallback(() => {
    if (pollRef.current) clearTimeout(pollRef.current)
    pollRef.current = setTimeout(async () => {
      const s = await load()
      if (s?.migration.running) schedulePoll()
    }, 1500)
  }, [load])

  useEffect(() => {
    void load().then((s) => {
      if (s?.migration.running) schedulePoll()
    })
    return () => {
      if (pollRef.current) clearTimeout(pollRef.current)
    }
  }, [load, schedulePoll])

  const migrate = async () => {
    setMigrating(true)
    try {
      await startStorageMigration()
      toast('Migration started')
      await load()
      schedulePoll()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Could not start migration', 'error')
    } finally {
      setMigrating(false)
    }
  }

  if (loading) {
    return (
      <div className="flex items-center gap-2 px-1 py-10 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" /> Loading storage status…
      </div>
    )
  }
  if (error || !info) {
    return (
      <div className="flex items-center gap-2 px-1 py-8 text-sm text-destructive">
        <AlertTriangle className="h-4 w-4" /> {error || 'No data'}
      </div>
    )
  }

  const mig = info.migration
  const p = mig.progress

  return (
    <div className="space-y-4">
      <p className="text-xs text-muted-foreground">
        Where this node stores blobs. To re-route storage without losing anything: set a new primary
        backend in <span className="font-mono">config.yml</span> and keep the old one as the
        <span className="font-mono"> fallback</span> (reads fall through to it), restart, then run the
        migration below to copy the backlog into the new primary. When it finishes, remove the fallback.
      </p>

      {/* Backends */}
      <div className="rounded-lg border border-border bg-card">
        <div className="border-b border-border px-4 py-2.5">
          <h3 className="text-sm font-semibold text-neutral-200">Backends</h3>
        </div>
        <div className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
          <span className="inline-flex items-center gap-1.5 rounded-md bg-[#212121] px-2.5 py-1.5 font-mono text-xs text-neutral-200">
            <Database className="h-3.5 w-3.5 text-primary" /> {backendLabel(info.primary)}
          </span>
          <span className="text-[10px] uppercase tracking-wide text-muted-foreground">primary (writes)</span>
          {info.fallback && (
            <>
              <ArrowRight className="h-4 w-4 text-neutral-600" />
              <span className="inline-flex items-center gap-1.5 rounded-md bg-[#212121] px-2.5 py-1.5 font-mono text-xs text-neutral-400">
                <Database className="h-3.5 w-3.5" /> {backendLabel(info.fallback)}
              </span>
              <span className="text-[10px] uppercase tracking-wide text-muted-foreground">fallback (read-only)</span>
            </>
          )}
        </div>
      </div>

      {/* Stats */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
        <Stat label="Blobs stored" value={info.blob_count.toLocaleString()} />
        <Stat label="Total size" value={formatBytes(info.blob_bytes)} />
        <Stat
          label="Node disk free"
          value={info.disk.available && info.disk.free_mb != null ? `${(info.disk.free_mb / 1024).toFixed(1)} GB` : 'n/a'}
          icon={<HardDrive className="h-3.5 w-3.5" />}
        />
      </div>

      {/* Migration */}
      <div className="rounded-lg border border-border bg-card">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <h3 className="text-sm font-semibold text-neutral-200">Migration</h3>
          {mig.running && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        </div>
        <div className="space-y-3 px-4 py-3">
          {!info.fallback ? (
            <p className="text-sm text-muted-foreground">
              No fallback backend configured. Add a <span className="font-mono">fallback</span> block to
              <span className="font-mono"> config.yml</span> and restart to migrate off an old backend.
            </p>
          ) : (
            <>
              <div className="flex items-center gap-3">
                <button
                  onClick={migrate}
                  disabled={migrating || mig.running}
                  className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground hover:bg-primary-hover disabled:opacity-60"
                >
                  {migrating || mig.running ? <Loader2 className="h-4 w-4 animate-spin" /> : <ArrowRight className="h-4 w-4" />}
                  {mig.running ? 'Migrating…' : 'Migrate fallback into primary'}
                </button>
                <span className="text-xs text-muted-foreground">Idempotent: safe to re-run.</span>
              </div>

              {(mig.running || p.done || p.total > 0) && (
                <div className="space-y-1.5">
                  <div className="h-2 overflow-hidden rounded-full bg-[#212121]">
                    <div
                      className="h-full bg-primary transition-all"
                      style={{ width: pct(p.copied + p.skipped, p.total) }}
                    />
                  </div>
                  <div className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground">
                    <span>{p.copied + p.skipped}/{p.total} processed</span>
                    <span className="text-emerald-400">{p.copied} copied</span>
                    <span>{p.skipped} already present</span>
                    {p.failed > 0 && <span className="text-destructive">{p.failed} failed</span>}
                    {p.done && !mig.running && <span className="text-neutral-300">done</span>}
                  </div>
                  {p.err && <p className="text-xs text-destructive">{p.err}</p>}
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}

function Stat({ label, value, icon }: { label: string; value: string; icon?: ReactNode }) {
  return (
    <div className="rounded-lg border border-border bg-card px-4 py-3">
      <div className="flex items-center gap-1.5 text-[11px] uppercase tracking-wide text-muted-foreground">
        {icon}
        {label}
      </div>
      <div className="mt-1 text-lg font-semibold text-neutral-100">{value}</div>
    </div>
  )
}

function pct(done: number, total: number): string {
  if (total <= 0) return '0%'
  return `${Math.min(100, Math.round((done / total) * 100))}%`
}

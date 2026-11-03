import { useEffect, useState } from 'react'
import {
  Loader2, Search, AlertTriangle, Plus, X, ChevronDown, ChevronRight,
  Copy, Check, Filter as FilterIcon, List, SlidersHorizontal, Info,
} from 'lucide-react'
import { queryEvents, getRelayConfig, type EventsPage, type RelayEvent, type RelayConfig } from '../api'
import { npubEncode, naddrEncode } from '../nostr'
import { cn, truncateMiddle, formatDate } from '../lib'
import { ConfigSection, ConfigRow } from './ConfigList'

/** Kinds the relay stores, with human labels. Unknown kinds fall back to "kind N". */
const KIND_LABELS: Record<number, string> = {
  36942: 'Hub',
  36943: 'Message',
  26943: 'Message edit hint',
  26950: 'Typing indicator',
  36944: 'Join request',
  36945: 'Pin list',
  36946: 'Voice host',
  36947: 'Voice presence',
  36948: 'Report',
  36949: 'Hide message',
  1067: 'Poll',
  1017: 'Poll vote',
  7: 'Reaction',
}
// Quick-filter chips: a useful subset of the kinds above; any other kind via the custom input.
const KIND_OPTIONS = [36942, 36943, 7, 1067, 36948, 36944].map((k) => ({ kind: k, label: KIND_LABELS[k] }))
const kindName = (k: number) => KIND_LABELS[k] ?? `kind ${k}`

type Sub = 'general' | 'settings'

export function RelayTab() {
  const [sub, setSub] = useState<Sub>('general')
  return (
    <div className="space-y-4">
      <div className="flex gap-1">
        <SubTab active={sub === 'general'} onClick={() => setSub('general')} icon={List} label="General" />
        <SubTab active={sub === 'settings'} onClick={() => setSub('settings')} icon={SlidersHorizontal} label="Settings" />
      </div>
      {sub === 'general' ? <RelayBrowse /> : <RelaySettings />}
    </div>
  )
}

function SubTab({ active, onClick, icon: Icon, label }: { active: boolean; onClick: () => void; icon: typeof List; label: string }) {
  return (
    <button
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-xs font-medium transition-colors',
        active ? 'bg-secondary text-white' : 'text-muted-foreground hover:text-neutral-300',
      )}
    >
      <Icon size={14} /> {label}
    </button>
  )
}

// ─── General: event browser ─────────────────────────────────────────

function RelayBrowse() {
  const [kinds, setKinds] = useState<number[]>([])
  const [customKind, setCustomKind] = useState('')
  const [author, setAuthor] = useState('')
  const [tags, setTags] = useState<{ name: string; value: string }[]>([{ name: '', value: '' }])
  const [since, setSince] = useState('')
  const [until, setUntil] = useState('')
  const [limit, setLimit] = useState('100')
  const [search, setSearch] = useState('')

  const [data, setData] = useState<EventsPage | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const toUnix = (local: string): number | undefined => {
    if (!local) return undefined
    const ms = new Date(local).getTime()
    return Number.isFinite(ms) ? Math.floor(ms / 1000) : undefined
  }

  const run = async () => {
    setLoading(true)
    setError(null)
    try {
      setData(
        await queryEvents({
          kinds: kinds.length ? kinds : undefined,
          author: author.trim() || undefined,
          tags: tags.filter((t) => t.name.trim() && t.value.trim()),
          since: toUnix(since),
          until: toUnix(until),
          limit: Math.max(1, Number(limit) || 100),
          search: search.trim() || undefined,
        }),
      )
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Query failed')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    void run()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const reset = () => {
    setKinds([])
    setCustomKind('')
    setAuthor('')
    setTags([{ name: '', value: '' }])
    setSince('')
    setUntil('')
    setLimit('100')
    setSearch('')
  }

  const toggleKind = (k: number) =>
    setKinds((prev) => (prev.includes(k) ? prev.filter((x) => x !== k) : [...prev, k]))
  const addCustomKind = () => {
    const k = parseInt(customKind, 10)
    if (Number.isInteger(k) && k >= 0 && !kinds.includes(k)) setKinds((prev) => [...prev, k])
    setCustomKind('')
  }
  const setTag = (i: number, field: 'name' | 'value', v: string) =>
    setTags((prev) => prev.map((t, j) => (j === i ? { ...t, [field]: v } : t)))
  const addTag = () => setTags((prev) => [...prev, { name: '', value: '' }])
  const removeTag = (i: number) =>
    setTags((prev) => (prev.length === 1 ? [{ name: '', value: '' }] : prev.filter((_, j) => j !== i)))

  const customKinds = kinds.filter((k) => !(k in KIND_LABELS))

  return (
    <div className="space-y-4">
      {/* Filter bar */}
      <div className="space-y-3 rounded-lg border border-border bg-card p-4">
        {/* Search */}
        <div className="relative">
          <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && run()}
            placeholder="Search naddr, npub, or any text in the event…"
            className={cn(inputCls, 'w-full pl-9')}
          />
        </div>

        {/* Kinds */}
        <div className="flex flex-wrap items-center gap-1">
          {KIND_OPTIONS.map(({ kind, label }) => (
            <button
              key={kind}
              onClick={() => toggleKind(kind)}
              className={cn('rounded-md border px-2 py-1 text-xs font-medium transition-colors', chipCls(kinds.includes(kind)))}
            >
              {label}
            </button>
          ))}
          {customKinds.map((k) => (
            <button
              key={k}
              onClick={() => toggleKind(k)}
              className={cn('inline-flex items-center gap-1 rounded-md border px-2 py-1 text-xs font-medium', chipCls(true))}
              title="Custom kind — click to remove"
            >
              kind {k} <X className="h-3 w-3" />
            </button>
          ))}
          <div className="flex items-center gap-1">
            <input
              type="number"
              min={0}
              value={customKind}
              onChange={(e) => setCustomKind(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && (e.preventDefault(), addCustomKind())}
              placeholder="custom #"
              className={cn(inputCls, 'w-24 py-1')}
            />
            <button
              onClick={addCustomKind}
              disabled={!customKind.trim()}
              className="rounded-md border border-border p-1 text-neutral-400 hover:border-[#404040] hover:text-neutral-200 disabled:opacity-40"
              title="Add custom kind"
            >
              <Plus className="h-4 w-4" />
            </button>
          </div>
        </div>

        {/* Author + limit */}
        <div className="flex flex-wrap gap-2">
          <input
            value={author}
            onChange={(e) => setAuthor(e.target.value)}
            placeholder="Author npub or hex"
            className={cn(inputCls, 'min-w-[240px] flex-1')}
          />
          <div className="flex items-center gap-2">
            <label className="text-xs text-muted-foreground">Limit</label>
            <input type="number" min={1} value={limit} onChange={(e) => setLimit(e.target.value)} className={cn(inputCls, 'w-24 text-right')} />
          </div>
        </div>

        {/* Date range */}
        <div className="flex flex-wrap gap-2">
          <div className="flex items-center gap-2">
            <label className="w-10 text-xs text-muted-foreground">Since</label>
            <input type="datetime-local" value={since} onChange={(e) => setSince(e.target.value)} className={cn(inputCls, 'text-neutral-300')} />
          </div>
          <div className="flex items-center gap-2">
            <label className="w-10 text-xs text-muted-foreground">Until</label>
            <input type="datetime-local" value={until} onChange={(e) => setUntil(e.target.value)} className={cn(inputCls, 'text-neutral-300')} />
          </div>
        </div>

        {/* Tag filters */}
        <div className="space-y-2">
          {tags.map((t, i) => (
            <div key={i} className="flex items-center gap-2">
              <input value={t.name} onChange={(e) => setTag(i, 'name', e.target.value)} placeholder="tag (e.g. t, g, d)" className={cn(inputCls, 'w-40')} />
              <input
                value={t.value}
                onChange={(e) => setTag(i, 'value', e.target.value)}
                onKeyDown={(e) => e.key === 'Enter' && run()}
                placeholder="value"
                className={cn(inputCls, 'flex-1')}
              />
              <button onClick={() => removeTag(i)} title="Remove tag filter" className="rounded-md p-2 text-neutral-500 hover:bg-secondary hover:text-white">
                <X className="h-4 w-4" />
              </button>
            </div>
          ))}
          <button onClick={addTag} className="inline-flex items-center gap-1 text-xs text-neutral-400 hover:text-neutral-200">
            <Plus className="h-3.5 w-3.5" /> Add tag filter
          </button>
        </div>

        {/* Actions */}
        <div className="flex items-center justify-between gap-3 border-t border-border/60 pt-3">
          <button onClick={reset} className="text-xs text-neutral-400 hover:text-neutral-200">
            Reset filters
          </button>
          <button
            onClick={run}
            disabled={loading}
            className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary-hover disabled:opacity-60"
          >
            {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : <FilterIcon className="h-4 w-4" />}
            Search
          </button>
        </div>
      </div>

      {/* Results */}
      <div className="rounded-lg border border-border bg-card">
        <div className="flex items-center justify-between border-b border-border px-4 py-2.5">
          <h3 className="text-sm font-semibold text-neutral-200">Events{data ? ` (${data.count})` : ''}</h3>
          {loading && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        </div>

        {error ? (
          <div className="flex items-center gap-2 px-4 py-8 text-sm text-destructive">
            <AlertTriangle className="h-4 w-4" /> {error}
          </div>
        ) : data && data.events.length === 0 && !loading ? (
          <div className="px-4 py-10 text-center text-sm text-muted-foreground">No events match.</div>
        ) : (
          <ul className="divide-y divide-border/60">{data?.events.map((e) => <EventRow key={e.id} evt={e} />)}</ul>
        )}

        {data?.truncated && (
          <div className="border-t border-border px-4 py-2.5 text-xs text-warning">
            Showing the first {data.count} matches (scanned {data.scanned.toLocaleString()}). Narrow the filters or raise the limit to see more.
          </div>
        )}
      </div>
    </div>
  )
}

const inputCls =
  'rounded-lg border border-border bg-[#212121] px-3 py-2 text-sm text-white placeholder:text-muted-foreground focus:border-[#404040] focus:outline-none'
const chipCls = (active: boolean) =>
  active ? 'border-primary/40 bg-primary/10 text-primary' : 'border-border text-neutral-400 hover:border-[#404040]'

function EventRow({ evt }: { evt: RelayEvent }) {
  const [open, setOpen] = useState(false)
  const dTag = evt.tags.find((t) => t[0] === 'd')?.[1]
  const title = evt.tags.find((t) => t[0] === 'title')?.[1]
  const naddr = dTag ? naddrEncode(`${evt.kind}:${evt.pubkey}:${dTag}`) : ''
  const preview = (title || evt.content || '').replace(/\s+/g, ' ').trim()

  return (
    <li className="px-4 py-2.5">
      <div className="flex items-center gap-3">
        <button onClick={() => setOpen((v) => !v)} className="shrink-0 text-neutral-500 hover:text-white">
          {open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
        </button>
        <span className="shrink-0 rounded-full bg-secondary px-2 py-0.5 text-[10px] font-medium text-neutral-300">{kindName(evt.kind)}</span>
        <span className="min-w-0 flex-1 truncate text-sm text-neutral-300" title={preview}>
          {preview || <span className="text-neutral-600">(no title)</span>}
        </span>
        <span className="hidden shrink-0 font-mono text-xs text-muted-foreground sm:inline" title={npubEncode(evt.pubkey)}>
          {truncateMiddle(npubEncode(evt.pubkey), 8, 4)}
        </span>
        <span className="shrink-0 text-xs text-muted-foreground">{formatDate(evt.created_at)}</span>
      </div>

      {open && (
        <div className="mt-2 space-y-2 pl-7">
          <div className="flex flex-wrap gap-1.5">
            {naddr && <CopyBtn label="Copy naddr" value={naddr} />}
            <CopyBtn label="Copy npub" value={npubEncode(evt.pubkey)} />
            <CopyBtn label="Copy ID" value={evt.id} />
            <CopyBtn label="Copy JSON" value={JSON.stringify(evt, null, 2)} />
          </div>
          <pre className="max-h-80 overflow-auto rounded-lg border border-border bg-[#1a1a1a] p-3 text-xs text-neutral-300">
            {JSON.stringify(evt, null, 2)}
          </pre>
        </div>
      )}
    </li>
  )
}

function CopyBtn({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={() => {
        navigator.clipboard.writeText(value)
        setCopied(true)
        setTimeout(() => setCopied(false), 1500)
      }}
      className="inline-flex items-center gap-1 rounded-md border border-border px-2 py-1 text-xs text-neutral-400 hover:border-[#404040] hover:text-neutral-200"
    >
      {copied ? <Check className="h-3 w-3 text-success" /> : <Copy className="h-3 w-3" />}
      {label}
    </button>
  )
}

// ─── Settings: read-only config snapshot ────────────────────────────

function RelaySettings() {
  const [cfg, setCfg] = useState<RelayConfig | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    getRelayConfig()
      .then(setCfg)
      .catch((e) => setError(e instanceof Error ? e.message : 'Failed to load config'))
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <div className="flex justify-center py-10"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>
  if (error) return <div className="flex items-center gap-2 rounded-lg border border-border bg-card px-4 py-8 text-sm text-destructive"><AlertTriangle className="h-4 w-4" /> {error}</div>
  if (!cfg) return null

  const bits = (n: number) => (n > 0 ? `${n} bits` : 'Off (0)')

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-2 rounded-lg border border-border bg-card p-3 text-xs text-muted-foreground">
        <Info className="mt-0.5 h-4 w-4 shrink-0 text-neutral-500" /> Read-only — the relay’s event policy, set in the node’s config file. Blob upload &amp; download settings live under the <span className="text-neutral-300">Settings</span> tab.
      </div>

      <ConfigSection title="Relay policy">
        <ConfigRow label="Kind allowlist" value={cfg.relay.accept_all_kinds ? 'Disabled: accepts any kind (fork mode)' : 'Hub-scoped (NIP-CHAT)'} />
        {!cfg.relay.accept_all_kinds && (
          <ConfigRow
            label="Accepted kinds"
            value={cfg.relay.accepted_kinds.map((k) => `${k.label} (${k.kind})`).join(', ')}
            stack
          />
        )}
        <ConfigRow label="Min event PoW" value={bits(cfg.relay.min_event_pow)} hint="NIP-13 bits required on accepted events" />
        <ConfigRow label="Admin key configured" value={cfg.relay.admin_configured ? 'Yes' : 'No'} />
      </ConfigSection>
    </div>
  )
}

import { useEffect, useState } from 'react'
import { Loader2, HardDrive, ShieldCheck, CreditCard, Info, Save, RotateCcw } from 'lucide-react'
import { getWhitelist, setUploadCaps, resetUploadCaps, getRelayConfig, getNodeStats, type RelayConfig, type NodeStats } from '../api'
import { ConfigSection, ConfigRow } from './ConfigList'
import { toast } from '../toast'

const fmtSize = (mb?: number): string => {
  if (mb == null) return '—'
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${mb.toLocaleString()} MB`
}

/**
 * Editable per-upload size caps (normal + whitelisted), plus a coming-soon row.
 * Reads the live values via getWhitelist (which returns the current caps) and
 * writes them through /admin/upload-caps. The node validates and persists; the
 * change takes effect on the next upload.
 */
export function SettingsTab() {
  const [loading, setLoading] = useState(true)
  const [limitMb, setLimitMb] = useState('')
  const [whitelistedMb, setWhitelistedMb] = useState('')
  const [defaults, setDefaults] = useState<{ limit: number; whitelisted: number } | null>(null)
  const [cfg, setCfg] = useState<RelayConfig | null>(null)
  const [stats, setStats] = useState<NodeStats | null>(null)
  const [saving, setSaving] = useState(false)
  const [resetting, setResetting] = useState(false)

  const load = () => {
    setLoading(true)
    getWhitelist()
      .then((info) => {
        setLimitMb(String(info.limit_mb))
        setWhitelistedMb(String(info.whitelisted_mb))
        if (info.default_limit_mb != null && info.default_whitelisted_mb != null) {
          setDefaults({ limit: info.default_limit_mb, whitelisted: info.default_whitelisted_mb })
        }
      })
      .catch(() => {})
      .finally(() => setLoading(false))
  }
  useEffect(load, [])
  // Read-only gate + rate-limit config (config-file values), shown below the caps.
  useEffect(() => {
    getRelayConfig().then(setCfg).catch(() => {})
    getNodeStats().then(setStats).catch(() => {})
  }, [])

  const limit = Number(limitMb)
  const wl = Number(whitelistedMb)
  const valid =
    Number.isInteger(limit) && limit >= 1 && Number.isInteger(wl) && wl >= limit
  const atDefaults = defaults != null && limit === defaults.limit && wl === defaults.whitelisted

  const save = async () => {
    if (!valid) return
    setSaving(true)
    try {
      await setUploadCaps(limit, wl)
      toast('Upload caps updated')
      load()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Save failed', 'error')
    } finally {
      setSaving(false)
    }
  }

  const reset = async () => {
    setResetting(true)
    try {
      await resetUploadCaps()
      toast('Restored config defaults')
      load()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Reset failed', 'error')
    } finally {
      setResetting(false)
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start gap-2 rounded-lg border border-border bg-card p-3 text-xs text-muted-foreground">
        <Info className="mt-0.5 h-4 w-4 shrink-0 text-neutral-500" />
        <p>
          Changes take effect on the next upload — an upload already in progress isn’t affected. The
          whitelisted cap must be at least the max upload size.
        </p>
      </div>

      <div className="space-y-3 rounded-lg border border-border bg-card p-4">
        <NumberRow
          icon={HardDrive}
          title="Max upload size"
          hint="Default cap for any uploader."
          value={limitMb}
          onChange={setLimitMb}
          loading={loading}
        />
        <NumberRow
          icon={ShieldCheck}
          title="Whitelisted cap"
          hint="Raised cap for keys on the whitelist."
          value={whitelistedMb}
          onChange={setWhitelistedMb}
          loading={loading}
        />
        <div className="flex items-center justify-between gap-3 border-t border-border/60 pt-3">
          <button
            onClick={reset}
            disabled={resetting || saving || loading || atDefaults}
            title={
              defaults
                ? `Restore the node's configured caps (${defaults.limit} / ${defaults.whitelisted} MB)`
                : "Restore the node's configured caps"
            }
            className="inline-flex items-center gap-1.5 rounded-lg px-2 py-1.5 text-xs text-neutral-400 hover:text-neutral-200 disabled:opacity-40 disabled:hover:text-neutral-400"
          >
            {resetting ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RotateCcw className="h-3.5 w-3.5" />}
            Reset to default{defaults ? ` (${defaults.limit} / ${defaults.whitelisted} MB)` : ''}
          </button>
          <div className="flex items-center gap-3">
            {!valid && !loading && (
              <span className="text-xs text-destructive">Whitelisted cap must be ≥ the max upload size, and both ≥ 1.</span>
            )}
            <button
              onClick={save}
              disabled={saving || loading || !valid}
              className="inline-flex items-center gap-1.5 rounded-lg bg-primary px-3 py-2 text-sm font-medium text-primary-foreground hover:bg-primary-hover disabled:opacity-60"
            >
              {saving ? <Loader2 className="h-4 w-4 animate-spin" /> : <Save className="h-4 w-4" />}
              Save
            </button>
          </div>
        </div>
      </div>

      {stats && <NodeStorage stats={stats} />}

      <Row
        icon={CreditCard}
        title="Subscriber tiers"
        value="Coming soon"
        hint="Per-subscriber limits — planned, not yet available."
        muted
      />

      {cfg && (
        <>
          <ConfigSection title="Download gate">
            <ConfigRow label="Proof-of-work" value={bits(cfg.download_gate.pow_difficulty)} hint="BUD-POW leading-zero bits on downloads" />
            <ConfigRow label="Challenge TTL" value={`${cfg.download_gate.challenge_ttl_sec}s`} />
            <ConfigRow label="Trusted IP header" value={cfg.download_gate.trusted_ip_header || 'socket IP (none set)'} />
          </ConfigSection>

          <ConfigSection title="Upload & rate limits">
            <ConfigRow label="Max concurrent uploads" value={String(cfg.upload.max_concurrent)} />
            <ConfigRow label="Min upload speed" value={cfg.upload.min_upload_rate_kbps > 0 ? `${cfg.upload.min_upload_rate_kbps} KB/s` : 'Off'} hint="Uploads below this over a 5s window are aborted" />
            <ConfigRow label="Idle timeout" value={`${cfg.upload.idle_timeout_sec}s`} hint="Abort an upload after this long with no data" />
            <ConfigRow label="Upload auth PoW" value={bits(cfg.upload.min_pow)} hint="NIP-13 bits on the kind-24242 upload auth" />
            <ConfigRow label="Min free disk" value={`${cfg.upload.min_free_disk_mb.toLocaleString()} MB`} hint="Refuse uploads below this free space" />
            <ConfigRow label="Allowed types" value={cfg.upload.allowed_types.map((t) => (t === '*' ? 'any' : `.${t}`)).join(', ')} />
          </ConfigSection>
          <p className="px-1 text-xs text-muted-foreground">
            These are read-only, set in the node’s config file. The size caps above are the editable ones.
          </p>
        </>
      )}
    </div>
  )
}

const bits = (n: number) => (n > 0 ? `${n} bits` : 'Off (0)')

/** The node's own disk (its data-dir volume) — total/used/free, not R2/S3. */
function NodeStorage({ stats }: { stats: NodeStats }) {
  const { disk, data_dir_mb } = stats
  const pct =
    disk.available && disk.total_mb && disk.total_mb > 0
      ? Math.min(100, Math.round(((disk.used_mb ?? 0) / disk.total_mb) * 100))
      : 0
  const near = pct >= 90
  return (
    <ConfigSection title="Node storage">
      {disk.available ? (
        <div className="space-y-2 px-4 py-3">
          <div className="flex items-center justify-between text-xs">
            <span className="text-neutral-300">{fmtSize(disk.used_mb)} used</span>
            <span className="text-muted-foreground">{fmtSize(disk.total_mb)} total</span>
          </div>
          <div className="h-2 overflow-hidden rounded-full bg-secondary">
            <div className={near ? 'h-full bg-destructive' : 'h-full bg-primary'} style={{ width: `${pct}%` }} />
          </div>
          <div className="text-xs text-muted-foreground">
            {fmtSize(disk.free_mb)} free · the node’s own disk, not the R2/S3 blob store
          </div>
        </div>
      ) : (
        <ConfigRow label="Disk usage" value="Unavailable" hint="Not reported on this platform" />
      )}
      <ConfigRow
        label="Node footprint"
        value={fmtSize(data_dir_mb)}
        hint="Event store + metadata files in the data dir"
      />
    </ConfigSection>
  )
}

function NumberRow({
  icon: Icon, title, hint, value, onChange, loading,
}: {
  icon: typeof HardDrive
  title: string
  hint: string
  value: string
  onChange: (v: string) => void
  loading: boolean
}) {
  return (
    <div className="flex items-center justify-between gap-4">
      <div className="flex items-center gap-3">
        <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-secondary">
          <Icon className="h-4 w-4 text-neutral-400" />
        </div>
        <div>
          <div className="text-sm font-medium text-neutral-200">{title}</div>
          <div className="text-xs text-muted-foreground">{hint}</div>
        </div>
      </div>
      <div className="flex items-center gap-2">
        {loading ? (
          <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
        ) : (
          <input
            type="number"
            min={1}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            className="w-24 rounded-lg border border-border bg-[#212121] px-3 py-1.5 text-right text-sm text-white focus:border-[#404040] focus:outline-none"
          />
        )}
        <span className="w-6 text-xs text-muted-foreground">MB</span>
      </div>
    </div>
  )
}

function Row({
  icon: Icon, title, value, hint, muted,
}: {
  icon: typeof HardDrive
  title: string
  value: string
  hint: string
  muted?: boolean
}) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-lg border border-border bg-card p-4">
      <div className="flex items-center gap-3">
        <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-secondary">
          <Icon className="h-4 w-4 text-neutral-400" />
        </div>
        <div>
          <div className="text-sm font-medium text-neutral-200">{title}</div>
          <div className="text-xs text-muted-foreground">{hint}</div>
        </div>
      </div>
      <div className={muted ? 'text-sm text-neutral-500' : 'text-sm font-semibold text-neutral-100'}>{value}</div>
    </div>
  )
}

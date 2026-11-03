/**
 * Admin API client for the node.
 *
 * The dashboard is served by the node itself at /dashboard, so every call is
 * same-origin — paths are relative and there's no CORS to negotiate. Each
 * request carries a NIP-98 Authorization header signed by the extension; the
 * node's verifyAdmin rejects anything not signed by the configured admin key,
 * so authorization is the node's decision, not ours.
 */
import { nip98Header } from './nostr'

/** A mod (or other stored event) that references a blob. */
export interface ModRef {
  /** Addressable coordinate "<kind>:<pubkey>:<d>", or the event id. */
  coord: string
  /** The mod's title, or "" if it has none. */
  title: string
}
export interface AdminBlob {
  hash: string
  ext: string
  size: number
  url: string
  added: number
  /** Uploader pubkeys (hex), original first. Empty for blobs uploaded before tracking. */
  uploaders?: string[]
  /** Mods that reference this blob. Empty/absent = no stored mod references it. */
  refs?: ModRef[]
  /** Withheld from download — the last uploader retracted it. Reversible. */
  quarantined?: boolean
}
export interface BlobsPage {
  total: number
  page: number
  per: number
  pages: number
  types: string[]
  blobs: AdminBlob[]
}
export type BlobSort = 'hash' | 'size' | 'date'

// ─── Relay events ───────────────────────────────────────────────────

export interface RelayEvent {
  id: string
  pubkey: string
  created_at: number
  kind: number
  tags: string[][]
  content: string
  sig: string
}
export interface EventsPage {
  events: RelayEvent[]
  count: number
  /** true = the limit or scan ceiling was hit; more may exist. */
  truncated: boolean
  /** how many stored events were examined. */
  scanned: number
}
export interface EventsQuery {
  kinds?: number[]
  author?: string
  tags?: { name: string; value: string }[]
  since?: number
  until?: number
  limit?: number
  search?: string
}

/** Read-only snapshot of the node's live config (Relay → Settings). No secrets. */
export interface RelayConfig {
  relay: {
    accept_all_kinds: boolean
    accepted_kinds: { kind: number; label: string }[]
    min_event_pow: number
    admin_configured: boolean
  }
  download_gate: {
    pow_difficulty: number
    challenge_ttl_sec: number
    trusted_ip_header: string
  }
  upload: {
    max_concurrent: number
    min_pow: number
    min_upload_rate_kbps: number
    idle_timeout_sec: number
    min_free_disk_mb: number
    allowed_types: string[]
    size_cap_mb: number
    whitelisted_cap_mb: number
  }
}
export interface WhitelistEntry {
  pubkey: string
  note?: string
}
export interface WhitelistInfo {
  limit_mb: number
  whitelisted_mb: number
  /** The node's configured defaults (what "reset" restores to). */
  default_limit_mb?: number
  default_whitelisted_mb?: number
  entries: WhitelistEntry[]
}

/** X-Reason carries the node's human-readable failure; fall back to status. */
async function reason(res: Response): Promise<string> {
  return res.headers.get('X-Reason') || `${res.status} ${res.statusText}`
}

async function adminFetch(path: string, method: string, body?: unknown): Promise<Response> {
  // Absolute URL for the NIP-98 `u` tag — the node matches it against the
  // request path exactly, so it has to be the real origin, not a relative path.
  const url = new URL(path, window.location.origin).toString()
  const headers: Record<string, string> = { Authorization: await nip98Header(url, method) }
  const init: RequestInit = { method, headers }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    init.body = JSON.stringify(body)
  }
  return fetch(url, init)
}

// ─── Blobs ──────────────────────────────────────────────────────────

export async function listBlobs(opts: {
  search?: string
  ext?: string[]
  sort?: BlobSort
  dir?: 'asc' | 'desc'
  page?: number
  per?: number
} = {}): Promise<BlobsPage> {
  const q = new URLSearchParams()
  if (opts.search) q.set('search', opts.search)
  if (opts.ext?.length) q.set('ext', opts.ext.join(','))
  if (opts.sort) q.set('sort', opts.sort)
  if (opts.dir) q.set('dir', opts.dir)
  if (opts.page) q.set('page', String(opts.page))
  if (opts.per) q.set('per', String(opts.per))
  const res = await adminFetch(`/admin/blobs?${q.toString()}`, 'GET')
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}

/**
 * Download a blob through the admin route, which bypasses the public
 * download gate (PoW). Fetched with the NIP-98 header, then handed to the
 * browser as a save — a plain <a download> can't carry the auth header.
 */
export async function downloadBlob(hash: string, ext: string): Promise<void> {
  const name = ext ? `${hash}.${ext}` : hash
  const url = new URL(`/admin/blob/${name}`, window.location.origin).toString()
  const res = await fetch(url, { headers: { Authorization: await nip98Header(url, 'GET') } })
  if (!res.ok) throw new Error(await reason(res))
  const objUrl = URL.createObjectURL(await res.blob())
  const a = document.createElement('a')
  a.href = objUrl
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(objUrl)
}

/** Read-only node config snapshot for the Relay → Settings view. */
export async function getRelayConfig(): Promise<RelayConfig> {
  const res = await adminFetch('/admin/config', 'GET')
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}

/** Query stored relay events by the given filters (all optional). Read-only. */
export async function queryEvents(opts: EventsQuery = {}): Promise<EventsPage> {
  const q = new URLSearchParams()
  if (opts.kinds?.length) q.set('kinds', opts.kinds.join(','))
  if (opts.author?.trim()) q.set('author', opts.author.trim())
  for (const t of opts.tags ?? []) {
    if (t.name.trim() && t.value.trim()) q.append('tag', `${t.name.trim()}:${t.value.trim()}`)
  }
  if (opts.since) q.set('since', String(opts.since))
  if (opts.until) q.set('until', String(opts.until))
  if (opts.limit) q.set('limit', String(opts.limit))
  if (opts.search?.trim()) q.set('search', opts.search.trim())
  const res = await adminFetch(`/admin/events?${q.toString()}`, 'GET')
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}

/** Delete a blob by hash (NIP-98 admin DELETE /admin/blob/<hash>). */
export async function deleteBlob(hash: string): Promise<void> {
  const res = await adminFetch(`/admin/blob/${hash}`, 'DELETE')
  if (!res.ok) throw new Error(await reason(res))
}

// ─── Blacklisted hashes ─────────────────────────────────────────────

export interface BlacklistEntry {
  hash: string
  reason?: string
  added?: number
}

export async function getBlacklist(): Promise<BlacklistEntry[]> {
  const res = await adminFetch('/admin/blacklist', 'GET')
  if (!res.ok) throw new Error(await reason(res))
  return (await res.json()).entries ?? []
}

/** Blacklist a hash (blocks re-upload) and purge any stored copy. */
export async function addBlacklist(hash: string, note?: string): Promise<void> {
  const res = await adminFetch('/admin/blacklist', 'POST', { hash, reason: note })
  if (!res.ok) throw new Error(await reason(res))
}

/** Lift a blacklist entry (does not restore any bytes). */
export async function removeBlacklist(hash: string): Promise<void> {
  const res = await adminFetch('/admin/blacklist', 'DELETE', { hash })
  if (!res.ok) throw new Error(await reason(res))
}

/** Lift a blob's quarantine, making it downloadable again (bytes were kept). */
export async function releaseQuarantine(hash: string): Promise<void> {
  const res = await adminFetch('/admin/quarantine', 'DELETE', { hash })
  if (!res.ok) throw new Error(await reason(res))
}

// ─── Upload-size whitelist ──────────────────────────────────────────

export async function getWhitelist(): Promise<WhitelistInfo> {
  const res = await adminFetch('/admin/whitelist', 'GET')
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}

export async function addWhitelist(pubkey: string, note?: string): Promise<void> {
  const res = await adminFetch('/admin/whitelist', 'POST', { pubkey, note })
  if (!res.ok) throw new Error(await reason(res))
}

export async function removeWhitelist(pubkey: string): Promise<void> {
  const res = await adminFetch('/admin/whitelist', 'DELETE', { pubkey })
  if (!res.ok) throw new Error(await reason(res))
}

/** Update the per-upload size caps (MB). Whitelisted must be ≥ the normal cap. */
export async function setUploadCaps(limitMb: number, whitelistedMb: number): Promise<void> {
  const res = await adminFetch('/admin/upload-caps', 'PUT', {
    limit_mb: limitMb,
    whitelisted_mb: whitelistedMb,
  })
  if (!res.ok) throw new Error(await reason(res))
}

/** Clear any runtime override and restore the node's configured caps. */
export async function resetUploadCaps(): Promise<void> {
  const res = await adminFetch('/admin/upload-caps', 'DELETE')
  if (!res.ok) throw new Error(await reason(res))
}

// ─── Hubs (the NIP-CHAT hub whitelist) ──────────────────────────────

export interface HubEntry {
  coordinate: string
  pubkey: string
  d: string
  note?: string
  relays?: string[]
  /** Poster-set epoch, or -1 if the member tree has not been read yet. */
  epoch: number
  /** Cached poster-set size (members) for this hub. */
  members: number
}

export async function listHubs(): Promise<HubEntry[]> {
  const res = await adminFetch('/admin/hubs', 'GET')
  if (!res.ok) throw new Error(await reason(res))
  const data = (await res.json()) as { hubs?: HubEntry[] }
  return data.hubs ?? []
}

/** Whitelist a hub by naddr or raw "36942:<pubkey>:<dTag>" coordinate. */
export async function addHub(ref: string, note?: string): Promise<HubEntry> {
  const res = await adminFetch('/admin/hubs', 'POST', { ref, note })
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}

export async function removeHub(coordinate: string): Promise<void> {
  const res = await adminFetch('/admin/hubs', 'DELETE', { coordinate })
  if (!res.ok) throw new Error(await reason(res))
}

// ─── Sync relays (durability §6) ────────────────────────────────────

export interface SyncRelay {
  url: string
  enabled: boolean
  /** Part of the node's seeded default set (removing it only disables it). */
  default?: boolean
}

export async function listSyncRelays(): Promise<SyncRelay[]> {
  const res = await adminFetch('/admin/sync-relays', 'GET')
  if (!res.ok) throw new Error(await reason(res))
  return (await res.json()).relays ?? []
}

/** Add a custom sync relay (ws:// or wss://). Returns the updated list. */
export async function addSyncRelay(url: string): Promise<SyncRelay[]> {
  const res = await adminFetch('/admin/sync-relays', 'POST', { url })
  if (!res.ok) throw new Error(await reason(res))
  return (await res.json()).relays ?? []
}

/** Toggle a sync relay on or off. */
export async function setSyncRelayEnabled(url: string, enabled: boolean): Promise<void> {
  const res = await adminFetch('/admin/sync-relays', 'PUT', { url, enabled })
  if (!res.ok) throw new Error(await reason(res))
}

/** Remove a sync relay (a custom one is deleted; a default is just disabled). */
export async function removeSyncRelay(url: string): Promise<void> {
  const res = await adminFetch('/admin/sync-relays', 'DELETE', { url })
  if (!res.ok) throw new Error(await reason(res))
}

// ─── Storage re-routing (durability §4) ─────────────────────────────

export interface BackendDesc {
  kind: string // r2 | s3 | disk
  endpoint?: string
  bucket?: string
  path?: string
}
export interface MigrationProgress {
  total: number
  copied: number
  skipped: number
  failed: number
  done: boolean
  err?: string
}
export interface StorageInfo {
  primary: BackendDesc
  /** The read-fallback backend, or null when none is configured. */
  fallback: BackendDesc | null
  blob_count: number
  blob_bytes: number
  disk: { available: boolean; total_mb?: number; used_mb?: number; free_mb?: number }
  migration: {
    running: boolean
    progress: MigrationProgress
    started_at?: number
    finished_at?: number
  }
}

export async function getStorage(): Promise<StorageInfo> {
  const res = await adminFetch('/admin/storage', 'GET')
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}

/** Start copying the read-fallback backend's blobs into the primary. */
export async function startStorageMigration(): Promise<void> {
  const res = await adminFetch('/admin/storage/migrate', 'POST')
  if (!res.ok) throw new Error(await reason(res))
}

// ─── Node runtime stats ─────────────────────────────────────────────

export interface NodeStats {
  /** The disk the node runs on (its data dir volume) — not R2/S3. */
  disk: { available: boolean; total_mb?: number; used_mb?: number; free_mb?: number }
  /** The node's own footprint (event store + metadata files), MB. */
  data_dir_mb: number
}

export async function getNodeStats(): Promise<NodeStats> {
  const res = await adminFetch('/admin/stats', 'GET')
  if (!res.ok) throw new Error(await reason(res))
  return res.json()
}

// ─── helpers ────────────────────────────────────────────────────────

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}

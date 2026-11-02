# REBLOS migration plan

**REBLOS Chat** (Relay & Blossom Server for NIP-CHAT), short name **REBLOS**
(Relay Blossom), is a NIP-CHAT relay + Blossom server that an owner (or a
whitelisted operator) runs to host their hubs' events and Blossom files, with
hub-scoped access rules.

It is a fork of the DEG Mods Blossom-Relay-Server (Go, built on
[khatru](https://github.com/fiatjaf/khatru) + eventstore + minio). We keep the
infrastructure, strip the DEG-specific pieces, and retarget the rest at
NIP-CHAT. This document is the single source of truth for that work.

Starting version: **0.1.0**.

---

## 0. Why fork instead of starting from vanilla khatru

The DEG node already solved, and tested, almost everything REBLOS needs:
whitelist + per-key upload caps + a dashboard to manage them, **magic-byte**
upload-type sniffing (does not trust the client `Content-Type`), multi-backend
storage (disk / Cloudflare R2 / self-hosted S3) with a circuit breaker,
blob ownership + reference counting, moderation (blocklist / blacklist / banned
events / quarantine), the NIP-98 admin API, upload safety rails (idle timeout,
min throughput, min free disk, concurrency cap), and Docker + Caddy deploy.
Rebuilding that from khatru would be weeks of work.

---

## 1. Keep / Strip / Change map

### KEEP (infrastructure, little or no change)

| File | What it is |
|---|---|
| `internal/storage/{storage,disk,r2,breaker}.go` | Blob backend abstraction + disk/R2/S3 + circuit breaker |
| `internal/identity/identity.go` | Node Nostr key (generated on first run) |
| `internal/server/limits.go` | Global concurrency cap + per-key in-flight limit |
| `internal/server/{disk_unix,disk_other}.go` | Free-disk check (Linux prod / Windows dev) |
| `internal/server/index.go` | `BlobIndex` over the storage layer |
| `internal/server/{stats,config_view,events}.go` | Dashboard stats, config snapshot, admin relay browser |
| `internal/server/{blocklist,blacklist,banned_events,quarantine}.go` | Moderation |
| `internal/server/{claim,blob_owners}.go` | Uploader tracking / multi-owner retraction |
| `internal/server/admin.go` | NIP-98 admin API |
| `internal/server/gate.go` | BUD-POW download gate (keep, default **off**; ad hooks removed) |

### CHANGE (retarget / extend)

| File | Change |
|---|---|
| `internal/server/whitelist.go` | **Whitelist by hub coordinate, not author.** Entry becomes `hubCoord -> {note, per-hub overrides}` (see §2). |
| `internal/server/upload.go` | Add `text/plain` (no magic: "allowed magic OR valid UTF-8" rule, or hubs brick), `image/*`, `video/*`, `audio/*` magics. Keep the byte-sniff-not-header design. |
| `internal/server/upload_caps.go` | Extend global caps to **per-type caps + per-hub exceptions**. |
| `internal/server/relay.go` | Replace the mod-kind allowlist with the **NIP-CHAT kind set** (see §3). |
| `internal/server/blob_refs.go` | "Referenced by a mod event" -> "referenced by a **hub event**" (36942 `struct`/`m`, message media). Feeds the delete/GC path. |
| `internal/announce/announce.go` | Rebrand the announcement; tie into the NIP-CHAT capability-probe / `dedicated` marker idea. |
| `internal/config/config.go` | Drop ads/ingest/mod fields; add `enforce_whitelist`, per-type caps, per-hub overrides, NIP-CHAT kinds. |
| `cmd/degnode/main.go` | Rename binary/package; strip ads/ingest/mod wiring. |
| `internal/server/server.go` | Follow-on wiring edits. |
| Module path, README, docs, config examples, Dockerfile, deploy, dashboard | Rebrand DEG -> REBLOS. |

### STRIP (DEG-specific, delete)

| File | Why |
|---|---|
| `internal/server/ad.go`, `ads_admin.go` (+ tests) | BUD-Ads gate + ad inventory. **Done in 0.1.0.** |
| `internal/server/ingest.go` | Mirrors mod kinds. Will be **rewritten** as the NIP-CHAT sync layer (§6), not kept as-is. |
| `config.yml` | DEG's live config. Replace with ours; **gitignore it** (prod holds R2 secrets). |

### REVIEW

- `delete.go` + `deletions.go`: confirm delete semantics line up with NIP-CHAT (v2 deletion was reverted to match v1 client-side).
- `LICENSE`: relicense MIT, copyright holder -> REBLOS Chat. **Done in 0.1.0.**

---

## 2. Access model: whitelist by hub coordinate

The admin whitelists a **hub addressable coordinate** `36942:<creatorPubkey>:<dTag>`
(entered as an `naddr` copy-pasted from the hub), **not** individual author keys.

- The admin manages a handful of hubs, not thousands of pseudonyms.
- Membership tracks itself: join / leave / rotation rewrites the member tree and
  the relay re-derives the poster set automatically.
- The coordinate contains the owner/creator pubkey, so the owner is authorized
  immediately (bootstraps the first structural uploads before a member tree exists).

This **replaces "Flow A"** (owner handing the admin each pseudonym). No manual
per-pseudonym step.

### The poster set is derived autonomously, for BOTH v1 and v2

Confirmed against the client: v2 member leaf pages are **not** fully encrypted.
Each leaf line is `leaf:<nodeId>:<pubkey>:<roles>:<encryptedLeafKey>[:<flags>]`.
The **pseudonym `P`** and its **roles** are plaintext; only the `encryptedLeafKey`
(LKH key material) and the page `roster` segment (the group-encrypted `P->R` map)
are encrypted. So the relay can read the full set of member pseudonyms and their
roles from the plaintext leaf pages without any secret, while remaining unable to
link a pseudonym to a real identity.

Therefore:
- **v1:** parse `leaf:` lines -> real member pubkeys -> poster set.
- **v2:** parse `leaf:` lines -> pseudonym `P`s -> poster set.

The relay watches each whitelisted hub's 36942 event, re-fetches the member tree
when the `m` tag / epoch changes, and caches the per-hub poster set (and per-`P`
roles). Upload-auth and event-auth both draw from this one set.

### Role gating

Because roles are plaintext in the leaf lines, the relay can gate finer than
membership: accept a post iff `author in poster set` **and** that author's role
permits posting in the tagged channel. Built on the same cache.

### DMs are the exception

Gift-wrapped DMs (kind 1059) are signed by **ephemeral throwaway keys** by
design, so they cannot be gated by `author in poster set`. Gate them by the
recipient `p` tag being in the poster set (deliver only to members).

---

## 3. Event gating (store) and serving (read)

**Store** (khatru `RejectEvent`): accept iff kind is in the allowed set AND the
author is in the tagged hub's poster set (role check on top for §2).

**How a message declares its hub** (confirmed in `client/src/lib/nostr/events.ts`):
kind-36943 messages carry `["h", <hubDTag>]` and `["c", <channelId>]`. The `a`
tag is only used for reply/root references to *other messages*
(`36943:<pubkey>:<dTag>`), never the hub's 36942 coordinate. So the relay keys
gating off **`#h` (hub dTag)** and channel/role gating off **`#c`**, not an `#a`
coordinate.

Caveat: `#h` is the dTag alone, not `<pubkey>:<dTag>`, so two different owners
could pick the same dTag. The whitelist entry holds the full coordinate
(pubkey + dTag), and the relay accepts a `#h`-tagged message only if its author
is in the P-set of a whitelisted hub with that dTag. That author-in-P-set check
disambiguates naturally (a message validates only for the hub it actually belongs
to), so colliding dTags are harmless.

**Kind policy:** toggle **allow-all** vs **allow-select**. Select default = the
NIP-CHAT-specific kinds + a configurable extra set (kind 1, DM kinds, etc.) +
admin-added custom kinds in the dashboard. Generalizes the existing
`accept_all_kinds` + allowlist.

**Serve** ("give me this hub's events, members only"): largely falls out of store
gating (non-member events were never stored). Add read-side enforcement as
belt-and-suspenders: on a `REQ` scoped to a hub (`#h` = dTag), intersect `authors`
with that hub's poster set so even events that slipped in (pre-gating / federated)
are not served. This is the anti-spam guarantee: members never download garbage to
reject locally.

**Max event size:** a configurable dashboard knob. Note the 64 KB figure is
NIP-CHAT's self-imposed *hub-event* cap (relieved by the struct blob), not a
relay default; the relay's own max-event-size is separate (default e.g. 128 KB).

**Freshness / revocation:** a kicked member can post until the relay's poster set
updates, so the owner's kick/rotation flow must push the new tree to the hub's
relays promptly (ties into client `publishHubList` / failover).

**Metadata note:** the relay learns full membership *as pseudonyms* (not real
identities). Standard relay metadata exposure; acceptable for an owner/whitelisted
node. State it in the spec.

---

## 4. Storage re-routing (R2 / self-hosted) from the dashboard

The `Storage` interface + disk/R2/S3 impls already exist (config-selected). New
work:

- Runtime backend swap behind the interface + dashboard UI to pick/configure.
- **Migration story** so a switch does not orphan existing blobs: a
  **primary + read-fallback** backend and/or a background **migrate job**
  (copy all blobs X -> Y) with dashboard progress; drop the old backend when done.
- Scope: this is **blob** storage (the offload-worthy thing). The relay **event
  store** is khatru's embedded badger DB; relocating that to a remote DB is a
  much bigger lift and is **descoped** initially.

---

## 5. Delete / ownership (keep as-is, retarget `refs`)

`delete.go` + `blob_owners.go` already implement exactly the desired behavior:

- Owners are an ordered set per blob (same bytes from N uploaders = N owners).
- Non-admin delete = **retract your own claim** (`removeOne`); never touches bytes.
- **Last owner out** + nothing references it -> **quarantine** (download withheld,
  bytes kept, reversible; a re-upload revives it).
- Admin delete = the only hard byte-removal (moderation).

This maps cleanly onto v2 (same struct/tree/`m` blob re-uploaded under multiple
pseudonyms + replication). The one change: the "nothing references it" check uses
`blob_refs`, which retargets from mod events to **hub events** (36942 `struct`/`m`,
message media).

---

## 6. Cross-relay + cross-Blossom sync (durability catch-all)

So a hub loses nothing during downtime or when a member can't reach this node.
Mostly a retarget of `ingest.go`. **Pull-based** (this node protects itself;
clients already publish widely via failover).

**Event sync:** per whitelisted hub, subscribe to the hub's **own declared relay
hints** with filters scoped to the hub (`kinds`, `#h` = dTag, `authors` = poster
set) and store the results. The member-gate is reused on this path, so we never
mirror non-member spam.

**Blossom sync:** per whitelisted hub, enumerate the current blob hashes from
plaintext data (36942 `m`/`struct` tags -> member-tree index -> page/spine/history
hashes; message media hashes). For any hash missing locally, fetch it from another
Blossom server (the hub's `blossomServers` / `r` hints), **verify the sha256**
(content-addressed, mandatory), and store it. BUD-04 mirroring, relay-initiated.

- **Zero-config default:** sync targets auto-derived from each hub's own hints;
  dashboard can add extra peers.
- **When:** startup catch-up + periodic reconcile + on a new 36942 revision.
- **GC-safe:** mirrored blobs are kept alive by hub-event `refs` (§5).
- **Autonomous for v2:** everything needed is in plaintext leaf pages + tags.

---

## 7. Deploy: one-command installer

Goal: paste one command into a fresh Linux server; answer a few questions; done.

**Image hosting:** CI builds the static image from this repo and publishes it to a
container **registry** (GHCR, `ghcr.io/<org>/reblos`). The installer pulls the
image; no Go toolchain or source checkout on the box. (The existing Dockerfile
already produces the static binary.)

**The installer:**
1. Prints a short **pre-flight** blurb (point the domain's A record at this box
   first; have your admin npub ready; storage creds if R2/S3), and waits for
   confirmation. Checks DNS resolves to this host before the TLS step.
2. Asks only what it can't infer: domain, admin npub, relay key
   (*"Generate a relay Nostr keypair? (recommended: yes. Say no to paste your
   own nsec, not your personal one.)"*), storage backend + creds.
3. Does everything else: install Docker + compose plugin; generate/store the relay
   key in `data/` (0600); write `config.yml` + `.env` (0600); write the
   `Caddyfile` (Caddy auto-TLS, no email needed); `docker compose up -d`; wait
   healthy; print the relay npub + dashboard URL + "add your hub by naddr".

**Updates without data loss:** all state lives **outside the image** (the mounted
`data/` volume + `config.yml` + `.env`: identity key, badger event store,
`*.json` state, disk blobs). Update = `docker compose pull && docker compose up -d`;
same volume, data intact. Keep **startup schema migrations** (precedent:
`blob_owners.go` upgrades its legacy format on load). Pin image tags for
deliberate updates + rollback. **Back up `data/` + `.env`.** Re-running the
installer is the upgrade path (idempotent).

**Adding hubs** is a dashboard action, not an install question.

---

## 8. Progress

- [x] Copy DEG node into `relay/`, excluding `.git` / `node_modules` / `dist` / binaries.
- [x] Relicense `LICENSE` (MIT, copyright REBLOS Chat).
- [x] `VERSION` = 0.1.0.
- [x] Strip server-side ads (`ad.go`, `ads_admin.go` + tests + all wiring).
- [x] Rebrand identifiers: Go module path -> `github.com/Freakoverse/REBLOS`; `cmd/degnode` -> `cmd/reblos`; Docker/compose/entrypoint, dashboard package name, README front door, "DEG Mods" brand strings. (Deferred: "mod"/kind-domain wording in comments + the deploy guide prose, which change when the kinds are retargeted.)
- [ ] Rewrite `ingest.go` as the NIP-CHAT sync layer (§6).
- [~] Whitelist by hub coordinate + poster-set cache (§2). Done (`internal/hub`): `membertree.go` (parse index + leaf pages -> `PosterSet`); `registry.go` (whitelist-by-coordinate, naddr/coordinate parsing, persistent); `fetcher.go` (local-first + remote-Blossom `BlobFetcher`, mandatory sha256 verify, optional local mirror); `cache.go` (`PosterCache`: epoch-aware per-hub cache, rebuild on index-hash change, keep last-good on build failure, out-of-order-revision guard). All table-tested. Wired into `server.New` (`server/hubs.go`): the registry loads from `hubs.json`, the cache builds via a storage-backed `Fetcher`, poster sets are reconciled from the local store at startup and refreshed live when a 36942 is saved (`OnEventSaved`). Admin API done (`server/hubs.go`): `GET/POST/DELETE /admin/hubs` (add by naddr or coordinate, reconcile-on-add, list with live poster-set epoch + member count, remove), NIP-98 admin-gated, end-to-end tested. Pending: the dashboard tab for it, and the remote-subscription watcher that pulls 36942 revisions from a hub's relay hints even when they are not republished here.
- [~] Event store/serve gating + kind policy + role gating (§3). Done: store-side `rejectEvent` is retargeted from mod kinds to NIP-CHAT — hub events accepted only for a whitelisted coordinate; member-content kinds (message, reaction, poll, report, etc.) accepted only when the author is in the target hub's poster set (resolved via `#h` dTag or `#a` coordinate); join requests accepted only when they target a whitelisted hub; `extra_kinds` config for extra non-gated kinds; `accept_all_kinds` now also drops the member gate. `rejectFilter` enforces the kind allowlist. Mod kinds / legacy / jam / moderation-tag logic removed; `ingest.go` retargeted to accepted kinds (interim). Table-tested. Pending: role gating (channel `#c` + the leaf roles) and the serve-side member-author intersection (belt-and-suspenders over the store gate).
- [~] Upload types + caps (§1 CHANGE). Done: `upload.go` now sniffs text (UTF-8, no NUL -> `text/plain`) and common image/audio/video magics (png/jpg/gif/webp/bmp, mp3/ogg/wav/flac, mp4/webm, pdf); the default allowlist is `config.DefaultUploadTypes` (text + media, `txt` included so member trees store); archives/exe are opt-in. The client's Content-Type is still never trusted. Table-tested (text blob, png, webp-at-offset-8, opaque-binary reject, restricted-allowlist reject) + full upload-path tests. Pending: per-type size caps + per-hub cap exceptions (caps are still global normal/whitelist).
- [ ] Retarget `blob_refs` to hub events (§5).
- [ ] Storage re-routing UI + migration (§4).
- [ ] One-command installer + CI image publish (§7).
- [ ] Dashboard rebrand + remove ad UI.

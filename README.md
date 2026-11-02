# blossom-relay-server

A self-hostable, federated **Blossom server + Nostr relay** node for
[DEG Mods](https://degmods.com) — a decentralized game-mods platform. It serves
mod files (content-addressed by SHA-256) from pluggable storage and runs a
mod-scoped Nostr relay, with anti-abuse **proof-of-work** and ad-view **download
gates** instead of payments or logins.

Licensed **MIT** — anyone can run a node.

## Features

- **Blossom blob server** — BUD-01/02/04/05/06 GET/HEAD/upload/delete, blobs
  addressed by SHA-256.
- **Streaming uploads** up to 500 MB — hashed on the fly (a lying `Content-Length`
  can't overflow), a **configurable** magic-byte type filter (default `.zip`; set
  `upload.allowed_types` to add types or `["*"]` for any), kind-24242 auth, a
  global concurrency cap plus one in-flight upload per npub, and a free-disk check.
- **Mod-scoped Nostr relay** — an embedded [badger](https://github.com/dgraph-io/badger)
  event store that accepts only mod events (current kind `31142` + legacy `30402`
  `GameMod` before a cutoff), the mod-jam family (`31143`/`31243`/`31343`), and
  admin moderation tags (`30985`), enforces per-event proof-of-work (NIP-13), and
  honors author deletions (NIP-09) **persistently** so a removed mod stays gone.
  Moderation tags apply tags to *other people's* posts, so they are writable only
  by `relay.admin_npub` and are PoW-exempt; anyone may read them.
- **Ingest / mirror** — optionally subscribes to other relays and stores every mod
  they carry (current + legacy), so the node becomes a complete, one-stop mod DB.
- **Moderation** — NIP-86 pubkey ban/allow, plus persistent **address-based
  takedowns**: a `<kind>:<pubkey>:<d>` coordinate stays rejected even if it's later
  re-published or re-ingested.
- **Pluggable storage** — Cloudflare R2, self-hosted S3 (Garage / Ceph / SeaweedFS),
  or local disk. Cloud is one option, not a requirement.
- **Download gates** — [BUD-POW](docs/specs/BUD-POW.md) (proof-of-work, anti-abuse)
  and [BUD-Ads](docs/specs/BUD-Ads.md) (ad view, funds the operator). Both use
  HTTP `428` and are deliberately **not** a payment flow, so payment clients fail
  over cleanly instead of misreading them.
- **Federation** — nodes announce their capabilities over Nostr; because blobs are
  content-addressed, any discovered node is a drop-in source for any hash.
- **Resilience** — an S3 circuit breaker fails fast during an outage, structured
  (`slog`) logging, and a fully static single binary.

## How it works

The node wraps [khatru](https://github.com/fiatjaf/khatru) (fiatjaf's Go relay +
Blossom framework) as a library rather than forking it. A streaming `/upload`
handler sits in front of the relay (khatru's built-in upload buffers the whole
file, unusable at 500 MB), and a gate middleware wraps blob `GET`/`HEAD` to
enforce the PoW/ad challenges. Storage is abstracted behind a small `Storage`
interface with R2/S3 and local-disk implementations.

## Quick start (Docker + Caddy)

The [`deploy/`](deploy/) directory has a Compose stack (node + Caddy for automatic
HTTPS):

```sh
cd deploy
cp .env.example .env                 # R2/S3 credentials
cp config.yml.example config.yml     # endpoint, public URL, admin npub
docker compose up -d
```

Full walkthrough — buy a server, set up storage (Cloudflare R2 **or** self-hosted
S3), DNS, deploy, verify, and turn on gates — in the
[deploy guide](docs/DEPLOY-GUIDE.md).

## Configuration

All options are documented in [`deploy/config.yml.example`](deploy/config.yml.example). Secrets
(R2/S3 keys) are read from the environment (`R2_ACCESS_KEY` / `R2_SECRET_KEY` or
`S3_ACCESS_KEY` / `S3_SECRET_KEY`) so they stay out of the config file. Key knobs:

| Section | What it controls |
|---|---|
| `backend` | `r2`, `s3`, or `disk` |
| `r2` / `s3` / `disk` | the selected backend's settings |
| `upload` | size cap, concurrency, min free disk, optional NIP-13 PoW |
| `relay` | admin npub (NIP-86 + moderation tags), min event PoW, kind allowlist |
| `download` | PoW difficulty, ad gate, trusted client-IP header |
| `ingest` | mirror mods (current + legacy) from other relays into this node |
| `ads` | relays the node publishes its BUD-Ads inventory to |
| `announce` | Nostr capability announcement for federation (on by default, weekly) |
| `log` | level + `text`/`json` format |

On first run the node generates its identity and prints its `npub`; the `nsec` is
saved to `data/identity.key` (used to sign the node's ad inventory and its
federation announcement).

## Forking this node

Everything below works untouched, which is exactly why it gets missed. Two
settings decide who controls your relay and what it's for.

### The admin key — read this one

```yaml
relay:
  admin_npub: "npub1youradminkey"
```

This npub holds **two** powers on your node:

1. The NIP-86 management API — banning and unbanning pubkeys.
2. The only key allowed to publish **moderation tags** (kind `30985`), which
   apply tags like `content-warning` to *other people's* posts. Clients that
   trust your relay render those tags as if the author had set them.

Leave DEG Mods' npub here and DEG Mods can moderate your relay. Nothing breaks
and nothing warns you. **Put your own npub in.**

Leaving it empty is safe but disables both: no management API, and moderation
tags are refused from everyone including you.

> Client-side counterpart: the DEG Mods **client** has its own `ADMIN_PUBKEY` in
> `src/lib/constants.ts`, and it decides whose moderation tags to *honour*. The
> relay decides whose it will *store*. If those two keys differ, your client will
> ignore tags your relay accepted (or want tags it refuses to store). Keep them
> the same key unless you have a reason not to.

### Accepting any kind

By default this is a **mod-scoped** relay: it stores and serves mod events
(`31142`, legacy `30402`), the mod-jam family (`31143`/`31243`/`31343`), and
moderation tags (`30985`). Everything else is rejected on write *and* on read.

For a general-purpose relay:

```yaml
relay:
  accept_all_kinds: true
  min_event_pow: 20        # strongly recommended when you do this
```

That drops the allowlist entirely — any kind, from anyone, including kind `30985`
from anyone. What still applies: banned pubkeys, event takedowns, and honored
NIP-09 deletions. What stops applying is the thing that was doing most of the
work: a relay that only accepts mod events simply isn't a useful target for
general spam. **Set `min_event_pow` when you open it up** — with the scope gone,
proof-of-work is the only floor left. Deletions stay PoW-exempt, so a takedown
never needs mining.

The federation announcement still advertises the mod kinds, so an open fork will
be discovered as a mod node until you adjust `AcceptedModKinds()` in
[`internal/server/relay.go`](internal/server/relay.go).

## Download gates

Gates are off by default. When enabled, an ungated blob request gets a `428` with
one `X-Blossom-Gate-*` challenge per unmet gate; the client satisfies them (mines
the PoW, shows the ad) and retries with the matching proof headers.

- **PoW** ([BUD-POW](docs/specs/BUD-POW.md)) is the enforceable anti-abuse gate:
  a stateless HMAC challenge bound to the blob + client IP + expiry.
- **Ads** ([BUD-Ads](docs/specs/BUD-Ads.md)) is a cooperative funding gate. The ad
  inventory is the operator's own NIP-78 event (`kind 30078`, `d=manual-blossom-ads`).
  Impression/view/click metrics are aggregate-only with ephemeral, salted in-window
  dedup — **no raw IPs are stored** — and advertisers reconcile against their own
  analytics (there is no central authority).

## Storage & retention

Blobs are keyed `<sha256>.<ext>`. Mod files are meant to **persist** — there is no
automatic pruning or expiry. Deletion is manual only: the authenticated Blossom
`DELETE /<hash>` path and the NIP-86 admin ban/allow API.

## Admin API

Management endpoints authenticated with **NIP-98** (a kind-27235 event signed by
`relay.admin_npub`), so an admin signs each request in the browser (NIP-07) with no
raw key on the server. All require `Authorization: Nostr <base64(event)>`.

- `GET /admin/blobs?search=&page=&per=` — list stored blobs (hash, ext, size, url),
  filtered by a hash substring, numbered pagination (cached 30s).
- `GET /admin/whitelist` — the upload-size whitelist + the normal/5× caps.
- `POST /admin/whitelist` `{ "pubkey", "note?" }` — grant a pubkey the **5× upload
  cap** (accepts npub or hex).
- `DELETE /admin/whitelist` `{ "pubkey" }` — revoke it.
- `GET|POST|DELETE /admin/banned-events` — persistent, **address-based** event
  takedowns. A `<kind>:<pubkey>:<d>` coordinate stays rejected even if the mod is
  later re-published or re-ingested from another relay.
- `GET|PUT /admin/ads` — read/replace the BUD-Ads inventory. The node signs the
  NIP-78 event (`30078:<node-pubkey>:manual-blossom-ads`) with **its own** key and
  publishes it (so an admin can't sign it via NIP-07), then serves it from here.
- `DELETE /<hash>` — remove a blob (admin-signed kind-24242 `t=delete`).

Pubkey **bans** use the relay's NIP-86 API and are unified: a banned key can neither
post mod events nor upload blobs.

## Build & run from source

Requires Go 1.25+.

```sh
cp deploy/config.yml.example config.yml   # edit; set backend + endpoint
go run ./cmd/degnode -config config.yml
go test ./...                             # unit tests (no cloud needed)
```

## Project layout

```
cmd/degnode        entrypoint (config load, storage factory, announce wiring)
internal/config    YAML config (+ env overrides for secrets)
internal/identity  node Nostr key (generated on first run)
internal/storage   Storage interface + backends: r2.go (R2/S3), disk.go (local), breaker.go
internal/server    khatru relay + Blossom handler, streaming upload, PoW/ad gate,
                   ingest/mirror, moderation (bans + address takedowns), ad inventory
internal/announce  Nostr capability announcement (federation discovery)
docs/              deployment guide + BUD specs
deploy/            Docker Compose + Caddy
```

## License

[MIT](LICENSE).

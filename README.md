# REBLOS

**REBLOS Chat** (Relay & Blossom Server for NIP-CHAT; short **REBLOS** = Relay
Blossom) is a self-hostable **Blossom server + Nostr relay** that an owner or a
whitelisted operator runs to host their NIP-CHAT hubs' events and Blossom files,
with hub-scoped access rules. Content is addressed by SHA-256 and served from
pluggable storage (disk / Cloudflare R2 / self-hosted S3).

It began as a fork of the DEG Mods Blossom-Relay-Server and has been retargeted
from game mods to NIP-CHAT. See **[docs/REBLOS-MIGRATION.md](docs/REBLOS-MIGRATION.md)**
for the design and rationale.

A REBLOS node is **closed by design**: it stores events from, and accepts uploads
from, only the members of the hubs you whitelist. It reads each hub's member tree
itself (no per-member setup) and keeps the hub durable by syncing its events and
blobs from the hub's own relays plus relays you control.

Licensed **MIT**: anyone can run a node.

## Quick install

On a fresh Ubuntu/Debian server whose domain's DNS A record already points at it:

```bash
curl -fsSL https://raw.githubusercontent.com/Freakoverse/REBLOS/main/deploy/install.sh | sudo bash
```

It asks for your domain, admin npub, relay key (generate or paste), and storage
backend, then sets up the node behind Caddy (automatic HTTPS). Open
`https://<your-domain>/dashboard`, sign in with your admin Nostr extension, and add
your hub by its `naddr` on the **Hubs** tab. Re-running the command updates the node
without touching your data; back up `/opt/reblos/deploy/data` and `.env`. See
[docs/REBLOS-MIGRATION.md](docs/REBLOS-MIGRATION.md) §7 for details.

## Features

- **Blossom blob server**: BUD-01/02/04/05/06 GET/HEAD/upload/delete, blobs
  addressed by SHA-256. Uploads are **member-gated**: only the admin, a trusted
  whitelisted key, a whitelisted hub's creator, or a member of a whitelisted hub.
- **Streaming uploads** up to 500 MB, hashed on the fly (a lying `Content-Length`
  can't overflow), with a **configurable** magic-byte type filter (default: text
  plus common image/audio/video; set `upload.allowed_types` to add types or `["*"]`
  for any), kind-24242 auth, a global concurrency cap plus one in-flight upload per
  npub, and a free-disk check.
- **Hub-scoped Nostr relay**: an embedded [badger](https://github.com/dgraph-io/badger)
  event store that whitelists hubs by coordinate (`36942:<pubkey>:<dTag>`), reads
  each hub's member tree, and accepts the NIP-CHAT kinds (hub event, messages,
  reactions, polls, reports, join requests, and more) **only from that hub's
  members**. Enforces an optional per-event proof-of-work (NIP-13) and honors author
  deletions (NIP-09) **persistently** so a removed event stays gone. `extra_kinds`
  adds non-gated kinds; `accept_all_kinds` turns it into a general-purpose relay.
- **Durability sync**: per whitelisted hub, pulls that hub's events + Blossom blobs
  (member tree, struct, member content) from the hub's own relay hints plus a
  configurable set of general relays, member-gated, so nothing is lost on downtime.
  Media a member fetches is mirrored on read (its hash is content-addressed, so no
  decryption is needed).
- **Moderation**: NIP-86 pubkey ban/allow, plus persistent **address-based
  takedowns**: a `<kind>:<pubkey>:<d>` coordinate stays rejected even if it is later
  re-published or re-synced.
- **Pluggable storage**: Cloudflare R2, self-hosted S3 (Garage / Ceph / SeaweedFS),
  or local disk. Cloud is one option, not a requirement.
- **Download gate**: [BUD-POW](docs/specs/BUD-POW.md), a stateless proof-of-work
  challenge (HTTP `428`) bound to the blob + client IP + expiry, as an anti-abuse
  floor on public downloads. Off by default; blobs from trusted keys are exempt.
- **Federation**: nodes announce their capabilities over Nostr; because blobs are
  content-addressed, any discovered node is a drop-in source for any hash.
- **Resilience**: an S3 circuit breaker fails fast during an outage, structured
  (`slog`) logging, and a fully static single binary.

## How it works

The node wraps [khatru](https://github.com/fiatjaf/khatru) (fiatjaf's Go relay +
Blossom framework) as a library rather than forking it. A streaming `/upload`
handler sits in front of the relay (khatru's built-in upload buffers the whole
file, unusable at 500 MB), and a gate middleware wraps blob `GET`/`HEAD` to enforce
the PoW challenge. A per-hub poster set, derived from each whitelisted hub's member
tree, decides which events and uploads are accepted. Storage is abstracted behind a
small `Storage` interface with R2/S3 and local-disk implementations.

## Quick start (Docker + Caddy)

The [`deploy/`](deploy/) directory has a Compose stack (node + Caddy for automatic
HTTPS):

```sh
cd deploy
cp config.yml.example config.yml     # backend, public URL, admin npub
# For R2/S3 only, put credentials in deploy/.env
#   R2_ACCESS_KEY=... / R2_SECRET_KEY=...  (or S3_ACCESS_KEY / S3_SECRET_KEY)
docker compose up -d --build
```

Full walkthrough (buy a server, set up storage, DNS, deploy, verify, whitelist a
hub, turn on the gate) is in the [deploy guide](docs/DEPLOY-GUIDE.md).

## Configuration

All options are documented in [`deploy/config.yml.example`](deploy/config.yml.example). Secrets
(R2/S3 keys) are read from the environment (`R2_ACCESS_KEY` / `R2_SECRET_KEY` or
`S3_ACCESS_KEY` / `S3_SECRET_KEY`) so they stay out of the config file. Key knobs:

| Section | What it controls |
|---|---|
| `backend` | `r2`, `s3`, or `disk` |
| `r2` / `s3` / `disk` | the selected backend's settings |
| `upload` | size cap, concurrency, min free disk, optional NIP-13 PoW, allowed types |
| `relay` | admin npub (NIP-86 + dashboard), min event PoW, `extra_kinds`, `accept_all_kinds` |
| `download` | PoW difficulty, challenge TTL, trusted client-IP header |
| `sync` | durability sync: refresh interval + extra relays to pull hub events/blobs from |
| `announce` | Nostr capability announcement for federation (on by default, weekly) |
| `log` | level + `text`/`json` format |

On first run the node generates its identity and prints its `npub`; the `nsec` is
saved to `data/identity.key` (used to sign the node's federation announcement). Back
it up.

## Running your node

Two settings decide who controls your relay and what it accepts.

### The admin key

```yaml
relay:
  admin_npub: "npub1youradminkey"
```

This npub is the only key allowed to use the **NIP-86 management API** and the
**admin dashboard** (whitelist hubs, ban/allow pubkeys, manage sync relays, take
down addresses). **Put your own npub here.** Leaving it empty is safe but closes the
admin surface to everyone, including you, so you cannot whitelist any hubs.

### What the relay accepts

By default this is a **NIP-CHAT, hub-scoped** relay. It stores and serves the
NIP-CHAT kinds (hub event `36942`, messages, reactions, polls, reports, join
requests, and the rest) **only for hubs you have whitelisted**, and **only from
those hubs' members** (derived from each hub's member tree). Everything else is
rejected on write and on read.

- `relay.extra_kinds` accepts additional kinds beyond the NIP-CHAT set. These are
  **not** member-gated; they pass the kind allowlist and `min_event_pow` only (e.g.
  `[1059]` for gift-wrapped DMs).
- `relay.accept_all_kinds: true` drops the kind allowlist **and** the member gate:
  any kind, from anyone, for a general-purpose relay. Banned pubkeys, address
  takedowns, and honored NIP-09 deletions still apply. **Set `min_event_pow` when
  you do this**, since with the scope gone proof-of-work is the only remaining floor.

```yaml
relay:
  accept_all_kinds: true
  min_event_pow: 20        # strongly recommended when you open it up
```

## Download gate

The gate is off by default. When enabled, an ungated blob `GET` gets a `428` with an
`X-Blossom-Gate-Pow` challenge; the client mines it and retries with the proof
header.

- **PoW** ([BUD-POW](docs/specs/BUD-POW.md)) is a stateless HMAC challenge bound to
  the blob + client IP + expiry, as an anti-abuse floor on public downloads.
- Blobs uploaded by a trusted (whitelisted) key are **exempt**, so a shared link to
  one stays directly downloadable. A `HEAD` existence check is never gated.

## Storage & retention

Blobs are keyed `<sha256>.<ext>`. Hub files are meant to **persist**: there is no
automatic pruning or expiry. Deletion is manual only: the authenticated Blossom
`DELETE /<hash>` path (multi-owner, last-owner-out) and the admin API.

## Admin API

Management endpoints authenticated with **NIP-98** (a kind-27235 event signed by
`relay.admin_npub`), so an admin signs each request in the browser (NIP-07) with no
raw key on the server. All require `Authorization: Nostr <base64(event)>`.

- `GET|POST|DELETE /admin/hubs`: the hub whitelist. List hubs (with member count +
  epoch), add one by `naddr` or `36942:<pubkey>:<dTag>` coordinate, remove one.
- `GET|POST|PUT|DELETE /admin/sync-relays`: the durability sync-relay list. List,
  add a custom relay, toggle one on/off, remove one (a default is disabled, a custom
  is deleted).
- `GET /admin/blobs?search=&page=&per=`: list stored blobs (hash, ext, size, url),
  filtered by a hash substring, numbered pagination (cached 30s).
- `GET /admin/whitelist`: the upload-size whitelist + the normal/5× caps.
- `POST /admin/whitelist` `{ "pubkey", "note?" }`: grant a pubkey the **5× upload
  cap** and gate exemption (accepts npub or hex). `DELETE` revokes it.
- `GET|POST|DELETE /admin/banned-events`: persistent, **address-based** event
  takedowns. A `<kind>:<pubkey>:<d>` coordinate stays rejected even if it is later
  re-published or re-synced from another relay.
- `GET|POST|DELETE /admin/blacklist` and `DELETE /admin/quarantine`: bar a blob hash
  from upload/download, or release a reversibly-quarantined blob.
- `DELETE /<hash>`: remove a blob (admin-signed kind-24242 `t=delete`).

Pubkey **bans** use the relay's NIP-86 API and are unified: a banned key can neither
post hub events nor upload blobs.

## Build & run from source

Requires Go 1.25+.

```sh
cp deploy/config.yml.example config.yml   # edit; set backend + public_url + admin_npub
go run ./cmd/reblos -config config.yml
go test ./...                             # unit tests (no cloud needed)
```

## Project layout

```
cmd/reblos         entrypoint (config load, storage factory, announce + sync wiring)
internal/config    YAML config (+ env overrides for secrets)
internal/identity  node Nostr key (generated on first run)
internal/hub       hub whitelist, member-tree parsing, poster-set cache, blob fetcher
internal/storage   Storage interface + backends: r2.go (R2/S3), disk.go (local), breaker.go
internal/server    khatru relay + Blossom handler, streaming upload, PoW gate,
                   hub whitelist + poster-set gating, durability sync, moderation
internal/announce  Nostr capability announcement (federation discovery)
dashboard/         embedded admin SPA (Hubs, Files, Relay, Sync, Whitelist, Settings)
docs/              deploy guide + migration/design doc + BUD specs
deploy/            Docker Compose + Caddy + installer
```

## License

[MIT](LICENSE).

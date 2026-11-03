# Deploy guide

A start-to-finish walkthrough for running your own **REBLOS** node: a NIP-CHAT
**Nostr relay + Blossom blob server** that hosts your hubs. No prior Nostr or
Blossom knowledge needed.

You end up with a small VPS running the node + [Caddy](https://caddyserver.com)
(automatic HTTPS) behind a domain you own, storing blobs on local disk, **Cloudflare
R2**, or a **self-hosted S3** on the same box. Everything runs in Docker.

A REBLOS node is **closed by design**: it only stores events from, and accepts
uploads from, members of the hubs you whitelist. It reads each hub's member tree
itself (no per-member configuration) and keeps the hub durable by syncing its events
and blobs from the hub's own relays plus a set of general relays you control.

---

## Fast path: the one-command installer

On a fresh Ubuntu/Debian server whose domain already points at it:

```bash
curl -fsSL https://raw.githubusercontent.com/Freakoverse/REBLOS/main/deploy/install.sh | sudo bash
```

It asks for your domain, admin npub, whether to generate a relay key (or paste one),
and a storage backend, then installs Docker, writes the config, obtains HTTPS, and
starts the node. It is **idempotent**: re-run it to update, and it never clobbers
your identity key or data. When it finishes it prints your node URL, the dashboard
URL, and the relay's npub.

Then open `https://your-domain/dashboard`, sign in with your admin Nostr extension,
and add your hub by its `naddr` (Hubs tab). That's it.

The rest of this guide is the **manual path**: the same result, step by step, for
when you want full control or the installer doesn't fit your environment.

---

## 0. What you need

- **A server (VPS).** 2 GB RAM is plenty. Disk: ~20 GB is fine with R2 (blobs live in
  the cloud); **60 GB+** if you store blobs on the box (`disk` or self-hosted S3).
  Ubuntu 22.04 or 24.04. DigitalOcean / Hetzner / Vultr / BitLaunch all work the same.
- **A domain** (or a subdomain like `relay.yourdomain.com`) whose DNS you can edit.
- **Your admin npub**: the Nostr key that will manage the node (dashboard + NIP-86).
- **~30 minutes.**

Two decisions up front, both changeable later:
- **Storage:** local `disk` (simplest), Cloudflare R2 (§2A), or self-hosted S3 (§2B).
- **Download gate:** leave the PoW gate **off** for the initial bring-up; turn it on
  once everything works (§8).

---

## 1. Point your domain at the server

1. Create the server (§0) and note its **public IPv4**.
2. At your **domain registrar / DNS host** (Namecheap, Porkbun, Cloudflare, etc.), open
   the domain's DNS settings and add an **A record**:
   - Host / Name: `relay` (for `relay.yourdomain.com`), or `@` for the root domain.
   - Value / Points to: the server's IP.
   - TTL: automatic / default.
3. Give DNS a few minutes to propagate. Verify with a global checker like
   [dnschecker.org](https://dnschecker.org) rather than a local `nslookup` (your own
   machine may cache an old result).

> **Using Cloudflare for DNS?** Add the A record there but set it to **"DNS only" (grey
> cloud), not "Proxied"**: the node terminates HTTPS itself, and proxying breaks the
> certificate challenge and the real-client-IP the download gate relies on.

---

## 2. Set up storage

Pick **one**. (You can start on `disk` and move to R2/S3 later; back up `data/` first.)

### 2A. Local disk (simplest)

Nothing to set up now: blobs live under the mounted `data/blobs` directory on the box.
Just make sure the server has enough disk for the hubs you host. You'll set
`backend: "disk"` in §4.

### 2B. Cloudflare R2 (managed)

1. In the Cloudflare dashboard → **R2** → **Create bucket** (e.g. `hub-storage`).
   Keep it **private**: the node reads/writes it; the public never touches R2 directly.
2. **R2 → Manage R2 API Tokens → Create API token**, scoped to that bucket with
   **Object Read & Write**. Copy the **Access Key ID** and **Secret Access Key** (shown
   once).
3. Note your **S3 endpoint**: `https://<accountid>.r2.cloudflarestorage.com`
   (the account id is in the R2 → *Settings → S3 API* panel).

You'll use `backend: "r2"` with these values in §4.

### 2C. Self-hosted S3 on the same box (Garage)

Everything stays on one server, no cloud account. You add
[Garage](https://garagehq.deuxfleurs.fr) (a lightweight, self-hostable, S3-compatible
object store) to the same Docker stack, so the node reaches it privately over the
internal network. You'll set it up in §4: an extra service in the compose file plus a
one-time bucket + access key. A few more steps than a managed bucket, but nothing
leaves your box. (For most single-box deploys, `disk` is simpler and does the same job.)

---

## 3. Prepare the server

SSH in (`ssh root@YOUR_IP` or your user), then install Docker:

```bash
# Docker Engine + Compose plugin (official convenience script)
curl -fsSL https://get.docker.com | sh
docker --version && docker compose version   # confirm both work
```

Open the firewall for web + SSH (if you use `ufw`):

```bash
sudo ufw allow 22 && sudo ufw allow 80 && sudo ufw allow 443 && sudo ufw enable
```

---

## 4. Get the code and configure

```bash
git clone https://github.com/Freakoverse/REBLOS ~/reblos
cd ~/reblos/deploy
cp config.yml.example config.yml
```

**Secrets → `.env`** (never committed). Create `deploy/.env` only if you use R2 or S3:

```
# R2:
R2_ACCESS_KEY=your-r2-access-key
R2_SECRET_KEY=your-r2-secret-key
# or self-hosted S3 (Garage/MinIO/Ceph):
# S3_ACCESS_KEY=...
# S3_SECRET_KEY=...
```

For local `disk`, no `.env` is needed.

**Config → `config.yml`.** Edit at least:

- `public_url` → `https://relay.yourdomain.com` (**must** be your real hostname, it's
  what the node announces to the network).
- `relay.admin_npub` → **your** npub (the only key allowed to manage the node).
- `backend` + the matching storage block (below).
- Leave `download.pow_difficulty: 0` **for now**.

**Set your domain in the Caddyfile** (`deploy/Caddyfile`): replace the placeholder host
with `relay.yourdomain.com` so Caddy issues the right certificate.

#### Storage config

**Disk:**
```yaml
backend: "disk"
disk:
  path: "/app/data/blobs"
```

**R2:**
```yaml
backend: "r2"
r2:
  endpoint: "<accountid>.r2.cloudflarestorage.com"
  region: "auto"
  bucket: "hub-storage"
  use_ssl: true
```

**Self-hosted Garage (same box).** Add a `garage` service to `docker-compose.yml`:

```yaml
  garage:
    image: dxflrs/garage:v1.0.1
    restart: unless-stopped
    volumes:
      - ./garage.toml:/etc/garage.toml:ro
      - ./garage-meta:/var/lib/garage/meta
      - ./garage-data:/var/lib/garage/data
```

Create `deploy/garage.toml` (generate the secret with `openssl rand -hex 32`):

```toml
metadata_dir = "/var/lib/garage/meta"
data_dir     = "/var/lib/garage/data"
db_engine    = "sqlite"
replication_factor = 1

rpc_bind_addr   = "[::]:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret      = "PASTE_64_HEX_CHARS_HERE"

[s3_api]
s3_region     = "garage"
api_bind_addr = "[::]:3900"
```

Start it, then initialise a single-node cluster + bucket + key (run these once):

```bash
docker compose up -d garage
docker compose exec garage /garage status                 # note the node ID (first column)
docker compose exec garage /garage layout assign -z dc1 -c 50G <NODE_ID>
docker compose exec garage /garage layout apply --version 1
docker compose exec garage /garage bucket create hub-storage
docker compose exec garage /garage key create hub-key     # prints a Key ID + Secret, copy them
docker compose exec garage /garage bucket allow --read --write hub-storage --key hub-key
```

Put the printed **Key ID / Secret** in `.env` as `S3_ACCESS_KEY` / `S3_SECRET_KEY`, then
set the node's storage block:

```yaml
backend: "s3"
s3:
  endpoint: "garage:3900"    # the compose service name; path-style
  region: "garage"
  bucket: "hub-storage"
  use_ssl: false             # internal network, no TLS needed
  path_style: true
```

---

## 5. Deploy

```bash
cd ~/reblos/deploy
docker compose up -d --build          # first run builds the node image (~1-2 min)
```

Caddy automatically obtains an HTTPS certificate for your domain on first request.

On its **first run the node generates its identity** and prints its `npub` in the logs
(the matching `nsec` is saved to `deploy/data/identity.key`). This key signs the node's
federation announcement. **Back up `identity.key`**.

---

## 6. Verify it's up

```bash
docker compose logs node | grep "node listening"
```
You want a line like `... addr=:3000 storage=disk public_url=… pow_bits=0`. Then from
your laptop:

```bash
curl -I https://relay.yourdomain.com/            # should answer over HTTPS (a 404 is fine)
```

Open `https://relay.yourdomain.com/dashboard` and sign in with your admin Nostr
extension. If a **502 from Caddy** appears, the node container isn't up yet: check
`docker compose logs node` for a crash (usually a bad storage endpoint or missing
`.env` secret).

---

## 7. Whitelist your hub

A fresh node hosts nothing until you whitelist a hub. In the dashboard:

1. **Hubs tab → Add hub.** Paste the hub's `naddr` (or a raw
   `36942:<pubkey>:<dTag>` coordinate). The node reads that hub's member tree and from
   then on accepts events and uploads **only from its members**, and serves only its
   content.
2. **Sync tab** (optional): the node pulls each hub's events + blobs from the hub's own
   relay hints plus a curated set of general relays, so nothing is lost on downtime.
   Toggle a default relay off if you don't want it, or add your own.

The member tree, hub-config struct, and member content are mirrored automatically;
media is mirrored the first time a member fetches it. Nothing per-member to configure.

---

## 8. Point your client at the node

In your NIP-CHAT client (DEN Chat), add `https://relay.yourdomain.com` to the Blossom
server list and `wss://relay.yourdomain.com` to the relay list. A hub owner who marks
this node as the hub's dedicated relay + Blossom server will have members publish and
upload here directly.

---

## 9. Turn on the download gate (optional)

The PoW download gate protects the node from scraping/abuse. Do this **after** the
basics work.

```yaml
download:
  pow_difficulty: 18     # ~1-3s of work per download; 0 = off
  challenge_ttl: 600
  trusted_ip_header: "X-Forwarded-For"
```

Apply and confirm the running node picked it up:

```bash
docker compose up -d --force-recreate node
docker compose logs --tail=20 node | grep "node listening"   # want pow_bits=18
```

A visitor's client mines the PoW, then the download completes. The gate only applies to
blobs served **by your node**. Blobs uploaded by a whitelisted (trusted) key are
exempt, so a shared link to one stays directly downloadable.

---

## 10. Updates and backup

**Update** (keeps all data):
```bash
cd ~/reblos/deploy
git pull
docker compose up -d --build
```
Or re-run the installer: it fetches the latest and restarts without touching your
identity key or data. (Once a release image is published you can switch the compose
`build:` block to `image: ghcr.io/freakoverse/reblos:latest` and update with
`docker compose pull && docker compose up -d`.)

All node state lives **outside the image**: the mounted `deploy/data/` volume (identity
key, event store, `*.json` state, and disk blobs) plus `config.yml` and `.env`. Startup
schema migrations run automatically, so updates don't lose data. **Back up `deploy/data`
and `deploy/.env`**: that is your whole node.

---

## 11. What to adjust (config reference)

Everything lives in `config.yml` (secrets in `.env`). The knobs you're most likely to touch:

| Key | What it does |
|---|---|
| `public_url` | your hostname, **what the node announces**; must be correct |
| `backend` + `disk`/`r2`/`s3` | where blobs are stored |
| `relay.admin_npub` | the key allowed to manage the node (dashboard + NIP-86) |
| `relay.min_event_pow` | NIP-13 PoW required on accepted events (0 = off) |
| `relay.extra_kinds` | extra event kinds to accept beyond the NIP-CHAT set (not member-gated) |
| `relay.accept_all_kinds` | drop the kind allowlist AND the member gate (general-purpose fork) |
| `upload.max_size_mb` | per-file cap (default 500) |
| `upload.allowed_types` | accepted file types by magic bytes; default is text + common media; `["*"]` = any; add `"zip"`/`"rar"`/`"7z"`/`"exe"`/`"gz"` for archives/binaries |
| `download.pow_difficulty` | the PoW download gate (§9); 0 = off |
| `sync.interval_minutes` / `extra_relays` | durability sync cadence + extra relays to pull hub events/blobs from (managed from the dashboard Sync tab) |
| `announce.enabled` / `relays` | advertise your node for discovery (on by default, weekly) |

After any config change: `docker compose up -d --force-recreate node`, then check the
startup log to confirm it took effect.

---

## Troubleshooting

- **Config change didn't apply.** A plain restart sometimes reuses the old process:
  use `docker compose up -d --force-recreate node`, then verify via the `node listening`
  log line. The config file is mounted, so you don't rebuild for config-only edits.
- **`no configuration file provided`.** Run `docker compose` from `~/reblos/deploy`
  (where `docker-compose.yml` lives), not the repo root.
- **502 Bad Gateway.** The node crashed or isn't ready: `docker compose logs node`.
  Most often a wrong storage endpoint/bucket or a missing `.env` secret.
- **Permission denied on `/app/data`.** The image drops to a non-root user and fixes
  ownership on start; if you created `deploy/data` manually as root, `sudo chown -R`
  it or just delete it and let the container recreate it.
- **Certificate won't issue.** Port 80 must be open and the domain's A record must point
  at this box (DNS-only, not proxied). Check `docker compose logs caddy`.
- **My events/uploads are rejected.** The node only accepts events and uploads from
  members of a **whitelisted** hub. Confirm the hub is added (Hubs tab) and that your
  key is in its member tree; the admin key and trusted-whitelisted keys are always
  allowed.
- **Download gate never fires.** The blob must be served **by your node**, the gate must
  be on (`pow_bits>0` in the startup log), and the blob must not have been uploaded by a
  trusted key (those are gate-exempt). Content-addressed files also cache in the
  browser, so hard-refresh when testing.

Full option docs: [`deploy/config.yml.example`](../deploy/config.yml.example). Node
internals and the admin API: [`README.md`](../README.md). Design + rationale:
[`docs/REBLOS-MIGRATION.md`](REBLOS-MIGRATION.md).

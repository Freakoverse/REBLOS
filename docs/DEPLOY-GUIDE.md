# Deploy guide

A start-to-finish walkthrough for running your own **blossom-relay-server** node —
from buying a server to a live, HTTPS node serving mod files. No prior Nostr or
Blossom knowledge needed.

You end up with: a small VPS running the node + [Caddy](https://caddyserver.com)
(automatic HTTPS) behind a domain you own, storing files in either **Cloudflare R2**
(managed) or a **self-hosted S3** on the same box. Everything runs in Docker.

---

## 0. What you need

- **A server (VPS).** 2 GB RAM is plenty. Disk: ~20 GB is fine with R2 (files live in
  the cloud); **60 GB+** if you self-host storage (files live on the box). Ubuntu 22.04
  or 24.04. [BitLaunch](https://bitlaunch.io) is a good, simple option (pay with card
  or crypto); DigitalOcean / Hetzner / Vultr work identically.
- **A domain** (or a subdomain like `brs.yourdomain.com`) whose DNS you can edit.
- **~30 minutes.**

Two decisions up front, both changeable later:
- **Storage:** Cloudflare R2 (§2A — easiest, generous free tier) or self-hosted S3
  (§2B — everything on one box, no cloud account).
- **Gates:** leave the download PoW/ad gates **off** for the initial bring-up; turn
  them on once it works (§8).

---

## 1. Point your domain at the server

1. Create the server (§0) and note its **public IPv4**.
2. At your **domain registrar / DNS host** (Namecheap, Porkbun, GoDaddy, etc.), open the
   domain's DNS settings and add an **A record**:
   - Host / Name: `brs` (for `brs.yourdomain.com`) — or `@` for the root domain.
   - Value / Points to: the server's IP.
   - TTL: automatic / default.
3. Give DNS a few minutes to propagate. Your own machine may cache an old result —
   verify with a global checker like [dnschecker.org](https://dnschecker.org) rather than
   a local `nslookup`.

> **Using Cloudflare for DNS?** Add the A record there but set it to **"DNS only" (grey
> cloud), not "Proxied"** — the node terminates HTTPS itself, and proxying breaks the
> certificate challenge and the real-client-IP the gates rely on.

---

## 2. Set up storage

Pick **one**.

### 2A. Cloudflare R2 (managed)

1. In the Cloudflare dashboard → **R2** → **Create bucket** (e.g. `mods-storage`).
   Keep it **private** — the node reads/writes it; the public never touches R2 directly.
2. **R2 → Manage R2 API Tokens → Create API token**, scoped to that bucket with
   **Object Read & Write**. Copy the **Access Key ID** and **Secret Access Key** (shown
   once).
3. Note your **S3 endpoint**: `https://<accountid>.r2.cloudflarestorage.com`
   (the account id is in the R2 → *Settings → S3 API* panel).

You'll use `backend: "r2"` with these values in §4.

### 2B. Self-hosted S3 on the same box (Garage)

Everything stays on one server — no cloud account. You'll add
[Garage](https://garagehq.deuxfleurs.fr) (a lightweight, self-hostable, S3-compatible
object store) to the same Docker stack, so the node reaches it privately over the
internal network.

You'll set it up in §4 — an extra service in the compose file plus a one-time bucket +
access key. A few more steps than a managed bucket, but nothing leaves your box.

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
git clone https://github.com/DEGMods/Blossom-Relay-Server ~/degnode
cd ~/degnode/deploy
cp .env.example .env
cp config.yml.example config.yml
```

**Secrets → `.env`** (never committed). For R2:

```
R2_ACCESS_KEY=your-r2-access-key
R2_SECRET_KEY=your-r2-secret-key
```

For self-hosted Garage, use `S3_ACCESS_KEY` / `S3_SECRET_KEY` instead (you'll paste the
key Garage prints for you below).

**Config → `config.yml`.** Edit at least:

- `public_url` → `https://brs.yourdomain.com` (**must** be your real hostname — it's
  what the node announces to the network).
- `relay.admin_npub` → **your** npub (the only key allowed to moderate / manage the node).
- The storage block (below).
- Leave `download.pow_difficulty: 0` and `download.ad_gate: false` **for now**.

**Set your domain in the Caddyfile** (`deploy/Caddyfile`) — replace the placeholder host
with `brs.yourdomain.com` so Caddy issues the right certificate.

#### Storage config

**R2:**
```yaml
backend: "r2"
r2:
  endpoint: "<accountid>.r2.cloudflarestorage.com"
  region: "auto"
  bucket: "mods-storage"
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
docker compose exec garage /garage bucket create mods-storage
docker compose exec garage /garage key create mods-key    # prints a Key ID + Secret — copy them
docker compose exec garage /garage bucket allow --read --write mods-storage --key mods-key
```

Put the printed **Key ID / Secret** in `.env` as `S3_ACCESS_KEY` / `S3_SECRET_KEY`, then
set the node's storage block:

```yaml
backend: "s3"
s3:
  endpoint: "garage:3900"    # the compose service name; path-style
  region: "garage"
  bucket: "mods-storage"
  use_ssl: false             # internal network, no TLS needed
  path_style: true
```

---

## 5. Deploy

```bash
cd ~/degnode/deploy
docker compose up -d --build          # first run builds the node image (~1–2 min)
```

Caddy automatically obtains an HTTPS certificate for your domain on first request.

On its **first run the node generates its identity** and prints its `npub` in the logs
(the matching `nsec` is saved to `deploy/data/identity.key`). This key signs the node's
ad inventory and its federation announcement — back up `identity.key`.

---

## 6. Verify it's up

```bash
docker compose logs node | grep "node listening"
```
You want a line like `... pow_bits=0 ad_gate=true|false storage=r2 public_url=…`. Then
from your laptop:

```bash
curl -I https://brs.yourdomain.com/            # should answer over HTTPS (a 404 is fine)
```

**Upload smoke test** (any small `.zip`) via the DEG Mods client, or confirm read parity
by fetching a known blob hash you migrated. If you get a **502 from Caddy**, the node
container isn't up yet — check `docker compose logs node` for a crash (usually a bad
storage endpoint or missing `.env` secret).

---

## 7. Point the client at your node (if self-hosting the whole stack)

In the DEG Mods client, add `https://brs.yourdomain.com` to the Blossom server list and
`wss://brs.yourdomain.com` to the relay list (Settings → Network). For the official
DEG Mods deployment these are already the defaults.

---

## 8. Turn on the download gates (optional)

Gates fund/protect the node. Do this **after** the basics work.

1. **Ad inventory** — in the client, **Settings → Admin → Ads → Download-gate ads**, add
   an ad and **Publish**. (Signed by the node key; the node serves it.)
2. Enable in `config.yml`:
   ```yaml
   download:
     pow_difficulty: 16     # ~2–3s of work per download; 0 = off. Legacy/anti-abuse knob.
     ad_gate: true          # show the ad before a download
   ```
3. Apply and confirm the running node picked it up:
   ```bash
   docker compose up -d --force-recreate node
   docker compose logs --tail=20 node | grep "node listening"   # want pow_bits=16 ad_gate=true
   ```

A visitor's client will now mine the PoW + view the ad, then the download completes.
The gate only applies to blobs served **by your node**.

---

## 9. What to adjust (config reference)

Everything lives in `config.yml` (secrets in `.env`). The knobs you're most likely to touch:

| Key | What it does |
|---|---|
| `public_url` | your hostname — **what the node announces**; must be correct |
| `backend` + `r2`/`s3`/`disk` | where files are stored |
| `relay.admin_npub` | the key allowed to moderate/manage (NIP-86 + admin API) |
| `relay.min_event_pow` | NIP-13 PoW required on **current** mods (legacy mods are exempt) |
| `upload.max_size_mb` | per-file cap (default 500) |
| `upload.allowed_types` | accepted file types by magic bytes — `["zip"]` default; `["*"]` = any; e.g. `["zip","rar","7z"]` |
| `download.pow_difficulty` / `ad_gate` | the download gates (§8) |
| `ingest.relays` | relays to mirror mods from, so your node is a complete DB |
| `announce.enabled` / `relays` | advertise your node for discovery (on by default, weekly) |

After any config change: `docker compose up -d --force-recreate node`, then check the
startup log to confirm it took effect.

---

## Troubleshooting

- **Config change didn't apply.** A plain restart sometimes reuses the old process —
  use `docker compose up -d --force-recreate node`, then verify via the `node listening`
  log line. The config file is mounted, so you don't rebuild for config-only edits.
- **`no configuration file provided`.** Run `docker compose` from `~/degnode/deploy`
  (where `docker-compose.yml` lives), not the repo root.
- **502 Bad Gateway.** The node crashed or isn't ready — `docker compose logs node`.
  Most often a wrong storage endpoint/bucket or a missing `.env` secret.
- **Permission denied on `/app/data`.** The image drops to a non-root user and fixes
  ownership on start; if you created `deploy/data` manually as root, `sudo chown -R`
  it or just delete it and let the container recreate it.
- **Certificate won't issue.** Port 80 must be open and the domain's A record must point
  at this box (DNS-only, not proxied). Check `docker compose logs caddy`.
- **Gate never fires in the browser.** The blob must be served **by your node** (not an
  old/other host), and gates must be on (`pow_bits>0` or `ad_gate=true` in the startup
  log). Content-addressed files also cache in the browser — hard-refresh when testing.

Full option docs: [`deploy/config.yml.example`](../deploy/config.yml.example). Node internals
and the admin API: [`README.md`](../README.md).

# Deploying a DEG Mods node (Docker + Caddy)

Step-by-step to run the node on a fresh Ubuntu VPS behind Caddy (automatic HTTPS),
pointed at the same R2 bucket as `bs.degmods.com`, then cut traffic over.

Files referenced live in [`../deploy/`](../deploy/):
`docker-compose.yml`, `Caddyfile`, `.env.example`, `config.yml.example`.

**The golden rule of cutover:** blobs are addressed by their SHA-256, so the new
node and the old server are interchangeable for any hash they both can reach.
Nothing breaks while both run — that's what makes this safe.

---

## Prerequisites

- An Ubuntu 22.04/24.04 VPS you can SSH into (2 GB RAM is enough).
- The hostname `brs.degmods.com` available to point at it.
- DNS managed at **Namecheap** (the domain registrar). **Cloudflare is used only
  for R2 storage** — it is not in the request path for the node.

---

## Step 1 — DNS (Namecheap)

Point the hostname straight at the box **first**, so the TLS cert can issue later.

1. Get the VPS public IP (`curl -4 ifconfig.me` on the box).
2. Namecheap → Domain List → `degmods.com` → **Manage** → **Advanced DNS** →
   **Add New Record**:
   - Type `A Record`, Host `brs`, Value = the VPS IP, TTL Automatic.
   - Remove any conflicting parking/URL-redirect record for the same host.
3. Verify from your laptop: `nslookup brs.degmods.com` returns the VPS IP.
   Namecheap propagation can take a few minutes (occasionally up to ~30). **Wait
   until it resolves before Step 4** — Caddy's cert issuance fails otherwise.

Because traffic goes directly to Caddy (no Cloudflare proxy), the node reads the
real client IP from the `X-Forwarded-For` header Caddy sets — already the default
in `config.yml.example`.

---

## Step 2 — Harden the box + install Docker

SSH in, then:

```sh
# Non-root sudo user (skip if you already have one). As root:
adduser deg && usermod -aG sudo deg
# ...then reconnect as deg.

# Firewall: allow SSH + web only.
sudo ufw allow OpenSSH
sudo ufw allow 80,443/tcp
sudo ufw enable

# Docker Engine + compose plugin (official convenience script).
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker $USER
# Log out and back in so the docker group applies, then verify:
docker run --rm hello-world
```

---

## Step 3 — Get the node onto the box + configure

**Clone the repo** on the box:

```sh
git clone https://github.com/DEGMods/Blossom-Relay-Server.git ~/degnode
cd ~/degnode/deploy
cp .env.example .env
cp config.yml.example config.yml
```

**Create a bucket-scoped R2 token** (Cloudflare → R2 → Manage R2 API Tokens →
Create): permission **Object Read & Write**, scoped to the `deg-blossom-storage`
bucket. Copy the Access Key ID + Secret. Put them in `.env`:

```sh
nano .env      # R2_ACCESS_KEY=... / R2_SECRET_KEY=...
```

**Edit `config.yml`:** set `r2.endpoint` (your account's S3 API host) and
`relay.admin_npub` (your npub). Leave all gates OFF for now (`pow_difficulty: 0`,
`ad_gate: false`) — we verify plain serving first.

> Secrets live only in `.env` (git-ignored) and are passed as env vars. Never put
> R2 keys in `config.yml` or commit them.

---

## Step 4 — Launch + verify

```sh
cd ~/degnode/deploy
docker compose up -d          # builds the node image, starts node + Caddy
docker compose logs -f caddy  # watch for the cert being issued (Ctrl-C to stop)
docker compose logs -f node   # should show {"msg":"node listening",...}
```

Verify (from your laptop):

```sh
# 1. TLS + relay info document
curl -sI https://brs.degmods.com/ -H "Accept: application/nostr+json"
#    → 200, content-type application/nostr+json

# 2. READ PARITY: fetch a blob you know exists in the bucket, by hash.
curl -s https://brs.degmods.com/<known-sha256>.zip -o out.zip
sha256sum out.zip            # must equal <known-sha256>
```

If read parity passes, the new node is serving the existing library correctly.

---

## Step 5 — Upload test

From the DEG Mods app (or `curl` with a kind-24242 auth), upload a small `.zip`
and confirm it returns a descriptor and is retrievable by hash. This exercises the
streaming upload → R2 write path end to end.

---

## Step 6 — Turn on the gates (optional, one at a time)

Edit `config.yml`, then `docker compose up -d` (recreates the node with new config).

- **PoW gate:** `download.pow_difficulty: 18` (tune 16–22). A cold `GET` now
  returns `428`; the app's download modal mines it and retries.
- **Ad gate:** publish your ad inventory first (a `kind:30078`, `d=manual-blossom-ads`
  event signed by the node key — its npub is printed on first run / in
  `data/identity.key`), then set `download.ad_gate: true`.
- **Federation announce:** `announce.enabled: true` to advertise the node.

Re-verify a download after each change.

---

## Step 7 — Cut traffic over

The DEG Mods client already does multi-server failover. Add `brs.degmods.com` to
the app's Blossom server list **alongside** `bs.degmods.com` and ship it. Both
serve the same hashes, so downloads/uploads keep working while traffic spreads.

Watch the node logs + R2 metrics for a few days. When `brs` is proven at parity
(reads, uploads, gates), make it the primary.

---

## Step 8 — Retire the old server

Once `brs` carries the traffic and you're confident:

1. Remove `bs.degmods.com` from the app's server list (or leave it as a read
   mirror for a grace period — content-addressing means it stays valid).
2. Decommission the old Koa server.

---

## Operating the node

```sh
docker compose logs -f node          # tail logs
docker compose restart node          # restart
docker compose down                  # stop everything
git -C ~/degnode pull  # or re-scp, then:
docker compose up -d --build node    # update to a new node build
```

**Backups:** `~/degnode/deploy/data/` holds the node identity key
(`identity.key`), the relay event store, and ad metrics. Back it up. Losing
`identity.key` means a new npub (and your ad inventory would need re-signing).

**Rollback:** if anything misbehaves, `docker compose down` the new node and the
app keeps serving from `bs.degmods.com` — you never removed it during cutover.

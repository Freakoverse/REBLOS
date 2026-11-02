# BUD-Ads: Ad view required

`draft` · `optional`

Defines a way for a Blossom server to require a client to **display an
advertisement** before it serves a blob, to fund server egress/storage **without
charging the user or requiring a login**. The ad inventory is served over Nostr
(NIP-78), so it is self-hosted, portable across clients, and independent of any
third-party ad network.

This document is self-contained and can be implemented on its own, in parallel to
BUD-07 (Payment required). It mirrors BUD-07's request/retry shape but is
deliberately **not** a payment flow (it uses `428`, see "Handshake"). It composes
with other retrieval gates such as [BUD-POW](./BUD-POW.md) — see "Composition".

> A future BUD may unify proof-of-work, ad-view, and payment gates into one
> document; until then each is specified independently.

## Handshake

1. Client requests a blob: `GET /<sha256>` or `GET /<sha256>.<ext>`.
2. If the server requires an ad view for this blob and the request carries no valid
   proof, it responds `428 Precondition Required` with the `X-Blossom-Gate-Ad`
   header (below). `428` (not BUD-07's `402`) is used so payment clients do not
   mistake this for a payment demand; a client that doesn't implement this BUD gets
   a `428` it can't satisfy and fails over.
3. The client displays an ad, then retries the identical `GET` with the
   `X-Blossom-Gate-Ad-Proof` header (below).

## Trust model (read first)

An ad *view* cannot be proven cryptographically — a modified client can send the
proof without rendering anything. This is therefore a **cooperative** gate:
enforcement relies on honest client implementations, and accountability comes from
(a) the reference client actually rendering the ad, and (b) the server recording
impressions/clicks that the **advertiser reconciles against their own analytics**.
Do not rely on the ad gate for abuse prevention — use `pow` for that. The ad gate
exists for **funding, not security**.

## Ad inventory (NIP-78)

Each operator publishes their own inventory, signed by the operator's (node) key:

- `kind`: `30078`
- `d` tag: `manual-blossom-ads`
- `content`: JSON `{ "ads": [ Ad, … ] }`

```jsonc
Ad = {
  "id":     string,   // stable id; PRIMARY KEY for metrics (never the link)
  "media":  string,   // image/media URL to display
  "link":   string,   // click-through URL
  "alt":    string,   // accessible text
  "weight": number?   // optional rotation weight (default: 1)
}
```

The `id` is the metrics key so that editing `link`/`media` does not fragment
history.

## Rotation (client)

To avoid showing the same ad over and over, clients SHOULD rotate using a
**weighted shuffle-bag**, mirroring the DEG Mods site's round-robin picker:

1. Build a cycle "bag" containing `weight` entries per ad (`weight` defaults to `1`).
2. Track which entries have been drawn this cycle, persisted across loads (e.g.
   `localStorage`, keyed by the inventory `ref`). Prune tracking for ads no longer
   in the inventory (handles operator edits).
3. Draw a random not-yet-drawn ad from the bag; if it equals the immediately
   previous ad and another option exists, draw again (no consecutive repeats).
4. When the bag is exhausted, reset the cycle.

This shows every ad (weighted by frequency) once per cycle before any repeats, and
never the same ad twice in a row when alternatives exist.

## Challenge (server → client)

In the `428` response:

```http
X-Blossom-Gate-Ad: v=1; ref=30078:<node-pubkey>:manual-blossom-ads; min=1000; c=<challenge>
```

- `ref` — coordinate of the operator's ad-inventory event.
- `min` — minimum on-screen time in **milliseconds** (SHOULD default to `1000`).
- `c` — an opaque stateless challenge that MUST bind to `{ sha256, ip, exp }`
  (recommended: `base64url(payload) "." base64url(HMAC-SHA256(server_key, payload))`),
  echoed verbatim in the proof. **IP source:** behind a proxy/CDN the server MUST
  read the client IP from the trusted forwarded header (e.g. `CF-Connecting-IP`).

## Flow (client)

1. Fetch the event referenced by `ref`; verify it is signed by `<node-pubkey>`.
2. Select an ad; render its `media` in the UI, linking to `link`.
3. Keep it **actually visible and the tab focused** for ≥ `min` ms. Clients SHOULD
   use IntersectionObserver + the Page Visibility API, not a background timer.
4. Record the impression locally and report it (see Metrics).

## Proof (client → server)

After the view timer completes:

```http
X-Blossom-Gate-Ad-Proof: <c>; ad=<ad-id>
```

The server verifies `c` (HMAC, blob/IP binding, expiry) and that `ad` is a current
inventory id, then considers the `ad` gate satisfied. It cannot and does not verify
that a human actually watched.

## Metrics

The server records, keyed on `ad-id`:

- **impression** — ad served,
- **view** — `min` satisfied (implied by a valid proof),
- **click** — reported click-through (optional).

Requirements:

- Unique counting MUST use **ephemeral in-window de-duplication** (a per-window set
  or a cardinality estimator such as HyperLogLog); only aggregate counts are
  persisted. **Raw IPs MUST NOT be stored.** If a per-viewer key is kept during a
  window it MUST be a salted hash whose salt is **stable for that window only** —
  rotating the salt mid-window would double-count uniques.
- The server SHOULD expose read-only aggregate counts per `ad-id` (e.g.
  `GET /ads/stats`) so advertisers can reconcile against their own tracking.
  Metrics are **self-reported and best-effort by design**; there is no central
  authority, and that is intentional.

### Click reporting (optional)

Clients MAY report a click via `POST /ads/click` with `{ ad, c }`. The same privacy
rules apply.

## Composition

A server signals this gate with the `X-Blossom-Gate-Ad` header. Other retrieval
gates use their own `X-Blossom-Gate-<name>` headers (e.g. `X-Blossom-Gate-Pow`). If
a `428` response carries more than one `X-Blossom-Gate-*` challenge, the client MUST
satisfy **all** of them and include every corresponding `…-Proof` header on retry.
The common DEG Mods policy is to require **both** `pow` (enforced anti-abuse) and
`ad` (cooperative funding) — the client mines the proof-of-work while the ad is on
screen, so the two waits overlap.

## Adblock / non-rendering clients

Because the ad is self-hosted (a Nostr event rendered by the client), ordinary ad
blockers do not detect it. If a client genuinely cannot render the ad, it MUST NOT
send an ad proof (that would be dishonest). Operators SHOULD prefer graceful
degradation (e.g. requiring only `pow` for such requests) over hard-failing a user
— this is server policy.

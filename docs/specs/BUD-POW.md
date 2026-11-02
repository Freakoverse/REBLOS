# BUD-POW: Proof-of-work required

`draft` · `optional`

Defines a way for a Blossom server to require a client to perform a **proof-of-work**
before it serves a blob. Purpose: raise the cost of bulk/automated retrieval
(egress abuse, scraping) **without payment, login, or any third-party service**.
Fully anonymous — bound to the client IP, not an identity.

This document is self-contained and can be implemented on its own, in parallel to
BUD-07 (Payment required). It mirrors BUD-07's request/retry shape but is
deliberately **not** a payment flow (see "Why a new status"). It composes with
other retrieval gates such as [BUD-Ads](./BUD-Ads.md) — see "Composition".

> A future BUD may unify proof-of-work, ad-view, and payment gates into one
> document; until then each is specified independently.

## Why a new status (not `402`)

BUD-07 uses `402 Payment Required`, and clients route a `402` into a payment
handler. So a proof-of-work challenge isn't mistaken for a payment demand, this
document uses **`428 Precondition Required`** and an `X-Blossom-Gate-Pow` header. A
client that doesn't implement this BUD receives a `428` it can't satisfy and simply
fails over to another server.

## Handshake

1. Client requests a blob: `GET /<sha256>` or `GET /<sha256>.<ext>`.
2. If the server requires proof-of-work for this blob and the request carries no
   valid proof, it responds:

   ```http
   HTTP/1.1 428 Precondition Required
   X-Blossom-Gate-Pow: v=1; d=<difficulty>; c=<challenge>
   ```

   - `d` — required difficulty in leading zero **bits**.
   - `c` — an opaque challenge string (see "Challenge binding").

3. The client performs the work and retries the identical `GET` with:

   ```http
   X-Blossom-Gate-Pow-Proof: <c>:<nonce>
   ```

4. If the proof is valid the server serves the blob (`200`, per BUD-01). If it is
   missing/invalid/expired the server responds `428` again (fresh challenge); a
   malformed proof MAY get `400`.

## Work (client)

Find a `nonce` (unsigned integer, decimal ASCII) such that

```
SHA-256( c || ":" || nonce )
```

has at least `d` leading zero bits (`||` = byte concatenation).

## Challenge binding (server)

To stop reuse, sharing, and precomputation, the challenge MUST bind to:

- the requested blob `sha256`,
- the client IP as seen by the server, and
- an expiry timestamp.

Servers SHOULD make challenges **stateless**:

```
c = base64url(payload) "." base64url( HMAC-SHA256(server_key, payload) )
```

where `payload` encodes `{ sha256, ip, exp, d, rand }`. The client treats `c` as
opaque and echoes it verbatim in the proof.

**IP source:** behind a proxy/CDN the server MUST read the client IP from the
trusted forwarded header (e.g. `CF-Connecting-IP`), not the socket peer.

## Verification (server)

1. Split the proof into `c` and `nonce`.
2. Verify the HMAC in `c` (stateless) or look it up (stateful).
3. Reject if the bound `sha256` ≠ the requested blob, the bound `ip` ≠ the request
   IP, or `exp` has passed.
4. Reject if `SHA-256(c || ":" || nonce)` lacks ≥ `d` leading zero bits.
5. Otherwise the proof is satisfied. A server MAY honor it for a short **grace
   window** (per blob+IP) so resumed/ranged downloads do not re-challenge.

## Difficulty & adaptivity

- Servers choose `d` per their egress budget; a few seconds of client work is
  typical.
- Servers MAY scale `d` per client — e.g. by recent per-IP volume: low/zero for the
  first few blobs, rising for heavy pullers. Because each challenge binds a single
  blob, **bulk retrieval pays the cost per file**, the intended deterrent. IP
  rotation resets the ramp but not the per-file cost.

## Composition

A server signals this gate with the `X-Blossom-Gate-Pow` header. Other retrieval
gates use their own `X-Blossom-Gate-<name>` headers (e.g. `X-Blossom-Gate-Ad`). If a
`428` response carries more than one `X-Blossom-Gate-*` challenge, the client MUST
satisfy **all** of them and include every corresponding `…-Proof` header on retry. A
client that does not understand a listed gate MUST treat the blob as unretrievable
from this server (and MAY fail over); it MUST NOT serve partial data.

## Notes

- No npub is involved; this works for **anonymous** downloads. A server MAY waive or
  lower `d` for authenticated/subscribed users.
- Proof-of-work is **enforceable** (cryptographic), so it is the load-bearing
  anti-abuse mechanism — unlike an ad-view gate, which is cooperative.

package server

import (
	"html"
	"net/http"
	"strings"
)

// rootHandler serves a human-readable landing page for a plain browser GET of "/",
// and delegates everything else at the root to the relay: WebSocket upgrades (the
// relay itself) and the NIP-11 relay-info document (Accept: application/nostr+json).
// Blob downloads live at "/{hash}" (a different path), so they are never affected.
func (s *Server) rootHandler(relay http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		isWS := strings.Contains(strings.ToLower(r.Header.Get("Upgrade")), "websocket")
		wantsNostr := strings.Contains(r.Header.Get("Accept"), "application/nostr+json")
		if isWS || wantsNostr {
			relay.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(landingHTML(r.Host)))
	}
}

// landingHTML renders the self-contained landing page. host is echoed into the
// relay/Blossom URLs, so it is HTML-escaped against a crafted Host header. A token
// replace (not fmt) is used so literal % in the CSS needs no escaping.
func landingHTML(host string) string {
	return strings.ReplaceAll(landingTemplate, "{{HOST}}", html.EscapeString(host))
}

const landingTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>REBLOS</title>
<meta name="description" content="A Nostr relay and Blossom media server built specifically for NIP-CHAT hubs.">
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0; min-height: 100vh; display: flex; align-items: center; justify-content: center;
    padding: 24px; background: #0a0a0a; color: #e5e5e5;
    font: 16px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  }
  .card {
    width: 100%; max-width: 560px; background: #131313; border: 1px solid #262626;
    border-radius: 16px; padding: 32px;
  }
  .brand { display: flex; align-items: center; gap: 10px; margin-bottom: 4px; }
  .brand h1 { margin: 0; font-size: 28px; letter-spacing: -0.02em; color: #fafafa; }
  .flame { font-size: 26px; }
  .tagline { margin: 0 0 20px; color: #a3a3a3; font-size: 14px; }
  p { margin: 0 0 16px; color: #d4d4d4; }
  .endpoints { margin: 20px 0; border: 1px solid #262626; border-radius: 12px; overflow: hidden; }
  .endpoints div { display: flex; justify-content: space-between; gap: 12px; padding: 10px 14px; font-size: 13px; }
  .endpoints div + div { border-top: 1px solid #262626; }
  .endpoints span:first-child { color: #a3a3a3; }
  .endpoints span:last-child { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; color: #e5e5e5; }
  .actions { margin-top: 24px; }
  a.btn {
    display: inline-flex; align-items: center; gap: 8px; text-decoration: none;
    background: #9333ea; color: #fff; font-weight: 600; font-size: 14px;
    padding: 11px 18px; border-radius: 10px; transition: background .15s;
  }
  a.btn:hover { background: #7e22ce; }
  footer { margin-top: 24px; padding-top: 16px; border-top: 1px solid #262626; font-size: 12px; color: #737373; }
  footer a { color: #a3a3a3; }
</style>
</head>
<body>
  <main class="card">
    <div class="brand"><span class="flame">&#128293;</span><h1>REBLOS</h1></div>
    <p class="tagline">Relay &amp; Blossom Server for NIP-CHAT</p>
    <p>
      REBLOS is a Nostr <strong>relay</strong> and <strong>Blossom</strong> media server built specifically
      for the needs of <strong>NIP-CHAT hubs</strong>. It hosts the hubs it whitelists: storing and serving
      each hub's events and media to its members, and keeping them durable by syncing from the hubs' own
      relays. It is closed by design: only members of a whitelisted hub can publish or upload here.
    </p>
    <div class="endpoints">
      <div><span>Relay</span><span>wss://{{HOST}}</span></div>
      <div><span>Blossom</span><span>https://{{HOST}}</span></div>
    </div>
    <div class="actions">
      <a class="btn" href="/dashboard">Open dashboard &rarr;</a>
    </div>
    <footer>
      Operator dashboard requires a Nostr extension (admin key). &middot;
      <a href="https://github.com/Freakoverse/REBLOS" target="_blank" rel="noopener">github.com/Freakoverse/REBLOS</a>
    </footer>
  </main>
</body>
</html>`

package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
)

// syncRelays is the persistent, operator-managed set of general relays this node
// pulls hub events + content from during sync (REBLOS-MIGRATION §6), IN ADDITION
// to each whitelisted hub's own declared relay hints. It is seeded on first run
// with a curated default set (config.DefaultSyncRelays + any configured extras);
// the operator can then disable a default or add custom relays from the dashboard.
//
// A disabled entry is KEPT in the list (so a default stays visible to re-enable)
// but not used by sync. Removing a default just disables it (seeding would re-add a
// truly-deleted default on the next start); a custom entry is removed outright.
type syncRelays struct {
	mu      sync.RWMutex
	path    string
	entries []syncRelayEntry // order-preserving (defaults first, then customs)
}

type syncRelayEntry struct {
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	Default bool   `json:"default,omitempty"` // part of the seeded default set
}

// loadSyncRelays reads the persisted list, then merges in any seed relays not
// already present (added enabled), so new defaults in a later version show up while
// the operator's toggles and custom entries persist. seed entries are marked
// Default; configured extras that are not in defaults are seeded as non-default.
func loadSyncRelays(path string, defaults, extras []string) *syncRelays {
	s := &syncRelays{path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s.entries)
	}
	have := map[string]bool{}
	for _, e := range s.entries {
		have[e.URL] = true
	}
	defaultSet := map[string]bool{}
	for _, u := range defaults {
		if n := normalizeRelayURL(u); n != "" {
			defaultSet[n] = true
		}
	}
	add := func(list []string, isDefault bool) {
		for _, u := range list {
			n := normalizeRelayURL(u)
			if n == "" || have[n] {
				continue
			}
			have[n] = true
			s.entries = append(s.entries, syncRelayEntry{URL: n, Enabled: true, Default: isDefault})
		}
	}
	add(defaults, true)
	add(extras, false)
	// A persisted entry whose URL is in the default set should report as a default
	// (covers a list written before an entry became a default).
	for i := range s.entries {
		if defaultSet[s.entries[i].URL] {
			s.entries[i].Default = true
		}
	}
	_ = s.save()
	return s
}

// enabled returns the URLs sync should use (enabled entries only), order-preserving.
func (s *syncRelays) enabled() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.entries))
	for _, e := range s.entries {
		if e.Enabled {
			out = append(out, e.URL)
		}
	}
	return out
}

// list returns a copy of all entries (for the dashboard).
func (s *syncRelays) list() []syncRelayEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]syncRelayEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

// add appends a custom relay (enabled). A duplicate (including of a default) is not
// an error: it is re-enabled instead. Returns the normalized URL stored.
func (s *syncRelays) add(url string) (string, error) {
	n := normalizeRelayURL(url)
	if n == "" {
		return "", fmt.Errorf("relay URL must start with ws:// or wss://")
	}
	s.mu.Lock()
	for i := range s.entries {
		if s.entries[i].URL == n {
			s.entries[i].Enabled = true
			s.mu.Unlock()
			return n, s.save()
		}
	}
	s.entries = append(s.entries, syncRelayEntry{URL: n, Enabled: true})
	s.mu.Unlock()
	return n, s.save()
}

// setEnabled toggles an entry on or off. Unknown URL is a no-op (no error).
func (s *syncRelays) setEnabled(url string, enabled bool) error {
	n := normalizeRelayURL(url)
	s.mu.Lock()
	for i := range s.entries {
		if s.entries[i].URL == n {
			s.entries[i].Enabled = enabled
			break
		}
	}
	s.mu.Unlock()
	return s.save()
}

// remove deletes a custom entry; a default is disabled instead (seeding would
// re-add a deleted default). Returns whether the entry was fully removed.
func (s *syncRelays) remove(url string) (removed bool, err error) {
	n := normalizeRelayURL(url)
	s.mu.Lock()
	for i := range s.entries {
		if s.entries[i].URL != n {
			continue
		}
		if s.entries[i].Default {
			s.entries[i].Enabled = false // keep it, just off
		} else {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			removed = true
		}
		break
	}
	s.mu.Unlock()
	return removed, s.save()
}

func (s *syncRelays) save() error {
	s.mu.RLock()
	data, _ := json.MarshalIndent(s.entries, "", "  ")
	s.mu.RUnlock()
	return os.WriteFile(s.path, data, 0o600)
}

// normalizeRelayURL trims whitespace + trailing slashes and requires a ws/wss
// scheme. Returns "" if the URL is not a websocket relay URL.
func normalizeRelayURL(u string) string {
	u = strings.TrimSpace(u)
	u = strings.TrimRight(u, "/")
	if u == "" {
		return ""
	}
	lower := strings.ToLower(u)
	if !strings.HasPrefix(lower, "ws://") && !strings.HasPrefix(lower, "wss://") {
		return ""
	}
	return u
}

// ── admin API: sync relays ───────────────────────────────────────────────────

func (s *Server) handleAdminSyncRelaysList(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"relays": s.syncR.list()})
}

func (s *Server) handleAdminSyncRelaysAdd(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if _, err := s.syncR.add(body.URL); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"relays": s.syncR.list()})
}

// handleAdminSyncRelaysSet toggles an entry enabled/disabled (PUT {url, enabled}).
func (s *Server) handleAdminSyncRelaysSet(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		URL     string `json:"url"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := s.syncR.setEnabled(body.URL, body.Enabled); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminSyncRelaysRemove(w http.ResponseWriter, r *http.Request) {
	setAdminCORS(w)
	if err := s.verifyAdmin(r); err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if _, err := s.syncR.remove(body.URL); err != nil {
		httpErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

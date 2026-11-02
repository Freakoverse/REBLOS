package server

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// quarantine is a persistent, REVERSIBLE set of blob hashes withheld from
// download. Unlike the blacklist (a permanent moderation ban), a quarantine is a
// lifecycle state: a blob lands here when its last uploader retracts their upload
// and no stored mod references it. The bytes are KEPT — downloads just 404 — and
// the state is lifted by a re-upload (self-heal) or an admin release. Nothing here
// ever deletes bytes; that stays an explicit admin action.
type quarantine struct {
	mu     sync.RWMutex
	saveMu sync.Mutex
	path   string
	set    map[string]int64 // hash -> since (unix seconds)
}

func loadQuarantine(path string) *quarantine {
	q := &quarantine{path: path, set: map[string]int64{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &q.set)
	}
	return q
}

func (q *quarantine) has(hash string) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	_, ok := q.set[hash]
	return ok
}

func (q *quarantine) add(hash string) {
	q.mu.Lock()
	if _, ok := q.set[hash]; ok {
		q.mu.Unlock()
		return
	}
	q.set[hash] = time.Now().Unix()
	q.mu.Unlock()
	q.save()
}

func (q *quarantine) remove(hash string) {
	q.mu.Lock()
	if _, ok := q.set[hash]; !ok {
		q.mu.Unlock()
		return
	}
	delete(q.set, hash)
	q.mu.Unlock()
	q.save()
}

func (q *quarantine) save() {
	q.mu.RLock()
	data, _ := json.Marshal(q.set)
	q.mu.RUnlock()
	q.saveMu.Lock()
	_ = os.WriteFile(q.path, data, 0o600)
	q.saveMu.Unlock()
}

package server

import (
	"errors"
	"sync"
)

var (
	errBusy     = errors.New("server is at upload capacity, try again shortly")
	errNpubBusy = errors.New("you already have an upload in progress")
)

// uploadLimiter enforces a global concurrency cap plus one in-flight upload per
// npub. (Rolling per-npub quotas / rate tiers could be layered on later.)
type uploadLimiter struct {
	global   chan struct{}
	mu       sync.Mutex
	inflight map[string]bool
}

func newUploadLimiter(maxConcurrent int) *uploadLimiter {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &uploadLimiter{
		global:   make(chan struct{}, maxConcurrent),
		inflight: make(map[string]bool),
	}
}

// acquire reserves a global slot and the per-npub slot, returning a release func.
func (l *uploadLimiter) acquire(pubkey string) (func(), error) {
	l.mu.Lock()
	if l.inflight[pubkey] {
		l.mu.Unlock()
		return nil, errNpubBusy
	}
	l.mu.Unlock()

	select {
	case l.global <- struct{}{}:
	default:
		return nil, errBusy
	}

	l.mu.Lock()
	if l.inflight[pubkey] { // re-check after taking the global slot
		l.mu.Unlock()
		<-l.global
		return nil, errNpubBusy
	}
	l.inflight[pubkey] = true
	l.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			delete(l.inflight, pubkey)
			l.mu.Unlock()
			<-l.global
		})
	}, nil
}

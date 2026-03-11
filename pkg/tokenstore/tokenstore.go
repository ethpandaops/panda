package tokenstore

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

type Store struct {
	mu      sync.RWMutex
	tokens  map[string]entry
	ttl     time.Duration
	stopCh  chan struct{}
	stopped bool
}

type entry struct {
	value     string
	expiresAt time.Time
}

func New(ttl time.Duration) *Store {
	store := &Store{
		tokens: make(map[string]entry, 64),
		ttl:    ttl,
		stopCh: make(chan struct{}),
	}

	go store.cleanupLoop()

	return store
}

func (s *Store) Register(value string) string {
	token := generateToken()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.tokens[token] = entry{
		value:     value,
		expiresAt: time.Now().Add(s.ttl),
	}

	return token
}

func (s *Store) Validate(token string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, ok := s.tokens[token]
	if !ok {
		return ""
	}

	if time.Now().After(entry.expiresAt) {
		return ""
	}

	return entry.value
}

func (s *Store) Revoke(value string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for token, entry := range s.tokens {
		if entry.value == value {
			delete(s.tokens, token)
			return
		}
	}
}

func (s *Store) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}

	s.stopped = true
	s.mu.Unlock()

	close(s.stopCh)
}

func (s *Store) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.cleanup()
		}
	}
}

func (s *Store) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for token, entry := range s.tokens {
		if now.After(entry.expiresAt) {
			delete(s.tokens, token)
		}
	}
}

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("failed to generate random token: " + err.Error())
	}

	return base64.URLEncoding.EncodeToString(b)
}

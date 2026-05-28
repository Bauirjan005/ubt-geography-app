package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Session holds the data stored server-side for one authenticated user.
type Session struct {
	UserID    int64
	Role      string // "student" or "teacher"
	CreatedAt time.Time
	ExpiresAt time.Time
}

// SessionStore is a thread-safe in-memory map of session IDs → Session.
// For production, swap the map for Redis or a sessions table in SQLite.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionStore() *SessionStore {
	s := &SessionStore{sessions: make(map[string]*Session)}
	// Background goroutine: prune expired sessions every 15 minutes.
	go s.reapLoop()
	return s
}

const sessionTTL = 7 * 24 * time.Hour // matches the cookie MaxAge

// Create generates a cryptographically random session ID, stores the session,
// and returns the ID.
func (s *SessionStore) Create(userID int64, role string) (string, error) {
	id, err := generateID()
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	s.mu.Lock()
	s.sessions[id] = &Session{
		UserID:    userID,
		Role:      role,
		CreatedAt: now,
		ExpiresAt: now.Add(sessionTTL),
	}
	s.mu.Unlock()

	return id, nil
}

// Get looks up a session by ID and returns it if it exists and has not expired.
func (s *SessionStore) Get(id string) (*Session, bool) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok || time.Now().UTC().After(sess.ExpiresAt) {
		return nil, false
	}
	return sess, true
}

// Delete removes a session from the store (used on logout).
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()
}

// reapLoop runs every 15 minutes and removes expired sessions to prevent
// unbounded memory growth.
func (s *SessionStore) reapLoop() {
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now().UTC()
		s.mu.Lock()
		for id, sess := range s.sessions {
			if now.After(sess.ExpiresAt) {
				delete(s.sessions, id)
			}
		}
		s.mu.Unlock()
	}
}

// generateID produces a 32-byte (256-bit) hex-encoded random session ID.
func generateID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("failed to generate session ID: " + err.Error())
	}
	return hex.EncodeToString(b), nil
}

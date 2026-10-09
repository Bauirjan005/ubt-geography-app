package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
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

// SessionStore manages sessions in memory with SQLite persistence.
type SessionStore struct {
	db       *sql.DB
	mu       sync.RWMutex
	sessions map[string]*Session
}

const sessionTTL = 30 * 24 * time.Hour // 30 days session validity

func NewSessionStore(db *sql.DB) *SessionStore {
	s := &SessionStore{
		db:       db,
		sessions: make(map[string]*Session),
	}
	// Initial cleanup of expired sessions from DB
	if db != nil {
		_, _ = db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().UTC())
	}
	// Background goroutine: prune expired sessions every 15 minutes.
	go s.reapLoop()
	return s
}

// Create generates a cryptographically random session ID, stores the session
// in SQLite and memory, and returns the ID.
func (s *SessionStore) Create(userID int64, role string) (string, error) {
	id, err := generateID()
	if err != nil {
		return "", err
	}

	now := time.Now().UTC()
	sess := &Session{
		UserID:    userID,
		Role:      role,
		CreatedAt: now,
		ExpiresAt: now.Add(sessionTTL),
	}

	if s.db != nil {
		_, err = s.db.Exec(
			`INSERT INTO sessions (id, user_id, role, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
			id, sess.UserID, sess.Role, sess.CreatedAt, sess.ExpiresAt,
		)
		if err != nil {
			log.Printf("⚠️ Failed to persist session to DB: %v", err)
			return "", err
		}
	}

	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	return id, nil
}

// Get looks up a session by ID in memory or SQLite and returns it if valid.
func (s *SessionStore) Get(id string) (*Session, bool) {
	now := time.Now().UTC()

	// 1. Fast RAM lookup
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if ok {
		if now.After(sess.ExpiresAt) {
			s.Delete(id)
			return nil, false
		}
		return sess, true
	}

	// 2. Fallback to SQLite (e.g. after server restart)
	if s.db != nil {
		var (
			userID    int64
			role      string
			createdAt time.Time
			expiresAt time.Time
		)
		err := s.db.QueryRow(
			`SELECT user_id, role, created_at, expires_at FROM sessions WHERE id = ?`,
			id,
		).Scan(&userID, &role, &createdAt, &expiresAt)

		if err == nil {
			if now.After(expiresAt) {
				s.Delete(id)
				return nil, false
			}
			loadedSess := &Session{
				UserID:    userID,
				Role:      role,
				CreatedAt: createdAt,
				ExpiresAt: expiresAt,
			}
			s.mu.Lock()
			s.sessions[id] = loadedSess
			s.mu.Unlock()
			return loadedSess, true
		}
	}

	return nil, false
}

// Delete removes a session from memory and SQLite (used on logout).
func (s *SessionStore) Delete(id string) {
	s.mu.Lock()
	delete(s.sessions, id)
	s.mu.Unlock()

	if s.db != nil {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	}
}

// reapLoop runs every 15 minutes and cleans expired sessions.
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

		if s.db != nil {
			_, _ = s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, now)
		}
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

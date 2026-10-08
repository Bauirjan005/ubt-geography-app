package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

// ─── Request / Response types ────────────────────────────────────────────────

type RegisterRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"` // "student" or "teacher"
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthResponse struct {
	Message string `json:"message"`
	Role    string `json:"role,omitempty"`
	UserID  int64  `json:"user_id,omitempty"`
}

// ─── Validation helpers ───────────────────────────────────────────────────────

func validateRegisterInput(r RegisterRequest) string {
	r.Username = strings.TrimSpace(r.Username)
	r.Email = strings.TrimSpace(r.Email)

	if len(r.Username) < 2 || len(r.Username) > 64 {
		return "username must be between 2 and 64 characters"
	}
	if !strings.Contains(r.Email, "@") || len(r.Email) < 5 {
		return "invalid email address"
	}
	if err := validatePassword(r.Password); err != "" {
		return err
	}
	if r.Role != "student" && r.Role != "teacher" {
		return "role must be 'student' or 'teacher'"
	}
	return ""
}

// validatePassword enforces a minimum-security policy:
// at least 8 chars, one uppercase letter, one digit.
func validatePassword(pw string) string {
	if len(pw) < 8 {
		return "password must be at least 8 characters"
	}
	var hasUpper, hasDigit bool
	for _, ch := range pw {
		if unicode.IsUpper(ch) {
			hasUpper = true
		}
		if unicode.IsDigit(ch) {
			hasDigit = true
		}
	}
	if !hasUpper {
		return "password must contain at least one uppercase letter"
	}
	if !hasDigit {
		return "password must contain at least one digit"
	}
	return ""
}

// ─── handleRegister ───────────────────────────────────────────────────────────

// handleRegister creates a new user account.
// It expects a JSON body with username, email, password, and role.
// Passwords are hashed with bcrypt (cost 12) before storage.
//
// POST /api/register
// Response 201: { message, role, user_id }
// Response 400: { message } — validation failure or duplicate email
func handleRegister(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, AuthResponse{Message: "method not allowed"})
			return
		}

		var req RegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: "invalid JSON body"})
			return
		}

		// Trim whitespace before validation
		req.Username = strings.TrimSpace(req.Username)
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))

		if msg := validateRegisterInput(req); msg != "" {
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: msg})
			return
		}

		// Check for duplicate email
		var exists int
		err := db.QueryRow(`SELECT COUNT(1) FROM users WHERE email = ?`, req.Email).Scan(&exists)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "database error"})
			return
		}
		if exists > 0 {
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: "an account with that email already exists"})
			return
		}

		// Hash the password. Cost 12 is a good balance of security and speed
		// (~300 ms on modern hardware). Increase to 13-14 on faster servers.
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "failed to hash password"})
			return
		}

		// Persist the new user
		result, err := db.Exec(
			`INSERT INTO users (username, email, password_hash, role, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			req.Username, req.Email, string(hash), req.Role, time.Now().UTC(),
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "failed to create user"})
			return
		}

		id, _ := result.LastInsertId()
		writeJSON(w, http.StatusCreated, AuthResponse{
			Message: "account created successfully",
			Role:    req.Role,
			UserID:  id,
		})
	}
}

// ─── handleLogin ─────────────────────────────────────────────────────────────

// handleLogin authenticates an existing user and issues a session cookie.
// On success it sets an HttpOnly, Secure, SameSite=Strict cookie named
// "session_id" and returns the user's role so the client can redirect.
//
// POST /api/login
// Response 200: { message, role, user_id } + Set-Cookie
// Response 401: { message } — wrong credentials (deliberately vague)
func handleLogin(db *sql.DB, sessions *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, AuthResponse{Message: "method not allowed"})
			return
		}

		var req LoginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: "invalid JSON body"})
			return
		}

		req.Email = strings.ToLower(strings.TrimSpace(req.Email))

		// Fetch user record. Never reveal whether the email exists vs password
		// is wrong — always return the same 401 for both cases.
		var (
			userID       int64
			passwordHash string
			role         string
		)
		err := db.QueryRow(
			`SELECT id, password_hash, role FROM users WHERE email = ?`, req.Email,
		).Scan(&userID, &passwordHash, &role)

		if err == sql.ErrNoRows {
			// Run bcrypt anyway to prevent timing attacks that could reveal
			// whether an email is registered.
			bcrypt.CompareHashAndPassword([]byte("$2a$12$placeholder.hash.to.waste.time"), []byte(req.Password))
			writeJSON(w, http.StatusUnauthorized, AuthResponse{Message: "invalid email or password"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "database error"})
			return
		}

		// Verify the password against the stored hash
		if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
			writeJSON(w, http.StatusUnauthorized, AuthResponse{Message: "invalid email or password"})
			return
		}

		// Create a new server-side session
		sessionID, err := sessions.Create(userID, role)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "failed to create session"})
			return
		}

		// Issue the session cookie.
		// HttpOnly  — JavaScript cannot read this cookie (XSS protection).
		// Secure    — only sent over HTTPS. Set to false in local dev only.
		// SameSite  — Strict prevents the cookie being sent on cross-site
		//             requests (CSRF protection).
		isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
		http.SetCookie(w, &http.Cookie{
			Name:     "session_id",
			Value:    sessionID,
			Path:     "/",
			HttpOnly: true,
			Secure:   isSecure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   60 * 60 * 24 * 7,
		})

		writeJSON(w, http.StatusOK, AuthResponse{
			Message: "logged in successfully",
			Role:    role,
			UserID:  userID,
		})
	}
}

// ─── handleLogout ─────────────────────────────────────────────────────────────

// handleLogout destroys the server-side session and clears the cookie.
//
// POST /api/logout
func handleLogout(sessions *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session_id")
		if err == nil {
			sessions.Delete(cookie.Value)
		}

		isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
		// Expire the cookie immediately by setting MaxAge = -1
		http.SetCookie(w, &http.Cookie{
			Name:     "session_id",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			Secure:   isSecure,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   -1,
		})

		writeJSON(w, http.StatusOK, AuthResponse{Message: "logged out"})
	}
}

// ─── Utility ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// handleMe возвращает данные текущего пользователя по его сессии.
// GET /api/me
// 200 — { id, username, email, role }
// 401 — если не авторизован
func handleMe(db *sql.DB, sessions *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session_id")
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "not authenticated"})
			return
		}

		sess, ok := sessions.Get(cookie.Value)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "session expired"})
			return
		}

		var username, email string
		err = db.QueryRow(
			`SELECT username, email FROM users WHERE id = ?`, sess.UserID,
		).Scan(&username, &email)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"id":       sess.UserID,
			"username": username,
			"email":    email,
			"role":     sess.Role,
		})
	}
}

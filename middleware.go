package main

import (
	"context"
	"net/http"
)

// contextKey is an unexported type for context values set by this package.
type contextKey string

const sessionContextKey contextKey = "session"

// requireAuth is middleware that blocks unauthenticated requests.
// It reads the session_id cookie, validates it, and injects the Session
// into the request context so downstream handlers can access it.
//
// Usage:
//
//	mux.Handle("/api/lessons", requireAuth(sessions)(listLessonsHandler))
func requireAuth(sessions *SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("session_id")
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, AuthResponse{Message: "authentication required"})
				return
			}

			sess, ok := sessions.Get(cookie.Value)
			if !ok {
				// Session expired or forged — clear the stale cookie
				isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
				http.SetCookie(w, &http.Cookie{
					Name:     "session_id",
					Value:    "",
					Path:     "/",
					HttpOnly: true,
					Secure:   isSecure,
					SameSite: http.SameSiteLaxMode,
					MaxAge:   -1,
				})
				writeJSON(w, http.StatusUnauthorized, AuthResponse{Message: "session expired, please log in again"})
				return
			}

			// Inject session into context so handlers don't need to re-validate
			ctx := context.WithValue(r.Context(), sessionContextKey, sess)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// requireRole wraps requireAuth and additionally enforces that the
// authenticated user has one of the allowed roles.
//
// Usage:
//
//	mux.Handle("/api/teacher/lessons", requireRole(sessions, "teacher")(createLessonHandler))
//	mux.Handle("/api/student/quiz",    requireRole(sessions, "student")(quizHandler))
func requireRole(sessions *SessionStore, roles ...string) func(http.Handler) http.Handler {
	allowedRoles := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowedRoles[r] = true
	}

	return func(next http.Handler) http.Handler {
		// Chain: first authenticate, then check role.
		authMiddleware := requireAuth(sessions)

		return authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sess := SessionFromContext(r)
			if sess == nil {
				// Should never happen if requireAuth ran, but be defensive.
				writeJSON(w, http.StatusUnauthorized, AuthResponse{Message: "authentication required"})
				return
			}
			if !allowedRoles[sess.Role] {
				writeJSON(w, http.StatusForbidden, AuthResponse{Message: "access denied: insufficient role"})
				return
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// SessionFromContext retrieves the Session injected by requireAuth.
// Returns nil if the request was not authenticated (e.g. in an unprotected handler).
func SessionFromContext(r *http.Request) *Session {
	sess, _ := r.Context().Value(sessionContextKey).(*Session)
	return sess
}

package main

import (
	"database/sql"
	"log"
	"net/http"

	_ "modernc.org/sqlite"
)

func main() {
	// ── Database ──────────────────────────────────────────────────────────────
	db, err := sql.Open("sqlite", "./ubt.db?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	if err := migrate(db); err != nil {
		log.Fatalf("migration failed: %v", err)
	}

	// ── Session store ─────────────────────────────────────────────────────────
	sessions := NewSessionStore()

	// ── Routes ────────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	// Public auth endpoints
	mux.Handle("/api/register", handleRegister(db))
	mux.Handle("/api/login", handleLogin(db, sessions))
	mux.Handle("/api/logout", requireAuth(sessions)(handleLogout(sessions)))

	// Example protected routes (implement these next)
	mux.Handle("/api/student/", requireRole(sessions, "student")(http.HandlerFunc(studentDashboard)))
	mux.Handle("/api/teacher/", requireRole(sessions, "teacher")(http.HandlerFunc(teacherPanel)))

	// Serve static files (your HTML/CSS/JS frontend)
	mux.Handle("/", http.FileServer(http.Dir("./static")))

	log.Println("UBT server listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

// migrate creates tables if they don't exist yet.
// In production, replace with a migration library (e.g. golang-migrate).
func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			username      TEXT    NOT NULL,
			email         TEXT    NOT NULL UNIQUE,
			password_hash TEXT    NOT NULL,
			role          TEXT    NOT NULL CHECK(role IN ('student', 'teacher')),
			created_at    DATETIME NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
	`)
	return err
}

// ─── Stub handlers (to be replaced in Part 2 & 3) ────────────────────────────

func studentDashboard(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "welcome, student!",
		"user_id": sess.UserID,
	})
}

func teacherPanel(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"message": "welcome, teacher!",
		"user_id": sess.UserID,
	})
}

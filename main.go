package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

func loadEnv(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	values, err := godotenv.Unmarshal(strings.TrimPrefix(string(contents), "\uFEFF"))
	if err != nil {
		return err
	}

	for key, value := range values {
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func main() {
	if err := loadEnv(".env"); err != nil {
		if os.IsNotExist(err) {
			log.Println("ℹ️ .env файлы табылмады (жүйелік айнымалылар қолданылады)")
		} else {
			log.Printf("⚠️ .env жүктеу: %v", err)
		}
	} else {
		log.Println("✅ .env успешно загружен")
	}

	if err := validateEnv(); err != nil {
		log.Fatalf("❌ Конфигурация қатесі: %v", err)
	}

	ensureUploadDir()

	var db *DB
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL != "" {
		cleanURL := strings.ReplaceAll(databaseURL, "&channel_binding=require", "")
		cleanURL = strings.ReplaceAll(cleanURL, "?channel_binding=require&", "?")
		cleanURL = strings.ReplaceAll(cleanURL, "?channel_binding=require", "")

		log.Println("🐘 Neon PostgreSQL режимі іске қосылуда...")
		sqlDB, err := sql.Open("postgres", cleanURL)
		if err != nil {
			log.Fatalf("❌ Postgres дерекқорын ашу мүмкін болмады: %v", err)
		}
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(15 * time.Minute)

		if err := sqlDB.Ping(); err != nil {
			log.Fatalf("❌ Postgres байланыс қатесі: %v", err)
		}
		log.Println("✅ Neon PostgreSQL дерекқорына сәтті қосылды!")
		db = &DB{DB: sqlDB, isPostgres: true}
	} else {
		log.Println("📁 SQLite режимі іске қосылуда (ubt.db)...")
		sqlDB, err := sql.Open("sqlite", "./ubt.db?_journal_mode=WAL&_foreign_keys=on")
		if err != nil {
			log.Fatalf("❌ Дерекқорды ашу мүмкін болмады: %v", err)
		}
		db = &DB{DB: sqlDB, isPostgres: false}
	}
	defer db.Close()

	if err := migrateAll(db); err != nil {
		log.Fatalf("❌ Миграция қатесі: %v", err)
	}

	sessions := NewSessionStore(db)
	mux := http.NewServeMux()

	// ── Public ────────────────────────────────────────────────────────────────
	mux.Handle("/api/register", handleRegister(db))
	mux.Handle("/api/login", handleLogin(db, sessions))
	mux.Handle("/api/logout", requireAuth(sessions)(handleLogout(sessions)))
	mux.Handle("/api/me", handleMe(db, sessions))

	// ── Teacher: lessons + questions ──────────────────────────────────────────
	RegisterTeacherRoutes(mux, db, sessions)

	// ── Teacher: materials + AI quiz + readers ────────────────────────────────
	tGuard := requireRole(sessions, "teacher")

	mux.Handle("/api/teacher/materials", tGuard(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				handleListTeacherMaterials(db)(w, r)
			case http.MethodPost:
				handleUploadMaterial(db)(w, r)
			default:
				writeJSON(w, http.StatusMethodNotAllowed,
					map[string]string{"message": "method not allowed"})
			}
		})))

	mux.Handle("/api/teacher/materials/", tGuard(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/generate-quiz") && r.Method == http.MethodPost {
				handleGenerateAIQuiz(db)(w, r)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/readers") && r.Method == http.MethodGet {
				handleMaterialReaders(db)(w, r)
				return
			}
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
		})))

	// ── Teacher: AI quiz list + details + update + delete + status + results ──
	mux.Handle("/api/teacher/ai-quizzes", tGuard(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				handleListTeacherAIQuizzes(db)(w, r)
				return
			}
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		})))

	mux.Handle("/api/teacher/ai-quizzes/", tGuard(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/results") && r.Method == http.MethodGet {
				handleGetAIQuizResults(db)(w, r)
				return
			}
			if (strings.HasSuffix(r.URL.Path, "/publish") || strings.HasSuffix(r.URL.Path, "/status")) && (r.Method == http.MethodPost || r.Method == http.MethodPatch) {
				handleUpdateAIQuizStatus(db)(w, r)
				return
			}
			switch r.Method {
			case http.MethodGet:
				handleGetTeacherAIQuizDetail(db)(w, r)
			case http.MethodPut:
				handleUpdateTeacherAIQuiz(db)(w, r)
			case http.MethodDelete:
				handleDeleteTeacherAIQuiz(db)(w, r)
			default:
				writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
			}
		})))

	// ── Student: lessons + AI quizzes + materials ─────────────────────────────
	// Барлығы student_handlers.go ішіндегі RegisterStudentRoutes арқылы тіркеледі
	RegisterStudentRoutes(mux, db, sessions)

	// ── Static files ──────────────────────────────────────────────────────────
	staticFS := http.FileServer(http.Dir("./static"))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		staticFS.ServeHTTP(w, r)
	}))

	// ── CORS ──────────────────────────────────────────────────────────────────
	origins := []string{
		"http://127.0.0.1:3001",
		"http://localhost:3001",
		"http://127.0.0.1:3000",
		"http://localhost:3000",
		"http://127.0.0.1:8080",
		"http://localhost:8080",
	}

	handler := corsMiddleware(origins, mux)

	port := os.Getenv("PORT")
	if port == "" {
		port = "3001"
	}
	addr := "0.0.0.0:" + port

	server := &http.Server{
		Addr:           addr,
		Handler:        handler,
		ReadTimeout:    120 * time.Second,
		WriteTimeout:   120 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	log.Printf("✅ UBT сервері іске қосылды → http://%s (порт: %s)", addr, port)
	log.Fatal(server.ListenAndServe())
}

func validateEnv() error {
	keys := map[string]string{
		"GEMINI_API_KEY":    os.Getenv("GEMINI_API_KEY"),
		"DEEPSEEK_API_KEY":  os.Getenv("DEEPSEEK_API_KEY"),
		"ANTHROPIC_API_KEY": os.Getenv("ANTHROPIC_API_KEY"),
	}
	for name, val := range keys {
		if val != "" {
			preview := val
			if len(preview) > 10 {
				preview = preview[:10] + "..."
			}
			log.Printf("✅ %s = %s", name, preview)
			return nil
		}
	}
	return fmt.Errorf(
		"API кілті табылмады!\n" +
			"  Тегін (Gemini): https://aistudio.google.com/app/apikey\n" +
			"  → $env:GEMINI_API_KEY = 'AIzaSy...'")
}

func corsMiddleware(allowedOrigins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			allowed := false
			for _, o := range allowedOrigins {
				if o == origin {
					allowed = true
					break
				}
			}
			if !allowed && (strings.HasSuffix(origin, ".onrender.com") ||
				origin == "https://"+r.Host || origin == "http://"+r.Host) {
				allowed = true
			}
			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Vary", "Origin")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

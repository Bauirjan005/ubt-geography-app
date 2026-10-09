package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestNeonDatabaseAndAuth(t *testing.T) {
	if err := loadEnv(".env"); err != nil {
		t.Logf("loadEnv error: %v", err)
	}

	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("DATABASE_URL not set, skipping Neon test")
	}

	cleanURL := strings.ReplaceAll(databaseURL, "&channel_binding=require", "")
	cleanURL = strings.ReplaceAll(cleanURL, "?channel_binding=require&", "?")
	cleanURL = strings.ReplaceAll(cleanURL, "?channel_binding=require", "")

	sqlDB, err := openPostgres(cleanURL)
	if err != nil {
		t.Fatalf("Failed to open Postgres: %v", err)
	}
	defer sqlDB.Close()

	db := &DB{DB: sqlDB, isPostgres: true}
	if err := db.Ping(); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	// 1. Verify users table has 12 users
	var userCount int64
	if err := db.QueryRow("SELECT count(*) FROM users").Scan(&userCount); err != nil {
		t.Fatalf("QueryRow count users failed: %v", err)
	}
	if userCount != 12 {
		t.Errorf("Expected 12 users in Neon, got %d", userCount)
	}

	// 2. Verify questions table has 209 AI questions
	var qCount int64
	if err := db.QueryRow("SELECT count(*) FROM ai_questions").Scan(&qCount); err != nil {
		t.Fatalf("QueryRow count ai_questions failed: %v", err)
	}
	if qCount != 209 {
		t.Errorf("Expected 209 questions in Neon, got %d", qCount)
	}

	// 3. Test handleLogin handler with invalid password (security check)
	sessions := NewSessionStore(db)
	loginHandler := handleLogin(db, sessions)

	body, _ := json.Marshal(map[string]string{
		"email":    "liza@gmail.com",
		"password": "wrong_password_test",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	loginHandler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401 Unauthorized for wrong password, got %d", rr.Code)
	}

	// 4. Test creating a session and verifying handleMe
	sessID, err := sessions.Create(2, "teacher")
	if err != nil {
		t.Fatalf("Failed to create test session: %v", err)
	}

	meHandler := handleMe(db, sessions)
	reqMe := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	reqMe.AddCookie(&http.Cookie{Name: "session_id", Value: sessID})
	rrMe := httptest.NewRecorder()

	meHandler.ServeHTTP(rrMe, reqMe)

	if rrMe.Code != http.StatusOK {
		t.Fatalf("Expected 200 OK for /api/me, got %d: %s", rrMe.Code, rrMe.Body.String())
	}

	var meResp map[string]any
	if err := json.Unmarshal(rrMe.Body.Bytes(), &meResp); err != nil {
		t.Fatalf("Failed to unmarshal /api/me response: %v", err)
	}

	if meResp["email"] != "liza@gmail.com" {
		t.Errorf("Expected email liza@gmail.com, got %v", meResp["email"])
	}
	if meResp["role"] != "teacher" {
		t.Errorf("Expected role teacher, got %v", meResp["role"])
	}

	// Cleanup test session
	sessions.Delete(sessID)
	t.Logf("✅ Verified Neon PostgreSQL integration: %d users, %d AI questions, handleLogin, and handleMe worked flawlessly!", userCount, qCount)
}

func openPostgres(url string) (*sql.DB, error) {
	return sql.Open("postgres", url)
}

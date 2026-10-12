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
	"time"
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
	var pingErr error
	for attempt := 0; attempt < 4; attempt++ {
		pingErr = db.Ping()
		if pingErr == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if pingErr != nil {
		t.Fatalf("Ping failed: %v", pingErr)
	}

	// 1. Verify users table has 12 users
	var userCount int64
	if err := db.QueryRow("SELECT count(*) FROM users").Scan(&userCount); err != nil {
		t.Fatalf("QueryRow count users failed: %v", err)
	}
	if userCount < 12 {
		t.Errorf("Expected at least 12 users in Neon, got %d", userCount)
	}

	// 2. Verify questions table has AI questions
	var qCount int64
	if err := db.QueryRow("SELECT count(*) FROM ai_questions").Scan(&qCount); err != nil {
		t.Fatalf("QueryRow count ai_questions failed: %v", err)
	}
	if qCount < 209 {
		t.Errorf("Expected at least 209 questions in Neon, got %d", qCount)
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

	if err := migrateAll(db); err != nil {
		t.Fatalf("migrateAll failed: %v", err)
	}

	// Cleanup test session
	sessions.Delete(sessID)
	t.Logf("✅ Verified Neon PostgreSQL integration: %d users, %d AI questions, handleLogin, and handleMe worked flawlessly!", userCount, qCount)

	// 5. Test Forgot Password & Reset Password Flow
	forgotHandler := handleForgotPassword(db)

	// Non-existent email
	bodyNonExist, _ := json.Marshal(ForgotPasswordRequest{Email: "notfound@example.com"})
	reqForgotBad := httptest.NewRequest(http.MethodPost, "/api/forgot-password", bytes.NewReader(bodyNonExist))
	rrForgotBad := httptest.NewRecorder()
	forgotHandler.ServeHTTP(rrForgotBad, reqForgotBad)
	if rrForgotBad.Code != http.StatusNotFound {
		t.Errorf("Expected 404 for non-existent email in forgot-password, got %d", rrForgotBad.Code)
	}

	// Valid email (sara@gmail.com)
	var origSaraHash string
	_ = db.QueryRow(`SELECT password_hash FROM users WHERE email = 'sara@gmail.com'`).Scan(&origSaraHash)
	defer func() {
		if origSaraHash != "" {
			_, _ = db.Exec(`UPDATE users SET password_hash = ? WHERE email = 'sara@gmail.com'`, origSaraHash)
		}
	}()

	bodySara, _ := json.Marshal(ForgotPasswordRequest{Email: "sara@gmail.com"})
	reqForgot := httptest.NewRequest(http.MethodPost, "/api/forgot-password", bytes.NewReader(bodySara))
	rrForgot := httptest.NewRecorder()
	forgotHandler.ServeHTTP(rrForgot, reqForgot)
	if rrForgot.Code != http.StatusOK {
		t.Fatalf("Expected 200 for valid forgot-password, got %d: %s", rrForgot.Code, rrForgot.Body.String())
	}

	var forgotResp ForgotPasswordResponse
	if err := json.Unmarshal(rrForgot.Body.Bytes(), &forgotResp); err != nil {
		t.Fatalf("Failed to parse forgot response: %v", err)
	}

	// Verify code in DB
	var dbCode string
	err = db.QueryRow(`SELECT code FROM password_resets WHERE email = 'sara@gmail.com' AND used = false ORDER BY id DESC LIMIT 1`).Scan(&dbCode)
	if err != nil {
		t.Fatalf("Failed to retrieve reset code from DB: %v", err)
	}
	if len(dbCode) != 6 {
		t.Errorf("Expected 6-digit code, got %s", dbCode)
	}

	// Reset with wrong code
	resetHandler := handleResetPassword(db, sessions)
	bodyResetBad, _ := json.Marshal(ResetPasswordRequest{
		Email:       "sara@gmail.com",
		Code:        "000000",
		NewPassword: "SaraNewPass123",
	})
	reqResetBad := httptest.NewRequest(http.MethodPost, "/api/reset-password", bytes.NewReader(bodyResetBad))
	rrResetBad := httptest.NewRecorder()
	resetHandler.ServeHTTP(rrResetBad, reqResetBad)
	if rrResetBad.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for wrong reset code, got %d", rrResetBad.Code)
	}

	// Reset with valid code
	bodyResetOk, _ := json.Marshal(ResetPasswordRequest{
		Email:       "sara@gmail.com",
		Code:        dbCode,
		NewPassword: "SaraNewPass123",
	})
	reqResetOk := httptest.NewRequest(http.MethodPost, "/api/reset-password", bytes.NewReader(bodyResetOk))
	rrResetOk := httptest.NewRecorder()
	resetHandler.ServeHTTP(rrResetOk, reqResetOk)
	if rrResetOk.Code != http.StatusOK {
		t.Fatalf("Expected 200 for valid reset, got %d: %s", rrResetOk.Code, rrResetOk.Body.String())
	}

	t.Logf("✅ Successfully tested Forgot Password & Reset Password flow with 6-digit verification code!")
}

func openPostgres(url string) (*sql.DB, error) {
	return sql.Open("postgres", url)
}

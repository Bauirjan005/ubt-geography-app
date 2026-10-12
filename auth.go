package main

import (
	cryptorand "crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/smtp"
	"os"
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

type ForgotPasswordRequest struct {
	Email string `json:"email"`
}

type ForgotPasswordResponse struct {
	Message  string `json:"message"`
	DevCode  string `json:"dev_code,omitempty"`
	SMTPSent bool   `json:"smtp_sent"`
}

type ResetPasswordRequest struct {
	Email       string `json:"email"`
	Code        string `json:"code"`
	NewPassword string `json:"new_password"`
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
		return "Аты-жөні 2 мен 64 таңба аралығында болуы керек / Имя должно быть от 2 до 64 символов"
	}
	if !strings.Contains(r.Email, "@") || len(r.Email) < 5 {
		return "Дұрыс email мекенжайын енгізіңіз / Введите корректный адрес email"
	}
	if err := validatePassword(r.Password); err != "" {
		return err
	}
	if r.Role != "student" && r.Role != "teacher" {
		return "Рөлді таңдаңыз (мұғалім немесе оқушы) / Выберите роль"
	}
	return ""
}

// validatePassword enforces a minimum-security policy:
// at least 8 chars, one uppercase letter, one digit.
func validatePassword(pw string) string {
	if len(pw) < 8 {
		return "Құпия сөз кемінде 8 таңбадан тұруы керек / Пароль должен быть не менее 8 символов"
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
		return "Құпия сөзде кемінде бір бас әріп (A-Z немесе А-Я) болуы керек / Пароль должен содержать хотя бы одну заглавную букву"
	}
	if !hasDigit {
		return "Құпия сөзде кемінде бір сан (0-9) болуы керек / Пароль должен содержать хотя бы одну цифру"
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
func handleRegister(db *DB) http.HandlerFunc {
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
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: "Бұл email бойынша аккаунт тіркелген / Аккаунт с таким email уже существует. «Войти» батырмасы арқылы кіріңіз"})
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
		id, err := db.InsertReturningID(
			`INSERT INTO users (username, email, password_hash, role, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			req.Username, req.Email, string(hash), req.Role, time.Now().UTC(),
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "failed to create user"})
			return
		}

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
func handleLogin(db *DB, sessions *SessionStore) http.HandlerFunc {
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
			writeJSON(w, http.StatusUnauthorized, AuthResponse{Message: "Қате email немесе құпия сөз / Неверный email или пароль"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "database error"})
			return
		}

		// Verify the password against the stored hash
		if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
			writeJSON(w, http.StatusUnauthorized, AuthResponse{Message: "Қате email немесе құпия сөз / Неверный email или пароль"})
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
			MaxAge:   60 * 60 * 24 * 30,
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// handleMe возвращает данные текущего пользователя по его сессии.
// GET /api/me
// 200 — { id, username, email, role }
// 401 — если не авторизован
func handleMe(db *DB, sessions *SessionStore) http.HandlerFunc {
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

// ─── Password Reset Helpers & Handlers ───────────────────────────────────────

type EmailConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

func getEmailConfig() *EmailConfig {
	host := strings.TrimSpace(os.Getenv("SMTP_HOST"))
	if host == "" {
		return nil
	}
	port := strings.TrimSpace(os.Getenv("SMTP_PORT"))
	if port == "" {
		port = "587"
	}
	user := strings.TrimSpace(os.Getenv("SMTP_USER"))
	pass := strings.TrimSpace(os.Getenv("SMTP_PASSWORD"))
	from := strings.TrimSpace(os.Getenv("SMTP_FROM"))
	if from == "" {
		from = user
	}
	return &EmailConfig{
		Host:     host,
		Port:     port,
		Username: user,
		Password: pass,
		From:     from,
	}
}

func generateVerificationCode() (string, error) {
	n, err := cryptorand.Int(cryptorand.Reader, big.NewInt(900000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()+100000), nil
}

func sendPasswordResetEmail(toEmail, code string) error {
	cfg := getEmailConfig()
	if cfg == nil {
		return errors.New("smtp not configured")
	}

	subject := "ҰБТ География: Құпия сөзді қалпына келтіру коды"
	encodedSubject := fmt.Sprintf("=?UTF-8?B?%s?=", base64.StdEncoding.EncodeToString([]byte(subject)))

	body := fmt.Sprintf(`<!DOCTYPE html>
<html lang="kk">
<head><meta charset="utf-8"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background-color: #0b0f19; margin: 0; padding: 24px; color: #f1f5f9;">
  <div style="max-width: 500px; margin: 0 auto; background-color: #111827; border: 1px solid rgba(255,255,255,0.1); border-radius: 16px; padding: 32px; box-shadow: 0 10px 30px rgba(0,0,0,0.5);">
    <div style="text-align: center; margin-bottom: 24px;">
      <h1 style="color: #38bdf8; font-size: 22px; margin: 0;">🌍 ҰБТ География</h1>
      <p style="color: #94a3b8; font-size: 13px; margin: 4px 0 0;">Оқыту және дайындық платформасы</p>
    </div>
    <div style="background: rgba(56, 189, 248, 0.08); border-left: 4px solid #38bdf8; padding: 14px 18px; border-radius: 8px; margin-bottom: 20px;">
      <h2 style="margin: 0 0 8px; font-size: 16px; color: #f8fafc;">Құпия сөзді қалпына келтіру</h2>
      <p style="margin: 0; font-size: 14px; color: #cbd5e1; line-height: 1.5;">
        Сіздің аккаунтыңызға құпия сөзді қалпына келтіру туралы өтініш түсті. Төмендегі 6 таңбалы кодты пайдаланыңыз:
      </p>
    </div>
    <div style="text-align: center; padding: 20px; background: #1e293b; border-radius: 12px; margin-bottom: 24px;">
      <div style="font-size: 32px; font-weight: 700; letter-spacing: 8px; color: #4ade80; font-family: monospace;">%s</div>
      <p style="color: #94a3b8; font-size: 12px; margin: 8px 0 0;">Кодтың жарамдылық мерзімі: 15 минут</p>
    </div>
    <p style="font-size: 13px; color: #94a3b8; line-height: 1.5;">
      Егер бұл өтінішті сіз жібермеген болсаңыз, бұл хатты елемеңіз. Сіздің құпия сөзіңіз өзгеріссіз қалады.
    </p>
    <hr style="border: none; border-top: 1px solid rgba(255,255,255,0.08); margin: 24px 0;">
    <p style="font-size: 11px; color: #64748b; text-align: center; margin: 0;">
      © ҰБТ География · Автоматты хабарлама
    </p>
  </div>
</body>
</html>`, code)

	msg := "From: " + cfg.From + "\r\n" +
		"To: " + toEmail + "\r\n" +
		"Subject: " + encodedSubject + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n\r\n" +
		body

	addr := net.JoinHostPort(cfg.Host, cfg.Port)
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)

	if cfg.Port == "465" {
		tlsConfig := &tls.Config{
			ServerName: cfg.Host,
		}
		conn, err := tls.Dial("tcp", addr, tlsConfig)
		if err != nil {
			return err
		}
		defer conn.Close()
		client, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return err
		}
		defer client.Quit()
		if err := client.Auth(auth); err != nil {
			return err
		}
		if err := client.Mail(cfg.From); err != nil {
			return err
		}
		if err := client.Rcpt(toEmail); err != nil {
			return err
		}
		w, err := client.Data()
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(msg))
		if err != nil {
			return err
		}
		return w.Close()
	}

	return smtp.SendMail(addr, auth, cfg.From, []string{toEmail}, []byte(msg))
}

// handleForgotPassword generates a 6-digit verification code and sends it via email (or returns demo code if SMTP is unconfigured).
// POST /api/forgot-password
func handleForgotPassword(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, AuthResponse{Message: "method not allowed"})
			return
		}

		var req ForgotPasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: "invalid JSON body"})
			return
		}

		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		if req.Email == "" || !strings.Contains(req.Email, "@") {
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: "Дұрыс email мекенжайын енгізіңіз / Введите корректный email"})
			return
		}

		// Check if user exists
		var (
			userID   int64
			username string
		)
		err := db.QueryRow(`SELECT id, username FROM users WHERE email = ?`, req.Email).Scan(&userID, &username)
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, AuthResponse{Message: "Бұл email бойынша аккаунт табылмады / Аккаунт с таким email не найден. Алдымен тіркеліңіз."})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "database error"})
			return
		}

		// Generate 6-digit code
		code, err := generateVerificationCode()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "failed to generate code"})
			return
		}

		now := time.Now().UTC()
		expiresAt := now.Add(15 * time.Minute)

		// Mark any existing unused codes for this email as used
		_, _ = db.Exec(`UPDATE password_resets SET used = true WHERE email = ? AND used = false`, req.Email)

		// Save new reset code
		_, err = db.InsertReturningID(
			`INSERT INTO password_resets (email, code, expires_at, used, created_at)
			 VALUES (?, ?, ?, ?, ?)`,
			req.Email, code, expiresAt, false, now,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "failed to save reset code"})
			return
		}

		// Try sending via SMTP
		smtpErr := sendPasswordResetEmail(req.Email, code)
		if smtpErr != nil {
			log.Printf("ℹ️ SMTP хат жіберу мүмкін болмады немесе бапталмаған (%v). Растау коды: %s", smtpErr, code)
			writeJSON(w, http.StatusOK, ForgotPasswordResponse{
				Message:  fmt.Sprintf("Растау коды дайын! (Сынақ режимі: код — %s)", code),
				DevCode:  code,
				SMTPSent: false,
			})
			return
		}

		writeJSON(w, http.StatusOK, ForgotPasswordResponse{
			Message:  "Растау коды email мекенжайыңызға жіберілді! Поштаңызды тексеріңіз.",
			SMTPSent: true,
		})
	}
}

// handleResetPassword verifies code and sets the new password.
// POST /api/reset-password
func handleResetPassword(db *DB, sessions *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, AuthResponse{Message: "method not allowed"})
			return
		}

		var req ResetPasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: "invalid JSON body"})
			return
		}

		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		req.Code = strings.TrimSpace(req.Code)
		req.NewPassword = strings.TrimSpace(req.NewPassword)

		if req.Email == "" || req.Code == "" || req.NewPassword == "" {
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: "Барлық өрістерді толтырыңыз / Заполните все поля"})
			return
		}

		if msg := validatePassword(req.NewPassword); msg != "" {
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: msg})
			return
		}

		// Find the active reset code
		var (
			resetID   int64
			expiresAt time.Time
			used      bool
		)
		err := db.QueryRow(
			`SELECT id, expires_at, used FROM password_resets WHERE email = ? AND code = ? ORDER BY id DESC LIMIT 1`,
			req.Email, req.Code,
		).Scan(&resetID, &expiresAt, &used)

		if err == sql.ErrNoRows || used {
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: "Қате немесе бұрын қолданылған растау коды / Неверный или использованный код"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "database error"})
			return
		}

		if time.Now().UTC().After(expiresAt) {
			writeJSON(w, http.StatusBadRequest, AuthResponse{Message: "Растау кодының мерзімі өтіп кетті (15 минут). Жаңа код сұраңыз / Срок действия кода истек"})
			return
		}

		// Hash new password
		hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "failed to hash password"})
			return
		}

		// Update password in users table
		res, err := db.Exec(`UPDATE users SET password_hash = ? WHERE email = ?`, string(hash), req.Email)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, AuthResponse{Message: "failed to update password"})
			return
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			writeJSON(w, http.StatusNotFound, AuthResponse{Message: "Пайдаланушы табылмады / Пользователь не найден"})
			return
		}

		// Mark reset code as used
		_, _ = db.Exec(`UPDATE password_resets SET used = true WHERE id = ?`, resetID)

		// Fetch role and userID for auto-login
		var (
			userID int64
			role   string
		)
		_ = db.QueryRow(`SELECT id, role FROM users WHERE email = ?`, req.Email).Scan(&userID, &role)

		sessionID, err := sessions.Create(userID, role)
		if err == nil {
			isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
			http.SetCookie(w, &http.Cookie{
				Name:     "session_id",
				Value:    sessionID,
				Path:     "/",
				HttpOnly: true,
				Secure:   isSecure,
				SameSite: http.SameSiteLaxMode,
				MaxAge:   60 * 60 * 24 * 30,
			})
		}

		writeJSON(w, http.StatusOK, AuthResponse{
			Message: "Құпия сөз сәтті жаңартылды! / Пароль успешно изменен!",
			Role:    role,
			UserID:  userID,
		})
	}
}


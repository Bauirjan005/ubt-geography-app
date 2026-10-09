package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const uploadDir = "./uploads"

// Лимит размера файла — меняй здесь
const maxFileSize = 100 * 1024 * 1024 // 100 МБ

func ensureUploadDir() {
	os.MkdirAll(uploadDir, 0755)
}

type MaterialSummary struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	OriginalName string `json:"original_name"`
	FileType     string `json:"file_type"`
	FileSize     int64  `json:"file_size"`
	ViewCount    int    `json:"view_count"`
	CreatedAt    string `json:"created_at"`
}

// POST /api/teacher/materials
// Принимает JSON с base64-encoded файлом
func handleUploadMaterial(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
			return
		}

		sess := SessionFromContext(r)

		// Ограничиваем размер тела запроса на уровне Go
		// base64 увеличивает размер примерно на 33%
		r.Body = http.MaxBytesReader(w, r.Body, int64(maxFileSize*4/3+1024))

		var req struct {
			Title    string `json:"title"`
			Filename string `json:"filename"`
			MimeType string `json:"mime_type"`
			Data     string `json:"data"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			if strings.Contains(err.Error(), "http: request body too large") {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
					"message": fmt.Sprintf("файл тым үлкен (макс %d МБ)", maxFileSize/1024/1024),
				})
				return
			}

			writeJSON(w, http.StatusBadRequest, map[string]string{
				"message": "invalid JSON: " + err.Error(),
			})
			return
		}

		req.Title = strings.TrimSpace(req.Title)
		req.Filename = strings.TrimSpace(req.Filename)

		if req.Title == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "title is required"})
			return
		}

		if req.Filename == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "filename is required"})
			return
		}

		if req.Data == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "file data is required"})
			return
		}

		// Проверяем расширение
		ext := strings.ToLower(filepath.Ext(req.Filename))

		allowed := map[string]string{
			".pdf": "application/pdf",
			".txt": "text/plain",
			".md":  "text/markdown",
		}

		mimeType, ok := allowed[ext]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"message": "рұқсат етілген форматтар: PDF, TXT, MD",
			})
			return
		}

		if req.MimeType != "" {
			mimeType = req.MimeType
		}

		// Декодируем base64
		fileData, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"message": "invalid base64 data",
			})
			return
		}

		// Проверяем реальный размер файла после декодирования
		if len(fileData) > maxFileSize {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{
				"message": fmt.Sprintf("файл тым үлкен (макс %d МБ)", maxFileSize/1024/1024),
			})
			return
		}

		// Генерируем уникальное имя файла на диске
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "failed to generate filename",
			})
			return
		}

		storedName := hex.EncodeToString(b) + ext
		storedPath := filepath.Join(uploadDir, storedName)

		if err := os.WriteFile(storedPath, fileData, 0644); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "failed to save file",
			})
			return
		}

		id, err := db.InsertReturningID(
			`INSERT INTO materials (teacher_id, title, filename, original_name, file_type, file_size, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sess.UserID,
			req.Title,
			storedName,
			req.Filename,
			mimeType,
			len(fileData),
			time.Now().UTC(),
		)
		if err != nil {
			os.Remove(storedPath)
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "database error",
			})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"message":     "жүктелді",
			"material_id": id,
			"size_mb":     fmt.Sprintf("%.2f МБ", float64(len(fileData))/1024/1024),
		})
	}
}

// GET /api/teacher/materials
func handleListTeacherMaterials(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromContext(r)

		rows, err := db.Query(`
			SELECT m.id, m.title, m.original_name, m.file_type, m.file_size,
			       COUNT(DISTINCT mv.student_id) AS view_count,
			       m.created_at
			FROM materials m
			LEFT JOIN material_views mv ON mv.material_id = m.id
			WHERE m.teacher_id = ?
			GROUP BY m.id
			ORDER BY m.created_at DESC
		`, sess.UserID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "database error",
			})
			return
		}
		defer rows.Close()

		var list []MaterialSummary

		for rows.Next() {
			var m MaterialSummary
			var ca time.Time

			if err := rows.Scan(
				&m.ID,
				&m.Title,
				&m.OriginalName,
				&m.FileType,
				&m.FileSize,
				&m.ViewCount,
				&ca,
			); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"message": "failed to read materials",
				})
				return
			}

			m.CreatedAt = ca.Format("2006-01-02")
			list = append(list, m)
		}

		// Проверяем ошибку, возникшую во время итерации
		if err := rows.Err(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "failed to read materials",
			})
			return
		}

		if list == nil {
			list = []MaterialSummary{}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"materials": list,
		})
	}
}

// GET /api/student/materials/{id}/view
func handleViewMaterial(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromContext(r)

		materialID, err := parseMaterialID(r.URL.Path)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"message": "invalid ID",
			})
			return
		}

		var filename, fileType, originalName string

		err = db.QueryRow(
			`SELECT filename, file_type, original_name FROM materials WHERE id = ?`,
			materialID,
		).Scan(&filename, &fileType, &originalName)

		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"message": "not found",
			})
			return
		}

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "database error",
			})
			return
		}

		// Записываем уникальный просмотр
		db.Exec(`
			INSERT OR IGNORE INTO material_views (material_id, student_id, viewed_at)
			VALUES (?, ?, ?)
		`, materialID, sess.UserID, time.Now().UTC())

		fPath := filepath.Join(uploadDir, filename)

		w.Header().Set("Content-Type", fileType)
		w.Header().Set(
			"Content-Disposition",
			fmt.Sprintf(`inline; filename="%s"`, originalName),
		)

		http.ServeFile(w, r, fPath)
	}
}

func parseMaterialID(path string) (int64, error) {
	path = strings.TrimSuffix(path, "/view")
	path = strings.TrimSuffix(path, "/generate-quiz")

	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	return strconv.ParseInt(parts[len(parts)-1], 10, 64)
}

// GET /api/teacher/materials/{id}/readers
// Материалды кім ашқанын ФИО + уақытымен береді
func handleMaterialReaders(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromContext(r)

		materialID, err := parseMaterialID(r.URL.Path)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"message": "invalid ID",
			})
			return
		}

		// Тек материал иесі көре алады
		var ownerID int64

		if err := db.QueryRow(
			`SELECT teacher_id FROM materials WHERE id = ?`,
			materialID,
		).Scan(&ownerID); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"message": "not found",
			})
			return
		}

		if ownerID != sess.UserID {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"message": "access denied",
			})
			return
		}

		rows, err := db.Query(`
			SELECT u.username, u.email, mv.viewed_at
			FROM material_views mv
			JOIN users u ON u.id = mv.student_id
			WHERE mv.material_id = ?
			ORDER BY mv.viewed_at DESC
		`, materialID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "database error",
			})
			return
		}
		defer rows.Close()

		type Reader struct {
			Username string `json:"username"`
			Email    string `json:"email"`
			ViewedAt string `json:"viewed_at"`
		}

		var readers []Reader

		for rows.Next() {
			var rd Reader
			var va time.Time

			if err := rows.Scan(&rd.Username, &rd.Email, &va); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"message": "failed to read material readers",
				})
				return
			}

			rd.ViewedAt = va.Format("02.01.2006 15:04")
			readers = append(readers, rd)
		}

		// Проверяем ошибку, возникшую во время итерации
		if err := rows.Err(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "failed to read material readers",
			})
			return
		}

		if readers == nil {
			readers = []Reader{}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"material_id": materialID,
			"total":       len(readers),
			"readers":     readers,
		})
	}
}

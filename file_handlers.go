package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
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
			".txt": "text/plain; charset=utf-8",
			".md":  "text/markdown; charset=utf-8",
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
		if (ext == ".txt" || ext == ".md") && !strings.Contains(mimeType, "charset") {
			mimeType += "; charset=utf-8"
		}

		// Декодируем base64
		fileData, err := base64.StdEncoding.DecodeString(req.Data)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"message": "invalid base64 data",
			})
			return
		}

		// Ensure text files are saved as clean UTF-8
		if ext == ".txt" || ext == ".md" {
			fileData = []byte(ensureUTF8(fileData))
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
			`INSERT INTO materials (teacher_id, title, filename, original_name, file_type, file_size, file_data, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sess.UserID,
			req.Title,
			storedName,
			req.Filename,
			mimeType,
			len(fileData),
			fileData,
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

// ─── Encoding & Format Helpers ───────────────────────────────────────────────

func ensureUTF8(b []byte) string {
	if utf8.Valid(b) {
		return strings.TrimPrefix(string(b), "\uFEFF")
	}

	// Fallback decoder for Windows-1251 (CP1251)
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		if c < 0x80 {
			sb.WriteByte(c)
		} else if c >= 0xC0 {
			sb.WriteRune(rune(0x0410 + int(c) - 0xC0))
		} else {
			switch c {
			case 0xA8:
				sb.WriteRune('Ё')
			case 0xB8:
				sb.WriteRune('ё')
			case 0xAA:
				sb.WriteRune('Є')
			case 0xBA:
				sb.WriteRune('є')
			case 0xAF:
				sb.WriteRune('Ї')
			case 0xBF:
				sb.WriteRune('ї')
			case 0xA5:
				sb.WriteRune('Ґ')
			case 0xB5:
				sb.WriteRune('ґ')
			case 0xA0:
				sb.WriteRune(' ')
			case 0x96:
				sb.WriteRune('–')
			case 0x97:
				sb.WriteRune('—')
			case 0xAB:
				sb.WriteRune('«')
			case 0xBB:
				sb.WriteRune('»')
			default:
				sb.WriteRune(rune(c))
			}
		}
	}
	return sb.String()
}

func contentDisposition(disposition, filename string) string {
	asciiFallback := strings.Map(func(r rune) rune {
		if r > 127 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, filename)
	if asciiFallback == "" {
		asciiFallback = "material"
	}
	encoded := url.PathEscape(filename)
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, disposition, asciiFallback, encoded)
}

func parseMaterialID(path string) (int64, error) {
	path = strings.TrimSuffix(path, "/view")
	path = strings.TrimSuffix(path, "/content")
	path = strings.TrimSuffix(path, "/generate-quiz")
	path = strings.TrimSuffix(path, "/readers")

	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	return strconv.ParseInt(parts[len(parts)-1], 10, 64)
}

// ─── handleViewMaterial ───────────────────────────────────────────────────────

// GET /api/student/materials/{id}/view
// GET /api/student/materials/{id}/content
// GET /api/teacher/materials/{id}/view
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

		var (
			filename     string
			fileType     string
			originalName string
			title        string
			fileData     []byte
		)

		err = db.QueryRow(
			`SELECT filename, file_type, original_name, title, file_data FROM materials WHERE id = ?`,
			materialID,
		).Scan(&filename, &fileType, &originalName, &title, &fileData)

		if err == sql.ErrNoRows {
			renderErrorPage(w, "Материал табылмады", "Кешіріңіз, бұл материал дерекқорда табылмады.")
			return
		}

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "database error",
			})
			return
		}

		// Студент көрген кезде ғана санағышты арттырамыз
		if sess != nil && sess.Role == "student" {
			db.Exec(`
				INSERT OR IGNORE INTO material_views (material_id, student_id, viewed_at)
				VALUES (?, ?, ?)
			`, materialID, sess.UserID, time.Now().UTC())
		}

		fPath := filepath.Join(uploadDir, filename)
		fileBytes, err := os.ReadFile(fPath)
		if err != nil || len(fileBytes) == 0 {
			if len(fileData) > 0 {
				fileBytes = fileData
				// Restore to disk cache
				_ = os.WriteFile(fPath, fileBytes, 0644)
			}
		}

		if len(fileBytes) == 0 {
			if strings.HasSuffix(r.URL.Path, "/content") || r.URL.Query().Get("format") == "json" {
				writeJSON(w, http.StatusNotFound, map[string]string{
					"message": "Файл табылмады / Файл не найден",
				})
				return
			}
			renderErrorPage(w, "Файл табылмады", "Кешіріңіз, бұл файл серверде табылмады. Мұғалім материалды қайта жүктеуі қажет.")
			return
		}

		ext := strings.ToLower(filepath.Ext(filename))
		if ext == "" {
			ext = strings.ToLower(filepath.Ext(originalName))
		}
		isPDF := ext == ".pdf" || strings.Contains(fileType, "pdf")
		isText := ext == ".txt" || ext == ".md" || strings.HasPrefix(fileType, "text/")

		// 1. JSON content сұранысы (дашборд ішіндегі оқу панелі үшін)
		if strings.HasSuffix(r.URL.Path, "/content") || r.URL.Query().Get("format") == "json" {
			if isText {
				textStr := ensureUTF8(fileBytes)
				writeJSON(w, http.StatusOK, map[string]any{
					"id":            materialID,
					"title":         title,
					"original_name": originalName,
					"file_type":     fileType,
					"file_size":     len(fileBytes),
					"is_text":       true,
					"content":       textStr,
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"id":            materialID,
				"title":         title,
				"original_name": originalName,
				"file_type":     fileType,
				"file_size":     len(fileBytes),
				"is_text":       false,
				"view_url":      fmt.Sprintf("/api/student/materials/%d/view", materialID),
			})
			return
		}

		// 2. Жүктеп алу сұранысы (?download=1)
		if r.URL.Query().Get("download") == "1" {
			mime := fileType
			if isText {
				mime = "text/plain; charset=utf-8"
			}
			w.Header().Set("Content-Type", mime)
			w.Header().Set("Content-Disposition", contentDisposition("attachment", originalName))
			w.Write(fileBytes)
			return
		}

		// 3. PDF файлын браузердің PDF-көру құралында ашу
		if isPDF {
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", contentDisposition("inline", originalName))
			w.Write(fileBytes)
			return
		}

		// 4. Тек шикі UTF-8 мәтін алу (?raw=1)
		if r.URL.Query().Get("raw") == "1" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Content-Disposition", contentDisposition("inline", originalName))
			w.Write([]byte(ensureUTF8(fileBytes)))
			return
		}

		// 5. TXT / MD үшін заманауи оқу залы (HTML Reader)
		renderHTMLReader(w, materialID, title, originalName, len(fileBytes), ensureUTF8(fileBytes))
	}
}

// ─── renderErrorPage ─────────────────────────────────────────────────────────

func renderErrorPage(w http.ResponseWriter, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="kk">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>%s | ҰБТ География</title>
  <style>
    body { background: #0b0f19; color: #f1f5f9; font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; display: flex; align-items: center; justify-content: center; min-height: 100vh; margin: 0; padding: 20px; box-sizing: border-box; }
    .card { background: #111827; border: 1px solid rgba(255,255,255,0.1); border-radius: 16px; padding: 36px; max-width: 460px; width: 100%%; text-align: center; box-shadow: 0 10px 30px rgba(0,0,0,0.4); }
    .icon { font-size: 48px; margin-bottom: 16px; }
    h2 { margin: 0 0 12px; font-size: 22px; color: #f87171; }
    p { margin: 0 0 24px; color: #94a3b8; font-size: 15px; line-height: 1.6; }
    .btn { display: inline-flex; align-items: center; justify-content: center; background: #10b981; color: #fff; text-decoration: none; padding: 10px 24px; border-radius: 10px; font-weight: 600; font-size: 14px; border: none; cursor: pointer; transition: background .2s; }
    .btn:hover { background: #059669; }
  </style>
</head>
<body>
  <div class="card">
    <div class="icon">📁⚠️</div>
    <h2>%s</h2>
    <p>%s</p>
    <button class="btn" onclick="window.history.length > 1 ? window.history.back() : window.location.href='/student_dashboard.html'">← Артқа қайту</button>
  </div>
</body>
</html>`, html.EscapeString(title), html.EscapeString(title), html.EscapeString(message))
	w.Write([]byte(html))
}

// ─── renderHTMLReader ─────────────────────────────────────────────────────────

func renderHTMLReader(w http.ResponseWriter, id int64, title, filename string, size int, content string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	words := len(strings.Fields(content))
	readMins := words / 160
	if readMins < 1 {
		readMins = 1
	}

	sizeStr := fmt.Sprintf("%d B", size)
	if size >= 1024*1024 {
		sizeStr = fmt.Sprintf("%.1f МБ", float64(size)/(1024*1024))
	} else if size >= 1024 {
		sizeStr = fmt.Sprintf("%.1f КБ", float64(size)/1024)
	}

	replacer := strings.NewReplacer(
		"{{TITLE}}", html.EscapeString(title),
		"{{FILENAME}}", html.EscapeString(filename),
		"{{SIZE}}", sizeStr,
		"{{WORDS}}", strconv.Itoa(words),
		"{{READ_MINS}}", strconv.Itoa(readMins),
		"{{CONTENT}}", html.EscapeString(content),
	)

	w.Write([]byte(replacer.Replace(htmlReaderTemplate)))
}

const htmlReaderTemplate = `<!DOCTYPE html>
<html lang="kk">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>{{TITLE}} — Оқу Залы | ҰБТ География</title>
  <link rel="preconnect" href="https://fonts.googleapis.com">
  <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
  <link href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700;800&family=Merriweather:ital,wght@0,300;0,400;0,700;1,300&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
  <style>
    :root {
      --bg: #0b0f19;
      --card: #111827;
      --surface: #1e293b;
      --border: rgba(255, 255, 255, 0.1);
      --text: #f1f5f9;
      --muted: #94a3b8;
      --primary: #10b981;
      --primary-hover: #059669;
      --accent: #38bdf8;
      --font-body: 'Merriweather', Georgia, serif;
      --font-ui: 'Plus Jakarta Sans', system-ui, sans-serif;
      --font-mono: 'JetBrains Mono', monospace;
      --reader-size: 17px;
      --reader-line: 1.85;
      --content-max-w: 860px;
    }
    body.theme-light {
      --bg: #f8fafc;
      --card: #ffffff;
      --surface: #f1f5f9;
      --border: #e2e8f0;
      --text: #0f172a;
      --muted: #64748b;
      --primary: #059669;
    }
    body.theme-sepia {
      --bg: #f4ecd8;
      --card: #fbf0d9;
      --surface: #eeddbb;
      --border: #dfcaa2;
      --text: #433422;
      --muted: #79664f;
      --primary: #92400e;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      background: var(--bg);
      color: var(--text);
      font-family: var(--font-ui);
      min-height: 100vh;
      display: flex;
      flex-direction: column;
      transition: background 0.25s, color 0.25s;
    }
    .topbar {
      position: sticky;
      top: 0;
      z-index: 100;
      backdrop-filter: blur(12px);
      background: rgba(17, 24, 39, 0.88);
      border-bottom: 1px solid var(--border);
      padding: 12px 24px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
    }
    body.theme-light .topbar { background: rgba(255, 255, 255, 0.92); }
    body.theme-sepia .topbar { background: rgba(244, 236, 216, 0.94); }
    .topbar-left {
      display: flex;
      align-items: center;
      gap: 12px;
      min-width: 0;
    }
    .back-btn {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 7px 14px;
      border-radius: 8px;
      background: var(--surface);
      color: var(--text);
      text-decoration: none;
      font-size: 13px;
      font-weight: 600;
      border: 1px solid var(--border);
      cursor: pointer;
      transition: all 0.2s;
      white-space: nowrap;
    }
    .back-btn:hover { border-color: var(--primary); color: var(--primary); }
    .doc-brand {
      font-size: 13px;
      font-weight: 700;
      color: var(--muted);
      display: flex;
      align-items: center;
      gap: 6px;
      white-space: nowrap;
    }
    .doc-brand span { color: var(--primary); }
    .topbar-right {
      display: flex;
      align-items: center;
      gap: 8px;
      flex-shrink: 0;
      flex-wrap: wrap;
    }
    .ctrl-btn {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      padding: 7px 11px;
      border-radius: 8px;
      background: var(--surface);
      color: var(--text);
      border: 1px solid var(--border);
      font-size: 13px;
      font-weight: 600;
      cursor: pointer;
      transition: all 0.2s;
      text-decoration: none;
    }
    .ctrl-btn:hover { border-color: var(--primary); color: var(--primary); }
    .ctrl-btn.active { background: var(--primary); color: #fff; border-color: var(--primary); }
    .btn-download {
      background: var(--primary);
      color: #fff;
      border: none;
    }
    .btn-download:hover { background: var(--primary-hover); color: #fff; }
    .main-wrap {
      flex: 1;
      width: 100%;
      max-width: var(--content-max-w);
      margin: 0 auto;
      padding: 32px 20px 80px;
    }
    .doc-header {
      background: var(--card);
      border: 1px solid var(--border);
      border-radius: 16px;
      padding: 24px 28px;
      margin-bottom: 24px;
      box-shadow: 0 4px 20px rgba(0,0,0,0.06);
    }
    .doc-badge {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 4px 10px;
      border-radius: 999px;
      background: rgba(16, 185, 129, 0.14);
      color: var(--primary);
      font-size: 12px;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0.5px;
      margin-bottom: 12px;
    }
    .doc-title {
      font-size: 26px;
      font-weight: 800;
      line-height: 1.3;
      margin-bottom: 14px;
      color: var(--text);
    }
    .doc-meta {
      display: flex;
      flex-wrap: wrap;
      gap: 16px;
      font-size: 13px;
      color: var(--muted);
      align-items: center;
    }
    .doc-meta-item {
      display: inline-flex;
      align-items: center;
      gap: 6px;
    }
    .reader-card {
      background: var(--card);
      border: 1px solid var(--border);
      border-radius: 16px;
      padding: 40px;
      box-shadow: 0 4px 24px rgba(0,0,0,0.08);
    }
    @media (max-width: 640px) {
      .reader-card { padding: 22px 16px; }
      .doc-header { padding: 18px; }
      .doc-title { font-size: 20px; }
      .topbar { padding: 10px 14px; }
    }
    .reader-text {
      font-family: var(--font-body);
      font-size: var(--reader-size);
      line-height: var(--reader-line);
      color: var(--text);
      white-space: pre-wrap;
      word-wrap: break-word;
      tab-size: 4;
      user-select: text;
    }
    .reader-text.font-sans { font-family: var(--font-ui); }
    .reader-text.font-mono { font-family: var(--font-mono); }
    .toast {
      position: fixed;
      bottom: 24px;
      right: 24px;
      padding: 12px 20px;
      border-radius: 10px;
      background: #10b981;
      color: #fff;
      font-weight: 600;
      font-size: 13px;
      box-shadow: 0 8px 24px rgba(0,0,0,0.3);
      opacity: 0;
      transform: translateY(10px);
      transition: all 0.25s;
      pointer-events: none;
      z-index: 1000;
    }
    .toast.show { opacity: 1; transform: translateY(0); }
  </style>
</head>
<body id="bodyRoot">
  <header class="topbar">
    <div class="topbar-left">
      <button class="back-btn" onclick="goBack()">← Артқа қайту</button>
      <div class="doc-brand">ҰБТ <span>География</span> • Оқу залы</div>
    </div>
    <div class="topbar-right">
      <button class="ctrl-btn" onclick="changeFontSize(-1)" title="Қаріпті кішірейту">A-</button>
      <button class="ctrl-btn" onclick="changeFontSize(1)" title="Қаріпті үлкейту">A+</button>
      <button class="ctrl-btn" id="fontToggleBtn" onclick="toggleFont()" title="Қаріп түрі">Қаріп: Кітап</button>
      <button class="ctrl-btn" id="themeToggleBtn" onclick="cycleTheme()" title="Тақырып ауыстыру">🌙 Қараңғы</button>
      <button class="ctrl-btn" onclick="copyContent()" title="Мәтінді көшіру">📋 Көшіру</button>
      <a class="ctrl-btn btn-download" href="?download=1" title="Түпнұсқаны жүктеу">📥 Жүктеу</a>
    </div>
  </header>

  <main class="main-wrap">
    <div class="doc-header">
      <div class="doc-badge">📖 Оқу материалы</div>
      <h1 class="doc-title">{{TITLE}}</h1>
      <div class="doc-meta">
        <div class="doc-meta-item">📄 {{FILENAME}}</div>
        <div class="doc-meta-item">💾 {{SIZE}}</div>
        <div class="doc-meta-item">📝 {{WORDS}} сөз</div>
        <div class="doc-meta-item">⏱️ ~{{READ_MINS}} мин оқу</div>
      </div>
    </div>

    <article class="reader-card">
      <div id="readerContent" class="reader-text">{{CONTENT}}</div>
    </article>
  </main>

  <div id="toastBox" class="toast">Мәтін көшірілді! ✓</div>

  <script>
    let currentSize = 17;
    function changeFontSize(delta) {
      currentSize = Math.max(13, Math.min(28, currentSize + delta * 2));
      document.documentElement.style.setProperty('--reader-size', currentSize + 'px');
      try { localStorage.setItem('reader_size', currentSize); } catch(e){}
    }

    const fonts = [
      { name: 'Кітап (Serif)', cls: '' },
      { name: 'Заманауи (Sans)', cls: 'font-sans' },
      { name: 'Моноширина', cls: 'font-mono' }
    ];
    let fontIdx = 0;
    function toggleFont() {
      fontIdx = (fontIdx + 1) % fonts.length;
      const el = document.getElementById('readerContent');
      el.className = 'reader-text ' + fonts[fontIdx].cls;
      document.getElementById('fontToggleBtn').textContent = 'Қаріп: ' + fonts[fontIdx].name;
    }

    const themes = [
      { name: '🌙 Қараңғы', cls: '' },
      { name: '📜 Сепия', cls: 'theme-sepia' },
      { name: '☀️ Ашық', cls: 'theme-light' }
    ];
    let themeIdx = 0;
    function cycleTheme() {
      themeIdx = (themeIdx + 1) % themes.length;
      document.getElementById('bodyRoot').className = themes[themeIdx].cls;
      document.getElementById('themeToggleBtn').textContent = themes[themeIdx].name;
      try { localStorage.setItem('reader_theme', themeIdx); } catch(e){}
    }

    function copyContent() {
      const text = document.getElementById('readerContent').innerText;
      navigator.clipboard.writeText(text).then(() => {
        showToast('Мәтін алмасу буферіне көшірілді! ✓');
      }).catch(() => {
        showToast('Көшіру сәтсіз аяқталды');
      });
    }

    function showToast(msg) {
      const t = document.getElementById('toastBox');
      t.textContent = msg;
      t.classList.add('show');
      setTimeout(() => t.classList.remove('show'), 2500);
    }

    function goBack() {
      if (window.history.length > 1) {
        window.history.back();
      } else {
        window.location.href = '/student_dashboard.html';
      }
    }

    // Load saved settings
    try {
      const savedTheme = localStorage.getItem('reader_theme');
      if (savedTheme !== null) {
        themeIdx = parseInt(savedTheme, 10) || 0;
        document.getElementById('bodyRoot').className = themes[themeIdx].cls;
        document.getElementById('themeToggleBtn').textContent = themes[themeIdx].name;
      }
      const savedSize = localStorage.getItem('reader_size');
      if (savedSize !== null) {
        currentSize = parseInt(savedSize, 10) || 17;
        document.documentElement.style.setProperty('--reader-size', currentSize + 'px');
      }
    } catch(e){}
  </script>
</body>
</html>`

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

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const claudeAPI = "https://api.anthropic.com/v1/messages"
const maxCharsForAI = 20000

type AIQuestion struct {
	ID            int64  `json:"id"`
	Text          string `json:"text"`
	OptionA       string `json:"option_a"`
	OptionB       string `json:"option_b"`
	OptionC       string `json:"option_c"`
	OptionD       string `json:"option_d"`
	CorrectOption string `json:"correct_option"`
	Explanation   string `json:"explanation"`
}

type rawAIQuestion struct {
	Text          string `json:"text"`
	OptionA       string `json:"option_a"`
	OptionB       string `json:"option_b"`
	OptionC       string `json:"option_c"`
	OptionD       string `json:"option_d"`
	CorrectOption string `json:"correct_option"`
	Explanation   string `json:"explanation"`
}

func prepareText(fileData []byte, fileType string) string {
	var text string
	switch fileType {
	case "application/pdf":
		raw := string(fileData)
		var sb strings.Builder
		inText := false
		for i := 0; i < len(raw); i++ {
			ch := raw[i]
			if ch == '(' && (i == 0 || raw[i-1] != '\\') {
				inText = true
				continue
			}
			if ch == ')' && inText {
				inText = false
				sb.WriteByte(' ')
				continue
			}
			if inText && ch >= 32 && ch < 127 {
				sb.WriteByte(ch)
			}
		}
		text = sb.String()
		if len(strings.TrimSpace(text)) < 100 {
			text = "UBT geography general questions."
		}
	default:
		text = string(fileData)
	}
	runes := []rune(text)
	if len(runes) > maxCharsForAI {
		text = string(runes[:maxCharsForAI])
	}
	if strings.TrimSpace(text) == "" {
		text = "UBT geography general questions."
	}
	return text
}

func buildPrompt(text, difficulty string) string {
	n := 60
	diffDesc := map[string]string{
		"easy":   "basic facts and simple definitions",
		"medium": "applying concepts and understanding connections",
		"hard":   "deep analysis, synthesis, geographic processes",
	}
	desc := diffDesc[difficulty]
	if desc == "" {
		desc = "medium level"
	}
	return fmt.Sprintf(
		`You are an expert creating UBT geography test questions in KAZAKH language.
Create EXACTLY %d questions based on the material below.
Level: %s (%s).
ALL questions and answers MUST be in KAZAKH language.
Questions must not repeat each other.

Return ONLY a JSON array. No markdown, no preamble.
Array must have exactly %d elements:
[{"text":"Question?","option_a":"A","option_b":"B","option_c":"C","option_d":"D","correct_option":"A","explanation":"Why correct"}]
correct_option must be only "A","B","C" or "D".
Each option max 15 words. Explanation max 20 words.

Study material:
%s`, n, difficulty, desc, n, text)
}

func parseQuestions(raw string) ([]rawAIQuestion, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start != -1 && end != -1 && end > start {
		raw = raw[start : end+1]
	}
	var questions []rawAIQuestion
	if err := json.Unmarshal([]byte(raw), &questions); err != nil {
		return nil, fmt.Errorf("JSON parse error: %w\nRaw: %.300s", err, raw)
	}
	return questions, nil
}

func callGemini(apiKey, text, difficulty string) ([]rawAIQuestion, error) {
	prompt := buildPrompt(text, difficulty)

	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-3.5-flash-lite:generateContent?key=" + apiKey

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{
						"text": prompt,
					},
				},
			},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": 8192,
		},
	}

	body, _ := json.Marshal(payload)

	client := &http.Client{Timeout: 120 * time.Second}

	var resp *http.Response
	var respBody []byte

	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest("POST", url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err = client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("Gemini request failed: %w", err)
		}
		respBody, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 503 || resp.StatusCode == 429 {
			wait := time.Duration(attempt*15) * time.Second
			time.Sleep(wait)
			continue
		}
		break
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gemini returned %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil ||
		len(apiResp.Candidates) == 0 ||
		len(apiResp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("cannot parse Gemini response: %.200s", string(respBody))
	}
	return parseQuestions(apiResp.Candidates[0].Content.Parts[0].Text)
}

func callDeepSeek(apiKey, text, difficulty string) ([]rawAIQuestion, error) {
	prompt := buildPrompt(text, difficulty)
	payload := map[string]any{
		"model":       "deepseek-chat",
		"messages":    []map[string]any{{"role": "user", "content": prompt}},
		"max_tokens":  8192,
		"temperature": 0.7,
	}
	return callOpenAICompatible("https://api.deepseek.com/chat/completions", apiKey, payload)
}

func callClaude(apiKey, text, difficulty string) ([]rawAIQuestion, error) {
	prompt := buildPrompt(text, difficulty)
	payload := map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 8192,
		"messages":   []map[string]any{{"role": "user", "content": prompt}},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Claude request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Claude returned %d: %s", resp.StatusCode, string(respBody))
	}
	var apiResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil || len(apiResp.Content) == 0 {
		return nil, fmt.Errorf("cannot parse Claude response")
	}
	return parseQuestions(apiResp.Content[0].Text)
}

func callOpenAICompatible(url, apiKey string, payload map[string]any) ([]rawAIQuestion, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
	}
	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil || len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("cannot parse response: %.200s", string(respBody))
	}
	return parseQuestions(apiResp.Choices[0].Message.Content)
}

func callClaudeForQuiz(text, difficulty string) ([]rawAIQuestion, error) {
	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		return callGemini(key, text, difficulty)
	}
	if key := os.Getenv("DEEPSEEK_API_KEY"); key != "" {
		return callDeepSeek(key, text, difficulty)
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		return callClaude(key, text, difficulty)
	}
	return nil, fmt.Errorf("API key not found. Set GEMINI_API_KEY")
}

// POST /api/teacher/materials/{id}/generate-quiz
func handleGenerateAIQuiz(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
			return
		}
		sess := SessionFromContext(r)

		materialID, err := parseMaterialID(r.URL.Path)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid material ID"})
			return
		}

		var req struct {
			Difficulty string `json:"difficulty"`
			Title      string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid JSON"})
			return
		}
		if req.Difficulty != "easy" && req.Difficulty != "medium" && req.Difficulty != "hard" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "difficulty must be easy, medium, or hard"})
			return
		}
		if strings.TrimSpace(req.Title) == "" {
			req.Title = "AI Test - " + req.Difficulty
		}

		var filename, fileType string
		err = db.QueryRow(
			`SELECT filename, file_type FROM materials WHERE id = ? AND teacher_id = ?`,
			materialID, sess.UserID,
		).Scan(&filename, &fileType)
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "material not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}

		fileData, err := os.ReadFile(filepath.Join(uploadDir, filename))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "cannot read file"})
			return
		}

		text := prepareText(fileData, fileType)
		questions, err := callClaudeForQuiz(text, req.Difficulty)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "AI error: " + err.Error()})
			return
		}
		if len(questions) == 0 {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "AI could not generate questions"})
			return
		}

		tx, err := db.Begin()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}
		defer tx.Rollback()

		res, err := tx.Exec(
			`INSERT INTO ai_quizzes (material_id, teacher_id, title, difficulty, created_at) VALUES (?, ?, ?, ?, ?)`,
			materialID, sess.UserID, req.Title, req.Difficulty, time.Now().UTC(),
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "failed to create quiz"})
			return
		}
		quizID, _ := res.LastInsertId()

		for i, q := range questions {
			correct := strings.ToUpper(strings.TrimSpace(q.CorrectOption))
			if correct != "A" && correct != "B" && correct != "C" && correct != "D" {
				correct = "A"
			}
			if _, err := tx.Exec(
				`INSERT INTO ai_questions (quiz_id, position, text, option_a, option_b, option_c, option_d, correct_option, explanation)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				quizID, i+1, q.Text, q.OptionA, q.OptionB, q.OptionC, q.OptionD, correct, q.Explanation,
			); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "failed to save questions"})
				return
			}
		}

		if err := tx.Commit(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "commit failed"})
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"message":        "quiz generated",
			"quiz_id":        quizID,
			"question_count": len(questions),
		})
	}
}

// GET /api/teacher/ai-quizzes
func handleListTeacherAIQuizzes(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromContext(r)
		rows, err := db.Query(`
			SELECT aq.id, aq.title, aq.difficulty, m.title,
			       COUNT(DISTINCT aqu.id),
			       COUNT(DISTINCT att.id),
			       COALESCE(AVG(CAST(att.score AS FLOAT) / NULLIF(att.total,0) * 100), 0),
			       aq.created_at
			FROM ai_quizzes aq
			JOIN materials m ON m.id = aq.material_id
			LEFT JOIN ai_questions aqu ON aqu.quiz_id = aq.id
			LEFT JOIN ai_quiz_attempts att ON att.quiz_id = aq.id
			WHERE aq.teacher_id = ?
			GROUP BY aq.id
			ORDER BY aq.created_at DESC
		`, sess.UserID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}
		defer rows.Close()

		type Item struct {
			ID            int64   `json:"id"`
			Title         string  `json:"title"`
			Difficulty    string  `json:"difficulty"`
			MaterialTitle string  `json:"material_title"`
			QuestionCount int     `json:"question_count"`
			AttemptCount  int     `json:"attempt_count"`
			AvgPercent    float64 `json:"avg_percent"`
			CreatedAt     string  `json:"created_at"`
		}
		var list []Item
		for rows.Next() {
			var it Item
			var ca time.Time
			if err := rows.Scan(&it.ID, &it.Title, &it.Difficulty, &it.MaterialTitle,
				&it.QuestionCount, &it.AttemptCount, &it.AvgPercent, &ca); err != nil {
				continue
			}
			it.CreatedAt = ca.Format("2006-01-02")
			list = append(list, it)
		}
		if list == nil {
			list = []Item{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"quizzes": list})
	}
}

// GET /api/teacher/ai-quizzes/{id}/results
func handleGetAIQuizResults(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromContext(r)

		path := strings.TrimSuffix(r.URL.Path, "/results")
		parts := strings.Split(strings.TrimRight(path, "/"), "/")
		quizID, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid quiz ID"})
			return
		}

		var ownerID int64
		if err := db.QueryRow(`SELECT teacher_id FROM ai_quizzes WHERE id = ?`, quizID).Scan(&ownerID); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "quiz not found"})
			return
		}
		if ownerID != sess.UserID {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "access denied"})
			return
		}

		rows, err := db.Query(`
			SELECT u.username, u.email, att.score, att.total,
			       ROUND(CAST(att.score AS FLOAT) / NULLIF(att.total,0) * 100) AS pct,
			       att.completed_at
			FROM ai_quiz_attempts att
			JOIN users u ON u.id = att.student_id
			WHERE att.quiz_id = ?
			ORDER BY att.completed_at DESC
		`, quizID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}
		defer rows.Close()

		type Result struct {
			Username    string  `json:"username"`
			Email       string  `json:"email"`
			Score       int     `json:"score"`
			Total       int     `json:"total"`
			Percent     float64 `json:"percent"`
			CompletedAt string  `json:"completed_at"`
		}
		var results []Result
		for rows.Next() {
			var res Result
			var ca time.Time
			if err := rows.Scan(&res.Username, &res.Email, &res.Score, &res.Total, &res.Percent, &ca); err != nil {
				continue
			}
			res.CompletedAt = ca.Format("02.01.2006 15:04")
			results = append(results, res)
		}
		if results == nil {
			results = []Result{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	}
}

func difficultyLabel(d string) string {
	switch d {
	case "easy":
		return "Zhengil"
	case "medium":
		return "Orta"
	case "hard":
		return "Kiyn"
	}
	return d
}

func parseAIQuizID(path string) (int64, error) {
	path = strings.TrimSuffix(path, "/complete")
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	return strconv.ParseInt(parts[len(parts)-1], 10, 64)
}

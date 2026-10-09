package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ── Response types ────────────────────────────────────────────────────────────

type StudentLesson struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	QuestionCount int    `json:"question_count"`
	Completed     bool   `json:"completed"`
	Score         int    `json:"score,omitempty"`
	Total         int    `json:"total,omitempty"`
}

type StudentLessonsResponse struct {
	Lessons []StudentLesson `json:"lessons"`
}

type QuestionForStudent struct {
	ID      int64  `json:"id"`
	Text    string `json:"text"`
	OptionA string `json:"option_a"`
	OptionB string `json:"option_b"`
	OptionC string `json:"option_c"`
	OptionD string `json:"option_d"`
}

type LessonDetailResponse struct {
	ID        int64                `json:"id"`
	Title     string               `json:"title"`
	Content   string               `json:"content"`
	Questions []QuestionForStudent `json:"questions"`
}

type AttemptRequest struct {
	Answers map[string]string `json:"answers"`
}

type AttemptResult struct {
	Score    int                     `json:"score"`
	Total    int                     `json:"total"`
	Feedback map[string]FeedbackItem `json:"feedback"`
}

type FeedbackItem struct {
	Correct string `json:"correct"`
	Chosen  string `json:"chosen"`
	IsRight bool   `json:"is_right"`
}

// ── GET /api/student/lessons ──────────────────────────────────────────────────

func handleListStudentLessons(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
			return
		}

		sess := SessionFromContext(r)

		rows, err := db.Query(`
			SELECT
				l.id, l.title,
				COUNT(q.id) AS question_count,
				COALESCE(sa.score, 0) AS score,
				COALESCE(sa.total, 0) AS total,
				CASE WHEN sa.id IS NOT NULL THEN 1 ELSE 0 END AS completed
			FROM lessons l
			LEFT JOIN questions q ON q.lesson_id = l.id
			LEFT JOIN student_attempts sa ON sa.lesson_id = l.id AND sa.student_id = ?
			GROUP BY l.id, l.title, l.created_at, sa.score, sa.total, sa.id
			ORDER BY l.created_at DESC
		`, sess.UserID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}
		defer rows.Close()

		var lessons []StudentLesson

		for rows.Next() {
			var l StudentLesson
			var completed int

			if err := rows.Scan(
				&l.ID,
				&l.Title,
				&l.QuestionCount,
				&l.Score,
				&l.Total,
				&completed,
			); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"message": "failed to read lessons",
				})
				return
			}

			l.Completed = completed == 1
			lessons = append(lessons, l)
		}

		if err := rows.Err(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "failed to read lessons",
			})
			return
		}

		if lessons == nil {
			lessons = []StudentLesson{}
		}

		writeJSON(w, http.StatusOK, StudentLessonsResponse{Lessons: lessons})
	}
}

// ── GET /api/student/lessons/{id} ────────────────────────────────────────────

func handleGetLessonDetail(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
			return
		}

		lessonID, err := studentLessonIDFromPath(r.URL.Path)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid lesson ID"})
			return
		}

		var lesson LessonDetailResponse

		err = db.QueryRow(
			`SELECT id, title, content FROM lessons WHERE id = ?`,
			lessonID,
		).Scan(&lesson.ID, &lesson.Title, &lesson.Content)

		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "lesson not found"})
			return
		}

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}

		// correct_option жіберілмейді — алдау мүмкін болмасын
		rows, err := db.Query(`
			SELECT id, text, option_a, option_b, option_c, option_d
			FROM questions
			WHERE lesson_id = ?
			ORDER BY position ASC
		`, lessonID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}
		defer rows.Close()

		for rows.Next() {
			var q QuestionForStudent

			if err := rows.Scan(
				&q.ID,
				&q.Text,
				&q.OptionA,
				&q.OptionB,
				&q.OptionC,
				&q.OptionD,
			); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"message": "failed to read questions",
				})
				return
			}

			lesson.Questions = append(lesson.Questions, q)
		}

		if err := rows.Err(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "failed to read questions",
			})
			return
		}

		if lesson.Questions == nil {
			lesson.Questions = []QuestionForStudent{}
		}

		writeJSON(w, http.StatusOK, lesson)
	}
}

// ── POST /api/student/lessons/{id}/attempt ────────────────────────────────────

func handleSubmitAttempt(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
			return
		}

		sess := SessionFromContext(r)

		lessonID, err := studentLessonIDFromPath(strings.TrimSuffix(r.URL.Path, "/attempt"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid lesson ID"})
			return
		}

		var req AttemptRequest

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid JSON body"})
			return
		}

		// Дерекқордан дұрыс жауаптарды алу
		rows, err := db.Query(
			`SELECT id, correct_option FROM questions WHERE lesson_id = ? ORDER BY position ASC`,
			lessonID,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}
		defer rows.Close()

		feedback := make(map[string]FeedbackItem)
		score := 0
		total := 0

		for rows.Next() {
			var qID int64
			var correctOption string

			if err := rows.Scan(&qID, &correctOption); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"message": "failed to read answers",
				})
				return
			}

			total++

			key := strconv.FormatInt(qID, 10)
			chosen := strings.ToUpper(strings.TrimSpace(req.Answers[key]))
			isRight := chosen == correctOption

			if isRight {
				score++
			}

			feedback[key] = FeedbackItem{
				Correct: correctOption,
				Chosen:  chosen,
				IsRight: isRight,
			}
		}

		if err := rows.Err(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "failed to read answers",
			})
			return
		}

		if total == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "this lesson has no questions"})
			return
		}

		// Нәтижені сақтау (қайта тапсырса жаңартады)
		_, err = db.Exec(`
			INSERT INTO student_attempts (student_id, lesson_id, score, total, completed_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(student_id, lesson_id)
			DO UPDATE SET score=excluded.score, total=excluded.total, completed_at=excluded.completed_at
		`, sess.UserID, lessonID, score, total, time.Now().UTC())

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "failed to save attempt"})
			return
		}

		writeJSON(w, http.StatusOK, AttemptResult{
			Score:    score,
			Total:    total,
			Feedback: feedback,
		})
	}
}

// ── GET /api/student/ai-quizzes ───────────────────────────────────────────────

func handleListAIQuizzes(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromContext(r)

		rows, err := db.Query(`
			SELECT aq.id, aq.title, aq.difficulty, m.title,
			       COUNT(aqu.id),
			       CASE WHEN att.id IS NOT NULL THEN 1 ELSE 0 END,
			       COALESCE(att.score,0), COALESCE(att.total,0),
			       aq.created_at
			FROM ai_quizzes aq
			JOIN materials m ON m.id = aq.material_id
			LEFT JOIN ai_questions aqu ON aqu.quiz_id = aq.id
			LEFT JOIN ai_quiz_attempts att ON att.quiz_id = aq.id AND att.student_id = ?
			WHERE (aq.status IS NULL OR aq.status = 'published')
			GROUP BY aq.id, aq.title, aq.difficulty, m.title, att.id, att.score, att.total, aq.created_at
			ORDER BY aq.created_at DESC
		`, sess.UserID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}
		defer rows.Close()

		type AIQuizItem struct {
			ID            int64  `json:"id"`
			Title         string `json:"title"`
			Difficulty    string `json:"difficulty"`
			MaterialTitle string `json:"material_title"`
			QuestionCount int    `json:"question_count"`
			Completed     bool   `json:"completed"`
			Score         int    `json:"score"`
			Total         int    `json:"total"`
			CreatedAt     string `json:"created_at"`
		}

		var list []AIQuizItem

		for rows.Next() {
			var q AIQuizItem
			var completed int
			var ca time.Time

			if err := rows.Scan(
				&q.ID,
				&q.Title,
				&q.Difficulty,
				&q.MaterialTitle,
				&q.QuestionCount,
				&completed,
				&q.Score,
				&q.Total,
				&ca,
			); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"message": "failed to read AI quizzes",
				})
				return
			}

			q.Completed = completed == 1
			q.CreatedAt = ca.Format("2006-01-02")
			list = append(list, q)
		}

		if err := rows.Err(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "failed to read AI quizzes",
			})
			return
		}

		if list == nil {
			list = []AIQuizItem{}
		}

		writeJSON(w, http.StatusOK, map[string]any{"quizzes": list})
	}
}

// ── GET /api/student/ai-quizzes/{id} — әр рет жаңа 20 сұрақ ─────────────────

func handleGetAIQuizForStudent(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromContext(r)

		quizID, err := parseAIQuizID(r.URL.Path)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid quiz ID"})
			return
		}

		type QuizDetail struct {
			ID         int64        `json:"id"`
			Title      string       `json:"title"`
			Difficulty string       `json:"difficulty"`
			Questions  []AIQuestion `json:"questions"`
			IsNewRound bool         `json:"is_new_round"`
		}

		var qz QuizDetail

		err = db.QueryRow(
			`SELECT id, title, difficulty FROM ai_quizzes WHERE id = ? AND (status IS NULL OR status = 'published')`,
			quizID,
		).Scan(&qz.ID, &qz.Title, &qz.Difficulty)

		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "quiz not found"})
			return
		}

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}

		const questionsPerRound = 20

		// Оқушы бұрын көрмеген сұрақтарды алу
		questions, err := fetchUnseenQuestions(db, sess.UserID, quizID, questionsPerRound)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}

		// Жеткіліксіз болса — тарихты тазалап жаңа раунд бастайды
		if len(questions) < questionsPerRound {
			_, err := db.Exec(`
				DELETE FROM student_seen_questions
				WHERE student_id = ? AND quiz_id = ?
			`, sess.UserID, quizID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"message": "failed to reset question history",
				})
				return
			}

			questions, err = fetchUnseenQuestions(db, sess.UserID, quizID, questionsPerRound)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
				return
			}

			qz.IsNewRound = true
		}

		// Берілген сұрақтарды "көрілді" деп белгілеу
		now := time.Now().UTC()

		for _, q := range questions {
			_, err := db.Exec(`
				INSERT OR IGNORE INTO student_seen_questions (student_id, quiz_id, question_id, seen_at)
				VALUES (?, ?, ?, ?)
			`, sess.UserID, quizID, q.ID, now)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{
					"message": "failed to save question history",
				})
				return
			}
		}

		if questions == nil {
			questions = []AIQuestion{}
		}

		qz.Questions = questions
		writeJSON(w, http.StatusOK, qz)
	}
}

func fetchUnseenQuestions(db *DB, studentID, quizID int64, limit int) ([]AIQuestion, error) {
	rows, err := db.Query(`
		SELECT id, text, option_a, option_b, option_c, option_d, correct_option, explanation
		FROM ai_questions
		WHERE quiz_id = ?
		  AND id NOT IN (
			SELECT question_id FROM student_seen_questions
			WHERE student_id = ? AND quiz_id = ?
		  )
		ORDER BY RANDOM()
		LIMIT ?
	`, quizID, studentID, quizID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var questions []AIQuestion

	for rows.Next() {
		var q AIQuestion

		if err := rows.Scan(
			&q.ID,
			&q.Text,
			&q.OptionA,
			&q.OptionB,
			&q.OptionC,
			&q.OptionD,
			&q.CorrectOption,
			&q.Explanation,
		); err != nil {
			return nil, err
		}

		questions = append(questions, q)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return questions, nil
}

// ── POST /api/student/ai-quizzes/{id}/complete ───────────────────────────────

func handleCompleteAIQuiz(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
			return
		}

		sess := SessionFromContext(r)

		quizID, err := parseAIQuizID(strings.TrimSuffix(r.URL.Path, "/complete"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid quiz ID"})
			return
		}

		var req struct {
			Score int `json:"score"`
			Total int `json:"total"`
		}

		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid JSON"})
			return
		}

		_, err = db.Exec(`
			INSERT INTO ai_quiz_attempts (student_id, quiz_id, score, total, completed_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(student_id, quiz_id) DO UPDATE SET
				score=excluded.score, total=excluded.total, completed_at=excluded.completed_at
		`, sess.UserID, quizID, req.Score, req.Total, time.Now().UTC())

		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "failed to save"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"message": "saved"})
	}
}

// ── GET /api/student/materials ────────────────────────────────────────────────

func handleListStudentMaterials(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`
			SELECT m.id, m.title, m.original_name, m.file_type, m.file_size,
			       COUNT(DISTINCT mv.student_id) AS view_count,
			       m.created_at
			FROM materials m
			LEFT JOIN material_views mv ON mv.material_id = m.id
			GROUP BY m.id, m.title, m.original_name, m.file_type, m.file_size, m.created_at
			ORDER BY m.created_at DESC
		`)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
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

		if err := rows.Err(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"message": "failed to read materials",
			})
			return
		}

		if list == nil {
			list = []MaterialSummary{}
		}

		writeJSON(w, http.StatusOK, map[string]any{"materials": list})
	}
}

// ── RegisterStudentRoutes ─────────────────────────────────────────────────────

func RegisterStudentRoutes(mux *http.ServeMux, db *DB, sessions *SessionStore) {
	guard := requireRole(sessions, "student")

	// Lessons
	mux.Handle("/api/student/lessons", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleListStudentLessons(db)(w, r)
	})))

	mux.Handle("/api/student/lessons/", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/attempt") {
			handleSubmitAttempt(db)(w, r)
			return
		}
		handleGetLessonDetail(db)(w, r)
	})))

	// AI Quizzes
	mux.Handle("/api/student/ai-quizzes", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleListAIQuizzes(db)(w, r)
	})))

	mux.Handle("/api/student/ai-quizzes/", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/complete") && r.Method == http.MethodPost {
			handleCompleteAIQuiz(db)(w, r)
			return
		}
		// Жаңа 20 сұрақ — бұрын көрмегендері
		handleGetAIQuizForStudent(db)(w, r)
	})))

	// Materials
	mux.Handle("/api/student/materials", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleListStudentMaterials(db)(w, r)
	})))

	mux.Handle("/api/student/materials/", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleViewMaterial(db)(w, r)
	})))
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func studentLessonIDFromPath(path string) (int64, error) {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	return strconv.ParseInt(parts[len(parts)-1], 10, 64)
}

package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type CreateLessonRequest struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type CreateLessonResponse struct {
	Message  string `json:"message"`
	LessonID int64  `json:"lesson_id"`
}

type QuestionInput struct {
	Text    string `json:"text"`
	OptionA string `json:"option_a"`
	OptionB string `json:"option_b"`
	OptionC string `json:"option_c"`
	OptionD string `json:"option_d"`
	Correct string `json:"correct"`
}

type SaveQuestionsRequest struct {
	Questions []QuestionInput `json:"questions"`
}

type LessonSummary struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
}

type ListLessonsResponse struct {
	Lessons []LessonSummary `json:"lessons"`
}

func handleListLessons(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
			return
		}
		sess := SessionFromContext(r)
		rows, err := db.Query(
			`SELECT id, title, created_at FROM lessons WHERE teacher_id = ? ORDER BY created_at DESC`,
			sess.UserID,
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}
		defer rows.Close()

		var lessons []LessonSummary
		for rows.Next() {
			var l LessonSummary
			var createdAt time.Time
			if err := rows.Scan(&l.ID, &l.Title, &createdAt); err != nil {
				continue
			}
			l.CreatedAt = createdAt.Format("2006-01-02")
			lessons = append(lessons, l)
		}
		if lessons == nil {
			lessons = []LessonSummary{}
		}
		writeJSON(w, http.StatusOK, ListLessonsResponse{Lessons: lessons})
	}
}

func handleCreateLesson(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
			return
		}
		sess := SessionFromContext(r)

		var req CreateLessonRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid JSON body"})
			return
		}
		req.Title = strings.TrimSpace(req.Title)
		req.Content = strings.TrimSpace(req.Content)

		if req.Title == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "title is required"})
			return
		}
		if len(req.Title) > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "title must be under 200 characters"})
			return
		}

		id, err := db.InsertReturningID(
			`INSERT INTO lessons (teacher_id, title, content, created_at) VALUES (?, ?, ?, ?)`,
			sess.UserID, req.Title, req.Content, time.Now().UTC(),
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "failed to save lesson"})
			return
		}
		writeJSON(w, http.StatusCreated, CreateLessonResponse{Message: "lesson created", LessonID: id})
	}
}

func handleSaveQuestions(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
			return
		}
		sess := SessionFromContext(r)

		lessonID, err := lessonIDFromPath(r.URL.Path)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid lesson ID in URL"})
			return
		}

		var ownerID int64
		err = db.QueryRow(`SELECT teacher_id FROM lessons WHERE id = ?`, lessonID).Scan(&ownerID)
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "lesson not found"})
			return
		}
		if err != nil || ownerID != sess.UserID {
			writeJSON(w, http.StatusForbidden, map[string]string{"message": "access denied"})
			return
		}

		var req SaveQuestionsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid JSON body"})
			return
		}

		for i, q := range req.Questions {
			if strings.TrimSpace(q.Text) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"message": "question " + strconv.Itoa(i+1) + " has no text",
				})
				return
			}
			if strings.TrimSpace(q.OptionA) == "" || strings.TrimSpace(q.OptionB) == "" ||
				strings.TrimSpace(q.OptionC) == "" || strings.TrimSpace(q.OptionD) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"message": "question " + strconv.Itoa(i+1) + " is missing an option",
				})
				return
			}
			q.Correct = strings.ToUpper(strings.TrimSpace(q.Correct))
			if q.Correct != "A" && q.Correct != "B" && q.Correct != "C" && q.Correct != "D" {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"message": "question " + strconv.Itoa(i+1) + ": correct must be A, B, C, or D",
				})
				return
			}
			req.Questions[i] = q
		}

		tx, err := db.Begin()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}
		defer tx.Rollback()

		if _, err := tx.Exec(`DELETE FROM questions WHERE lesson_id = ?`, lessonID); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "failed to clear old questions"})
			return
		}
		for i, q := range req.Questions {
			_, err := tx.Exec(
				`INSERT INTO questions (lesson_id, position, text, option_a, option_b, option_c, option_d, correct_option)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				lessonID, i+1, q.Text, q.OptionA, q.OptionB, q.OptionC, q.OptionD, q.Correct,
			)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "failed to insert questions"})
				return
			}
		}

		if err := tx.Commit(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "transaction failed"})
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"message":        "questions saved",
			"question_count": len(req.Questions),
		})
	}
}

func RegisterTeacherRoutes(mux *http.ServeMux, db *DB, sessions *SessionStore) {
	guard := requireRole(sessions, "teacher")

	mux.Handle("/api/teacher/lessons", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListLessons(db)(w, r)
		case http.MethodPost:
			handleCreateLesson(db)(w, r)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"message": "method not allowed"})
		}
	})))

	mux.Handle("/api/teacher/lessons/", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/questions") {
			handleSaveQuestions(db)(w, r)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "not found"})
	})))
}

func lessonIDFromPath(path string) (int64, error) {
	path = strings.TrimSuffix(path, "/questions")
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	return strconv.ParseInt(parts[len(parts)-1], 10, 64)
}

// ── Student Results & Analytics ──────────────────────────────────────────────

type StudentAttemptItem struct {
	ID           int64   `json:"id"`
	StudentID    int64   `json:"student_id"`
	StudentName  string  `json:"student_name"`
	StudentEmail string  `json:"student_email"`
	TestType     string  `json:"test_type"` // "ai_quiz" or "lesson"
	TestID       int64   `json:"test_id"`
	TestTitle    string  `json:"test_title"`
	Score        int     `json:"score"`
	Total        int     `json:"total"`
	Percent      float64 `json:"percent"`
	CompletedAt  string  `json:"completed_at"`
}

type TopStudentItem struct {
	StudentID     int64   `json:"student_id"`
	StudentName   string  `json:"student_name"`
	StudentEmail  string  `json:"student_email"`
	AttemptsCount int     `json:"attempts_count"`
	AvgPercent    float64 `json:"avg_percent"`
	BestScore     float64 `json:"best_score"`
	LastActive    string  `json:"last_active"`
}

type AvailableTestItem struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"` // "ai_quiz" or "lesson"
}

// GET /api/teacher/results
func handleGetTeacherAllResults(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromContext(r)

		query := `
			SELECT 
				att.id,
				att.student_id,
				u.username,
				u.email,
				'ai_quiz' AS test_type,
				aq.id AS test_id,
				aq.title AS test_title,
				att.score,
				att.total,
				COALESCE(ROUND(CAST(att.score AS FLOAT) / NULLIF(att.total, 0) * 100), 0) AS pct,
				att.completed_at
			FROM ai_quiz_attempts att
			JOIN ai_quizzes aq ON aq.id = att.quiz_id
			JOIN users u ON u.id = att.student_id
			WHERE aq.teacher_id = ?

			UNION ALL

			SELECT 
				sa.id,
				sa.student_id,
				u.username,
				u.email,
				'lesson' AS test_type,
				l.id AS test_id,
				l.title AS test_title,
				sa.score,
				sa.total,
				COALESCE(ROUND(CAST(sa.score AS FLOAT) / NULLIF(sa.total, 0) * 100), 0) AS pct,
				sa.completed_at
			FROM student_attempts sa
			JOIN lessons l ON l.id = sa.lesson_id
			JOIN users u ON u.id = sa.student_id
			WHERE l.teacher_id = ?

			ORDER BY completed_at DESC
		`

		rows, err := db.Query(query, sess.UserID, sess.UserID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}
		defer rows.Close()

		var attempts []StudentAttemptItem
		type studentAgg struct {
			name       string
			email      string
			count      int
			sumPct     float64
			maxPct     float64
			lastActive string
		}
		studentMap := make(map[int64]*studentAgg)

		var totalScoreSum float64
		var highScores, midScores, lowScores int

		for rows.Next() {
			var att StudentAttemptItem
			var ca time.Time
			if err := rows.Scan(
				&att.ID, &att.StudentID, &att.StudentName, &att.StudentEmail,
				&att.TestType, &att.TestID, &att.TestTitle,
				&att.Score, &att.Total, &att.Percent, &ca,
			); err != nil {
				continue
			}
			att.CompletedAt = ca.Format("02.01.2006 15:04")
			attempts = append(attempts, att)

			totalScoreSum += att.Percent
			if att.Percent >= 80 {
				highScores++
			} else if att.Percent >= 40 {
				midScores++
			} else {
				lowScores++
			}

			st, exists := studentMap[att.StudentID]
			if !exists {
				st = &studentAgg{
					name:       att.StudentName,
					email:      att.StudentEmail,
					maxPct:     att.Percent,
					lastActive: att.CompletedAt,
				}
				studentMap[att.StudentID] = st
			}
			st.count++
			st.sumPct += att.Percent
			if att.Percent > st.maxPct {
				st.maxPct = att.Percent
			}
		}

		if attempts == nil {
			attempts = []StudentAttemptItem{}
		}

		// Calculate top students
		var topStudents []TopStudentItem
		for sID, st := range studentMap {
			avg := 0.0
			if st.count > 0 {
				avg = float64(int(st.sumPct/float64(st.count)*10+0.5)) / 10.0
			}
			topStudents = append(topStudents, TopStudentItem{
				StudentID:     sID,
				StudentName:   st.name,
				StudentEmail:  st.email,
				AttemptsCount: st.count,
				AvgPercent:    avg,
				BestScore:     st.maxPct,
				LastActive:    st.lastActive,
			})
		}
		sort.Slice(topStudents, func(i, j int) bool {
			if topStudents[i].AvgPercent != topStudents[j].AvgPercent {
				return topStudents[i].AvgPercent > topStudents[j].AvgPercent
			}
			return topStudents[i].AttemptsCount > topStudents[j].AttemptsCount
		})

		// Retrieve all available tests for filter dropdown
		var availableTests []AvailableTestItem

		// 1. AI quizzes
		aiRows, err := db.Query(`SELECT id, title FROM ai_quizzes WHERE teacher_id = ? ORDER BY title ASC`, sess.UserID)
		if err == nil {
			for aiRows.Next() {
				var item AvailableTestItem
				item.Type = "ai_quiz"
				if err := aiRows.Scan(&item.ID, &item.Title); err == nil {
					availableTests = append(availableTests, item)
				}
			}
			aiRows.Close()
		}

		// 2. Lessons
		lRows, err := db.Query(`SELECT id, title FROM lessons WHERE teacher_id = ? ORDER BY title ASC`, sess.UserID)
		if err == nil {
			for lRows.Next() {
				var item AvailableTestItem
				item.Type = "lesson"
				if err := lRows.Scan(&item.ID, &item.Title); err == nil {
					availableTests = append(availableTests, item)
				}
			}
			lRows.Close()
		}

		if availableTests == nil {
			availableTests = []AvailableTestItem{}
		}

		avgOverall := 0.0
		if len(attempts) > 0 {
			avgOverall = float64(int(totalScoreSum/float64(len(attempts))*10+0.5)) / 10.0
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"attempts": attempts,
			"stats": map[string]any{
				"total_attempts":  len(attempts),
				"unique_students": len(studentMap),
				"avg_percent":     avgOverall,
				"high_scores":     highScores,
				"mid_scores":      midScores,
				"low_scores":      lowScores,
			},
			"top_students":    topStudents,
			"available_tests": availableTests,
		})
	}
}

// GET /api/teacher/students/{id}/results
func handleGetTeacherStudentDetail(db *DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromContext(r)

		clean := strings.TrimSuffix(r.URL.Path, "/results")
		parts := strings.Split(strings.TrimRight(clean, "/"), "/")
		studentID, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid student ID"})
			return
		}

		var studentName, studentEmail string
		var studentCreatedAt time.Time
		err = db.QueryRow(`SELECT username, email, created_at FROM users WHERE id = ? AND role = 'student'`, studentID).
			Scan(&studentName, &studentEmail, &studentCreatedAt)
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "student not found"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}

		query := `
			SELECT 
				att.id,
				'ai_quiz' AS test_type,
				aq.id AS test_id,
				aq.title AS test_title,
				att.score,
				att.total,
				COALESCE(ROUND(CAST(att.score AS FLOAT) / NULLIF(att.total, 0) * 100), 0) AS pct,
				att.completed_at
			FROM ai_quiz_attempts att
			JOIN ai_quizzes aq ON aq.id = att.quiz_id
			WHERE aq.teacher_id = ? AND att.student_id = ?

			UNION ALL

			SELECT 
				sa.id,
				'lesson' AS test_type,
				l.id AS test_id,
				l.title AS test_title,
				sa.score,
				sa.total,
				COALESCE(ROUND(CAST(sa.score AS FLOAT) / NULLIF(sa.total, 0) * 100), 0) AS pct,
				sa.completed_at
			FROM student_attempts sa
			JOIN lessons l ON l.id = sa.lesson_id
			WHERE l.teacher_id = ? AND sa.student_id = ?

			ORDER BY completed_at DESC
		`

		rows, err := db.Query(query, sess.UserID, studentID, sess.UserID, studentID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "database error"})
			return
		}
		defer rows.Close()

		var attempts []StudentAttemptItem
		var totalScoreSum float64
		var bestScore float64

		for rows.Next() {
			var att StudentAttemptItem
			var ca time.Time
			att.StudentID = studentID
			att.StudentName = studentName
			att.StudentEmail = studentEmail

			if err := rows.Scan(
				&att.ID, &att.TestType, &att.TestID, &att.TestTitle,
				&att.Score, &att.Total, &att.Percent, &ca,
			); err != nil {
				continue
			}
			att.CompletedAt = ca.Format("02.01.2006 15:04")
			attempts = append(attempts, att)

			totalScoreSum += att.Percent
			if att.Percent > bestScore {
				bestScore = att.Percent
			}
		}

		if attempts == nil {
			attempts = []StudentAttemptItem{}
		}

		avgScore := 0.0
		if len(attempts) > 0 {
			avgScore = float64(int(totalScoreSum/float64(len(attempts))*10+0.5)) / 10.0
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"student": map[string]any{
				"id":         studentID,
				"username":   studentName,
				"email":      studentEmail,
				"created_at": studentCreatedAt.Format("02.01.2006"),
			},
			"stats": map[string]any{
				"attempts_count": len(attempts),
				"avg_percent":    avgScore,
				"best_score":     bestScore,
			},
			"attempts": attempts,
		})
	}
}


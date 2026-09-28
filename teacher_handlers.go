package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
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

func handleListLessons(db *sql.DB) http.HandlerFunc {
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

func handleCreateLesson(db *sql.DB) http.HandlerFunc {
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

		result, err := db.Exec(
			`INSERT INTO lessons (teacher_id, title, content, created_at) VALUES (?, ?, ?, ?)`,
			sess.UserID, req.Title, req.Content, time.Now().UTC(),
		)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "failed to save lesson"})
			return
		}
		id, _ := result.LastInsertId()
		writeJSON(w, http.StatusCreated, CreateLessonResponse{Message: "lesson created", LessonID: id})
	}
}

func handleSaveQuestions(db *sql.DB) http.HandlerFunc {
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

func RegisterTeacherRoutes(mux *http.ServeMux, db *sql.DB, sessions *SessionStore) {
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

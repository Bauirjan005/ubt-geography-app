package main

import "database/sql"

func migrateAll(db *sql.DB) error {
	migrations := []string{

		// ── users ─────────────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS users (
			id            INTEGER  PRIMARY KEY AUTOINCREMENT,
			username      TEXT     NOT NULL,
			email         TEXT     NOT NULL UNIQUE,
			password_hash TEXT     NOT NULL,
			role          TEXT     NOT NULL CHECK(role IN ('student', 'teacher')),
			created_at    DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_email ON users(email)`,

		// ── lessons ───────────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS lessons (
			id         INTEGER  PRIMARY KEY AUTOINCREMENT,
			teacher_id INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title      TEXT     NOT NULL,
			content    TEXT     NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_lessons_teacher ON lessons(teacher_id)`,

		// ── questions ─────────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS questions (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			lesson_id      INTEGER NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
			position       INTEGER NOT NULL DEFAULT 0,
			text           TEXT    NOT NULL,
			option_a       TEXT    NOT NULL,
			option_b       TEXT    NOT NULL,
			option_c       TEXT    NOT NULL,
			option_d       TEXT    NOT NULL,
			correct_option TEXT    NOT NULL CHECK(correct_option IN ('A','B','C','D'))
		)`,
		`CREATE INDEX IF NOT EXISTS idx_questions_lesson ON questions(lesson_id)`,

		// ── student_attempts ──────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS student_attempts (
			id           INTEGER  PRIMARY KEY AUTOINCREMENT,
			student_id   INTEGER  NOT NULL REFERENCES users(id)   ON DELETE CASCADE,
			lesson_id    INTEGER  NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
			score        INTEGER  NOT NULL DEFAULT 0,
			total        INTEGER  NOT NULL DEFAULT 0,
			completed_at DATETIME NOT NULL,
			UNIQUE(student_id, lesson_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_attempts_student ON student_attempts(student_id)`,

		// ── materials ─────────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS materials (
			id            INTEGER  PRIMARY KEY AUTOINCREMENT,
			teacher_id    INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title         TEXT     NOT NULL,
			filename      TEXT     NOT NULL,
			original_name TEXT     NOT NULL,
			file_type     TEXT     NOT NULL,
			file_size     INTEGER  NOT NULL DEFAULT 0,
			created_at    DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_materials_teacher ON materials(teacher_id)`,

		// ── material_views — one row per unique student per material ───────────
		`CREATE TABLE IF NOT EXISTS material_views (
			id          INTEGER  PRIMARY KEY AUTOINCREMENT,
			material_id INTEGER  NOT NULL REFERENCES materials(id) ON DELETE CASCADE,
			student_id  INTEGER  NOT NULL REFERENCES users(id)     ON DELETE CASCADE,
			viewed_at   DATETIME NOT NULL,
			UNIQUE(material_id, student_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_views_material ON material_views(material_id)`,

		// ── ai_quizzes ────────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS ai_quizzes (
			id          INTEGER  PRIMARY KEY AUTOINCREMENT,
			material_id INTEGER  NOT NULL REFERENCES materials(id) ON DELETE CASCADE,
			teacher_id  INTEGER  NOT NULL REFERENCES users(id)     ON DELETE CASCADE,
			title       TEXT     NOT NULL,
			difficulty  TEXT     NOT NULL CHECK(difficulty IN ('easy','medium','hard')),
			created_at  DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ai_quizzes_material ON ai_quizzes(material_id)`,

		// ── ai_questions ──────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS ai_questions (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			quiz_id        INTEGER NOT NULL REFERENCES ai_quizzes(id) ON DELETE CASCADE,
			position       INTEGER NOT NULL DEFAULT 0,
			text           TEXT    NOT NULL,
			option_a       TEXT    NOT NULL,
			option_b       TEXT    NOT NULL,
			option_c       TEXT    NOT NULL,
			option_d       TEXT    NOT NULL,
			correct_option TEXT    NOT NULL CHECK(correct_option IN ('A','B','C','D')),
			explanation    TEXT    NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ai_questions_quiz ON ai_questions(quiz_id)`,

		// ── ai_quiz_attempts ──────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS ai_quiz_attempts (
			id           INTEGER  PRIMARY KEY AUTOINCREMENT,
			student_id   INTEGER  NOT NULL REFERENCES users(id)       ON DELETE CASCADE,
			quiz_id      INTEGER  NOT NULL REFERENCES ai_quizzes(id)  ON DELETE CASCADE,
			score        INTEGER  NOT NULL DEFAULT 0,
			total        INTEGER  NOT NULL DEFAULT 0,
			completed_at DATETIME NOT NULL,
			UNIQUE(student_id, quiz_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ai_attempts_student ON ai_quiz_attempts(student_id)`,

		// Оқушы қандай сұрақтарды көргенін қадағалайды
		`CREATE TABLE IF NOT EXISTS student_seen_questions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    student_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    quiz_id     INTEGER NOT NULL REFERENCES ai_quizzes(id) ON DELETE CASCADE,
    question_id INTEGER NOT NULL REFERENCES ai_questions(id) ON DELETE CASCADE,
    seen_at     DATETIME NOT NULL,
    UNIQUE(student_id, question_id)
)`,
		`CREATE INDEX IF NOT EXISTS idx_seen_student ON student_seen_questions(student_id, quiz_id)`,

		// ── sessions ─────────────────────────────────────────────────────────
		`CREATE TABLE IF NOT EXISTS sessions (
			id         TEXT     PRIMARY KEY,
			user_id    INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			role       TEXT     NOT NULL,
			created_at DATETIME NOT NULL,
			expires_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at)`,
	}

	for _, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

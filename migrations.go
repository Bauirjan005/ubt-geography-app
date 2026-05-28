package main

import "database/sql"

func migrateAll(db *sql.DB) error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id            INTEGER  PRIMARY KEY AUTOINCREMENT,
			username      TEXT     NOT NULL,
			email         TEXT     NOT NULL UNIQUE,
			password_hash TEXT     NOT NULL,
			role          TEXT     NOT NULL CHECK(role IN ('student', 'teacher')),
			created_at    DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_users_email ON users(email)`,

		`CREATE TABLE IF NOT EXISTS lessons (
			id         INTEGER  PRIMARY KEY AUTOINCREMENT,
			teacher_id INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title      TEXT     NOT NULL,
			content    TEXT     NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_lessons_teacher ON lessons(teacher_id)`,

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

		`CREATE TABLE IF NOT EXISTS student_attempts (
			id           INTEGER  PRIMARY KEY AUTOINCREMENT,
			student_id   INTEGER  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			lesson_id    INTEGER  NOT NULL REFERENCES lessons(id) ON DELETE CASCADE,
			score        INTEGER  NOT NULL DEFAULT 0,
			total        INTEGER  NOT NULL DEFAULT 0,
			completed_at DATETIME NOT NULL,
			UNIQUE(student_id, lesson_id)
		)`,
	}

	for _, stmt := range migrations {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

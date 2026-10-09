package main

import (
	"database/sql"
	"strconv"
	"strings"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// DB wraps *sql.DB and provides dialect-aware query execution for SQLite and PostgreSQL.
type DB struct {
	*sql.DB
	isPostgres bool
}

// Tx wraps *sql.Tx and provides dialect-aware query execution within a transaction.
type Tx struct {
	*sql.Tx
	isPostgres bool
}

// adaptQuery rewrites query dialect differences between SQLite and PostgreSQL:
// 1. "INSERT OR IGNORE INTO ..." -> "INSERT INTO ... ON CONFLICT DO NOTHING"
// 2. Placeholder '?' -> '$1', '$2', '$3', etc.
func adaptQuery(query string, isPostgres bool) string {
	if !isPostgres {
		return query
	}

	trimmed := strings.TrimSpace(query)

	// Replace "INSERT OR IGNORE INTO" with "INSERT INTO ... ON CONFLICT DO NOTHING"
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "INSERT OR IGNORE INTO") {
		rest := trimmed[len("INSERT OR IGNORE INTO"):]
		trimmed = "INSERT INTO" + rest + " ON CONFLICT DO NOTHING"
	}

	// Convert '?' placeholders to '$1', '$2', ... outside of string literals
	var b strings.Builder
	b.Grow(len(trimmed) + 16)
	paramIdx := 1
	inString := false

	for i := 0; i < len(trimmed); i++ {
		ch := trimmed[i]
		if ch == '\'' {
			inString = !inString
			b.WriteByte(ch)
			continue
		}
		if ch == '?' && !inString {
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(paramIdx))
			paramIdx++
		} else {
			b.WriteByte(ch)
		}
	}

	return b.String()
}

// Exec executes a query after dialect adaptation.
func (db *DB) Exec(query string, args ...any) (sql.Result, error) {
	return db.DB.Exec(adaptQuery(query, db.isPostgres), args...)
}

// Query executes a query returning rows after dialect adaptation.
func (db *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return db.DB.Query(adaptQuery(query, db.isPostgres), args...)
}

// QueryRow executes a query returning a single row after dialect adaptation.
func (db *DB) QueryRow(query string, args ...any) *sql.Row {
	return db.DB.QueryRow(adaptQuery(query, db.isPostgres), args...)
}

// Begin starts a transaction and returns an adapted Tx.
func (db *DB) Begin() (*Tx, error) {
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx, isPostgres: db.isPostgres}, nil
}

// InsertReturningID executes an INSERT statement and returns the generated primary key ID.
// For PostgreSQL, it appends " RETURNING id" and reads the id via QueryRow.
// For SQLite, it executes the query and retrieves Result.LastInsertId().
func (db *DB) InsertReturningID(query string, args ...any) (int64, error) {
	if db.isPostgres {
		q := adaptQuery(query, true) + " RETURNING id"
		var id int64
		if err := db.DB.QueryRow(q, args...).Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	}
	res, err := db.DB.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Exec executes a query within a transaction after dialect adaptation.
func (tx *Tx) Exec(query string, args ...any) (sql.Result, error) {
	return tx.Tx.Exec(adaptQuery(query, tx.isPostgres), args...)
}

// Query executes a query within a transaction after dialect adaptation.
func (tx *Tx) Query(query string, args ...any) (*sql.Rows, error) {
	return tx.Tx.Query(adaptQuery(query, tx.isPostgres), args...)
}

// QueryRow executes a single-row query within a transaction after dialect adaptation.
func (tx *Tx) QueryRow(query string, args ...any) *sql.Row {
	return tx.Tx.QueryRow(adaptQuery(query, tx.isPostgres), args...)
}

// InsertReturningID executes an INSERT statement in a transaction and returns the generated ID.
func (tx *Tx) InsertReturningID(query string, args ...any) (int64, error) {
	if tx.isPostgres {
		q := adaptQuery(query, true) + " RETURNING id"
		var id int64
		if err := tx.Tx.QueryRow(q, args...).Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	}
	res, err := tx.Tx.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

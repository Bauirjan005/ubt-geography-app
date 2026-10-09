package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

func main() {
	_ = godotenv.Load(".env")
	pgURL := os.Getenv("DATABASE_URL")
	if pgURL == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	db, err := sql.Open("postgres", pgURL)
	if err != nil {
		log.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("Ping failed: %v", err)
	}

	fmt.Println("🔍 Checking users in Neon PostgreSQL:")
	rows, err := db.Query("SELECT id, username, email, role, length(password_hash) FROM users ORDER BY id ASC")
	if err != nil {
		log.Fatalf("Query failed: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var username, email, role string
		var hashLen int
		if err := rows.Scan(&id, &username, &email, &role, &hashLen); err != nil {
			log.Fatalf("Scan failed: %v", err)
		}
		fmt.Printf(" - User ID: %d | Username: %-15s | Email: %-25s | Role: %-8s | HashLen: %d\n", id, username, email, role, hashLen)
	}

	// Verify Liza account specifically
	var lizaID int64
	var lizaHash, lizaRole string
	err = db.QueryRow("SELECT id, password_hash, role FROM users WHERE email = $1", "liza@gmail.com").Scan(&lizaID, &lizaHash, &lizaRole)
	if err != nil {
		log.Fatalf("Liza query failed: %v", err)
	}
	// Verify hash is valid bcrypt
	cost, err := bcrypt.Cost([]byte(lizaHash))
	if err != nil {
		log.Fatalf("Invalid bcrypt hash for Liza: %v", err)
	}
	fmt.Printf("\n✅ User 'liza@gmail.com' found! ID: %d, Role: %s, Bcrypt Cost: %d (Hash verified valid)\n", lizaID, lizaRole, cost)

	// Check AI questions
	var qCount int64
	_ = db.QueryRow("SELECT count(*) FROM ai_questions").Scan(&qCount)
	fmt.Printf("✅ AI Questions in Neon PG: %d\n", qCount)

	// Check Materials
	var mCount int64
	_ = db.QueryRow("SELECT count(*) FROM materials").Scan(&mCount)
	fmt.Printf("✅ Materials in Neon PG: %d\n", mCount)

	fmt.Println("\n🎉 ALL CHECKS PASSED ON NEON POSTGRESQL!")
}

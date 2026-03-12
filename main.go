package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"

	_ "github.com/lib/pq"
)

type User struct {
	ID          int
	Name        string
	TargetHours float64
}

type Log struct {
	ID      int
	Date    string
	Hours   float64
	TimeIn  string
	TimeOut string
}

type DashboardData struct {
	ActiveUser     User
	AllUsers       []User
	RenderedHours  float64
	RemainingHours float64
	Logs           []Log
	CurrentPage    int
	NextPage       int
	PrevPage       int
}

var db *sql.DB

// migrateDatabase automatically adds missing columns to an existing database table.
func migrateDatabase(db *sql.DB) {
	var exists bool
	// Check if the time_in column already exists in the logs table
	query := `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns 
		WHERE table_name='logs' AND column_name='time_in'
	);`

	err := db.QueryRow(query).Scan(&exists)
	if err == nil && !exists {
		fmt.Println("Detected missing columns. Migrating database...")
		_, err := db.Exec(`ALTER TABLE logs 
			ADD COLUMN time_in TEXT DEFAULT '', 
			ADD COLUMN time_out TEXT DEFAULT ''`)
		if err != nil {
			log.Println("Migration Error:", err)
		} else {
			fmt.Println("Database migration successful: time_in and time_out added.")
		}
	}
}

func main() {
	var err error

	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		connStr = "postgres://postgres:password@localhost:5432/ojt_tracker?sslmode=disable"
	}

	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal("Error opening database:", err)
	}
	defer db.Close()

	// 1. Create Tables if they don't exist
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id SERIAL PRIMARY KEY, 
		name TEXT, 
		target REAL
	)`)
	if err != nil {
		log.Fatal("Error creating users table:", err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS logs (
		id SERIAL PRIMARY KEY, 
		user_id INTEGER, 
		date TEXT, 
		hours REAL,
		time_in TEXT DEFAULT '',
		time_out TEXT DEFAULT ''
	)`)
	if err != nil {
		log.Fatal("Error creating logs table:", err)
	}

	// 2. Run the migration to ensure existing tables get the new columns
	migrateDatabase(db)

	// 3. Initialize Default Users if empty
	var count int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count == 0 {
		names := []string{"Harold", "OJT Student 2", "OJT Student 3", "OJT Student 4", "OJT Student 5"}
		for _, name := range names {
			db.Exec("INSERT INTO users (name, target) VALUES ($1, $2)", name, 480.0)
		}
	}

	// 4. Routes
	http.HandleFunc("/", dashboardHandler)
	http.HandleFunc("/add", addLogHandler)
	http.HandleFunc("/delete", deleteLogHandler)
	http.HandleFunc("/update-target", updateTargetHandler)
	http.HandleFunc("/rename", renameUserHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Println("Multi-User OJT Server starting at http://localhost:" + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func renameUserHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		uID := r.FormValue("user_id")
		newName := r.FormValue("new_name")
		if newName != "" {
			_, err := db.Exec("UPDATE users SET name = $1 WHERE id = $2", newName, uID)
			if err != nil {
				log.Println("Rename error:", err)
			}
		}
		http.Redirect(w, r, "/?u="+uID, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	uParam := r.URL.Query().Get("u")
	userID, _ := strconv.Atoi(uParam)

	pParam := r.URL.Query().Get("p")
	currentPage, _ := strconv.Atoi(pParam)
	if currentPage < 1 {
		currentPage = 1
	}
	offset := (currentPage - 1) * 10

	rowsU, err := db.Query("SELECT id, name, target FROM users ORDER BY id ASC")
	if err != nil {
		http.Error(w, "Database error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rowsU.Close()

	var allUsers []User
	var activeUser User
	userFound := false

	for rowsU.Next() {
		var u User
		rowsU.Scan(&u.ID, &u.Name, &u.TargetHours)
		allUsers = append(allUsers, u)
		if u.ID == userID {
			activeUser = u
			userFound = true
		}
	}

	if !userFound && len(allUsers) > 0 {
		activeUser = allUsers[0]
		userID = activeUser.ID
	}

	// Calculate Total Rendered (using COALESCE to handle NULL/Empty cases)
	var totalRendered float64
	db.QueryRow("SELECT COALESCE(SUM(hours), 0) FROM logs WHERE user_id = $1", userID).Scan(&totalRendered)

	// Fetch Logs with Time Data
	rowsL, err := db.Query("SELECT id, date, hours, time_in, time_out FROM logs WHERE user_id = $1 ORDER BY date DESC LIMIT 10 OFFSET $2", userID, offset)
	if err != nil {
		http.Error(w, "Query error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rowsL.Close()

	var logs []Log
	for rowsL.Next() {
		var l Log
		if err := rowsL.Scan(&l.ID, &l.Date, &l.Hours, &l.TimeIn, &l.TimeOut); err != nil {
			continue
		}
		logs = append(logs, l)
	}

	data := DashboardData{
		ActiveUser:     activeUser,
		AllUsers:       allUsers,
		RenderedHours:  totalRendered,
		RemainingHours: activeUser.TargetHours - totalRendered,
		Logs:           logs,
		CurrentPage:    currentPage,
		NextPage:       currentPage + 1,
		PrevPage:       currentPage - 1,
	}

	tmpl, err := template.ParseFiles("index.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmpl.Execute(w, data)
}

func addLogHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		uID := r.FormValue("user_id")
		date := r.FormValue("date")

		// Force whole numbers for hours to match your UI changes
		rawHours, _ := strconv.ParseFloat(r.FormValue("hours"), 64)
		hours := float64(int(rawHours + 0.5))

		timeIn := r.FormValue("time_in")
		timeOut := r.FormValue("time_out")

		if date != "" && hours > 0 {
			_, err := db.Exec("INSERT INTO logs (user_id, date, hours, time_in, time_out) VALUES ($1, $2, $3, $4, $5)", uID, date, hours, timeIn, timeOut)
			if err != nil {
				log.Println("Insert error:", err)
			}
		}
		http.Redirect(w, r, "/?u="+uID, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func deleteLogHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	uID := r.URL.Query().Get("u")
	if id != "" {
		db.Exec("DELETE FROM logs WHERE id = $1", id)
	}
	http.Redirect(w, r, "/?u="+uID, http.StatusSeeOther)
}

func updateTargetHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		uID := r.FormValue("user_id")
		newTarget, _ := strconv.ParseFloat(r.FormValue("goal"), 64)
		if newTarget > 0 {
			db.Exec("UPDATE users SET target = $1 WHERE id = $2", newTarget, uID)
		}
		http.Redirect(w, r, "/?u="+uID, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

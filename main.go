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
	ID    int
	Date  string
	Hours float64
}

type DashboardData struct {
	ActiveUser     User
	AllUsers       []User
	RenderedHours  float64
	RemainingHours float64
	Logs           []Log
}

var db *sql.DB

func main() {
	var err error

	// Get the connection string from Render's environment variables
	connStr := os.Getenv("DATABASE_URL")
	if connStr == "" {
		// Fallback for local testing - make sure you have Postgres running locally
		connStr = "postgres://postgres:password@localhost:5432/ojt_tracker?sslmode=disable"
	}

	db, err = sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal("Error opening database:", err)
	}
	defer db.Close()

	// 1. Create Tables (Postgres Syntax)
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
		hours REAL
	)`)
	if err != nil {
		log.Fatal("Error creating logs table:", err)
	}

	// 2. Initialize 5 Default Users if empty
	var count int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if count == 0 {
		names := []string{"Harold", "OJT Student 2", "OJT Student 3", "OJT Student 4", "OJT Student 5"}
		for _, name := range names {
			db.Exec("INSERT INTO users (name, target) VALUES ($1, $2)", name, 480.0)
		}
	}

	// 3. Routes
	http.HandleFunc("/", dashboardHandler)
	http.HandleFunc("/add", addLogHandler)
	http.HandleFunc("/delete", deleteLogHandler)
	http.HandleFunc("/update-target", updateTargetHandler)
	http.HandleFunc("/rename", renameUserHandler)

	// Use PORT environment variable for Render
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

	// Fetch all users to find the first valid ID if none provided
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

	// Fetch logs for the specific active user (Postgres uses $1)
	rowsL, err := db.Query("SELECT id, date, hours FROM logs WHERE user_id = $1 ORDER BY date DESC", userID)
	if err != nil {
		http.Error(w, "Query error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer rowsL.Close()

	var logs []Log
	var totalRendered float64
	for rowsL.Next() {
		var l Log
		if err := rowsL.Scan(&l.ID, &l.Date, &l.Hours); err != nil {
			continue
		}
		logs = append(logs, l)
		totalRendered += l.Hours
	}

	data := DashboardData{
		ActiveUser:     activeUser,
		AllUsers:       allUsers,
		RenderedHours:  totalRendered,
		RemainingHours: activeUser.TargetHours - totalRendered,
		Logs:           logs,
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
		hours, _ := strconv.ParseFloat(r.FormValue("hours"), 64)

		if date != "" && hours > 0 {
			_, err := db.Exec("INSERT INTO logs (user_id, date, hours) VALUES ($1, $2, $3)", uID, date, hours)
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

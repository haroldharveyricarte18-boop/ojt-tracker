package main

import (
	"bytes"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/sessions"

	_ "github.com/lib/pq"
)

type User struct {
	ID          int
	Name        string
	Username    string // NEW: For logging in
	Password    string // NEW: To protect the account
	TargetHours float64
	Notes       string
	NotePass    string
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
	Announcement   string
}

var db *sql.DB
var userHistories = make(map[string][]map[string]string)

// Replace your store definition with this:
var store = sessions.NewFilesystemStore("./sessions", []byte("your-secret-key"))

func init() {
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   60 * 60 * 24 * 7, // 7 Days (Persistent)
		HttpOnly: true,             // Prevents JavaScript access (Security)
		Secure:   false,            // Set to true if using HTTPS
	}
}

func migrateDatabase(db *sql.DB) {
	// 1. Migrate Logs Table (Time In/Out)
	var logsExist bool
	db.QueryRow("SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='logs' AND column_name='time_in')").Scan(&logsExist)
	if !logsExist {
		db.Exec("ALTER TABLE logs ADD COLUMN time_in TEXT DEFAULT '', ADD COLUMN time_out TEXT DEFAULT ''")
	}

	// 2. Migrate Users Table (Notes and Password)
	var notesExist bool
	db.QueryRow("SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='users' AND column_name='notes')").Scan(&notesExist)
	if !notesExist {
		fmt.Println("Migrating Users table for Notepad...")
		db.Exec("ALTER TABLE users ADD COLUMN notes TEXT DEFAULT '', ADD COLUMN note_pass TEXT DEFAULT ''")
	}
	// 3. Migrate Users Table for Login System
	var loginExist bool
	db.QueryRow("SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='users' AND column_name='username')").Scan(&loginExist)
	if !loginExist {
		fmt.Println("Migrating Users table for Login/Register...")
		// We allow NULL at first so Erika/Harvz data doesn't break
		db.Exec("ALTER TABLE users ADD COLUMN username TEXT UNIQUE, ADD COLUMN password TEXT")
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
	// Create Announcements Table
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS announcements (
        id SERIAL PRIMARY KEY, 
        content TEXT, 
        updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
    )`)
	if err != nil {
		log.Fatal("Error creating announcements table:", err)
	}

	// Insert default message if table is empty
	var annCount int
	db.QueryRow("SELECT COUNT(*) FROM announcements").Scan(&annCount)
	if annCount == 0 {
		db.Exec("INSERT INTO announcements (content) VALUES ($1)", "Welcome to the OJT Tracker! Stay productive!")
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
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// 4. Routes
	http.HandleFunc("/", dashboardHandler)
	http.HandleFunc("/add", addLogHandler)
	http.HandleFunc("/delete", deleteLogHandler)
	http.HandleFunc("/update-target", updateTargetHandler)
	http.HandleFunc("/rename", renameUserHandler)
	http.HandleFunc("/setup-note-pass", setupNotePassHandler)
	http.HandleFunc("/save-notes", saveNotesHandler)
	http.HandleFunc("/verify-note-pass", verifyNotePassHandler)
	http.HandleFunc("/export", exportHandler)
	http.HandleFunc("/ai-chat", aiChatHandler)
	http.HandleFunc("/delete-by-date", deleteByDateHandler(db))
	// New Login System Routes
	http.HandleFunc("/login", loginPageHandler)       // Shows the pink login.html
	http.HandleFunc("/register", registerPageHandler) // Shows the pink register.html
	http.HandleFunc("/auth-login", authLoginHandler)  // Logic to check password
	http.HandleFunc("/auth-reg", authRegHandler)      // Logic to save new account
	http.HandleFunc("/logout", logoutHandler)         // To sign out
	http.HandleFunc("/admin", adminHandler)
	http.HandleFunc("/admin/reset-password", adminResetPasswordHandler)
	http.HandleFunc("/admin/update-announcement", updateAnnouncementHandler)
	http.HandleFunc("/delete-announcement", deleteAnnouncementHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Println("Multi-User OJT Server starting at http://localhost:" + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// Updated: Removed the default "Welcome" text so "Clear" actually works
func updateAnnouncementHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	content := r.FormValue("content")

	// If content is empty here, it will save as "" in the DB.
	// This ensures {{if .Announcement}} in your HTML will evaluate to false.
	_, err := db.Exec("UPDATE announcements SET content = $1, updated_at = CURRENT_TIMESTAMP WHERE id = 1", content)
	if err != nil {
		log.Printf("Error updating announcement: %v", err)
		http.Error(w, "Failed to update", 500)
		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// New: Dedicated function to quickly clear the broadcast
func deleteAnnouncementHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	// We set content to an empty string to "hide" the broadcast on the dashboard
	_, err := db.Exec("UPDATE announcements SET content = '', updated_at = CURRENT_TIMESTAMP WHERE id = 1")
	if err != nil {
		log.Printf("Error deleting announcement: %v", err)
		http.Error(w, "Failed to delete", 500)
		return
	}

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func adminResetPasswordHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")
	uIDStr, _ := session.Values["user_id"].(string)
	userID, _ := strconv.Atoi(uIDStr)

	// Security: Only Erika (1) or Harvey (4)
	if userID != 1 && userID != 4 {
		http.Error(w, "Unauthorized", http.StatusForbidden)
		return
	}

	if r.Method == http.MethodPost {
		targetID := r.FormValue("target_user_id")
		newPass := r.FormValue("new_password")

		_, err := db.Exec("UPDATE users SET password = $1 WHERE id = $2", newPass, targetID)
		if err != nil {
			http.Error(w, "Failed to reset password", 500)
			return
		}
		// Redirect back to admin with a success message
		http.Redirect(w, r, "/admin?reset_success=1", http.StatusSeeOther)
	}
}

func adminHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")

	// 1. Security Check: Only Erika (1) and Harvey (4)
	uIDStr, ok := session.Values["user_id"].(string)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	userID, _ := strconv.Atoi(uIDStr)
	if userID != 1 && userID != 4 {
		http.Error(w, "🛡️ Access Denied: Admins Only!", http.StatusForbidden)
		return
	}

	// 2. Fetch all students and their total hours
	rows, err := db.Query(`
		SELECT u.id, u.name, u.target, COALESCE(SUM(l.hours), 0) as rendered
		FROM users u 
		LEFT JOIN logs l ON u.id = l.user_id 
		GROUP BY u.id 
		ORDER BY rendered DESC`)

	if err != nil {
		log.Println("Admin Query Error:", err)
		http.Error(w, "Database error", 500)
		return
	}
	defer rows.Close()

	type AdminUser struct {
		ID       int
		Name     string
		Target   float64
		Rendered float64
	}

	var students []AdminUser
	for rows.Next() {
		var s AdminUser
		rows.Scan(&s.ID, &s.Name, &s.Target, &s.Rendered)
		students = append(students, s)
	}

	// 3. FETCH THE CURRENT ANNOUNCEMENT
	var currentAnn string
	// We pull the message with ID 1 from the table we created in Step 1
	err = db.QueryRow("SELECT content FROM announcements WHERE id = 1").Scan(&currentAnn)
	if err != nil {
		currentAnn = "Welcome to the OJT Tracker!" // Fallback
	}

	// 4. PREPARE DATA WRAPPER
	// We wrap both Students and the Announcement into one object
	data := struct {
		Students     []AdminUser
		Announcement string
	}{
		Students:     students,
		Announcement: currentAnn,
	}

	// 5. Load and Execute the Admin Template
	tmpl, err := template.ParseFiles("admin.html")
	if err != nil {
		http.Error(w, "Template error: "+err.Error(), 500)
		return
	}

	// We now execute with 'data' instead of just 'students'
	tmpl.Execute(w, data)
}

func deleteByDateHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		date := r.URL.Query().Get("date")
		userID := r.URL.Query().Get("u")

		// DEBUG: See what is actually arriving in your terminal
		log.Printf("Attempting delete - Date: '%s', UserID: '%s'", date, userID)

		if date == "" || userID == "" {
			http.Error(w, "Missing date or user ID", 400)
			return
		}

		// IMPORTANT: PostgreSQL uses $1, $2 instead of ?
		// Also, make sure your table name is 'logs' and columns are 'date' and 'user_id'
		query := `DELETE FROM logs WHERE date = $1 AND user_id = $2`

		_, err := db.Exec(query, date, userID)
		if err != nil {
			log.Println("DATABASE ERROR:", err)
			http.Error(w, "Database error", 500)
			return
		}

		log.Println("Successfully deleted log for", date)
		w.WriteHeader(http.StatusOK)
	}
}

func aiChatHandler(w http.ResponseWriter, r *http.Request) {
	// 1. Get query, name, and current progress from the URL
	query := r.URL.Query().Get("msg")
	userName := r.URL.Query().Get("name")
	renderedHours := r.URL.Query().Get("hours")

	if userName == "" {
		userName = "Guest"
	}

	// Check if API Key exists
	apiKey := os.Getenv("GROQ_API_KEY")
	if apiKey == "" {
		log.Println("CRITICAL ERROR: GROQ_API_KEY is not set in environment variables.")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"answer": "System Error: API Key missing. Please check server settings."})
		return
	}

	// 2. THE BRAIN UPGRADE: The System Prompt
	if _, exists := userHistories[userName]; !exists {
		today := time.Now().Format("2006-01-02")
		userHistories[userName] = []map[string]string{
			{
				"role": "system",
				"content": "You are HarveyAI, a smart OJT assistant for " + userName + ". " +
					"CURRENT DATE: " + today + ". " +
					"PROGRESS: " + renderedHours + " hours done. " +
					"STRICT RULES:\n" +
					"1. For normal greetings, respond warmly without COMMANDs.\n" +
					"2. ACTION - ADD LOG: If asked to record hours, append: COMMAND:{\"action\": \"add_log\", \"hours\": 0, \"date\": \"" + today + "\", \"time_in\": \"\", \"time_out\": \"\"}\n" +
					"3. ACTION - DELETE LOG: If asked to delete, append: COMMAND:{\"action\": \"delete_log\", \"date\": \"YYYY-MM-DD\"}\n" +
					"4. Be concise and professional.",
			},
		}
	}

	// 3. Add user message
	userHistories[userName] = append(userHistories[userName], map[string]string{
		"role":    "user",
		"content": query,
	})

	// 4. Send to Groq
	requestBody, _ := json.Marshal(map[string]interface{}{
		"model":    "llama-3.3-70b-versatile",
		"messages": userHistories[userName],
	})

	req, _ := http.NewRequest("POST", "https://api.groq.com/openai/v1/chat/completions", bytes.NewBuffer(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 15 * time.Second} // Added timeout for safety
	resp, err := client.Do(req)
	if err != nil {
		log.Println("Network/Request Error:", err)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"answer": "I'm having trouble connecting to the network."})
		return
	}
	defer resp.Body.Close()

	// 5. NEW: Status Check for API Errors (Expired Key, Rate Limit, etc.)
	if resp.StatusCode != http.StatusOK {
		var errorDetail map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errorDetail)
		log.Printf("GROQ API ERROR: Status %d - Details: %v", resp.StatusCode, errorDetail)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"answer": "I'm sorry, my AI service is currently unavailable (Status " + fmt.Sprint(resp.StatusCode) + ")."})
		return
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Println("JSON Decoding Error:", err)
	}

	// 6. Save and Return
	reply := "I'm sorry, I'm resting right now."
	if len(result.Choices) > 0 {
		reply = result.Choices[0].Message.Content
		userHistories[userName] = append(userHistories[userName], map[string]string{
			"role":    "assistant",
			"content": reply,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"answer": reply})
}

func exportHandler(w http.ResponseWriter, r *http.Request) {
	uID := r.URL.Query().Get("u")
	if uID == "" {
		http.Error(w, "User ID required", http.StatusBadRequest)
		return
	}

	// 1. Set the headers to tell the browser this is a downloadable CSV
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment;filename=OJT_Logs_User_%s.csv", uID))

	// 2. Initialize the CSV writer
	writer := csv.NewWriter(w)
	defer writer.Flush()

	// 3. Write the Header Row
	writer.Write([]string{"Date", "Hours", "Time In", "Time Out"})

	// 4. Fetch ALL logs for this user from the database
	rows, err := db.Query("SELECT date, hours, time_in, time_out FROM logs WHERE user_id = $1 ORDER BY date DESC", uID)
	if err != nil {
		log.Println("Export Query Error:", err)
		return
	}
	defer rows.Close()

	// 5. Loop through database results and write to CSV
	for rows.Next() {
		var l Log
		if err := rows.Scan(&l.Date, &l.Hours, &l.TimeIn, &l.TimeOut); err != nil {
			continue
		}

		row := []string{
			l.Date,
			fmt.Sprintf("%.1f", l.Hours),
			l.TimeIn,
			l.TimeOut,
		}
		writer.Write(row)
	}
}

func verifyNotePassHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		uID := r.FormValue("user_id")
		inputPass := r.FormValue("password")

		var dbPass string
		err := db.QueryRow("SELECT note_pass FROM users WHERE id = $1", uID).Scan(&dbPass)

		if err == nil && inputPass == dbPass {
			w.WriteHeader(http.StatusOK) // Send 200 OK if match
			return
		}
	}
	w.WriteHeader(http.StatusUnauthorized) // Send 401 if wrong
}

func setupNotePassHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		uID := r.FormValue("user_id")
		pass := r.FormValue("password")
		if pass != "" {
			db.Exec("UPDATE users SET note_pass = $1 WHERE id = $2", pass, uID)
		}
		http.Redirect(w, r, "/?u="+uID, http.StatusSeeOther)
	}
}

func saveNotesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		uID := r.FormValue("user_id")
		notes := r.FormValue("notes")
		db.Exec("UPDATE users SET notes = $1 WHERE id = $2", notes, uID)
		http.Redirect(w, r, "/?u="+uID, http.StatusSeeOther)
	}
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
	// 1. GET THE SESSION
	session, _ := store.Get(r, "session-name")

	// 2. CHECK IF LOGGED IN
	auth, ok := session.Values["authenticated"].(bool)
	if !ok || !auth {
		// If not logged in, send them to the login page
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// 3. GET USER ID FROM SESSION (Instead of URL)
	sessionUserIDStr, _ := session.Values["user_id"].(string)
	userID, _ := strconv.Atoi(sessionUserIDStr)

	// --- PAGINATION LOGIC ---
	pParam := r.URL.Query().Get("p")
	currentPage, _ := strconv.Atoi(pParam)
	if currentPage < 1 {
		currentPage = 1
	}
	offset := (currentPage - 1) * 10

	// 4. FETCH THE LOGGED-IN USER DETAILS
	var activeUser User
	err := db.QueryRow("SELECT id, name, target, notes, note_pass FROM users WHERE id = $1", userID).
		Scan(&activeUser.ID, &activeUser.Name, &activeUser.TargetHours, &activeUser.Notes, &activeUser.NotePass)

	if err != nil {
		// If user doesn't exist anymore, clear session and boot them
		session.Options.MaxAge = -1
		session.Save(r, w)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// 5. FETCH TOTAL HOURS
	var totalRendered float64
	db.QueryRow("SELECT COALESCE(SUM(hours), 0) FROM logs WHERE user_id = $1", userID).Scan(&totalRendered)

	// 6. FETCH LOGS (ONLY FOR THIS USER)
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

	// 7. FETCH THE LATEST ANNOUNCEMENT (New part!)
	var currentAnnouncement string
	err = db.QueryRow("SELECT content FROM announcements WHERE id = 1").Scan(&currentAnnouncement)
	if err != nil {
		// Fallback if the query fails or table is empty
		currentAnnouncement = "Welcome to the OJT Tracker!"
	}

	// 8. PREPARE DATA FOR TEMPLATE
	data := DashboardData{
		ActiveUser:     activeUser,
		RenderedHours:  totalRendered,
		RemainingHours: activeUser.TargetHours - totalRendered,
		Logs:           logs,
		CurrentPage:    currentPage,
		NextPage:       currentPage + 1,
		PrevPage:       currentPage - 1,
		Announcement:   currentAnnouncement, // Now index.html can see this!
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

	if id != "" && uID != "" {
		// FIX: Use $1 and $2 for PostgreSQL
		_, err := db.Exec("DELETE FROM logs WHERE id = $1 AND user_id = $2", id, uID)
		if err != nil {
			log.Println("Error deleting record:", err)
		}
	}

	// Double check if your main route is "/" or "/dashboard"
	// If you are using the pink dashboard, it's usually "/dashboard"
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

func loginPageHandler(w http.ResponseWriter, r *http.Request) {
	// 1. Get the session
	session, _ := store.Get(r, "session-name")

	// 2. Retrieve the "error" flashes
	// This returns an []interface{} containing any messages we saved
	flashes := session.Flashes("error")

	// 3. IMPORTANT: You must save the session after reading flashes.
	// This tells the session to "delete" them so they don't show up again on refresh.
	session.Save(r, w)

	tmpl, _ := template.ParseFiles("login.html")

	// 4. Pass the flashes slice to the template
	tmpl.Execute(w, flashes)
}

func registerPageHandler(w http.ResponseWriter, r *http.Request) {
	// Fetch existing names (Erika, Harvz, etc.) so they can "claim" them
	rows, _ := db.Query("SELECT id, name FROM users WHERE username IS NULL OR username = ''")
	var users []User
	for rows.Next() {
		var u User
		rows.Scan(&u.ID, &u.Name)
		users = append(users, u)
	}
	tmpl, _ := template.ParseFiles("register.html")
	tmpl.Execute(w, users)
}

func authRegHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		uID := r.FormValue("user_id") // This will be "new" or a number like "1"
		username := r.FormValue("username")
		password := r.FormValue("password")
		newName := r.FormValue("new_name") // From the new input field we added

		if uID == "new" {
			// 1. Logic for a BRAND NEW user
			if newName == "" {
				http.Redirect(w, r, "/register?error=empty_name", http.StatusSeeOther)
				return
			}

			// Insert a fresh record into the database
			_, err := db.Exec("INSERT INTO users (name, username, password, target) VALUES ($1, $2, $3, $4)",
				newName, username, password, 480.0) // 480 is the default OJT target

			if err != nil {
				// Likely means the username is already taken
				http.Redirect(w, r, "/register?error=taken", http.StatusSeeOther)
				return
			}
		} else {
			// 2. Logic for CLAIMING an existing name (Erika, Harvz, etc.)
			_, err := db.Exec("UPDATE users SET username = $1, password = $2 WHERE id = $3", username, password, uID)
			if err != nil {
				http.Redirect(w, r, "/register?error=taken", http.StatusSeeOther)
				return
			}
		}

		// Success! Send them to login
		http.Redirect(w, r, "/login?success=1", http.StatusSeeOther)
	}
}

func authLoginHandler(w http.ResponseWriter, r *http.Request) {
	// 1. Only allow POST requests
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// 2. Parse the form data
	err := r.ParseForm()
	if err != nil {
		http.Error(w, "Error parsing form", http.StatusBadRequest)
		return
	}

	user := r.FormValue("username")
	pass := r.FormValue("password")

	var dbID int
	var dbPass string
	// Get user from DB
	err = db.QueryRow("SELECT id, password FROM users WHERE username = $1", user).Scan(&dbID, &dbPass)

	if err == nil && pass == dbPass {
		// 3. Get the session
		session, _ := store.Get(r, "session-name")

		// 4. Set the values
		session.Values["authenticated"] = true
		session.Values["user_id"] = strconv.Itoa(dbID)

		// 5. IMPORTANT: Save the session and check for errors
		err = session.Save(r, w)
		if err != nil {
			log.Println("Error saving session:", err)
			http.Error(w, "Internal Server Error: Could not save session", http.StatusInternalServerError)
			return
		}

		// 6. Success! Redirect to dashboard
		http.Redirect(w, r, "/", http.StatusSeeOther)
	} else {
		// --- FLASH MESSAGE LOGIC ---
		log.Println("Login failed for user:", user)

		// Get the session to store the flash message
		session, _ := store.Get(r, "session-name")

		// Add an "error" flash message
		session.AddFlash("Invalid username or password. Please try again!", "error")

		// Save the session so the flash is stored
		session.Save(r, w)

		// Redirect back to login; the handler for /login will now see this message
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "session-name")

	// Set MaxAge to -1 to tell the browser to delete the cookie immediately
	session.Options.MaxAge = -1
	session.Save(r, w)

	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

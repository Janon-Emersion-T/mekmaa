package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const (
	sessionCookieName = "mekmaa3_session"
	csrfCookieName    = "mekmaa3_csrf"
	flashCookieName   = "mekmaa3_flash"
	sessionTTL        = 24 * time.Hour
	otpTTL            = 10 * time.Minute
)

var (
	emailPattern    = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	passwordPattern = regexp.MustCompile(`^.{10,}$`)
	otpPattern      = regexp.MustCompile(`^\d{6}$`)
	allRoles        = []string{"superadmin", "admin", "editor", "customer"}
	allPermissions  = []string{"dashboard.view", "editor.access", "users.manage", "roles.manage", "admissions.manage"}
)

type contextKey string

const userContextKey contextKey = "currentUser"

type App struct {
	db           *sql.DB
	templates    map[string]*template.Template
	cookieSecure bool
	smtp         SMTPConfig
}

type SMTPConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
	Enabled  bool
}

type User struct {
	ID          int64
	Email       string
	Name        string
	Roles       []string
	Permissions []string
	Verified    bool
	CreatedAt   time.Time
}

type Role struct {
	ID          int64
	Name        string
	Permissions []string
	System      bool
}

type Admission struct {
	ID                       int64
	FullName                 string
	DateOfBirth              string
	Gender                   string
	Address                  string
	PassportNumber           string
	School                   string
	GuardianName             string
	GuardianRelationship     string
	GuardianContactNumber    string
	GuardianAlternativePhone string
	MedicalInformation       string
	CreatedAt                time.Time
}

type TemplateData struct {
	Title             string
	Description       string
	CurrentPath       string
	User              *User
	HideChrome        bool
	CSRFToken         string
	Flash             string
	Error             string
	Stats             []Stat
	Features          []Feature
	Users             []User
	Available         []string
	Roles             []Role
	Permissions       []string
	Admissions        []Admission
	SelectedAdmission *Admission
	AdmissionMode     string
	PendingEmail      string
	OTPCodeLength     int
	ResendAction      string
}

type Stat struct {
	Label string
	Value string
}

type Feature struct {
	Title string
	Body  string
}

var (
	ErrEmailTaken = errors.New("email already exists")
	ErrInvalidOTP = errors.New("invalid verification code")
)

func main() {
	if err := loadDotEnv(".env"); err != nil {
		log.Printf("load .env: %v", err)
	}

	addr := envOrDefault("ADDR", ":8080")
	dbPath := envOrDefault("DB_PATH", "app.db")
	cookieSecure := os.Getenv("COOKIE_SECURE") == "true"

	smtpConfig := SMTPConfig{
		Host:     envOrDefault("SMTP_HOST", "smtp.gmail.com"),
		Port:     envOrDefault("SMTP_PORT", "587"),
		Username: strings.TrimSpace(os.Getenv("SMTP_USER")),
		Password: os.Getenv("SMTP_PASS"),
		From:     strings.TrimSpace(os.Getenv("SMTP_FROM")),
	}
	if smtpConfig.From == "" {
		smtpConfig.From = smtpConfig.Username
	}
	smtpConfig.Enabled = smtpConfig.Username != "" && smtpConfig.Password != "" && smtpConfig.From != ""
	log.Printf("smtp enabled=%t host=%s port=%s from=%s", smtpConfig.Enabled, smtpConfig.Host, smtpConfig.Port, smtpConfig.From)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := runMigrations(db); err != nil {
		log.Fatalf("run migrations: %v", err)
	}
	if err := seedRoles(db); err != nil {
		log.Fatalf("seed roles: %v", err)
	}
	if err := bootstrapSuperadmin(db); err != nil {
		log.Fatalf("bootstrap superadmin: %v", err)
	}

	templates, err := buildTemplates()
	if err != nil {
		log.Fatalf("parse templates: %v", err)
	}

	app := &App{
		db:           db,
		templates:    templates,
		cookieSecure: cookieSecure,
		smtp:         smtpConfig,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.homeHandler)
	mux.HandleFunc("/register", app.registerHandler)
	mux.HandleFunc("/login", app.loginHandler)
	mux.HandleFunc("/verify-email", app.verifyEmailHandler)
	mux.HandleFunc("/verify-email/resend", app.resendVerificationHandler)
	mux.HandleFunc("/logout", app.logoutHandler)
	mux.Handle("/dashboard", app.sessionMiddleware(http.HandlerFunc(app.dashboardHandler)))
	mux.Handle("/editor", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.editorHandler), "editor", "admin", "superadmin")))
	mux.Handle("/admin", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.adminRedirectHandler), "admin", "superadmin")))
	mux.Handle("/admin/users", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.userManagementHandler), "admin", "superadmin")))
	mux.Handle("/admin/roles", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.roleManagementHandler), "admin", "superadmin")))
	mux.Handle("/admin/users/create", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.createManagedUserHandler), "admin", "superadmin")))
	mux.Handle("/admin/users/roles", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.updateRolesHandler), "admin", "superadmin")))
	mux.Handle("/admin/roles/create", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.createRoleHandler), "admin", "superadmin")))
	mux.Handle("/admin/roles/update", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.updateRoleHandler), "admin", "superadmin")))
	mux.Handle("/admin/roles/delete", app.sessionMiddleware(app.requireRoles(http.HandlerFunc(app.deleteRoleHandler), "admin", "superadmin")))
	mux.Handle("/admin/admissions", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.admissionManagementHandler), "admissions.manage")))
	mux.Handle("/admin/admissions/create", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.createAdmissionHandler), "admissions.manage")))
	mux.Handle("/admin/admissions/update", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.updateAdmissionHandler), "admissions.manage")))
	mux.Handle("/admin/admissions/delete", app.sessionMiddleware(app.requirePermission(http.HandlerFunc(app.deleteAdmissionHandler), "admissions.manage")))

	log.Printf("server listening on %s", addr)
	if err := http.ListenAndServe(addr, app.securityHeaders(mux)); err != nil {
		log.Fatal(err)
	}
}

func (a *App) homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	user := a.optionalUser(r)
	data := a.newTemplateData(w, r, user)
	data.Title = "Go Auth System with RBAC"
	data.Description = "Production-oriented starter with secure sessions, role checks, and an admin workflow."
	data.Stats = []Stat{
		{Value: "4", Label: "Built-in roles"},
		{Value: "6", Label: "Digit email OTP"},
		{Value: "24h", Label: "Session lifetime"},
	}
	data.Features = []Feature{
		{Title: "Credential security", Body: "Passwords are hashed with bcrypt and sessions are stored server-side as SHA-256 token hashes."},
		{Title: "Email verification", Body: "New accounts receive a 6-digit verification code before they can create a session."},
		{Title: "Role-based access control", Body: "Customer, editor, admin, and superadmin roles gate separate areas of the application with dedicated middleware."},
	}

	a.render(w, "home", data, http.StatusOK)
}

func (a *App) registerHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		user := a.optionalUser(r)
		if user != nil {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}

		data := a.newTemplateData(w, r, user)
		data.Title = "Create account"
		data.Description = "Register a new account."
		data.HideChrome = true
		a.render(w, "register", data, http.StatusOK)
	case http.MethodPost:
		if err := a.verifyCSRF(r); err != nil {
			a.writeFormError(w, r, "register", "Create account", "Your session token is invalid. Refresh and try again.", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			a.writeFormError(w, r, "register", "Create account", "Invalid form submission.", http.StatusBadRequest)
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		password := r.FormValue("password")
		passwordConfirm := r.FormValue("password_confirm")

		if name == "" || !emailPattern.MatchString(email) || !passwordPattern.MatchString(password) {
			a.writeFormError(w, r, "register", "Create account", "Use a valid email and a password with at least 10 characters.", http.StatusBadRequest)
			return
		}
		if password != passwordConfirm {
			a.writeFormError(w, r, "register", "Create account", "Passwords do not match.", http.StatusBadRequest)
			return
		}

		user, err := a.createUser(name, email, password)
		if err != nil {
			if errors.Is(err, ErrEmailTaken) {
				a.writeFormError(w, r, "register", "Create account", "That email is already registered.", http.StatusConflict)
				return
			}
			log.Printf("create user: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		otp, err := a.issueVerificationCode(user.ID)
		if err != nil {
			log.Printf("issue verification code: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := a.sendVerificationEmail(user, otp); err != nil {
			log.Printf("send verification email: %v", err)
			a.setFlash(w, "Account created, but the verification email could not be sent automatically. Configure SMTP, then resend the code on the next screen.")
		} else {
			a.setFlash(w, "Account created. Enter the 6-digit code we sent to your email.")
		}
		http.Redirect(w, r, "/verify-email?email="+url.QueryEscape(user.Email), http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) loginHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		user := a.optionalUser(r)
		if user != nil {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}

		data := a.newTemplateData(w, r, user)
		data.Title = "Sign in"
		data.Description = "Access your account."
		data.HideChrome = true
		a.render(w, "login", data, http.StatusOK)
	case http.MethodPost:
		if err := a.verifyCSRF(r); err != nil {
			a.writeFormError(w, r, "login", "Sign in", "Your session token is invalid. Refresh and try again.", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			a.writeFormError(w, r, "login", "Sign in", "Invalid form submission.", http.StatusBadRequest)
			return
		}

		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		password := r.FormValue("password")

		user, passwordHash, err := a.findUserByEmail(email)
		if err != nil || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil {
			a.writeFormError(w, r, "login", "Sign in", "Invalid email or password.", http.StatusUnauthorized)
			return
		}

		if !user.Verified {
			otp, err := a.issueVerificationCode(user.ID)
			if err != nil {
				log.Printf("issue verification code: %v", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if err := a.sendVerificationEmail(user, otp); err != nil {
				log.Printf("send verification email: %v", err)
				a.setFlash(w, "Your account is not verified. Configure SMTP if needed, then resend the code on the next screen.")
			} else {
				a.setFlash(w, "Your account is not verified. Enter the 6-digit code we sent to your email.")
			}
			http.Redirect(w, r, "/verify-email?email="+url.QueryEscape(user.Email), http.StatusSeeOther)
			return
		}

		if err := a.deleteSessionsForUser(user.ID); err != nil {
			log.Printf("delete old sessions: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := a.createSession(w, user.ID); err != nil {
			log.Printf("create session: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		a.setFlash(w, "Signed in successfully.")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) verifyEmailHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if user := a.optionalUser(r); user != nil && user.Verified {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
			return
		}

		data := a.newTemplateData(w, r, nil)
		data.Title = "Verify your email"
		data.Description = "Confirm your email with a 6-digit code."
		data.HideChrome = true
		data.PendingEmail = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("email")))
		data.ResendAction = "/verify-email/resend"
		a.render(w, "verify-email", data, http.StatusOK)
	case http.MethodPost:
		if err := a.verifyCSRF(r); err != nil {
			a.writeVerificationError(w, r, "", "Your session token is invalid. Refresh and try again.", http.StatusForbidden)
			return
		}
		if err := r.ParseForm(); err != nil {
			a.writeVerificationError(w, r, "", "Invalid form submission.", http.StatusBadRequest)
			return
		}

		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		otp := strings.TrimSpace(r.FormValue("otp"))
		if !emailPattern.MatchString(email) || !otpPattern.MatchString(otp) {
			a.writeVerificationError(w, r, email, "Enter the 6-digit verification code.", http.StatusBadRequest)
			return
		}

		user, _, err := a.findUserByEmail(email)
		if err != nil {
			a.writeVerificationError(w, r, email, "Invalid verification attempt.", http.StatusBadRequest)
			return
		}
		if user.Verified {
			a.setFlash(w, "Your email is already verified. Sign in to continue.")
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		if err := a.consumeVerificationCode(user.ID, otp); err != nil {
			if errors.Is(err, ErrInvalidOTP) {
				a.writeVerificationError(w, r, email, "The verification code is invalid or expired.", http.StatusBadRequest)
				return
			}
			log.Printf("consume verification code: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		if err := a.deleteSessionsForUser(user.ID); err != nil {
			log.Printf("delete old sessions: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if err := a.createSession(w, user.ID); err != nil {
			log.Printf("create session: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}

		a.setFlash(w, "Email verified. You are now signed in.")
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) resendVerificationHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	if emailPattern.MatchString(email) {
		if user, _, err := a.findUserByEmail(email); err == nil && !user.Verified {
			otp, err := a.issueVerificationCode(user.ID)
			if err != nil {
				log.Printf("issue verification code: %v", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if err := a.sendVerificationEmail(user, otp); err != nil {
				log.Printf("send verification email: %v", err)
				a.setFlash(w, "A new code was generated, but email delivery is not configured correctly yet.")
			} else {
				a.setFlash(w, "A new verification code has been sent.")
			}
		} else {
			a.setFlash(w, "If the account exists and still needs verification, a new code has been sent.")
		}
	} else {
		a.setFlash(w, "If the account exists and still needs verification, a new code has been sent.")
	}

	http.Redirect(w, r, "/verify-email?email="+url.QueryEscape(email), http.StatusSeeOther)
}

func (a *App) logoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}

	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = a.deleteSessionByToken(cookie.Value)
	}

	a.clearCookie(w, sessionCookieName)
	a.clearCookieWithOptions(w, csrfCookieName, false)
	a.setFlash(w, "You have been signed out.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) dashboardHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	data := a.newTemplateData(w, r, user)
	data.Title = "Dashboard"
	data.Description = "Authenticated user dashboard."
	data.Stats = []Stat{
		{Value: strconv.FormatInt(user.ID, 10), Label: "User ID"},
		{Value: strings.Join(user.Roles, ", "), Label: "Assigned roles"},
		{Value: verifiedLabel(user.Verified), Label: "Email status"},
	}
	a.render(w, "dashboard", data, http.StatusOK)
}

func verifiedLabel(verified bool) string {
	if verified {
		return "Verified"
	}
	return "Pending"
}

func (a *App) editorHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	data := a.newTemplateData(w, r, user)
	data.Title = "Editor Area"
	data.Description = "Editor and admin access only."
	a.render(w, "editor", data, http.StatusOK)
}

func (a *App) adminRedirectHandler(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (a *App) userManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	users, err := a.listUsers()
	if err != nil {
		log.Printf("list users: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	roles, err := a.listRoles()
	if err != nil {
		log.Printf("list roles: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "User Management"
	data.Description = "Manage users."
	data.Users = users
	data.Available = allRoles
	data.Roles = roles
	a.render(w, "user-management", data, http.StatusOK)
}

func (a *App) roleManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	roles, err := a.listRoles()
	if err != nil {
		log.Printf("list roles: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Role Management"
	data.Description = "Manage roles."
	data.Roles = roles
	data.Permissions = allPermissions
	a.render(w, "role-management", data, http.StatusOK)
}

func (a *App) admissionManagementHandler(w http.ResponseWriter, r *http.Request) {
	user, _ := a.currentUser(r.Context())
	admissions, err := a.listAdmissions()
	if err != nil {
		log.Printf("list admissions: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Admissions Management"
	data.Description = "Manage admissions."
	data.Admissions = admissions
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("action")))
	switch mode {
	case "new", "view", "edit":
		data.AdmissionMode = mode
	}
	if data.AdmissionMode == "view" || data.AdmissionMode == "edit" {
		admissionID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
		if err == nil && admissionID > 0 {
			selectedAdmission, err := a.findAdmissionByID(admissionID)
			if err == nil {
				data.SelectedAdmission = selectedAdmission
			}
		}
	}
	a.render(w, "admission-management", data, http.StatusOK)
}

func (a *App) createManagedUserHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	roles := normalizeRoles(r.Form["roles"])
	verified := r.FormValue("verified") == "true"

	if name == "" || !emailPattern.MatchString(email) || !passwordPattern.MatchString(password) {
		http.Error(w, "invalid user fields", http.StatusBadRequest)
		return
	}
	if len(roles) == 0 {
		http.Error(w, "at least one role must be selected", http.StatusBadRequest)
		return
	}

	if _, err := a.createManagedUser(name, email, password, roles, verified); err != nil {
		if errors.Is(err, ErrEmailTaken) {
			http.Error(w, "email already exists", http.StatusConflict)
			return
		}
		log.Printf("create managed user: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "User created.")
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (a *App) createRoleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	name := normalizeRoleName(r.FormValue("name"))
	permissions := normalizePermissions(r.Form["permissions"])
	if name == "" {
		http.Error(w, "role name is required", http.StatusBadRequest)
		return
	}
	if len(permissions) == 0 {
		http.Error(w, "at least one permission must be selected", http.StatusBadRequest)
		return
	}
	if err := a.createRole(name, permissions); err != nil {
		log.Printf("create role: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Role created.")
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

func (a *App) updateRoleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	roleID, err := strconv.ParseInt(r.FormValue("role_id"), 10, 64)
	if err != nil || roleID <= 0 {
		http.Error(w, "invalid role id", http.StatusBadRequest)
		return
	}
	name := normalizeRoleName(r.FormValue("name"))
	permissions := normalizePermissions(r.Form["permissions"])
	if name == "" {
		http.Error(w, "role name is required", http.StatusBadRequest)
		return
	}
	if len(permissions) == 0 {
		http.Error(w, "at least one permission must be selected", http.StatusBadRequest)
		return
	}

	if err := a.updateRole(roleID, name, permissions); err != nil {
		log.Printf("update role: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Role updated.")
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

func (a *App) deleteRoleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	roleID, err := strconv.ParseInt(r.FormValue("role_id"), 10, 64)
	if err != nil || roleID <= 0 {
		http.Error(w, "invalid role id", http.StatusBadRequest)
		return
	}
	if err := a.deleteRole(roleID); err != nil {
		log.Printf("delete role: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Role deleted.")
	http.Redirect(w, r, "/admin/roles", http.StatusSeeOther)
}

func (a *App) updateRolesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	targetID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
	if err != nil || targetID <= 0 {
		http.Error(w, "invalid user id", http.StatusBadRequest)
		return
	}

	roles := normalizeRoles(r.Form["roles"])
	if len(roles) == 0 {
		http.Error(w, "at least one role must be selected", http.StatusBadRequest)
		return
	}

	current, _ := a.currentUser(r.Context())
	if current != nil && current.ID == targetID && containsRole(current.Roles, "superadmin") && !containsRole(roles, "superadmin") {
		http.Error(w, "you cannot remove your own superadmin access", http.StatusBadRequest)
		return
	}
	if current != nil && current.ID == targetID && containsRole(current.Roles, "admin") && !containsRole(roles, "admin") && !containsRole(current.Roles, "superadmin") {
		http.Error(w, "you cannot remove your own admin access", http.StatusBadRequest)
		return
	}

	if err := a.replaceUserRoles(targetID, roles); err != nil {
		log.Printf("replace roles: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	redirectTo := r.FormValue("return_to")
	if redirectTo != "/admin/users" && redirectTo != "/admin/roles" {
		redirectTo = "/admin/users"
	}
	a.setFlash(w, "Roles updated.")
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

func (a *App) createAdmissionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	admission := admissionFromRequest(r)
	if err := validateAdmission(admission); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createAdmission(admission); err != nil {
		log.Printf("create admission: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Admission created.")
	http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
}

func (a *App) updateAdmissionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	admissionID, err := strconv.ParseInt(r.FormValue("admission_id"), 10, 64)
	if err != nil || admissionID <= 0 {
		http.Error(w, "invalid admission id", http.StatusBadRequest)
		return
	}

	admission := admissionFromRequest(r)
	admission.ID = admissionID
	if err := validateAdmission(admission); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateAdmission(admission); err != nil {
		log.Printf("update admission: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Admission updated.")
	http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
}

func (a *App) deleteAdmissionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.verifyCSRF(r); err != nil {
		http.Error(w, "invalid csrf token", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form submission", http.StatusBadRequest)
		return
	}

	admissionID, err := strconv.ParseInt(r.FormValue("admission_id"), 10, 64)
	if err != nil || admissionID <= 0 {
		http.Error(w, "invalid admission id", http.StatusBadRequest)
		return
	}
	if err := a.deleteAdmission(admissionID); err != nil {
		log.Printf("delete admission: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	a.setFlash(w, "Admission deleted.")
	http.Redirect(w, r, "/admin/admissions", http.StatusSeeOther)
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' https://cdn.tailwindcss.com; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'self'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *App) sessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			a.setFlash(w, "Sign in to continue.")
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		user, err := a.userFromSessionToken(cookie.Value)
		if err != nil {
			a.clearCookie(w, sessionCookieName)
			a.clearCookieWithOptions(w, csrfCookieName, false)
			a.setFlash(w, "Your session has expired. Sign in again.")
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !user.Verified {
			a.clearCookie(w, sessionCookieName)
			a.clearCookieWithOptions(w, csrfCookieName, false)
			a.setFlash(w, "Verify your email to continue.")
			http.Redirect(w, r, "/verify-email?email="+url.QueryEscape(user.Email), http.StatusSeeOther)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *App) requireRoles(next http.Handler, roles ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.currentUser(r.Context())
		if !ok || !userHasAnyRole(user, roles...) {
			data := a.newTemplateData(w, r, user)
			data.Title = "Forbidden"
			data.Description = "You do not have permission to view this page."
			data.Error = "You do not have permission to view this page."
			a.render(w, "forbidden", data, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (a *App) requirePermission(next http.Handler, permission string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := a.currentUser(r.Context())
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		permissions, err := a.permissionsForUser(user.ID)
		if err != nil {
			log.Printf("permissions for user: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if !containsPermission(permissions, permission) {
			data := a.newTemplateData(w, r, user)
			data.Title = "Forbidden"
			data.Description = "You do not have permission to view this page."
			data.Error = "You do not have permission to view this page."
			a.render(w, "forbidden", data, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (a *App) currentUser(ctx context.Context) (*User, bool) {
	user, ok := ctx.Value(userContextKey).(*User)
	return user, ok
}

func (a *App) optionalUser(r *http.Request) *User {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil
	}
	user, err := a.userFromSessionToken(cookie.Value)
	if err != nil {
		return nil
	}
	return user
}

func (a *App) render(w http.ResponseWriter, name string, data TemplateData, status int) {
	tmpl, ok := a.templates[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if data.Flash != "" {
		a.clearCookieWithOptions(w, flashCookieName, true)
	}
	w.WriteHeader(status)
	if err := tmpl.ExecuteTemplate(w, "base", data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (a *App) newTemplateData(w http.ResponseWriter, r *http.Request, user *User) TemplateData {
	csrfToken := a.ensureCSRFCookie(w, r)
	return TemplateData{
		CurrentPath:   r.URL.Path,
		User:          user,
		CSRFToken:     csrfToken,
		Flash:         a.consumeFlash(r),
		OTPCodeLength: 6,
	}
}

func (a *App) writeFormError(w http.ResponseWriter, r *http.Request, tmplName, title, message string, status int) {
	user, _ := a.currentUser(r.Context())
	data := a.newTemplateData(w, r, user)
	data.Title = title
	data.Description = title
	if tmplName == "login" || tmplName == "register" || tmplName == "verify-email" {
		data.HideChrome = true
	}
	data.Error = message
	if tmplName == "verify-email" {
		data.ResendAction = "/verify-email/resend"
	}
	a.render(w, tmplName, data, status)
}

func (a *App) writeVerificationError(w http.ResponseWriter, r *http.Request, email, message string, status int) {
	data := a.newTemplateData(w, r, nil)
	data.Title = "Verify your email"
	data.Description = "Confirm your email with a 6-digit code."
	data.HideChrome = true
	data.PendingEmail = email
	data.ResendAction = "/verify-email/resend"
	data.Error = message
	a.render(w, "verify-email", data, status)
}

func (a *App) ensureCSRFCookie(w http.ResponseWriter, r *http.Request) string {
	if cookie, err := r.Cookie(csrfCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	token, err := generateToken(24)
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
		Expires:  time.Now().UTC().Add(sessionTTL),
	})
	return token
}

func (a *App) verifyCSRF(r *http.Request) error {
	formToken := r.FormValue("csrf_token")
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" || formToken == "" || cookie.Value != formToken {
		return errors.New("csrf verification failed")
	}
	return nil
}

func (a *App) createSession(w http.ResponseWriter, userID int64) error {
	rawToken, err := generateToken(32)
	if err != nil {
		return err
	}

	hash := sha256.Sum256([]byte(rawToken))
	expiresAt := time.Now().UTC().Add(sessionTTL)
	if _, err := a.db.Exec(`
		INSERT INTO sessions (user_id, token_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?)
	`, userID, fmt.Sprintf("%x", hash[:]), expiresAt, time.Now().UTC()); err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    rawToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(sessionTTL.Seconds()),
	})

	csrfToken, err := generateToken(24)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookieName,
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: false,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(sessionTTL.Seconds()),
	})

	return nil
}

func (a *App) userFromSessionToken(token string) (*User, error) {
	hash := sha256.Sum256([]byte(token))
	row := a.db.QueryRow(`
		SELECT u.id, u.email, u.name, u.email_verified_at, u.created_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?
	`, fmt.Sprintf("%x", hash[:]), time.Now().UTC())

	var user User
	var verifiedAt sql.NullTime
	if err := row.Scan(&user.ID, &user.Email, &user.Name, &verifiedAt, &user.CreatedAt); err != nil {
		return nil, err
	}
	user.Verified = verifiedAt.Valid

	roles, err := a.rolesForUser(user.ID)
	if err != nil {
		return nil, err
	}
	user.Roles = roles
	permissions, err := a.permissionsForUser(user.ID)
	if err != nil {
		return nil, err
	}
	user.Permissions = permissions
	return &user, nil
}

func (a *App) createUser(name, email, password string) (*User, error) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var existingUsers int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&existingUsers); err != nil {
		return nil, err
	}

	result, err := tx.Exec(`
		INSERT INTO users (email, name, password_hash, created_at)
		VALUES (?, ?, ?, ?)
	`, email, name, string(passwordHash), time.Now().UTC())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrEmailTaken
		}
		return nil, err
	}

	userID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}

	rolesToAssign := []string{"customer"}
	for _, role := range rolesToAssign {
		roleID, err := roleIDByName(tx, role)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, roleID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	roles, err := a.rolesForUser(userID)
	if err != nil {
		return nil, err
	}
	permissions, err := a.permissionsForUser(userID)
	if err != nil {
		return nil, err
	}
	return &User{
		ID:          userID,
		Email:       email,
		Name:        name,
		Roles:       roles,
		Permissions: permissions,
		Verified:    false,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

func (a *App) createManagedUser(name, email, password string, roles []string, verified bool) (*User, error) {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var verifiedAt any
	if verified {
		verifiedAt = time.Now().UTC()
	}

	result, err := tx.Exec(`
		INSERT INTO users (email, name, password_hash, created_at, email_verified_at)
		VALUES (?, ?, ?, ?, ?)
	`, email, name, string(passwordHash), time.Now().UTC(), verifiedAt)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrEmailTaken
		}
		return nil, err
	}

	userID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	for _, role := range roles {
		roleID, err := roleIDByName(tx, role)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, roleID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return a.findUserByID(userID)
}

func (a *App) findUserByEmail(email string) (*User, string, error) {
	row := a.db.QueryRow(`
		SELECT id, email, name, password_hash, email_verified_at, created_at
		FROM users
		WHERE email = ?
	`, email)

	var user User
	var passwordHash string
	var verifiedAt sql.NullTime
	if err := row.Scan(&user.ID, &user.Email, &user.Name, &passwordHash, &verifiedAt, &user.CreatedAt); err != nil {
		return nil, "", err
	}
	user.Verified = verifiedAt.Valid

	roles, err := a.rolesForUser(user.ID)
	if err != nil {
		return nil, "", err
	}
	user.Roles = roles
	permissions, err := a.permissionsForUser(user.ID)
	if err != nil {
		return nil, "", err
	}
	user.Permissions = permissions
	return &user, passwordHash, nil
}

func (a *App) findUserByID(userID int64) (*User, error) {
	row := a.db.QueryRow(`
		SELECT id, email, name, email_verified_at, created_at
		FROM users
		WHERE id = ?
	`, userID)

	var user User
	var verifiedAt sql.NullTime
	if err := row.Scan(&user.ID, &user.Email, &user.Name, &verifiedAt, &user.CreatedAt); err != nil {
		return nil, err
	}
	user.Verified = verifiedAt.Valid
	roles, err := a.rolesForUser(user.ID)
	if err != nil {
		return nil, err
	}
	user.Roles = roles
	permissions, err := a.permissionsForUser(user.ID)
	if err != nil {
		return nil, err
	}
	user.Permissions = permissions
	return &user, nil
}

func (a *App) listUsers() ([]User, error) {
	rows, err := a.db.Query(`
		SELECT id, email, name, email_verified_at, created_at
		FROM users
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var user User
		var verifiedAt sql.NullTime
		if err := rows.Scan(&user.ID, &user.Email, &user.Name, &verifiedAt, &user.CreatedAt); err != nil {
			return nil, err
		}
		user.Verified = verifiedAt.Valid
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range users {
		roles, err := a.rolesForUser(users[i].ID)
		if err != nil {
			return nil, err
		}
		users[i].Roles = roles
	}

	return users, nil
}

func (a *App) rolesForUser(userID int64) ([]string, error) {
	rows, err := a.db.Query(`
		SELECT r.name
		FROM roles r
		JOIN user_roles ur ON ur.role_id = r.id
		WHERE ur.user_id = ?
		ORDER BY r.name ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

func (a *App) listRoles() ([]Role, error) {
	rows, err := a.db.Query(`SELECT id, name FROM roles ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []Role
	for rows.Next() {
		var role Role
		if err := rows.Scan(&role.ID, &role.Name); err != nil {
			return nil, err
		}
		role.System = isSystemRole(role.Name)
		roles = append(roles, role)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for i := range roles {
		permissions, err := a.permissionsForRole(roles[i].ID)
		if err != nil {
			return nil, err
		}
		roles[i].Permissions = permissions
	}
	return roles, nil
}

func (a *App) permissionsForUser(userID int64) ([]string, error) {
	rows, err := a.db.Query(`
		SELECT DISTINCT rp.permission
		FROM role_permissions rp
		JOIN user_roles ur ON ur.role_id = rp.role_id
		WHERE ur.user_id = ?
		ORDER BY rp.permission ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var permissions []string
	for rows.Next() {
		var permission string
		if err := rows.Scan(&permission); err != nil {
			return nil, err
		}
		permissions = append(permissions, permission)
	}
	return permissions, rows.Err()
}

func (a *App) permissionsForRole(roleID int64) ([]string, error) {
	rows, err := a.db.Query(`
		SELECT permission
		FROM role_permissions
		WHERE role_id = ?
		ORDER BY permission ASC
	`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var permissions []string
	for rows.Next() {
		var permission string
		if err := rows.Scan(&permission); err != nil {
			return nil, err
		}
		permissions = append(permissions, permission)
	}
	return permissions, rows.Err()
}

func (a *App) createRole(name string, permissions []string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`INSERT INTO roles (name) VALUES (?)`, name)
	if err != nil {
		return err
	}
	roleID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	for _, permission := range permissions {
		if _, err := tx.Exec(`INSERT INTO role_permissions (role_id, permission) VALUES (?, ?)`, roleID, permission); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) updateRole(roleID int64, name string, permissions []string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var currentName string
	if err := tx.QueryRow(`SELECT name FROM roles WHERE id = ?`, roleID).Scan(&currentName); err != nil {
		return err
	}
	if isSystemRole(currentName) {
		name = currentName
	}

	if _, err := tx.Exec(`UPDATE roles SET name = ? WHERE id = ?`, name, roleID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM role_permissions WHERE role_id = ?`, roleID); err != nil {
		return err
	}
	for _, permission := range permissions {
		if _, err := tx.Exec(`INSERT INTO role_permissions (role_id, permission) VALUES (?, ?)`, roleID, permission); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (a *App) deleteRole(roleID int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var roleName string
	if err := tx.QueryRow(`SELECT name FROM roles WHERE id = ?`, roleID).Scan(&roleName); err != nil {
		return err
	}
	if isSystemRole(roleName) {
		return errors.New("system role cannot be deleted")
	}
	if _, err := tx.Exec(`DELETE FROM role_permissions WHERE role_id = ?`, roleID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_roles WHERE role_id = ?`, roleID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM roles WHERE id = ?`, roleID); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) replaceUserRoles(userID int64, roles []string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM user_roles WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, role := range roles {
		roleID, err := roleIDByName(tx, role)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, roleID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) deleteSessionsForUser(userID int64) error {
	_, err := a.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (a *App) listAdmissions() ([]Admission, error) {
	rows, err := a.db.Query(`
		SELECT id, full_name, date_of_birth, gender, address, passport_number, school,
		       guardian_name, guardian_relationship, guardian_contact_number, guardian_alternative_contact_number,
		       medical_information, created_at
		FROM admissions
		ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var admissions []Admission
	for rows.Next() {
		var admission Admission
		if err := rows.Scan(
			&admission.ID,
			&admission.FullName,
			&admission.DateOfBirth,
			&admission.Gender,
			&admission.Address,
			&admission.PassportNumber,
			&admission.School,
			&admission.GuardianName,
			&admission.GuardianRelationship,
			&admission.GuardianContactNumber,
			&admission.GuardianAlternativePhone,
			&admission.MedicalInformation,
			&admission.CreatedAt,
		); err != nil {
			return nil, err
		}
		admissions = append(admissions, admission)
	}
	return admissions, rows.Err()
}

func (a *App) createAdmission(admission Admission) error {
	_, err := a.db.Exec(`
		INSERT INTO admissions (
			full_name, date_of_birth, gender, address, passport_number, school,
			guardian_name, guardian_relationship, guardian_contact_number, guardian_alternative_contact_number,
			medical_information, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		admission.FullName,
		admission.DateOfBirth,
		admission.Gender,
		admission.Address,
		admission.PassportNumber,
		admission.School,
		admission.GuardianName,
		admission.GuardianRelationship,
		admission.GuardianContactNumber,
		admission.GuardianAlternativePhone,
		admission.MedicalInformation,
		time.Now().UTC(),
		time.Now().UTC(),
	)
	return err
}

func (a *App) updateAdmission(admission Admission) error {
	_, err := a.db.Exec(`
		UPDATE admissions
		SET full_name = ?, date_of_birth = ?, gender = ?, address = ?, passport_number = ?, school = ?,
		    guardian_name = ?, guardian_relationship = ?, guardian_contact_number = ?, guardian_alternative_contact_number = ?,
		    medical_information = ?, updated_at = ?
		WHERE id = ?
	`,
		admission.FullName,
		admission.DateOfBirth,
		admission.Gender,
		admission.Address,
		admission.PassportNumber,
		admission.School,
		admission.GuardianName,
		admission.GuardianRelationship,
		admission.GuardianContactNumber,
		admission.GuardianAlternativePhone,
		admission.MedicalInformation,
		time.Now().UTC(),
		admission.ID,
	)
	return err
}

func (a *App) deleteAdmission(admissionID int64) error {
	_, err := a.db.Exec(`DELETE FROM admissions WHERE id = ?`, admissionID)
	return err
}

func (a *App) findAdmissionByID(admissionID int64) (*Admission, error) {
	row := a.db.QueryRow(`
		SELECT id, full_name, date_of_birth, gender, address, passport_number, school,
		       guardian_name, guardian_relationship, guardian_contact_number, guardian_alternative_contact_number,
		       medical_information, created_at
		FROM admissions
		WHERE id = ?
	`, admissionID)

	var admission Admission
	if err := row.Scan(
		&admission.ID,
		&admission.FullName,
		&admission.DateOfBirth,
		&admission.Gender,
		&admission.Address,
		&admission.PassportNumber,
		&admission.School,
		&admission.GuardianName,
		&admission.GuardianRelationship,
		&admission.GuardianContactNumber,
		&admission.GuardianAlternativePhone,
		&admission.MedicalInformation,
		&admission.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &admission, nil
}

func (a *App) deleteSessionByToken(token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := a.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, fmt.Sprintf("%x", hash[:]))
	return err
}

func (a *App) setFlash(w http.ResponseWriter, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(message)),
		Path:     "/",
		HttpOnly: true,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   10,
	})
}

func (a *App) consumeFlash(r *http.Request) string {
	cookie, err := r.Cookie(flashCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (a *App) clearCookie(w http.ResponseWriter, name string) {
	a.clearCookieWithOptions(w, name, true)
}

func (a *App) clearCookieWithOptions(w http.ResponseWriter, name string, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: httpOnly,
		Secure:   a.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func runMigrations(db *sql.DB) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`ALTER TABLE users ADD COLUMN email_verified_at DATETIME`,
		`CREATE TABLE IF NOT EXISTS roles (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE
		)`,
		`CREATE TABLE IF NOT EXISTS user_roles (
			user_id INTEGER NOT NULL,
			role_id INTEGER NOT NULL,
			PRIMARY KEY (user_id, role_id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS role_permissions (
			role_id INTEGER NOT NULL,
			permission TEXT NOT NULL,
			PRIMARY KEY (role_id, permission),
			FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			token_hash TEXT NOT NULL UNIQUE,
			expires_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS email_verifications (
			user_id INTEGER PRIMARY KEY,
			otp_hash TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			created_at DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS admissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			full_name TEXT NOT NULL,
			date_of_birth TEXT NOT NULL,
			gender TEXT NOT NULL,
			address TEXT NOT NULL,
			passport_number TEXT NOT NULL,
			school TEXT NOT NULL,
			guardian_name TEXT NOT NULL,
			guardian_relationship TEXT NOT NULL,
			guardian_contact_number TEXT NOT NULL,
			guardian_alternative_contact_number TEXT NOT NULL,
			medical_information TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_token_hash ON sessions(token_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_role_permissions_role_id ON role_permissions(role_id)`,
		`CREATE INDEX IF NOT EXISTS idx_email_verifications_expires_at ON email_verifications(expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_admissions_created_at ON admissions(created_at)`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil && !isIgnorableMigrationError(err, stmt) {
			return err
		}
	}

	if _, err := db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC()); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM email_verifications WHERE expires_at <= ?`, time.Now().UTC())
	return err
}

func seedRoles(db *sql.DB) error {
	for _, role := range allRoles {
		if _, err := db.Exec(`INSERT OR IGNORE INTO roles (name) VALUES (?)`, role); err != nil {
			return err
		}
	}
	rolePermissions := map[string][]string{
		"customer":   {"dashboard.view"},
		"editor":     {"dashboard.view", "editor.access"},
		"admin":      {"dashboard.view", "editor.access", "users.manage", "roles.manage", "admissions.manage"},
		"superadmin": {"dashboard.view", "editor.access", "users.manage", "roles.manage", "admissions.manage"},
	}
	for roleName, permissions := range rolePermissions {
		roleID, err := queryRoleID(db, roleName)
		if err != nil {
			return err
		}
		for _, permission := range permissions {
			if _, err := db.Exec(`INSERT OR IGNORE INTO role_permissions (role_id, permission) VALUES (?, ?)`, roleID, permission); err != nil {
				return err
			}
		}
	}
	return nil
}

func bootstrapSuperadmin(db *sql.DB) error {
	const (
		superadminName     = "Janon Emersion T"
		superadminEmail    = "janon@lkprofessionals.com"
		superadminPassword = "Jj112112@!@!"
	)

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(superadminPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	row := tx.QueryRow(`SELECT id FROM users WHERE email = ?`, superadminEmail)
	var userID int64
	switch err := row.Scan(&userID); {
	case err == nil:
		if _, err := tx.Exec(`
			UPDATE users
			SET name = ?, password_hash = ?, email_verified_at = ?
			WHERE id = ?
		`, superadminName, string(passwordHash), now, userID); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
		result, err := tx.Exec(`
			INSERT INTO users (email, name, password_hash, created_at, email_verified_at)
			VALUES (?, ?, ?, ?, ?)
		`, superadminEmail, superadminName, string(passwordHash), now, now)
		if err != nil {
			return err
		}
		userID, err = result.LastInsertId()
		if err != nil {
			return err
		}
	default:
		return err
	}

	if _, err := tx.Exec(`DELETE FROM user_roles WHERE user_id = ?`, userID); err != nil {
		return err
	}

	for _, role := range []string{"superadmin", "admin", "editor"} {
		roleID, err := roleIDByName(tx, role)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, roleID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func roleIDByName(tx *sql.Tx, role string) (int64, error) {
	row := tx.QueryRow(`SELECT id FROM roles WHERE name = ?`, role)
	var roleID int64
	if err := row.Scan(&roleID); err != nil {
		return 0, err
	}
	return roleID, nil
}

func queryRoleID(db *sql.DB, role string) (int64, error) {
	row := db.QueryRow(`SELECT id FROM roles WHERE name = ?`, role)
	var roleID int64
	if err := row.Scan(&roleID); err != nil {
		return 0, err
	}
	return roleID, nil
}

func generateToken(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func generateOTP() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func (a *App) issueVerificationCode(userID int64) (string, error) {
	otp, err := generateOTP()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	_, err = a.db.Exec(`
		INSERT INTO email_verifications (user_id, otp_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			otp_hash = excluded.otp_hash,
			expires_at = excluded.expires_at,
			created_at = excluded.created_at
	`, userID, hashValue(otp), now.Add(otpTTL), now)
	if err != nil {
		return "", err
	}
	return otp, nil
}

func (a *App) sendVerificationEmail(user *User, otp string) error {
	if !a.smtp.Enabled {
		return errors.New("smtp is not configured")
	}

	headers := "" +
		"From: " + a.smtp.From + "\r\n" +
		"To: " + user.Email + "\r\n" +
		"Subject: Verify your email address\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n\r\n"
	body := fmt.Sprintf(
		"Hi %s,\r\n\r\nYour mekmaa3 verification code is %s.\r\nIt expires in 10 minutes.\r\n\r\nIf you did not create this account, you can ignore this email.\r\n",
		user.Name,
		otp,
	)
	auth := smtp.PlainAuth("", a.smtp.Username, a.smtp.Password, a.smtp.Host)
	return smtp.SendMail(a.smtp.Host+":"+a.smtp.Port, auth, a.smtp.From, []string{user.Email}, []byte(headers+body))
}

func (a *App) consumeVerificationCode(userID int64, otp string) error {
	row := a.db.QueryRow(`SELECT otp_hash, expires_at FROM email_verifications WHERE user_id = ?`, userID)
	var otpHash string
	var expiresAt time.Time
	if err := row.Scan(&otpHash, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidOTP
		}
		return err
	}

	if expiresAt.Before(time.Now().UTC()) || otpHash != hashValue(otp) {
		return ErrInvalidOTP
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE users SET email_verified_at = ? WHERE id = ?`, time.Now().UTC(), userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM email_verifications WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func hashValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])
}

func userHasAnyRole(user *User, roles ...string) bool {
	if user == nil {
		return false
	}
	for _, candidate := range roles {
		for _, assigned := range user.Roles {
			if assigned == candidate {
				return true
			}
		}
	}
	return false
}

func userHasRole(user *User, role string) bool {
	return userHasAnyRole(user, role)
}

func containsRole(roles []string, target string) bool {
	for _, role := range roles {
		if role == target {
			return true
		}
	}
	return false
}

func normalizeRoles(roles []string) []string {
	allowed := make(map[string]struct{}, len(allRoles))
	for _, role := range allRoles {
		allowed[role] = struct{}{}
	}

	seen := map[string]struct{}{}
	var normalized []string
	for _, role := range roles {
		role = strings.ToLower(strings.TrimSpace(role))
		if _, ok := allowed[role]; !ok {
			continue
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		normalized = append(normalized, role)
	}
	sort.Strings(normalized)
	return normalized
}

func normalizePermissions(permissions []string) []string {
	allowed := make(map[string]struct{}, len(allPermissions))
	for _, permission := range allPermissions {
		allowed[permission] = struct{}{}
	}

	seen := map[string]struct{}{}
	var normalized []string
	for _, permission := range permissions {
		permission = strings.ToLower(strings.TrimSpace(permission))
		if _, ok := allowed[permission]; !ok {
			continue
		}
		if _, ok := seen[permission]; ok {
			continue
		}
		seen[permission] = struct{}{}
		normalized = append(normalized, permission)
	}
	sort.Strings(normalized)
	return normalized
}

func admissionFromRequest(r *http.Request) Admission {
	return Admission{
		FullName:                 strings.TrimSpace(r.FormValue("full_name")),
		DateOfBirth:              strings.TrimSpace(r.FormValue("date_of_birth")),
		Gender:                   strings.ToLower(strings.TrimSpace(r.FormValue("gender"))),
		Address:                  strings.TrimSpace(r.FormValue("address")),
		PassportNumber:           strings.TrimSpace(r.FormValue("passport_number")),
		School:                   strings.TrimSpace(r.FormValue("school")),
		GuardianName:             strings.TrimSpace(r.FormValue("guardian_name")),
		GuardianRelationship:     strings.TrimSpace(r.FormValue("guardian_relationship")),
		GuardianContactNumber:    strings.TrimSpace(r.FormValue("guardian_contact_number")),
		GuardianAlternativePhone: strings.TrimSpace(r.FormValue("guardian_alternative_contact_number")),
		MedicalInformation:       strings.TrimSpace(r.FormValue("medical_information")),
	}
}

func validateAdmission(admission Admission) error {
	switch {
	case admission.FullName == "":
		return errors.New("full name is required")
	case admission.DateOfBirth == "":
		return errors.New("date of birth is required")
	case admission.Gender != "male" && admission.Gender != "female":
		return errors.New("gender is required")
	case admission.Address == "":
		return errors.New("address is required")
	case admission.PassportNumber == "":
		return errors.New("p.p. no is required")
	case admission.School == "":
		return errors.New("school is required")
	case admission.GuardianName == "":
		return errors.New("parent or guardian name is required")
	case admission.GuardianRelationship == "":
		return errors.New("relationship is required")
	case admission.GuardianContactNumber == "":
		return errors.New("contact number is required")
	case admission.GuardianAlternativePhone == "":
		return errors.New("alternative contact number is required")
	case admission.MedicalInformation == "":
		return errors.New("medical information is required")
	default:
		return nil
	}
}

func containsPermission(permissions []string, target string) bool {
	for _, permission := range permissions {
		if permission == target {
			return true
		}
	}
	return false
}

func normalizeRoleName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func isSystemRole(name string) bool {
	switch name {
	case "customer", "editor", "admin", "superadmin":
		return true
	default:
		return false
	}
}

func isIgnorableMigrationError(err error, stmt string) bool {
	return strings.Contains(stmt, "ALTER TABLE users ADD COLUMN email_verified_at") &&
		strings.Contains(strings.ToLower(err.Error()), "duplicate column name")
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func loadDotEnv(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	for _, rawLine := range strings.Split(string(content), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}

	return nil
}

func buildTemplates() (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"hasRole": func(user *User, role string) bool {
			return userHasAnyRole(user, role)
		},
		"hasAnyRole": func(user *User, roles ...string) bool {
			return userHasAnyRole(user, roles...)
		},
		"isCurrentPath": func(current string, paths ...string) bool {
			for _, path := range paths {
				if current == path {
					return true
				}
			}
			return false
		},
		"contains": func(roles []string, role string) bool {
			return containsRole(roles, role)
		},
		"containsPermission": func(permissions []string, permission string) bool {
			return containsPermission(permissions, permission)
		},
		"hasPermission": func(user *User, permission string) bool {
			if user == nil {
				return false
			}
			return containsPermission(user.Permissions, permission)
		},
		"isSystemRole": isSystemRole,
	}

	base, err := template.New("base.html").Funcs(funcs).ParseFiles("templates/base.html")
	if err != nil {
		return nil, err
	}

	pages := map[string]string{
		"home":                 "templates/home.html",
		"login":                "templates/login.html",
		"register":             "templates/register.html",
		"verify-email":         "templates/verify-email.html",
		"dashboard":            "templates/dashboard/dashboard.html",
		"editor":               "templates/dashboard/editor.html",
		"user-management":      "templates/dashboard/user-management.html",
		"role-management":      "templates/dashboard/role-management.html",
		"admission-management": "templates/dashboard/admission-management.html",
		"forbidden":            "templates/dashboard/forbidden.html",
	}
	dashboardPartials := []string{
		"templates/dashboard/src/sidebar.html",
		"templates/dashboard/src/header.html",
		"templates/dashboard/src/footer.html",
	}
	templates := make(map[string]*template.Template, len(pages))
	for page, path := range pages {
		tmpl, err := base.Clone()
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(path, "templates/dashboard/") {
			if _, err := tmpl.ParseFiles(dashboardPartials...); err != nil {
				return nil, err
			}
		}
		if _, err := tmpl.ParseFiles(path); err != nil {
			return nil, err
		}
		templates[page] = tmpl
	}
	return templates, nil
}

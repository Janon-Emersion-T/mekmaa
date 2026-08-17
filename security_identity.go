package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrSessionNotFound = errors.New("session not found")

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.tailwindcss.com; style-src 'self' 'unsafe-inline'; img-src 'self' data:; base-uri 'self'; form-action 'self'")
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
			if errors.Is(err, ErrSessionNotFound) {
				a.clearCookie(w, sessionCookieName)
				a.clearCookieWithOptions(w, csrfCookieName, false)
				a.setFlash(w, "Your session has expired. Sign in again.")
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			log.Printf("session lookup failed: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if !user.Verified {
			a.clearCookie(w, sessionCookieName)
			a.clearCookieWithOptions(w, csrfCookieName, false)
			a.setFlash(w, "Verify your email to continue.")
			http.Redirect(w, r, "/verify-email?email="+url.QueryEscape(user.Email), http.StatusSeeOther)
			return
		}
		if err := a.refreshSession(w, cookie.Value); err != nil {
			log.Printf("refresh session: %v", err)
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
		if containsRole(user.Roles, "superadmin") {
			next.ServeHTTP(w, r)
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

func (a *App) requireAnyPermission(next http.Handler, required ...string) http.Handler {
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
		for _, permission := range required {
			if containsPermission(permissions, permission) {
				next.ServeHTTP(w, r)
				return
			}
		}

		data := a.newTemplateData(w, r, user)
		data.Title = "Forbidden"
		data.Description = "You do not have permission to view this page."
		data.Error = "You do not have permission to view this page."
		a.render(w, "forbidden", data, http.StatusForbidden)
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
		if !errors.Is(err, ErrSessionNotFound) {
			log.Printf("optional session lookup failed: %v", err)
		}
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
	data := TemplateData{
		CurrentPath:   r.URL.Path,
		User:          user,
		CSRFToken:     csrfToken,
		Flash:         a.consumeFlash(r),
		OTPCodeLength: 6,
	}
	query := r.URL.Query()
	queryKeys := make([]string, 0, len(query))
	for key := range query {
		if key == "division" {
			continue
		}
		queryKeys = append(queryKeys, key)
	}
	sort.Strings(queryKeys)
	for _, key := range queryKeys {
		for _, value := range query[key] {
			data.CurrentQueryFields = append(data.CurrentQueryFields, QueryField{
				Key:   key,
				Value: value,
			})
		}
	}
	if a != nil && a.db != nil {
		if divisions, err := a.listDivisions(false); err == nil {
			data.Divisions = divisions
			for _, division := range divisions {
				if division.Active {
					data.ActiveDivisions = append(data.ActiveDivisions, division)
				}
			}
		}
	}
	if user != nil {
		availableDivisions := append([]Division(nil), user.Divisions...)
		if a != nil {
			if accessible, err := a.accessibleDivisionsForUser(user, false); err == nil {
				availableDivisions = accessible
			}
		}
		data.AvailableDivisions = append(data.AvailableDivisions, availableDivisions...)
		if canViewAllDivisions(user) {
			data.DivisionScopeOptions = append(data.DivisionScopeOptions, DivisionScopeOption{
				Key:   divisionScopeAll,
				Label: "All Mekmaa",
			})
		}
		if userCanSwitchOperationalDivision(user) {
			for i := range availableDivisions {
				division := availableDivisions[i]
				data.DivisionScopeOptions = append(data.DivisionScopeOptions, DivisionScopeOption{
					Key:        division.Slug,
					Label:      division.Name,
					DivisionID: division.ID,
					Division:   &division,
				})
			}
		}
	}
	data.SelectedDivisionScope = strings.TrimSpace(r.URL.Query().Get("division"))
	if data.SelectedDivisionScope == "" && user != nil {
		data.SelectedDivisionScope = userDivisionScope(user)
	}
	return data
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
	if _, err := a.execDB(a.dbQuery(`
		INSERT INTO sessions (user_id, token_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?)
	`), userID, fmt.Sprintf("%x", hash[:]), expiresAt, time.Now().UTC()); err != nil {
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

func (a *App) refreshSession(w http.ResponseWriter, rawToken string) error {
	hash := sha256.Sum256([]byte(rawToken))
	expiresAt := time.Now().UTC().Add(sessionTTL)
	result, err := a.execDB(a.dbQuery(`
		UPDATE sessions
		SET expires_at = ?
		WHERE token_hash = ?
	`), expiresAt, fmt.Sprintf("%x", hash[:]))
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return ErrSessionNotFound
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
	return nil
}

func (a *App) userFromSessionToken(token string) (*User, error) {
	hash := sha256.Sum256([]byte(token))
	row := a.queryRowDB(a.dbQuery(`
		SELECT u.id, u.email, u.name, u.email_verified_at, u.created_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?
	`), fmt.Sprintf("%x", hash[:]), time.Now().UTC())

	var user User
	var verifiedAt sql.NullTime
	if err := row.Scan(&user.ID, &user.Email, &user.Name, &verifiedAt, &user.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
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
	divisions, err := a.divisionsForUser(user.ID)
	if err != nil {
		return nil, err
	}
	fillUserDivisions(&user, divisions)
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
	divisions, err := a.divisionsForUser(userID)
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
		Divisions:   divisions,
	}, nil
}

func coachFromRequest(r *http.Request, creating bool) (User, string, error) {
	var coach User
	var password string

	if !creating {
		coachID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("coach_id")), 10, 64)
		if err != nil || coachID <= 0 {
			return coach, "", errors.New("invalid coach id")
		}
		coach.ID = coachID
	}

	coach.Name = strings.TrimSpace(r.FormValue("name"))
	coach.Email = strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	coach.Phone = strings.TrimSpace(r.FormValue("phone"))
	coach.Address = strings.TrimSpace(r.FormValue("address"))
	coach.Specialties = strings.TrimSpace(r.FormValue("specialties"))
	coach.Notes = strings.TrimSpace(r.FormValue("notes"))
	coach.Active = r.FormValue("active") == "true"
	coach.CoachType = normalizeCoachType(r.FormValue("coach_type"))
	coach.ParentCoachID = parseInt64Query(r.FormValue("parent_coach_id"))

	if coach.Name == "" || !emailPattern.MatchString(coach.Email) {
		return coach, "", errors.New("invalid coach details")
	}
	if coach.CoachType == "" {
		return coach, "", errors.New("invalid coach type")
	}
	if coach.CoachType == "main" {
		coach.ParentCoachID = 0
	}

	if creating {
		password = r.FormValue("password")
		if !passwordPattern.MatchString(password) {
			return coach, "", errors.New("invalid temporary password")
		}
	}

	return coach, password, nil
}

func normalizeCoachType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "main":
		return "main"
	case "sub":
		return "sub"
	default:
		return "main"
	}
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

func (a *App) createCoach(coach User, password string) (*User, error) {
	if err := a.validateCoachHierarchy(coach); err != nil {
		return nil, err
	}
	createdCoach, err := a.createManagedUser(
		coach.Name,
		coach.Email,
		password,
		[]string{"coach"},
		true,
	)
	if err != nil {
		return nil, err
	}
	if err := a.upsertCoachProfile(createdCoach.ID, coach); err != nil {
		return nil, err
	}
	return a.findCoachByID(createdCoach.ID)
}

func (a *App) updateCoach(coach User) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(`
		UPDATE users
		SET name = ?, email = ?
		WHERE id = ?
	`, coach.Name, coach.Email, coach.ID)
	if err != nil {
		if isUniqueConstraintError(err) {
			return ErrEmailTaken
		}
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}

	isCoach, err := a.userHasRoleTx(tx, coach.ID, "coach")
	if err != nil {
		return err
	}
	if !isCoach {
		return sql.ErrNoRows
	}
	if err := a.ensureCoachCanChangeType(tx, coach); err != nil {
		return err
	}
	if err := a.validateCoachHierarchy(coach); err != nil {
		return err
	}

	if err := upsertCoachProfileTx(tx, coach.ID, coach); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) deleteCoach(coachID int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	roles, err := a.rolesForUserTx(tx, coachID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return err
	}
	if len(roles) != 1 || roles[0] != "coach" {
		return ErrCoachHasOtherRoles
	}
	if err := ensureCoachHasNoSubCoachesTx(tx, coachID); err != nil {
		return err
	}

	result, err := tx.Exec(`DELETE FROM users WHERE id = ?`, coachID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}

	return tx.Commit()
}

func (a *App) validateCoachHierarchy(coach User) error {
	coachType := normalizeCoachType(coach.CoachType)
	if coachType == "sub" {
		if coach.ParentCoachID <= 0 {
			return ErrCoachRequiresMainCoach
		}
		if coach.ParentCoachID == coach.ID {
			return ErrCoachParentMustBeMain
		}
		parent, err := a.findCoachByID(coach.ParentCoachID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCoachParentMustBeMain
			}
			return err
		}
		if parent.CoachType != "main" {
			return ErrCoachParentMustBeMain
		}
		return nil
	}
	return nil
}

func ensureCoachHasNoSubCoachesTx(tx *sql.Tx, coachID int64) error {
	var count int
	if err := tx.QueryRow(`
		SELECT COUNT(*)
		FROM coach_profiles
		WHERE parent_coach_id = ?
	`, coachID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrCoachHasSubCoaches
	}
	return nil
}

func (a *App) ensureCoachCanChangeType(tx *sql.Tx, coach User) error {
	if normalizeCoachType(coach.CoachType) == "main" {
		return nil
	}
	return ensureCoachHasNoSubCoachesTx(tx, coach.ID)
}

func (a *App) findUserByEmail(email string) (*User, string, error) {
	row := a.queryRowDB(a.dbQuery(`
		SELECT id, email, name, password_hash, email_verified_at, created_at
		FROM users
		WHERE email = ?
	`), email)

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
	row := a.queryRowDB(`
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
	divisions, err := a.divisionsForUser(user.ID)
	if err != nil {
		return nil, err
	}
	fillUserDivisions(&user, divisions)
	return &user, nil
}

func (a *App) listUsers() ([]User, error) {
	rows, err := a.queryDB(`
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
		divisions, err := a.divisionsForUser(users[i].ID)
		if err != nil {
			return nil, err
		}
		fillUserDivisions(&users[i], divisions)
	}

	return users, nil
}

func (a *App) listUsersVisibleToManager(current *User) ([]User, error) {
	users, err := a.listUsers()
	if err != nil {
		return nil, err
	}
	if current == nil || canViewAllDivisions(current) {
		return users, nil
	}
	filtered := make([]User, 0, len(users))
	for _, user := range users {
		if user.ID == current.ID {
			filtered = append(filtered, user)
			continue
		}
		if divisionSlicesOverlap(current.DivisionIDs, user.DivisionIDs) {
			filtered = append(filtered, user)
		}
	}
	return filtered, nil
}

func (a *App) listCoachUsersDetailed(includeInactive bool) ([]User, error) {
	return a.listCoachUsersDetailedByDivisionIDs(nil, includeInactive)
}

func (a *App) listCoachUsersDetailedByDivisionIDs(divisionIDs []int64, includeInactive bool) ([]User, error) {
	query := `
		SELECT DISTINCT
			u.id,
			u.email,
			u.name,
			u.email_verified_at,
			u.created_at,
			COALESCE(cp.phone, ''),
			COALESCE(cp.address, ''),
			COALESCE(cp.specialties, ''),
			COALESCE(cp.notes, ''),
			COALESCE(cp.active, 1),
			COALESCE(cp.coach_type, 'main'),
			COALESCE(cp.parent_coach_id, 0),
			COALESCE(parent.name, '')
		FROM users u
		JOIN user_roles ur
			ON ur.user_id = u.id
		JOIN roles r
			ON r.id = ur.role_id
		LEFT JOIN user_divisions ud
			ON ud.user_id = u.id
		LEFT JOIN coach_profiles cp
			ON cp.user_id = u.id
		LEFT JOIN users parent
			ON parent.id = cp.parent_coach_id
		WHERE r.name = 'coach'
	`
	args := []any{}
	if placeholders, scopeArgs := int64ScopePlaceholders(divisionIDs); placeholders != "" {
		query += ` AND ud.division_id IN (` + placeholders + `)`
		args = append(args, scopeArgs...)
	}
	if !includeInactive {
		query += ` AND COALESCE(cp.active, 1) = 1`
	}
	query += `
		ORDER BY
			COALESCE(cp.active, 1) DESC,
			u.name ,
			u.id ASC
	`

	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var coaches []User
	for rows.Next() {
		var coach User
		var verifiedAt sql.NullTime
		var active int
		if err := rows.Scan(
			&coach.ID,
			&coach.Email,
			&coach.Name,
			&verifiedAt,
			&coach.CreatedAt,
			&coach.Phone,
			&coach.Address,
			&coach.Specialties,
			&coach.Notes,
			&active,
			&coach.CoachType,
			&coach.ParentCoachID,
			&coach.ParentCoachName,
		); err != nil {
			return nil, err
		}
		coach.Verified = verifiedAt.Valid
		coach.Active = active == 1
		if coach.CoachType == "" {
			coach.CoachType = "main"
		}
		coach.Roles = []string{"coach"}
		coaches = append(coaches, coach)
	}

	return coaches, rows.Err()
}

func (a *App) findCoachByID(userID int64) (*User, error) {
	coaches, err := a.listCoachUsersDetailed(true)
	if err != nil {
		return nil, err
	}
	for i := range coaches {
		if coaches[i].ID == userID {
			return &coaches[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (a *App) listCoachUsers() ([]User, error) {
	return a.listCoachUsersDetailed(false)
}

func (a *App) listCoachAttendanceRecords(attendanceDate string) ([]CoachAttendanceRecord, error) {
	return a.listCoachAttendanceRecordsByUserIDs(attendanceDate, nil)
}

func (a *App) listCoachAttendanceRecordsByUserIDs(attendanceDate string, userIDs []int64) ([]CoachAttendanceRecord, error) {
	query := `
		SELECT
			id,
			user_id,
			attendance_date,
			status,
			note,
			COALESCE(recorded_by_user_id, 0),
			recorded_at,
			updated_at
		FROM coach_attendance_records
		WHERE attendance_date = ?
	`
	args := []any{attendanceDate}
	if placeholders, scopedArgs := int64ScopePlaceholders(userIDs); placeholders != "" {
		query += ` AND user_id IN (` + placeholders + `)`
		args = append(args, scopedArgs...)
	}
	query += ` ORDER BY user_id ASC, id ASC`
	rows, err := a.queryDB(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []CoachAttendanceRecord
	for rows.Next() {
		var record CoachAttendanceRecord
		if err := rows.Scan(
			&record.ID,
			&record.UserID,
			&record.AttendanceDate,
			&record.Status,
			&record.Note,
			&record.RecordedByUserID,
			&record.RecordedAt,
			&record.UpdatedAt,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (a *App) replaceCoachAttendanceRecords(attendanceDate string, records []CoachAttendanceRecord) error {
	return a.replaceCoachAttendanceRecordsByUserIDs(attendanceDate, nil, records)
}

func (a *App) replaceCoachAttendanceRecordsByUserIDs(attendanceDate string, userIDs []int64, records []CoachAttendanceRecord) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	deleteQuery := `DELETE FROM coach_attendance_records WHERE attendance_date = ?`
	deleteArgs := []any{attendanceDate}
	if placeholders, scopedArgs := int64ScopePlaceholders(userIDs); placeholders != "" {
		deleteQuery += ` AND user_id IN (` + placeholders + `)`
		deleteArgs = append(deleteArgs, scopedArgs...)
	}
	if _, err := tx.Exec(deleteQuery, deleteArgs...); err != nil {
		return err
	}

	now := time.Now().UTC()
	for _, record := range records {
		if _, err := tx.Exec(`
			INSERT INTO coach_attendance_records (
				user_id,
				attendance_date,
				status,
				note,
				recorded_by_user_id,
				recorded_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`,
			record.UserID,
			record.AttendanceDate,
			record.Status,
			record.Note,
			record.RecordedByUserID,
			now,
			now,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) upsertCoachProfile(userID int64, coach User) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := upsertCoachProfileTx(tx, userID, coach); err != nil {
		return err
	}

	return tx.Commit()
}

func upsertCoachProfileTx(tx *sql.Tx, userID int64, coach User) error {
	now := time.Now().UTC()
	coachType := normalizeCoachType(coach.CoachType)
	parentCoachID := coach.ParentCoachID
	if coachType == "main" {
		parentCoachID = 0
	}
	_, err := a.execTxDB(tx, `
		INSERT INTO coach_profiles (
			user_id,
			phone,
			address,
			specialties,
			notes,
			active,
			coach_type,
			parent_coach_id,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			phone = excluded.phone,
			address = excluded.address,
			specialties = excluded.specialties,
			notes = excluded.notes,
			active = excluded.active,
			coach_type = excluded.coach_type,
			parent_coach_id = excluded.parent_coach_id,
			updated_at = excluded.updated_at
	`,
		userID,
		coach.Phone,
		coach.Address,
		coach.Specialties,
		coach.Notes,
		boolToInt(coach.Active),
		coachType,
		nullInt64(parentCoachID),
		now,
		now,
	)
	return err
}

func (a *App) rolesForUser(userID int64) ([]string, error) {
	rows, err := a.queryDB(a.dbQuery(`
		SELECT r.name
		FROM roles r
		JOIN user_roles ur ON ur.role_id = r.id
		WHERE ur.user_id = ?
		ORDER BY r.name ASC
	`), userID)
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

func (a *App) rolesForUserTx(tx *sql.Tx, userID int64) ([]string, error) {
	rows, err := a.queryTxDB(tx, `
		SELECT r.name
		FROM users u
		JOIN user_roles ur ON ur.user_id = u.id
		JOIN roles r ON r.id = ur.role_id
		WHERE u.id = ?
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(roles) == 0 {
		var exists int
		if err := a.queryRowTxDB(tx, `SELECT COUNT(*) FROM users WHERE id = ?`, userID).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			return nil, sql.ErrNoRows
		}
	}
	return roles, nil
}

func (a *App) userHasRoleTx(tx *sql.Tx, userID int64, roleName string) (bool, error) {
	var count int
	err := a.queryRowTxDB(tx, `
		SELECT COUNT(*)
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = ? AND r.name = ?
	`, userID, roleName).Scan(&count)
	return count > 0, err
}

func (a *App) listRoles() ([]Role, error) {
	rows, err := a.queryDB(`
		SELECT r.id, r.name, COUNT(ur.user_id)
		FROM roles r
		LEFT JOIN user_roles ur ON ur.role_id = r.id
		GROUP BY r.id, r.name
		ORDER BY r.name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []Role
	for rows.Next() {
		var role Role
		if err := rows.Scan(&role.ID, &role.Name, &role.UserCount); err != nil {
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

func (a *App) findRoleByID(roleID int64) (*Role, error) {
	var role Role
	if err := a.queryRowDB(`
		SELECT r.id, r.name, COUNT(ur.user_id)
		FROM roles r
		LEFT JOIN user_roles ur ON ur.role_id = r.id
		WHERE r.id = ?
		GROUP BY r.id, r.name
	`, roleID).Scan(&role.ID, &role.Name, &role.UserCount); err != nil {
		return nil, err
	}
	role.System = isSystemRole(role.Name)
	permissions, err := a.permissionsForRole(role.ID)
	if err != nil {
		return nil, err
	}
	role.Permissions = permissions
	return &role, nil
}

func (a *App) userHasRole(userID int64, roleName string) (bool, error) {
	var count int
	err := a.queryRowDB(`
		SELECT COUNT(*)
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = ? AND r.name = ?
	`, userID, roleName).Scan(&count)
	return count > 0, err
}

func (a *App) permissionsForUser(userID int64) ([]string, error) {
	rows, err := a.queryDB(`
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
	rows, err := a.queryDB(`
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

	var roleID int64

	if a.runtimeConfig.DBDriver == databaseDriverPostgres {
		if err := a.queryRowTxDB(
			tx,
			`INSERT INTO roles (name) VALUES (?) RETURNING id`,
			name,
		).Scan(&roleID); err != nil {
			return err
		}
	} else {
		result, err := a.execTxDB(
			tx,
			`INSERT INTO roles (name) VALUES (?)`,
			name,
		)
		if err != nil {
			return err
		}

		roleID, err = result.LastInsertId()
		if err != nil {
			return err
		}
	}

	for _, permission := range permissions {
		if _, err := a.execTxDB(
			tx,
			`INSERT INTO role_permissions (role_id, permission) VALUES (?, ?)`,
			roleID,
			permission,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) updateRole(
	roleID int64,
	name string,
	permissions []string,
) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var currentName string
	if err := a.queryRowTxDB(
		tx,
		`SELECT name FROM roles WHERE id = ?`,
		roleID,
	).Scan(&currentName); err != nil {
		return err
	}

	if isSystemRole(currentName) {
		return ErrSystemRoleProtected
	}

	if _, err := a.execTxDB(
		tx,
		`UPDATE roles SET name = ? WHERE id = ?`,
		name,
		roleID,
	); err != nil {
		return err
	}

	if _, err := a.execTxDB(
		tx,
		`DELETE FROM role_permissions WHERE role_id = ?`,
		roleID,
	); err != nil {
		return err
	}

	for _, permission := range permissions {
		if _, err := a.execTxDB(
			tx,
			`INSERT INTO role_permissions (role_id, permission) VALUES (?, ?)`,
			roleID,
			permission,
		); err != nil {
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
	if err := a.queryRowTxDB(
		tx,
		`SELECT name FROM roles WHERE id = ?`,
		roleID,
	).Scan(&roleName); err != nil {
		return err
	}

	if isSystemRole(roleName) {
		return ErrSystemRoleProtected
	}

	var userCount int
	if err := a.queryRowTxDB(
		tx,
		`SELECT COUNT(*) FROM user_roles WHERE role_id = ?`,
		roleID,
	).Scan(&userCount); err != nil {
		return err
	}

	if userCount > 0 {
		return ErrRoleAssigned
	}

	if _, err := a.execTxDB(
		tx,
		`DELETE FROM role_permissions WHERE role_id = ?`,
		roleID,
	); err != nil {
		return err
	}

	if _, err := a.execTxDB(
		tx,
		`DELETE FROM roles WHERE id = ?`,
		roleID,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) replaceUserRoles(
	userID int64,
	roles []string,
) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := a.execTxDB(
		tx,
		`DELETE FROM user_roles WHERE user_id = ?`,
		userID,
	); err != nil {
		return err
	}

	for _, role := range roles {
		var roleID int64
		if err := a.queryRowTxDB(
			tx,
			`SELECT id FROM roles WHERE name = ?`,
			role,
		).Scan(&roleID); err != nil {
			return err
		}

		if _, err := a.execTxDB(
			tx,
			`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`,
			userID,
			roleID,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *App) deleteSessionsForUser(userID int64) error {
	_, err := a.execDB(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

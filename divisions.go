package main

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	divisionCodeSports    = "SPORTS"
	divisionCodeKEC       = "KEC"
	divisionCodeChess     = "CHESS"
	divisionCodeCorporate = "CORPORATE"
	divisionScopeAll      = "all"
)

type Division struct {
	ID          int64
	Code        string
	Slug        string
	Name        string
	Description string
	Active      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type DivisionScopeOption struct {
	Key        string
	Label      string
	DivisionID int64
	Division   *Division
}

func canViewAllDivisions(user *User) bool {
	if user == nil {
		return false
	}
	if containsRole(user.Roles, "superadmin") {
		return true
	}
	if containsPermission(user.Permissions, "finance.consolidated") {
		return true
	}
	return false
}

func userCanSwitchOperationalDivision(user *User) bool {
	if user == nil {
		return false
	}
	if canViewAllDivisions(user) {
		return true
	}
	return len(user.DivisionIDs) > 1
}

func userPrimaryDivision(user *User) *Division {
	if user == nil || len(user.Divisions) == 0 {
		return nil
	}
	division := user.Divisions[0]
	return &division
}

func userHasDivisionCode(user *User, code string) bool {
	if user == nil {
		return false
	}
	for _, divisionCode := range user.DivisionCodes {
		if strings.EqualFold(strings.TrimSpace(divisionCode), strings.TrimSpace(code)) {
			return true
		}
	}
	return false
}

func userCanAccessSportsSilo(user *User) bool {
	return canViewAllDivisions(user) || userHasDivisionCode(user, divisionCodeSports)
}

func defaultDivisionSeeds() []Division {
	return []Division{
		{Code: divisionCodeSports, Slug: "sports", Name: "Indoor Sports", Description: "Indoor sports operations and programmes.", Active: true},
		{Code: divisionCodeKEC, Slug: "kec", Name: "Kids Education Center", Description: "Kids education programmes and administration.", Active: true},
		{Code: divisionCodeChess, Slug: "chess", Name: "Chess Academy", Description: "Chess academy and coaching operations.", Active: true},
		{Code: divisionCodeCorporate, Slug: "corporate", Name: "Mekmaa Corporate / Shared", Description: "Shared or corporate-wide finance and operations.", Active: true},
	}
}

func seedDivisions(db *sql.DB) error {
	now := time.Now().UTC()
	for _, division := range defaultDivisionSeeds() {
		_, err := db.Exec(`
			INSERT INTO divisions (code, slug, name, description, active, created_at, updated_at)
			SELECT ?, ?, ?, ?, ?, ?, ?
			WHERE NOT EXISTS (
				SELECT 1
				FROM divisions
				WHERE UPPER(code) = UPPER(?)
			)
		`, division.Code, division.Slug, division.Name, division.Description, boolToInt(division.Active), now, now, division.Code)
		if err != nil {
			return err
		}
		if _, err := db.Exec(`
			UPDATE divisions
			SET slug = CASE WHEN TRIM(COALESCE(slug, '')) = '' THEN ? ELSE slug END,
			    name = CASE WHEN TRIM(COALESCE(name, '')) = '' THEN ? ELSE name END,
			    description = CASE WHEN TRIM(COALESCE(description, '')) = '' THEN ? ELSE description END,
			    updated_at = ?
			WHERE UPPER(code) = UPPER(?)
		`, division.Slug, division.Name, division.Description, now, division.Code); err != nil {
			return err
		}
	}
	return nil
}

func migrateDivisions(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS divisions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			code TEXT NOT NULL UNIQUE,
			slug TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS user_divisions (
			user_id INTEGER NOT NULL,
			division_id INTEGER NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (user_id, division_id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY (division_id) REFERENCES divisions(id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_divisions_division_user ON user_divisions(division_id, user_id)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	for _, migration := range []struct {
		table  string
		column string
		stmt   string
	}{
		{"training_programs", "division_id", `ALTER TABLE training_programs ADD COLUMN division_id INTEGER`},
		{"finance_transactions", "division_id", `ALTER TABLE finance_transactions ADD COLUMN division_id INTEGER`},
	} {
		exists, err := tableHasColumn(db, migration.table, migration.column)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := db.Exec(migration.stmt); err != nil {
				return err
			}
		}
	}
	if err := seedDivisions(db); err != nil {
		return err
	}

	sportsID, err := divisionIDByCode(db, divisionCodeSports)
	if err != nil {
		return err
	}
	corporateID, err := divisionIDByCode(db, divisionCodeCorporate)
	if err != nil {
		return err
	}
	if _, err := db.Exec(`
		UPDATE training_programs
		SET division_id = ?
		WHERE division_id IS NULL OR division_id <= 0
	`, sportsID); err != nil {
		return err
	}
	hasFinanceReferenceType, err := tableHasColumn(db, "finance_transactions", "reference_type")
	if err != nil {
		return err
	}
	hasFinanceSourceType, err := tableHasColumn(db, "finance_transactions", "source_type")
	if err != nil {
		return err
	}
	hasFinanceReferenceID, err := tableHasColumn(db, "finance_transactions", "reference_id")
	if err != nil {
		return err
	}
	hasFinanceSourceID, err := tableHasColumn(db, "finance_transactions", "source_id")
	if err != nil {
		return err
	}
	if hasFinanceReferenceType && hasFinanceSourceType && hasFinanceReferenceID && hasFinanceSourceID {
		if _, err := db.Exec(`
			UPDATE finance_transactions
			SET division_id = COALESCE(
				(
					SELECT tp.division_id
					FROM student_enrollments se
					JOIN training_programs tp ON tp.id = se.training_program_id
					WHERE finance_transactions.reference_type = 'student_enrollment'
					  AND se.id = finance_transactions.reference_id
				),
				(
					SELECT tp.division_id
					FROM admissions adm
					JOIN admission_training_programs atp ON atp.admission_id = adm.id
					JOIN training_programs tp ON tp.id = atp.training_program_id
					WHERE finance_transactions.reference_type = 'admission'
					  AND adm.id = finance_transactions.reference_id
					ORDER BY atp.created_at ASC, tp.id ASC
					LIMIT 1
				),
				(
					SELECT tp.division_id
					FROM student_monthly_payments smp
					JOIN student_enrollments se ON se.id = smp.enrollment_id
					JOIN training_programs tp ON tp.id = se.training_program_id
					WHERE finance_transactions.source_type = 'student_monthly_payment'
					  AND smp.id = finance_transactions.source_id
				),
				CASE
					WHEN finance_transactions.reference_type IN ('space_schedule', 'booking_referral', 'booking_payment_collection')
						OR finance_transactions.source_type IN ('space_schedule', 'booking_referral', 'booking_payment_collection', 'finance_transfer')
					THEN ?
					ELSE ?
				END
			)
			WHERE division_id IS NULL OR division_id <= 0
		`, sportsID, corporateID); err != nil {
			return err
		}
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_training_programs_division_active ON training_programs(division_id, active, sort_order, id)`,
		`CREATE INDEX IF NOT EXISTS idx_finance_transactions_division_recorded ON finance_transactions(division_id, recorded_at DESC, id DESC)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func divisionIDByCode(db *sql.DB, code string) (int64, error) {
	var divisionID int64
	if err := db.QueryRow(`SELECT id FROM divisions WHERE UPPER(code) = UPPER(?)`, strings.TrimSpace(code)).Scan(&divisionID); err != nil {
		return 0, err
	}
	return divisionID, nil
}

func (a *App) listDivisions(activeOnly bool) ([]Division, error) {
	query := `
		SELECT id, code, slug, name, COALESCE(description, ''), COALESCE(active, 1), created_at, updated_at
		FROM divisions
	`
	if activeOnly {
		query += ` WHERE COALESCE(active, 1) = 1`
	}
	query += ` ORDER BY CASE WHEN UPPER(code) = 'CORPORATE' THEN 1 ELSE 0 END ASC, name ASC, id ASC`
	rows, err := a.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var divisions []Division
	for rows.Next() {
		var division Division
		var active int
		if err := rows.Scan(&division.ID, &division.Code, &division.Slug, &division.Name, &division.Description, &active, &division.CreatedAt, &division.UpdatedAt); err != nil {
			return nil, err
		}
		division.Active = active == 1
		divisions = append(divisions, division)
	}
	return divisions, rows.Err()
}

func (a *App) findDivisionByID(divisionID int64) (*Division, error) {
	var division Division
	var active int
	if err := a.db.QueryRow(`
		SELECT id, code, slug, name, COALESCE(description, ''), COALESCE(active, 1), created_at, updated_at
		FROM divisions
		WHERE id = ?
	`, divisionID).Scan(&division.ID, &division.Code, &division.Slug, &division.Name, &division.Description, &active, &division.CreatedAt, &division.UpdatedAt); err != nil {
		return nil, err
	}
	division.Active = active == 1
	return &division, nil
}

func (a *App) findDivisionBySlugOrCode(value string) (*Division, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, sql.ErrNoRows
	}
	var division Division
	var active int
	if err := a.db.QueryRow(`
		SELECT id, code, slug, name, COALESCE(description, ''), COALESCE(active, 1), created_at, updated_at
		FROM divisions
		WHERE LOWER(slug) = LOWER(?) OR UPPER(code) = UPPER(?)
		ORDER BY id ASC
		LIMIT 1
	`, value, value).Scan(&division.ID, &division.Code, &division.Slug, &division.Name, &division.Description, &active, &division.CreatedAt, &division.UpdatedAt); err != nil {
		return nil, err
	}
	division.Active = active == 1
	return &division, nil
}

func (a *App) divisionsForUser(userID int64) ([]Division, error) {
	rows, err := a.db.Query(`
		SELECT d.id, d.code, d.slug, d.name, COALESCE(d.description, ''), COALESCE(d.active, 1), d.created_at, d.updated_at
		FROM divisions d
		JOIN user_divisions ud ON ud.division_id = d.id
		WHERE ud.user_id = ?
		ORDER BY d.name ASC, d.id ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var divisions []Division
	for rows.Next() {
		var division Division
		var active int
		if err := rows.Scan(&division.ID, &division.Code, &division.Slug, &division.Name, &division.Description, &active, &division.CreatedAt, &division.UpdatedAt); err != nil {
			return nil, err
		}
		division.Active = active == 1
		divisions = append(divisions, division)
	}
	return divisions, rows.Err()
}

func (a *App) replaceUserDivisions(userID int64, divisionIDs []int64) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM user_divisions WHERE user_id = ?`, userID); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, divisionID := range divisionIDs {
		if divisionID <= 0 {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO user_divisions (user_id, division_id, created_at, updated_at)
			VALUES (?, ?, ?, ?)
		`, userID, divisionID, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func fillUserDivisions(user *User, divisions []Division) {
	if user == nil {
		return
	}
	user.Divisions = append(user.Divisions[:0], divisions...)
	user.DivisionIDs = user.DivisionIDs[:0]
	user.DivisionCodes = user.DivisionCodes[:0]
	for _, division := range divisions {
		user.DivisionIDs = append(user.DivisionIDs, division.ID)
		user.DivisionCodes = append(user.DivisionCodes, division.Code)
	}
}

func normalizeDivisionIDs(values []string) []int64 {
	return normalizePositiveIDs(values)
}

func userCanAccessDivision(user *User, divisionID int64) bool {
	if user == nil {
		return false
	}
	if canViewAllDivisions(user) {
		return true
	}
	if divisionID <= 0 {
		return false
	}
	for _, allowed := range user.DivisionIDs {
		if allowed == divisionID {
			return true
		}
	}
	return false
}

func userDivisionScope(user *User) string {
	if user == nil {
		return ""
	}
	if canViewAllDivisions(user) {
		return divisionScopeAll
	}
	if primary := userPrimaryDivision(user); primary != nil {
		return primary.Slug
	}
	return ""
}

func (a *App) accessibleDivisionsForUser(user *User, includeInactive bool) ([]Division, error) {
	if user == nil {
		return nil, nil
	}
	if canViewAllDivisions(user) {
		return a.listDivisions(!includeInactive)
	}
	divisions := user.Divisions
	if len(divisions) == 0 {
		divisions = nil
		loaded, err := a.divisionsForUser(user.ID)
		if err != nil {
			return nil, err
		}
		divisions = loaded
	}
	if includeInactive {
		return append([]Division(nil), divisions...), nil
	}
	filtered := make([]Division, 0, len(divisions))
	for _, division := range divisions {
		if division.Active {
			filtered = append(filtered, division)
		}
	}
	return filtered, nil
}

func (a *App) accessibleDivisionIDsForUser(user *User, includeInactive bool) ([]int64, error) {
	divisions, err := a.accessibleDivisionsForUser(user, includeInactive)
	if err != nil {
		return nil, err
	}
	return divisionIDsFromDivisions(divisions), nil
}

func (a *App) requireDivisionAccessForDivision(w http.ResponseWriter, r *http.Request, user *User, divisionID int64) bool {
	if user == nil {
		return true
	}
	if userCanAccessDivision(user, divisionID) {
		return true
	}
	a.writeDivisionForbidden(w, r, user)
	return false
}

func (a *App) validateAssignableDivisionIDs(currentUser *User, rawIDs []int64) ([]Division, error) {
	if len(rawIDs) == 0 {
		return nil, nil
	}
	authorizedIDs, err := a.accessibleDivisionIDsForUser(currentUser, false)
	if err != nil {
		return nil, err
	}
	authorizedSet := map[int64]struct{}{}
	for _, id := range authorizedIDs {
		authorizedSet[id] = struct{}{}
	}
	validated := make([]Division, 0, len(rawIDs))
	for _, divisionID := range rawIDs {
		division, err := a.findDivisionByID(divisionID)
		if err != nil {
			return nil, err
		}
		if !division.Active {
			return nil, errors.New("inactive divisions cannot be assigned")
		}
		if !canViewAllDivisions(currentUser) {
			if _, ok := authorizedSet[divisionID]; !ok {
				return nil, ErrForbiddenDivision
			}
		}
		validated = append(validated, *division)
	}
	return validated, nil
}

func (a *App) resolveAuthorizedDivisionFromRequest(r *http.Request, allowAll bool) (*Division, error) {
	return a.authorizedDivisionFilter(r, r.URL.Query().Get("division"), allowAll)
}

func (a *App) programDivisionAllowed(user *User, program *TrainingProgram) bool {
	if program == nil {
		return false
	}
	return userCanAccessDivision(user, program.DivisionID)
}

func (a *App) financeDivisionAllowed(user *User, transaction *FinanceTransaction) bool {
	if transaction == nil {
		return false
	}
	return userCanAccessDivision(user, transaction.DivisionID)
}

func (a *App) admissionVisibleToUser(user *User, admission *Admission) bool {
	if admission == nil {
		return false
	}
	if canViewAllDivisions(user) {
		return true
	}
	for _, divisionID := range admission.DivisionIDs {
		if userCanAccessDivision(user, divisionID) {
			return true
		}
	}
	return false
}

func (a *App) authorizedDivisionFilter(r *http.Request, raw string, allowAll bool) (*Division, error) {
	user, _ := a.currentUser(r.Context())
	value := strings.TrimSpace(raw)
	if value == "" || strings.EqualFold(value, divisionScopeAll) {
		if allowAll && user != nil && canViewAllDivisions(user) {
			return nil, nil
		}
		if user != nil && len(user.DivisionIDs) == 1 {
			return a.findDivisionByID(user.DivisionIDs[0])
		}
		if userCanSwitchOperationalDivision(user) && user != nil && len(user.DivisionIDs) > 1 {
			return nil, nil
		}
		return nil, ErrForbiddenDivision
	}
	division, err := a.findDivisionBySlugOrCode(value)
	if err != nil {
		return nil, err
	}
	if !userCanAccessDivision(user, division.ID) {
		return nil, ErrForbiddenDivision
	}
	return division, nil
}

var ErrForbiddenDivision = errors.New("forbidden division")

func (a *App) writeDivisionForbidden(w http.ResponseWriter, r *http.Request, user *User) {
	if len(a.templates) == 0 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	data := a.newTemplateData(w, r, user)
	data.Title = "Forbidden"
	data.Description = "You do not have access to this division."
	data.Error = "You do not have access to this division."
	a.render(w, "forbidden", data, http.StatusForbidden)
}

func divisionIDsFromDivisions(divisions []Division) []int64 {
	ids := make([]int64, 0, len(divisions))
	for _, division := range divisions {
		ids = append(ids, division.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func financeDivisionIDForEntryTx(tx *sql.Tx, entry financeTransactionCreate) (int64, error) {
	if entry.DivisionID > 0 {
		return entry.DivisionID, nil
	}
	resolveProgramDivision := func(query string, args ...any) (int64, error) {
		var divisionID int64
		err := tx.QueryRow(query, args...).Scan(&divisionID)
		if err == nil && divisionID > 0 {
			return divisionID, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		return 0, nil
	}
	switch {
	case entry.ReferenceType == "student_enrollment" && entry.ReferenceID > 0:
		return resolveProgramDivision(`
			SELECT COALESCE(tp.division_id, 0)
			FROM student_enrollments se
			JOIN training_programs tp ON tp.id = se.training_program_id
			WHERE se.id = ?
		`, entry.ReferenceID)
	case entry.ReferenceType == "admission" && entry.ReferenceID > 0:
		return resolveProgramDivision(`
			SELECT COALESCE(tp.division_id, 0)
			FROM admission_training_programs atp
			JOIN training_programs tp ON tp.id = atp.training_program_id
			WHERE atp.admission_id = ?
			ORDER BY atp.created_at ASC, tp.id ASC
			LIMIT 1
		`, entry.ReferenceID)
	case entry.SourceType == "student_monthly_payment" && entry.SourceID > 0:
		return resolveProgramDivision(`
			SELECT COALESCE(tp.division_id, 0)
			FROM student_monthly_payments smp
			JOIN student_enrollments se ON se.id = smp.enrollment_id
			JOIN training_programs tp ON tp.id = se.training_program_id
			WHERE smp.id = ?
		`, entry.SourceID)
	}
	code := divisionCodeSports
	if entry.ReferenceType == "finance_transfer" || entry.SourceType == "finance_transfer" {
		code = divisionCodeCorporate
	}
	return divisionIDByCodeTx(tx, code)
}

func divisionIDByCodeTx(tx *sql.Tx, code string) (int64, error) {
	var divisionID int64
	if err := tx.QueryRow(`SELECT id FROM divisions WHERE UPPER(code) = UPPER(?)`, strings.TrimSpace(code)).Scan(&divisionID); err != nil {
		return 0, err
	}
	return divisionID, nil
}

package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	SalaryTypeHourly     = "hourly"
	SalaryTypeWeekly     = "weekly"
	SalaryTypeMonthly    = "monthly"
	SalaryTypePerStudent = "per_student"
)

const (
	SalaryStudentBasisActiveEnrollment = "active_enrollment"
	SalaryStudentBasisGroupMembership  = "group_membership"
	SalaryStudentBasisAttendance       = "attendance"
)

type StaffSalaryProfile struct {
	ID int64

	UserID    int64
	UserName  string
	UserEmail string

	DivisionID   int64
	DivisionCode string
	DivisionName string

	TrainingProgramID   int64
	TrainingProgramName string

	CompensationType string
	Rate             float64
	StudentBasis     string

	EffectiveFrom string
	EffectiveTo   string

	Active bool
	Notes  string

	CreatedByUserID int64
	UpdatedByUserID int64

	CreatedAt time.Time
	UpdatedAt time.Time
}

func normalizeSalaryType(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validSalaryType(value string) bool {
	switch normalizeSalaryType(value) {
	case SalaryTypeHourly,
		SalaryTypeWeekly,
		SalaryTypeMonthly,
		SalaryTypePerStudent:
		return true
	default:
		return false
	}
}

func normalizeSalaryStudentBasis(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))

	if value == "" {
		return SalaryStudentBasisActiveEnrollment
	}

	return value
}

func validSalaryStudentBasis(value string) bool {
	switch normalizeSalaryStudentBasis(value) {
	case SalaryStudentBasisActiveEnrollment,
		SalaryStudentBasisGroupMembership,
		SalaryStudentBasisAttendance:
		return true
	default:
		return false
	}
}

func salaryTypeLabel(value string) string {
	switch normalizeSalaryType(value) {
	case SalaryTypeHourly:
		return "Per hour"
	case SalaryTypeWeekly:
		return "Per week"
	case SalaryTypeMonthly:
		return "Per month"
	case SalaryTypePerStudent:
		return "Per student"
	default:
		return strings.TrimSpace(value)
	}
}

func salaryStudentBasisLabel(value string) string {
	switch normalizeSalaryStudentBasis(value) {
	case SalaryStudentBasisActiveEnrollment:
		return "Active enrolled students"
	case SalaryStudentBasisGroupMembership:
		return "Group / class membership"
	case SalaryStudentBasisAttendance:
		return "Students attended"
	default:
		return strings.TrimSpace(value)
	}
}

func validateStaffSalaryProfile(profile StaffSalaryProfile) error {
	if profile.UserID <= 0 {
		return errors.New("staff member is required")
	}

	profile.CompensationType =
		normalizeSalaryType(profile.CompensationType)

	if !validSalaryType(profile.CompensationType) {
		return errors.New("invalid salary type")
	}

	if profile.Rate < 0 {
		return errors.New("salary rate cannot be negative")
	}

	if strings.TrimSpace(profile.EffectiveFrom) == "" {
		return errors.New("effective from date is required")
	}

	from, err := time.Parse(
		"2006-01-02",
		strings.TrimSpace(profile.EffectiveFrom),
	)
	if err != nil {
		return errors.New("invalid effective from date")
	}

	if strings.TrimSpace(profile.EffectiveTo) != "" {
		to, err := time.Parse(
			"2006-01-02",
			strings.TrimSpace(profile.EffectiveTo),
		)
		if err != nil {
			return errors.New("invalid effective to date")
		}

		if to.Before(from) {
			return errors.New(
				"effective to date cannot be before effective from date",
			)
		}
	}

	if profile.DivisionID < 0 {
		return errors.New("invalid division")
	}

	if profile.TrainingProgramID < 0 {
		return errors.New("invalid training programme")
	}

	if profile.CompensationType == SalaryTypePerStudent {
		if !validSalaryStudentBasis(profile.StudentBasis) {
			return errors.New(
				"invalid per-student calculation basis",
			)
		}
	}

	return nil
}

func scanStaffSalaryProfile(
	scanner interface {
		Scan(dest ...any) error
	},
	profile *StaffSalaryProfile,
) error {
	var active int

	return scanner.Scan(
		&profile.ID,
		&profile.UserID,
		&profile.UserName,
		&profile.UserEmail,
		&profile.DivisionID,
		&profile.DivisionCode,
		&profile.DivisionName,
		&profile.TrainingProgramID,
		&profile.TrainingProgramName,
		&profile.CompensationType,
		&profile.Rate,
		&profile.StudentBasis,
		&profile.EffectiveFrom,
		&profile.EffectiveTo,
		&active,
		&profile.Notes,
		&profile.CreatedByUserID,
		&profile.UpdatedByUserID,
		&profile.CreatedAt,
		&profile.UpdatedAt,
	)
}

const staffSalaryProfileSelect = `
	SELECT
		sp.id,
		sp.user_id,
		COALESCE(u.name, ''),
		COALESCE(u.email, ''),
		COALESCE(sp.division_id, 0),
		COALESCE(d.code, ''),
		COALESCE(d.name, ''),
		COALESCE(sp.training_program_id, 0),
		COALESCE(tp.name, ''),
		sp.compensation_type,
		COALESCE(sp.rate, 0),
		COALESCE(sp.student_basis, 'active_enrollment'),
		COALESCE(sp.effective_from, ''),
		COALESCE(sp.effective_to, ''),
		COALESCE(sp.active, 1),
		COALESCE(sp.notes, ''),
		COALESCE(sp.created_by_user_id, 0),
		COALESCE(sp.updated_by_user_id, 0),
		sp.created_at,
		sp.updated_at
	FROM staff_salary_profiles sp
	JOIN users u
		ON u.id = sp.user_id
	LEFT JOIN divisions d
		ON d.id = sp.division_id
	LEFT JOIN training_programs tp
		ON tp.id = sp.training_program_id
`

func (a *App) listStaffSalaryProfiles() (
	[]StaffSalaryProfile,
	error,
) {
	rows, err := a.queryDB(
		staffSalaryProfileSelect + `
		ORDER BY
			LOWER(COALESCE(u.name, '')) ASC,
			sp.active DESC,
			sp.effective_from DESC,
			sp.id DESC
	`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	profiles := make([]StaffSalaryProfile, 0)

	for rows.Next() {
		var profile StaffSalaryProfile
		var active int

		err := rows.Scan(
			&profile.ID,
			&profile.UserID,
			&profile.UserName,
			&profile.UserEmail,
			&profile.DivisionID,
			&profile.DivisionCode,
			&profile.DivisionName,
			&profile.TrainingProgramID,
			&profile.TrainingProgramName,
			&profile.CompensationType,
			&profile.Rate,
			&profile.StudentBasis,
			&profile.EffectiveFrom,
			&profile.EffectiveTo,
			&active,
			&profile.Notes,
			&profile.CreatedByUserID,
			&profile.UpdatedByUserID,
			&profile.CreatedAt,
			&profile.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}

		profile.Active = active == 1
		profiles = append(profiles, profile)
	}

	return profiles, rows.Err()
}

func (a *App) findStaffSalaryProfileByID(
	profileID int64,
) (*StaffSalaryProfile, error) {
	if profileID <= 0 {
		return nil, sql.ErrNoRows
	}

	row := a.queryRowDB(
		staffSalaryProfileSelect+`
		WHERE sp.id = ?
	`,
		profileID,
	)

	var profile StaffSalaryProfile
	var active int

	err := row.Scan(
		&profile.ID,
		&profile.UserID,
		&profile.UserName,
		&profile.UserEmail,
		&profile.DivisionID,
		&profile.DivisionCode,
		&profile.DivisionName,
		&profile.TrainingProgramID,
		&profile.TrainingProgramName,
		&profile.CompensationType,
		&profile.Rate,
		&profile.StudentBasis,
		&profile.EffectiveFrom,
		&profile.EffectiveTo,
		&active,
		&profile.Notes,
		&profile.CreatedByUserID,
		&profile.UpdatedByUserID,
		&profile.CreatedAt,
		&profile.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	profile.Active = active == 1

	return &profile, nil
}

func (a *App) createStaffSalaryProfile(
	profile StaffSalaryProfile,
	actorUserID int64,
) (int64, error) {
	if err := validateStaffSalaryProfile(profile); err != nil {
		return 0, err
	}

	var exists int

	if err := a.queryRowDB(
		`SELECT COUNT(*) FROM users WHERE id = ?`,
		profile.UserID,
	).Scan(&exists); err != nil {
		return 0, err
	}

	if exists == 0 {
		return 0, errors.New("staff member was not found")
	}

	if profile.DivisionID > 0 {
		if err := a.queryRowDB(
			`SELECT COUNT(*) FROM divisions WHERE id = ?`,
			profile.DivisionID,
		).Scan(&exists); err != nil {
			return 0, err
		}

		if exists == 0 {
			return 0, errors.New("division was not found")
		}
	}

	if profile.TrainingProgramID > 0 {
		var programmeDivisionID int64

		if err := a.queryRowDB(
			`
			SELECT COALESCE(division_id, 0)
			FROM training_programs
			WHERE id = ?
			`,
			profile.TrainingProgramID,
		).Scan(&programmeDivisionID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, errors.New(
					"training programme was not found",
				)
			}

			return 0, err
		}

		if profile.DivisionID > 0 &&
			programmeDivisionID > 0 &&
			programmeDivisionID != profile.DivisionID {
			return 0, errors.New(
				"training programme does not belong to the selected division",
			)
		}
	}

	now := time.Now().UTC()
	studentBasis :=
		normalizeSalaryStudentBasis(profile.StudentBasis)

	if profile.CompensationType != SalaryTypePerStudent {
		studentBasis = SalaryStudentBasisActiveEnrollment
	}

	profileID, err := a.insertAndReturnID(
		`
		INSERT INTO staff_salary_profiles (
			user_id,
			division_id,
			training_program_id,
			compensation_type,
			rate,
			student_basis,
			effective_from,
			effective_to,
			active,
			notes,
			created_by_user_id,
			updated_by_user_id,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
		profile.UserID,
		nullIfZero(profile.DivisionID),
		nullIfZero(profile.TrainingProgramID),
		normalizeSalaryType(profile.CompensationType),
		profile.Rate,
		studentBasis,
		strings.TrimSpace(profile.EffectiveFrom),
		strings.TrimSpace(profile.EffectiveTo),
		boolToInt(profile.Active),
		strings.TrimSpace(profile.Notes),
		nullIfZero(actorUserID),
		nullIfZero(actorUserID),
		now,
		now,
	)
	if err != nil {
		return 0, err
	}

	return profileID, nil
}

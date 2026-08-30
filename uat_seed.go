package main

import (
	"database/sql"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"log"
	"strings"
	"time"
)

const (
	uatDefaultPassword = "MekmaaUAT2026!"
	uatSharedStudentID = "UAT-STU-001"
)

func runSeedUAT(db *sql.DB, env AppEnvironment) error {
	if env == appEnvProduction {
		return errors.New("seed-uat is disabled in production")
	}
	if err := seedCourtManager(db); err != nil {
		return fmt.Errorf("seed court manager: %w", err)
	}
	if err := seedPricingRules(db); err != nil {
		return fmt.Errorf("seed pricing rules: %w", err)
	}

	app := &App{db: db}
	divisions, err := loadSeedDivisions(db)
	if err != nil {
		return err
	}

	superadminID, err := upsertSeedUser(app, seedUserSpec{
		Name:        "UAT Superadmin",
		Email:       "superadmin+uat@mekmaa.local",
		Password:    uatDefaultPassword,
		Roles:       []string{"superadmin", "admin", "editor"},
		DivisionIDs: []int64{divisions[divisionCodeSports].ID, divisions[divisionCodeKEC].ID, divisions[divisionCodeChess].ID, divisions[divisionCodeCorporate].ID},
	})
	if err != nil {
		return err
	}
	sportsAdminID, err := upsertSeedUser(app, seedUserSpec{
		Name:        "UAT Sports Admin",
		Email:       "sports-admin+uat@mekmaa.local",
		Password:    uatDefaultPassword,
		Roles:       []string{"admin", "editor"},
		DivisionIDs: []int64{divisions[divisionCodeSports].ID},
	})
	if err != nil {
		return err
	}
	kecAdminID, err := upsertSeedUser(app, seedUserSpec{
		Name:        "UAT KEC Admin",
		Email:       "kec-admin+uat@mekmaa.local",
		Password:    uatDefaultPassword,
		Roles:       []string{"admin", "editor"},
		DivisionIDs: []int64{divisions[divisionCodeKEC].ID},
	})
	if err != nil {
		return err
	}
	chessAdminID, err := upsertSeedUser(app, seedUserSpec{
		Name:        "UAT Chess Admin",
		Email:       "chess-admin+uat@mekmaa.local",
		Password:    uatDefaultPassword,
		Roles:       []string{"admin", "editor"},
		DivisionIDs: []int64{divisions[divisionCodeChess].ID},
	})
	if err != nil {
		return err
	}
	if _, err := upsertSeedUser(app, seedUserSpec{
		Name:        "UAT Corporate Finance",
		Email:       "corporate-finance+uat@mekmaa.local",
		Password:    uatDefaultPassword,
		Roles:       []string{"admin", "editor"},
		DivisionIDs: []int64{divisions[divisionCodeCorporate].ID},
	}); err != nil {
		return err
	}

	sportsCoachID, err := upsertSeedCoach(app, seedCoachSpec{
		User: seedUserSpec{
			Name:        "UAT Sports Coach",
			Email:       "sports-coach+uat@mekmaa.local",
			Password:    uatDefaultPassword,
			Roles:       []string{"coach"},
			DivisionIDs: []int64{divisions[divisionCodeSports].ID},
		},
		Phone:       "0771000001",
		Address:     "Sports Wing, Temple Road, Jaffna",
		Specialties: "Cricket, badminton",
		Notes:       "Local UAT coach account",
	})
	if err != nil {
		return err
	}
	kecCoachID, err := upsertSeedCoach(app, seedCoachSpec{
		User: seedUserSpec{
			Name:        "UAT KEC Coach",
			Email:       "kec-coach+uat@mekmaa.local",
			Password:    uatDefaultPassword,
			Roles:       []string{"coach"},
			DivisionIDs: []int64{divisions[divisionCodeKEC].ID},
		},
		Phone:       "0771000002",
		Address:     "KEC Wing, Temple Road, Jaffna",
		Specialties: "Foundation learning",
		Notes:       "Local UAT coach account",
	})
	if err != nil {
		return err
	}
	chessCoachID, err := upsertSeedCoach(app, seedCoachSpec{
		User: seedUserSpec{
			Name:        "UAT Chess Coach",
			Email:       "chess-coach+uat@mekmaa.local",
			Password:    uatDefaultPassword,
			Roles:       []string{"coach"},
			DivisionIDs: []int64{divisions[divisionCodeChess].ID},
		},
		Phone:       "0771000003",
		Address:     "Chess Wing, Temple Road, Jaffna",
		Specialties: "Tournament prep",
		Notes:       "Local UAT coach account",
	})
	if err != nil {
		return err
	}

	sportsProgramID, err := ensureTrainingProgram(app, TrainingProgram{
		DivisionID:     divisions[divisionCodeSports].ID,
		Name:           "UAT Sports Group Practice",
		Activity:       "sports_uat",
		TrainingFormat: "group",
		AdmissionFee:   3500,
		MonthlyFee:     4200,
		Active:         true,
		SortOrder:      610,
	})
	if err != nil {
		return err
	}
	kecProgramID, err := ensureTrainingProgram(app, TrainingProgram{
		DivisionID:     divisions[divisionCodeKEC].ID,
		Name:           "UAT KEC Learning Circle",
		Activity:       "education",
		TrainingFormat: "group",
		AdmissionFee:   2800,
		MonthlyFee:     3600,
		Active:         true,
		SortOrder:      620,
	})
	if err != nil {
		return err
	}
	chessProgramID, err := ensureTrainingProgram(app, TrainingProgram{
		DivisionID:     divisions[divisionCodeChess].ID,
		Name:           "UAT Chess Academy Group",
		Activity:       "chess",
		TrainingFormat: "group",
		AdmissionFee:   3000,
		MonthlyFee:     3900,
		Active:         true,
		SortOrder:      630,
	})
	if err != nil {
		return err
	}

	admissionID, err := ensureSharedUATAdmission(app, []int64{sportsProgramID, kecProgramID, chessProgramID})
	if err != nil {
		return err
	}
	sportsEnrollmentID, err := ensureEnrollment(app, admissionID, sportsProgramID, false, false, true, "cash", superadminID)
	if err != nil {
		return err
	}
	kecEnrollmentID, err := ensureEnrollment(app, admissionID, kecProgramID, false, false, true, "cash", superadminID)
	if err != nil {
		return err
	}
	chessEnrollmentID, err := ensureEnrollment(app, admissionID, chessProgramID, false, false, true, "bank_transfer", superadminID)
	if err != nil {
		return err
	}

	if err := ensureStudentGroup(app, seedGroupSpec{
		Name:              "UAT Sports Evening Group",
		Code:              "UAT-SPORTS-G1",
		Description:       "Shared UAT student sports cohort",
		TrainingProgramID: sportsProgramID,
		AdmissionIDs:      []int64{admissionID},
		CoachIDs:          []int64{sportsCoachID},
		Sessions: []StudentGroupSession{
			{Title: "Friday Evening", DayOfWeek: "Friday", StartTime: "18:00", EndTime: "19:30", Active: true},
		},
	}); err != nil {
		return err
	}
	if err := ensureStudentGroup(app, seedGroupSpec{
		Name:              "UAT KEC Saturday Group",
		Code:              "UAT-KEC-G1",
		Description:       "Shared UAT student KEC cohort",
		TrainingProgramID: kecProgramID,
		AdmissionIDs:      []int64{admissionID},
		CoachIDs:          []int64{kecCoachID},
		Sessions: []StudentGroupSession{
			{Title: "Saturday Morning", DayOfWeek: "Saturday", StartTime: "09:00", EndTime: "10:30", Active: true},
		},
	}); err != nil {
		return err
	}
	if err := ensureStudentGroup(app, seedGroupSpec{
		Name:              "UAT Chess Strategy Group",
		Code:              "UAT-CHESS-G1",
		Description:       "Shared UAT student chess cohort",
		TrainingProgramID: chessProgramID,
		AdmissionIDs:      []int64{admissionID},
		CoachIDs:          []int64{chessCoachID},
		Sessions: []StudentGroupSession{
			{Title: "Sunday Strategy", DayOfWeek: "Sunday", StartTime: "10:00", EndTime: "11:30", Active: true},
		},
	}); err != nil {
		return err
	}

	if err := ensureAttendanceFixture(app, "UAT-SPORTS-G1", admissionID, "2026-08-14", sportsAdminID, "present"); err != nil {
		return err
	}
	if err := ensureAttendanceFixture(app, "UAT-KEC-G1", admissionID, "2026-08-08", kecAdminID, "present"); err != nil {
		return err
	}
	if err := ensureAttendanceFixture(app, "UAT-CHESS-G1", admissionID, "2026-08-09", chessAdminID, "late"); err != nil {
		return err
	}

	if _, err := ensureMonthlyPayment(app, sportsEnrollmentID, "2026-07", "cash", 0, sportsAdminID); err != nil {
		return err
	}
	if _, err := ensureMonthlyPayment(app, kecEnrollmentID, "2026-07", "cash", 1800, kecAdminID); err != nil {
		return err
	}
	if _, err := ensureMonthlyPayment(app, chessEnrollmentID, "2026-06", "bank_transfer", 0, chessAdminID); err != nil {
		return err
	}

	if err := ensureCorporateFinanceFixture(app, divisions[divisionCodeCorporate].ID, superadminID); err != nil {
		return err
	}
	if err := ensureSportsBookingFixture(app); err != nil {
		return err
	}

	log.Printf("seed-uat complete: shared_student=%s", uatSharedStudentID)
	log.Printf("seed-uat default password configured for local UAT accounts")
	log.Printf("seed-uat accounts: superadmin+uat@mekmaa.local sports-admin+uat@mekmaa.local kec-admin+uat@mekmaa.local chess-admin+uat@mekmaa.local corporate-finance+uat@mekmaa.local")
	return nil
}

type seedUserSpec struct {
	Name        string
	Email       string
	Password    string
	Roles       []string
	DivisionIDs []int64
}

type seedCoachSpec struct {
	User        seedUserSpec
	Phone       string
	Address     string
	Specialties string
	Notes       string
}

type seedGroupSpec struct {
	Name              string
	Code              string
	Description       string
	TrainingProgramID int64
	AdmissionIDs      []int64
	CoachIDs          []int64
	Sessions          []StudentGroupSession
}

func loadSeedDivisions(db *sql.DB) (map[string]Division, error) {
	rows, err := db.Query(`SELECT id, code, slug, name, COALESCE(description, ''), COALESCE(active, 1), created_at, updated_at FROM divisions WHERE active = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byCode := make(map[string]Division)
	for rows.Next() {
		var division Division
		var active int
		if err := rows.Scan(&division.ID, &division.Code, &division.Slug, &division.Name, &division.Description, &active, &division.CreatedAt, &division.UpdatedAt); err != nil {
			return nil, err
		}
		division.Active = active == 1
		byCode[strings.ToUpper(division.Code)] = division
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, code := range []string{divisionCodeSports, divisionCodeKEC, divisionCodeChess, divisionCodeCorporate} {
		if _, ok := byCode[code]; !ok {
			return nil, fmt.Errorf("required division %s is missing", code)
		}
	}
	return byCode, nil
}

func upsertSeedUser(app *App, spec seedUserSpec) (int64, error) {
	email := strings.ToLower(strings.TrimSpace(spec.Email))
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(spec.Password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}

	tx, err := app.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	var userID int64
	switch err := tx.QueryRow(`SELECT id FROM users WHERE email = ?`, email).Scan(&userID); {
	case err == nil:
		if _, err := tx.Exec(`
			UPDATE users
			SET name = ?, password_hash = ?, email_verified_at = ?
			WHERE id = ?
		`, spec.Name, string(passwordHash), now, userID); err != nil {
			return 0, err
		}
	case errors.Is(err, sql.ErrNoRows):
		result, err := tx.Exec(`
			INSERT INTO users (email, name, password_hash, created_at, email_verified_at)
			VALUES (?, ?, ?, ?, ?)
		`, email, spec.Name, string(passwordHash), now, now)
		if err != nil {
			return 0, err
		}
		userID, err = result.LastInsertId()
		if err != nil {
			return 0, err
		}
	default:
		return 0, err
	}

	if _, err := tx.Exec(`DELETE FROM user_roles WHERE user_id = ?`, userID); err != nil {
		return 0, err
	}
	for _, role := range spec.Roles {
		roleID, err := roleIDByName(tx, role)
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, roleID); err != nil {
			return 0, err
		}
	}

	if _, err := tx.Exec(`DELETE FROM user_divisions WHERE user_id = ?`, userID); err != nil {
		return 0, err
	}
	for _, divisionID := range spec.DivisionIDs {
		if divisionID <= 0 {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO user_divisions (user_id, division_id, created_at, updated_at)
			VALUES (?, ?, ?, ?)
		`, userID, divisionID, now, now); err != nil {
			return 0, err
		}
	}

	return userID, tx.Commit()
}

func upsertSeedCoach(app *App, spec seedCoachSpec) (int64, error) {
	coachID, err := upsertSeedUser(app, spec.User)
	if err != nil {
		return 0, err
	}
	if err := app.upsertCoachProfile(coachID, User{
		Phone:       spec.Phone,
		Address:     spec.Address,
		Specialties: spec.Specialties,
		Notes:       spec.Notes,
		Active:      true,
		CoachType:   "main",
	}); err != nil {
		return 0, err
	}
	return coachID, nil
}

func ensureTrainingProgram(app *App, program TrainingProgram) (int64, error) {
	var existingID int64
	err := app.db.QueryRow(`
		SELECT id
		FROM training_programs
		WHERE division_id = ?
		  AND LOWER(name) = LOWER(?)
		LIMIT 1
	`, program.DivisionID, program.Name).Scan(&existingID)
	if err == nil {
		program.ID = existingID
		return existingID, app.updateTrainingProgram(program)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return app.createTrainingProgram(program)
}

func ensureSharedUATAdmission(app *App, programIDs []int64) (int64, error) {
	admission := Admission{
		StudentID:                uatSharedStudentID,
		FullName:                 "UAT Shared Division Student",
		AdmissionDate:            "2026-05-20",
		DateOfBirth:              "2014-03-17",
		Gender:                   "female",
		PracticeType:             "group_practice",
		Address:                  "No. 64, Temple Road, Jaffna",
		PassportNumber:           "UAT-PP-001",
		School:                   "Jaffna Central",
		GuardianName:             "UAT Parent",
		GuardianRelationship:     "Mother",
		GuardianContactNumber:    "0772000000",
		GuardianAlternativePhone: "0772000001",
		MedicalInformation:       "No known medical issues",
		TrainingProgramID:        programIDs[0],
	}

	tx, err := app.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	var admissionID int64
	switch err := tx.QueryRow(`SELECT id FROM admissions WHERE student_id = ?`, admission.StudentID).Scan(&admissionID); {
	case err == nil:
		if _, err := tx.Exec(`
			UPDATE admissions
			SET full_name = ?, admission_date = ?, date_of_birth = ?, gender = ?, practice_type = ?,
			    training_program_id = ?, address = ?, passport_number = ?, school = ?, guardian_name = ?,
			    guardian_relationship = ?, guardian_contact_number = ?, guardian_alternative_contact_number = ?,
			    medical_information = ?, updated_at = ?
			WHERE id = ?
		`,
			admission.FullName,
			admission.AdmissionDate,
			admission.DateOfBirth,
			admission.Gender,
			admission.PracticeType,
			admission.TrainingProgramID,
			admission.Address,
			admission.PassportNumber,
			admission.School,
			admission.GuardianName,
			admission.GuardianRelationship,
			admission.GuardianContactNumber,
			admission.GuardianAlternativePhone,
			admission.MedicalInformation,
			now,
			admissionID,
		); err != nil {
			return 0, err
		}
	case errors.Is(err, sql.ErrNoRows):
		result, err := tx.Exec(`
			INSERT INTO admissions (
				student_id, full_name, admission_date, date_of_birth, gender, practice_type, training_program_id,
				address, passport_number, school, guardian_name, guardian_relationship, guardian_contact_number,
				guardian_alternative_contact_number, medical_information, photo_path, qr_code_path, qr_code_value,
				free_admission, free_monthly_fee, payment_collected, payment_collected_at, admission_payment_amount,
				finance_transaction_id, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', '', 0, 0, 0, NULL, 0, NULL, ?, ?)
		`,
			admission.StudentID,
			admission.FullName,
			admission.AdmissionDate,
			admission.DateOfBirth,
			admission.Gender,
			admission.PracticeType,
			admission.TrainingProgramID,
			admission.Address,
			admission.PassportNumber,
			admission.School,
			admission.GuardianName,
			admission.GuardianRelationship,
			admission.GuardianContactNumber,
			admission.GuardianAlternativePhone,
			admission.MedicalInformation,
			now,
			now,
		)
		if err != nil {
			return 0, err
		}
		admissionID, err = result.LastInsertId()
		if err != nil {
			return 0, err
		}
	default:
		return 0, err
	}

	if err := syncAdmissionTrainingProgramsTx(tx, databaseDriverSQLite, admissionID, programIDs, now); err != nil {
		return 0, err
	}
	return admissionID, tx.Commit()
}

func ensureEnrollment(app *App, admissionID int64, programID int64, freeAdmission bool, freeMonthlyFee bool, collectAdmission bool, paymentMethod string, recordedByUserID int64) (int64, error) {
	var enrollmentID int64
	err := app.db.QueryRow(`
		SELECT id
		FROM student_enrollments
		WHERE admission_id = ? AND training_program_id = ?
	`, admissionID, programID).Scan(&enrollmentID)
	switch {
	case err == nil:
		if _, err := app.db.Exec(`
			UPDATE student_enrollments
			SET free_admission = ?, free_monthly_fee = ?, active = 1, updated_at = ?
			WHERE id = ?
		`, boolToInt(freeAdmission), boolToInt(freeMonthlyFee), time.Now().UTC(), enrollmentID); err != nil {
			return 0, err
		}
	case errors.Is(err, sql.ErrNoRows):
		loadedProgram, err := app.findTrainingProgramByID(programID)
		if err != nil {
			return 0, err
		}
		createdID, _, err := app.createStudentEnrollmentWithOptionalPayment(StudentEnrollment{
			AdmissionID:         admissionID,
			TrainingProgramID:   programID,
			TrainingProgramName: loadedProgram.Name,
			FreeAdmission:       freeAdmission,
			FreeMonthlyFee:      freeMonthlyFee,
		}, collectAdmission, paymentMethod, recordedByUserID)
		if err != nil {
			return 0, err
		}
		enrollmentID = createdID
	default:
		return 0, err
	}

	if collectAdmission {
		var paymentCount int
		if err := app.db.QueryRow(`
			SELECT COUNT(*)
			FROM finance_transactions
			WHERE reference_type = 'student_enrollment' AND reference_id = ?
		`, enrollmentID).Scan(&paymentCount); err != nil {
			return 0, err
		}
		if paymentCount == 0 {
			enrollment, err := app.findStudentEnrollmentByID(enrollmentID)
			if err != nil {
				return 0, err
			}
			tx, err := app.db.Begin()
			if err != nil {
				return 0, err
			}
			defer tx.Rollback()
			if _, err := app.collectEnrollmentAdmissionPaymentTx(tx, *enrollment, paymentMethod, recordedByUserID); err != nil {
				return 0, err
			}
			if err := tx.Commit(); err != nil {
				return 0, err
			}
		}
	}

	return enrollmentID, nil
}

func ensureStudentGroup(app *App, spec seedGroupSpec) error {
	tx, err := app.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	var groupID int64
	switch err := tx.QueryRow(`SELECT id FROM student_groups WHERE code = ?`, spec.Code).Scan(&groupID); {
	case err == nil:
		if _, err := tx.Exec(`
			UPDATE student_groups
			SET name = ?, description = ?, training_program_id = ?, updated_at = ?
			WHERE id = ?
		`, spec.Name, spec.Description, nullIfZero(spec.TrainingProgramID), now, groupID); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
		result, err := tx.Exec(`
			INSERT INTO student_groups (name, code, description, training_program_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
		`, spec.Name, spec.Code, spec.Description, nullIfZero(spec.TrainingProgramID), now, now)
		if err != nil {
			return err
		}
		groupID, err = result.LastInsertId()
		if err != nil {
			return err
		}
	default:
		return err
	}

	if _, err := tx.Exec(`DELETE FROM student_group_members WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	for _, admissionID := range spec.AdmissionIDs {
		if _, err := tx.Exec(`INSERT INTO student_group_members (group_id, admission_id) VALUES (?, ?)`, groupID, admissionID); err != nil {
			return err
		}
	}
	if err := replaceStudentGroupCoachesTx(app, tx, databaseDriverSQLite, groupID, spec.CoachIDs); err != nil {
		return err
	}
	if err := replaceStudentGroupSessionsTx(tx, databaseDriverSQLite, groupID, spec.Sessions); err != nil {
		return err
	}

	return tx.Commit()
}

func ensureAttendanceFixture(app *App, groupCode string, admissionID int64, attendanceDate string, recordedByUserID int64, status string) error {
	group, err := loadGroupByCode(app.db, groupCode)
	if err != nil {
		return err
	}
	if len(group.Sessions) == 0 {
		return fmt.Errorf("group %s has no sessions", groupCode)
	}
	return app.replaceAttendanceRecords(group.ID, group.Sessions[0].ID, attendanceDate, []AttendanceRecord{{
		GroupID:          group.ID,
		SessionID:        group.Sessions[0].ID,
		AdmissionID:      admissionID,
		AttendanceDate:   attendanceDate,
		Status:           status,
		RecordedByUserID: recordedByUserID,
	}})
}

func ensureMonthlyPayment(app *App, enrollmentID int64, paymentMonth string, paymentMethod string, amount float64, recordedByUserID int64) (int64, error) {
	var existingID int64
	if err := app.db.QueryRow(`
		SELECT id
		FROM student_monthly_payments
		WHERE enrollment_id = ? AND payment_month = ? AND COALESCE(voided, 0) = 0
		ORDER BY id ASC
		LIMIT 1
	`, enrollmentID, paymentMonth).Scan(&existingID); err == nil {
		return existingID, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	monthDate, err := parsePaymentMonth(paymentMonth)
	if err != nil {
		return 0, err
	}
	paymentID, err := app.collectStudentMonthlyPaymentAmount(enrollmentID, paymentMonth, monthDate, paymentMethod, amount, recordedByUserID)
	if errors.Is(err, ErrStudentPaymentAlreadyCollected) {
		if err := app.db.QueryRow(`
			SELECT id
			FROM student_monthly_payments
			WHERE enrollment_id = ? AND payment_month = ? AND COALESCE(voided, 0) = 0
			ORDER BY id ASC
			LIMIT 1
		`, enrollmentID, paymentMonth).Scan(&existingID); err == nil {
			return existingID, nil
		}
		return 0, nil
	}
	return paymentID, err
}

func ensureCorporateFinanceFixture(app *App, divisionID int64, recordedByUserID int64) error {
	rows := []struct {
		category      string
		personName    string
		description   string
		paymentMethod string
		amount        float64
		recordedAt    time.Time
	}{
		{"manual_income", "UAT Sponsor", "UAT Corporate Sponsorship - July 2026", "bank_transfer", 25000, time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)},
		{"utilities_expense", "UAT Facilities", "UAT Corporate Utilities - July 2026", "cash", 6200, time.Date(2026, time.July, 30, 14, 0, 0, 0, time.UTC)},
	}
	for _, row := range rows {
		var count int
		if err := app.db.QueryRow(`
			SELECT COUNT(*)
			FROM finance_transactions
			WHERE division_id = ? AND description = ?
		`, divisionID, row.description).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		if _, err := app.createManualFinanceTransactionInDivision(row.category, row.personName, row.description, row.paymentMethod, row.amount, divisionID, row.recordedAt, recordedByUserID); err != nil {
			return err
		}
	}
	return nil
}

func ensureSportsBookingFixture(app *App) error {
	var count int
	if err := app.db.QueryRow(`
		SELECT COUNT(*)
		FROM space_schedules
		WHERE slot_date = '2026-08-14' AND slot_hour = '18:00' AND title = 'UAT Sports Booking'
	`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return app.createSpaceSchedule(SpaceSchedule{
		SlotDate:       "2026-08-14",
		SlotHour:       "18:00",
		EntryType:      "booking",
		Activity:       "badminton",
		Quantity:       1,
		Title:          "UAT Sports Booking",
		Notes:          "Local UAT booking fixture",
		RequesterName:  "UAT Booker",
		RequesterEmail: "booker+uat@mekmaa.local",
		RequesterPhone: "0773000000",
		QuotedPrice:    4500,
	})
}

func loadGroupByCode(db *sql.DB, code string) (*StudentGroup, error) {
	row := db.QueryRow(`
		SELECT id, name, code, description, COALESCE(training_program_id, 0), created_at
		FROM student_groups
		WHERE code = ?
	`, code)
	var group StudentGroup
	if err := row.Scan(&group.ID, &group.Name, &group.Code, &group.Description, &group.TrainingProgramID, &group.CreatedAt); err != nil {
		return nil, err
	}

	rows, err := db.Query(`
		SELECT id, group_id, title, day_of_week, start_time, end_time, active, created_at, updated_at
		FROM student_group_sessions
		WHERE group_id = ?
		ORDER BY id ASC
	`, group.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var session StudentGroupSession
		var active int
		if err := rows.Scan(&session.ID, &session.GroupID, &session.Title, &session.DayOfWeek, &session.StartTime, &session.EndTime, &active, &session.CreatedAt, &session.UpdatedAt); err != nil {
			return nil, err
		}
		session.Active = active == 1
		group.Sessions = append(group.Sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &group, nil
}

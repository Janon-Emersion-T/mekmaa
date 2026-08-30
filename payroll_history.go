package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type groupStaffHistoryState struct {
	UserID            int64
	AssignmentRole    string
	PrimaryAssignment bool
	EffectiveFrom     string
}

func normalizePayrollHistoryDate(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("effective date is required")
	}

	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return "", errors.New("invalid effective date")
	}

	return parsed.Format("2006-01-02"), nil
}

func uniquePositiveHistoryIDs(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))

	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}

		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}

func syncPayrollHistoryTimestamp() time.Time {
	return time.Now().UTC()
}

func syncStudentGroupMembershipHistoryTx(
	a *App,
	tx *sql.Tx,
	groupID int64,
	admissionIDs []int64,
	effectiveDate string,
) error {
	if groupID <= 0 {
		return errors.New("invalid student group")
	}

	effectiveDate, err := normalizePayrollHistoryDate(effectiveDate)
	if err != nil {
		return err
	}

	admissionIDs = uniquePositiveHistoryIDs(admissionIDs)

	rows, err := a.queryTxDB(
		tx,
		`
		SELECT
			admission_id,
			CAST(effective_from AS TEXT)
		FROM student_group_membership_history
		WHERE group_id = ?
		  AND effective_to IS NULL
		`,
		groupID,
	)
	if err != nil {
		return err
	}

	type openMembership struct {
		AdmissionID   int64
		EffectiveFrom string
	}

	open := make(map[int64]openMembership)

	for rows.Next() {
		var row openMembership

		if err := rows.Scan(
			&row.AdmissionID,
			&row.EffectiveFrom,
		); err != nil {
			rows.Close()
			return err
		}

		open[row.AdmissionID] = row
	}

	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	desired := make(map[int64]struct{}, len(admissionIDs))
	for _, admissionID := range admissionIDs {
		desired[admissionID] = struct{}{}
	}

	now := syncPayrollHistoryTimestamp()

	// Close memberships that no longer exist.
	for admissionID, current := range open {
		if _, exists := desired[admissionID]; exists {
			continue
		}

		// Added and removed on the same effective date:
		// remove the zero-length historical interval completely.
		if current.EffectiveFrom >= effectiveDate {
			if _, err := a.execTxDB(
				tx,
				`
				DELETE FROM student_group_membership_history
				WHERE group_id = ?
				  AND admission_id = ?
				  AND effective_to IS NULL
				`,
				groupID,
				admissionID,
			); err != nil {
				return err
			}
			continue
		}

		if _, err := a.execTxDB(
			tx,
			`
			UPDATE student_group_membership_history
			SET
				effective_to = ?,
				updated_at = ?
			WHERE group_id = ?
			  AND admission_id = ?
			  AND effective_to IS NULL
			`,
			effectiveDate,
			now,
			groupID,
			admissionID,
		); err != nil {
			return err
		}
	}

	// Open memberships that did not already exist.
	for _, admissionID := range admissionIDs {
		if _, exists := open[admissionID]; exists {
			continue
		}

		if _, err := a.execTxDB(
			tx,
			`
			INSERT INTO student_group_membership_history (
				group_id,
				admission_id,
				effective_from,
				effective_to,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, NULL, ?, ?)
			`,
			groupID,
			admissionID,
			effectiveDate,
			now,
			now,
		); err != nil {
			return err
		}
	}

	return nil
}

func syncStudentGroupStaffHistoryTx(
	a *App,
	tx *sql.Tx,
	groupID int64,
	assignments []GroupStaffAssignmentInput,
	effectiveDate string,
) error {
	if groupID <= 0 {
		return errors.New("invalid student group")
	}

	effectiveDate, err := normalizePayrollHistoryDate(effectiveDate)
	if err != nil {
		return err
	}

	rows, err := a.queryTxDB(
		tx,
		`
		SELECT
			user_id,
			COALESCE(assignment_role, ''),
			COALESCE(primary_assignment, 0),
			CAST(effective_from AS TEXT)
		FROM student_group_staff_assignment_history
		WHERE group_id = ?
		  AND effective_to IS NULL
		`,
		groupID,
	)
	if err != nil {
		return err
	}

	open := make(map[int64]groupStaffHistoryState)

	for rows.Next() {
		var state groupStaffHistoryState
		var primary int

		if err := rows.Scan(
			&state.UserID,
			&state.AssignmentRole,
			&primary,
			&state.EffectiveFrom,
		); err != nil {
			rows.Close()
			return err
		}

		state.AssignmentRole =
			normalizeGroupStaffRole(state.AssignmentRole)
		state.PrimaryAssignment = primary == 1

		open[state.UserID] = state
	}

	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	desired := make(map[int64]GroupStaffAssignmentInput)

	for _, assignment := range assignments {
		if assignment.UserID <= 0 {
			continue
		}

		assignment.AssignmentRole =
			normalizeGroupStaffRole(assignment.AssignmentRole)

		desired[assignment.UserID] = assignment
	}

	now := syncPayrollHistoryTimestamp()

	for userID, current := range open {
		next, stillAssigned := desired[userID]

		unchanged := stillAssigned &&
			current.AssignmentRole == next.AssignmentRole &&
			current.PrimaryAssignment == next.PrimaryAssignment

		if unchanged {
			continue
		}

		// Same-day replacement/removal should not leave a zero-length row.
		if current.EffectiveFrom >= effectiveDate {
			if _, err := a.execTxDB(
				tx,
				`
				DELETE FROM student_group_staff_assignment_history
				WHERE group_id = ?
				  AND user_id = ?
				  AND effective_to IS NULL
				`,
				groupID,
				userID,
			); err != nil {
				return err
			}
		} else {
			if _, err := a.execTxDB(
				tx,
				`
				UPDATE student_group_staff_assignment_history
				SET
					effective_to = ?,
					updated_at = ?
				WHERE group_id = ?
				  AND user_id = ?
				  AND effective_to IS NULL
				`,
				effectiveDate,
				now,
				groupID,
				userID,
			); err != nil {
				return err
			}
		}
	}

	for userID, assignment := range desired {
		current, exists := open[userID]

		if exists &&
			current.AssignmentRole == assignment.AssignmentRole &&
			current.PrimaryAssignment == assignment.PrimaryAssignment {
			continue
		}

		primary := 0
		if assignment.PrimaryAssignment {
			primary = 1
		}

		if _, err := a.execTxDB(
			tx,
			`
			INSERT INTO student_group_staff_assignment_history (
				group_id,
				user_id,
				assignment_role,
				primary_assignment,
				effective_from,
				effective_to,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, NULL, ?, ?)
			`,
			groupID,
			userID,
			assignment.AssignmentRole,
			primary,
			effectiveDate,
			now,
			now,
		); err != nil {
			return err
		}
	}

	return nil
}

func syncStudentGroupCoachHistoryTx(
	a *App,
	tx *sql.Tx,
	groupID int64,
	coachIDs []int64,
	effectiveDate string,
) error {
	if groupID <= 0 {
		return errors.New("invalid student group")
	}

	effectiveDate, err := normalizePayrollHistoryDate(effectiveDate)
	if err != nil {
		return err
	}

	coachIDs = uniquePositiveHistoryIDs(coachIDs)

	rows, err := a.queryTxDB(
		tx,
		`
		SELECT
			user_id,
			CAST(effective_from AS TEXT)
		FROM student_group_staff_assignment_history
		WHERE group_id = ?
		  AND effective_to IS NULL
		  AND LOWER(COALESCE(assignment_role, '')) = 'coach'
		`,
		groupID,
	)
	if err != nil {
		return err
	}

	type openCoachHistory struct {
		UserID        int64
		EffectiveFrom string
	}

	open := make(map[int64]openCoachHistory)

	for rows.Next() {
		var row openCoachHistory

		if err := rows.Scan(
			&row.UserID,
			&row.EffectiveFrom,
		); err != nil {
			rows.Close()
			return err
		}

		open[row.UserID] = row
	}

	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	desired := make(map[int64]struct{}, len(coachIDs))
	for _, coachID := range coachIDs {
		desired[coachID] = struct{}{}
	}

	now := syncPayrollHistoryTimestamp()

	for userID, current := range open {
		if _, exists := desired[userID]; exists {
			continue
		}

		if current.EffectiveFrom >= effectiveDate {
			if _, err := a.execTxDB(
				tx,
				`
				DELETE FROM student_group_staff_assignment_history
				WHERE group_id = ?
				  AND user_id = ?
				  AND effective_to IS NULL
				  AND LOWER(COALESCE(assignment_role, '')) = 'coach'
				`,
				groupID,
				userID,
			); err != nil {
				return err
			}
			continue
		}

		if _, err := a.execTxDB(
			tx,
			`
			UPDATE student_group_staff_assignment_history
			SET
				effective_to = ?,
				updated_at = ?
			WHERE group_id = ?
			  AND user_id = ?
			  AND effective_to IS NULL
			  AND LOWER(COALESCE(assignment_role, '')) = 'coach'
			`,
			effectiveDate,
			now,
			groupID,
			userID,
		); err != nil {
			return err
		}
	}

	for _, coachID := range coachIDs {
		if _, exists := open[coachID]; exists {
			continue
		}

		if _, err := a.execTxDB(
			tx,
			`
			INSERT INTO student_group_staff_assignment_history (
				group_id,
				user_id,
				assignment_role,
				primary_assignment,
				effective_from,
				effective_to,
				created_at,
				updated_at
			)
			VALUES (?, ?, 'coach', 0, ?, NULL, ?, ?)
			`,
			groupID,
			coachID,
			effectiveDate,
			now,
			now,
		); err != nil {
			return err
		}
	}

	return nil
}

func syncStudentEnrollmentStatusHistoryTx(
	a *App,
	tx *sql.Tx,
	enrollmentID int64,
	active bool,
	effectiveDate string,
) error {
	if enrollmentID <= 0 {
		return errors.New("invalid student enrollment")
	}

	effectiveDate, err := normalizePayrollHistoryDate(effectiveDate)
	if err != nil {
		return err
	}

	var (
		historyID     int64
		currentActive int
		effectiveFrom string
	)

	err = a.queryRowTxDB(
		tx,
		`
		SELECT
			id,
			active,
			CAST(effective_from AS TEXT)
		FROM student_enrollment_status_history
		WHERE enrollment_id = ?
		  AND effective_to IS NULL
		ORDER BY id DESC
		LIMIT 1
		`,
		enrollmentID,
	).Scan(
		&historyID,
		&currentActive,
		&effectiveFrom,
	)

	desiredActive := boolToInt(active)
	now := syncPayrollHistoryTimestamp()

	if errors.Is(err, sql.ErrNoRows) {
		_, err = a.execTxDB(
			tx,
			`
			INSERT INTO student_enrollment_status_history (
				enrollment_id,
				active,
				effective_from,
				effective_to,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, NULL, ?, ?)
			`,
			enrollmentID,
			desiredActive,
			effectiveDate,
			now,
			now,
		)
		return err
	}
	if err != nil {
		return err
	}

	if currentActive == desiredActive {
		return nil
	}

	if effectiveFrom >= effectiveDate {
		if _, err := a.execTxDB(
			tx,
			`
			DELETE FROM student_enrollment_status_history
			WHERE id = ?
			`,
			historyID,
		); err != nil {
			return err
		}
	} else {
		if _, err := a.execTxDB(
			tx,
			`
			UPDATE student_enrollment_status_history
			SET
				effective_to = ?,
				updated_at = ?
			WHERE id = ?
			`,
			effectiveDate,
			now,
			historyID,
		); err != nil {
			return err
		}
	}

	_, err = a.execTxDB(
		tx,
		`
		INSERT INTO student_enrollment_status_history (
			enrollment_id,
			active,
			effective_from,
			effective_to,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, NULL, ?, ?)
		`,
		enrollmentID,
		desiredActive,
		effectiveDate,
		now,
		now,
	)

	return err
}

func syncStudentEnrollmentActiveStartDateTx(
	a *App,
	tx *sql.Tx,
	enrollmentID int64,
	previousDate string,
	nextDate string,
) error {
	if enrollmentID <= 0 {
		return errors.New("invalid student enrollment")
	}

	previousDate = strings.TrimSpace(previousDate)
	nextDate = strings.TrimSpace(nextDate)
	if previousDate == "" || nextDate == "" || previousDate == nextDate {
		return nil
	}

	previousDate, err := normalizePayrollHistoryDate(previousDate)
	if err != nil {
		return fmt.Errorf("invalid previous enrollment date: %w", err)
	}
	nextDate, err = normalizePayrollHistoryDate(nextDate)
	if err != nil {
		return fmt.Errorf("invalid next enrollment date: %w", err)
	}

	var (
		historyID     int64
		currentActive int
		effectiveFrom string
	)

	err = a.queryRowTxDB(
		tx,
		`
		SELECT
			id,
			active,
			CAST(effective_from AS TEXT)
		FROM student_enrollment_status_history
		WHERE enrollment_id = ?
		  AND effective_to IS NULL
		ORDER BY id DESC
		LIMIT 1
		`,
		enrollmentID,
	).Scan(
		&historyID,
		&currentActive,
		&effectiveFrom,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	if currentActive != 1 || strings.TrimSpace(effectiveFrom) != previousDate {
		return nil
	}

	_, err = a.execTxDB(
		tx,
		`
		UPDATE student_enrollment_status_history
		SET
			effective_from = ?,
			updated_at = ?
		WHERE id = ?
		`,
		nextDate,
		syncPayrollHistoryTimestamp(),
		historyID,
	)

	return err
}

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	groupStaffRoleCoach            = "coach"
	groupStaffRoleAssistantCoach   = "assistant_coach"
	groupStaffRoleTeacher          = "teacher"
	groupStaffRoleAssistantTeacher = "assistant_teacher"
	groupStaffRoleCoordinator      = "coordinator"
)

type GroupStaffAssignment struct {
	GroupID           int64
	UserID            int64
	UserName          string
	UserEmail         string
	AssignmentRole    string
	RoleLabel         string
	PrimaryAssignment bool
}

type GroupStaffAssignmentInput struct {
	UserID            int64
	AssignmentRole    string
	PrimaryAssignment bool
}

type GroupStaffRoleOption struct {
	Key   string
	Label string
}

func normalizeGroupStaffRole(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func groupStaffRoleLabel(role string) string {
	switch normalizeGroupStaffRole(role) {
	case groupStaffRoleCoach:
		return "Coach"
	case groupStaffRoleAssistantCoach:
		return "Assistant Coach"
	case groupStaffRoleTeacher:
		return "Teacher"
	case groupStaffRoleAssistantTeacher:
		return "Assistant Teacher"
	case groupStaffRoleCoordinator:
		return "Coordinator"
	default:
		return ""
	}
}

func allowedGroupStaffRolesForDivisionCode(code string) []string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case divisionCodeSports:
		return []string{
			groupStaffRoleCoach,
			groupStaffRoleAssistantCoach,
		}
	case divisionCodeKEC:
		return []string{
			groupStaffRoleTeacher,
			groupStaffRoleAssistantTeacher,
			groupStaffRoleCoordinator,
		}
	case divisionCodeChess:
		return []string{
			groupStaffRoleCoach,
			groupStaffRoleAssistantCoach,
			groupStaffRoleCoordinator,
		}
	default:
		return nil
	}
}

func groupStaffRoleOptionsForDivisionCode(
	divisionCode string,
) []GroupStaffRoleOption {
	roles := allowedGroupStaffRolesForDivisionCode(divisionCode)
	options := make([]GroupStaffRoleOption, 0, len(roles))

	for _, role := range roles {
		label := groupStaffRoleLabel(role)
		if label == "" {
			continue
		}

		options = append(options, GroupStaffRoleOption{
			Key:   role,
			Label: label,
		})
	}

	return options
}

func (a *App) listAssignableGroupStaffByDivisionIDs(
	divisionIDs []int64,
) ([]User, error) {
	placeholders, args := int64ScopePlaceholders(divisionIDs)
	if placeholders == "" {
		return nil, nil
	}

	rows, err := a.db.Query(`
		SELECT DISTINCT
			u.id,
			COALESCE(u.email, ''),
			COALESCE(u.name, ''),
			COALESCE(u.phone, ''),
			COALESCE(u.address, ''),
			COALESCE(u.specialties, ''),
			COALESCE(u.notes, ''),
			COALESCE(u.active, 1)
		FROM users u
		JOIN user_divisions ud
			ON ud.user_id = u.id
		WHERE ud.division_id IN (`+placeholders+`)
		  AND COALESCE(u.active, 1) = 1
		ORDER BY
			LOWER(COALESCE(u.name, '')) ASC,
			LOWER(COALESCE(u.email, '')) ASC,
			u.id ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := make([]User, 0)

	for rows.Next() {
		var user User
		var active int

		if err := rows.Scan(
			&user.ID,
			&user.Email,
			&user.Name,
			&user.Phone,
			&user.Address,
			&user.Specialties,
			&user.Notes,
			&active,
		); err != nil {
			return nil, err
		}

		user.Active = active == 1
		users = append(users, user)
	}

	return users, rows.Err()
}

func groupStaffRoleSelected(
	assignments []GroupStaffAssignment,
	userID int64,
	role string,
) bool {
	role = normalizeGroupStaffRole(role)

	for _, assignment := range assignments {
		if assignment.UserID == userID &&
			normalizeGroupStaffRole(assignment.AssignmentRole) == role {
			return true
		}
	}

	return false
}

func groupStaffUserAssigned(
	assignments []GroupStaffAssignment,
	userID int64,
) bool {
	for _, assignment := range assignments {
		if assignment.UserID == userID {
			return true
		}
	}

	return false
}

func legacyCoachIDsFromGroupStaff(
	assignments []GroupStaffAssignmentInput,
) []int64 {
	seen := make(map[int64]struct{})
	result := make([]int64, 0)

	for _, assignment := range assignments {
		role := normalizeGroupStaffRole(assignment.AssignmentRole)

		if role != groupStaffRoleCoach &&
			role != groupStaffRoleAssistantCoach {
			continue
		}

		if assignment.UserID <= 0 {
			continue
		}

		if _, exists := seen[assignment.UserID]; exists {
			continue
		}

		seen[assignment.UserID] = struct{}{}
		result = append(result, assignment.UserID)
	}

	return result
}

func validGroupStaffRoleForDivisionCode(divisionCode, role string) bool {
	role = normalizeGroupStaffRole(role)

	for _, allowed := range allowedGroupStaffRolesForDivisionCode(divisionCode) {
		if role == allowed {
			return true
		}
	}

	return false
}

func validateGroupStaffAssignments(
	divisionCode string,
	assignments []GroupStaffAssignmentInput,
) error {
	seen := make(map[string]struct{})

	for i := range assignments {
		assignment := assignments[i]

		if assignment.UserID <= 0 {
			return errors.New("select a valid staff member")
		}

		role := normalizeGroupStaffRole(assignment.AssignmentRole)
		if !validGroupStaffRoleForDivisionCode(divisionCode, role) {
			return fmt.Errorf(
				"%s is not a valid staff role for this division",
				groupStaffRoleLabel(role),
			)
		}

		key := fmt.Sprintf("%d:%s", assignment.UserID, role)
		if _, exists := seen[key]; exists {
			return errors.New("duplicate staff assignment")
		}

		seen[key] = struct{}{}
	}

	return nil
}

func (a *App) findStudentGroupDivisionCode(groupID int64) (string, error) {
	var code string

	err := a.db.QueryRow(`
		SELECT d.code
		FROM student_groups sg
		JOIN training_programs tp
			ON tp.id = sg.training_program_id
		JOIN divisions d
			ON d.id = tp.division_id
		WHERE sg.id = ?
	`, groupID).Scan(&code)

	return strings.TrimSpace(code), err
}

func (a *App) listStudentGroupStaff(
	groupID int64,
) ([]GroupStaffAssignment, error) {
	rows, err := a.db.Query(`
		SELECT
			sgs.group_id,
			sgs.user_id,
			COALESCE(u.name, ''),
			COALESCE(u.email, ''),
			sgs.assignment_role,
			COALESCE(sgs.primary_assignment, 0)
		FROM student_group_staff sgs
		JOIN users u
			ON u.id = sgs.user_id
		WHERE sgs.group_id = ?
		ORDER BY
			COALESCE(sgs.primary_assignment, 0) DESC,
			LOWER(COALESCE(u.name, '')) ASC,
			LOWER(sgs.assignment_role) ASC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	assignments := make([]GroupStaffAssignment, 0)

	for rows.Next() {
		var assignment GroupStaffAssignment
		var primary int

		if err := rows.Scan(
			&assignment.GroupID,
			&assignment.UserID,
			&assignment.UserName,
			&assignment.UserEmail,
			&assignment.AssignmentRole,
			&primary,
		); err != nil {
			return nil, err
		}

		assignment.AssignmentRole =
			normalizeGroupStaffRole(assignment.AssignmentRole)
		assignment.RoleLabel =
			groupStaffRoleLabel(assignment.AssignmentRole)
		assignment.PrimaryAssignment = primary == 1

		assignments = append(assignments, assignment)
	}

	return assignments, rows.Err()
}

func (a *App) replaceStudentGroupStaff(
	groupID int64,
	assignments []GroupStaffAssignmentInput,
) error {
	if groupID <= 0 {
		return errors.New("invalid student group")
	}

	divisionCode, err := a.findStudentGroupDivisionCode(groupID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("student group not found")
		}
		return err
	}

	for i := range assignments {
		assignments[i].AssignmentRole =
			normalizeGroupStaffRole(assignments[i].AssignmentRole)
	}

	if err := validateGroupStaffAssignments(
		divisionCode,
		assignments,
	); err != nil {
		return err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`DELETE FROM student_group_staff WHERE group_id = ?`,
		groupID,
	); err != nil {
		return err
	}

	// Keep the legacy coach relationship synchronized while Sports/Chess
	// attendance authorization still reads student_group_coaches.
	if _, err := tx.Exec(
		`DELETE FROM student_group_coaches WHERE group_id = ?`,
		groupID,
	); err != nil {
		return err
	}

	now := time.Now()

	for _, assignment := range assignments {
		primary := 0
		if assignment.PrimaryAssignment {
			primary = 1
		}

		if _, err := tx.Exec(`
			INSERT INTO student_group_staff (
				group_id,
				user_id,
				assignment_role,
				primary_assignment,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?)
		`,
			groupID,
			assignment.UserID,
			assignment.AssignmentRole,
			primary,
			now,
			now,
		); err != nil {
			return err
		}
	}

	for _, userID := range legacyCoachIDsFromGroupStaff(assignments) {
		if _, err := tx.Exec(`
			INSERT OR IGNORE INTO student_group_coaches (
				group_id,
				user_id,
				created_at
			)
			VALUES (?, ?, ?)
		`, groupID, userID, now); err != nil {
			return err
		}
	}

	return tx.Commit()
}

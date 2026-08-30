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

func allGroupStaffRoleOptions() []GroupStaffRoleOption {
	keys := []string{
		groupStaffRoleCoach,
		groupStaffRoleAssistantCoach,
		groupStaffRoleTeacher,
		groupStaffRoleAssistantTeacher,
		groupStaffRoleCoordinator,
	}

	options := make([]GroupStaffRoleOption, 0, len(keys))
	for _, key := range keys {
		label := groupStaffRoleLabel(key)
		if label == "" {
			continue
		}

		options = append(options, GroupStaffRoleOption{
			Key:   key,
			Label: label,
		})
	}

	return options
}

func (a *App) listAssignableGroupStaffByDivisionIDs(
	divisionIDs []int64,
) ([]User, error) {
	placeholders, args := int64ScopePlaceholders(divisionIDs)

	rows, err := a.queryDB(
		`
		SELECT DISTINCT
			u.id,
			COALESCE(u.email, ''),
			COALESCE(u.name, ''),
			'' AS phone,
			'' AS address,
			'' AS specialties,
			'' AS notes,
			1 AS active
		FROM users u
		JOIN user_divisions ud
			ON ud.user_id = u.id
		WHERE 1 = 1
`+func() string {
			if placeholders == "" {
				return ""
			}
			return " AND ud.division_id IN (" + placeholders + ")"
		}()+`
		ORDER BY
			3 ASC,
			2 ASC,
			1 ASC
		`,
		args...,
	)
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

func (a *App) hydrateStaffDirectoryUserDivisions(
	users []User,
) error {
	if len(users) == 0 {
		return nil
	}

	userIDs := make([]int64, 0, len(users))
	indexByUserID := make(map[int64]int, len(users))

	for index := range users {
		userIDs = append(userIDs, users[index].ID)
		indexByUserID[users[index].ID] = index

		users[index].DivisionIDs = nil
		users[index].DivisionCodes = nil
		users[index].Divisions = nil
	}

	placeholders, args := int64ScopePlaceholders(userIDs)
	if placeholders == "" {
		return nil
	}

	rows, err := a.queryDB(`
		SELECT
			ud.user_id,
			d.id,
			COALESCE(d.code, ''),
			COALESCE(d.slug, ''),
			COALESCE(d.name, ''),
			COALESCE(d.description, ''),
			COALESCE(d.active, 1)
		FROM user_divisions ud
		JOIN divisions d
			ON d.id = ud.division_id
		WHERE ud.user_id IN (`+placeholders+`)
		ORDER BY
			LOWER(COALESCE(d.name, '')) ASC,
			d.id ASC
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			userID   int64
			active   int
			division Division
		)

		if err := rows.Scan(
			&userID,
			&division.ID,
			&division.Code,
			&division.Slug,
			&division.Name,
			&division.Description,
			&active,
		); err != nil {
			return err
		}

		index, ok := indexByUserID[userID]
		if !ok {
			continue
		}

		division.Active = active == 1

		users[index].DivisionIDs = append(
			users[index].DivisionIDs,
			division.ID,
		)

		users[index].DivisionCodes = append(
			users[index].DivisionCodes,
			division.Code,
		)

		users[index].Divisions = append(
			users[index].Divisions,
			division,
		)
	}

	return rows.Err()
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

	err := a.queryRowDB(`
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
	rows, err := a.queryDB(`
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

	if err := syncStudentGroupStaffHistoryTx(
		a,
		tx,
		groupID,
		assignments,
		currentBusinessDate(),
	); err != nil {
		return err
	}

	// Replace canonical group staff assignments.
	if _, err := a.execTxDB(
		tx,
		`DELETE FROM student_group_staff WHERE group_id = ?`,
		groupID,
	); err != nil {
		return err
	}

	// Keep the legacy coach relationship synchronized because
	// the current attendance authorization still reads it.
	if _, err := a.execTxDB(
		tx,
		`DELETE FROM student_group_coaches WHERE group_id = ?`,
		groupID,
	); err != nil {
		return err
	}

	now := time.Now().UTC()

	for _, assignment := range assignments {
		primary := 0
		if assignment.PrimaryAssignment {
			primary = 1
		}

		if _, err := a.execTxDB(
			tx,
			`
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
		if _, err := a.execTxDB(
			tx,
			`
			INSERT INTO student_group_coaches (
				group_id,
				user_id,
				created_at
			)
			VALUES (?, ?, ?)
			ON CONFLICT (group_id, user_id) DO NOTHING
			`,
			groupID,
			userID,
			now,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

type StaffDirectoryAssignment struct {
	GroupID           int64
	GroupName         string
	AssignmentRole    string
	RoleLabel         string
	PrimaryAssignment bool
}

type StaffDirectoryRow struct {
	User            User
	Assignments     []StaffDirectoryAssignment
	AssignmentCount int
}

func buildStaffDirectoryRows(
	users []User,
	groups []StudentGroup,
) []StaffDirectoryRow {
	rows := make([]StaffDirectoryRow, 0, len(users))
	indexByUserID := make(map[int64]int, len(users))

	for _, user := range users {
		indexByUserID[user.ID] = len(rows)

		rows = append(rows, StaffDirectoryRow{
			User: user,
		})
	}

	for _, group := range groups {
		for _, assignment := range group.AssignedStaff {
			index, ok := indexByUserID[assignment.UserID]
			if !ok {
				continue
			}

			rows[index].Assignments = append(
				rows[index].Assignments,
				StaffDirectoryAssignment{
					GroupID:           group.ID,
					GroupName:         group.Name,
					AssignmentRole:    assignment.AssignmentRole,
					RoleLabel:         assignment.RoleLabel,
					PrimaryAssignment: assignment.PrimaryAssignment,
				},
			)

			rows[index].AssignmentCount++
		}
	}

	return rows
}

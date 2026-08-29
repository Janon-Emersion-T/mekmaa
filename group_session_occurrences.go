package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	GroupSessionOccurrenceStatusScheduled = "scheduled"
	GroupSessionOccurrenceStatusCompleted = "completed"
	GroupSessionOccurrenceStatusCancelled = "cancelled"
)

const (
	GroupSessionWorkStatusWorked  = "worked"
	GroupSessionWorkStatusAbsent  = "absent"
	GroupSessionWorkStatusExcused = "excused"
)

type StudentGroupSessionOccurrenceInput struct {
	ID int64

	GroupID           int64
	TimetableSessionID int64

	OccurrenceDate string
	ActualStartTime string
	ActualEndTime   string

	Status  string
	IsAdHoc bool
	Notes   string

	StaffAssignments []StudentGroupSessionStaffAssignmentInput
}

type StudentGroupSessionStaffAssignmentInput struct {
	UserID         int64
	AssignmentRole string
	WorkStatus     string
	Notes          string
}

func normalizeGroupSessionOccurrenceStatus(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validGroupSessionOccurrenceStatus(value string) bool {
	switch normalizeGroupSessionOccurrenceStatus(value) {
	case GroupSessionOccurrenceStatusScheduled,
		GroupSessionOccurrenceStatusCompleted,
		GroupSessionOccurrenceStatusCancelled:
		return true
	default:
		return false
	}
}

func groupSessionOccurrenceStatusLabel(value string) string {
	switch normalizeGroupSessionOccurrenceStatus(value) {
	case GroupSessionOccurrenceStatusScheduled:
		return "Scheduled"
	case GroupSessionOccurrenceStatusCompleted:
		return "Completed"
	case GroupSessionOccurrenceStatusCancelled:
		return "Cancelled"
	default:
		return strings.TrimSpace(value)
	}
}

func normalizeGroupSessionWorkStatus(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validGroupSessionWorkStatus(value string) bool {
	switch normalizeGroupSessionWorkStatus(value) {
	case GroupSessionWorkStatusWorked,
		GroupSessionWorkStatusAbsent,
		GroupSessionWorkStatusExcused:
		return true
	default:
		return false
	}
}

func groupSessionParticipationStatusLabel(value string) string {
	switch normalizeGroupSessionWorkStatus(value) {
	case GroupSessionWorkStatusWorked:
		return "Worked"
	case GroupSessionWorkStatusAbsent:
		return "Absent"
	case GroupSessionWorkStatusExcused:
		return "Excused"
	default:
		return strings.TrimSpace(value)
	}
}

func groupSessionParticipationForUser(
	assignments []StudentGroupSessionStaffAssignment,
	userID int64,
) *StudentGroupSessionStaffAssignment {
	for i := range assignments {
		if assignments[i].UserID == userID {
			return &assignments[i]
		}
	}

	return nil
}

func normalizeGroupSessionOccurrenceDate(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("session date is required")
	}

	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return "", errors.New("invalid session date")
	}

	if err := validateHistoricalEntryTime(parsed, "session date"); err != nil {
		return "", err
	}

	return parsed.Format("2006-01-02"), nil
}

func normalizeOptionalClockTime(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}

	parsed, err := time.Parse("15:04", value)
	if err != nil {
		return "", errors.New("invalid session time")
	}

	return parsed.Format("15:04"), nil
}

func validateGroupSessionOccurrenceInput(
	input StudentGroupSessionOccurrenceInput,
	divisionCode string,
) error {
	if input.GroupID <= 0 {
		return errors.New("student group is required")
	}

	if !validGroupSessionOccurrenceStatus(input.Status) {
		return errors.New("invalid session status")
	}

	if _, err := normalizeGroupSessionOccurrenceDate(input.OccurrenceDate); err != nil {
		return err
	}

	startTime, err := normalizeOptionalClockTime(input.ActualStartTime)
	if err != nil {
		return err
	}

	endTime, err := normalizeOptionalClockTime(input.ActualEndTime)
	if err != nil {
		return err
	}

	if startTime != "" && endTime != "" && endTime <= startTime {
		return errors.New("session end time must be after start time")
	}

	seenUsers := make(map[int64]struct{}, len(input.StaffAssignments))
	allowedRoles := groupSessionRoleOptionsForDivisionCode(divisionCode)
	allowedRoleKeys := make(map[string]struct{}, len(allowedRoles))
	for _, option := range allowedRoles {
		allowedRoleKeys[option.Key] = struct{}{}
	}

	for _, assignment := range input.StaffAssignments {
		if assignment.UserID <= 0 {
			return errors.New("invalid staff member")
		}

		if _, ok := seenUsers[assignment.UserID]; ok {
			return errors.New("duplicate session staff submission")
		}
		seenUsers[assignment.UserID] = struct{}{}

		role := normalizeGroupStaffRole(assignment.AssignmentRole)
		if role == "" {
			return errors.New("session staff role is required")
		}

		if len(allowedRoleKeys) > 0 {
			if _, ok := allowedRoleKeys[role]; !ok {
				return errors.New("invalid session staff role")
			}
		}

		if !validGroupSessionWorkStatus(assignment.WorkStatus) {
			return errors.New("invalid session staff status")
		}
	}

	return nil
}

func groupSessionRoleOptionsForDivisionCode(
	divisionCode string,
) []GroupStaffRoleOption {
	options := groupStaffRoleOptionsForDivisionCode(divisionCode)
	if len(options) > 0 {
		return options
	}

	return allGroupStaffRoleOptions()
}

func studentGroupSessionOccurrenceInputsFromRequest(
	r *http.Request,
) ([]StudentGroupSessionStaffAssignmentInput, error) {
	userIDs := normalizePositiveIDs(r.Form["staff_user_id"])
	roles := r.Form["staff_assignment_role"]
	statuses := r.Form["staff_work_status"]
	notes := r.Form["staff_note"]

	assignments := make([]StudentGroupSessionStaffAssignmentInput, 0, len(userIDs))
	for index, userID := range userIDs {
		role := ""
		if index < len(roles) {
			role = strings.TrimSpace(roles[index])
		}

		status := ""
		if index < len(statuses) {
			status = strings.TrimSpace(statuses[index])
		}

		note := ""
		if index < len(notes) {
			note = strings.TrimSpace(notes[index])
		}

		if role == "" && status == "" && note == "" {
			continue
		}

		if role == "" || status == "" {
			return nil, errors.New("session staff entries require both role and status")
		}

		assignments = append(assignments, StudentGroupSessionStaffAssignmentInput{
			UserID:         userID,
			AssignmentRole: role,
			WorkStatus:     status,
			Notes:          note,
		})
	}

	return assignments, nil
}

func (a *App) listStudentGroupSessionOccurrencesByGroupAndDate(
	groupID int64,
	occurrenceDate string,
) ([]StudentGroupSessionOccurrence, error) {
	occurrenceDate = strings.TrimSpace(occurrenceDate)
	if groupID <= 0 || occurrenceDate == "" {
		return nil, nil
	}

	rows, err := a.queryDB(`
		SELECT
			o.id,
			o.group_id,
			COALESCE(o.timetable_session_id, 0),
			COALESCE(s.title, ''),
			o.occurrence_date,
			COALESCE(o.actual_start_time, ''),
			COALESCE(o.actual_end_time, ''),
			COALESCE(o.status, ''),
			COALESCE(o.is_ad_hoc, 0),
			COALESCE(o.notes, ''),
			COALESCE(o.created_by_user_id, 0),
			COALESCE(o.updated_by_user_id, 0),
			o.created_at,
			o.updated_at
		FROM student_group_session_occurrences o
		LEFT JOIN student_group_sessions s
			ON s.id = o.timetable_session_id
		WHERE o.group_id = ?
		  AND o.occurrence_date = ?
		ORDER BY
			COALESCE(NULLIF(o.actual_start_time, ''), s.start_time, '23:59') ASC,
			o.id ASC
	`, groupID, occurrenceDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	occurrences := make([]StudentGroupSessionOccurrence, 0)
	occurrenceIDs := make([]int64, 0)
	for rows.Next() {
		var occurrence StudentGroupSessionOccurrence
		var isAdHoc int

		if err := rows.Scan(
			&occurrence.ID,
			&occurrence.GroupID,
			&occurrence.TimetableSessionID,
			&occurrence.TimetableSessionTitle,
			&occurrence.OccurrenceDate,
			&occurrence.ActualStartTime,
			&occurrence.ActualEndTime,
			&occurrence.Status,
			&isAdHoc,
			&occurrence.Notes,
			&occurrence.CreatedByUserID,
			&occurrence.UpdatedByUserID,
			&occurrence.CreatedAt,
			&occurrence.UpdatedAt,
		); err != nil {
			return nil, err
		}

		occurrence.IsAdHoc = isAdHoc == 1
		occurrenceIDs = append(occurrenceIDs, occurrence.ID)
		occurrences = append(occurrences, occurrence)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	assignmentsByOccurrence, err := a.listStudentGroupSessionStaffAssignmentsByOccurrenceIDs(occurrenceIDs)
	if err != nil {
		return nil, err
	}

	for i := range occurrences {
		occurrences[i].StaffAssignments = assignmentsByOccurrence[occurrences[i].ID]
	}

	return occurrences, nil
}

func (a *App) listStudentGroupSessionStaffAssignmentsByOccurrenceIDs(
	occurrenceIDs []int64,
) (map[int64][]StudentGroupSessionStaffAssignment, error) {
	occurrenceIDs = uniquePositiveInt64Values(occurrenceIDs)
	if len(occurrenceIDs) == 0 {
		return map[int64][]StudentGroupSessionStaffAssignment{}, nil
	}

	placeholders, args := int64ScopePlaceholders(occurrenceIDs)
	rows, err := a.queryDB(`
		SELECT
			occurrence_id,
			user_id,
			COALESCE(u.name, ''),
			COALESCE(u.email, ''),
			COALESCE(assignment_role, ''),
			COALESCE(work_status, ''),
			COALESCE(notes, ''),
			COALESCE(recorded_by_user_id, 0),
			recorded_at,
			updated_at
		FROM student_group_session_staff s
		LEFT JOIN users u
			ON u.id = s.user_id
		WHERE occurrence_id IN (`+placeholders+`)
		ORDER BY occurrence_id ASC, LOWER(COALESCE(u.name, '')) ASC, s.user_id ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	assignments := make(map[int64][]StudentGroupSessionStaffAssignment, len(occurrenceIDs))
	for rows.Next() {
		var assignment StudentGroupSessionStaffAssignment
		if err := rows.Scan(
			&assignment.OccurrenceID,
			&assignment.UserID,
			&assignment.UserName,
			&assignment.UserEmail,
			&assignment.AssignmentRole,
			&assignment.WorkStatus,
			&assignment.Notes,
			&assignment.RecordedByUserID,
			&assignment.RecordedAt,
			&assignment.UpdatedAt,
		); err != nil {
			return nil, err
		}

		assignments[assignment.OccurrenceID] = append(
			assignments[assignment.OccurrenceID],
			assignment,
		)
	}

	return assignments, rows.Err()
}

func (a *App) saveStudentGroupSessionOccurrence(
	input StudentGroupSessionOccurrenceInput,
	actorUserID int64,
) (int64, error) {
	if actorUserID <= 0 {
		return 0, errors.New("session recorder is required")
	}

	group, err := a.findStudentGroupByID(input.GroupID)
	if err != nil {
		return 0, err
	}

	divisionCode := ""
	if group.TrainingProgramID > 0 {
		program, err := a.findTrainingProgramByID(group.TrainingProgramID)
		if err != nil {
			return 0, err
		}

		division, err := a.findDivisionByID(program.DivisionID)
		if err == nil {
			divisionCode = division.Code
		}
	}

	input.Status = normalizeGroupSessionOccurrenceStatus(input.Status)
	input.Notes = strings.TrimSpace(input.Notes)
	input.OccurrenceDate, err = normalizeGroupSessionOccurrenceDate(input.OccurrenceDate)
	if err != nil {
		return 0, err
	}

	input.ActualStartTime, err = normalizeOptionalClockTime(input.ActualStartTime)
	if err != nil {
		return 0, err
	}

	input.ActualEndTime, err = normalizeOptionalClockTime(input.ActualEndTime)
	if err != nil {
		return 0, err
	}

	for i := range input.StaffAssignments {
		input.StaffAssignments[i].AssignmentRole = normalizeGroupStaffRole(input.StaffAssignments[i].AssignmentRole)
		input.StaffAssignments[i].WorkStatus = normalizeGroupSessionWorkStatus(input.StaffAssignments[i].WorkStatus)
		input.StaffAssignments[i].Notes = strings.TrimSpace(input.StaffAssignments[i].Notes)
	}

	if err := validateGroupSessionOccurrenceInput(input, divisionCode); err != nil {
		return 0, err
	}

	if input.TimetableSessionID > 0 {
		session, err := a.findStudentGroupSessionByID(input.TimetableSessionID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, errors.New("selected timetable session was not found")
			}
			return 0, err
		}
		if session.GroupID != input.GroupID {
			return 0, errors.New("selected timetable session does not belong to this group")
		}
	}

	if len(input.StaffAssignments) > 0 {
		var divisionIDs []int64
		if group.TrainingProgram != nil && group.TrainingProgram.DivisionID > 0 {
			divisionIDs = []int64{group.TrainingProgram.DivisionID}
		}

		if len(divisionIDs) > 0 {
			assignable, err := a.listAssignableGroupStaffByDivisionIDs(divisionIDs)
			if err != nil {
				return 0, err
			}

			if err := validateSubmittedGroupSessionUsers(input.StaffAssignments, assignable); err != nil {
				return 0, err
			}
		}
	}

	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	occurrenceID := input.ID

	if occurrenceID > 0 {
		result, err := a.execTxDB(tx, `
			UPDATE student_group_session_occurrences
			SET
				timetable_session_id = ?,
				occurrence_date = ?,
				actual_start_time = ?,
				actual_end_time = ?,
				status = ?,
				is_ad_hoc = ?,
				notes = ?,
				updated_by_user_id = ?,
				updated_at = ?
			WHERE id = ?
			  AND group_id = ?
		`,
			nullIfZero(input.TimetableSessionID),
			input.OccurrenceDate,
			input.ActualStartTime,
			input.ActualEndTime,
			input.Status,
			boolToInt(input.IsAdHoc),
			input.Notes,
			nullIfZero(actorUserID),
			now,
			occurrenceID,
			input.GroupID,
		)
		if err != nil {
			return 0, err
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if rowsAffected == 0 {
			return 0, sql.ErrNoRows
		}
	} else {
		occurrenceID, err = a.insertAndReturnIDTx(tx, `
			INSERT INTO student_group_session_occurrences (
				group_id,
				timetable_session_id,
				occurrence_date,
				actual_start_time,
				actual_end_time,
				status,
				is_ad_hoc,
				notes,
				created_by_user_id,
				updated_by_user_id,
				created_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			input.GroupID,
			nullIfZero(input.TimetableSessionID),
			input.OccurrenceDate,
			input.ActualStartTime,
			input.ActualEndTime,
			input.Status,
			boolToInt(input.IsAdHoc),
			input.Notes,
			nullIfZero(actorUserID),
			nullIfZero(actorUserID),
			now,
			now,
		)
		if err != nil {
			return 0, err
		}
	}

	if _, err := a.execTxDB(tx, `
		DELETE FROM student_group_session_staff
		WHERE occurrence_id = ?
	`, occurrenceID); err != nil {
		return 0, err
	}

	for _, assignment := range input.StaffAssignments {
		if _, err := a.execTxDB(tx, `
			INSERT INTO student_group_session_staff (
				occurrence_id,
				user_id,
				assignment_role,
				work_status,
				notes,
				recorded_by_user_id,
				recorded_at,
				updated_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`,
			occurrenceID,
			assignment.UserID,
			assignment.AssignmentRole,
			assignment.WorkStatus,
			assignment.Notes,
			nullIfZero(actorUserID),
			now,
			now,
		); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return occurrenceID, nil
}
func validateSubmittedGroupSessionUsers(
	assignments []StudentGroupSessionStaffAssignmentInput,
	users []User,
) error {
	allowed := make(map[int64]struct{}, len(users))
	for _, user := range users {
		if user.ID > 0 {
			allowed[user.ID] = struct{}{}
		}
	}

	for _, assignment := range assignments {
		if _, ok := allowed[assignment.UserID]; !ok {
			return errors.New("selected session staff must belong to the same division as the group")
		}
	}

	return nil
}

func (a *App) saveStudentGroupSessionOccurrenceHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	target := "/admin/student-groups"
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

	if division := strings.TrimSpace(r.FormValue("division")); division != "" {
		target = withDivisionQuery(target, division)
	}

	groupID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("group_id")), 10, 64)
	if err != nil || groupID <= 0 {
		a.setFlash(w, "Select a valid student group.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	currentUser, _ := a.currentUser(r.Context())
	if currentUser == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	group, err := a.findStudentGroupByID(groupID)
	if err != nil {
		a.setFlash(w, "Student group not found.")
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}

	if group.TrainingProgramID > 0 {
		program, err := a.findTrainingProgramByID(group.TrainingProgramID)
		if err != nil {
			log.Printf("find training programme for group occurrence: %v", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		if !a.requireDivisionAccessForDivision(w, r, currentUser, program.DivisionID) {
			return
		}
		group.TrainingProgram = program
	}

	assignments, err := studentGroupSessionOccurrenceInputsFromRequest(r)
	if err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, withQueryValue(withQueryValue(target, "action", "view"), "id", strconv.FormatInt(groupID, 10)), http.StatusSeeOther)
		return
	}

	occurrenceID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("occurrence_id")), 10, 64)
	input := StudentGroupSessionOccurrenceInput{
		ID:                occurrenceID,
		GroupID:           groupID,
		TimetableSessionID: parseInt64Query(r.FormValue("timetable_session_id")),
		OccurrenceDate:    strings.TrimSpace(r.FormValue("occurrence_date")),
		ActualStartTime:   strings.TrimSpace(r.FormValue("actual_start_time")),
		ActualEndTime:     strings.TrimSpace(r.FormValue("actual_end_time")),
		Status:            strings.TrimSpace(r.FormValue("status")),
		IsAdHoc:           r.FormValue("is_ad_hoc") == "1",
		Notes:             strings.TrimSpace(r.FormValue("notes")),
		StaffAssignments:  assignments,
	}

	savedID, err := a.saveStudentGroupSessionOccurrence(input, currentUser.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.setFlash(w, "Session occurrence not found.")
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		if isUniqueConstraintError(err) {
			a.setFlash(w, "A normal occurrence already exists for that group session and date. Use Ad hoc / extra session if this is an additional class.")
			http.Redirect(w, r, withQueryValue(withQueryValue(withQueryValue(target, "action", "view"), "id", strconv.FormatInt(groupID, 10)), "occurrence_date", input.OccurrenceDate), http.StatusSeeOther)
			return
		}

		a.setFlash(w, err.Error())
		http.Redirect(w, r, withQueryValue(withQueryValue(withQueryValue(target, "action", "view"), "id", strconv.FormatInt(groupID, 10)), "occurrence_date", input.OccurrenceDate), http.StatusSeeOther)
		return
	}

	message := "Session occurrence saved."
	if occurrenceID > 0 && savedID > 0 {
		message = "Session occurrence updated."
	}
	a.setFlash(w, message)

	redirectTo := withQueryValue(
		withQueryValue(
			withQueryValue(target, "action", "view"),
			"id",
			strconv.FormatInt(groupID, 10),
		),
		"occurrence_date",
		input.OccurrenceDate,
	)
	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}

func loadStudentGroupOccurrenceDate(r *http.Request) string {
	date := strings.TrimSpace(r.URL.Query().Get("occurrence_date"))
	if date == "" {
		return time.Now().Format("2006-01-02")
	}

	normalized, err := normalizeGroupSessionOccurrenceDate(date)
	if err != nil {
		return time.Now().Format("2006-01-02")
	}

	return normalized
}

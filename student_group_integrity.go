package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var errStudentGroupDivisionForbidden = errors.New(
	"student group is outside the permitted division scope",
)

func uniquePositiveInt64Values(values []int64) []int64 {
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

// validateStudentGroupMemberEnrollments ensures that every student assigned
// to a Training Group / Class / Batch has an active enrollment in the exact
// programme attached to that group.
func (a *App) validateStudentGroupMemberEnrollments(
	trainingProgramID int64,
	admissionIDs []int64,
) error {
	if trainingProgramID <= 0 {
		return errors.New("select a valid programme")
	}

	admissionIDs = uniquePositiveInt64Values(admissionIDs)

	if len(admissionIDs) == 0 {
		return nil
	}

	placeholders, admissionArgs :=
		int64ScopePlaceholders(admissionIDs)

	if placeholders == "" {
		return errors.New(
			"selected students could not be validated",
		)
	}

	args := make([]any, 0, len(admissionArgs)+1)
	args = append(args, trainingProgramID)
	args = append(args, admissionArgs...)

	var enrolledCount int

	err := a.queryRowDB(`
		SELECT COUNT(DISTINCT se.admission_id)
		FROM student_enrollments se
		WHERE se.training_program_id = ?
		  AND COALESCE(se.active, 1) = 1
		  AND se.admission_id IN (`+placeholders+`)
	`, args...).Scan(&enrolledCount)
	if err != nil {
		return fmt.Errorf(
			"validate programme enrollments: %w",
			err,
		)
	}

	if enrolledCount != len(admissionIDs) {
		return errors.New(
			"every selected student must have an active enrollment in the selected programme",
		)
	}

	return nil
}

// validateStudentGroupUpdatedMembers permits existing group members to
// remain selected while requiring newly added members to be actively enrolled.
// When the programme changes, all submitted members require validation.
func (a *App) validateStudentGroupUpdatedMembers(
	programmeID int64,
	previousProgrammeID int64,
	previousMemberIDs []int64,
	submittedIDs []int64,
) error {
	if programmeID <= 0 {
		return errors.New("select a valid programme")
	}

	existing := make(map[int64]struct{}, len(previousMemberIDs))
	if programmeID == previousProgrammeID {
		for _, studentID := range previousMemberIDs {
			existing[studentID] = struct{}{}
		}
	}

	newIDs := make([]int64, 0, len(submittedIDs))
	for _, id := range uniquePositiveInt64Values(submittedIDs) {
		if _, ok := existing[id]; !ok {
			newIDs = append(newIDs, id)
		}
	}

	return a.validateStudentGroupMemberEnrollments(
		programmeID,
		newIDs,
	)
}

func studentGroupMutationScopeAllowed(
	user *User,
	groupDivisionID int64,
	requestedDivision *Division,
) bool {
	if user == nil || groupDivisionID <= 0 {
		return false
	}

	if !canViewAllDivisions(user) &&
		!userCanAccessDivision(user, groupDivisionID) {
		return false
	}

	if requestedDivision != nil &&
		requestedDivision.ID != groupDivisionID {
		return false
	}

	return true
}

// validateStudentGroupMutationScope validates the real group division rather
// than trusting a group ID or division value supplied by the browser.
func (a *App) validateStudentGroupMutationScope(
	user *User,
	groupID int64,
	requestedScope string,
) error {
	if groupID <= 0 {
		return sql.ErrNoRows
	}

	groupDivisionID, err :=
		a.findStudentGroupDivisionByID(groupID)

	if err != nil {
		return err
	}

	requestedScope = strings.TrimSpace(requestedScope)

	var requestedDivision *Division

	if requestedScope != "" &&
		!strings.EqualFold(
			requestedScope,
			divisionScopeAll,
		) {

		division, err :=
			a.findDivisionBySlugOrCode(
				requestedScope,
			)

		if err != nil {
			return errStudentGroupDivisionForbidden
		}

		requestedDivision = division
	}

	if !studentGroupMutationScopeAllowed(
		user,
		groupDivisionID,
		requestedDivision,
	) {
		return errStudentGroupDivisionForbidden
	}

	return nil
}

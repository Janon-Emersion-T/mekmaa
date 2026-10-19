package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type CourseAttendanceStudent struct {
	EnrollmentID int64
	AdmissionID  int64
	StudentID    string
	Name         string
	Status       string
	Note         string
}

type CourseAttendanceEntry struct {
	EnrollmentID int64
	Status       string
	Note         string
}

func (a *App) courseAttendancePeriods(
	enrollmentID int64,
) ([]CourseAttendanceEnrollmentPeriod, error) {
	rows, err := a.queryDB(`
		SELECT
			COALESCE(CAST(effective_from AS TEXT), ''),
			COALESCE(CAST(effective_to AS TEXT), ''),
			active
		FROM student_enrollment_status_history
		WHERE enrollment_id = ?
		ORDER BY effective_from, id
	`, enrollmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var periods []CourseAttendanceEnrollmentPeriod
	for rows.Next() {
		var p CourseAttendanceEnrollmentPeriod
		if err := rows.Scan(
			&p.EffectiveFrom, &p.EffectiveTo, &p.Active,
		); err != nil {
			return nil, err
		}
		periods = append(periods, p)
	}
	return periods, rows.Err()
}

func (a *App) courseAttendanceRoster(
	programID int64, date string,
) ([]CourseAttendanceStudent, error) {
	if programID <= 0 || !validCourseAttendanceDate(date) {
		return nil, errors.New("invalid course or attendance date")
	}

	enrollments, err := a.listStudentEnrollments()
	if err != nil {
		return nil, err
	}

	var roster []CourseAttendanceStudent
	for _, enrollment := range enrollments {
		if enrollment.TrainingProgramID != programID {
			continue
		}

		periods, err := a.courseAttendancePeriods(enrollment.ID)
		if err != nil {
			return nil, err
		}
		eligible, err := courseAttendanceEnrollmentEligible(
			date,
			enrollment.EnrollmentDate,
			enrollment.Active,
			periods,
		)
		if err != nil {
			return nil, err
		}
		if !eligible {
			continue
		}

		student := CourseAttendanceStudent{
			EnrollmentID: enrollment.ID,
			AdmissionID:  enrollment.AdmissionID,
			StudentID:    enrollment.Student.StudentID,
			Name:         enrollment.Student.FullName,
		}

		err = a.queryRowDB(`
			SELECT status, note
			FROM course_attendance_records
			WHERE enrollment_id = ?
			  AND attendance_date = ?
		`, enrollment.ID, date).Scan(
			&student.Status, &student.Note,
		)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		roster = append(roster, student)
	}

	return roster, nil
}

func (a *App) saveCourseAttendance(
	programID int64,
	date string,
	entries []CourseAttendanceEntry,
	userID int64,
) error {
	if programID <= 0 || !validCourseAttendanceDate(date) {
		return errors.New("invalid course or attendance date")
	}
	if date > time.Now().Format("2006-01-02") {
		return errors.New("future attendance is not allowed")
	}

	roster, err := a.courseAttendanceRoster(programID, date)
	if err != nil {
		return err
	}

	allowed := make(map[int64]bool, len(roster))
	for _, student := range roster {
		allowed[student.EnrollmentID] = true
	}

	seen := make(map[int64]bool)
	for _, entry := range entries {
		if !allowed[entry.EnrollmentID] {
			return fmt.Errorf(
				"enrollment %d is not eligible for this course and date",
				entry.EnrollmentID,
			)
		}
		if seen[entry.EnrollmentID] {
			return errors.New("duplicate enrollment in attendance submission")
		}
		seen[entry.EnrollmentID] = true

		switch entry.Status {
		case "present", "absent", "late", "excused":
		default:
			return errors.New("invalid attendance status")
		}
		if len(strings.TrimSpace(entry.Note)) > 2000 {
			return errors.New("attendance note is too long")
		}
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Revalidate the selected enrollment under the write transaction.
	// PostgreSQL row locks serialize this save with enrollment changes.
	for _, entry := range entries {
		var actualProgramID int64
		var enrollmentDate string
		var active bool

		query := `SELECT training_program_id,
			COALESCE(CAST(enrollment_date AS TEXT), ''),
			active
			FROM student_enrollments WHERE id = ?`
		if a.runtimeConfig.DBDriver == databaseDriverPostgres {
			query += ` FOR UPDATE`
		}
		if err := a.queryRowTxDB(tx, query, entry.EnrollmentID).Scan(
			&actualProgramID, &enrollmentDate, &active,
		); err != nil {
			return err
		}
		if actualProgramID != programID {
			return errors.New("enrollment belongs to another course")
		}

		rows, err := a.queryTxDB(tx, `
			SELECT COALESCE(CAST(effective_from AS TEXT), ''),
				COALESCE(CAST(effective_to AS TEXT), ''), active
			FROM student_enrollment_status_history
			WHERE enrollment_id = ?
			ORDER BY effective_from, id
		`, entry.EnrollmentID)
		if err != nil {
			return err
		}

		var periods []CourseAttendanceEnrollmentPeriod
		for rows.Next() {
			var period CourseAttendanceEnrollmentPeriod
			if err := rows.Scan(&period.EffectiveFrom,
				&period.EffectiveTo, &period.Active); err != nil {
				rows.Close()
				return err
			}
			periods = append(periods, period)
		}
		readErr := rows.Err()
		rows.Close()
		if readErr != nil {
			return readErr
		}

		eligible, err := courseAttendanceEnrollmentEligible(
			date, enrollmentDate, active, periods,
		)
		if err != nil {
			return err
		}
		if !eligible {
			return errors.New("enrollment is not active on attendance date")
		}
	}

	// Only explicitly submitted enrollment records are updated.
	// Other saved records, including legacy group attendance, remain intact.
	for _, entry := range entries {
		_, err := a.execTxDB(tx, `
			INSERT INTO course_attendance_records (
				enrollment_id, attendance_date,
				status, note, recorded_by_user_id
			) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(enrollment_id, attendance_date)
			DO UPDATE SET
				status = excluded.status,
				note = excluded.note,
				recorded_by_user_id = excluded.recorded_by_user_id,
				updated_at = CURRENT_TIMESTAMP
		`, entry.EnrollmentID, date, entry.Status,
			strings.TrimSpace(entry.Note), userID)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

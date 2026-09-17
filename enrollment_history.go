package main

import "sort"

// Historical records do not identify whether an enrollment ended through a
// transfer, unenrollment or archival, so do not infer a destination programme.
type PreviousEnrollmentDetail struct {
	StudentEnrollment
	EndDate string
}

func (a *App) previousEnrollmentDetails(scopedEnrollments []StudentEnrollment, admissionID int64) ([]PreviousEnrollmentDetail, error) {
	var details []PreviousEnrollmentDetail
	var ids []int64
	for _, enrollment := range scopedEnrollments {
		if enrollment.AdmissionID == admissionID && !enrollment.Active {
			details = append(details, PreviousEnrollmentDetail{StudentEnrollment: enrollment})
			ids = append(ids, enrollment.ID)
		}
	}
	if len(ids) == 0 {
		return details, nil
	}
	placeholders, args := int64ScopePlaceholders(ids)
	rows, err := a.queryDB(`SELECT enrollment_id, COALESCE(CAST(MAX(effective_from) AS TEXT), '')
 FROM student_enrollment_status_history
 WHERE enrollment_id IN (`+placeholders+`) AND active = 0 AND effective_to IS NULL
 GROUP BY enrollment_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dates := make(map[int64]string)
	for rows.Next() {
		var id int64
		var date string
		if err := rows.Scan(&id, &date); err != nil {
			return nil, err
		}
		dates[id] = date
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range details {
		details[i].EndDate = dates[details[i].ID]
	}
	sort.SliceStable(details, func(i, j int) bool { return details[i].EndDate > details[j].EndDate })
	return details, nil
}

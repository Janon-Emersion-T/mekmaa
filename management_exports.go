package main

import (
	"net/http"
	"strconv"
)

func writeAdmissionsCSV(w http.ResponseWriter, rows []Admission, filter AdmissionsFilter) error {
	writer := newCSVReportWriter(w, "mekmaa-students.csv")
	defer writer.Flush()
	if err := writeCSVReportPreamble(writer, "Mekmaa Students Report",
		CSVReportMetaRow{Section: "filter", Field: "Division", Value: fallbackReportValue(filter.Division, "All divisions")},
		CSVReportMetaRow{Section: "filter", Field: "Search", Value: fallbackReportValue(filter.Search, "All students")},
		CSVReportMetaRow{Section: "report", Field: "Rows", Value: strconv.Itoa(len(rows))},
	); err != nil {
		return err
	}
	if err := writer.Write([]string{"Student ID", "Name", "Admission Date", "Date of Birth", "Gender", "School", "Guardian", "Guardian Phone", "Programmes", "Admission Payment", "Monthly Fee Waived"}); err != nil {
		return err
	}
	for _, row := range rows {
		if err := writer.Write([]string{row.StudentID, row.FullName, row.AdmissionDate, row.DateOfBirth, row.Gender, row.School, row.GuardianName, row.GuardianContactNumber, row.TrainingProgramNames, strconv.FormatBool(row.PaymentCollected), strconv.FormatBool(row.FreeMonthlyFee)}); err != nil {
			return err
		}
	}
	return writer.Error()
}

func writeEnrollmentsCSV(w http.ResponseWriter, rows []StudentEnrollment, division string, admissionID int64) error {
	writer := newCSVReportWriter(w, "mekmaa-enrollments.csv")
	defer writer.Flush()
	if err := writeCSVReportPreamble(writer, "Mekmaa Enrollments Report",
		CSVReportMetaRow{Section: "filter", Field: "Division", Value: fallbackReportValue(division, "All divisions")},
		CSVReportMetaRow{Section: "filter", Field: "Student", Value: strconv.FormatInt(admissionID, 10)},
	); err != nil {
		return err
	}
	if err := writer.Write([]string{"Student", "Student ID", "Programme", "Division", "Enrollment Date", "Active", "Admission Payment Paid", "Monthly Fee Waived", "Discounted Monthly Fee"}); err != nil {
		return err
	}
	for _, row := range rows {
		if err := writer.Write([]string{row.Student.FullName, row.Student.StudentID, row.TrainingProgramName, row.DivisionName, row.EnrollmentDate, strconv.FormatBool(row.Active), strconv.FormatBool(row.AdmissionPaymentPaid), strconv.FormatBool(row.FreeMonthlyFee), strconv.FormatFloat(row.DiscountedMonthlyFee, 'f', 2, 64)}); err != nil {
			return err
		}
	}
	return writer.Error()
}

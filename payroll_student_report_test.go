package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestPayrollStudentNamesUseCalculationSnapshot(t *testing.T) {
	details := []PayrollPaymentCalculationDetail{
		{DetailType: payrollDetailTypeSummary, Label: "3 students"},
		{DetailType: payrollDetailTypePerStudentIncluded, SourceType: "admission", SourceID: 1, Label: "STU-001 · Zoe"},
		{DetailType: payrollDetailTypePerStudentAttendance, SourceType: "admission", SourceID: 2, Label: "STU-002 · Amali"},
		{DetailType: payrollDetailTypePerStudentIncluded, SourceType: "admission", SourceID: 1, Label: "STU-001 · Zoe"},
		{DetailType: payrollDetailTypePerStudentIncluded, SourceType: "admission", SourceID: 3, Label: "STU-003 · Amali"},
		{DetailType: payrollDetailTypePerStudentExcludedFullLeave, Label: "STU-004 · Excluded"},
	}
	if got := payrollStudentNames(details); !reflect.DeepEqual(got, []string{"Amali", "Amali", "Zoe"}) {
		t.Fatalf("names: %#v", got)
	}
}

func TestPayrollStudentReportContainsOnlyNamesAndHeading(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}
	data := TemplateData{HideChrome: true, PayrollRun: &PayrollRun{ID: 2, Label: "August 2026"}, PayrollPayment: &PayrollPayment{UserName: "Teacher", RateSnapshot: 12345}, PayrollStudentNames: []string{"Amali", "Zoe <Test>"}}
	html := renderTemplateToString(t, templates, "payroll-student-report", data)
	for _, text := range []string{"Student Names — Teacher · August 2026", "Amali", "Zoe &lt;Test&gt;"} {
		if !strings.Contains(html, text) {
			t.Errorf("missing %q", text)
		}
	}
	for _, text := range []string{"12345", "Calculation lines", "Rate</th>", "Amount</th>"} {
		if strings.Contains(html, text) {
			t.Errorf("unexpected payroll detail %q", text)
		}
	}
}

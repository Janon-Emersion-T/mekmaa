package main

import (
	"strings"
	"testing"
)

func TestSalaryProfileDashboardV2(t *testing.T) {
	templates, err := buildTemplates()
	if err != nil {
		t.Fatal(err)
	}

	data := TemplateData{
		User: &User{
			ID:   1,
			Name: "Payroll Administrator",
			Permissions: []string{
				"payroll.view",
				"payroll.create",
				"payroll.update",
			},
		},
		SelectedDivisionScope: "all",
		SalaryProfiles: []StaffSalaryProfile{
			{
				ID:               1,
				UserID:           10,
				UserName:         "Coach One",
				CompensationType: SalaryTypeDaily,
				Rate:             2500,
				EffectiveFrom:    "2026-01-01",
				Active:           true,
			},
			{
				ID:               2,
				UserID:           20,
				UserName:         "Coach Two",
				CompensationType: SalaryTypeHourly,
				Rate:             1000,
				EffectiveFrom:    "2026-01-01",
				Active:           false,
			},
		},
	}

	html := renderTemplateToString(
		t,
		templates,
		"salary-profiles",
		data,
	)

	required := []string{
		`id="salary-total"`,
		`id="salary-active"`,
		`id="salary-staff"`,
		`id="salary-methods"`,
		`id="salary-search"`,
		`id="salary-type-filter"`,
		`id="salary-status-filter"`,
		`id="salary-filter-count"`,
		`data-salary-row`,
		`data-type="daily"`,
		`data-type="hourly"`,
		`data-status="active"`,
		`data-status="inactive"`,
		"Coach One",
		"Coach Two",
	}

	for _, value := range required {
		if !strings.Contains(html, value) {
			t.Errorf("salary dashboard missing %q", value)
		}
	}
}

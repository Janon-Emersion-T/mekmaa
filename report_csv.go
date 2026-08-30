package main

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type CSVReportMetaRow struct {
	Section string
	Field   string
	Value   string
}

func newCSVReportWriter(
	w http.ResponseWriter,
	filename string,
) *csv.Writer {
	w.Header().Set(
		"Content-Type",
		"text/csv; charset=utf-8",
	)
	w.Header().Set(
		"Content-Disposition",
		fmt.Sprintf(
			`attachment; filename="%s"`,
			filename,
		),
	)

	return csv.NewWriter(w)
}

func writeCSVReportPreamble(
	writer *csv.Writer,
	title string,
	rows ...CSVReportMetaRow,
) error {
	if err := writer.Write(
		[]string{
			"Section",
			"Field",
			"Value",
		},
	); err != nil {
		return err
	}

	baseRows := []CSVReportMetaRow{
		{
			Section: "report",
			Field:   "Title",
			Value:   strings.TrimSpace(title),
		},
		{
			Section: "report",
			Field:   "Generated At",
			Value:   formatDateTime(time.Now()),
		},
	}

	baseRows = append(
		baseRows,
		rows...,
	)

	for _, row := range baseRows {
		if strings.TrimSpace(row.Field) == "" ||
			strings.TrimSpace(row.Value) == "" {
			continue
		}

		if err := writer.Write(
			[]string{
				strings.TrimSpace(row.Section),
				strings.TrimSpace(row.Field),
				strings.TrimSpace(row.Value),
			},
		); err != nil {
			return err
		}
	}

	return writer.Write([]string{})
}

func reportPeriodKindLabel(
	kind string,
) string {
	switch strings.ToLower(
		strings.TrimSpace(kind),
	) {
	case "week":
		return "Weekly"
	case "month":
		return "Monthly"
	default:
		return "Daily"
	}
}

func reportScopeLabel(
	selectedDivision *Division,
	selectedDivisionScope string,
) string {
	if selectedDivision != nil {
		return selectedDivision.Name
	}

	if strings.EqualFold(
		strings.TrimSpace(
			selectedDivisionScope,
		),
		"all",
	) {
		return "All divisions"
	}

	return "Authorized divisions"
}

func fallbackReportValue(
	value string,
	fallback string,
) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}

	return value
}

package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

type financeLedgerOptionResponse struct {
	Quantities []int `json:"quantities,omitempty"`
}

func (a *App) financeLedgerOptionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	kind := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))

	switch kind {
	case "booking_quantities":
		a.financeLedgerBookingQuantities(w, r)
	default:
		http.Error(w, "invalid ledger option request", http.StatusBadRequest)
	}
}

func (a *App) financeLedgerBookingQuantities(w http.ResponseWriter, r *http.Request) {
	activity := strings.TrimSpace(r.URL.Query().Get("activity"))

	query := `
		SELECT DISTINCT quantity
		FROM pricing_rules
		WHERE quantity > 0
	`
	args := []any{}

	if activity != "" {
		query += ` AND LOWER(activity) = LOWER(?)`
		args = append(args, activity)
	}

	query += ` ORDER BY quantity ASC`

	rows, err := a.db.Query(query, args...)
	if err != nil {
		http.Error(w, "could not load booking quantities", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	response := financeLedgerOptionResponse{
		Quantities: make([]int, 0),
	}

	for rows.Next() {
		var quantity int
		if err := rows.Scan(&quantity); err != nil {
			http.Error(w, "could not load booking quantities", http.StatusInternalServerError)
			return
		}
		response.Quantities = append(response.Quantities, quantity)
	}

	if err := rows.Err(); err != nil {
		http.Error(w, "could not load booking quantities", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

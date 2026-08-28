package main

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/skip2/go-qrcode"
	"log"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) staffDirectoryHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodGet {
		http.Error(
			w,
			"method not allowed",
			http.StatusMethodNotAllowed,
		)
		return
	}

	user, _ := a.currentUser(r.Context())

	divisionIDs, ok := a.studentGroupDivisionScope(
		w,
		r,
		user,
	)
	if !ok {
		return
	}

	staff, err := a.listAssignableGroupStaffByDivisionIDs(
		divisionIDs,
	)
	if err != nil {
		log.Printf("list staff directory users: %v", err)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	if err := a.hydrateStaffDirectoryUserDivisions(staff); err != nil {
		log.Printf(
			"hydrate staff directory divisions: %v",
			err,
		)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	groups, err := a.listStudentGroupsByDivisionIDs(
		divisionIDs,
	)
	if err != nil {
		log.Printf("list staff directory groups: %v", err)

		http.Error(
			w,
			"internal server error",
			http.StatusInternalServerError,
		)
		return
	}

	data := a.newTemplateData(w, r, user)

	data.Title = "Staff Directory"
	data.Description =
		"Review division staff and their operational assignments."

	data.StaffDirectoryRows = buildStaffDirectoryRows(
		staff,
		groups,
	)

	a.render(
		w,
		"staff-directory",
		data,
		http.StatusOK,
	)
}

func staffIDCardNumber(userID int64) string {
	return fmt.Sprintf("STF/%04d", userID)
}

func staffIDCardQRCodeValue(user *User) string {
	if user == nil {
		return ""
	}
	parts := []string{
		"Mekmaa Staff",
		"ID: " + staffIDCardNumber(user.ID),
		"Name: " + strings.TrimSpace(user.Name),
		"Email: " + strings.TrimSpace(user.Email),
	}
	return strings.Join(parts, "\n")
}

func generateQRCodeDataURI(value string, size int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if size <= 0 {
		size = 256
	}
	png, err := qrcode.Encode(value, qrcode.Medium, size)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

func (a *App) staffIDCardHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, _ := a.currentUser(r.Context())
	staffID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || staffID <= 0 {
		http.Error(w, "invalid staff id", http.StatusBadRequest)
		return
	}

	divisionIDs, ok := a.requireOperationalDivisionScope(w, r, user)
	if !ok {
		return
	}

	staff, err := a.findUserByID(staffID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "staff member not found", http.StatusNotFound)
			return
		}
		log.Printf("find staff for id card: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	if !canViewAllDivisions(user) {
		allowed := make(map[int64]struct{}, len(divisionIDs))
		for _, divisionID := range divisionIDs {
			allowed[divisionID] = struct{}{}
		}
		visible := false
		for _, divisionID := range staff.DivisionIDs {
			if _, ok := allowed[divisionID]; ok {
				visible = true
				break
			}
		}
		if !visible {
			a.writeDivisionForbidden(w, r, user)
			return
		}
	}

	qrDataURI, err := generateQRCodeDataURI(staffIDCardQRCodeValue(staff), 256)
	if err != nil {
		log.Printf("generate staff id qr: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	data := a.newTemplateData(w, r, user)
	data.Title = "Staff ID"
	data.Description = "Printable staff identity card."
	data.HideChrome = true
	data.SelectedStaff = staff
	data.StaffIDCardQRCodeDataURI = qrDataURI
	a.render(w, "staff-id-card", data, http.StatusOK)
}

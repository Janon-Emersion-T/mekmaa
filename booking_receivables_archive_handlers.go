package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) bookingReceivablesArchiveHandler(w http.ResponseWriter, r *http.Request) {
	a.bookingReceivablesArchiveAction(w, r, false)
}

func (a *App) bookingReceivablesRestoreHandler(w http.ResponseWriter, r *http.Request) {
	a.bookingReceivablesArchiveAction(w, r, true)
}

func (a *App) bookingReceivablesArchiveAction(
	w http.ResponseWriter,
	r *http.Request,
	restore bool,
) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user, ok := a.currentUser(r.Context())
	if !ok || !containsRole(user.Roles, "superadmin") {
		http.Error(w, "forbidden", http.StatusForbidden)
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

	scheduleID, err := strconv.ParseInt(
		r.FormValue("schedule_id"), 10, 64,
	)
	if err != nil || scheduleID <= 0 {
		http.Error(w, "invalid booking id", http.StatusBadRequest)
		return
	}

	// This register excludes one-to-one bookings.
	oneToOneIDs, err := a.oneToOneScheduleIDSet()
	if err != nil {
		log.Printf("booking archive scope check: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if _, found := oneToOneIDs[scheduleID]; found {
		http.Error(w, "booking belongs to another workflow", http.StatusForbidden)
		return
	}

	if restore {
		err = a.restoreBookingReceivable(scheduleID, user.ID)
	} else {
		reason := strings.TrimSpace(r.FormValue("reason"))
		if reason == "" {
			http.Error(w, "archive reason is required", http.StatusBadRequest)
			return
		}
		err = a.archiveBookingReceivable(scheduleID, user.ID, reason)
	}

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "booking not found", http.StatusNotFound)
			return
		}
		log.Printf("booking receivables archive action: %v", err)
		http.Error(w, "unable to update booking archive", http.StatusConflict)
		return
	}

	if restore {
		a.setFlash(w, "Booking restored to active register.")
	} else {
		a.setFlash(w, "Booking archived. Financial records preserved.")
	}

	http.Redirect(
		w,
		r,
		"/admin/finance/receivables/bookings",
		http.StatusSeeOther,
	)
}

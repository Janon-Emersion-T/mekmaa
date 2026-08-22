package main

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func parseTournamentRecordedAt(raw string, label string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, errors.New(label + " is required")
	}
	recordedAt, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, errors.New(label + " is invalid")
	}
	return recordedAt, nil
}

func parseTournamentHeaderForm(r *http.Request) (string, int64, string, int, float64, int64, time.Time, string, error) {
	gameID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("game_id")), 10, 64)
	if err != nil || gameID <= 0 {
		return "", 0, "", 0, 0, 0, time.Time{}, "", errors.New("select a game")
	}
	participantCount, err := strconv.Atoi(strings.TrimSpace(r.FormValue("participant_count")))
	if err != nil {
		return "", 0, "", 0, 0, 0, time.Time{}, "", errors.New("valid number of participants is required")
	}
	entryFee, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("entry_fee")), 64)
	if err != nil {
		return "", 0, "", 0, 0, 0, time.Time{}, "", errors.New("valid entry fee is required")
	}
	accountID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("entry_fee_finance_account_id")), 10, 64)
	if err != nil || accountID <= 0 {
		return "", 0, "", 0, 0, 0, time.Time{}, "", errors.New("select an entry fee finance account")
	}
	recordedAt, err := parseTournamentRecordedAt(
		r.FormValue("entry_fee_recorded_at"),
		"entry fee transaction date",
	)
	if err != nil {
		return "", 0, "", 0, 0, 0, time.Time{}, "", err
	}
	return strings.TrimSpace(r.FormValue("name")),
		gameID,
		strings.TrimSpace(r.FormValue("tournament_date")),
		participantCount,
		entryFee,
		accountID,
		recordedAt,
		strings.TrimSpace(r.FormValue("notes")),
		nil
}

func sportsFinanceAccounts(app *App) ([]FinanceAccount, error) {
	sportsID, err := divisionIDByCode(app.db, divisionCodeSports)
	if err != nil {
		return nil, err
	}
	return app.listFinanceAccountsByDivisionIDs([]int64{sportsID}, true)
}

func (a *App) buildTournamentTemplateData(
	w http.ResponseWriter,
	r *http.Request,
	user *User,
) (TemplateData, error) {
	data := a.newTemplateData(w, r, user)
	games, err := a.listGames(true)
	if err != nil {
		return data, err
	}
	accounts, err := sportsFinanceAccounts(a)
	if err != nil {
		return data, err
	}
	data.Games = games
	data.FinanceAccounts = accounts
	return data, nil
}

func (a *App) tournamentManagementHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user, _ := a.currentUser(r.Context())
	data, err := a.buildTournamentTemplateData(w, r, user)
	if err != nil {
		log.Printf("build tournament management data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	tournaments, err := a.listTournaments()
	if err != nil {
		log.Printf("list tournaments: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Title = "Tournaments"
	data.Description = "Track tournament income, sponsorships, referee and scorer payments, and operating expenses."
	data.Tournaments = tournaments
	a.render(w, "tournaments-management", data, http.StatusOK)
}

func (a *App) tournamentDetailHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tournamentID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("id")), 10, 64)
	if err != nil || tournamentID <= 0 {
		http.Error(w, "invalid tournament", http.StatusBadRequest)
		return
	}
	user, _ := a.currentUser(r.Context())
	data, err := a.buildTournamentTemplateData(w, r, user)
	if err != nil {
		log.Printf("build tournament detail data: %v", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	tournament, err := a.findTournamentByID(tournamentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		log.Printf("find tournament %d: %v", tournamentID, err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	data.Title = tournament.Name + " Tournament"
	data.Description = "Manage tournament income, expenses, and ledger-linked records."
	data.SelectedTournament = tournament
	a.render(w, "tournament-detail", data, http.StatusOK)
}

func (a *App) createTournamentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	name, gameID, tournamentDate, participantCount, entryFee, accountID, recordedAt, notes, err := parseTournamentHeaderForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tournamentID, err := a.createTournament(
		name,
		gameID,
		tournamentDate,
		participantCount,
		entryFee,
		accountID,
		recordedAt,
		notes,
		currentUserID(r),
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	a.setFlash(w, "Tournament created.")
	http.Redirect(
		w,
		r,
		"/admin/tournaments/view?id="+strconv.FormatInt(tournamentID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) updateTournamentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	tournamentID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("tournament_id")), 10, 64)
	if err != nil || tournamentID <= 0 {
		http.Error(w, "invalid tournament", http.StatusBadRequest)
		return
	}
	name, gameID, tournamentDate, participantCount, entryFee, accountID, recordedAt, notes, err := parseTournamentHeaderForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.updateTournament(
		tournamentID,
		name,
		gameID,
		tournamentDate,
		participantCount,
		entryFee,
		accountID,
		recordedAt,
		notes,
		currentUserID(r),
	); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(
			w,
			r,
			"/admin/tournaments/view?id="+strconv.FormatInt(tournamentID, 10),
			http.StatusSeeOther,
		)
		return
	}
	a.setFlash(w, "Tournament updated.")
	http.Redirect(
		w,
		r,
		"/admin/tournaments/view?id="+strconv.FormatInt(tournamentID, 10),
		http.StatusSeeOther,
	)
}

func (a *App) createTournamentSponsorshipHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	tournamentID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("tournament_id")), 10, 64)
	if err != nil || tournamentID <= 0 {
		http.Error(w, "invalid tournament", http.StatusBadRequest)
		return
	}
	accountID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("finance_account_id")), 10, 64)
	if err != nil || accountID <= 0 {
		http.Error(w, "select a finance account", http.StatusBadRequest)
		return
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil {
		http.Error(w, "valid sponsorship amount is required", http.StatusBadRequest)
		return
	}
	recordedAt, err := parseTournamentRecordedAt(r.FormValue("recorded_at"), "sponsorship date")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createTournamentSponsorship(
		tournamentID,
		strings.TrimSpace(r.FormValue("sponsor_name")),
		strings.TrimSpace(r.FormValue("description")),
		amount,
		accountID,
		recordedAt,
		currentUserID(r),
	); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, "/admin/tournaments/view?id="+strconv.FormatInt(tournamentID, 10), http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Sponsorship added.")
	http.Redirect(w, r, "/admin/tournaments/view?id="+strconv.FormatInt(tournamentID, 10), http.StatusSeeOther)
}

func (a *App) createTournamentOfficialPaymentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	tournamentID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("tournament_id")), 10, 64)
	if err != nil || tournamentID <= 0 {
		http.Error(w, "invalid tournament", http.StatusBadRequest)
		return
	}
	accountID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("finance_account_id")), 10, 64)
	if err != nil || accountID <= 0 {
		http.Error(w, "select a finance account", http.StatusBadRequest)
		return
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil {
		http.Error(w, "valid payment amount is required", http.StatusBadRequest)
		return
	}
	recordedAt, err := parseTournamentRecordedAt(r.FormValue("recorded_at"), "payment date")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createTournamentOfficialPayment(
		tournamentID,
		strings.TrimSpace(r.FormValue("person_name")),
		strings.TrimSpace(r.FormValue("role")),
		strings.TrimSpace(r.FormValue("description")),
		amount,
		accountID,
		recordedAt,
		currentUserID(r),
	); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, "/admin/tournaments/view?id="+strconv.FormatInt(tournamentID, 10), http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Official payment added.")
	http.Redirect(w, r, "/admin/tournaments/view?id="+strconv.FormatInt(tournamentID, 10), http.StatusSeeOther)
}

func (a *App) createTournamentExpenseHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	tournamentID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("tournament_id")), 10, 64)
	if err != nil || tournamentID <= 0 {
		http.Error(w, "invalid tournament", http.StatusBadRequest)
		return
	}
	accountID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("finance_account_id")), 10, 64)
	if err != nil || accountID <= 0 {
		http.Error(w, "select a finance account", http.StatusBadRequest)
		return
	}
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil {
		http.Error(w, "valid expense amount is required", http.StatusBadRequest)
		return
	}
	recordedAt, err := parseTournamentRecordedAt(r.FormValue("recorded_at"), "expense date")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.createTournamentExpense(
		tournamentID,
		r.FormValue("expense_type"),
		strings.TrimSpace(r.FormValue("item_name")),
		strings.TrimSpace(r.FormValue("description")),
		amount,
		accountID,
		recordedAt,
		currentUserID(r),
	); err != nil {
		a.setFlash(w, err.Error())
		http.Redirect(w, r, "/admin/tournaments/view?id="+strconv.FormatInt(tournamentID, 10), http.StatusSeeOther)
		return
	}
	a.setFlash(w, "Expense added.")
	http.Redirect(w, r, "/admin/tournaments/view?id="+strconv.FormatInt(tournamentID, 10), http.StatusSeeOther)
}

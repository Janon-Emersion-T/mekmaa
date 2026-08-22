package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	tournamentExpenseTypePrizes       = "prizes"
	tournamentExpenseTypeRefreshments = "refreshments"
	tournamentExpenseTypeOther        = "other"
)

func tournamentEntryIncomeTotal(participantCount int, entryFee float64) float64 {
	if participantCount <= 0 || entryFee <= 0 {
		return 0
	}
	return normalizeMoney(float64(participantCount) * normalizeMoney(entryFee))
}

func findGameByIDQuery(queryer sqlQueryer, gameID int64) (*Game, error) {
	row := queryer.QueryRow(`
		SELECT id, name, activity, COALESCE(description, ''), active, sort_order, created_at, updated_at
		FROM games
		WHERE id = $1
	`, gameID)

	var game Game
	var active int
	if err := row.Scan(
		&game.ID,
		&game.Name,
		&game.Activity,
		&game.Description,
		&active,
		&game.SortOrder,
		&game.CreatedAt,
		&game.UpdatedAt,
	); err != nil {
		return nil, err
	}
	game.Active = active == 1
	return &game, nil
}

func normalizeTournamentExpenseType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case tournamentExpenseTypePrizes:
		return tournamentExpenseTypePrizes
	case tournamentExpenseTypeRefreshments:
		return tournamentExpenseTypeRefreshments
	default:
		return tournamentExpenseTypeOther
	}
}

func tournamentExpenseTypeLabel(value string) string {
	switch normalizeTournamentExpenseType(value) {
	case tournamentExpenseTypePrizes:
		return "Gifts, shields, certificates, cash and vouchers"
	case tournamentExpenseTypeRefreshments:
		return "Refreshments"
	default:
		return "Other"
	}
}

func tournamentExpenseCategory(value string) string {
	switch normalizeTournamentExpenseType(value) {
	case tournamentExpenseTypePrizes:
		return "prizes_expense"
	case tournamentExpenseTypeRefreshments:
		return "refreshments_expense"
	default:
		return "other_expense"
	}
}

func tournamentExpenseDescription(value string) string {
	switch normalizeTournamentExpenseType(value) {
	case tournamentExpenseTypePrizes:
		return "Tournament gifts and awards"
	case tournamentExpenseTypeRefreshments:
		return "Tournament refreshments"
	default:
		return "Tournament operating expense"
	}
}

func buildTournamentFinancialSummary(tournament *Tournament) {
	if tournament == nil {
		return
	}
	tournament.EntryIncomeTotal = tournamentEntryIncomeTotal(
		tournament.ParticipantCount,
		tournament.EntryFee,
	)
	var sponsorshipTotal float64
	for _, sponsor := range tournament.Sponsorships {
		sponsorshipTotal += sponsor.Amount
	}
	var officialTotal float64
	for _, payment := range tournament.OfficialPayments {
		officialTotal += payment.Amount
	}
	var expenseTotal float64
	for _, expense := range tournament.Expenses {
		expenseTotal += expense.Amount
	}
	tournament.SponsorshipIncomeTotal = normalizeMoney(sponsorshipTotal)
	tournament.OfficialExpenseTotal = normalizeMoney(officialTotal)
	tournament.OtherExpenseTotal = normalizeMoney(expenseTotal)
	tournament.TotalIncome = normalizeMoney(
		tournament.EntryIncomeTotal + tournament.SponsorshipIncomeTotal,
	)
	tournament.TotalExpense = normalizeMoney(
		tournament.OfficialExpenseTotal + tournament.OtherExpenseTotal,
	)
	tournament.NetIncome = normalizeMoney(
		tournament.TotalIncome - tournament.TotalExpense,
	)
}

func (a *App) listTournaments() ([]Tournament, error) {
	rows, err := a.queryDB(`
		SELECT
			t.id,
			t.name,
			COALESCE(t.game_id, 0),
			COALESCE(g.name, ''),
			COALESCE(t.participant_count, 0),
			COALESCE(t.entry_fee, 0),
			COALESCE(t.tournament_date, ''),
			COALESCE(t.entry_fee_finance_transaction_id, 0),
			COALESCE(t.entry_fee_finance_account_id, 0),
			COALESCE(fa.name, ''),
			t.entry_fee_recorded_at,
			COALESCE(t.notes, ''),
			t.created_at,
			t.updated_at,
			COALESCE((
				SELECT SUM(amount)
				FROM tournament_sponsorships
				WHERE tournament_id = t.id
			), 0),
			COALESCE((
				SELECT SUM(amount)
				FROM tournament_official_payments
				WHERE tournament_id = t.id
			), 0),
			COALESCE((
				SELECT SUM(amount)
				FROM tournament_expenses
				WHERE tournament_id = t.id
			), 0)
		FROM tournaments t
		LEFT JOIN games g ON g.id = t.game_id
		LEFT JOIN finance_accounts fa ON fa.id = t.entry_fee_finance_account_id
		ORDER BY t.tournament_date DESC, t.created_at DESC, t.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tournaments := make([]Tournament, 0)
	for rows.Next() {
		var tournament Tournament
		var entryRecordedAt sql.NullTime
		if err := rows.Scan(
			&tournament.ID,
			&tournament.Name,
			&tournament.GameID,
			&tournament.GameName,
			&tournament.ParticipantCount,
			&tournament.EntryFee,
			&tournament.TournamentDate,
			&tournament.EntryFeeFinanceTransactionID,
			&tournament.EntryFeeFinanceAccountID,
			&tournament.EntryFeeFinanceAccountName,
			&entryRecordedAt,
			&tournament.Notes,
			&tournament.CreatedAt,
			&tournament.UpdatedAt,
			&tournament.SponsorshipIncomeTotal,
			&tournament.OfficialExpenseTotal,
			&tournament.OtherExpenseTotal,
		); err != nil {
			return nil, err
		}
		if entryRecordedAt.Valid {
			tournament.EntryFeeRecordedAt = entryRecordedAt.Time
		}
		tournament.EntryIncomeTotal = tournamentEntryIncomeTotal(
			tournament.ParticipantCount,
			tournament.EntryFee,
		)
		tournament.TotalIncome = normalizeMoney(
			tournament.EntryIncomeTotal + tournament.SponsorshipIncomeTotal,
		)
		tournament.TotalExpense = normalizeMoney(
			tournament.OfficialExpenseTotal + tournament.OtherExpenseTotal,
		)
		tournament.NetIncome = normalizeMoney(
			tournament.TotalIncome - tournament.TotalExpense,
		)
		tournaments = append(tournaments, tournament)
	}

	return tournaments, rows.Err()
}

func (a *App) findTournamentByID(tournamentID int64) (*Tournament, error) {
	row := a.queryRowDB(`
		SELECT
			t.id,
			t.name,
			COALESCE(t.game_id, 0),
			COALESCE(g.name, ''),
			COALESCE(t.participant_count, 0),
			COALESCE(t.entry_fee, 0),
			COALESCE(t.tournament_date, ''),
			COALESCE(t.entry_fee_finance_transaction_id, 0),
			COALESCE(t.entry_fee_finance_account_id, 0),
			COALESCE(fa.name, ''),
			t.entry_fee_recorded_at,
			COALESCE(t.notes, ''),
			t.created_at,
			t.updated_at
		FROM tournaments t
		LEFT JOIN games g ON g.id = t.game_id
		LEFT JOIN finance_accounts fa ON fa.id = t.entry_fee_finance_account_id
		WHERE t.id = ?
	`, tournamentID)

	var tournament Tournament
	var entryRecordedAt sql.NullTime
	if err := row.Scan(
		&tournament.ID,
		&tournament.Name,
		&tournament.GameID,
		&tournament.GameName,
		&tournament.ParticipantCount,
		&tournament.EntryFee,
		&tournament.TournamentDate,
		&tournament.EntryFeeFinanceTransactionID,
		&tournament.EntryFeeFinanceAccountID,
		&tournament.EntryFeeFinanceAccountName,
		&entryRecordedAt,
		&tournament.Notes,
		&tournament.CreatedAt,
		&tournament.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if entryRecordedAt.Valid {
		tournament.EntryFeeRecordedAt = entryRecordedAt.Time
	}

	sponsorships, err := a.listTournamentSponsorships(tournament.ID)
	if err != nil {
		return nil, err
	}
	officialPayments, err := a.listTournamentOfficialPayments(tournament.ID)
	if err != nil {
		return nil, err
	}
	expenses, err := a.listTournamentExpenses(tournament.ID)
	if err != nil {
		return nil, err
	}

	tournament.Sponsorships = sponsorships
	tournament.OfficialPayments = officialPayments
	tournament.Expenses = expenses
	buildTournamentFinancialSummary(&tournament)
	return &tournament, nil
}

func (a *App) listTournamentSponsorships(tournamentID int64) ([]TournamentSponsorship, error) {
	rows, err := a.queryDB(`
		SELECT
			ts.id,
			ts.tournament_id,
			ts.sponsor_name,
			COALESCE(ts.description, ''),
			COALESCE(ts.amount, 0),
			COALESCE(ts.finance_transaction_id, 0),
			COALESCE(ts.finance_account_id, 0),
			COALESCE(fa.name, ''),
			ts.recorded_at,
			ts.created_at,
			ts.updated_at
		FROM tournament_sponsorships ts
		LEFT JOIN finance_accounts fa ON fa.id = ts.finance_account_id
		WHERE ts.tournament_id = ?
		ORDER BY ts.recorded_at DESC, ts.id DESC
	`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sponsorships := make([]TournamentSponsorship, 0)
	for rows.Next() {
		var sponsorship TournamentSponsorship
		if err := rows.Scan(
			&sponsorship.ID,
			&sponsorship.TournamentID,
			&sponsorship.SponsorName,
			&sponsorship.Description,
			&sponsorship.Amount,
			&sponsorship.FinanceTransactionID,
			&sponsorship.FinanceAccountID,
			&sponsorship.FinanceAccountName,
			&sponsorship.RecordedAt,
			&sponsorship.CreatedAt,
			&sponsorship.UpdatedAt,
		); err != nil {
			return nil, err
		}
		sponsorships = append(sponsorships, sponsorship)
	}
	return sponsorships, rows.Err()
}

func (a *App) listTournamentOfficialPayments(tournamentID int64) ([]TournamentOfficialPayment, error) {
	rows, err := a.queryDB(`
		SELECT
			top.id,
			top.tournament_id,
			top.person_name,
			COALESCE(top.role, ''),
			COALESCE(top.description, ''),
			COALESCE(top.amount, 0),
			COALESCE(top.finance_transaction_id, 0),
			COALESCE(top.finance_account_id, 0),
			COALESCE(fa.name, ''),
			top.recorded_at,
			top.created_at,
			top.updated_at
		FROM tournament_official_payments top
		LEFT JOIN finance_accounts fa ON fa.id = top.finance_account_id
		WHERE top.tournament_id = ?
		ORDER BY top.recorded_at DESC, top.id DESC
	`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	payments := make([]TournamentOfficialPayment, 0)
	for rows.Next() {
		var payment TournamentOfficialPayment
		if err := rows.Scan(
			&payment.ID,
			&payment.TournamentID,
			&payment.PersonName,
			&payment.Role,
			&payment.Description,
			&payment.Amount,
			&payment.FinanceTransactionID,
			&payment.FinanceAccountID,
			&payment.FinanceAccountName,
			&payment.RecordedAt,
			&payment.CreatedAt,
			&payment.UpdatedAt,
		); err != nil {
			return nil, err
		}
		payments = append(payments, payment)
	}
	return payments, rows.Err()
}

func (a *App) listTournamentExpenses(tournamentID int64) ([]TournamentExpense, error) {
	rows, err := a.queryDB(`
		SELECT
			te.id,
			te.tournament_id,
			COALESCE(te.expense_type, ''),
			COALESCE(te.item_name, ''),
			COALESCE(te.description, ''),
			COALESCE(te.amount, 0),
			COALESCE(te.finance_transaction_id, 0),
			COALESCE(te.finance_account_id, 0),
			COALESCE(fa.name, ''),
			te.recorded_at,
			te.created_at,
			te.updated_at
		FROM tournament_expenses te
		LEFT JOIN finance_accounts fa ON fa.id = te.finance_account_id
		WHERE te.tournament_id = ?
		ORDER BY te.recorded_at DESC, te.id DESC
	`, tournamentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	expenses := make([]TournamentExpense, 0)
	for rows.Next() {
		var expense TournamentExpense
		if err := rows.Scan(
			&expense.ID,
			&expense.TournamentID,
			&expense.ExpenseType,
			&expense.ItemName,
			&expense.Description,
			&expense.Amount,
			&expense.FinanceTransactionID,
			&expense.FinanceAccountID,
			&expense.FinanceAccountName,
			&expense.RecordedAt,
			&expense.CreatedAt,
			&expense.UpdatedAt,
		); err != nil {
			return nil, err
		}
		expenses = append(expenses, expense)
	}
	return expenses, rows.Err()
}

func loadTournamentBaseTx(tx *sql.Tx, tournamentID int64) (*Tournament, error) {
	row := tx.QueryRow(`
		SELECT
			t.id,
			t.name,
			COALESCE(t.game_id, 0),
			COALESCE(g.name, ''),
			COALESCE(t.participant_count, 0),
			COALESCE(t.entry_fee, 0),
			COALESCE(t.tournament_date, ''),
			COALESCE(t.entry_fee_finance_transaction_id, 0),
			COALESCE(t.entry_fee_finance_account_id, 0),
			t.entry_fee_recorded_at,
			COALESCE(t.notes, ''),
			t.created_at,
			t.updated_at
		FROM tournaments t
		LEFT JOIN games g ON g.id = t.game_id
		WHERE t.id = $1
	`, tournamentID)
	var tournament Tournament
	var entryRecordedAt sql.NullTime
	if err := row.Scan(
		&tournament.ID,
		&tournament.Name,
		&tournament.GameID,
		&tournament.GameName,
		&tournament.ParticipantCount,
		&tournament.EntryFee,
		&tournament.TournamentDate,
		&tournament.EntryFeeFinanceTransactionID,
		&tournament.EntryFeeFinanceAccountID,
		&entryRecordedAt,
		&tournament.Notes,
		&tournament.CreatedAt,
		&tournament.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("tournament was not found")
		}
		return nil, err
	}
	if entryRecordedAt.Valid {
		tournament.EntryFeeRecordedAt = entryRecordedAt.Time
	}
	return &tournament, nil
}

func validateTournamentInput(
	name string,
	gameID int64,
	tournamentDate string,
	participantCount int,
	entryFee float64,
	recordedAt time.Time,
) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("tournament name is required")
	}
	if gameID <= 0 {
		return errors.New("select a game")
	}
	if participantCount < 0 {
		return errors.New("number of participants must be zero or greater")
	}
	if entryFee < 0 {
		return errors.New("entry fee must be zero or greater")
	}
	if strings.TrimSpace(tournamentDate) == "" {
		return errors.New("tournament date is required")
	}
	if _, err := time.Parse("2006-01-02", strings.TrimSpace(tournamentDate)); err != nil {
		return errors.New("tournament date is invalid")
	}
	if err := validateHistoricalEntryDateValue(
		strings.TrimSpace(tournamentDate),
		"tournament date",
	); err != nil {
		return err
	}
	if err := validateFinanceRecordedAt(recordedAt, "entry fee transaction date"); err != nil {
		return err
	}
	return nil
}

func updateFinanceTransactionForTournamentTx(
	tx *sql.Tx,
	transactionID int64,
	accountID int64,
	category string,
	referenceType string,
	referenceID int64,
	sourceType string,
	sourceID int64,
	personName string,
	description string,
	notes string,
	amount float64,
	recordedAt time.Time,
	recordedByUserID int64,
) error {
	if transactionID <= 0 {
		return errors.New("finance transaction is required")
	}
	amount = normalizeMoney(amount)
	if amount == 0 {
		return errors.New("finance transaction amount must not be zero")
	}
	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		return err
	}
	if !account.IsActive {
		return errors.New("selected finance account is inactive")
	}
	sportsID, err := divisionIDByCodeTx(tx, divisionCodeSports)
	if err != nil {
		return err
	}
	if account.DivisionID != sportsID {
		return errors.New("selected finance account must belong to Indoor Sports")
	}
	transactionType := financeTxnTypeIncome
	if amount < 0 {
		transactionType = financeTxnTypeExpense
	}
	result, err := tx.Exec(`
		UPDATE finance_transactions
		SET
			division_id = $1,
			category = $2,
			approval_status = $3,
			transaction_type = $4,
			reference_type = $5,
			reference_id = $6,
			source_type = $7,
			source_id = $8,
			finance_account_id = $9,
			person_name = $10,
			description = $11,
			notes = $12,
			payment_method = $13,
			amount = $14,
			recorded_by_user_id = $15,
			approved_by_user_id = $16,
			recorded_at = $17,
			approved_at = $18,
			updated_at = $19
		WHERE id = $20
		  AND voided_at IS NULL
	`,
		sportsID,
		category,
		financeApprovalApproved,
		transactionType,
		referenceType,
		nullIfZero(referenceID),
		sourceType,
		nullIfZero(sourceID),
		account.ID,
		truncateString(strings.TrimSpace(personName), 120),
		truncateString(strings.TrimSpace(description), 300),
		truncateString(strings.TrimSpace(notes), 400),
		financePaymentMethodForAccount(account.AccountType),
		amount,
		nullableExistingUserIDTx(tx, recordedByUserID),
		nullableExistingUserIDTx(tx, recordedByUserID),
		recordedAt.UTC(),
		recordedAt.UTC(),
		time.Now().UTC(),
		transactionID,
	)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("active finance transaction was not found")
	}
	return nil
}

func (a *App) createTournament(
	name string,
	gameID int64,
	tournamentDate string,
	participantCount int,
	entryFee float64,
	accountID int64,
	recordedAt time.Time,
	notes string,
	recordedByUserID int64,
) (int64, error) {
	if err := validateTournamentInput(
		name,
		gameID,
		tournamentDate,
		participantCount,
		entryFee,
		recordedAt,
	); err != nil {
		return 0, err
	}
	if err := a.ensureFinanceDateUnlocked(recordedAt, "entry fee transaction date"); err != nil {
		return 0, err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	game, err := findGameByIDQuery(tx, gameID)
	if err != nil || game == nil || !game.Active {
		return 0, errors.New("selected game was not found")
	}
	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		return 0, errors.New("select a valid finance account")
	}
	sportsID, err := divisionIDByCodeTx(tx, divisionCodeSports)
	if err != nil {
		return 0, err
	}
	if account.DivisionID != sportsID {
		return 0, errors.New("selected finance account must belong to Indoor Sports")
	}

	now := time.Now().UTC()
	tournamentID, err := a.insertAndReturnIDTx(
		tx,
		`
		INSERT INTO tournaments (
			name,
			game_id,
			participant_count,
			entry_fee,
			tournament_date,
			entry_fee_finance_account_id,
			entry_fee_recorded_at,
			notes,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		strings.TrimSpace(name),
		gameID,
		participantCount,
		normalizeMoney(entryFee),
		strings.TrimSpace(tournamentDate),
		account.ID,
		recordedAt.UTC(),
		strings.TrimSpace(notes),
		now,
		now,
	)
	if err != nil {
		return 0, err
	}

	entryIncome := tournamentEntryIncomeTotal(participantCount, entryFee)
	if entryIncome > 0 {
		transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
			ReceiptNumber:    financeVoucherReference("MKM-TRN-INC", recordedAt),
			ReferenceNumber:  financeVoucherReference("MKM-TRN-INC", recordedAt),
			DivisionID:       sportsID,
			Category:         "tournament_entry_income",
			ApprovalStatus:   financeApprovalApproved,
			TransactionType:  financeTxnTypeIncome,
			ReferenceType:    "tournament",
			ReferenceID:      tournamentID,
			SourceType:       "tournament",
			SourceID:         tournamentID,
			FinanceAccountID: account.ID,
			PersonName:       strings.TrimSpace(name),
			Description:      fmt.Sprintf("Tournament entry fees · %s", strings.TrimSpace(name)),
			Notes:            strings.TrimSpace(notes),
			PaymentMethod:    financePaymentMethodForAccount(account.AccountType),
			Amount:           entryIncome,
			RecordedByUserID: recordedByUserID,
			ApprovedByUserID: recordedByUserID,
			RecordedAt:       recordedAt,
			ApprovedAt:       recordedAt,
		})
		if err != nil {
			return 0, err
		}
		if _, err := a.execTxDB(tx, `
			UPDATE tournaments
			SET entry_fee_finance_transaction_id = ?, updated_at = ?
			WHERE id = ?
		`, transactionID, now, tournamentID); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return tournamentID, nil
}

func (a *App) updateTournament(
	tournamentID int64,
	name string,
	gameID int64,
	tournamentDate string,
	participantCount int,
	entryFee float64,
	accountID int64,
	recordedAt time.Time,
	notes string,
	recordedByUserID int64,
) error {
	if tournamentID <= 0 {
		return errors.New("valid tournament is required")
	}
	if err := validateTournamentInput(
		name,
		gameID,
		tournamentDate,
		participantCount,
		entryFee,
		recordedAt,
	); err != nil {
		return err
	}
	if err := a.ensureFinanceDateUnlocked(recordedAt, "entry fee transaction date"); err != nil {
		return err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	existing, err := loadTournamentBaseTx(tx, tournamentID)
	if err != nil {
		return err
	}
	game, err := findGameByIDQuery(tx, gameID)
	if err != nil || game == nil || !game.Active {
		return errors.New("selected game was not found")
	}
	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		return errors.New("select a valid finance account")
	}
	sportsID, err := divisionIDByCodeTx(tx, divisionCodeSports)
	if err != nil {
		return err
	}
	if account.DivisionID != sportsID {
		return errors.New("selected finance account must belong to Indoor Sports")
	}

	now := time.Now().UTC()
	entryIncome := tournamentEntryIncomeTotal(participantCount, entryFee)
	if entryIncome > 0 {
		if existing.EntryFeeFinanceTransactionID > 0 {
			if err := updateFinanceTransactionForTournamentTx(
				tx,
				existing.EntryFeeFinanceTransactionID,
				account.ID,
				"tournament_entry_income",
				"tournament",
				tournamentID,
				"tournament",
				tournamentID,
				strings.TrimSpace(name),
				fmt.Sprintf("Tournament entry fees · %s", strings.TrimSpace(name)),
				strings.TrimSpace(notes),
				entryIncome,
				recordedAt,
				recordedByUserID,
			); err != nil {
				return err
			}
		} else {
			transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
				ReceiptNumber:    financeVoucherReference("MKM-TRN-INC", recordedAt),
				ReferenceNumber:  financeVoucherReference("MKM-TRN-INC", recordedAt),
				DivisionID:       sportsID,
				Category:         "tournament_entry_income",
				ApprovalStatus:   financeApprovalApproved,
				TransactionType:  financeTxnTypeIncome,
				ReferenceType:    "tournament",
				ReferenceID:      tournamentID,
				SourceType:       "tournament",
				SourceID:         tournamentID,
				FinanceAccountID: account.ID,
				PersonName:       strings.TrimSpace(name),
				Description:      fmt.Sprintf("Tournament entry fees · %s", strings.TrimSpace(name)),
				Notes:            strings.TrimSpace(notes),
				PaymentMethod:    financePaymentMethodForAccount(account.AccountType),
				Amount:           entryIncome,
				RecordedByUserID: recordedByUserID,
				ApprovedByUserID: recordedByUserID,
				RecordedAt:       recordedAt,
				ApprovedAt:       recordedAt,
			})
			if err != nil {
				return err
			}
			existing.EntryFeeFinanceTransactionID = transactionID
		}
	} else if existing.EntryFeeFinanceTransactionID > 0 {
		if err := voidFinanceTransactionTx(
			tx,
			existing.EntryFeeFinanceTransactionID,
			"Tournament entry fee income was removed from the tournament manager.",
			recordedByUserID,
		); err != nil {
			return err
		}
		existing.EntryFeeFinanceTransactionID = 0
	}

	_, err = tx.Exec(`
		UPDATE tournaments
		SET
			name = $1,
			game_id = $2,
			participant_count = $3,
			entry_fee = $4,
			tournament_date = $5,
			entry_fee_finance_transaction_id = $6,
			entry_fee_finance_account_id = $7,
			entry_fee_recorded_at = $8,
			notes = $9,
			updated_at = $10
		WHERE id = $11
	`,
		strings.TrimSpace(name),
		gameID,
		participantCount,
		normalizeMoney(entryFee),
		strings.TrimSpace(tournamentDate),
		nullIfZero(existing.EntryFeeFinanceTransactionID),
		account.ID,
		recordedAt.UTC(),
		strings.TrimSpace(notes),
		now,
		tournamentID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) createTournamentSponsorship(
	tournamentID int64,
	sponsorName string,
	description string,
	amount float64,
	accountID int64,
	recordedAt time.Time,
	recordedByUserID int64,
) error {
	if tournamentID <= 0 {
		return errors.New("valid tournament is required")
	}
	if strings.TrimSpace(sponsorName) == "" {
		return errors.New("sponsor name is required")
	}
	amount = normalizeMoney(amount)
	if amount <= 0 {
		return errors.New("sponsorship amount must be greater than zero")
	}
	if err := validateFinanceRecordedAt(recordedAt, "sponsorship date"); err != nil {
		return err
	}
	if err := a.ensureFinanceDateUnlocked(recordedAt, "sponsorship date"); err != nil {
		return err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tournament, err := loadTournamentBaseTx(tx, tournamentID)
	if err != nil {
		return err
	}
	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		return errors.New("select a valid finance account")
	}
	sportsID, err := divisionIDByCodeTx(tx, divisionCodeSports)
	if err != nil {
		return err
	}
	if account.DivisionID != sportsID {
		return errors.New("selected finance account must belong to Indoor Sports")
	}

	now := time.Now().UTC()
	sponsorshipID, err := a.insertAndReturnIDTx(
		tx,
		`
		INSERT INTO tournament_sponsorships (
			tournament_id,
			sponsor_name,
			description,
			amount,
			finance_account_id,
			recorded_at,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		tournamentID,
		strings.TrimSpace(sponsorName),
		strings.TrimSpace(description),
		amount,
		account.ID,
		recordedAt.UTC(),
		now,
		now,
	)
	if err != nil {
		return err
	}

	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    financeVoucherReference("MKM-TRN-SPN", recordedAt),
		ReferenceNumber:  financeVoucherReference("MKM-TRN-SPN", recordedAt),
		DivisionID:       sportsID,
		Category:         "sponsorship_income",
		ApprovalStatus:   financeApprovalApproved,
		TransactionType:  financeTxnTypeIncome,
		ReferenceType:    "tournament",
		ReferenceID:      tournamentID,
		SourceType:       "tournament_sponsorship",
		SourceID:         sponsorshipID,
		FinanceAccountID: account.ID,
		PersonName:       strings.TrimSpace(sponsorName),
		Description:      fmt.Sprintf("Tournament sponsorship · %s", tournament.Name),
		Notes:            strings.TrimSpace(description),
		PaymentMethod:    financePaymentMethodForAccount(account.AccountType),
		Amount:           amount,
		RecordedByUserID: recordedByUserID,
		ApprovedByUserID: recordedByUserID,
		RecordedAt:       recordedAt,
		ApprovedAt:       recordedAt,
	})
	if err != nil {
		return err
	}

	if _, err := a.execTxDB(tx, `
		UPDATE tournament_sponsorships
		SET finance_transaction_id = ?, updated_at = ?
		WHERE id = ?
	`, transactionID, now, sponsorshipID); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) createTournamentOfficialPayment(
	tournamentID int64,
	personName string,
	role string,
	description string,
	amount float64,
	accountID int64,
	recordedAt time.Time,
	recordedByUserID int64,
) error {
	if tournamentID <= 0 {
		return errors.New("valid tournament is required")
	}
	if strings.TrimSpace(personName) == "" {
		return errors.New("person name is required")
	}
	amount = normalizeMoney(amount)
	if amount <= 0 {
		return errors.New("payment amount must be greater than zero")
	}
	if err := validateFinanceRecordedAt(recordedAt, "payment date"); err != nil {
		return err
	}
	if err := a.ensureFinanceDateUnlocked(recordedAt, "payment date"); err != nil {
		return err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tournament, err := loadTournamentBaseTx(tx, tournamentID)
	if err != nil {
		return err
	}
	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		return errors.New("select a valid finance account")
	}
	sportsID, err := divisionIDByCodeTx(tx, divisionCodeSports)
	if err != nil {
		return err
	}
	if account.DivisionID != sportsID {
		return errors.New("selected finance account must belong to Indoor Sports")
	}

	now := time.Now().UTC()
	paymentID, err := a.insertAndReturnIDTx(
		tx,
		`
		INSERT INTO tournament_official_payments (
			tournament_id,
			person_name,
			role,
			description,
			amount,
			finance_account_id,
			recorded_at,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		tournamentID,
		strings.TrimSpace(personName),
		strings.TrimSpace(role),
		strings.TrimSpace(description),
		amount,
		account.ID,
		recordedAt.UTC(),
		now,
		now,
	)
	if err != nil {
		return err
	}

	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    financeVoucherReference("MKM-TRN-OFF", recordedAt),
		ReferenceNumber:  financeVoucherReference("MKM-TRN-OFF", recordedAt),
		DivisionID:       sportsID,
		Category:         "staff_expense",
		ApprovalStatus:   financeApprovalApproved,
		TransactionType:  financeTxnTypeExpense,
		ReferenceType:    "tournament",
		ReferenceID:      tournamentID,
		SourceType:       "tournament_official_payment",
		SourceID:         paymentID,
		FinanceAccountID: account.ID,
		PersonName:       strings.TrimSpace(personName),
		Description:      fmt.Sprintf("Tournament %s payment · %s", strings.TrimSpace(role), tournament.Name),
		Notes:            strings.TrimSpace(description),
		PaymentMethod:    financePaymentMethodForAccount(account.AccountType),
		Amount:           -amount,
		RecordedByUserID: recordedByUserID,
		ApprovedByUserID: recordedByUserID,
		RecordedAt:       recordedAt,
		ApprovedAt:       recordedAt,
	})
	if err != nil {
		return err
	}

	if _, err := a.execTxDB(tx, `
		UPDATE tournament_official_payments
		SET finance_transaction_id = ?, updated_at = ?
		WHERE id = ?
	`, transactionID, now, paymentID); err != nil {
		return err
	}

	return tx.Commit()
}

func (a *App) createTournamentExpense(
	tournamentID int64,
	expenseType string,
	itemName string,
	description string,
	amount float64,
	accountID int64,
	recordedAt time.Time,
	recordedByUserID int64,
) error {
	if tournamentID <= 0 {
		return errors.New("valid tournament is required")
	}
	expenseType = normalizeTournamentExpenseType(expenseType)
	if strings.TrimSpace(itemName) == "" {
		return errors.New("expense item is required")
	}
	amount = normalizeMoney(amount)
	if amount <= 0 {
		return errors.New("expense amount must be greater than zero")
	}
	if err := validateFinanceRecordedAt(recordedAt, "expense date"); err != nil {
		return err
	}
	if err := a.ensureFinanceDateUnlocked(recordedAt, "expense date"); err != nil {
		return err
	}

	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	tournament, err := loadTournamentBaseTx(tx, tournamentID)
	if err != nil {
		return err
	}
	account, err := findFinanceAccountByIDQuery(tx, accountID)
	if err != nil {
		return errors.New("select a valid finance account")
	}
	sportsID, err := divisionIDByCodeTx(tx, divisionCodeSports)
	if err != nil {
		return err
	}
	if account.DivisionID != sportsID {
		return errors.New("selected finance account must belong to Indoor Sports")
	}

	now := time.Now().UTC()
	expenseID, err := a.insertAndReturnIDTx(
		tx,
		`
		INSERT INTO tournament_expenses (
			tournament_id,
			expense_type,
			item_name,
			description,
			amount,
			finance_account_id,
			recorded_at,
			created_at,
			updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		tournamentID,
		expenseType,
		strings.TrimSpace(itemName),
		strings.TrimSpace(description),
		amount,
		account.ID,
		recordedAt.UTC(),
		now,
		now,
	)
	if err != nil {
		return err
	}

	transactionID, err := insertFinanceTransactionTx(tx, financeTransactionCreate{
		ReceiptNumber:    financeVoucherReference("MKM-TRN-EXP", recordedAt),
		ReferenceNumber:  financeVoucherReference("MKM-TRN-EXP", recordedAt),
		DivisionID:       sportsID,
		Category:         tournamentExpenseCategory(expenseType),
		ApprovalStatus:   financeApprovalApproved,
		TransactionType:  financeTxnTypeExpense,
		ReferenceType:    "tournament",
		ReferenceID:      tournamentID,
		SourceType:       "tournament_expense",
		SourceID:         expenseID,
		FinanceAccountID: account.ID,
		PersonName:       strings.TrimSpace(itemName),
		Description:      fmt.Sprintf("%s · %s", tournamentExpenseDescription(expenseType), tournament.Name),
		Notes:            strings.TrimSpace(description),
		PaymentMethod:    financePaymentMethodForAccount(account.AccountType),
		Amount:           -amount,
		RecordedByUserID: recordedByUserID,
		ApprovedByUserID: recordedByUserID,
		RecordedAt:       recordedAt,
		ApprovedAt:       recordedAt,
	})
	if err != nil {
		return err
	}

	if _, err := a.execTxDB(tx, `
		UPDATE tournament_expenses
		SET finance_transaction_id = ?, updated_at = ?
		WHERE id = ?
	`, transactionID, now, expenseID); err != nil {
		return err
	}

	return tx.Commit()
}

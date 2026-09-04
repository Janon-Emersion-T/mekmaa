package main

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

type financeSpecifiedLedgerDefinition struct {
	Key         string
	Title       string
	Description string
	Nature      string
	Match       func(FinanceTransaction) bool
}

type financeSpecifiedLedgerSeed struct {
	Key         string
	Title       string
	Description string
	Nature      string
}

func financeSpecifiedLedgerPeriod(
	fromRaw,
	toRaw string,
) (time.Time, time.Time, string, string, error) {
	// A ledger is an audit view, so its initial range covers all available records.
	if strings.TrimSpace(fromRaw) == "" && strings.TrimSpace(toRaw) == "" {
		fromRaw = companyHistoricalEntryStartDate
	}
	fromDate, toDate, _, _, from, to, _, _, err :=
		financeProfitAndLossPeriod(fromRaw, toRaw, time.Now())
	if err != nil {
		return time.Time{}, time.Time{}, "", "", err
	}
	return fromDate, toDate, from, to, nil
}

func financeSpecifiedLedgerDefinitions() []financeSpecifiedLedgerDefinition {
	return []financeSpecifiedLedgerDefinition{
		{
			Key:         "one_to_one",
			Title:       "1 to 1",
			Description: "All income, refunds, adjustments, and linked finance movements for 1 to 1 packages.",
			Nature:      "income",
			Match: func(tx FinanceTransaction) bool {
				return tx.OneToOneOfferingID > 0
			},
		},
		{
			Key:         "bookings_all_games",
			Title:       "Bookings",
			Description: "All booking collections across every game and quantity.",
			Nature:      "income",
			Match: func(tx FinanceTransaction) bool {
				return tx.Category == "booking_payment"
			},
		},
		{
			Key:         "class_monthly_fees",
			Title:       "Class Monthly Fees",
			Description: "Monthly class and programme fee collections.",
			Nature:      "income",
			Match: func(tx FinanceTransaction) bool {
				return tx.Category == "student_monthly_payment"
			},
		},
		{
			Key:         "tournaments",
			Title:       "Tournaments",
			Description: "Tournament entry fees, sponsorships, official payments, and operating expenses.",
			Nature:      "income",
			Match: func(tx FinanceTransaction) bool {
				return strings.EqualFold(strings.TrimSpace(tx.ReferenceType), "tournament")
			},
		},
		{
			Key:         "banking",
			Title:       "Banking",
			Description: "Bank transfers, bank opening balances, bank adjustments, and bank charges.",
			Nature:      "asset",
			Match: func(tx FinanceTransaction) bool {
				if tx.FinanceAccountType != financeAccountTypeBank {
					return false
				}
				switch tx.TransactionType {
				case financeTxnTypeTransferIn,
					financeTxnTypeTransferOut,
					financeTxnTypeOpeningBalance,
					financeTxnTypeAdjustment:
					return true
				default:
					return false
				}
			},
		},
	}
}

func financeSpecifiedAdmissionLedgerSeed(tx FinanceTransaction) financeSpecifiedLedgerSeed {
	studentName := strings.TrimSpace(tx.StudentName)
	studentID := strings.TrimSpace(tx.StudentID)
	if studentName == "" {
		studentName = "Admission #" + strconv.FormatInt(tx.AdmissionID, 10)
	}
	if studentID != "" {
		studentName += " · " + studentID
	}
	return financeSpecifiedLedgerSeed{
		Key:         "admission-" + strconv.FormatInt(tx.AdmissionID, 10),
		Title:       studentName,
		Description: "Admission payment history for this student.",
		Nature:      "income",
	}
}

func financeSpecifiedAdmissionFallbackLedgerSeed(tx FinanceTransaction) financeSpecifiedLedgerSeed {
	return financeSpecifiedLedgerSeed{
		Key:         "admission-transaction-" + strconv.FormatInt(tx.ID, 10),
		Title:       "Admission payment · " + strings.TrimSpace(tx.ReferenceNumber),
		Description: "Admission payment with no linked student record.",
		Nature:      "income",
	}
}

func financeSpecifiedLedgerSeedForCategory(
	category FinanceCategory,
) financeSpecifiedLedgerSeed {
	return financeSpecifiedLedgerSeed{
		Key:         category.Code,
		Title:       financeCategoryLabel(category.Code),
		Description: "Specified ledger for the " + strings.ToLower(financeCategoryLabel(category.Code)) + " category.",
		Nature:      category.Direction,
	}
}

func financeSpecifiedLedgerCounterparty(
	tx FinanceTransaction,
) string {
	switch {
	case strings.TrimSpace(tx.PersonName) != "":
		return strings.TrimSpace(tx.PersonName)
	case strings.TrimSpace(tx.StudentName) != "":
		return strings.TrimSpace(tx.StudentName)
	case strings.TrimSpace(tx.TrainingProgramName) != "":
		return strings.TrimSpace(tx.TrainingProgramName)
	default:
		return "General"
	}
}

func financeSpecifiedLedgerDescription(
	tx FinanceTransaction,
) string {
	description := strings.TrimSpace(tx.Description)
	switch {
	case tx.Category == "booking_payment" &&
		strings.TrimSpace(tx.BookingActivity) != "":
		if description == "" {
			return activityLabel(tx.BookingActivity)
		}
		return description + " · " + activityLabel(tx.BookingActivity)
	case tx.Category == "student_monthly_payment" &&
		strings.TrimSpace(tx.TrainingProgramName) != "":
		if description == "" {
			return tx.TrainingProgramName
		}
		return description + " · " + tx.TrainingProgramName
	case description != "":
		return description
	default:
		return financeCategoryLabel(tx.Category)
	}
}

func financeSpecifiedLedgerNatureForCategory(
	tx FinanceTransaction,
) string {
	if strings.HasSuffix(tx.Category, "_income") {
		return "income"
	}
	if strings.HasSuffix(tx.Category, "_expense") {
		return "expense"
	}
	if tx.Amount < 0 {
		return "expense"
	}
	return "income"
}

func appendFinanceSpecifiedLedgerEntry(
	ledger *FinanceSpecifiedLedger,
	tx FinanceTransaction,
) {
	entry := FinanceSpecifiedLedgerEntry{
		TransactionID:      tx.ID,
		RecordedAt:         tx.RecordedAt,
		ReferenceNumber:    tx.ReferenceNumber,
		Counterparty:       financeSpecifiedLedgerCounterparty(tx),
		Description:        financeSpecifiedLedgerDescription(tx),
		DivisionName:       tx.DivisionName,
		FinanceAccountName: tx.FinanceAccountName,
	}

	amount := normalizeMoney(absMoney(tx.Amount))

	switch ledger.Nature {
	case "income":
		if tx.Amount >= 0 {
			entry.CreditAmount = amount
			ledger.CreditEntries = append(
				ledger.CreditEntries,
				entry,
			)
			ledger.CreditTotal = normalizeMoney(
				ledger.CreditTotal + amount,
			)
		} else {
			entry.DebitAmount = amount
			ledger.DebitEntries = append(
				ledger.DebitEntries,
				entry,
			)
			ledger.DebitTotal = normalizeMoney(
				ledger.DebitTotal + amount,
			)
		}
	case "expense":
		if tx.Amount < 0 {
			entry.DebitAmount = amount
			ledger.DebitEntries = append(
				ledger.DebitEntries,
				entry,
			)
			ledger.DebitTotal = normalizeMoney(
				ledger.DebitTotal + amount,
			)
		} else {
			entry.CreditAmount = amount
			ledger.CreditEntries = append(
				ledger.CreditEntries,
				entry,
			)
			ledger.CreditTotal = normalizeMoney(
				ledger.CreditTotal + amount,
			)
		}
	default:
		if tx.Amount >= 0 {
			entry.DebitAmount = amount
			ledger.DebitEntries = append(
				ledger.DebitEntries,
				entry,
			)
			ledger.DebitTotal = normalizeMoney(
				ledger.DebitTotal + amount,
			)
		} else {
			entry.CreditAmount = amount
			ledger.CreditEntries = append(
				ledger.CreditEntries,
				entry,
			)
			ledger.CreditTotal = normalizeMoney(
				ledger.CreditTotal + amount,
			)
		}
	}

	ledger.EntryCount++
}

func finalizeFinanceSpecifiedLedger(
	ledger *FinanceSpecifiedLedger,
) {
	sort.Slice(ledger.DebitEntries, func(i, j int) bool {
		if ledger.DebitEntries[i].RecordedAt.Equal(ledger.DebitEntries[j].RecordedAt) {
			return ledger.DebitEntries[i].TransactionID < ledger.DebitEntries[j].TransactionID
		}
		return ledger.DebitEntries[i].RecordedAt.Before(ledger.DebitEntries[j].RecordedAt)
	})
	sort.Slice(ledger.CreditEntries, func(i, j int) bool {
		if ledger.CreditEntries[i].RecordedAt.Equal(ledger.CreditEntries[j].RecordedAt) {
			return ledger.CreditEntries[i].TransactionID < ledger.CreditEntries[j].TransactionID
		}
		return ledger.CreditEntries[i].RecordedAt.Before(ledger.CreditEntries[j].RecordedAt)
	})

	switch ledger.Nature {
	case "income":
		ledger.NetBalance = normalizeMoney(
			ledger.CreditTotal - ledger.DebitTotal,
		)
		ledger.BalanceLabel = "Credit balance"
	case "expense":
		ledger.NetBalance = normalizeMoney(
			ledger.DebitTotal - ledger.CreditTotal,
		)
		ledger.BalanceLabel = "Debit balance"
	default:
		ledger.NetBalance = normalizeMoney(
			ledger.DebitTotal - ledger.CreditTotal,
		)
		ledger.BalanceLabel = "Debit balance"
	}
	if ledger.NetBalance < 0 {
		ledger.NetBalance = absMoney(ledger.NetBalance)
		if ledger.BalanceLabel == "Debit balance" {
			ledger.BalanceLabel = "Credit balance"
		} else {
			ledger.BalanceLabel = "Debit balance"
		}
	}
}

func (a *App) buildFinanceSpecifiedLedgers(
	fromRaw,
	toRaw string,
	divisionIDs []int64,
) ([]FinanceSpecifiedLedger, string, string, error) {
	fromDate, toDate, from, to, err := financeSpecifiedLedgerPeriod(
		fromRaw,
		toRaw,
	)
	if err != nil {
		return nil, "", "", err
	}

	filter := FinanceFilter{}
	if len(divisionIDs) > 0 {
		filter.DivisionIDs = append([]int64(nil), divisionIDs...)
	}
	transactions, err := a.listFinanceTransactionsFiltered(filter)
	if err != nil {
		return nil, "", "", err
	}
	categories, err := a.listFinanceCategories(false)
	if err != nil {
		return nil, "", "", err
	}

	definitions := financeSpecifiedLedgerDefinitions()
	ledgers := make([]FinanceSpecifiedLedger, 0, len(definitions)+len(categories))
	positions := make(map[string]int, len(definitions)+len(categories))

	for _, definition := range definitions {
		positions[definition.Key] = len(ledgers)
		ledgers = append(
			ledgers,
			FinanceSpecifiedLedger{
				Key:         definition.Key,
				Title:       definition.Title,
				Description: definition.Description,
				Nature:      definition.Nature,
			},
		)
	}

	categoryCodes := make(map[string]FinanceCategory, len(categories))
	for _, category := range categories {
		seed := financeSpecifiedLedgerSeedForCategory(category)
		categoryCodes[category.Code] = category
		positions[seed.Key] = len(ledgers)
		ledgers = append(
			ledgers,
			FinanceSpecifiedLedger{
				Key:         seed.Key,
				Title:       seed.Title,
				Description: seed.Description,
				Nature:      seed.Nature,
			},
		)
	}

	dynamic := make(map[string]*FinanceSpecifiedLedger)

	for _, tx := range transactions {
		if !financeTransactionPosted(tx) ||
			!financeTransactionWithinLocalDates(
				tx.RecordedAt,
				fromDate,
				toDate,
			) {
			continue
		}

		if tx.Category == "admission_payment" {
			seed := financeSpecifiedAdmissionFallbackLedgerSeed(tx)
			if tx.AdmissionID > 0 {
				seed = financeSpecifiedAdmissionLedgerSeed(tx)
			}
			ledger, exists := dynamic[seed.Key]
			if !exists {
				ledger = &FinanceSpecifiedLedger{
					Key:         seed.Key,
					Title:       seed.Title,
					Description: seed.Description,
					Nature:      seed.Nature,
				}
				dynamic[seed.Key] = ledger
			}
			appendFinanceSpecifiedLedgerEntry(ledger, tx)
			continue
		}

		matched := false
		for _, definition := range definitions {
			if !definition.Match(tx) {
				continue
			}
			ledger := &ledgers[positions[definition.Key]]
			appendFinanceSpecifiedLedgerEntry(ledger, tx)
			matched = true
			break
		}
		if matched {
			continue
		}

		key := strings.TrimSpace(tx.Category)
		if _, exists := categoryCodes[key]; exists {
			ledger := &ledgers[positions[key]]
			appendFinanceSpecifiedLedgerEntry(ledger, tx)
			continue
		}

		if key == "" {
			key = "other_ledger_movements"
		}
		ledger, exists := dynamic[key]
		if !exists {
			title := "Other Ledger Movements"
			description := "Transactions that do not fit one of the core specified ledgers."
			nature := "asset"
			if key != "other_ledger_movements" {
				title = financeCategoryLabel(key)
				description = "Additional specified ledger generated from finance category history."
				nature = financeSpecifiedLedgerNatureForCategory(tx)
			}
			dynamic[key] = &FinanceSpecifiedLedger{
				Key:         key,
				Title:       title,
				Description: description,
				Nature:      nature,
			}
			ledger = dynamic[key]
		}
		appendFinanceSpecifiedLedgerEntry(ledger, tx)
	}

	for i := range ledgers {
		finalizeFinanceSpecifiedLedger(&ledgers[i])
	}

	dynamicKeys := make([]string, 0, len(dynamic))
	for key := range dynamic {
		dynamicKeys = append(dynamicKeys, key)
	}
	sort.Strings(dynamicKeys)
	for _, key := range dynamicKeys {
		ledger := *dynamic[key]
		finalizeFinanceSpecifiedLedger(&ledger)
		ledgers = append(ledgers, ledger)
	}

	return ledgers, from, to, nil
}

func findFinanceSpecifiedLedger(
	ledgers []FinanceSpecifiedLedger,
	key string,
) *FinanceSpecifiedLedger {
	key = strings.TrimSpace(key)
	for i := range ledgers {
		if ledgers[i].Key == key {
			return &ledgers[i]
		}
	}
	return nil
}

func isFinanceSpecifiedSystemLedgerKey(key string) bool {
	switch strings.TrimSpace(key) {
	case "bookings_all_games",
		"class_monthly_fees",
		"banking":
		return true
	default:
		return false
	}
}

func isFinanceSpecifiedAdmissionLedgerKey(key string) bool {
	return strings.HasPrefix(strings.TrimSpace(key), "admission-")
}

func financeSpecifiedAdmissionLedgers(
	ledgers []FinanceSpecifiedLedger,
) []FinanceSpecifiedLedger {
	filtered := make([]FinanceSpecifiedLedger, 0, len(ledgers))
	for _, ledger := range ledgers {
		if isFinanceSpecifiedAdmissionLedgerKey(ledger.Key) {
			filtered = append(filtered, ledger)
		}
	}
	return filtered
}

func financeSpecifiedSystemLedgers(
	ledgers []FinanceSpecifiedLedger,
) []FinanceSpecifiedLedger {
	filtered := make([]FinanceSpecifiedLedger, 0, len(ledgers))
	for _, ledger := range ledgers {
		if isFinanceSpecifiedSystemLedgerKey(ledger.Key) {
			filtered = append(filtered, ledger)
		}
	}
	return filtered
}

func financeSpecifiedIncomeLedgers(
	ledgers []FinanceSpecifiedLedger,
) []FinanceSpecifiedLedger {
	filtered := make([]FinanceSpecifiedLedger, 0, len(ledgers))
	for _, ledger := range ledgers {
		if isFinanceSpecifiedSystemLedgerKey(ledger.Key) {
			continue
		}
		if isFinanceSpecifiedAdmissionLedgerKey(ledger.Key) {
			continue
		}
		if ledger.Nature == "income" {
			filtered = append(filtered, ledger)
		}
	}
	return filtered
}

func financeSpecifiedExpenseLedgers(
	ledgers []FinanceSpecifiedLedger,
) []FinanceSpecifiedLedger {
	filtered := make([]FinanceSpecifiedLedger, 0, len(ledgers))
	for _, ledger := range ledgers {
		if isFinanceSpecifiedSystemLedgerKey(ledger.Key) {
			continue
		}
		if ledger.Nature == "expense" {
			filtered = append(filtered, ledger)
		}
	}
	return filtered
}

func financeSpecifiedOtherLedgers(
	ledgers []FinanceSpecifiedLedger,
) []FinanceSpecifiedLedger {
	filtered := make([]FinanceSpecifiedLedger, 0, len(ledgers))
	for _, ledger := range ledgers {
		if isFinanceSpecifiedSystemLedgerKey(ledger.Key) {
			continue
		}
		if ledger.Nature != "income" && ledger.Nature != "expense" {
			filtered = append(filtered, ledger)
		}
	}
	return filtered
}

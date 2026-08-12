package main

import (
	"errors"
	"fmt"
	"github.com/skip2/go-qrcode"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (a *App) bookingQuote(schedule SpaceSchedule) (float64, error) {
	pricings, err := a.listPricingRules()
	if err != nil {
		return 0, err
	}
	settings, err := a.getPricingSettings()
	if err != nil {
		return 0, err
	}
	rule := pricingRuleForOption(pricings, schedule.Activity, schedule.Quantity)
	if rule == nil {
		return 0, errors.New("pricing is not configured for this booking")
	}
	amount := priceForRuleSlot(*rule, settings, schedule.SlotDate, schedule.SlotHour)
	if amount <= 0 {
		return 0, errors.New("a positive price is required before creating this booking")
	}
	return amount, nil
}

func money(value float64) string {
	return fmt.Sprintf("LKR %.2f", value)
}

func negate(value float64) float64 {
	return -value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullIfBlank(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func nullIfZero(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func prepareUploadStorage(configuredRoot string) (UploadStorage, error) {
	resolvedRoot, err := resolveUploadRoot(configuredRoot)
	if err != nil {
		return UploadStorage{}, err
	}

	storage := UploadStorage{
		Root:            resolvedRoot,
		EventDir:        filepath.Join(resolvedRoot, "events"),
		StudentPhotoDir: filepath.Join(resolvedRoot, "students", "photos"),
		StudentQRDir:    filepath.Join(resolvedRoot, "students", "qr"),
	}
	if err := os.MkdirAll(storage.Root, 0o755); err != nil {
		return UploadStorage{}, fmt.Errorf("create upload directory %s: %w", storage.Root, err)
	}
	if err := os.MkdirAll(storage.EventDir, 0o755); err != nil {
		return UploadStorage{}, fmt.Errorf("create event upload directory %s: %w", storage.EventDir, err)
	}
	if err := os.MkdirAll(storage.StudentPhotoDir, 0o755); err != nil {
		return UploadStorage{}, fmt.Errorf("create student photo upload directory %s: %w", storage.StudentPhotoDir, err)
	}
	if err := os.MkdirAll(storage.StudentQRDir, 0o755); err != nil {
		return UploadStorage{}, fmt.Errorf("create student qr upload directory %s: %w", storage.StudentQRDir, err)
	}

	for _, dir := range []struct {
		path  string
		label string
	}{
		{storage.EventDir, "event upload"},
		{storage.StudentPhotoDir, "student photo upload"},
		{storage.StudentQRDir, "student qr upload"},
	} {
		probe, err := os.CreateTemp(dir.path, ".mekmaa-write-check-*")
		if err != nil {
			return UploadStorage{}, fmt.Errorf("%s directory is not writable %s: %w", dir.label, dir.path, err)
		}
		probeName := probe.Name()
		if err := probe.Close(); err != nil {
			_ = os.Remove(probeName)
			return UploadStorage{}, fmt.Errorf("close %s write check %s: %w", dir.label, probeName, err)
		}
		if err := os.Remove(probeName); err != nil {
			return UploadStorage{}, fmt.Errorf("remove %s write check %s: %w", dir.label, probeName, err)
		}
	}
	return storage, nil
}

func registerUploadRoutes(mux *http.ServeMux, storage UploadStorage) {
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(storage.Root))))
	// Keep previously persisted /event-images/ paths available during migration.
	mux.Handle("/event-images/", http.StripPrefix("/event-images/", http.FileServer(http.Dir(storage.EventDir))))
}

func resolveUploadRoot(configuredRoot string) (string, error) {
	root := strings.TrimSpace(configuredRoot)
	if root == "" {
		root = defaultUploadDir
	}
	resolvedRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve upload directory %q: %w", root, err)
	}
	return resolvedRoot, nil
}

func (a *App) uploadedEventImagePath(r *http.Request) (string, error) {
	file, header, err := r.FormFile("image")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return "", nil
		}
		return "", errors.New("invalid event image upload")
	}
	defer file.Close()

	return a.uploads.saveEventImage(file, header)
}

func (a *App) uploadedStudentPhotoPath(r *http.Request, fieldName string) (string, error) {
	file, header, err := r.FormFile(fieldName)
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return "", nil
		}
		return "", errors.New("invalid student photo upload")
	}
	defer file.Close()

	return a.uploads.saveStudentPhoto(file, header)
}

func (s UploadStorage) saveEventImage(file multipart.File, header *multipart.FileHeader) (string, error) {
	if header != nil && header.Size > maxEventImageSize {
		return "", errors.New("event image must be 8MB or smaller")
	}

	buf := make([]byte, 512)
	n, err := io.ReadFull(file, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("read uploaded event image: %w", err)
	}
	if n == 0 {
		return "", errors.New("event image is empty")
	}
	contentType := http.DetectContentType(buf[:n])
	ext, ok := eventImageExtension(contentType)
	if !ok {
		return "", errors.New("event image must be a JPEG, PNG or WebP file")
	}

	if err := os.MkdirAll(s.EventDir, 0o755); err != nil {
		return "", fmt.Errorf("prepare event image directory %s: %w", s.EventDir, err)
	}

	filename, err := newEventImageFilename(ext)
	if err != nil {
		return "", err
	}
	targetPath := filepath.Join(s.EventDir, filename)

	dst, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("create uploaded event image %s: %w", targetPath, err)
	}
	complete := false
	closed := false
	defer func() {
		if !closed {
			_ = dst.Close()
		}
		if !complete {
			_ = os.Remove(targetPath)
		}
	}()

	if _, err := dst.Write(buf[:n]); err != nil {
		return "", fmt.Errorf("write uploaded event image %s: %w", targetPath, err)
	}
	remaining := int64(maxEventImageSize - n)
	copied, err := io.Copy(dst, io.LimitReader(file, remaining+1))
	if err != nil {
		return "", fmt.Errorf("copy uploaded event image %s: %w", targetPath, err)
	}
	if int64(n)+copied > maxEventImageSize {
		return "", errors.New("event image must be 8MB or smaller")
	}
	if err := dst.Close(); err != nil {
		closed = true
		return "", fmt.Errorf("close uploaded event image %s: %w", targetPath, err)
	}
	closed = true
	complete = true

	return eventImagePublicPath(filename)
}

func newEventImageFilename(extension string) (string, error) {
	if extension != ".jpg" && extension != ".png" && extension != ".webp" {
		return "", errors.New("unsupported event image extension")
	}
	token, err := generateToken(18)
	if err != nil {
		return "", fmt.Errorf("generate event image filename: %w", err)
	}
	filename := "event-" + strings.ToLower(token) + extension
	if !eventImagePattern.MatchString(filename) {
		return "", errors.New("generated event image filename is invalid")
	}
	return filename, nil
}

func eventImagePublicPath(filename string) (string, error) {
	if !eventImagePattern.MatchString(filename) || filepath.Base(filename) != filename {
		return "", errors.New("invalid event image filename")
	}
	return "/uploads/events/" + filename, nil
}

func eventImageExtension(contentType string) (string, bool) {
	switch contentType {
	case "image/jpeg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "image/webp":
		return ".webp", true
	default:
		return "", false
	}
}

func (s UploadStorage) saveStudentPhoto(file multipart.File, header *multipart.FileHeader) (string, error) {
	if header != nil && header.Size > maxStudentPhotoSize {
		return "", errors.New("student photo must be 5MB or smaller")
	}

	buf := make([]byte, 512)
	n, err := io.ReadFull(file, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("read uploaded student photo: %w", err)
	}
	if n == 0 {
		return "", errors.New("student photo is empty")
	}
	contentType := http.DetectContentType(buf[:n])
	ext, ok := eventImageExtension(contentType)
	if !ok {
		return "", errors.New("student photo must be a JPEG, PNG or WebP file")
	}

	filename, err := newStudentPhotoFilename(ext)
	if err != nil {
		return "", err
	}
	targetPath := filepath.Join(s.StudentPhotoDir, filename)
	dst, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("create uploaded student photo %s: %w", targetPath, err)
	}
	complete := false
	closed := false
	defer func() {
		if !closed {
			_ = dst.Close()
		}
		if !complete {
			_ = os.Remove(targetPath)
		}
	}()

	if _, err := dst.Write(buf[:n]); err != nil {
		return "", fmt.Errorf("write uploaded student photo %s: %w", targetPath, err)
	}
	remaining := int64(maxStudentPhotoSize - n)
	copied, err := io.Copy(dst, io.LimitReader(file, remaining+1))
	if err != nil {
		return "", fmt.Errorf("copy uploaded student photo %s: %w", targetPath, err)
	}
	if int64(n)+copied > maxStudentPhotoSize {
		return "", errors.New("student photo must be 5MB or smaller")
	}
	if err := dst.Close(); err != nil {
		closed = true
		return "", fmt.Errorf("close uploaded student photo %s: %w", targetPath, err)
	}
	closed = true
	complete = true

	return studentPhotoPublicPath(filename)
}

func (s UploadStorage) saveStudentQRCode(studentID, value string) (string, error) {
	filename, err := newStudentQRFilename()
	if err != nil {
		return "", err
	}
	targetPath := filepath.Join(s.StudentQRDir, filename)
	if err := qrcode.WriteFile(strings.TrimSpace(value), qrcode.Medium, 256, targetPath); err != nil {
		return "", fmt.Errorf("write student qr image %s: %w", targetPath, err)
	}
	return studentQRPublicPath(filename)
}

func newStudentPhotoFilename(extension string) (string, error) {
	if extension != ".jpg" && extension != ".png" && extension != ".webp" {
		return "", errors.New("unsupported student photo extension")
	}
	token, err := generateToken(18)
	if err != nil {
		return "", fmt.Errorf("generate student photo filename: %w", err)
	}
	filename := "student-photo-" + strings.ToLower(token) + extension
	if !studentPhotoPattern.MatchString(filename) {
		return "", errors.New("generated student photo filename is invalid")
	}
	return filename, nil
}

func newStudentQRFilename() (string, error) {
	token, err := generateToken(18)
	if err != nil {
		return "", fmt.Errorf("generate student qr filename: %w", err)
	}
	filename := "student-qr-" + strings.ToLower(token) + ".png"
	if !studentQRPattern.MatchString(filename) {
		return "", errors.New("generated student qr filename is invalid")
	}
	return filename, nil
}

func studentPhotoPublicPath(filename string) (string, error) {
	if !studentPhotoPattern.MatchString(filename) || filepath.Base(filename) != filename {
		return "", errors.New("invalid student photo filename")
	}
	return "/uploads/students/photos/" + filename, nil
}

func studentQRPublicPath(filename string) (string, error) {
	if !studentQRPattern.MatchString(filename) || filepath.Base(filename) != filename {
		return "", errors.New("invalid student qr filename")
	}
	return "/uploads/students/qr/" + filename, nil
}

func (s UploadStorage) deleteEventImage(imagePath string) error {
	trimmed := strings.TrimSpace(imagePath)
	if trimmed == "" {
		return nil
	}

	filename := ""
	for _, prefix := range []string{"/uploads/events/", "/event-images/"} {
		if strings.HasPrefix(trimmed, prefix) {
			filename = strings.TrimPrefix(trimmed, prefix)
			break
		}
	}
	if filename == "" {
		return nil
	}
	if !storedEventPattern.MatchString(filename) || filepath.Base(filename) != filename {
		return errors.New("invalid event image path")
	}
	localPath := filepath.Join(s.EventDir, filename)
	if err := os.Remove(localPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete event image %s: %w", localPath, err)
	}
	return nil
}

func (a *App) removeUploadedEventImage(imagePath string) {
	if err := a.uploads.deleteEventImage(imagePath); err != nil {
		log.Printf("remove uploaded event image: %v", err)
	}
}

func (a *App) removeUploadedStudentPhoto(photoPath string) {
	if err := a.uploads.deleteStudentPhoto(photoPath); err != nil {
		log.Printf("remove uploaded student photo: %v", err)
	}
}

func (a *App) removeUploadedStudentQRCode(qrPath string) {
	if err := a.uploads.deleteStudentQRCode(qrPath); err != nil {
		log.Printf("remove uploaded student qr: %v", err)
	}
}

func (s UploadStorage) deleteStudentPhoto(photoPath string) error {
	return s.deleteStudentAsset(photoPath, "/uploads/students/photos/", s.StudentPhotoDir, studentPhotoPattern, "student photo")
}

func (s UploadStorage) deleteStudentQRCode(qrPath string) error {
	return s.deleteStudentAsset(qrPath, "/uploads/students/qr/", s.StudentQRDir, studentQRPattern, "student qr")
}

func (s UploadStorage) deleteStudentAsset(publicPath, prefix, dir string, pattern *regexp.Regexp, label string) error {
	trimmed := strings.TrimSpace(publicPath)
	if trimmed == "" || !strings.HasPrefix(trimmed, prefix) {
		return nil
	}
	filename := strings.TrimPrefix(trimmed, prefix)
	if !pattern.MatchString(filename) || filepath.Base(filename) != filename {
		return fmt.Errorf("invalid %s path", label)
	}
	localPath := filepath.Join(dir, filename)
	if err := os.Remove(localPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete %s %s: %w", label, localPath, err)
	}
	return nil
}

func financeCategoryLabel(value string) string {
	switch value {
	case "admission_payment":
		return "Admission payment"
	case "student_monthly_payment":
		return "Student monthly payment"
	case "booking_payment":
		return "Booking payment"
	case "referral_commission_payment":
		return "Referral commission"
	case "manual_income":
		return "Other income"
	case "sponsorship_income":
		return "Sponsorship income"
	case "other_income":
		return "Other income"
	case "facility_expense":
		return "Facility or court rental"
	case "utilities_expense":
		return "Utilities"
	case "loan_repayment_expense":
		return "Loan repayment"
	case "staff_salary_expense":
		return "Staff salary"
	case "electricity_bills_expense":
		return "Utility bills - Electricity bills"
	case "telephone_bills_expense":
		return "Utility bills - Telephone bills"
	case "maintenance_expense":
		return "Maintenance and repairs"
	case "staff_expense":
		return "Staff and wages"
	case "donation_expense":
		return "Donation"
	case "stationery_expense":
		return "Stationery"
	case "equipment_expense":
		return "Equipment"
	case "sports_supplies_expense":
		return "Sports supplies"
	case "refreshments_expense":
		return "Refreshments and drinks"
	case "prizes_expense":
		return "Prizes and awards"
	case "marketing_expense":
		return "Marketing"
	case "transport_expense":
		return "Transport"
	case "event_expense":
		return "Event expense"
	case "bank_charges_expense":
		return "Bank charges"
	case "internal_transfer":
		return "Internal transfer"
	case "opening_balance":
		return "Opening balance"
	case "cash_adjustment":
		return "Adjustment"
	case "other_expense":
		return "Other expense"
	default:
		return financeCategoryLabelFallback(value)
	}
}

func parsePaymentMonth(value string) (time.Time, error) {
	if len(value) != 7 {
		return time.Time{}, errors.New("a valid payment month is required")
	}
	parsed, err := time.Parse("2006-01", value)
	if err != nil || parsed.Format("2006-01") != value {
		return time.Time{}, errors.New("a valid payment month is required")
	}
	return parsed, nil
}

func paymentMonthLabel(value string) string {
	parsed, err := parsePaymentMonth(value)
	if err != nil {
		return value
	}
	return parsed.Format("January 2006")
}

func validPaymentMethod(value string) bool {
	switch value {
	case "cash", "bank_transfer", "qr_pay":
		return true
	default:
		return false
	}
}

func paymentMethodLabel(value string) string {
	switch normalizePaymentMethod(value) {
	case "bank_transfer":
		return "Bank transfer"
	case "qr_pay":
		return "QR pay"
	default:
		return "Cash"
	}
}

func formatDateTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.In(time.Local).Format(displayDateTimeLayout)
}

func formatCalendarDate(value string) string {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return value
	}
	return parsed.In(time.Local).Format(displayDateLayout)
}

func formatClockTime(value string) string {
	if strings.TrimSpace(value) == "" {
		return "Time to be announced"
	}
	parsed, err := time.Parse("15:04", strings.TrimSpace(value))
	if err != nil {
		return value
	}
	return parsed.Format("3:04 PM")
}

func formatEventTiming(event Event) string {
	switch {
	case event.StartTime != "" && event.EndTime != "":
		return formatClockTime(event.StartTime) + " to " + formatClockTime(event.EndTime)
	case event.StartTime != "":
		return "Starts at " + formatClockTime(event.StartTime)
	default:
		return "Date only"
	}
}

func eventScheduleLabel(event Event) string {
	base := formatCalendarDate(event.EventDate)
	switch {
	case event.StartTime != "" && event.EndTime != "":
		return base + " • " + formatClockTime(event.StartTime) + " to " + formatClockTime(event.EndTime)
	case event.StartTime != "":
		return base + " • " + formatClockTime(event.StartTime)
	default:
		return base
	}
}

func hasRegistrationDeadline(event Event) bool {
	return strings.TrimSpace(event.RegistrationDeadline) != ""
}

func registrationDeadlineLabel(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "Register before " + formatCalendarDate(value)
}

func isPastEventDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(value))
	if err != nil {
		return false
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return parsed.Before(today)
}

func upcomingEvents(events []Event, limit int) []Event {
	var filtered []Event
	for _, event := range events {
		if !isPastEventDate(event.EventDate) {
			filtered = append(filtered, event)
		}
	}
	if limit > 0 && len(filtered) > limit {
		return filtered[:limit]
	}
	return filtered
}

func hasTime(value time.Time) bool {
	return !value.IsZero()
}

func admissionAge(dateOfBirth string) string {
	dob, err := time.Parse("2006-01-02", strings.TrimSpace(dateOfBirth))
	if err != nil {
		return "—"
	}

	now := time.Now()
	age := now.Year() - dob.Year()
	if now.Month() < dob.Month() || (now.Month() == dob.Month() && now.Day() < dob.Day()) {
		age--
	}
	if age < 0 {
		return "—"
	}

	return strconv.Itoa(age)
}

func scheduleToneClasses(schedule SpaceSchedule) string {
	switch schedule.Activity {
	case "training":
		return "border-amber-200 bg-amber-50 text-amber-900"
	case "full_indoor_cricket":
		return "border-emerald-200 bg-emerald-50 text-emerald-900"
	case "futsal":
		return "border-sky-200 bg-sky-50 text-sky-900"
	case "badminton":
		return "border-violet-200 bg-violet-50 text-violet-900"
	case "table_tennis":
		return "border-cyan-200 bg-cyan-50 text-cyan-900"
	case "cricket_net":
		return "border-lime-200 bg-lime-50 text-lime-900"
	case "tennis":
		return "border-emerald-200 bg-emerald-50 text-emerald-900"
	default:
		return "border-slate/10 bg-white text-slate"
	}
}

func scheduleBadgeClasses(schedule SpaceSchedule) string {
	switch schedule.Activity {
	case "training":
		return "bg-amber-100 text-amber-800"
	case "full_indoor_cricket":
		return "bg-emerald-100 text-emerald-800"
	case "futsal":
		return "bg-sky-100 text-sky-800"
	case "badminton":
		return "bg-violet-100 text-violet-800"
	case "table_tennis":
		return "bg-cyan-100 text-cyan-800"
	case "cricket_net":
		return "bg-lime-100 text-lime-800"
	case "tennis":
		return "bg-emerald-100 text-emerald-800"
	default:
		return "bg-slate-100 text-slate-800"
	}
}

func schedulesForCalendarSlot(schedules []SpaceSchedule, slotDate, slotHour string) []SpaceSchedule {
	var filtered []SpaceSchedule
	for _, schedule := range schedules {
		if schedule.SlotDate == slotDate && schedule.SlotHour == slotHour {
			filtered = append(filtered, schedule)
		}
	}
	return filtered
}

func overlappingSchedulesForCalendarSlot(schedules []SpaceSchedule, slotDate, slotHour string) []SpaceSchedule {
	var filtered []SpaceSchedule
	for _, schedule := range schedules {
		if scheduleOverlapsSlot(schedule, slotDate, slotHour) {
			filtered = append(filtered, schedule)
		}
	}
	return filtered
}

func schedulesForDate(schedules []SpaceSchedule, slotDate string) []SpaceSchedule {
	var filtered []SpaceSchedule
	for _, schedule := range schedules {
		if schedule.SlotDate == slotDate {
			filtered = append(filtered, schedule)
		}
	}
	return filtered
}

func buildDailyBookingStats(schedules []SpaceSchedule, hours []string) []Stat {
	occupiedHours := map[string]struct{}{}
	trainingHours := map[string]struct{}{}
	bookingEntries := 0
	for _, schedule := range schedules {
		occupiedHours[schedule.SlotHour] = struct{}{}
		if schedule.EntryType == "training" {
			trainingHours[schedule.SlotHour] = struct{}{}
		}
		if schedule.EntryType == "booking" {
			bookingEntries++
		}
	}

	return []Stat{
		{Label: "Total slots used", Value: strconv.Itoa(len(occupiedHours))},
		{Label: "Training hours", Value: strconv.Itoa(len(trainingHours))},
		{Label: "Booking entries", Value: strconv.Itoa(bookingEntries)},
		{Label: "Open hours", Value: strconv.Itoa(len(hours) - len(occupiedHours))},
	}
}

func buildFinanceStats(transactions []FinanceTransaction) []Stat {
	totalIncome := 0.0
	admissionPayments := 0
	studentPayments := 0
	referralPayouts := 0.0

	for _, transaction := range transactions {
		if !financeTransactionPosted(transaction) {
			continue
		}
		totalIncome += transaction.Amount
		if transaction.Category == "admission_payment" {
			admissionPayments++
		}
		if transaction.Category == "student_monthly_payment" {
			studentPayments++
		}
		if transaction.Category == "referral_commission_payment" {
			referralPayouts += -transaction.Amount
		}
	}

	return []Stat{
		{Label: "Net recorded cash", Value: money(totalIncome)},
		{Label: "Admission payments", Value: strconv.Itoa(admissionPayments)},
		{Label: "Student payments", Value: strconv.Itoa(studentPayments)},
		{Label: "Referral commission paid", Value: money(referralPayouts)},
	}
}

func buildLedgerStats(transactions []FinanceTransaction) []Stat {
	totalIn := 0.0
	totalOut := 0.0
	activeCount := 0
	voidedCount := 0

	for _, transaction := range transactions {
		if transaction.Voided {
			voidedCount++
			continue
		}
		activeCount++
		totalIn += transaction.MoneyIn
		totalOut += transaction.MoneyOut
	}

	net := normalizeMoney(totalIn - totalOut)
	return []Stat{
		{Label: "Active entries", Value: strconv.Itoa(activeCount)},
		{Label: "Voided entries", Value: strconv.Itoa(voidedCount)},
		{Label: "Money in", Value: money(totalIn)},
		{Label: "Money out", Value: money(totalOut)},
		{Label: "Net movement", Value: money(net)},
	}
}

func financeSourceTypeLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "manual":
		return "Manual entry"
	case "admission":
		return "Admission"
	case "student_monthly_payment":
		return "Student monthly payment"
	case "student_enrollment":
		return "Enrollment"
	case "booking_payment_collection":
		return "Booking collection"
	case "booking_referral_payment":
		return "Referral payout"
	case "finance_transfer":
		return "Transfer"
	case "finance_adjustment":
		return "Adjustment"
	case "finance_account_opening_balance":
		return "Opening balance"
	default:
		return financeCategoryLabelFallback(value)
	}
}

func buildFinanceSummary(accounts []FinanceAccount, transactions []FinanceTransaction, bookings []BookingFinancial, monthly []StudentPaymentRow, referrals []BookingReferral, reconciliations []CashReconciliation) FinanceSummary {
	var summary FinanceSummary
	summary.CashBalance = financeAccountDisplayBalance(accounts, transactions, financeAccountCashInHand)
	summary.BankBalance = financeAccountDisplayBalance(accounts, transactions, financeAccountMainBank)
	summary.TotalAvailableFunds = normalizeMoney(summary.CashBalance + summary.BankBalance)
	for _, transaction := range transactions {
		if !financeTransactionPosted(transaction) {
			continue
		}
		switch transaction.TransactionType {
		case financeTxnTypeOpeningBalance, financeTxnTypeAdjustment:
			// Excluded from operating revenue and expense KPIs.
		case financeTxnTypeTransferIn, financeTxnTypeTransferOut:
			// Internal transfers move cash between accounts only.
		default:
			if transaction.Amount >= 0 {
				summary.NetOperatingCashFlow += transaction.Amount
			} else {
				summary.NetOperatingCashFlow += transaction.Amount
			}
		}
		if transaction.TransactionType == financeTxnTypeTransferIn || transaction.TransactionType == financeTxnTypeTransferOut {
			continue
		}
		if transaction.TransactionType == financeTxnTypeOpeningBalance || transaction.TransactionType == financeTxnTypeAdjustment {
			continue
		}
		if transaction.Amount >= 0 {
			summary.GrossIncome += transaction.Amount
		} else {
			summary.TotalExpenses += -transaction.Amount
		}
	}
	summary.NetCash = normalizeMoney(summary.GrossIncome - summary.TotalExpenses)
	summary.NetOperatingCashFlow = normalizeMoney(summary.NetOperatingCashFlow)
	for _, booking := range bookings {
		if booking.OutstandingAmount > 0 {
			summary.OutstandingBooking += booking.OutstandingAmount
		}
	}
	for _, row := range monthly {
		if row.Payment == nil {
			summary.OutstandingMonthly += row.MonthlyFee
		}
	}
	for _, referral := range referrals {
		if referral.BookingStatus == "confirmed" && !referral.Paid {
			summary.PayableReferrals += referral.CommissionAmount
		}
	}
	summary.UnreconciledCashDelta, summary.LastCashReconciliationOn = latestUnreconciledCashDelta(accounts, reconciliations, transactions)
	return summary
}

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"os"
	"strconv"
	"strings"
	"time"
)

type runtimeDependencies struct {
	AppEnv              AppEnvironment
	RuntimeConfig       AppRuntimeConfig
	UploadStorage       UploadStorage
	CookieSecure        bool
	SMTPConfig          SMTPConfig
	SMSConfig           SMSConfig
	BookingMessages     BookingCommunicationSettings
	BookingAccess       BookingAccessSettings
	ConfigurationErrors []string
}

type superadminBootstrapSeed struct {
	Name     string
	Email    string
	Password string
}

func loadRuntimeDependencies() (runtimeDependencies, error) {
	appEnv, err := parseAppEnvironment(os.Getenv("APP_ENV"))
	if err != nil {
		return runtimeDependencies{}, err
	}

	addr := envOrDefault("ADDR", ":8080")
	dbPath, dbPathErrs := validateDatabasePath(appEnv, os.Getenv("DB_PATH"))
	if err := prepareDatabasePath(dbPath); err != nil {
		return runtimeDependencies{}, fmt.Errorf("prepare database path: %w", err)
	}

	cookieSecure := os.Getenv("COOKIE_SECURE") == "true"
	uploadStorage, err := prepareUploadStorage(os.Getenv("UPLOAD_DIR"))
	if err != nil {
		return runtimeDependencies{}, fmt.Errorf("prepare upload storage: %w", err)
	}

	smtpConfig := SMTPConfig{
		Host:     strings.TrimSpace(os.Getenv("SMTP_HOST")),
		Port:     envOrDefault("SMTP_PORT", "587"),
		Username: strings.TrimSpace(os.Getenv("SMTP_USER")),
		Password: os.Getenv("SMTP_PASS"),
		From:     strings.TrimSpace(os.Getenv("SMTP_FROM")),
	}
	if smtpConfig.From == "" {
		smtpConfig.From = smtpConfig.Username
	}
	smtpConfig.Enabled = smtpConfig.Username != "" && smtpConfig.Password != "" && smtpConfig.From != ""

	smsConfig := SMSConfig{
		UserID:   envValue("SMS_USER_ID", "SMSLENZ_USER_ID"),
		APIKey:   envValue("SMS_API_KEY", "SMSLENZ_API_KEY"),
		SenderID: envValue("SMS_SENDER_ID", "SMSLENZ_SENDER_ID"),
	}
	smsConfig.Enabled = smsConfig.UserID != "" && smsConfig.APIKey != "" && smsConfig.SenderID != ""

	bookingMessages := BookingCommunicationSettings{
		EmailEnabled: envOrDefault("BOOKING_EMAIL_ENABLED", "true") != "false",
		SMSEnabled:   envOrDefault("BOOKING_SMS_ENABLED", "false") == "true" && smsConfig.Enabled,
		ContactPhone: envOrDefault("MEKMAA_CONTACT_PHONE", "077 220 7297"),
		ContactEmail: envOrDefault("MEKMAA_CONTACT_EMAIL", "mekmaa.jo@gmail.com"),
		VenueName:    envOrDefault("MEKMAA_VENUE_NAME", "Mekmaa (Private Limited)"),
		VenueAddress: envOrDefault("MEKMAA_VENUE_ADDRESS", "No. 64, Temple Road, Jaffna - 40000, Sri Lanka"),
	}

	tokenTTLDays, err := strconv.Atoi(envOrDefault("BOOKING_ACCESS_TOKEN_TTL_DAYS", "180"))
	if err != nil || tokenTTLDays <= 0 {
		tokenTTLDays = 180
	}
	bookingAccess := BookingAccessSettings{
		BaseURL:     envOrDefault("MEKMAA_PUBLIC_BASE_URL", "http://localhost:8080"),
		TokenSecret: envOrDefault("BOOKING_ACCESS_TOKEN_SECRET", defaultBookingAccessTokenSecret),
		TokenTTL:    time.Duration(tokenTTLDays) * 24 * time.Hour,
	}

	runtimeConfig := AppRuntimeConfig{
		Env:           appEnv,
		Addr:          addr,
		DBPath:        dbPath,
		UploadRoot:    uploadStorage.Root,
		PublicBaseURL: bookingAccess.BaseURL,
		CookieSecure:  cookieSecure,
	}

	configErrs := validateRuntimeConfiguration(runtimeConfig, bookingMessages, bookingAccess, smtpConfig, smsConfig)
	configErrs = append(configErrs, validateUploadPath(appEnv, uploadStorage.Root)...)
	configErrs = append(configErrs, dbPathErrs...)

	if _, err := loadBootstrapSuperadminSeed(); err != nil {
		return runtimeDependencies{}, err
	}

	return runtimeDependencies{
		AppEnv:              appEnv,
		RuntimeConfig:       runtimeConfig,
		UploadStorage:       uploadStorage,
		CookieSecure:        cookieSecure,
		SMTPConfig:          smtpConfig,
		SMSConfig:           smsConfig,
		BookingMessages:     bookingMessages,
		BookingAccess:       bookingAccess,
		ConfigurationErrors: configErrs,
	}, nil
}

func openConfiguredDatabase(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteRuntimeDSN(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	if err := enableSQLiteForeignKeys(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

func applyBootstrapData(db *sql.DB) error {
	if err := runMigrations(db); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	if err := seedRoles(db); err != nil {
		return fmt.Errorf("seed roles: %w", err)
	}
	if err := seedTrainingPrograms(db); err != nil {
		return fmt.Errorf("seed training programmes: %w", err)
	}
	if err := seedFinanceCategories(db); err != nil {
		return fmt.Errorf("seed finance categories: %w", err)
	}
	bootstrapSeed, err := loadBootstrapSuperadminSeed()
	if err != nil {
		return fmt.Errorf("load superadmin bootstrap: %w", err)
	}
	if err := bootstrapSuperadmin(db, bootstrapSeed); err != nil {
		return fmt.Errorf("bootstrap superadmin: %w", err)
	}
	return nil
}

func loadBootstrapSuperadminSeed() (*superadminBootstrapSeed, error) {
	email := strings.ToLower(strings.TrimSpace(os.Getenv("BOOTSTRAP_SUPERADMIN_EMAIL")))
	password := os.Getenv("BOOTSTRAP_SUPERADMIN_PASSWORD")
	name := strings.TrimSpace(os.Getenv("BOOTSTRAP_SUPERADMIN_NAME"))
	if name == "" {
		name = "Platform Superadmin"
	}

	if email == "" && password == "" && strings.TrimSpace(os.Getenv("BOOTSTRAP_SUPERADMIN_NAME")) == "" {
		return nil, nil
	}
	if email == "" || password == "" {
		return nil, errors.New("BOOTSTRAP_SUPERADMIN_EMAIL and BOOTSTRAP_SUPERADMIN_PASSWORD must be set together")
	}
	if !emailPattern.MatchString(email) {
		return nil, errors.New("BOOTSTRAP_SUPERADMIN_EMAIL must be a valid email address")
	}
	if !passwordPattern.MatchString(password) {
		return nil, errors.New("BOOTSTRAP_SUPERADMIN_PASSWORD must be at least 10 characters")
	}

	return &superadminBootstrapSeed{
		Name:     name,
		Email:    email,
		Password: password,
	}, nil
}

func bootstrapSuperadmin(db *sql.DB, seed *superadminBootstrapSeed) error {
	if seed == nil {
		return nil
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(seed.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	row := tx.QueryRow(`SELECT id FROM users WHERE email = ?`, seed.Email)
	var userID int64
	switch err := row.Scan(&userID); {
	case err == nil:
		if _, err := tx.Exec(`
			UPDATE users
			SET name = ?, password_hash = ?, email_verified_at = ?
			WHERE id = ?
		`, seed.Name, string(passwordHash), now, userID); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
		result, err := tx.Exec(`
			INSERT INTO users (email, name, password_hash, created_at, email_verified_at)
			VALUES (?, ?, ?, ?, ?)
		`, seed.Email, seed.Name, string(passwordHash), now, now)
		if err != nil {
			return err
		}
		userID, err = result.LastInsertId()
		if err != nil {
			return err
		}
	default:
		return err
	}

	if _, err := tx.Exec(`DELETE FROM user_roles WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, role := range []string{"superadmin", "admin", "editor"} {
		roleID, err := roleIDByName(tx, role)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id) VALUES (?, ?)`, userID, roleID); err != nil {
			return err
		}
	}

	return tx.Commit()
}

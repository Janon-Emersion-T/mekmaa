package main

import (
	"os"
	"testing"
)

func TestPostgresConnectionWhenConfigured(t *testing.T) {
	databaseURL := os.Getenv("MEKMAA_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("MEKMAA_TEST_DATABASE_URL not configured")
	}

	config := DatabaseConfig{
		Driver: databaseDriverPostgres,
		URL:    databaseURL,
	}

	db, err := openDatabase(config)
	if err != nil {
		t.Fatalf("open postgres database: %v", err)
	}
	defer db.Close()

	var databaseName string
	var currentUser string

	if err := db.QueryRow(
		`SELECT current_database(), current_user`,
	).Scan(&databaseName, &currentUser); err != nil {
		t.Fatalf("query postgres identity: %v", err)
	}

	if databaseName != "mekmaa_dev" {
		t.Fatalf(
			"database = %q, want mekmaa_dev",
			databaseName,
		)
	}

	if currentUser != "mekmaa_dev" {
		t.Fatalf(
			"user = %q, want mekmaa_dev",
			currentUser,
		)
	}
}

package database

import "testing"

func TestSQLiteRebindLeavesQueryUntouched(t *testing.T) {
	query := "SELECT * FROM users WHERE id = ? AND email = ?"

	got := SQLite.Rebind(query)

	if got != query {
		t.Fatalf("expected query to remain unchanged, got %q", got)
	}
}

func TestPostgresRebind(t *testing.T) {
	query := "SELECT * FROM users WHERE id = ? AND email = ?"

	got := Postgres.Rebind(query)
	want := "SELECT * FROM users WHERE id = $1 AND email = $2"

	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestParseDialect(t *testing.T) {
	tests := []struct {
		value string
		want  Dialect
	}{
		{"", SQLite},
		{"sqlite", SQLite},
		{"sqlite3", SQLite},
		{"postgres", Postgres},
		{"postgresql", Postgres},
		{"pgsql", Postgres},
	}

	for _, tt := range tests {
		got, err := ParseDialect(tt.value)
		if err != nil {
			t.Fatalf("ParseDialect(%q): %v", tt.value, err)
		}

		if got != tt.want {
			t.Fatalf("ParseDialect(%q): expected %q, got %q", tt.value, tt.want, got)
		}
	}
}

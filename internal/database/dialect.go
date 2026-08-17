package database

import (
	"fmt"
	"strings"
)

type Dialect string

const (
	SQLite   Dialect = "sqlite"
	Postgres Dialect = "postgres"
)

func ParseDialect(value string) (Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "sqlite", "sqlite3":
		return SQLite, nil
	case "postgres", "postgresql", "pgsql":
		return Postgres, nil
	default:
		return "", fmt.Errorf("unsupported database dialect %q", value)
	}
}

func (d Dialect) DriverName() string {
	switch d {
	case Postgres:
		return "pgx"
	default:
		return "sqlite"
	}
}

func (d Dialect) IsPostgres() bool {
	return d == Postgres
}

func (d Dialect) IsSQLite() bool {
	return d == SQLite
}

// Rebind converts database/sql-style question-mark placeholders into
// PostgreSQL positional placeholders.
//
// SQL passed here must use ? only for bind parameters.
func (d Dialect) Rebind(query string) string {
	if d != Postgres {
		return query
	}

	var b strings.Builder
	b.Grow(len(query) + 16)

	arg := 1

	for i := 0; i < len(query); i++ {
		if query[i] != '?' {
			b.WriteByte(query[i])
			continue
		}

		b.WriteString(fmt.Sprintf("$%d", arg))
		arg++
	}

	return b.String()
}

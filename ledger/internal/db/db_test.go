package db

import (
	"net/url"
	"testing"
)

func clearDatabaseEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"DATABASE_URL", "DB_HOST", "DB_PORT", "DB_USER", "DB_PASS", "DB_NAME"} {
		t.Setenv(name, "")
	}
}

func TestConnectionStringDefaults(t *testing.T) {
	clearDatabaseEnv(t)
	dsn, err := connectionString()
	if err != nil {
		t.Fatal(err)
	}
	const expected = "postgres://postgres:postgres@localhost:5432/cashapp?sslmode=disable"
	if dsn != expected {
		t.Fatalf("connectionString() = %q, want %q", dsn, expected)
	}
}

func TestDatabaseURLTakesPriority(t *testing.T) {
	clearDatabaseEnv(t)
	const expected = "postgres://application:secret@database:6543/production?sslmode=require"
	t.Setenv("DATABASE_URL", expected)
	t.Setenv("DB_PORT", "invalid")
	t.Setenv("DB_NAME", "ignored")
	dsn, err := connectionString()
	if err != nil {
		t.Fatal(err)
	}
	if dsn != expected {
		t.Fatalf("connectionString() = %q, want DATABASE_URL", dsn)
	}
}

func TestConnectionStringPreservesSpecialCharacters(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("DB_HOST", "::1")
	t.Setenv("DB_PORT", "6543")
	t.Setenv("DB_USER", "user@ledger")
	t.Setenv("DB_PASS", "pass:/?# @word")
	t.Setenv("DB_NAME", "cash app")
	dsn, err := connectionString()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, ok := parsed.User.Password()
	if !ok || parsed.User.Username() != "user@ledger" || password != "pass:/?# @word" {
		t.Fatal("connection string did not preserve credentials")
	}
	if parsed.Hostname() != "::1" || parsed.Port() != "6543" || parsed.Path != "/cash app" {
		t.Fatalf("unexpected database address: host=%q, port=%q, path=%q", parsed.Hostname(), parsed.Port(), parsed.Path)
	}
	if parsed.Query().Get("sslmode") != "disable" {
		t.Fatal("local database connection should default to sslmode=disable")
	}
}

func TestConnectionStringRejectsInvalidPort(t *testing.T) {
	for _, port := range []string{"invalid", "0", "-1", "65536"} {
		t.Run(port, func(t *testing.T) {
			clearDatabaseEnv(t)
			t.Setenv("DB_PORT", port)
			if _, err := connectionString(); err == nil {
				t.Fatal("expected invalid DB_PORT to fail before connecting")
			}
		})
	}
}

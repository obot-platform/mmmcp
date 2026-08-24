package storage

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifySQLiteDSNForms(t *testing.T) {
	dataDirectory := t.TempDir()
	plainPath := filepath.Join(t.TempDir(), "plain.db")
	filePath := filepath.Join(t.TempDir(), "file.db")
	nestedPath := filepath.Join(t.TempDir(), "nested.db")
	tests := []struct {
		name      string
		input     string
		wantPath  string
		wantValue string
	}{
		{name: "empty default", wantPath: filepath.Join(dataDirectory, "mmmcp.db"), wantValue: "mmmcp.db"},
		{name: "plain path", input: plainPath, wantPath: plainPath, wantValue: "plain.db"},
		{name: "file URI", input: fileDSN(filePath), wantPath: filepath.ToSlash(filePath), wantValue: "file.db"},
		{name: "nested file URI", input: "sqlite://file:" + nestedPath, wantPath: filepath.ToSlash(nestedPath), wantValue: "nested.db"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classified, err := classifySQLiteDSN(test.input, Options{DataDirectory: dataDirectory})
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Clean(classified.path) != filepath.Clean(test.wantPath) {
				t.Fatalf("path = %q, want %q", classified.path, test.wantPath)
			}
			if !strings.Contains(classified.value, test.wantValue) {
				t.Fatalf("normalized DSN does not contain filename: %q", classified.value)
			}
			for _, pragma := range []string{"busy_timeout", "foreign_keys", "journal_mode", "synchronous", "trusted_schema"} {
				if !strings.Contains(classified.value, pragma) {
					t.Fatalf("normalized DSN is missing %s: %q", pragma, classified.value)
				}
			}
			if classified.key == "" || strings.Contains(classified.key, test.wantValue) {
				t.Fatalf("registry key is not a non-reversible digest: %q", classified.key)
			}
		})
	}
}

func TestClassifyDSNAcceptsSupportedDialects(t *testing.T) {
	dataDirectory := t.TempDir()
	tests := []struct {
		name    string
		input   string
		dialect Dialect
		driver  string
	}{
		{name: "empty SQLite", dialect: DialectSQLite, driver: "sqlite"},
		{name: "SQLite URL", input: "sqlite:///tmp/mmmcp.db", dialect: DialectSQLite, driver: "sqlite"},
		{name: "SQLite URL with nested file URI", input: "sqlite://file:mmmcp.db?_journal=WAL&cache=shared", dialect: DialectSQLite, driver: "sqlite"},
		{name: "PostgreSQL URL", input: "postgres://user:secret@localhost/mmmcp?sslmode=disable", dialect: DialectPostgres, driver: "pgx"},
		{name: "PostgreSQL keyword", input: "host=localhost port=5432 user=user password=secret dbname=mmmcp sslmode=disable", dialect: DialectPostgres, driver: "pgx"},
		{name: "MySQL URL", input: "mysql://user:secret@localhost:3306/mmmcp?parseTime=true", dialect: DialectMySQL, driver: "mysql"},
		{name: "MySQL driver", input: "user:secret@tcp(localhost:3306)/mmmcp?parseTime=true", dialect: DialectMySQL, driver: "mysql"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classified, err := classifyDSN(test.input, Options{DataDirectory: dataDirectory})
			if err != nil {
				t.Fatal(err)
			}
			if classified.dialect != test.dialect || classified.driver != test.driver {
				t.Fatalf("classification = %s/%s, want %s/%s", classified.dialect, classified.driver, test.dialect, test.driver)
			}
			if classified.key == "" || strings.Contains(classified.key, "secret") || strings.Contains(classified.key, "localhost") {
				t.Fatalf("classification key is not secret-safe: %q", classified.key)
			}
		})
	}
}

func TestClassifyDSNRejectsUnsupportedAndMalformedFormsWithoutSecrets(t *testing.T) {
	secret := "do-not-expose"
	for _, input := range []string{
		"redis://user:" + secret + "@localhost/db",
		"mysql://user:" + secret + "@localhost",
		"host='unterminated password=" + secret,
	} {
		_, err := classifyDSN(input, Options{DataDirectory: t.TempDir()})
		if err == nil {
			t.Fatalf("classifyDSN(%q) unexpectedly succeeded", input)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), input) {
			t.Fatalf("classification error exposes input: %v", err)
		}
	}
}

func TestEquivalentMySQLURLsHaveStableRegistryKeys(t *testing.T) {
	first, err := classifyDSN("mysql://user:secret@localhost:3306/mmmcp?parseTime=true&charset=utf8mb4", Options{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := classifyDSN("mysql://user:secret@localhost:3306/mmmcp?charset=utf8mb4&parseTime=true", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if first.key != second.key || first.value != second.value {
		t.Fatalf("equivalent MySQL URL normalization differs: %q != %q", first.value, second.value)
	}
}

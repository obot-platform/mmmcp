package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
)

type classifiedDSN struct {
	dialect Dialect
	driver  string
	value   string
	path    string
	key     string
}

type sqliteDSN struct {
	value string
	path  string
	key   string
}

func classifyDSN(input string, options Options) (classifiedDSN, error) {
	value := strings.TrimSpace(input)
	lower := strings.ToLower(value)

	switch {
	case value == "", strings.HasPrefix(lower, "file:"), strings.HasPrefix(lower, "sqlite://"), value == ":memory:":
		sqlite, err := classifySQLiteDSN(value, options)
		if err != nil {
			return classifiedDSN{}, err
		}
		return classifiedDSN{dialect: DialectSQLite, driver: "sqlite", value: sqlite.value, path: sqlite.path, key: sqlite.key}, nil
	case strings.HasPrefix(lower, "postgres://"), strings.HasPrefix(lower, "postgresql://"), looksLikePostgresKeywordDSN(value):
		if _, err := pgx.ParseConfig(value); err != nil {
			return classifiedDSN{}, errors.New("storage: invalid PostgreSQL DSN")
		}
		return newClassifiedDSN(DialectPostgres, "pgx", value, ""), nil
	case strings.HasPrefix(lower, "mysql://"):
		normalized, err := normalizeMySQLURL(value)
		if err != nil {
			return classifiedDSN{}, err
		}
		return newClassifiedDSN(DialectMySQL, "mysql", normalized, ""), nil
	case looksLikeMySQLDriverDSN(value):
		cfg, err := mysql.ParseDSN(value)
		if err != nil || cfg.DBName == "" {
			return classifiedDSN{}, errors.New("storage: invalid MySQL DSN")
		}
		return newClassifiedDSN(DialectMySQL, "mysql", cfg.FormatDSN(), ""), nil
	case strings.Contains(value, "://"):
		return classifiedDSN{}, errors.New("storage: unsupported DSN")
	default:
		sqlite, err := classifySQLiteDSN(value, options)
		if err != nil {
			return classifiedDSN{}, err
		}
		return classifiedDSN{dialect: DialectSQLite, driver: "sqlite", value: sqlite.value, path: sqlite.path, key: sqlite.key}, nil
	}
}

func newClassifiedDSN(dialect Dialect, driver, value, path string) classifiedDSN {
	sum := sha256.Sum256([]byte(string(dialect) + "\x00" + value))
	return classifiedDSN{
		dialect: dialect,
		driver:  driver,
		value:   value,
		path:    path,
		key:     hex.EncodeToString(sum[:]),
	}
}

func classifySQLiteDSN(input string, options Options) (sqliteDSN, error) {
	var path string
	value := strings.TrimSpace(input)
	lower := strings.ToLower(value)
	switch {
	case value == "":
		path = filepath.Join(defaultDataDirectory(options.DataDirectory), "mmmcp.db")
		value = fileDSN(path)
	case value == ":memory:":
		value = "file::memory:"
	case strings.HasPrefix(lower, "sqlite://"):
		var err error
		value, path, err = normalizeSQLiteURL(value)
		if err != nil {
			return sqliteDSN{}, err
		}
	case strings.HasPrefix(lower, "file:"):
		parsed, err := url.Parse(value)
		if err != nil {
			return sqliteDSN{}, errors.New("storage: invalid SQLite DSN")
		}
		if parsed.Opaque != "" {
			path = parsed.Opaque
		} else {
			path = parsed.Path
		}
		if strings.Contains(lower, "mode=memory") || path == ":memory:" || strings.HasPrefix(path, ":memory:") {
			path = ""
		}
	case strings.Contains(value, "://"):
		return sqliteDSN{}, errors.New("storage: unsupported DSN")
	default:
		absolute, err := filepath.Abs(value)
		if err != nil {
			return sqliteDSN{}, errors.New("storage: invalid SQLite path")
		}
		path = absolute
		value = fileDSN(absolute)
	}

	value, err := addSQLitePragmas(value)
	if err != nil {
		return sqliteDSN{}, errors.New("storage: invalid SQLite DSN")
	}
	classified := newClassifiedDSN(DialectSQLite, "sqlite", value, path)
	return sqliteDSN{value: classified.value, path: classified.path, key: classified.key}, nil
}

func normalizeSQLiteURL(value string) (string, string, error) {
	// Drop the redundant inner file: scheme, which url.Parse would read as an authority.
	if rest, ok := strings.CutPrefix(value, "sqlite://file:"); ok {
		value = "sqlite://" + rest
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, "sqlite") || parsed.User != nil || parsed.Fragment != "" {
		return "", "", errors.New("storage: invalid SQLite DSN")
	}
	var path string
	switch {
	case parsed.Opaque != "":
		path = parsed.Opaque
	case parsed.Host == "" || strings.EqualFold(parsed.Host, "localhost"):
		path = parsed.Path
	case parsed.Path == "":
		path = parsed.Host
	default:
		return "", "", errors.New("storage: invalid SQLite DSN")
	}
	if path == "" {
		return "", "", errors.New("storage: invalid SQLite DSN")
	}
	if path == ":memory:" || strings.Contains(strings.ToLower(parsed.RawQuery), "mode=memory") {
		fileURL := &url.URL{Scheme: "file", Opaque: ":memory:", RawQuery: parsed.RawQuery}
		return fileURL.String(), "", nil
	}
	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return "", "", errors.New("storage: invalid SQLite path")
		}
	}
	fileURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(path), RawQuery: parsed.RawQuery}
	return fileURL.String(), path, nil
}

func normalizeMySQLURL(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || !strings.EqualFold(parsed.Scheme, "mysql") || parsed.Host == "" || parsed.Fragment != "" {
		return "", errors.New("storage: invalid MySQL DSN")
	}
	database, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	if err != nil || database == "" || strings.Contains(database, "/") {
		return "", errors.New("storage: invalid MySQL DSN")
	}
	var user, password string
	if parsed.User != nil {
		user = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	cfg := mysql.NewConfig()
	cfg.User = user
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = parsed.Host
	cfg.DBName = database
	if len(parsed.Query()) > 0 {
		cfg.Params = make(map[string]string, len(parsed.Query()))
		for name, values := range parsed.Query() {
			if len(values) != 1 {
				return "", errors.New("storage: invalid MySQL DSN")
			}
			cfg.Params[name] = values[0]
		}
	}
	normalized := cfg.FormatDSN()
	if _, err := mysql.ParseDSN(normalized); err != nil {
		return "", errors.New("storage: invalid MySQL DSN")
	}
	return normalized, nil
}

func looksLikePostgresKeywordDSN(value string) bool {
	trimmed := strings.TrimSpace(value)
	equals := strings.IndexByte(trimmed, '=')
	if equals <= 0 {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(trimmed[:equals]))
	switch key {
	case "host", "hostaddr", "port", "dbname", "database", "user", "password", "passfile", "service", "servicefile", "sslmode", "sslcert", "sslkey", "sslrootcert", "connect_timeout", "application_name", "options", "target_session_attrs":
		return true
	default:
		return false
	}
}

func looksLikeMySQLDriverDSN(value string) bool {
	if strings.Contains(value, "@") {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "tcp(") || strings.HasPrefix(lower, "unix(")
}

func addSQLitePragmas(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(NORMAL)")
	query.Add("_pragma", "trusted_schema(OFF)")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func fileDSN(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func defaultDataDirectory(override string) string {
	if override != "" {
		return override
	}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		if value := os.Getenv("XDG_DATA_HOME"); value != "" {
			return filepath.Join(value, "mmmcp")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", "mmmcp")
		}
	}
	if directory, err := os.UserConfigDir(); err == nil {
		return filepath.Join(directory, "mmmcp")
	}
	return filepath.Join(os.TempDir(), "mmmcp")
}

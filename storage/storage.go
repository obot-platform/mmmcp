// Package storage provides persistent state used by the composite server.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
	DialectMySQL    Dialect = "mysql"
)

// Dialect identifies a supported database family.
type Dialect string

// Store is an opened persistent store.
type Store interface {
	mcp.EventStore
	Prune(context.Context) error
	Close() error
}

// SQLStore is a database/sql-backed Store.
type SQLStore struct {
	db      *sql.DB
	dialect Dialect
	options Options
}

type operationError struct {
	dialect   Dialect
	operation string
}

// DB returns the underlying database handle for repository integrations.
func (s *SQLStore) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Dialect returns the store's database family.
func (s *SQLStore) Dialect() Dialect {
	if s == nil {
		return ""
	}
	return s.dialect
}

// Close releases the database pool.
func (s *SQLStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (e *operationError) Error() string {
	return "storage: " + string(e.dialect) + " " + e.operation + " failed"
}

func storageError(dialect Dialect, operation string, cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return fmt.Errorf("storage: %s %s failed: %w", dialect, operation, cause)
	}
	return &operationError{dialect: dialect, operation: operation}
}

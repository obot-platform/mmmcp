package storage

import (
	"context"
	"database/sql"
	"errors"
	"iter"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	errUnknownEventStream = errors.New("unknown event stream")
)

// Open prepares a stream for ordered event storage.
func (s *SQLStore) Open(ctx context.Context, sessionID, streamID string) error {
	if s == nil || s.db == nil {
		return errors.New("storage: store is closed")
	}
	now := s.options.Now()
	err := s.runTransaction(ctx, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.bind(s.openStreamSQL()),
			sessionID, streamID, now.Add(s.options.EventTTL).UnixNano(), now.UnixNano()); err != nil {
			return err
		}
		return s.pruneTx(ctx, tx, now.UnixNano())
	})
	if err != nil {
		return storageError(s.dialect, "open event stream", err)
	}
	return nil
}

// Append atomically allocates the next stream index and records data.
func (s *SQLStore) Append(ctx context.Context, sessionID, streamID string, data []byte) error {
	if s == nil || s.db == nil {
		return errors.New("storage: store is closed")
	}
	now := s.options.Now()
	err := s.runTransaction(ctx, nil, func(tx *sql.Tx) error {
		var eventIndex int64
		if err := tx.QueryRowContext(ctx, s.bind(s.nextIndexSQL()), sessionID, streamID).Scan(&eventIndex); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errUnknownEventStream
			}
			return err
		}

		payloadIsNull := 0
		payload := data
		if data == nil {
			payloadIsNull = 1
			payload = []byte{}
		}
		if _, err := tx.ExecContext(ctx, s.bind(`
			INSERT INTO mmmcp_event_log (
				session_id, stream_id, event_index, payload, payload_is_null, payload_size, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?)`),
			sessionID, streamID, eventIndex, payload, payloadIsNull, len(data), now.UnixNano()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, s.bind(`
			UPDATE mmmcp_event_streams
			SET next_index = ?, expires_at = ?, updated_at = ?
			WHERE session_id = ? AND stream_id = ?`),
			eventIndex+1, now.Add(s.options.EventTTL).UnixNano(), now.UnixNano(), sessionID, streamID); err != nil {
			return err
		}
		return s.pruneTx(ctx, tx, now.UnixNano())
	})
	if errors.Is(err, errUnknownEventStream) {
		return errors.New("storage: append to unknown event stream")
	}
	if err != nil {
		return storageError(s.dialect, "append event", err)
	}
	return nil
}

// After returns a stable snapshot strictly after index. It checks the purge
// watermark before reading any rows so callers never receive partial replay.
func (s *SQLStore) After(ctx context.Context, sessionID, streamID string, index int) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		values, err := s.readAfter(ctx, sessionID, streamID, index)
		if err != nil {
			yield(nil, err)
			return
		}
		for _, value := range values {
			if !yield(value, nil) {
				return
			}
		}
	}
}

func (s *SQLStore) readAfter(ctx context.Context, sessionID, streamID string, index int) ([][]byte, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("storage: store is closed")
	}
	var values [][]byte
	err := s.runTransaction(ctx, &sql.TxOptions{ReadOnly: true}, func(tx *sql.Tx) error {
		values = nil
		var firstRetained, next int64
		if err := tx.QueryRowContext(ctx, s.bind(`
			SELECT first_retained_index, next_index
			FROM mmmcp_event_streams
			WHERE session_id = ? AND stream_id = ?`), sessionID, streamID).Scan(&firstRetained, &next); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return mcp.ErrEventsPurged
			}
			return err
		}
		start := int64(index) + 1
		if firstRetained > start {
			return mcp.ErrEventsPurged
		}
		if start >= next {
			return nil
		}
		rows, err := tx.QueryContext(ctx, s.bind(`
			SELECT payload, payload_is_null
			FROM mmmcp_event_log
			WHERE session_id = ? AND stream_id = ? AND event_index > ?
			ORDER BY event_index`), sessionID, streamID, index)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var payload []byte
			var isNull int
			if err := rows.Scan(&payload, &isNull); err != nil {
				return err
			}
			if isNull != 0 {
				values = append(values, nil)
			} else if payload == nil {
				values = append(values, []byte{})
			} else {
				values = append(values, append([]byte(nil), payload...))
			}
		}
		return rows.Err()
	})
	if errors.Is(err, mcp.ErrEventsPurged) {
		return nil, mcp.ErrEventsPurged
	}
	if err != nil {
		return nil, storageError(s.dialect, "replay events", err)
	}
	return values, nil
}

// SessionClosed removes all streams for a session. Repeated calls are safe.
func (s *SQLStore) SessionClosed(ctx context.Context, sessionID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.runTransaction(ctx, nil, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM mmmcp_event_log WHERE session_id = ?`), sessionID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, s.bind(`DELETE FROM mmmcp_event_streams WHERE session_id = ?`), sessionID)
		return err
	})
	if err != nil {
		return storageError(s.dialect, "close event session", err)
	}
	return nil
}

func (s *SQLStore) runTransaction(ctx context.Context, options *sql.TxOptions, operation func(*sql.Tx) error) error {
	var last error
	for attempt := range defaultTransactionAttempts {
		tx, err := s.db.BeginTx(ctx, options)
		if err == nil {
			err = operation(tx)
			if err == nil {
				err = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
		if err == nil {
			return nil
		}
		last = err
		if !s.retryable(err) || attempt == defaultTransactionAttempts-1 {
			return err
		}
		delay := time.Duration(1<<attempt) * 2 * time.Millisecond
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return last
}

func (s *SQLStore) retryable(err error) bool {
	switch s.dialect {
	case DialectPostgres:
		return isPostgresRetryable(err)
	case DialectMySQL:
		return isMySQLRetryable(err)
	default:
		return isSQLiteRetryable(err)
	}
}

func (s *SQLStore) bind(query string) string {
	if s.dialect != DialectPostgres {
		return query
	}
	var builder strings.Builder
	parameter := 1
	for _, char := range query {
		if char == '?' {
			builder.WriteByte('$')
			builder.WriteString(intString(parameter))
			parameter++
		} else {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func (s *SQLStore) openStreamSQL() string {
	if s.dialect == DialectMySQL {
		return `
			INSERT INTO mmmcp_event_streams (
				session_id, stream_id, first_retained_index, next_index, expires_at, updated_at
			) VALUES (?, ?, 0, 0, ?, ?)
			ON DUPLICATE KEY UPDATE
				expires_at = VALUES(expires_at),
				updated_at = VALUES(updated_at)`
	}
	return `
		INSERT INTO mmmcp_event_streams (
			session_id, stream_id, first_retained_index, next_index, expires_at, updated_at
		) VALUES (?, ?, 0, 0, ?, ?)
		ON CONFLICT(session_id, stream_id) DO UPDATE SET
			expires_at = excluded.expires_at,
			updated_at = excluded.updated_at`
}

func (s *SQLStore) nextIndexSQL() string {
	query := `
		SELECT next_index
		FROM mmmcp_event_streams
		WHERE session_id = ? AND stream_id = ?`
	if s.dialect != DialectSQLite {
		query += " FOR UPDATE"
	}
	return query
}

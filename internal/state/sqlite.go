package state

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	store := &Store{db: db}
	if err := store.init(); err != nil {
		db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) init() error {
	const query = `
CREATE TABLE IF NOT EXISTS processed_messages (
	message_id TEXT PRIMARY KEY,
	processed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS message_failures (
	message_id TEXT PRIMARY KEY,
	failure_count INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
`
	_, err := s.db.Exec(query)
	return err
}

func (s *Store) IsProcessed(ctx context.Context, messageID string) bool {
	var exists int
	err := s.db.QueryRowContext(
		ctx,
		`SELECT 1 FROM processed_messages WHERE message_id = ? LIMIT 1`,
		messageID,
	).Scan(&exists)
	return err == nil
}

func (s *Store) MarkProcessed(ctx context.Context, messageID string) error {
	_, err := s.db.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO processed_messages(message_id) VALUES (?)`,
		messageID,
	)
	if err != nil {
		return fmt.Errorf("insert processed message: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM message_failures WHERE message_id = ?`, messageID); err != nil {
		return fmt.Errorf("clear failures for processed message: %w", err)
	}
	return nil
}

func (s *Store) RecordFailure(ctx context.Context, messageID, reason string) (int, error) {
	_, err := s.db.ExecContext(
		ctx,
		`
INSERT INTO message_failures(message_id, failure_count, last_error)
VALUES (?, 1, ?)
ON CONFLICT(message_id) DO UPDATE SET
	failure_count = failure_count + 1,
	last_error = excluded.last_error,
	updated_at = CURRENT_TIMESTAMP
`,
		messageID,
		reason,
	)
	if err != nil {
		return 0, fmt.Errorf("record failure: %w", err)
	}

	var count int
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT failure_count FROM message_failures WHERE message_id = ?`,
		messageID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("read failure count: %w", err)
	}
	return count, nil
}

func (s *Store) FailureDetails(ctx context.Context, messageID string) (int, string, bool, error) {
	var count int
	var reason string
	err := s.db.QueryRowContext(
		ctx,
		`SELECT failure_count, last_error FROM message_failures WHERE message_id = ?`,
		messageID,
	).Scan(&count, &reason)
	if err == sql.ErrNoRows {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("read failure details: %w", err)
	}
	return count, reason, true, nil
}

func (s *Store) IsFailed(ctx context.Context, messageID string, maxFailures int) bool {
	var count int
	err := s.db.QueryRowContext(
		ctx,
		`SELECT failure_count FROM message_failures WHERE message_id = ?`,
		messageID,
	).Scan(&count)
	if err != nil {
		return false
	}
	return count >= maxFailures
}

func (s *Store) ClearFailure(ctx context.Context, messageID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM message_failures WHERE message_id = ?`, messageID)
	if err != nil {
		return fmt.Errorf("clear failure: %w", err)
	}
	return nil
}

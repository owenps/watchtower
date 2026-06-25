package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/owenps/watchtower/internal/domain"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS item_state (
  target_id TEXT PRIMARY KEY,
  override TEXT NOT NULL DEFAULT '',
  last_seen_action_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS completed_item (
  target_id TEXT PRIMARY KEY,
  completed_at TEXT NOT NULL,
  title TEXT NOT NULL,
  url TEXT NOT NULL
);
`)
	return err
}

func (s *Store) States(ctx context.Context) (map[string]domain.ItemState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT target_id, override, last_seen_action_at FROM item_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]domain.ItemState{}
	for rows.Next() {
		var st domain.ItemState
		var seen string
		if err := rows.Scan(&st.TargetID, &st.Override, &seen); err != nil {
			return nil, err
		}
		if seen != "" {
			st.LastSeenActionAt, _ = time.Parse(time.RFC3339, seen)
		}
		out[st.TargetID] = st
	}
	return out, rows.Err()
}

func (s *Store) MarkSeen(ctx context.Context, targetID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO item_state(target_id, override, last_seen_action_at) VALUES(?, '', ?)
ON CONFLICT(target_id) DO UPDATE SET last_seen_action_at = excluded.last_seen_action_at
`, targetID, at.UTC().Format(time.RFC3339))
	return err
}

func (s *Store) SetOverride(ctx context.Context, targetID, override string) error {
	if override != "" && override != "watch" && override != "unwatch" {
		return fmt.Errorf("invalid override %q", override)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO item_state(target_id, override, last_seen_action_at) VALUES(?, ?, '')
ON CONFLICT(target_id) DO UPDATE SET override = excluded.override
`, targetID, override)
	return err
}

func (s *Store) RecordCompleted(ctx context.Context, item domain.RawItem) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT OR IGNORE INTO completed_item(target_id, completed_at, title, url) VALUES(?, ?, ?, ?)
`, item.TargetID, time.Now().UTC().Format(time.RFC3339), item.Title, item.URL)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

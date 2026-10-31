/*
 * Copyright (C) 2026 Kingkor Roy Tirtho and Spotube Contributors
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// roomState is everything about a room that outlives its sockets, the Worker's
// Durable Object storage. The playback anchor is deliberately not here: like in the
// Worker it lives in memory only, because the host answers a join with a fresh one.
type roomState struct {
	Host        string
	Queue       json.RawMessage // nil: no queue yet
	Banned      []string
	BannedMarks []string
	Members     []string
	// Unix milliseconds; zero means unset.
	HostGoneAt int64
	EmptySince int64
}

// store keeps one row per room in SQLite. The lists are small JSON arrays, bounded by
// maxBans and maxMembers.
type store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS rooms (
  code         TEXT PRIMARY KEY,
  host         TEXT NOT NULL,
  queue        TEXT,
  banned       TEXT NOT NULL DEFAULT '[]',
  banned_marks TEXT NOT NULL DEFAULT '[]',
  members      TEXT NOT NULL DEFAULT '[]',
  host_gone_at INTEGER NOT NULL DEFAULT 0,
  empty_since  INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;
`

func openStore(path string) (*store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// One writer at a time; the load is a handful of small writes per room action.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Nobody is connected after a restart, so every room starts its empty-room clock now.
	if _, err := db.Exec(`UPDATE rooms SET empty_since = ? WHERE empty_since = 0`, time.Now().UnixMilli()); err != nil {
		db.Close()
		return nil, err
	}
	return &store{db}, nil
}

func (s *store) Close() error { return s.db.Close() }

// load returns the room's state, or nil if the room doesn't exist.
func (s *store) load(code string) (*roomState, error) {
	var (
		st                     roomState
		queue                  sql.NullString
		banned, marks, members string
	)
	err := s.db.QueryRow(
		`SELECT host, queue, banned, banned_marks, members, host_gone_at, empty_since FROM rooms WHERE code = ?`, code,
	).Scan(&st.Host, &queue, &banned, &marks, &members, &st.HostGoneAt, &st.EmptySince)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if queue.Valid {
		st.Queue = json.RawMessage(queue.String)
	}
	for src, dst := range map[string]*[]string{banned: &st.Banned, marks: &st.BannedMarks, members: &st.Members} {
		if err := json.Unmarshal([]byte(src), dst); err != nil {
			return nil, err
		}
	}
	return &st, nil
}

// save writes the whole room (an upsert).
func (s *store) save(code string, st *roomState) error {
	var queue any
	if st.Queue != nil {
		queue = string(st.Queue)
	}
	_, err := s.db.Exec(
		`INSERT INTO rooms (code, host, queue, banned, banned_marks, members, host_gone_at, empty_since)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(code) DO UPDATE SET host = excluded.host, queue = excluded.queue, banned = excluded.banned,
		   banned_marks = excluded.banned_marks, members = excluded.members,
		   host_gone_at = excluded.host_gone_at, empty_since = excluded.empty_since`,
		code, st.Host, queue, marshalList(st.Banned), marshalList(st.BannedMarks), marshalList(st.Members), st.HostGoneAt, st.EmptySince,
	)
	return err
}

func (s *store) delete(code string) error {
	_, err := s.db.Exec(`DELETE FROM rooms WHERE code = ?`, code)
	return err
}

// expired deletes rooms that have been empty past the TTL and returns their codes.
func (s *store) expired(before int64) ([]string, error) {
	rows, err := s.db.Query(`DELETE FROM rooms WHERE empty_since != 0 AND empty_since <= ? RETURNING code`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var codes []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

func marshalList(list []string) string {
	if list == nil {
		return "[]"
	}
	return string(marshal(list))
}

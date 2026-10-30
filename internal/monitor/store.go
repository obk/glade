package monitor

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure Go, so glade still builds without cgo

	"github.com/obk/glade/internal/check"
)

// The database keeps every result, one row for each check in each round.
// The counts the page shows are made from them in memory: at the start by
// reading them back, then as each round comes in.
const schema = `
CREATE TABLE IF NOT EXISTS results (
	check_name TEXT NOT NULL,
	time       INTEGER NOT NULL, -- Unix milliseconds
	state      TEXT NOT NULL,    -- up, degraded or down
	took_ms    INTEGER NOT NULL,
	note       TEXT NOT NULL     -- why it wasn't up, as the page shows it
);
CREATE INDEX IF NOT EXISTS results_by_time ON results (time);
PRAGMA user_version = 1;
`

// schemaVersion is the user_version of the schema above; a database with a
// later one was made by a newer glade.
const schemaVersion = 1

// keep is how long results are kept: the longest scale.
func keep() time.Duration {
	sc := Scales[len(Scales)-1]
	return time.Duration(sc.Keep) * sc.Period
}

func openDB(name string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", name+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	// One connection: glade is the only writer, and SQLite takes one at a
	// time anyway.
	db.SetMaxOpenConns(1)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if version > schemaVersion {
		db.Close()
		return nil, fmt.Errorf("%s: made by a newer glade (schema %d; this one knows %d)", name, version, schemaVersion)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return db, nil
}

// load reads back the results young enough to show, oldest first, into a
// record for each check in the config.
func (m *Monitor) load(now time.Time) error {
	rows, err := m.db.Query(`SELECT check_name, time, state, took_ms, note FROM results WHERE time >= ? ORDER BY time`,
		now.Add(-keep()).UnixMilli())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			name   string
			ms, ts int64
			r      check.Result
		)
		if err := rows.Scan(&name, &ms, &r.State, &ts, &r.Note); err != nil {
			return err
		}
		rec := m.records[name]
		if rec == nil {
			continue // a check no longer in the config
		}
		r.Time, r.Took = time.UnixMilli(ms).UTC(), time.Duration(ts)*time.Millisecond
		rec.record(r)
	}
	return rows.Err()
}

// Save adds results, one for each name: a round's, or a demo's.
func (m *Monitor) Save(names []string, results []check.Result) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, r := range results {
		if _, err := tx.Exec(`INSERT INTO results (check_name, time, state, took_ms, note) VALUES (?, ?, ?, ?, ?)`,
			names[i], r.Time.UnixMilli(), r.State, r.Took.Milliseconds(), r.Note); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// prune deletes the results too old to show.
func (m *Monitor) prune(now time.Time) error {
	_, err := m.db.Exec(`DELETE FROM results WHERE time < ?`, now.Add(-keep()).UnixMilli())
	return err
}

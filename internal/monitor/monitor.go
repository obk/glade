// Package monitor runs the checks every interval, keeps their results in
// SQLite and runs on_change when a check changes state.
package monitor

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/obk/glade/internal/check"
	"github.com/obk/glade/internal/config"
)

// A Scale is one series of counts kept for each check: the results in
// each Period, for the last Keep periods. The page shows the finest scale
// that covers how long glade has been running, so each is kept from the
// start.
type Scale struct {
	Name   string
	Period time.Duration
	Keep   int
}

var Scales = []Scale{
	{"minute", time.Minute, 60},         // an hour
	{"half-hour", 30 * time.Minute, 48}, // a day
	{"two-hour", 2 * time.Hour, 84},     // a week
	{"day", 24 * time.Hour, 90},         // 90 days
}

// A Record is what the page shows of one check. Results are kept by check
// name: renaming a check starts its history again.
type Record struct {
	Last    *check.Result
	Since   time.Time           // when Last.State began
	Start   time.Time           // the first result
	Buckets map[string][]Bucket // by scale name, oldest first
}

// Bucket counts the results in one period, from Start, in UTC.
type Bucket struct {
	Start    time.Time
	Up       int
	Degraded int
	Down     int
}

type Monitor struct {
	cfg        *config.Config
	client     *http.Client
	db         *sql.DB
	RetryAfter time.Duration // before a failed check is tried again

	mu      sync.Mutex
	records map[string]*Record // by check name
	checked time.Time          // when the last round finished
	pruned  time.Time          // when old results were last deleted
}

// New opens the database, making it if need be, and reads back the results
// an earlier run saved.
func New(cfg *config.Config) (*Monitor, error) {
	db, err := openDB(cfg.Database)
	if err != nil {
		return nil, err
	}
	m := &Monitor{
		cfg:        cfg,
		client:     &http.Client{},
		db:         db,
		RetryAfter: 2 * time.Second,
		records:    map[string]*Record{},
	}
	for _, ch := range cfg.Checks {
		m.records[ch.Name] = &Record{}
	}
	now := time.Now()
	if err := m.prune(now); err != nil {
		db.Close()
		return nil, err
	}
	m.pruned = now
	if err := m.load(now); err != nil {
		db.Close()
		return nil, err
	}
	return m, nil
}

// Close closes the database. Call it once Loop has returned.
func (m *Monitor) Close() error {
	return m.db.Close()
}

// Snapshot is the state after the last round: when it finished, and a
// copy of each check's record, by name.
type Snapshot struct {
	Checked time.Time
	Records map[string]Record
}

func (m *Monitor) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Snapshot{Checked: m.checked, Records: map[string]Record{}}
	for name, rec := range m.records {
		if rec.Last == nil {
			continue // not checked yet
		}
		r := *rec
		if rec.Last != nil {
			last := *rec.Last
			r.Last = &last
		}
		r.Buckets = map[string][]Bucket{}
		for scale, b := range rec.Buckets {
			r.Buckets[scale] = append([]Bucket(nil), b...)
		}
		s.Records[name] = r
	}
	return s
}

// Loop runs a round of checks now and then every interval, until ctx is
// done.
func (m *Monitor) Loop(ctx context.Context) {
	t := time.NewTicker(m.cfg.Interval)
	defer t.Stop()
	for {
		m.Round()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Round runs every check at once, records the results and saves them, and
// once an hour deletes the results too old to show.
func (m *Monitor) Round() {
	results := make([]check.Result, len(m.cfg.Checks))
	var wg sync.WaitGroup
	for i := range m.cfg.Checks {
		wg.Go(func() { results[i] = check.Run(m.cfg.Checks[i], m.client, m.RetryAfter) })
	}
	wg.Wait()

	type change struct {
		check, from string
		to          check.Result
	}
	var changes []change
	names := make([]string, len(m.cfg.Checks))
	m.mu.Lock()
	for i, ch := range m.cfg.Checks {
		names[i] = ch.Name
		if from := m.records[ch.Name].record(results[i]); from != "" && from != results[i].State {
			changes = append(changes, change{ch.Name, from, results[i]})
		}
	}
	m.checked = time.Now().UTC()
	m.mu.Unlock()

	// The page has the results already; one the database misses is lost
	// only if glade restarts.
	if err := m.Save(names, results); err != nil {
		log.Printf("saving the results: %v", err)
	}
	if now := time.Now(); now.Sub(m.pruned) >= time.Hour {
		if err := m.prune(now); err != nil {
			log.Printf("deleting old results: %v", err)
		}
		m.pruned = now
	}

	for _, c := range changes {
		why := c.to.Detail
		if why == "" {
			why = c.to.Note
		}
		log.Printf("%s: %s → %s %s", c.check, c.from, c.to.State, why)
		m.onChange(c.check, c.from, c.to)
	}
}

// record takes in a check's next result, and returns the state it was in
// before, or "" if it had none.
func (rec *Record) record(r check.Result) (from string) {
	if rec.Last != nil {
		from = rec.Last.State
	}
	if from != r.State {
		rec.Since = r.Time
	}
	rec.Last = &r
	rec.add(r)
	return from
}

// add counts a result in its period on every scale, and forgets periods
// too old to keep.
func (rec *Record) add(r check.Result) {
	if rec.Start.IsZero() {
		rec.Start = r.Time
	}
	if rec.Buckets == nil {
		rec.Buckets = map[string][]Bucket{}
	}
	for _, sc := range Scales {
		bs := rec.Buckets[sc.Name]
		start := r.Time.UTC().Truncate(sc.Period)
		if n := len(bs); n == 0 || !bs[n-1].Start.Equal(start) {
			bs = append(bs, Bucket{Start: start})
		}
		b := &bs[len(bs)-1]
		switch r.State {
		case check.Up:
			b.Up++
		case check.Degraded:
			b.Degraded++
		default:
			b.Down++
		}
		oldest := start.Add(-time.Duration(sc.Keep-1) * sc.Period)
		for len(bs) > 0 && bs[0].Start.Before(oldest) {
			bs = bs[1:]
		}
		rec.Buckets[sc.Name] = bs
	}
}

// onChange runs the on_change command, if there is one, with the change
// in its environment.
func (m *Monitor) onChange(name, from string, to check.Result) {
	if m.cfg.OnChange == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", m.cfg.OnChange)
	cmd.Env = append(os.Environ(),
		"GLADE_CHECK="+name,
		"GLADE_STATE="+to.State,
		"GLADE_PREVIOUS="+from,
		"GLADE_NOTE="+to.Note,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("on_change for %s: %v\n%s", name, err, out)
	}
}

package monitor

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obk/glade/internal/check"
	"github.com/obk/glade/internal/config"
)

func TestAdd(t *testing.T) {
	rec := &Record{}
	start := time.Date(2026, 1, 1, 12, 0, 30, 0, time.UTC)
	// A result every 20 minutes for 100 days, and a failure at the end.
	var last time.Time
	for at := start; at.Before(start.AddDate(0, 0, 100)); at = at.Add(20 * time.Minute) {
		rec.add(check.Result{Time: at, State: check.Up})
		last = at
	}
	rec.add(check.Result{Time: last, State: check.Down})

	if !rec.Start.Equal(start) {
		t.Errorf("start = %s, want the first result's time", rec.Start)
	}
	for _, sc := range Scales {
		bs := rec.Buckets[sc.Name]
		newest := last.Truncate(sc.Period)
		oldest := newest.Add(-time.Duration(sc.Keep-1) * sc.Period)
		if !bs[len(bs)-1].Start.Equal(newest) || bs[0].Start.Before(oldest) {
			t.Errorf("%s: buckets from %s to %s, want within %s to %s", sc.Name, bs[0].Start, bs[len(bs)-1].Start, oldest, newest)
		}
		if b := bs[len(bs)-1]; b.Down != 1 {
			t.Errorf("%s: the last bucket %+v has no failure", sc.Name, b)
		}
	}
	// Results 20 minutes apart: one in some minutes, one in each half-hour
	// or none, six in two hours, 72 in a day.
	if n := len(rec.Buckets["minute"]); n != 3 {
		t.Errorf("%d minute buckets in the last hour, want 3", n)
	}
	if b := rec.Buckets["two-hour"][0]; b.Up != 6 {
		t.Errorf("two-hour bucket: %+v, want 6 up", b)
	}
	if bs := rec.Buckets["day"]; len(bs) != 90 || bs[0].Up != 72 {
		t.Errorf("%d day buckets, the first %+v; want 90, 72 up", len(bs), bs[0])
	}
}

func TestRounds(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := &config.Config{
		Interval: time.Minute,
		Database: filepath.Join(dir, "glade.db"),
		OnChange: `echo "$GLADE_CHECK $GLADE_PREVIOUS $GLADE_STATE $GLADE_NOTE" >> ` + filepath.Join(dir, "changes.txt"),
		Checks:   []config.Check{{Name: "web", URL: srv.URL, Timeout: 5 * time.Second}},
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m.RetryAfter = 0

	if s := m.Snapshot(); !s.Checked.IsZero() || len(s.Records) != 0 {
		t.Errorf("before any round: %+v", s)
	}
	m.Round()
	status.Store(http.StatusBadGateway)
	m.Round()
	m.Round()
	status.Store(http.StatusOK)
	m.Round()

	b, err := os.ReadFile(filepath.Join(dir, "changes.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "web up down 502 Bad Gateway\nweb down up \n"; string(b) != want {
		t.Errorf("on_change ran with\n%q\nwant\n%q", b, want)
	}

	// A snapshot is a copy: the next round doesn't change it.
	s := m.Snapshot()
	m.Round()
	if rec := s.Records["web"]; rec.Last.State != check.Up || count(rec, "day") != [2]int{2, 2} {
		t.Errorf("snapshot: %+v", rec)
	}

	// The results are saved, and read again on a restart.
	before := m.Snapshot().Records["web"]
	m.Close()
	again, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	rec := again.Snapshot().Records["web"]
	if rec.Last == nil || rec.Last.State != check.Up || count(rec, "day") != [2]int{3, 2} {
		t.Errorf("results after a restart: %+v", rec)
	}
	// Times are kept to the millisecond.
	if !rec.Start.Equal(before.Start.Truncate(time.Millisecond)) || !rec.Since.Equal(before.Since.Truncate(time.Millisecond)) {
		t.Errorf("after a restart, start %s and since %s; before, %s and %s", rec.Start, rec.Since, before.Start, before.Since)
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Database: filepath.Join(dir, "glade.db"),
		Checks:   []config.Check{{Name: "web", URL: "http://web/"}},
	}
	m, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := check.Result{Time: now.Add(-keep() - time.Hour), State: check.Down, Note: "connection refused"}
	recent := check.Result{Time: now.Add(-time.Hour), State: check.Up}
	gone := check.Result{Time: now.Add(-time.Hour), State: check.Up}
	if err := m.Save([]string{"web", "web", "removed"}, []check.Result{old, recent, gone}); err != nil {
		t.Fatal(err)
	}
	if err := m.prune(now); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := m.db.QueryRow(`SELECT count(*) FROM results`).Scan(&n); err != nil || n != 2 {
		t.Errorf("%d results after pruning, want 2: %v", n, err)
	}
	m.Close()

	// Read back, only the configured check's recent result counts.
	again, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	s := again.Snapshot()
	if len(s.Records) != 1 || count(s.Records["web"], "day") != [2]int{1, 0} {
		t.Errorf("read back: %+v", s.Records)
	}
}

func TestNewerSchema(t *testing.T) {
	name := filepath.Join(t.TempDir(), "glade.db")
	db, err := openDB(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := New(&config.Config{Database: name}); err == nil || !strings.Contains(err.Error(), "newer glade") {
		t.Errorf("a database from a newer glade: %v", err)
	}
}

// count is the ups and downs on one scale.
func count(rec Record, scale string) [2]int {
	var n [2]int
	for _, b := range rec.Buckets[scale] {
		n[0] += b.Up
		n[1] += b.Down
	}
	return n
}

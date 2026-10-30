package server

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obk/glade/internal/config"
	"github.com/obk/glade/internal/monitor"
)

func TestChoose(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for age, want := range map[time.Duration]string{
		0:                    "1 hour",
		59 * time.Minute:     "1 hour",
		61 * time.Minute:     "1 day",
		23 * time.Hour:       "1 day",
		25 * time.Hour:       "1 week",
		8 * day:              "4 weeks",
		28*day + time.Minute: "40 days",
		45 * day:             "50 days",
		80*day + time.Hour:   "90 days",
		400 * day:            "90 days",
	} {
		if got := choose(now.Add(-age), now).Name; got != want {
			t.Errorf("after %s: %s, want %s", age, got, want)
		}
	}
	for _, w := range windows {
		if w.bars() > w.Scale.Keep {
			t.Errorf("%s: %d bars, but %s keeps only %d", w.Name, w.bars(), w.Scale.Name, w.Scale.Keep)
		}
	}
}

func TestBars(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 40, 10, 0, time.UTC)
	hour := windows[0]
	recorded := []monitor.Bucket{
		{Start: time.Date(2026, 10, 15, 11, 41, 0, 0, time.UTC), Up: 1},   // the first bar
		{Start: time.Date(2026, 10, 15, 12, 39, 0, 0, time.UTC), Down: 1}, // the one before last
		{Start: time.Date(2026, 10, 15, 12, 40, 0, 0, time.UTC), Up: 5, Degraded: 1},
	}
	if b := bars(recorded, hour, now)[59]; b.Label != "15 Oct, 12:40 UTC: 100% up, 16.66% degraded" {
		t.Errorf("a degraded bar's label: %q", b.Label)
	}
	b := bars(recorded, hour, now)
	if len(b) != 60 || b[0] != (barView{"up", "15 Oct, 11:41 UTC: 100% up"}) || b[58].State != "down" || b[59].State != "partial" || b[30].State != "none" {
		t.Errorf("an hour's bars: first %+v, 30th %+v, last two %+v %+v", b[0], b[30], b[58], b[59])
	}
	if got := uptime(recorded, hour, now); got != "87.50%" {
		t.Errorf("uptime = %s, want 87.50%%", got)
	}
	// Results from before the window don't count.
	old := append([]monitor.Bucket{{Start: now.Add(-2 * time.Hour), Down: 100}}, recorded...)
	if got := uptime(old, hour, now); got != "87.50%" {
		t.Errorf("uptime with older results = %s, want 87.50%%", got)
	}

	dayBars := bars(nil, windows[1], now)
	if len(dayBars) != 48 || dayBars[47].Label != "15 Oct, 12:30–13:00 UTC: no checks" {
		t.Errorf("a day's bars: %d, the last %+v", len(dayBars), dayBars[47])
	}
	// A day with one failure in many is partial, not down.
	partial := bars([]monitor.Bucket{{Start: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), Up: 1439, Down: 1}}, windows[9], now)
	if s := partial[89]; s != (barView{"partial", "15 Oct 2026: 99.93% up"}) {
		t.Errorf("one failure in a day: %+v", s)
	}
	if len(partial) != 90 || partial[0].Label != "18 Jul 2026: no checks" {
		t.Errorf("90 days: %d bars, the first %+v", len(partial), partial[0])
	}
}

func TestSpan(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Minute: "30 minutes", 15 * time.Hour: "15 hours", 60 * time.Hour: "60 hours", 30 * day: "30 days", day: "1 day",
	} {
		if got := span(d); got != want {
			t.Errorf("span(%s) = %s, want %s", d, got, want)
		}
	}
}

func TestPercent(t *testing.T) {
	for _, tc := range []struct {
		n, total int
		want     string
	}{{1, 1, "100%"}, {99999, 100000, "99.99%"}, {1, 3, "33.33%"}, {0, 5, "0.00%"}} {
		if got := percent(tc.n, tc.total); got != tc.want {
			t.Errorf("percent(%d, %d) = %s, want %s", tc.n, tc.total, got, tc.want)
		}
	}
}

func TestShortAndAgo(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Minute: "1m", 10 * time.Second: "10s", 90 * time.Second: "1m30s", time.Hour: "1h", 2*time.Hour + 5*time.Minute: "2h5m",
	} {
		if got := short(d); got != want {
			t.Errorf("short(%s) = %s, want %s", d, got, want)
		}
	}
	now := time.Now()
	for d, want := range map[time.Duration]string{
		10 * time.Second: "under a minute", time.Minute: "1 minute", 5 * time.Minute: "5 minutes",
		3 * time.Hour: "3 hours", 72 * time.Hour: "3 days",
	} {
		if got := ago(now.Add(-d), now); got != want {
			t.Errorf("ago(%s) = %s, want %s", d, got, want)
		}
	}
}

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w.Code, w.Body.String()
}

func writeFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPage(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()

	defer func(ttl time.Duration) { incidentsTTL = ttl }(incidentsTTL)
	incidentsTTL = 0 // read on every load, to see each change

	dir := t.TempDir()
	cfg := &config.Config{
		Title:     "Test status",
		URL:       "https://status.example.com/",
		Interval:  time.Minute,
		Database:  filepath.Join(dir, "glade.db"),
		Incidents: filepath.Join(dir, "incidents"),
		Checks:    []config.Check{{Name: "web", URL: srv.URL, Timeout: 5 * time.Second}},
	}
	mon, err := monitor.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer mon.Close()
	mon.RetryAfter = 0
	h := New(cfg, mon)

	if code, body := get(t, h, "/"); code != 200 || !strings.Contains(body, "Checking…") || !strings.Contains(body, "Not checked yet") {
		t.Errorf("before any round: %d\n%s", code, body)
	}
	if _, js := get(t, h, "/status.json"); strings.Contains(js, `"checked"`) {
		t.Errorf("status.json before any round has a time:\n%s", js)
	}

	mon.Round()
	_, body := get(t, h, "/")
	for _, want := range []string{
		"<title>Test status</title>", "All systems normal", `<meta http-equiv="refresh" content="60">`,
		"every 1m", "Up for under a minute", "100%", `class="s-up"`, `title="` + time.Now().UTC().Format("2 Jan, 15:04 UTC") + `: 100% up"`,
		`<span class="wide">1 hour ago</span><span class="narrow">30 minutes ago</span><span>Now</span>`,
		`title="Uptime over the last 1 hour"`,
		".bars li", // the stylesheet, inlined
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page: no %q in\n%s", want, body)
		}
	}

	status.Store(http.StatusServiceUnavailable)
	mon.Round()
	if _, body := get(t, h, "/"); !strings.Contains(body, "web is having problems") || !strings.Contains(body, "Down for under a minute: 503 Service Unavailable") {
		t.Errorf("page while down:\n%s", body)
	}

	resolved := time.Now().UTC().AddDate(0, 0, -1).Format(time.DateOnly)
	writeFile(t, filepath.Join(dir, "incidents", "outage.md"), "---\ntitle: The web was down\ndate: 2026-10-01 09:00\nresolved: "+resolved+"\n---\nA **restart** fixed it.\n")
	writeFile(t, filepath.Join(dir, "incidents", "now.md"), "---\ntitle: Looking into it\ndate: 2026-10-15\n---\nWe know.\n")
	_, body = get(t, h, "/")
	if !strings.Contains(body, `<article class="incident open" id="now">`) || !strings.Contains(body, "Since 15 Oct 2026, 00:00 UTC") {
		t.Errorf("ongoing incident not shown:\n%s", body)
	}
	if !strings.Contains(body, `<article class="incident" id="outage">`) || !strings.Contains(body, "<strong>restart</strong>") {
		t.Errorf("past incident not shown:\n%s", body)
	}

	code, feed := get(t, h, "/feed.xml")
	var f struct {
		Entries []struct {
			ID    string `xml:"id"`
			Title string `xml:"title"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal([]byte(feed), &f); code != 200 || err != nil {
		t.Fatalf("feed: %d %v\n%s", code, err, feed)
	}
	if len(f.Entries) != 2 || f.Entries[0].Title != "Looking into it" || f.Entries[1].Title != "Resolved: The web was down" || f.Entries[0].ID != "https://status.example.com/#now" {
		t.Errorf("feed entries: %+v", f.Entries)
	}

	_, js := get(t, h, "/status.json")
	var s struct {
		Checked time.Time
		Checks  []struct{ Name, State, Note string }
	}
	if err := json.Unmarshal([]byte(js), &s); err != nil || s.Checked.IsZero() || len(s.Checks) != 1 || s.Checks[0].State != "down" || s.Checks[0].Note != "503 Service Unavailable" {
		t.Errorf("status.json: %v\n%s", err, js)
	}

	// A broken incident is a line on the page, not a broken page, and the
	// incidents read before stay.
	writeFile(t, filepath.Join(dir, "incidents", "bad.md"), "no front matter")
	if code, body := get(t, h, "/"); code != 200 || !strings.Contains(body, `<p class="problem">`) || !strings.Contains(body, `id="now"`) {
		t.Errorf("broken incident: %d\n%s", code, body)
	}
	if code, feed := get(t, h, "/feed.xml"); code != 200 || !strings.Contains(feed, "Looking into it") {
		t.Errorf("feed with a broken incident: %d\n%s", code, feed)
	}

	if code, _ := get(t, h, "/nothing"); code != 404 {
		t.Errorf("/nothing: %d, want 404", code)
	}
}

// The incidents are read again only after incidentsTTL, however many
// times the page is loaded.
func TestIncidentsCached(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{Incidents: dir, Interval: time.Minute}
	s := &server{cfg: cfg}
	if all, err := s.incidents(); err != nil || len(all) != 0 {
		t.Fatalf("empty folder: %v, %v", all, err)
	}
	writeFile(t, filepath.Join(dir, "new.md"), "---\ntitle: New\ndate: 2026-10-15\n---\n")
	if all, _ := s.incidents(); len(all) != 0 {
		t.Errorf("read again within incidentsTTL: %+v", all)
	}
	s.read = time.Now().Add(-incidentsTTL)
	if all, _ := s.incidents(); len(all) != 1 {
		t.Errorf("not read again after incidentsTTL: %+v", all)
	}
}

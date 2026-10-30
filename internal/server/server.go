// Package server serves the status page, the incidents feed and
// status.json.
package server

import (
	"bytes"
	"embed"
	"html/template"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/obk/glade/internal/check"
	"github.com/obk/glade/internal/config"
	"github.com/obk/glade/internal/incident"
	"github.com/obk/glade/internal/monitor"
)

//go:embed templates static
var assets embed.FS

var (
	page = template.Must(template.New("page.html").Funcs(template.FuncMap{
		"when": func(t time.Time) string { return t.UTC().Format("2 Jan 2006, 15:04 UTC") },
		"ago":  ago,
	}).ParseFS(assets, "templates/page.html"))
	style = template.CSS(must(assets.ReadFile("static/style.css")))
)

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

type server struct {
	cfg *config.Config
	mon *monitor.Monitor

	mu      sync.Mutex
	read    time.Time // when the incidents were last read
	cached  []incident.Incident
	readErr error
}

// incidentsTTL is how long the incidents are kept before they are read
// again, so that a flood of page loads doesn't mean a flood of reads.
var incidentsTTL = 10 * time.Second

// incidents reads the incidents folder, at most every incidentsTTL. If a
// file can't be read, the error is logged, once each time, and the last
// incidents read are kept: one bad file mustn't hide an ongoing incident.
func (s *server) incidents() ([]incident.Incident, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.read.IsZero() || time.Since(s.read) >= incidentsTTL {
		all, err := incident.ReadAll(s.cfg.Incidents)
		if err == nil {
			s.cached = all
		} else {
			log.Print(err)
		}
		s.read, s.readErr = time.Now(), err
	}
	return s.cached, s.readErr
}

// New is the handler for the whole site.
func New(cfg *config.Config, mon *monitor.Monitor) http.Handler {
	s := &server{cfg: cfg, mon: mon}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.servePage)
	mux.HandleFunc("GET /feed.xml", s.serveFeed)
	mux.HandleFunc("GET /status.json", s.serveJSON)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-cache")
		mux.ServeHTTP(w, r)
	})
}

// view is what the page shows.
type view struct {
	Title    string
	Style    template.CSS // inlined, so the page is one request
	Refresh  int          // seconds between reloads, the interval
	Interval string       // as "1m"
	State    string       // the worst check's, or "" before the first round
	Summary  string
	Checked  time.Time
	Now      time.Time
	Checks   []checkView
	From     string              // the start of the bars: "1 day ago"
	Narrow   string              // the same on a phone, which shows the last 30 bars
	To       string              // their end: "Now" or "Today"
	Span     string              // the window: "1 day"
	Open     []incident.Incident // ongoing, newest first
	Past     []incident.Incident // resolved in the days shown, newest first
	Problem  string              // why the incidents couldn't be read
}

type checkView struct {
	Name   string
	Last   *check.Result
	Text   string // "Up for 3 days", "Down for 5 minutes: 502 Bad Gateway"
	Uptime string // over the window, degraded counting as up
	Bars   []barView
}

type barView struct {
	State string // up, partial, down, or none for a period with no checks
	Label string
}

// pastIncidents is how long a resolved incident stays on the page.
const pastIncidents = 90 * day

func (s *server) servePage(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	v := view{
		Title:    s.cfg.Title,
		Style:    style,
		Refresh:  int(s.cfg.Interval.Seconds()),
		Interval: short(s.cfg.Interval),
		Now:      now,
	}
	// A broken incident file mustn't take the page down with it: it shows
	// as a line on the page instead.
	incidents, err := s.incidents()
	if err != nil {
		v.Problem = "An incident couldn't be read; see glade's log."
	}
	oldest := now.Add(-pastIncidents)
	for _, in := range incidents {
		switch {
		case in.Resolved.IsZero():
			v.Open = append(v.Open, in)
		case in.Resolved.After(oldest):
			v.Past = append(v.Past, in)
		}
	}

	snap := s.mon.Snapshot()
	v.Checked = snap.Checked

	// Every check's bars cover the same window, from the oldest history.
	start := now
	for _, ch := range s.cfg.Checks {
		if rec, ok := snap.Records[ch.Name]; ok && !rec.Start.IsZero() && rec.Start.Before(start) {
			start = rec.Start
		}
	}
	win := choose(start, now)
	v.From, v.Narrow, v.To, v.Span = win.Name+" ago", win.Name+" ago", "Now", win.Name
	if win.bars() > 30 {
		v.Narrow = span(30*win.Scale.Period) + " ago"
	}
	if win.Scale.Period >= day {
		v.To = "Today"
	}

	var bad []string
	worst := ""
	for _, ch := range s.cfg.Checks {
		cv := checkView{Name: ch.Name, Text: "Not checked yet"}
		if rec, ok := snap.Records[ch.Name]; ok && rec.Last != nil {
			last := rec.Last
			cv.Last = last
			cv.Text = strings.ToUpper(last.State[:1]) + last.State[1:] + " for " + ago(rec.Since, now)
			if last.Note != "" {
				cv.Text += ": " + last.Note
			}
			cv.Uptime = uptime(rec.Buckets[win.Scale.Name], win, now)
			cv.Bars = bars(rec.Buckets[win.Scale.Name], win, now)
			if rank(last.State) > rank(worst) {
				worst = last.State
			}
			if last.State != check.Up {
				bad = append(bad, ch.Name)
			}
		} else {
			cv.Bars = bars(nil, win, now)
		}
		v.Checks = append(v.Checks, cv)
	}

	v.State = worst
	switch worst {
	case "":
		v.Summary = "Checking…"
	case check.Up:
		v.Summary = "All systems normal"
	case check.Degraded:
		v.Summary = list(bad) + " " + verb(bad) + " degraded"
	case check.Down:
		v.Summary = "Some systems are down"
		if len(bad) <= 3 {
			v.Summary = list(bad) + " " + verb(bad) + " having problems"
		}
	}

	var b bytes.Buffer
	if err := page.Execute(&b, v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b.Bytes())
}

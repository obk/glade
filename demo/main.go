// Command demo makes up a history for trying out glade: demo/glade.db
// with a result a minute for each check in demo/glade.toml, ending now, and
// incidents in demo/incidents to go with its outages. Run it from the glade
// folder:
//
//	go run ./demo               # 90 days
//	go run ./demo -age 5h       # less, to see the shorter windows
//	go run . -config demo/glade.toml
//
// Safe to re-run; the database and incidents are made again each time.
package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/obk/glade/internal/check"
	"github.com/obk/glade/internal/config"
	"github.com/obk/glade/internal/monitor"
)

const day = 24 * time.Hour

// An event is a stretch of time when a check wasn't up, from and to as
// how long ago.
type event struct {
	check    string
	from, to time.Duration
	state    string
	note     string
	took     time.Duration // 0: the check's usual time
}

var events = []event{
	{"example.com", 12*day + 3*time.Hour, 12*day + 2*time.Hour + 35*time.Minute, check.Down, "502 Bad Gateway", 0},
	{"example.com", 41*day + 5*time.Hour, 41*day + 4*time.Hour + 40*time.Minute, check.Down, "no answer within 10s", 10 * time.Second},
	{"example.org", 33*day + 9*time.Hour, 33*day + 5*time.Hour, check.Degraded, "slow: 2.4s", 2400 * time.Millisecond},
	{"example.org", 70*day + 9*time.Hour, 70*day + 5*time.Hour, check.Down, "the connection was closed", 0},
	{"example.net", 58*day + 12*time.Hour, 55*day + 9*time.Hour, check.Degraded, "the certificate expires in 9 days", 0},
	{"example.net", 2*day + 5*time.Hour, 2*day + 3*time.Hour, check.Down, "connection refused", time.Millisecond},
	{"example.net", 2 * time.Hour, 0, check.Degraded, "slow: 152ms", 152 * time.Millisecond},
	{"Mail", 45 * time.Minute, 0, check.Down, "connection refused", time.Millisecond},
}

// usual is how long each check takes when all is well.
var usual = map[string]time.Duration{
	"example.com": 80 * time.Millisecond,
	"example.org": 95 * time.Millisecond,
	"example.net": 150 * time.Millisecond,
	"Mail":        15 * time.Millisecond,
}

// An incident is written for an event, if the history goes back that far.
type incident struct {
	slug, title, body string
	from, to          time.Duration // to 0: ongoing
}

var incidents = []incident{
	{"mail", "Mail server unreachable", "**{from}** The mail server stopped accepting connections. We're looking into it.", 45 * time.Minute, 0},
	{"example-net", "example.net unreachable", "**{to}** Back after a restart of the server.\n\n**{from}** example.net isn't answering.", 2*day + 5*time.Hour, 2*day + 3*time.Hour},
	{"example-com", "example.com returned errors", "**{to}** Fixed: a bad deploy, rolled back.\n\n**{from}** example.com answers with 502 errors.", 12*day + 3*time.Hour, 12*day + 2*time.Hour + 35*time.Minute},
	{"example-org-down", "example.org unreachable", "**{to}** Back: the server's disk was full.\n\n**{from}** The site closes every connection.", 70*day + 9*time.Hour, 70*day + 5*time.Hour},
	{"example-org-slow", "example.org slow", "**{to}** Back to normal.\n\n**{from}** Pages take a few seconds to load.", 33*day + 9*time.Hour, 33*day + 5*time.Hour},
}

func main() {
	ageFlag := flag.String("age", "90d", "how much history to make, such as 30m, 5h or 3d; at most 90d")
	flag.Parse()
	age, err := parseAge(*ageFlag)
	if err != nil || age < time.Minute || age > 90*day {
		fail(fmt.Sprintf("-age %s: a duration from 1m to 90d, such as 30m, 5h or 3d", *ageFlag))
	}

	cfg, err := config.Load(filepath.Join("demo", "glade.toml"))
	if err != nil {
		fail("run this from the glade folder: go run ./demo (" + err.Error() + ")")
	}
	for _, ch := range cfg.Checks {
		if _, ok := usual[ch.Name]; !ok {
			fail("demo/glade.toml has a check the demo doesn't know: " + ch.Name)
		}
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(cfg.Database + suffix); err != nil && !os.IsNotExist(err) {
			fail(err.Error())
		}
	}
	m, err := monitor.New(cfg)
	if err != nil {
		fail(err.Error())
	}

	// The same history every time, ending a minute ago, so that the first
	// live round carries on from it.
	rng := rand.New(rand.NewPCG(1, 2))
	now := time.Now().UTC()
	var (
		names   []string
		results []check.Result
		total   int
	)
	for at := now.Add(-age).Truncate(time.Minute); at.Before(now.Add(-30 * time.Second)); at = at.Add(time.Minute) {
		for _, ch := range cfg.Checks {
			names = append(names, ch.Name)
			results = append(results, result(ch.Name, at, now, rng))
		}
		if len(results) >= 20000 {
			save(m, names, results)
			total += len(results)
			names, results = names[:0], results[:0]
		}
	}
	save(m, names, results)
	total += len(results)
	if err := m.Close(); err != nil {
		fail(err.Error())
	}

	written := writeIncidents(cfg.Incidents, age, now)
	fmt.Printf("%d results over %s in %s, and %d incidents in %s\n", total, *ageFlag, cfg.Database, written, cfg.Incidents)
	fmt.Println("now run: go run . -config demo/glade.toml")
}

// result is a check's made-up result at a time: an event's, now and then a
// timeout, or up.
func result(name string, at, now time.Time, rng *rand.Rand) check.Result {
	ago := now.Sub(at)
	took := usual[name] + time.Duration(rng.Int64N(int64(usual[name]/2)))
	r := check.Result{Time: at.Add(time.Duration(rng.Int64N(int64(5 * time.Second)))), State: check.Up, Took: took}
	for _, e := range events {
		if e.check == name && ago <= e.from && ago > e.to {
			r.State, r.Note = e.state, e.note
			if e.took != 0 {
				r.Took = e.took + time.Duration(rng.Int64N(int64(e.took/10+1)))
			}
			return r
		}
	}
	// One check in about 50,000 fails on its own: a partial day now and
	// then.
	if rng.IntN(50000) == 0 {
		r.State, r.Note, r.Took = check.Down, "no answer within 10s", 10*time.Second
	}
	return r
}

func save(m *monitor.Monitor, names []string, results []check.Result) {
	if err := m.Save(names, results); err != nil {
		fail(err.Error())
	}
}

// writeIncidents replaces the incidents folder with the incidents that
// began within the history.
func writeIncidents(dir string, age time.Duration, now time.Time) int {
	if err := os.RemoveAll(dir); err != nil {
		fail(err.Error())
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err.Error())
	}
	n := 0
	for _, in := range incidents {
		if in.from > age {
			continue
		}
		from := now.Add(-in.from)
		front := "---\ntitle: " + in.title + "\ndate: " + from.Format("2006-01-02 15:04") + "\n"
		body := strings.ReplaceAll(in.body, "{from}", from.Format("15:04"))
		if in.to != 0 {
			to := now.Add(-in.to)
			front += "resolved: " + to.Format("2006-01-02 15:04") + "\n"
			body = strings.ReplaceAll(body, "{to}", to.Format("15:04"))
		}
		name := filepath.Join(dir, from.Format("2006-01-02")+"-"+in.slug+".md")
		if err := os.WriteFile(name, []byte(front+"---\n\n"+body+"\n"), 0o644); err != nil {
			fail(err.Error())
		}
		n++
	}
	return n
}

// parseAge is a duration that may also be in days: 3d.
func parseAge(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		return time.Duration(n) * day, err
	}
	return time.ParseDuration(s)
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "demo:", msg)
	os.Exit(1)
}

package server

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/obk/glade/internal/check"
	"github.com/obk/glade/internal/monitor"
)

func rank(state string) int {
	switch state {
	case check.Up:
		return 1
	case check.Degraded:
		return 2
	case check.Down:
		return 3
	}
	return 0
}

// A window is how much history the bars show, and on which scale.
type window struct {
	Name  string // "1 hour", "40 days"
	Span  time.Duration
	Scale monitor.Scale
}

func (w window) bars() int { return int(w.Span / w.Scale.Period) }

const day = 24 * time.Hour

// windows grow with glade's history, so that a new page isn't 90 days of
// grey: the last hour at first, then a day, a week, four weeks, and on to
// 90 days in steps of ten.
var windows = []window{
	{"1 hour", time.Hour, scale("minute")},
	{"1 day", day, scale("half-hour")},
	{"1 week", 7 * day, scale("two-hour")},
	{"4 weeks", 28 * day, scale("day")},
	{"40 days", 40 * day, scale("day")},
	{"50 days", 50 * day, scale("day")},
	{"60 days", 60 * day, scale("day")},
	{"70 days", 70 * day, scale("day")},
	{"80 days", 80 * day, scale("day")},
	{"90 days", 90 * day, scale("day")},
}

func scale(name string) monitor.Scale {
	for _, sc := range monitor.Scales {
		if sc.Name == name {
			return sc
		}
	}
	panic("no scale " + name)
}

// choose is the smallest window that covers the history since start.
func choose(start, now time.Time) window {
	for _, w := range windows {
		if now.Sub(start) <= w.Span {
			return w
		}
	}
	return windows[len(windows)-1]
}

// bars is one bar for each period in the window, oldest first, with the
// periods that have no results left grey.
func bars(recorded []monitor.Bucket, w window, now time.Time) []barView {
	byStart := map[int64]monitor.Bucket{}
	for _, b := range recorded {
		byStart[b.Start.Unix()] = b
	}
	p := w.Scale.Period
	last := now.UTC().Truncate(p)
	out := make([]barView, w.bars())
	for i := range out {
		start := last.Add(-time.Duration(len(out)-1-i) * p)
		label := period(start, p)
		b := byStart[start.Unix()]
		total := b.Up + b.Degraded + b.Down
		if total == 0 {
			out[i] = barView{"none", label + ": no checks"}
			continue
		}
		state := check.Up
		switch {
		case b.Down*10 > total: // down for more than a tenth of the period's checks
			state = check.Down
		case b.Down+b.Degraded > 0:
			state = "partial"
		}
		label = fmt.Sprintf("%s: %s up", label, percent(b.Up+b.Degraded, total))
		if b.Degraded > 0 {
			label += ", " + percent(b.Degraded, total) + " degraded"
		}
		out[i] = barView{state, label}
	}
	return out
}

// period names the time a bar covers.
func period(start time.Time, p time.Duration) string {
	switch {
	case p >= day:
		return start.Format("2 Jan 2006")
	case p <= time.Minute:
		return start.Format("2 Jan, 15:04 UTC")
	}
	return start.Format("2 Jan, 15:04") + "–" + start.Add(p).Format("15:04") + " UTC"
}

// uptime is over the window, degraded counting as up.
func uptime(recorded []monitor.Bucket, w window, now time.Time) string {
	first := now.UTC().Truncate(w.Scale.Period).Add(-time.Duration(w.bars()-1) * w.Scale.Period)
	var ok, total int
	for _, b := range recorded {
		if b.Start.Before(first) {
			continue
		}
		ok += b.Up + b.Degraded
		total += b.Up + b.Degraded + b.Down
	}
	if total == 0 {
		return ""
	}
	return percent(ok, total)
}

// span is a length of time in words: 30 minutes, 15 hours, 30 days.
func span(d time.Duration) string {
	switch {
	case d%day == 0:
		return plural(int(d/day), "day")
	case d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/time.Minute), "minute")
}

// percent rounds down, so that 99.999% never shows as 100%.
func percent(n, total int) string {
	p := math.Floor(float64(n)*10000/float64(total)) / 100
	if p == 100 {
		return "100%"
	}
	return fmt.Sprintf("%.2f%%", p)
}

// short is a duration as people write it: 1m, not 1m0s.
func short(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

func ago(since, now time.Time) string {
	d := now.Sub(since)
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 48*time.Hour:
		return plural(int(d.Hours()), "hour")
	}
	return plural(int(d.Hours()/24), "day")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func list(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func verb(names []string) string {
	if len(names) == 1 {
		return "is"
	}
	return "are"
}

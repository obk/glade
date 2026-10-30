// Package incident reads incidents: markdown files written by hand, one
// for each.
package incident

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Incident is one file in the incidents folder:
//
//	---
//	title: example.net unreachable
//	date: 2026-10-15 14:20
//	resolved: 2026-10-15 15:05
//	---
//
//	**15:05** The server is back after a restart.
//
// Times are UTC. One without resolved: is ongoing.
type Incident struct {
	Slug     string // the file's name without .md
	Title    string
	Date     time.Time
	Resolved time.Time // zero while ongoing
	Body     template.HTML
}

var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

// ReadAll reads every incident in dir, newest first. A missing folder has
// none.
func ReadAll(dir string) ([]Incident, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, err
	}
	var all []Incident
	for _, name := range names {
		in, err := Read(name)
		if err != nil {
			return nil, err
		}
		all = append(all, in)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Date.After(all[j].Date) })
	return all, nil
}

// Read reads one incident.
func Read(name string) (Incident, error) {
	raw, err := os.ReadFile(name)
	if err != nil {
		return Incident{}, err
	}
	in := Incident{Slug: strings.TrimSuffix(filepath.Base(name), ".md")}
	parts := strings.SplitN(strings.ReplaceAll(string(raw), "\r\n", "\n"), "---\n", 3)
	if len(parts) != 3 || parts[0] != "" {
		return Incident{}, fmt.Errorf("%s: no front matter: start with ---, title: ..., date: ..., ---", name)
	}
	for _, line := range strings.Split(parts[1], "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		k, v, _ := strings.Cut(line, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "title":
			in.Title = v
		case "date", "resolved":
			t, err := parseTime(v)
			if err != nil {
				return Incident{}, fmt.Errorf("%s: %s: %w", name, k, err)
			}
			if k == "date" {
				in.Date = t
			} else {
				in.Resolved = t
			}
		default:
			return Incident{}, fmt.Errorf("%s: %q in the front matter: only title, date and resolved", name, line)
		}
	}
	if in.Title == "" || in.Date.IsZero() {
		return Incident{}, fmt.Errorf("%s: an incident needs a title and a date", name)
	}
	var b bytes.Buffer
	if err := md.Convert([]byte(parts[2]), &b); err != nil {
		return Incident{}, fmt.Errorf("%s: %w", name, err)
	}
	in.Body = template.HTML(b.String())
	return in, nil
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04", time.DateOnly} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("%q: not 2006-01-02 15:04 or 2006-01-02", s)
}

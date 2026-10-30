package server

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"time"
)

// serveJSON is the current state, for scripts and other monitors.
func (s *server) serveJSON(w http.ResponseWriter, r *http.Request) {
	type checkJSON struct {
		Name  string    `json:"name"`
		State string    `json:"state"`
		Note  string    `json:"note,omitempty"`
		Since time.Time `json:"since"`
		Took  int64     `json:"took_ms"`
	}
	var out struct {
		Checked *time.Time  `json:"checked,omitempty"` // left out until the first round ends
		Checks  []checkJSON `json:"checks"`
	}
	snap := s.mon.Snapshot()
	if !snap.Checked.IsZero() {
		out.Checked = &snap.Checked
	}
	for _, ch := range s.cfg.Checks {
		c := checkJSON{Name: ch.Name, State: "unknown"}
		if rec, ok := snap.Records[ch.Name]; ok && rec.Last != nil {
			c.State, c.Note, c.Since, c.Took = rec.Last.State, rec.Last.Note, rec.Since, rec.Last.Took.Milliseconds()
		}
		out.Checks = append(out.Checks, c)
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "\t")
	enc.Encode(out)
}

// serveFeed is the incidents as an Atom feed, newest first.
func (s *server) serveFeed(w http.ResponseWriter, r *http.Request) {
	// With a file that can't be read, the last incidents read; the log
	// says what's wrong.
	incidents, _ := s.incidents()
	// From the config, never the request: a request's Host header can say
	// anything.
	base := s.cfg.URL
	if base == "" {
		base = "http://" + s.cfg.Listen + "/"
	}

	type link struct {
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr,omitempty"`
	}
	type content struct {
		Type string `xml:"type,attr"`
		Body string `xml:",chardata"`
	}
	type entry struct {
		ID      string  `xml:"id"`
		Title   string  `xml:"title"`
		Updated string  `xml:"updated"`
		Link    link    `xml:"link"`
		Content content `xml:"content"`
	}
	feed := struct {
		XMLName xml.Name `xml:"http://www.w3.org/2005/Atom feed"`
		ID      string   `xml:"id"`
		Title   string   `xml:"title"`
		Updated string   `xml:"updated"`
		Links   []link   `xml:"link"`
		Author  string   `xml:"author>name"`
		Entries []entry  `xml:"entry"`
	}{
		ID:      base,
		Title:   s.cfg.Title + ": incidents",
		Updated: time.Unix(0, 0).UTC().Format(time.RFC3339),
		Links:   []link{{Href: base}, {Href: base + "feed.xml", Rel: "self"}},
		Author:  s.cfg.Title,
	}
	for i, in := range incidents {
		if i == 20 {
			break
		}
		title, updated := in.Title, in.Date
		if !in.Resolved.IsZero() {
			title, updated = "Resolved: "+in.Title, in.Resolved
		}
		if u := updated.Format(time.RFC3339); u > feed.Updated {
			feed.Updated = u
		}
		feed.Entries = append(feed.Entries, entry{
			ID:      base + "#" + in.Slug,
			Title:   title,
			Updated: updated.Format(time.RFC3339),
			Link:    link{Href: base + "#" + in.Slug},
			Content: content{Type: "html", Body: string(in.Body)},
		})
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "\t")
	enc.Encode(feed)
}

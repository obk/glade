// Package config loads glade's TOML configuration file.
package config

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Title     string        `toml:"title"`
	URL       string        `toml:"url"` // the page's public address, for the feed's links; http://listen/ if empty
	Listen    string        `toml:"listen"`
	Interval  time.Duration `toml:"interval"`  // between rounds of checks
	Database  string        `toml:"database"`  // SQLite file, relative to the config's folder
	Incidents string        `toml:"incidents"` // folder, relative to the config's folder
	OnChange  string        `toml:"on_change"` // shell command run when a check changes state
	Checks    []Check       `toml:"check"`
}

type Check struct {
	Name     string        `toml:"name"`
	URL      string        `toml:"url"`      // an HTTP(S) check: the page must answer 2xx
	Contains string        `toml:"contains"` // and, if set, include this text
	TCP      string        `toml:"tcp"`      // a TCP check: host:port must accept a connection
	Timeout  time.Duration `toml:"timeout"`
	Slow     time.Duration `toml:"slow"` // slower than this is degraded; 0 never is
}

// Load reads the config and fills in the defaults. Relative paths in it
// become relative to the current folder.
func Load(name string) (*Config, error) {
	c := &Config{
		Title:     "Status",
		Listen:    "127.0.0.1:8080",
		Interval:  time.Minute,
		Database:  "glade.db",
		Incidents: "incidents",
	}
	md, err := toml.DecodeFile(name, c)
	if err != nil {
		return nil, err
	}
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("%s: unknown setting %s", name, u[0])
	}
	dir := filepath.Dir(name)
	if !filepath.IsAbs(c.Database) {
		c.Database = filepath.Join(dir, c.Database)
	}
	if !filepath.IsAbs(c.Incidents) {
		c.Incidents = filepath.Join(dir, c.Incidents)
	}

	if c.URL != "" {
		if u, err := url.Parse(c.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%s: url %q is not http:// or https://", name, c.URL)
		}
		if !strings.HasSuffix(c.URL, "/") {
			c.URL += "/"
		}
	}

	if c.Interval < 10*time.Second {
		return nil, fmt.Errorf("%s: interval %s: at least 10s", name, c.Interval)
	}
	if len(c.Checks) == 0 {
		return nil, fmt.Errorf("%s: no checks: add a [[check]] with a name and a url or tcp", name)
	}
	seen := map[string]bool{}
	for i := range c.Checks {
		ch := &c.Checks[i]
		if ch.Name == "" {
			return nil, fmt.Errorf("%s: check %d has no name", name, i+1)
		}
		if seen[ch.Name] {
			return nil, fmt.Errorf("%s: two checks named %q", name, ch.Name)
		}
		seen[ch.Name] = true
		switch {
		case ch.URL != "" && ch.TCP != "":
			return nil, fmt.Errorf("%s: check %q: a url or tcp, not both", name, ch.Name)
		case ch.URL != "":
			u, err := url.Parse(ch.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, fmt.Errorf("%s: check %q: url %q is not http:// or https://", name, ch.Name, ch.URL)
			}
		case ch.TCP != "":
			if _, _, err := net.SplitHostPort(ch.TCP); err != nil {
				return nil, fmt.Errorf("%s: check %q: tcp %q is not host:port", name, ch.Name, ch.TCP)
			}
			if ch.Contains != "" {
				return nil, fmt.Errorf("%s: check %q: contains is for url checks", name, ch.Name)
			}
		default:
			return nil, fmt.Errorf("%s: check %q has no url or tcp", name, ch.Name)
		}
		if ch.Timeout == 0 {
			ch.Timeout = 10 * time.Second
		}
	}
	return c, nil
}

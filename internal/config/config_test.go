package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "glade.toml")
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestLoad(t *testing.T) {
	name := write(t, `
title = "spinney status"
interval = "30s"

[[check]]
name = "site"
url = "https://example.com/"
contains = "Example"
slow = "2s"

[[check]]
name = "ssh"
tcp = "example.com:22"
timeout = "3s"
`)
	c, err := Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "spinney status" || c.Interval != 30*time.Second || c.Listen != "127.0.0.1:8080" {
		t.Errorf("settings: %+v", c)
	}
	dir := filepath.Dir(name)
	if c.Database != filepath.Join(dir, "glade.db") || c.Incidents != filepath.Join(dir, "incidents") {
		t.Errorf("paths not beside the config: %s, %s", c.Database, c.Incidents)
	}
	if c.Checks[0].Slow != 2*time.Second || c.Checks[0].Timeout != 10*time.Second || c.Checks[1].Timeout != 3*time.Second {
		t.Errorf("checks: %+v", c.Checks)
	}
}

func TestAbsolutePaths(t *testing.T) {
	c, err := Load(write(t, "database = \"/var/lib/glade/glade.db\"\nincidents = \"/srv/incidents\"\n[[check]]\nname = \"a\"\nurl = \"https://a/\""))
	if err != nil {
		t.Fatal(err)
	}
	if c.Database != "/var/lib/glade/glade.db" || c.Incidents != "/srv/incidents" {
		t.Errorf("absolute paths changed: %s, %s", c.Database, c.Incidents)
	}
}

func TestURL(t *testing.T) {
	c, err := Load(write(t, "url = \"https://status.example.com\"\n[[check]]\nname = \"a\"\nurl = \"https://a/\""))
	if err != nil {
		t.Fatal(err)
	}
	if c.URL != "https://status.example.com/" {
		t.Errorf("url = %q, want it with a slash", c.URL)
	}
}

func TestLoadErrors(t *testing.T) {
	for want, body := range map[string]string{
		"no checks":                  `title = "x"`,
		`url "status.example.com"`:   "url = \"status.example.com\"\n[[check]]\nname = \"a\"\nurl = \"https://a/\"",
		"unknown setting":            "titel = \"x\"\n[[check]]\nname = \"a\"\nurl = \"https://a/\"",
		"at least 10s":               "interval = \"1s\"\n[[check]]\nname = \"a\"\nurl = \"https://a/\"",
		"has no name":                "[[check]]\nurl = \"https://a/\"",
		"two checks named":           "[[check]]\nname = \"a\"\nurl = \"https://a/\"\n[[check]]\nname = \"a\"\ntcp = \"a:1\"",
		"not both":                   "[[check]]\nname = \"a\"\nurl = \"https://a/\"\ntcp = \"a:1\"",
		"is not http:// or https://": "[[check]]\nname = \"a\"\nurl = \"ftp://a/\"",
		"is not host:port":           "[[check]]\nname = \"a\"\ntcp = \"a\"",
		"contains is for url":        "[[check]]\nname = \"a\"\ntcp = \"a:1\"\ncontains = \"x\"",
		"has no url or tcp":          "[[check]]\nname = \"a\"",
	} {
		if _, err := Load(write(t, body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want an error with %q", body, err, want)
		}
	}
}

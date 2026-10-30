package incident

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadAll(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"old.md": "---\ntitle: Old\ndate: 2026-10-01 09:00\nresolved: 2026-10-01 09:30\n---\nA **restart** fixed it.\n",
		"new.md": "---\r\ntitle: New\r\ndate: 2026-10-15\r\n---\r\nWe know.\r\n",
		"x.txt":  "not an incident",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	all, err := ReadAll(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Slug != "new" || all[1].Slug != "old" {
		t.Fatalf("got %+v, want new then old", all)
	}
	if !all[0].Resolved.IsZero() || !all[1].Resolved.Equal(time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("resolved: %v, %v", all[0].Resolved, all[1].Resolved)
	}
	if !strings.Contains(string(all[1].Body), "<strong>restart</strong>") {
		t.Errorf("body: %s", all[1].Body)
	}
	if none, err := ReadAll(filepath.Join(dir, "missing")); err != nil || len(none) != 0 {
		t.Errorf("a missing folder: %v, %v", none, err)
	}
}

func TestErrors(t *testing.T) {
	for want, body := range map[string]string{
		"no front matter":               "just text",
		"needs a title and a date":      "---\ntitle: x\n---\n",
		"not 2006-01-02":                "---\ntitle: x\ndate: yesterday\n---\n",
		"only title, date and resolved": "---\ntitle: x\ndate: 2026-10-01\nseverity: high\n---\n",
	} {
		name := filepath.Join(t.TempDir(), "i.md")
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Read(name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want an error with %q", body, err, want)
		}
	}
}

package control

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogAndLookup(t *testing.T) {
	cat := Catalog()
	if len(cat) == 0 {
		t.Fatal("catalog must not be empty")
	}
	// Creating/renaming/dropping sites must never be allowlisted.
	for _, a := range cat {
		k := a.Key
		if strings.Contains(k, "new-site") || strings.Contains(k, "drop") || strings.Contains(k, "rename") {
			t.Fatalf("disallowed lifecycle action present in catalog: %s", k)
		}
	}
	if _, ok := Lookup("bench.migrate"); !ok {
		t.Fatal("bench.migrate must be allowlisted")
	}
	if _, ok := Lookup("rm -rf /"); ok {
		t.Fatal("arbitrary command must never resolve to an action")
	}
}

func TestCatalogJSONExposesOnlyMetadata(t *testing.T) {
	b, err := json.Marshal(Catalog())
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"key": true, "label": true, "description": true, "scope": true, "dangerous": true}
	for _, row := range rows {
		for k := range row {
			if !allowed[k] {
				t.Fatalf("catalog JSON exposes unexpected field %q (build internals must stay server-side)", k)
			}
		}
	}
}

func TestResolveBuildsExpectedCommands(t *testing.T) {
	a, _ := Lookup("bench.migrate")
	cmd, err := a.Resolve(Params{BenchPath: "/home/frappe/frappe-bench", Site: "site1.local"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "cd '/home/frappe/frappe-bench' && bench --site 'site1.local' migrate"; cmd != want {
		t.Fatalf("got %q, want %q", cmd, want)
	}

	sup, _ := Lookup("supervisor.restart")
	c2, err := sup.Resolve(Params{})
	if err != nil {
		t.Fatal(err)
	}
	if c2 != "sudo supervisorctl restart all" {
		t.Fatalf("got %q", c2)
	}
}

func TestResolveRequiresScopeParams(t *testing.T) {
	mig, _ := Lookup("bench.migrate")
	if _, err := mig.Resolve(Params{Site: "s.local"}); err == nil {
		t.Fatal("site-scoped action with no bench path must error")
	}
	if _, err := mig.Resolve(Params{BenchPath: "/home/frappe/frappe-bench"}); err == nil {
		t.Fatal("site-scoped action with no site must error")
	}
	restart, _ := Lookup("bench.restart")
	if _, err := restart.Resolve(Params{}); err == nil {
		t.Fatal("bench-scoped action with no bench path must error")
	}
}

func TestResolveRejectsInjection(t *testing.T) {
	mig, _ := Lookup("bench.migrate")
	badSites := []string{"site;rm -rf /", "site && reboot", "site`whoami`", "$(reboot)", "a b", "a'b", "../../etc"}
	for _, s := range badSites {
		if _, err := mig.Resolve(Params{BenchPath: "/home/frappe/frappe-bench", Site: s}); err == nil {
			t.Fatalf("injection site accepted: %q", s)
		}
	}
	badBench := []string{"/home; rm -rf /", "relative/path", "/home/$(id)", "/home/a b", "/home/`x`"}
	for _, p := range badBench {
		if _, err := mig.Resolve(Params{BenchPath: p, Site: "ok.local"}); err == nil {
			t.Fatalf("injection bench accepted: %q", p)
		}
	}
}

func TestShellQuoteEscapesSingleQuote(t *testing.T) {
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("got %q", got)
	}
}

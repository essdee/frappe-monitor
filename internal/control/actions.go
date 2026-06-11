// Package control implements the control panel: a fixed allowlist of bench and
// service commands an operator may run against monitored servers over SSH, plus
// site-config editing. Every run is recorded in an immutable audit log.
//
// Security model: only the keys in the catalog below can ever be executed —
// there is no free-form command path. The only operator-supplied values that
// reach a shell are the bench path and site name; both are pattern-validated
// AND single-quote-escaped before interpolation, so command injection is not
// possible even if validation were bypassed.
package control

import (
	"fmt"
	"regexp"
	"strings"
)

// Scope says what an action needs to run.
type Scope string

const (
	ScopeServer Scope = "server" // server-wide; no bench/site
	ScopeBench  Scope = "bench"  // needs a bench path
	ScopeSite   Scope = "site"   // needs a bench path + site
)

// Action is one allowlisted command. The build function is unexported so it is
// never serialised to clients — the catalog API exposes only the metadata.
type Action struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Scope       Scope  `json:"scope"`
	Dangerous   bool   `json:"dangerous"` // heavy/disruptive — UI warns harder, longer timeout
	build       func(p Params) string
}

// Params are the validated inputs used to build a command.
type Params struct {
	BenchPath string
	Site      string
}

// Validation patterns for the only shell-bound, operator-supplied values.
var (
	siteRe      = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	benchPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)
)

// catalog is the complete, fixed set of runnable actions. Creating, renaming or
// dropping sites is deliberately absent.
var catalog = []Action{
	{
		Key: "bench.migrate", Label: "Migrate site", Scope: ScopeSite,
		Description: "Apply pending database migrations for a site (bench --site <site> migrate).",
		build: func(p Params) string {
			return fmt.Sprintf("cd %s && bench --site %s migrate", shellQuote(p.BenchPath), shellQuote(p.Site))
		},
	},
	{
		Key: "bench.clear-cache", Label: "Clear cache", Scope: ScopeSite,
		Description: "Clear a site's Redis/document cache (bench --site <site> clear-cache).",
		build: func(p Params) string {
			return fmt.Sprintf("cd %s && bench --site %s clear-cache", shellQuote(p.BenchPath), shellQuote(p.Site))
		},
	},
	{
		Key: "bench.build", Label: "Build assets", Scope: ScopeBench,
		Description: "Rebuild front-end assets for the bench (bench build).",
		build: func(p Params) string {
			return fmt.Sprintf("cd %s && bench build", shellQuote(p.BenchPath))
		},
	},
	{
		Key: "bench.update", Label: "Update bench", Scope: ScopeBench, Dangerous: true,
		Description: "Pull latest apps, build assets and migrate every site (bench update). Long-running and disruptive.",
		build: func(p Params) string {
			return fmt.Sprintf("cd %s && bench update", shellQuote(p.BenchPath))
		},
	},
	{
		Key: "bench.restart", Label: "Restart bench", Scope: ScopeBench,
		Description: "Restart this bench's web/worker processes (bench restart).",
		build: func(p Params) string {
			return fmt.Sprintf("cd %s && bench restart", shellQuote(p.BenchPath))
		},
	},
	{
		Key: "supervisor.restart", Label: "Restart supervisor", Scope: ScopeServer, Dangerous: true,
		Description: "Restart all supervisor-managed processes on the host (sudo supervisorctl restart all).",
		build:       func(_ Params) string { return "sudo supervisorctl restart all" },
	},
	{
		Key: "supervisor.status", Label: "Supervisor status", Scope: ScopeServer,
		Description: "Show the status of supervisor-managed processes (sudo supervisorctl status).",
		build:       func(_ Params) string { return "sudo supervisorctl status" },
	},
}

var byKey = func() map[string]Action {
	m := make(map[string]Action, len(catalog))
	for _, a := range catalog {
		m[a.Key] = a
	}
	return m
}()

// Catalog returns the public list of runnable actions (no build functions).
func Catalog() []Action { return append([]Action(nil), catalog...) }

// Lookup returns the action for a key, or ok=false if it is not allowlisted.
func Lookup(key string) (Action, bool) {
	a, ok := byKey[key]
	return a, ok
}

// Resolve validates params for the action's scope and returns the concrete
// shell command to execute. It is the single choke point where operator input
// becomes a command.
func (a Action) Resolve(p Params) (string, error) {
	if a.Scope == ScopeBench || a.Scope == ScopeSite {
		if p.BenchPath == "" {
			return "", fmt.Errorf("bench path is required for %s", a.Key)
		}
		if !benchPathRe.MatchString(p.BenchPath) {
			return "", fmt.Errorf("invalid bench path")
		}
	}
	if a.Scope == ScopeSite {
		if p.Site == "" {
			return "", fmt.Errorf("site is required for %s", a.Key)
		}
		if !siteRe.MatchString(p.Site) {
			return "", fmt.Errorf("invalid site name")
		}
	}
	return a.build(p), nil
}

// ValidateBenchPath / ValidateSite are exposed for the site-config endpoints,
// which build their own commands but must enforce the same input rules.
func ValidateBenchPath(p string) error {
	if p == "" || !benchPathRe.MatchString(p) {
		return fmt.Errorf("invalid bench path")
	}
	return nil
}

func ValidateSite(s string) error {
	if s == "" || !siteRe.MatchString(s) {
		return fmt.Errorf("invalid site name")
	}
	return nil
}

// shellQuote wraps s in single quotes, escaping embedded single quotes, so it
// is safe to interpolate into a /bin/sh command line.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"frappe-monitor/internal/installer"
)

// installCmd implements `frappe-monitor install`: cross-platform, asks for the
// install root + DB + process manager (or takes them via flags), and lays down
// the binary, config, and a service file.
func installCmd(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	root := fs.String("root", "", "install root directory (prompted if empty)")
	listen := fs.String("listen", ":8080", "HTTP listen address")
	db := fs.String("db", "mariadb", "database driver: mariadb|postgres|sqlite")
	dbHost := fs.String("db-host", "127.0.0.1", "database host")
	dbPort := fs.Int("db-port", 0, "database port (0 = engine default)")
	dbUser := fs.String("db-user", "monitor", "database user")
	dbPass := fs.String("db-password", "", "database password")
	dbName := fs.String("db-name", "frappe_monitor", "database name")
	dbSSL := fs.String("db-sslmode", "disable", "postgres sslmode")
	dbPath := fs.String("db-path", "", "sqlite file path (sqlite driver only)")
	password := fs.String("password", "", "dashboard password (auto-generated if empty)")
	service := fs.String("service", "", "process manager: systemd|launchd|supervisor|pm2|none (default: OS-native)")
	forceConfig := fs.Bool("force-config", false, "overwrite an existing monitor.yaml (apply the DB/password flags)")
	yes := fs.Bool("yes", false, "non-interactive: use flags + defaults, no prompts")
	if err := fs.Parse(args); err != nil {
		return err
	}

	o := installer.Options{
		Root: *root, ListenAddr: *listen,
		DBDriver: *db, DBHost: *dbHost, DBPort: *dbPort, DBUser: *dbUser,
		DBPassword: *dbPass, DBName: *dbName, DBSSLMode: *dbSSL, DBPath: *dbPath,
		AuthPassword: *password, Service: *service, Overwrite: *forceConfig,
	}

	// Only prompt for values the operator did NOT pass on the command line, so
	// a fully-flagged invocation runs non-interactively.
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	in := bufio.NewScanner(os.Stdin)
	interactive := !*yes && isTTY()
	prompt := func(name string) bool { return interactive && !provided[name] }

	if !provided["root"] {
		o.Root = ask(in, interactive, "Install root directory",
			orDefault(o.Root, installer.DefaultRoot(runtime.GOOS)))
	}
	o.Root = expandHome(o.Root)

	if !provided["service"] {
		o.Service = installer.DefaultService(runtime.GOOS)
		if prompt("service") {
			o.Service = ask(in, true, "Process manager (systemd|launchd|supervisor|pm2|none)", o.Service)
		}
	}

	if prompt("db") {
		o.DBDriver = ask(in, true, "Database driver (mariadb|postgres|sqlite)", orDefault(o.DBDriver, "mariadb"))
	}
	if normDriver(o.DBDriver) != "sqlite" {
		if prompt("db-host") {
			o.DBHost = ask(in, true, "DB host", orDefault(o.DBHost, "127.0.0.1"))
		}
		if prompt("db-port") {
			o.DBPort = askInt(in, true, "DB port (0 = engine default)", o.DBPort)
		}
		if prompt("db-user") {
			o.DBUser = ask(in, true, "DB user", orDefault(o.DBUser, "monitor"))
		}
		if prompt("db-password") {
			o.DBPassword = ask(in, true, "DB password", o.DBPassword)
		}
		if prompt("db-name") {
			o.DBName = ask(in, true, "DB name", orDefault(o.DBName, "frappe_monitor"))
		}
		if normDriver(o.DBDriver) == "postgres" && prompt("db-sslmode") {
			o.DBSSLMode = ask(in, true, "Postgres sslmode", orDefault(o.DBSSLMode, "disable"))
		}
	}

	if o.AuthPassword == "" && prompt("password") {
		o.AuthPassword = ask(in, true, "Dashboard password (blank = auto-generate)", "")
	}
	if o.AuthPassword == "" {
		pw, err := randomPassword()
		if err != nil {
			return fmt.Errorf("could not generate a dashboard password (%w) — pass --password explicitly", err)
		}
		o.AuthPassword = pw
		fmt.Printf("\n  Generated dashboard password: %s\n  ^^ save this — you need it to log in ^^\n\n", o.AuthPassword)
	}

	fmt.Printf("Installing frappe-monitor (%s) to %s ...\n", runtime.GOOS, o.Root)
	if err := installer.Install(o, os.Stdout); err != nil {
		return err
	}
	fmt.Printf("\nDashboard: http://%s\n", humanAddr(o.ListenAddr))
	return nil
}

// uninstallCmd removes the install. It prints manager-specific stop hints and,
// with --purge, deletes the root directory (data + config included).
func uninstallCmd(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	root := fs.String("root", installer.DefaultRoot(runtime.GOOS), "install root directory")
	purge := fs.Bool("purge", false, "also delete the root directory (config + data)")
	yes := fs.Bool("yes", false, "do not prompt for confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := expandHome(*root)

	fmt.Printf("Stop the service first (whichever manager you used), e.g.:\n")
	fmt.Printf("  systemd:    sudo systemctl disable --now frappe-monitor\n")
	fmt.Printf("  launchd:    launchctl unload ~/Library/LaunchAgents/com.frappe-monitor.plist\n")
	fmt.Printf("  supervisor: sudo supervisorctl stop frappe-monitor\n")
	fmt.Printf("  pm2:        pm2 delete frappe-monitor && pm2 save\n\n")

	if !*purge {
		fmt.Printf("Config + data kept at %s (re-run with --purge to delete).\n", dir)
		return nil
	}
	if err := safeToPurge(dir); err != nil {
		return err
	}
	in := bufio.NewScanner(os.Stdin)
	if !*yes && isTTY() {
		if a := ask(in, true, fmt.Sprintf("Delete everything under %s? (y/N)", dir), "N"); !strings.EqualFold(a, "y") {
			fmt.Println("aborted.")
			return nil
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	fmt.Printf("removed %s\n", dir)
	return nil
}

// safeToPurge refuses obviously-dangerous --purge targets: empty paths, a
// filesystem/volume root, the user's home directory, or any path that doesn't
// actually look like a frappe-monitor install (no monitor.yaml and no bin/).
func safeToPurge(dir string) error {
	clean := filepath.Clean(dir)
	if clean == "" || clean == "." {
		return fmt.Errorf("refusing to purge an empty path")
	}
	sep := string(filepath.Separator)
	if clean == sep || clean == filepath.VolumeName(clean)+sep {
		return fmt.Errorf("refusing to purge a filesystem root: %q", clean)
	}
	if home, err := os.UserHomeDir(); err == nil && clean == filepath.Clean(home) {
		return fmt.Errorf("refusing to purge your home directory: %q", clean)
	}
	_, errCfg := os.Stat(filepath.Join(clean, "monitor.yaml"))
	_, errBin := os.Stat(filepath.Join(clean, "bin"))
	if errCfg != nil && errBin != nil {
		return fmt.Errorf("%s does not look like a frappe-monitor install (no monitor.yaml or bin/); refusing --purge", clean)
	}
	return nil
}

// --- small prompt/util helpers ---------------------------------------------

func isTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

func ask(in *bufio.Scanner, interactive bool, label, def string) string {
	if !interactive {
		return def
	}
	if def != "" {
		fmt.Printf("  %s [%s]: ", label, def)
	} else {
		fmt.Printf("  %s: ", label)
	}
	if !in.Scan() {
		return def
	}
	v := strings.TrimSpace(in.Text())
	if v == "" {
		return def
	}
	return v
}

func askInt(in *bufio.Scanner, interactive bool, label string, def int) int {
	s := ask(in, interactive, label, strconv.Itoa(def))
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func normDriver(d string) string {
	switch strings.ToLower(strings.TrimSpace(d)) {
	case "", "sqlite", "sqlite3":
		return "sqlite"
	case "postgres", "postgresql", "pg":
		return "postgres"
	default:
		return "mariadb"
	}
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func humanAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}

func randomPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		// Never silently fall back to a predictable credential.
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

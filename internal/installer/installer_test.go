package installer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultsPerOS(t *testing.T) {
	require.Equal(t, "/opt/frappe-monitor", DefaultRoot("linux"))
	require.Contains(t, DefaultRoot("darwin"), ".frappe-monitor")
	require.Contains(t, DefaultRoot("windows"), "frappe-monitor")

	require.Equal(t, ServiceSystemd, DefaultService("linux"))
	require.Equal(t, ServiceLaunchd, DefaultService("darwin"))
	require.Equal(t, ServicePM2, DefaultService("windows"))

	require.Equal(t, "frappe-monitor.exe", BinaryName("windows"))
	require.Equal(t, "frappe-monitor", BinaryName("linux"))
	require.Equal(t, "frappe-monitor", BinaryName("darwin"))
}

func TestRenderConfig(t *testing.T) {
	ma := RenderConfig(Options{
		Root: "/opt/fm", ListenAddr: ":8080", GOOS: "linux",
		DBDriver: "mariadb", DBHost: "db", DBPort: 3306, DBUser: "monitor",
		DBPassword: `p"a\ss`, DBName: "frappe_monitor", AuthPassword: "secret",
	})
	require.Contains(t, ma, `driver: "mariadb"`)
	require.Contains(t, ma, `host: "db"`)
	require.Contains(t, ma, `port: 3306`)
	require.Contains(t, ma, `name: "frappe_monitor"`)
	require.Contains(t, ma, `password: "p\"a\\ss"`, "special chars must be YAML-escaped")
	require.Contains(t, ma, `password: "secret"`)
	require.NotContains(t, ma, "sslmode", "mariadb has no sslmode line")

	pg := RenderConfig(Options{
		Root: "/opt/fm", GOOS: "linux", DBDriver: "postgres", DBHost: "pg",
		DBUser: "u", DBName: "n", AuthPassword: "x",
	})
	require.Contains(t, pg, `driver: "postgres"`)
	require.Contains(t, pg, `sslmode: "disable"`)

	sq := RenderConfig(Options{Root: "/opt/fm", GOOS: "linux", DBDriver: "sqlite", AuthPassword: "x"})
	require.Contains(t, sq, `driver: "sqlite"`)
	require.Contains(t, sq, "monitor.db")
	require.NotContains(t, sq, `host: "`, "sqlite config has no DB host line")
}

func TestServiceFile(t *testing.T) {
	base := Options{Root: "/opt/fm", GOOS: "linux"}

	cases := []struct {
		svc, file, sig string
	}{
		{ServiceSystemd, "frappe-monitor.service", "[Service]"},
		{ServiceSupervisor, "frappe-monitor.supervisor.conf", "[program:frappe-monitor]"},
		{ServicePM2, "ecosystem.config.js", "module.exports"},
	}
	for _, c := range cases {
		o := base
		o.Service = c.svc
		name, content, hint, err := ServiceFile(o)
		require.NoError(t, err, c.svc)
		require.Equal(t, c.file, name)
		require.Contains(t, content, c.sig)
		require.Contains(t, content, "frappe-monitor") // points at the binary
		require.NotEmpty(t, hint)
	}

	// launchd uses the darwin paths
	o := Options{Root: "/Users/x/.frappe-monitor", GOOS: "darwin", Service: ServiceLaunchd}
	name, content, _, err := ServiceFile(o)
	require.NoError(t, err)
	require.Equal(t, "com.frappe-monitor.plist", name)
	require.Contains(t, content, "com.frappe-monitor")
	require.Contains(t, content, "KeepAlive")

	// none / unknown error
	_, _, _, err = ServiceFile(Options{Service: ServiceNone})
	require.Error(t, err)
	_, _, _, err = ServiceFile(Options{Service: "nssm"})
	require.Error(t, err)
}

func TestServiceFile_PM2EscapesWindowsPaths(t *testing.T) {
	o := Options{Root: `C:\ProgramData\frappe-monitor`, GOOS: "windows", Service: ServicePM2}
	_, content, _, err := ServiceFile(o)
	require.NoError(t, err)
	// Backslashes from the Windows root must be JS-escaped so the file is valid.
	require.Contains(t, content, `C:\\ProgramData\\frappe-monitor`)
	require.Contains(t, content, "frappe-monitor.exe")
}

func TestInstall(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "fm")
	srcBin := filepath.Join(tmp, "src-binary")
	require.NoError(t, os.WriteFile(srcBin, []byte("binary-bytes"), 0o755))

	o := Options{
		Root: root, ListenAddr: ":8080", GOOS: "linux",
		DBDriver: "mariadb", DBHost: "127.0.0.1", DBPort: 3306, DBUser: "monitor",
		DBPassword: "p", DBName: "frappe_monitor",
		AuthPassword: "secret", Service: ServiceSystemd, SourceBinary: srcBin,
	}
	var buf bytes.Buffer
	require.NoError(t, Install(o, &buf))

	require.FileExists(t, filepath.Join(root, "bin", "frappe-monitor"))
	require.DirExists(t, filepath.Join(root, "data"))
	require.DirExists(t, filepath.Join(root, "logs"))
	require.FileExists(t, filepath.Join(root, "frappe-monitor.service"))

	cfg, err := os.ReadFile(filepath.Join(root, "monitor.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(cfg), `driver: "mariadb"`)
	require.Contains(t, string(cfg), `password: "secret"`)

	// The copied binary matches the source.
	got, _ := os.ReadFile(filepath.Join(root, "bin", "frappe-monitor"))
	require.Equal(t, "binary-bytes", string(got))

	// Re-running must NOT clobber an edited config (idempotent upgrade).
	require.NoError(t, os.WriteFile(filepath.Join(root, "monitor.yaml"), []byte("custom: true\n"), 0o600))
	buf.Reset()
	require.NoError(t, Install(o, &buf))
	cfg2, _ := os.ReadFile(filepath.Join(root, "monitor.yaml"))
	require.Equal(t, "custom: true\n", string(cfg2), "existing config preserved on re-install")

	// Guards.
	require.Error(t, Install(Options{Root: "", AuthPassword: "x"}, &buf))
	require.Error(t, Install(Options{Root: root, AuthPassword: ""}, &buf), "password required")
}

func TestJSStrEscaping(t *testing.T) {
	require.Equal(t, `"a\\b"`, jsStr(`a\b`))
	require.Equal(t, `"a\"b"`, jsStr(`a"b`))
}

// Sanity: a rendered mariadb config must satisfy the same shape the loader
// expects (driver + the required engine fields are present).
func TestRenderConfig_HasRequiredFields(t *testing.T) {
	c := RenderConfig(Options{
		Root: "/opt/fm", GOOS: "linux", DBDriver: "mariadb",
		DBHost: "h", DBUser: "u", DBName: "n", AuthPassword: "x",
	})
	for _, want := range []string{"driver:", "host:", "user:", "name:", "listen_addr:", "auth:"} {
		require.True(t, strings.Contains(c, want), "config missing %q", want)
	}
}

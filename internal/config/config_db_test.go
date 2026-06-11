package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDatabaseDSN(t *testing.T) {
	// sqlite (default-ish)
	drv, dsn, err := DatabaseConfig{Driver: "sqlite", Path: "./data/monitor.db"}.DSN()
	require.NoError(t, err)
	require.Equal(t, "sqlite", drv)
	require.Contains(t, dsn, "file:./data/monitor.db")
	require.Contains(t, dsn, "foreign_keys")

	// empty driver normalizes to sqlite
	drv, _, err = DatabaseConfig{Path: "x.db"}.DSN()
	require.NoError(t, err)
	require.Equal(t, "sqlite", drv)

	// mariadb is an alias of mysql; explicit port honored
	drv, dsn, err = DatabaseConfig{
		Driver: "mariadb", Host: "db", Port: 3307, User: "mon", Password: "p4ss", Name: "frappe_monitor",
	}.DSN()
	require.NoError(t, err)
	require.Equal(t, "mysql", drv)
	require.Equal(t, "mon:p4ss@tcp(db:3307)/frappe_monitor?parseTime=true&loc=UTC&charset=utf8mb4", dsn)

	// mysql default port
	_, dsn, err = DatabaseConfig{Driver: "mysql", Host: "h", User: "u", Name: "n"}.DSN()
	require.NoError(t, err)
	require.Contains(t, dsn, "tcp(h:3306)")

	// postgres URL form + sslmode
	drv, dsn, err = DatabaseConfig{
		Driver: "postgres", Host: "pg", Port: 5433, User: "u", Password: "secret", Name: "mon", SSLMode: "require",
	}.DSN()
	require.NoError(t, err)
	require.Equal(t, "postgres", drv)
	require.Contains(t, dsn, "postgres://u:secret@pg:5433/mon")
	require.Contains(t, dsn, "sslmode=require")

	// postgres default port + default sslmode=disable
	_, dsn, err = DatabaseConfig{Driver: "postgres", Host: "pg", User: "u", Name: "mon"}.DSN()
	require.NoError(t, err)
	require.Contains(t, dsn, "pg:5432")
	require.Contains(t, dsn, "sslmode=disable")

	// unknown driver errors
	_, _, err = DatabaseConfig{Driver: "oracle"}.DSN()
	require.Error(t, err)
}

func TestValidateDatabase(t *testing.T) {
	cfg := func(d DatabaseConfig) *Config { return &Config{Database: d} }

	// sqlite needs a path
	require.Error(t, cfg(DatabaseConfig{Driver: "sqlite", Path: ""}).validateDatabase())
	require.NoError(t, cfg(DatabaseConfig{Driver: "sqlite", Path: "x.db"}).validateDatabase())
	require.NoError(t, cfg(DatabaseConfig{Path: "x.db"}).validateDatabase(), "empty driver = sqlite")

	// mariadb/postgres need host + user + name
	require.Error(t, cfg(DatabaseConfig{Driver: "mariadb", Host: "", User: "u", Name: "n"}).validateDatabase())
	require.Error(t, cfg(DatabaseConfig{Driver: "mariadb", Host: "h", User: "", Name: "n"}).validateDatabase())
	require.Error(t, cfg(DatabaseConfig{Driver: "postgres", Host: "h", User: "u", Name: ""}).validateDatabase())
	require.NoError(t, cfg(DatabaseConfig{Driver: "mariadb", Host: "h", User: "u", Name: "n"}).validateDatabase())
	require.NoError(t, cfg(DatabaseConfig{Driver: "postgres", Host: "h", User: "u", Name: "n"}).validateDatabase())

	// unknown driver
	require.Error(t, cfg(DatabaseConfig{Driver: "oracle"}).validateDatabase())

	// a ':' in the mysql user would corrupt the DSN split — reject it
	require.Error(t, cfg(DatabaseConfig{Driver: "mariadb", Host: "h", User: "a:b", Name: "n"}).validateDatabase())
	// postgres is safe (url-escaped), so a ':' there is allowed
	require.NoError(t, cfg(DatabaseConfig{Driver: "postgres", Host: "h", User: "a:b", Name: "n"}).validateDatabase())
}

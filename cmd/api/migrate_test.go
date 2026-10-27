package main

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/config"
)

func TestMigrateDSN(t *testing.T) {
	t.Parallel()

	pg := func(raw string) config.Postgres {
		return config.Postgres{URL: raw, ConnectTimeout: 1500 * time.Millisecond, StatementTimeout: 5 * time.Second}
	}

	t.Run("switches to the pgx5 driver and keeps credentials, host and parameters", func(t *testing.T) {
		t.Parallel()

		dsn, err := migrateDSN(pg("postgres://flashdeal:secret@db:5432/flashdeal?sslmode=disable"))

		require.NoError(t, err)
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		require.Equal(t, "pgx5", u.Scheme)
		require.Equal(t, "flashdeal", u.User.Username())
		pass, _ := u.User.Password()
		require.Equal(t, "secret", pass)
		require.Equal(t, "db:5432", u.Host)
		require.Equal(t, "/flashdeal", u.Path)
		require.Equal(t, "disable", u.Query().Get("sslmode"))
	})

	t.Run("carries the timeouts, rounding the connect timeout up to whole seconds", func(t *testing.T) {
		t.Parallel()

		dsn, err := migrateDSN(pg("postgresql://db/flashdeal"))

		require.NoError(t, err)
		u, err := url.Parse(dsn)
		require.NoError(t, err)
		require.Equal(t, "2", u.Query().Get("connect_timeout"))
		require.Equal(t, "5000", u.Query().Get("x-statement-timeout"))
	})

	t.Run("rejects a URL that is not postgres", func(t *testing.T) {
		t.Parallel()

		_, err := migrateDSN(pg("mysql://db/flashdeal"))

		require.ErrorContains(t, err, "want postgres or postgresql")
	})

	t.Run("does not echo an unparsable URL, which may hold credentials", func(t *testing.T) {
		t.Parallel()

		_, err := migrateDSN(pg("postgres://user:p%zz@db/flashdeal"))

		require.Error(t, err)
		require.NotContains(t, err.Error(), "p%zz")
	})
}

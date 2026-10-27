//go:build integration

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestMigrateUp(t *testing.T) {
	ctr, err := tcpostgres.Run(t.Context(), "postgres:17-alpine",
		tcpostgres.WithDatabase("flashdeal"),
		tcpostgres.WithUsername("flashdeal"),
		tcpostgres.WithPassword("flashdeal"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })

	dsn, err := ctr.ConnectionString(t.Context(), "sslmode=disable")
	require.NoError(t, err)
	environ := []string{"POSTGRES_URL=" + dsn}

	t.Run("applies every migration to an empty database", func(t *testing.T) {
		var logs bytes.Buffer

		require.NoError(t, migrateUp(t.Context(), environ, &logs))

		require.Contains(t, logs.String(), `"msg":"migrations applied"`)
		require.Contains(t, logs.String(), `"changed":true`)
		conn, err := pgx.Connect(t.Context(), dsn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		for _, table := range []string{"deals", "reservations", "orders"} {
			var exists bool
			require.NoError(t, conn.QueryRow(t.Context(),
				"SELECT to_regclass($1) IS NOT NULL", "public."+table).Scan(&exists))
			require.True(t, exists, "table %s", table)
		}
	})

	t.Run("is a no-op when the schema is already current", func(t *testing.T) {
		var logs bytes.Buffer

		require.NoError(t, migrateUp(t.Context(), environ, &logs))

		require.Contains(t, logs.String(), `"changed":false`)
		require.Contains(t, logs.String(), `"dirty":false`)
	})
}

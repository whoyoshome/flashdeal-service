package uuidgen_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/uuidgen"
)

func TestV7(t *testing.T) {
	t.Parallel()

	t.Run("returns distinct, time-ordered version 7 UUIDs", func(t *testing.T) {
		t.Parallel()
		gen := uuidgen.V7{}

		first, err := gen.New()
		require.NoError(t, err)
		second, err := gen.New()
		require.NoError(t, err)

		parsed, err := uuid.Parse(first)
		require.NoError(t, err)
		require.Equal(t, uuid.Version(7), parsed.Version())
		require.NotEqual(t, first, second)
		require.Less(t, first, second, "v7 strings sort by creation time")
	})
}

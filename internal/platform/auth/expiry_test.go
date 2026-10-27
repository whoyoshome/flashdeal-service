package auth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseRejectsExpiredToken(t *testing.T) {
	t.Parallel()

	s, err := New(Config{Secret: "test-secret-at-least-32-bytes-long!!", Issuer: "flashdeal-test"})
	require.NoError(t, err)

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return start }
	tok, err := s.Issue("late", RoleUser, time.Minute)
	require.NoError(t, err)

	s.now = func() time.Time { return start.Add(2 * time.Minute) }
	_, err = s.Parse(tok)
	require.ErrorIs(t, err, ErrInvalidToken)
}

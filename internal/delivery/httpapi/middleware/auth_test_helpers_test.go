package middleware_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/auth"
)

const (
	testJWTSecret = "test-secret-at-least-32-bytes-long!!"
	testJWTIssuer = "flashdeal-test"
)

func testAuth(t *testing.T) *auth.Service {
	t.Helper()
	s, err := auth.New(auth.Config{Secret: testJWTSecret, Issuer: testJWTIssuer})
	require.NoError(t, err)
	return s
}

func bearer(t *testing.T, tokens *auth.Service, subject string, role auth.Role) string {
	t.Helper()
	tok, err := tokens.Issue(subject, role, time.Hour)
	require.NoError(t, err)
	return "Bearer " + tok
}

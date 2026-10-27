package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/whoyoshome/flashdeal-service/internal/platform/auth"
)

const (
	testSecret = "test-secret-at-least-32-bytes-long!!"
	testIssuer = "flashdeal-test"
)

func newService(t *testing.T) *auth.Service {
	t.Helper()
	s, err := auth.New(auth.Config{Secret: testSecret, Issuer: testIssuer})
	require.NoError(t, err)
	return s
}

func TestNew(t *testing.T) {
	t.Parallel()

	t.Run("rejects a short secret", func(t *testing.T) {
		t.Parallel()
		_, err := auth.New(auth.Config{Secret: "too-short", Issuer: testIssuer})
		require.Error(t, err)
	})

	t.Run("rejects an empty issuer", func(t *testing.T) {
		t.Parallel()
		_, err := auth.New(auth.Config{Secret: testSecret, Issuer: ""})
		require.Error(t, err)
	})
}

func TestIssueAndParse(t *testing.T) {
	t.Parallel()
	s := newService(t)

	tok, err := s.Issue("alice", auth.RoleAdmin, time.Hour)
	require.NoError(t, err)

	id, err := s.Parse(tok)
	require.NoError(t, err)
	require.Equal(t, auth.Identity{Subject: "alice", Role: auth.RoleAdmin}, id)
}

func TestParseAuthorization(t *testing.T) {
	t.Parallel()
	s := newService(t)
	tok, err := s.Issue("bob", auth.RoleUser, time.Hour)
	require.NoError(t, err)

	id, err := s.ParseAuthorization("Bearer " + tok)
	require.NoError(t, err)
	require.Equal(t, "bob", id.Subject)
	require.Equal(t, auth.RoleUser, id.Role)

	_, err = s.ParseAuthorization(tok)
	require.ErrorIs(t, err, auth.ErrInvalidToken)

	_, err = s.ParseAuthorization("Bearer ")
	require.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestParseRejectsBadTokens(t *testing.T) {
	t.Parallel()
	s := newService(t)

	other, err := auth.New(auth.Config{Secret: strings.Repeat("x", 32), Issuer: testIssuer})
	require.NoError(t, err)
	foreign, err := other.Issue("eve", auth.RoleUser, time.Hour)
	require.NoError(t, err)

	tests := []struct {
		name  string
		token string
	}{
		{name: "empty", token: ""},
		{name: "garbage", token: "not.a.jwt"},
		{name: "wrong secret", token: foreign},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := s.Parse(tc.token)
			require.ErrorIs(t, err, auth.ErrInvalidToken)
		})
	}
}

func TestIssueValidation(t *testing.T) {
	t.Parallel()
	s := newService(t)

	_, err := s.Issue("", auth.RoleUser, time.Hour)
	require.Error(t, err)

	_, err = s.Issue("alice", auth.Role("guest"), time.Hour)
	require.Error(t, err)

	_, err = s.Issue("alice", auth.RoleUser, 0)
	require.Error(t, err)
}

func TestValidSubject(t *testing.T) {
	t.Parallel()
	require.True(t, auth.ValidSubject("user-42"))
	require.False(t, auth.ValidSubject(""))
	require.False(t, auth.ValidSubject("user 42"))
	require.False(t, auth.ValidSubject(strings.Repeat("u", auth.MaxSubjectLen+1)))
}

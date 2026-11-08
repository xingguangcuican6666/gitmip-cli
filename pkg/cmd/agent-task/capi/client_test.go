package capi

import (
	"errors"
	"net/http"
	"testing"

	"github.com/cli/cli/v2/internal/gm"
	"github.com/stretchr/testify/require"
)

// recordingRoundTripper captures the Authorization header of every request it sees and returns a stub 200 response.
type recordingRoundTripper struct {
	authHeaders []string
}

func (r *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.authHeaders = append(r.authHeaders, req.Header.Get("Authorization"))
	return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
}

// sequencedAuthConfig returns a different credential on each call and records the hostname it was asked about, so a
// test can prove the transport resolves a token per request rather than once at construction time.
type sequencedAuthConfig struct {
	creds  []gm.Credential
	status gm.RefreshStatus
	err    error
	calls  []string
}

func (s *sequencedAuthConfig) ActiveTokenWithRefresh(hostname string) (gm.Credential, gm.RefreshStatus, error) {
	cred := s.creds[len(s.calls)]
	s.calls = append(s.calls, hostname)
	return cred, s.status, s.err
}

func TestCAPITransportResolvesFreshTokenPerRequest(t *testing.T) {
	base := &recordingRoundTripper{}
	auth := &sequencedAuthConfig{
		creds:  []gm.Credential{{Token: "token-1"}, {Token: "token-2"}},
		status: gm.RefreshStatusDone,
	}
	ct := newCAPITransport(auth, "github.com", "https://api.githubcopilot.com", base)

	for range 2 {
		req, err := http.NewRequest(http.MethodGet, "https://api.githubcopilot.com/foo", nil)
		require.NoError(t, err)
		_, err = ct.RoundTrip(req)
		require.NoError(t, err)
	}

	// The token is resolved once per request against the configured host, and each freshly resolved token is the one
	// presented on that request. A regression to a single construction-time lookup would reuse one token and fail here.
	require.Equal(t, []string{"github.com", "github.com"}, auth.calls)
	require.Equal(t, []string{"Bearer token-1", "Bearer token-2"}, base.authHeaders)
}

func TestCAPITransportUsesLastTokenWhenRefreshErrors(t *testing.T) {
	base := &recordingRoundTripper{}
	auth := &sequencedAuthConfig{
		creds:  []gm.Credential{{Token: "stale-token"}},
		status: gm.RefreshStatusFailed,
		err:    errors.New("network failure"),
	}
	ct := newCAPITransport(auth, "github.com", "https://api.githubcopilot.com", base)

	req, err := http.NewRequest(http.MethodGet, "https://api.githubcopilot.com/foo", nil)
	require.NoError(t, err)
	resp, err := ct.RoundTrip(req)

	// Refresh is best effort: a failed refresh still yields the last stored token, so the request proceeds with it.
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, []string{"Bearer stale-token"}, base.authHeaders)
}

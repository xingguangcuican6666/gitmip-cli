package gitea

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cli/cli/v2/internal/hosttype"
	"github.com/cli/cli/v2/pkg/httpmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// asGitea marks hostname as a Gitea host for the duration of the test, so the
// api client addresses /api/v1 rather than the GitHub Enterprise /api/v3
// prefix.
func asGitea(t *testing.T, hostname string) {
	t.Helper()
	restore := hosttype.SetHostTypeOverride(map[string]hosttype.Type{hostname: hosttype.Gitea})
	t.Cleanup(restore)
}

func TestPersonalAccessTokenURL(t *testing.T) {
	// Gitea has no /settings/tokens page; tokens live under the applications
	// page of the user settings.
	assert.Equal(t, "https://git.example.com/user/settings/applications", PersonalAccessTokenURL("git.example.com"))
}

func TestTokenName(t *testing.T) {
	name := TokenName()
	assert.True(t, strings.HasPrefix(name, "gitmip-cli-"), "name %q should start with the product prefix", name)

	// The suffix exists so that a second login does not collide with a token
	// already created by the first, which Gitea rejects by name.
	seen := map[string]bool{name: true}
	for range 20 {
		next := TokenName()
		assert.False(t, seen[next], "TokenName returned %q twice", next)
		seen[next] = true
	}
}

func TestCreateToken(t *testing.T) {
	const hostname = "git.example.com"
	asGitea(t, hostname)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("POST", "api/v1/users/monalisa/tokens"),
		httpmock.StatusJSONResponse(http.StatusCreated, map[string]any{
			"id":     1,
			"name":   "gitmip-cli-abcdef",
			"sha1":   "a1b2c3d4e5f6",
			"scopes": TokenScopes,
		}))

	token, err := CreateToken(&http.Client{Transport: reg}, hostname, "monalisa", "hunter2", "gitmip-cli-abcdef", "")
	require.NoError(t, err)
	assert.Equal(t, "a1b2c3d4e5f6", token)

	require.Len(t, reg.Requests, 1)
	req := reg.Requests[0]

	wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("monalisa:hunter2"))
	assert.Equal(t, wantBasic, req.Header.Get("Authorization"))
	// Gitea's binder rejects a POST without a JSON content type with a 422, so
	// the header is part of the contract with the server.
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Empty(t, req.Header.Get("X-Gitea-OTP"))

	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	var sent createTokenRequest
	require.NoError(t, json.Unmarshal(body, &sent))
	assert.Equal(t, "gitmip-cli-abcdef", sent.Name)
	assert.Equal(t, TokenScopes, sent.Scopes)
}

func TestCreateToken_sendsOTPHeaderOnlyWhenGiven(t *testing.T) {
	const hostname = "git.example.com"
	asGitea(t, hostname)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("POST", "api/v1/users/monalisa/tokens"),
		httpmock.StatusJSONResponse(http.StatusCreated, map[string]any{"sha1": "a1b2c3d4e5f6"}))

	_, err := CreateToken(&http.Client{Transport: reg}, hostname, "monalisa", "hunter2", "gitmip-cli-abcdef", "123456")
	require.NoError(t, err)

	require.Len(t, reg.Requests, 1)
	assert.Equal(t, "123456", reg.Requests[0].Header.Get("X-Gitea-OTP"))
}

func TestCreateToken_requiresCredentials(t *testing.T) {
	reg := &httpmock.Registry{}
	defer reg.Verify(t)

	_, err := CreateToken(&http.Client{Transport: reg}, "git.example.com", "", "hunter2", "name", "")
	assert.EqualError(t, err, "a username and password are required")

	_, err = CreateToken(&http.Client{Transport: reg}, "git.example.com", "monalisa", "", "name", "")
	assert.EqualError(t, err, "a username and password are required")

	// Neither call may reach the network.
	assert.Empty(t, reg.Requests)
}

func TestCreateToken_emptyTokenIsAnError(t *testing.T) {
	const hostname = "git.example.com"
	asGitea(t, hostname)

	reg := &httpmock.Registry{}
	defer reg.Verify(t)
	reg.Register(
		httpmock.REST("POST", "api/v1/users/monalisa/tokens"),
		httpmock.StatusJSONResponse(http.StatusCreated, map[string]any{"name": "gitmip-cli-abcdef"}))

	_, err := CreateToken(&http.Client{Transport: reg}, hostname, "monalisa", "hunter2", "gitmip-cli-abcdef", "")
	assert.EqualError(t, err, "git.example.com created a token but returned no value for it")
}

func TestCreateToken_errorMapping(t *testing.T) {
	const hostname = "git.example.com"

	tests := []struct {
		name          string
		status        int
		message       string
		wantRejected  bool
		wantNoRetry   bool
		wantUnavail   bool
		wantErrString string
	}{
		{
			name:         "wrong password or missing OTP",
			status:       http.StatusUnauthorized,
			message:      "invalid username, password or token",
			wantRejected: true,
		},
		{
			name:         "two-factor lockout",
			status:       http.StatusUnauthorized,
			message:      tooManyOTPAttempts,
			wantRejected: true,
			wantNoRetry:  true,
		},
		{
			name:        "basic auth not accepted",
			status:      http.StatusUnauthorized,
			message:     authRequired,
			wantUnavail: true,
		},
		{
			name:        "account uses WebAuthn",
			status:      http.StatusUnauthorized,
			message:     webAuthnEnrolled,
			wantUnavail: true,
		},
		{
			name:        "password sign-in disabled reaches the self-or-admin check",
			status:      http.StatusForbidden,
			message:     notSelfOrAdmin,
			wantUnavail: true,
		},
		{
			name:          "account prohibited from signing in",
			status:        http.StatusForbidden,
			message:       "This account is prohibited from signing in, please contact your site administrator.",
			wantErrString: "git.example.com does not allow this account to sign in: This account is prohibited from signing in, please contact your site administrator.",
		},
		{
			name:          "request body rejected",
			status:        http.StatusUnprocessableEntity,
			message:       "Unsupported Content-Type",
			wantErrString: "HTTP 422: Unsupported Content-Type (https://git.example.com/api/v1/users/monalisa/tokens)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asGitea(t, hostname)

			reg := &httpmock.Registry{}
			defer reg.Verify(t)
			reg.Register(
				httpmock.REST("POST", "api/v1/users/monalisa/tokens"),
				httpmock.StatusJSONResponse(tt.status, map[string]string{"message": tt.message}))

			_, err := CreateToken(&http.Client{Transport: reg}, hostname, "monalisa", "hunter2", "name", "")
			require.Error(t, err)

			if tt.wantRejected {
				rejected, ok := err.(PasswordRejectedError)
				require.True(t, ok, "expected PasswordRejectedError, got %T: %v", err, err)
				assert.Equal(t, hostname, rejected.Host)
				assert.Equal(t, tt.message, rejected.Message)
				assert.Equal(t, !tt.wantNoRetry, rejected.CanRetryWithOTP())
				return
			}

			if tt.wantUnavail {
				unavailable, ok := err.(PasswordLoginUnavailableError)
				require.True(t, ok, "expected PasswordLoginUnavailableError, got %T: %v", err, err)
				assert.Equal(t, hostname, unavailable.Host)
				assert.Equal(t, tt.message, unavailable.Message)
				return
			}

			assert.EqualError(t, err, tt.wantErrString)
		})
	}
}

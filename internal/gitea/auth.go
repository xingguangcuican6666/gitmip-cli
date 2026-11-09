// Package gitea holds the parts of Gitea's API that behave differently enough
// from GitHub's that they are not a matter of choosing an endpoint: mutually
// exclusive scope names, token creation guarded by HTTP Basic, and error
// messages that do not say which of username, password or code was wrong.
package gitea

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/cli/cli/v2/api"
	"github.com/cli/cli/v2/internal/safeurl"
)

// TokenScopes are the scopes gm asks for on a token it creates for itself.
//
// Gitea gates each API family on a scope category and derives read or write from
// the HTTP method, and write implies read within a category. These four
// therefore cover everything gm does: repository content, pull requests,
// releases and commit status; issues, comments, labels and milestones;
// organization membership; the viewer query and SSH key upload.
var TokenScopes = []string{
	"write:repository",
	"write:issue",
	"read:organization",
	"write:user",
}

// PersonalAccessTokenURL is the web page where a token can be created by hand.
//
// It is not GitHub's /settings/tokens: Gitea keeps both personal access tokens
// and OAuth2 applications under the user's applications page, and serves
// /settings/tokens as a 404.
func PersonalAccessTokenURL(hostname string) string {
	return fmt.Sprintf("https://%s/user/settings/applications", hostname)
}

// Messages Gitea returns for sign-in failures that need different handling.
// They are the literal strings from the server, which is the only way to tell
// the failures apart: the response body is the sole signal, and Gitea uses one
// message for every credential problem.
const (
	// authRequired is returned by the reqBasicOrRevProxyAuth middleware when a
	// request was authenticated, but not with HTTP Basic.
	authRequired = "auth required"
	// webAuthnEnrolled is returned for an account with a registered WebAuthn
	// credential, which Gitea refuses to authenticate with Basic.
	webAuthnEnrolled = "basic authorization is not allowed while WebAuthn enrolled"
	// notSelfOrAdmin is returned by the reqSelfOrAdmin middleware. A request
	// whose Basic credentials were not attempted at all, because password
	// sign-in is disabled, reaches that check unauthenticated and receives it.
	notSelfOrAdmin = "doer should be the site admin or be same as the target user"
	// tooManyOTPAttempts is returned once the account has used up its
	// two-factor attempts. Waiting is the only way forward, so asking for
	// another code would be misleading.
	tooManyOTPAttempts = "too many failed OTP attempts, try again later"
)

// PasswordRejectedError reports that a host refused a username and password.
//
// Gitea answers every failed sign-in with the same message, whether the password
// was wrong, the account has two-factor authentication and the code was missing,
// or the instance has password sign-in turned off because it delegates to an SSO
// provider. Nothing in the response distinguishes them, so the message is
// carried along and the caller explains the possibilities.
type PasswordRejectedError struct {
	Host    string
	Message string
}

func (e PasswordRejectedError) Error() string {
	return fmt.Sprintf("%s rejected the username or password", e.Host)
}

// CanRetryWithOTP reports whether asking for a two-factor code and retrying
// the same credentials could succeed. Gitea gives no reason for a failed
// sign-in, so a rejected password and a missing code look identical; only the
// lockout is explicit, and it rules out an immediate retry.
func (e PasswordRejectedError) CanRetryWithOTP() bool {
	return e.Message != tooManyOTPAttempts
}

// PasswordLoginUnavailableError reports that a host did not accept password
// sign-in at all, so repeating the attempt with the same credentials cannot
// succeed. The caller should guide the user to a personal access token.
type PasswordLoginUnavailableError struct {
	Host    string
	Message string
}

func (e PasswordLoginUnavailableError) Error() string {
	return fmt.Sprintf("%s does not accept password login", e.Host)
}

type createTokenRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

// createTokenResponse is the subset of Gitea's AccessToken schema that matters
// here. The token itself is serialised as "sha1" rather than "token", which is a
// leftover name from before Gitea hashed tokens before storing them.
type createTokenResponse struct {
	Token  string   `json:"sha1"`
	Scopes []string `json:"scopes"`
}

// TokenName returns a name for a token gm is about to create.
//
// Gitea rejects a second token with a name already in use, so a fixed name would
// make the second login on a host fail. A short random suffix keeps the name
// recognisable in the host's token list while staying unique.
func TokenName() string {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "gitmip-cli"
	}
	return "gitmip-cli-" + hex.EncodeToString(suffix)
}

// CreateToken creates a personal access token for username by signing the
// request with HTTP Basic, which is the one flow Gitea offers a programmatic
// client that has no OAuth2 application registered.
//
// otp may be empty. Gitea reads a two-factor code from the X-Gitea-OTP header
// and ignores the header for accounts that do not use two-factor authentication.
//
// Gitea requires Basic authentication on this endpoint rather than a token, so a
// token cannot be used to mint another token; the request carries the password
// instead. The password is never sent anywhere else and is not stored.
func CreateToken(httpClient *http.Client, hostname, username, password, tokenName, otp string) (string, error) {
	if username == "" || password == "" {
		return "", errors.New("a username and password are required")
	}

	path, err := safeurl.JoinPath("users", username, "tokens")
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(createTokenRequest{Name: tokenName, Scopes: TokenScopes})
	if err != nil {
		return "", err
	}

	opts := []api.RequestOption{
		api.WithHeader("Authorization", "Basic "+basicCredentials(username, password)),
		// Gitea's request binder dispatches on Content-Type and rejects a POST
		// without a recognised one, so the JSON body is only read when this
		// header is present. The go-gh client deliberately sends no default
		// headers, so it has to be set here.
		api.WithHeader("Content-Type", "application/json"),
	}
	if otp != "" {
		opts = append(opts, api.WithHeader("X-Gitea-OTP", otp))
	}

	res, err := api.NewClientFromHTTP(httpClient).Request(
		hostname, http.MethodPost, path.String(), bytes.NewReader(payload), opts...)
	if err != nil {
		return "", explainCreateTokenError(hostname, err)
	}
	defer res.Body.Close()

	var created createTokenResponse
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		return "", fmt.Errorf("could not read the new token from %s: %w", hostname, err)
	}
	if created.Token == "" {
		// A 201 with no token would leave the user logged out with no clue why,
		// so treat it as a failure rather than storing an empty credential.
		return "", fmt.Errorf("%s created a token but returned no value for it", hostname)
	}
	return created.Token, nil
}

func basicCredentials(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

// explainCreateTokenError translates a failed token creation into an error the
// login flow can act on.
//
// Which failure happened has to be read from the status code and the message,
// because the endpoint's middleware chain answers several different problems
// with the same status. A 401 is either a rejected credential, a missing
// two-factor code, or a sign-in method that is not available at all; a 403 is
// either the account being barred from signing in or Basic authentication being
// switched off, in which case the request never reached the credential check
// and the user needs a token instead.
func explainCreateTokenError(hostname string, err error) error {
	httpErr, ok := errors.AsType[api.HTTPError](err)
	if !ok {
		return err
	}

	switch httpErr.StatusCode {
	case http.StatusUnauthorized:
		switch httpErr.Message {
		case authRequired, webAuthnEnrolled:
			// Basic authentication was not the method the host accepted, so the
			// same username and password will never work, with or without a code.
			return PasswordLoginUnavailableError{Host: hostname, Message: httpErr.Message}
		default:
			return PasswordRejectedError{Host: hostname, Message: httpErr.Message}
		}
	case http.StatusForbidden:
		if httpErr.Message == notSelfOrAdmin {
			// The request was not signed in by the time the handler checked that
			// the caller is the target user. With Basic credentials present that
			// only happens when the host did not attempt password sign-in.
			return PasswordLoginUnavailableError{Host: hostname, Message: httpErr.Message}
		}
		// Any other 403 means the credentials were accepted but the account is
		// not allowed to sign in, which a token does not change.
		return fmt.Errorf("%s does not allow this account to sign in: %s", hostname, httpErr.Message)
	default:
		return err
	}
}

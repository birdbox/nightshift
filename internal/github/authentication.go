package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	ErrAuthenticationNeeded = errors.New("authentication required")
	ErrUnauthorized         = errors.New("unauthorized")
	ErrCodeExpired          = errors.New("the sign-in code expired before it was approved")
	ErrAccessDenied         = errors.New("sign-in was cancelled in the browser")
	ErrDeviceFlowDisabled   = errors.New("device flow is not enabled for this GitHub App")
)

// tokenResponse covers both outcomes of a poll: a token, or an error code. GitHub
// returns HTTP 200 for both, so the Error field is what tells them apart.
type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
	TokenType             string `json:"token_type"`

	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Interval         int    `json:"interval"` // sent along with slow_down
}

// credentials converts relative lifetimes ("expires in 28800s") into absolute
// timestamps, which is what we need once the token is stored on disk.
func (r tokenResponse) credentials(now time.Time) Credentials {
	c := Credentials{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken}
	if r.ExpiresIn > 0 {
		c.ExpiresAt = now.Add(time.Duration(r.ExpiresIn) * time.Second)
	}
	if r.RefreshTokenExpiresIn > 0 {
		c.RefreshExpiresAt = now.Add(time.Duration(r.RefreshTokenExpiresIn) * time.Second)
	}
	return c
}

func Authenticate(ctx context.Context) (Credentials, error) {
	credentials, err := LoadCredentials()
	if err != nil {
		if errors.Is(err, ErrCredentialsNotFound) {
			return Credentials{}, ErrAuthenticationNeeded
		}
		return Credentials{}, err
	}

	if credentials.Expired() {
		if credentials.CanRefresh() {
		} else {
			return Credentials{}, ErrAuthenticationNeeded
		}
	}

	login, err := CurrentUser(ctx, credentials.AccessToken)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return Credentials{}, ErrAuthenticationNeeded
		}
		return Credentials{}, err
	}

	credentials.Login = login
	return credentials, nil
}

func getJSON(ctx context.Context, token, path string, out any) error {
	url := apiBaseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() // nolint:errcheck

	switch resp.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(resp.Body).Decode(out)
	case http.StatusUnauthorized:
		return ErrUnauthorized
	default:
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
}

// CurrentUser returns the login of the token's owner. It is also
// the cheapest way to check if a token is still valid.
func CurrentUser(ctx context.Context, token string) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := getJSON(ctx, token, "/user", &user); err != nil {
		return "", fmt.Errorf("get current user: %w", err)
	}
	return user.Login, nil
}

// Installations returns the number of accounts or organizations the
// token's owner can reach. Zero means that it has not been installed.
func Installations(ctx context.Context, token string) (int, error) {
	var page struct {
		TotalCount int `json:"total_count"`
	}
	if err := getJSON(ctx, token, "/user/installations", &page); err != nil {
		return 0, fmt.Errorf("list installations: %w", err)
	}
	return page.TotalCount, nil
}

// InstallURL allows the user or organization administrator to install
// the application.
func InstallURL() string {
	return "https://github.com/apps/" + Application + "/installations/new"
}

// postForm sends a form-encoded POST request and decodes a JSON response.
func postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	values := strings.NewReader(form.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, values)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() // nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return json.NewDecoder(resp.Body).Decode(out)
}

// RequestDeviceCode starts the GitHub sign in process using a device code. Asking for
// "offline_access" requests a refresh token (8-hours) along with the access token.
func RequestDeviceCode(ctx context.Context) (DeviceCode, error) {
	form := url.Values{
		"client_id": {ClientID},
		"scope":     {"offline_access"},
	}
	var code DeviceCode
	if err := postForm(ctx, "https://github.com/login/device/code", form, &code); err != nil {
		return DeviceCode{}, fmt.Errorf("request device code: %w", err)
	}
	return code, nil
}

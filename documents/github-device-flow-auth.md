# Signing in to GitHub from a TUI: the OAuth Device Flow

This guide explains how a terminal app like nightshift can sign a user in to
GitHub without asking them to hand-craft a personal access token, and walks
through implementing it in Go with Bubble Tea, step by step.

By the end you'll have:

- an `internal/auth` package (standard library only) that talks to GitHub, and
- a Bubble Tea model that checks for a saved token at startup and, if there
  isn't a usable one, walks the user through approving nightshift in their
  browser.

All the code in this guide was compiled, vetted, and tested against Go 1.26,
`charm.land/bubbletea/v2` v2.1.0, `charm.land/bubbles/v2`, and
`charm.land/lipgloss/v2`. Treat it as a reference, not something to paste. The
point is for you to write your own.

---

## Contents

1. [What the user sees](#1-what-the-user-sees)
2. [Why the device flow?](#2-why-the-device-flow)
3. [Who does what: register, install, authorize](#3-who-does-what-register-install-authorize)
4. [Step 0: Register the GitHub App](#4-step-0-register-the-github-app)
5. [How the flow works](#5-how-the-flow-works)
6. [Step 1: Request a device code](#6-step-1-request-a-device-code)
7. [Step 2: Show the code and open the browser](#7-step-2-show-the-code-and-open-the-browser)
8. [Step 3: Poll for the token](#8-step-3-poll-for-the-token)
9. [Step 4: Validate the token](#9-step-4-validate-the-token)
10. [Step 5: Store the credentials](#10-step-5-store-the-credentials)
11. [Step 6: Expiry and refresh](#11-step-6-expiry-and-refresh)
12. [Step 7: Wire it into Bubble Tea](#12-step-7-wire-it-into-bubble-tea)
13. [Testing](#13-testing)
14. [Security notes](#14-security-notes)
15. [Further reading](#15-further-reading)

---

## 1. What the user sees

```
  Sign in to GitHub

  1. Copy your one-time code (copied to clipboard)

     ╭─────────────────╮
     │                 │
     │    WDJB-MJHT    │
     │                 │
     ╰─────────────────╯

  2. Press Enter to open https://github.com/login/device

  ⣾ Waiting for approval… code expires in 14m32s

  q quit
```

The user presses Enter, the browser opens on `github.com/login/device`, they
paste the code, click **Authorize**, and the terminal flips to:

```
  ✓ Signed in as @octocat
```

On the next launch, nightshift finds the saved token, checks it's still good,
and skips straight past this screen.

---

## 2. Why the device flow?

There are three common ways a CLI gets a GitHub token:

| Approach | How it works | Problems for a CLI |
|---|---|---|
| **Manual PAT** | User creates a token on github.com and pastes it in | Tedious and error-prone. Users pick the wrong permissions or expiry, and tokens live forever in password managers. |
| **Web flow + localhost callback** | App starts an HTTP server on `127.0.0.1`, opens the browser, GitHub redirects back with a `code`, app exchanges the code for a token | The exchange requires the app's **client secret**, and anything shipped in a binary is public. It also breaks over SSH because the browser and the listener are on different machines. |
| **Device flow** ✅ | App gets a short code from GitHub, the user enters it on github.com from *any* device, and the app polls until approved | No secret, no local server, works over SSH and in containers |

The device flow ([RFC 8628](https://datatracker.ietf.org/doc/html/rfc8628))
was designed for "input-constrained devices" such as smart TVs and printers. A
terminal fits that description. `gh auth login`, the Azure CLI, and many others
use it.

**Why is it safe to embed the client ID?** The client ID only *identifies* the
app. It's like a username, not a password. The security of the flow comes from
the **user** approving the request while logged in to github.com. An attacker
who copies your client ID can only start sign-ins that a user would have to
approve, and GitHub shows the app name on the approval page.

---

## 3. Who does what: register, install, authorize

GitHub Apps have three separate steps. Keeping them apart avoids a lot of
confusion:

```mermaid
flowchart LR
    subgraph Once["Once, by you (the maintainer)"]
        R["Register the<br/>GitHub App"]
    end
    subgraph PerOrg["Once per account or org"]
        I["Install the app<br/>and choose repos"]
    end
    subgraph PerUser["Per user and machine"]
        A["Authorize via<br/>device flow"]
    end
    R -->|"client ID baked<br/>into the binary"| A
    R --> I
    I -.->|"decides which repos<br/>the token can reach"| A
```

| Step | Who | How often | What it does |
|---|---|---|---|
| **Register** | You | Once, ever | Creates the app's identity (name, client ID, permissions). |
| **Install** | An owner or admin of an account or org | Once per account or org | Grants the app access to specific repositories. |
| **Authorize** | Each user | Once per machine, plus re-auth on expiry | Lets the app act **as that user** (a *user access token*). |

A user access token from a GitHub App can only do what **both** of these allow:

- the **user** can do, and
- the **app** is installed and permitted to do.

If the app isn't installed anywhere the user can see, sign-in still succeeds,
but the token can't touch any repositories. Step 4 shows how to detect that and
send the user to the install page.

> **Make the app public.** In GitHub's terms, "public" only means *other
> accounts can install it*. It exposes no code or data. A private app can only
> be installed on the account that owns it.

---

## 4. Step 0: Register the GitHub App

1. Go to **GitHub → Settings → Developer settings → GitHub Apps → New GitHub
   App**. To have the app owned by an organization, use the org's settings
   instead.
2. Fill in:
   - **GitHub App name**: `nightshift` (or whatever is available). This
     determines the **slug** used in URLs, e.g.
     `github.com/apps/nightshift`.
   - **Homepage URL**: the repo URL is fine.
   - **Callback URL**: leave empty. The device flow doesn't use one.
   - **Expire user authorization tokens**: ✅ keep checked. You get 8-hour
     access tokens plus refresh tokens (see Step 6).
   - **Enable Device Flow**: ✅ **must be checked**. Otherwise polling fails
     with `device_flow_disabled`.
   - **Webhook → Active**: ☐ uncheck. nightshift doesn't receive events.
3. **Repository permissions** (only what nightshift needs):
   - Contents: *Read and write* (push branches)
   - Issues: *Read and write* (comment, label)
   - Pull requests: *Read and write* (open PRs)
   - Metadata: *Read-only* (mandatory, auto-selected)
4. **Where can this GitHub App be installed?** → *Any account*.
5. Create the app, then copy the **Client ID** from the app's settings page.
   It looks like `Iv23li…`. Don't confuse it with the numeric **App ID**.
   You do **not** need to generate a client secret or a private key.

---

## 5. How the flow works

```mermaid
sequenceDiagram
    autonumber
    actor U as User
    participant T as nightshift (TUI)
    participant G as github.com
    participant B as Browser
    participant API as api.github.com

    T->>G: POST /login/device/code<br/>client_id, scope=offline_access
    G-->>T: device_code, user_code, verification_uri,<br/>expires_in, interval
    T->>U: Show user_code (and copy it to the clipboard)
    U->>T: Press Enter
    T->>B: Open verification_uri
    loop every interval seconds
        T->>G: POST /login/oauth/access_token<br/>client_id, device_code, grant_type
        G-->>T: error=authorization_pending
    end
    U->>B: Paste user_code, click Authorize
    B->>G: Approve
    T->>G: POST /login/oauth/access_token
    G-->>T: access_token (ghu_…), refresh_token (ghr_…), expires_in
    T->>API: GET /user (Authorization: Bearer ghu_…)
    API-->>T: { "login": "octocat" }
    T->>T: Save credentials (0600)
    T->>U: ✓ Signed in as @octocat
```

Note what crosses the wire, and what doesn't:

- **`device_code`** is secret-ish. Only the app knows it, and it's what the app
  trades for a token.
- **`user_code`** is public. It's short and meant for a human to type. It only
  ties the browser session to the pending request.
- The **token never passes through the browser**. The app picks it up directly
  from GitHub on its next poll.

---

## 6. Step 1: Request a device code

**Request**

```http
POST https://github.com/login/device/code
Content-Type: application/x-www-form-urlencoded
Accept: application/json

client_id=Iv23liEXAMPLE0000000&scope=offline_access
```

- `scope=offline_access` asks for a **refresh token** alongside the access
  token. GitHub Apps don't otherwise use scopes, because permissions come from
  the app's settings, so this is the only scope value they accept.
- `Accept: application/json` matters. Without it, GitHub's OAuth endpoints
  reply form-encoded (`device_code=…&user_code=…`).

**Response**

```json
{
  "device_code": "3584d83530557fdd1f46af8289938c8ef79f9dc5",
  "user_code": "WDJB-MJHT",
  "verification_uri": "https://github.com/login/device",
  "expires_in": 900,
  "interval": 5
}
```

| Field | Meaning |
|---|---|
| `device_code` | 40-character code the app sends back when polling. Don't show it. |
| `user_code` | 8 characters with a hyphen. Show this one. |
| `verification_uri` | Where the user enters the code. |
| `expires_in` | Seconds until both codes expire (usually 900 = 15 min). |
| `interval` | Minimum seconds between polls. |

**Go**

The package starts with the app's identity and the base URLs. The URLs are
variables rather than constants so tests can point them at a fake server
(see [Testing](#13-testing)).

```go
// internal/auth/device.go
package auth

// ClientID identifies the nightshift GitHub App. It is public by design:
// the device flow never needs the client secret.
const ClientID = "Iv23liEXAMPLE0000000"

// AppSlug is the app's URL name, used to link to its install page.
const AppSlug = "nightshift"

// Base URLs are variables so tests can point them at an httptest.Server.
var (
	githubURL = "https://github.com"
	apiURL    = "https://api.github.com"
)
```

The response maps onto a struct with JSON tags:

```go
// DeviceCode is GitHub's answer to "I'd like to sign a user in".
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`      // secret; only we send it back
	UserCode        string `json:"user_code"`        // shown to the user, e.g. WDJB-MJHT
	VerificationURI string `json:"verification_uri"` // https://github.com/login/device
	ExpiresIn       int    `json:"expires_in"`       // seconds, usually 900
	Interval        int    `json:"interval"`         // minimum seconds between polls
}
```

Both OAuth endpoints (device code and token) take the same kind of request:
a form-encoded POST that returns JSON. That makes it worth a small helper:

```go
// postForm sends a form-encoded POST and decodes the JSON response into out.
// Without "Accept: application/json" GitHub answers these OAuth endpoints
// with a form-encoded body instead.
func postForm(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
```

Things to notice:

- `http.NewRequestWithContext`: every request takes a `context.Context`, so
  when the user quits the TUI you can cancel in-flight requests. This is
  important later.
- `url.Values` handles the form encoding and escaping for you.
- `io.LimitReader` stops a misbehaving server from making you read a huge
  error body into memory.

With the helper, requesting a code is short:

```go
// RequestDeviceCode starts a sign-in. offline_access asks GitHub for a
// refresh token alongside the (8-hour) access token.
func RequestDeviceCode(ctx context.Context) (DeviceCode, error) {
	form := url.Values{
		"client_id": {ClientID},
		"scope":     {"offline_access"},
	}
	var dc DeviceCode
	if err := postForm(ctx, githubURL+"/login/device/code", form, &dc); err != nil {
		return DeviceCode{}, fmt.Errorf("request device code: %w", err)
	}
	return dc, nil
}
```

> 💡 **Try it before writing any Go.** With your real client ID:
>
> ```sh
> curl -s -X POST https://github.com/login/device/code \
>   -H 'Accept: application/json' \
>   -d client_id=Iv23li... -d scope=offline_access
> ```
>
> Go to the URL, enter the code, then run the polling `curl` from Step 3.
> Doing the whole flow by hand once makes the Go code much easier to follow.

---

## 7. Step 2: Show the code and open the browser

Three things make this screen feel polished:

1. **Display the code prominently.** It's the only thing the user has to act
   on. Step 7 puts it in a bordered lipgloss box.
2. **Copy it to the clipboard.** Bubble Tea v2 has `tea.SetClipboard(s)`,
   which uses the **OSC52** terminal escape sequence. That means it also works
   over SSH, because the *local* terminal sets *its own* clipboard. Not every
   terminal supports OSC52 (most modern ones do), so don't make it the only way
   to get the code.
3. **Open the browser on a keypress**, not automatically. Opening a browser
   without warning is jarring, and over SSH it would open on the remote machine
   or fail. Always print the URL as well.

Opening a URL is OS-specific:

```go
// openBrowser returns no message: if it fails, the URL is on screen anyway.
func openBrowser(url string) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", url)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
		default:
			cmd = exec.Command("xdg-open", url)
		}
		// Stdout/Stderr stay nil (→ /dev/null) so the opener can't scribble
		// over the TUI. Wait in the background to reap the process.
		if err := cmd.Start(); err == nil {
			go cmd.Wait()
		}
		return nil
	}
}
```

Two details:

- It's a `tea.Cmd` (a `func() tea.Msg`) because launching a process is I/O,
  and Bubble Tea wants I/O done in commands, never in `Update`. More on this in
  Step 7.
- `xdg-open` and some browsers print noise to stderr. If that reached your
  terminal it would corrupt the TUI. Leaving `cmd.Stderr` nil sends it to the
  null device.

---

## 8. Step 3: Poll for the token

While the user is in the browser, the app repeatedly asks GitHub "approved
yet?".

**Request**

```http
POST https://github.com/login/oauth/access_token
Accept: application/json

client_id=Iv23li…&device_code=3584d8…&grant_type=urn:ietf:params:oauth:grant-type:device_code
```

The odd-looking `grant_type` is a fixed string defined by RFC 8628. Copy it
exactly.

**Response.** This is the subtle part: GitHub answers **HTTP 200 whether or
not it worked**. You have to look inside the body.

Still waiting:

```json
{ "error": "authorization_pending", "error_description": "The authorization request is still pending.", "error_uri": "…" }
```

Success:

```json
{
  "access_token": "ghu_16C7e42F292c6912E7710c838347Ae178B4a",
  "expires_in": 28800,
  "refresh_token": "ghr_1B4a2e77838347a7E420ce178F2E7c6912E169246c34E1ccbF66C46812d16D5B1A9Dc86A1498",
  "refresh_token_expires_in": 15897600,
  "scope": "",
  "token_type": "bearer"
}
```

**Error codes you must handle**

| `error` | Meaning | What to do |
|---|---|---|
| `authorization_pending` | User hasn't finished yet | Keep polling at the same interval |
| `slow_down` | You polled too fast | Add **5 seconds** to the interval (the response also includes a new `interval`) and keep polling |
| `expired_token` | The 15 minutes ran out | Stop. Offer to start over with a new code. |
| `access_denied` | User clicked **Cancel** | Stop. The code can't be reused. |
| `device_flow_disabled` | "Enable Device Flow" isn't checked on the app | Stop. This is a setup bug, not a user error. |
| `incorrect_client_credentials` | Wrong client ID (maybe you used the App ID?) | Stop. Setup bug. |
| `incorrect_device_code` | `device_code` is wrong | Stop. Programming bug. |
| `unsupported_grant_type` | `grant_type` typo | Stop. Programming bug. |

**Go**

One struct covers both success and error responses. Fields that weren't sent
remain zero values:

```go
// tokenResponse covers both outcomes of a poll: a token, or an error code.
// GitHub returns HTTP 200 for both, so the Error field is what tells them apart.
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
```

The outcomes the UI cares about get **sentinel errors**, so the TUI can show a
friendly message with `errors.Is`:

```go
var (
	ErrCodeExpired        = errors.New("the sign-in code expired before it was approved")
	ErrAccessDenied       = errors.New("sign-in was cancelled in the browser")
	ErrDeviceFlowDisabled = errors.New("device flow is not enabled for this GitHub App")
)
```

The polling loop:

```go
// PollToken waits for the user to approve the device code in their browser.
// It blocks until approval, a terminal error, or ctx is cancelled.
func PollToken(ctx context.Context, dc DeviceCode) (Credentials, error) {
	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	form := url.Values{
		"client_id":   {ClientID},
		"device_code": {dc.DeviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	for {
		// Wait first: the user can't possibly have approved yet on the first pass.
		select {
		case <-ctx.Done():
			return Credentials{}, ctx.Err()
		case <-time.After(interval):
		}
		if time.Now().After(deadline) {
			return Credentials{}, ErrCodeExpired
		}

		var tr tokenResponse
		if err := postForm(ctx, githubURL+"/login/oauth/access_token", form, &tr); err != nil {
			return Credentials{}, fmt.Errorf("poll for token: %w", err)
		}

		switch tr.Error {
		case "":
			return tr.credentials(time.Now()), nil
		case "authorization_pending":
			// The user hasn't finished yet; keep waiting.
		case "slow_down":
			interval = nextInterval(interval, tr.Interval)
		case "expired_token", "token_expired":
			return Credentials{}, ErrCodeExpired
		case "access_denied":
			return Credentials{}, ErrAccessDenied
		case "device_flow_disabled":
			return Credentials{}, ErrDeviceFlowDisabled
		default:
			return Credentials{}, fmt.Errorf("poll for token: %s: %s", tr.Error, tr.ErrorDescription)
		}
	}
}

// nextInterval applies a slow_down: use GitHub's suggested interval if it
// sent one, otherwise back off by the 5 seconds the spec mandates.
func nextInterval(current time.Duration, suggestedSeconds int) time.Duration {
	if s := time.Duration(suggestedSeconds) * time.Second; s > current {
		return s
	}
	return current + 5*time.Second
}
```

Why it's built this way:

- **`select` on `ctx.Done()` and `time.After`** is the idiomatic Go "sleep,
  but wake up early if cancelled". A plain `time.Sleep` would keep the
  goroutine alive for up to 5 seconds after the user quits.
- **Both `expired_token` and `token_expired`** are matched because GitHub's
  docs use both spellings in different places. Being lenient costs nothing.
- **`nextInterval` is a separate pure function** so it's trivial to unit test.
  That's a good habit: push decisions into small pure functions, and keep the
  I/O loop thin.
- **Exercise:** a single network blip during polling currently ends the whole
  sign-in. Try tolerating a few consecutive network errors before giving up.

Lifetimes arrive as relative seconds ("expires in 28800"), which mean nothing
once saved to disk. Convert them to absolute times immediately:

```go
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
```

Passing `now` in, instead of calling `time.Now()` inside, makes it testable
with a fixed clock.

---

## 9. Step 4: Validate the token

A saved token can stop working at any time: the user may revoke the app at
*github.com → Settings → Applications*, or an org admin may uninstall it. The
cheapest check is `GET /user`. It returns the user's login on success and
**401** if the token is dead.

```go
// ErrUnauthorized means GitHub rejected the token: revoked, expired, or bogus.
var ErrUnauthorized = errors.New("GitHub rejected the token")

func getJSON(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL+path, nil)
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
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return json.NewDecoder(resp.Body).Decode(out)
	case http.StatusUnauthorized:
		return ErrUnauthorized
	default:
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
}

// CurrentUser returns the login of the token's owner. It doubles as the
// cheapest way to ask GitHub "is this token still good?".
func CurrentUser(ctx context.Context, token string) (string, error) {
	var user struct {
		Login string `json:"login"`
	}
	if err := getJSON(ctx, token, "/user", &user); err != nil {
		return "", fmt.Errorf("get current user: %w", err)
	}
	return user.Login, nil
}
```

Because `CurrentUser` wraps with `%w`, callers can still check
`errors.Is(err, auth.ErrUnauthorized)` through the extra context.

**Is the app installed?** Section 3 explained that a valid token can still
have no repos to work with. `GET /user/installations` lists the installations
this user can access through the app:

```go
// InstallationCount reports how many accounts/orgs this user can reach
// through the app. Zero means the app isn't installed anywhere they can see,
// so the token works but can't touch any repository yet.
func InstallationCount(ctx context.Context, token string) (int, error) {
	var page struct {
		TotalCount int `json:"total_count"`
	}
	if err := getJSON(ctx, token, "/user/installations", &page); err != nil {
		return 0, fmt.Errorf("list installations: %w", err)
	}
	return page.TotalCount, nil
}

// InstallURL is where a user (or org admin) installs the app.
func InstallURL() string {
	return "https://github.com/apps/" + AppSlug + "/installations/new"
}
```

> **Going further:** once nightshift knows which repo it's operating on, a
> more precise check is `GET /repos/{owner}/{repo}/installation`. Its result
> tells you whether *this particular repo* is covered.

---

## 10. Step 5: Store the credentials

```go
// Credentials is what we persist between runs.
type Credentials struct {
	AccessToken      string    `json:"access_token"`
	ExpiresAt        time.Time `json:"expires_at,omitzero"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitzero"`
	Login            string    `json:"login,omitempty"`
}
```

`omitzero` (Go 1.24+) leaves out zero `time.Time` values. `omitempty` doesn't
work for structs like `time.Time`.

**Where:** `os.UserConfigDir()` gives `$XDG_CONFIG_HOME` (or `~/.config`) on
Linux and `~/Library/Application Support` on macOS. The existing
`internal/secret` package always uses `~/.config/nightshift`. Pick one
convention and use it consistently.

**How:** the file must be readable **only by you** (mode `0600`) in a
directory only you can enter (`0700`). This is the same model as `gh`, `git`'s
credential store, and the AWS CLI. The file isn't encrypted; it's protected by
permissions.

```go
// ErrNoCredentials means nothing has been saved yet.
var ErrNoCredentials = errors.New("no saved GitHub credentials")

func credentialsPath() (string, error) {
	// $XDG_CONFIG_HOME or ~/.config on Linux, ~/Library/Application Support on macOS.
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "nightshift", "credentials.json"), nil
}

// Load reads saved credentials, returning ErrNoCredentials if there are none.
func Load() (Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return Credentials{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credentials{}, ErrNoCredentials
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("read credentials: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return Credentials{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, nil
}

// Save writes credentials readable only by the current user (0600). It
// writes a temp file and renames it over the old one, so a crash mid-write
// can never leave a half-written credentials file behind.
func Save(c Credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	// CreateTemp makes the file 0600, so the token is never world-readable,
	// not even briefly.
	tmp, err := os.CreateTemp(filepath.Dir(path), "credentials-*.json")
	if err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("save credentials: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	return nil
}
```

**Why write-then-rename?** `os.Rename` within one directory is atomic on
POSIX. Readers see either the old file or the new one, never a half-written
mix. This matters more than it sounds, because refresh tokens are **single
use** (see the next step). If a crash corrupted the file right after a
refresh, the only valid refresh token would be lost.

> **Alternative: the OS keychain.** macOS Keychain, GNOME Keyring, and Windows
> Credential Manager encrypt at rest. In Go that usually means a third-party
> module such as `github.com/zalando/go-keyring`, which goes against this
> project's standard-library-first rule, so a `0600` file is a reasonable
> starting point.

---

## 11. Step 6: Expiry and refresh

With "Expire user authorization tokens" on and `scope=offline_access`, you get:

| Token | Prefix | Lifetime |
|---|---|---|
| Access token | `ghu_` | **8 hours** (`expires_in: 28800`) |
| Refresh token | `ghr_` | **~6 months** (`refresh_token_expires_in: 15897600`) |

Expiry limits the damage if a token leaks. The refresh token means the user
still only signs in about twice a year.

**Refresh request**

```http
POST https://github.com/login/oauth/access_token
Accept: application/json

client_id=Iv23li…&grant_type=refresh_token&refresh_token=ghr_…
```

The response has the same shape as a successful poll, with a **new** access
token **and a new refresh token**.

> **No client secret needed.** GitHub's docs list `client_secret` as required
> for refreshing *unless the token was generated with the device flow*. So a
> CLI can refresh without holding any secret.

> ⚠️ **Refresh tokens are single use.** Using one invalidates it *and* the old
> access token. Always save the new pair immediately. If two nightshift
> processes refresh at the same time, one of them will lose. That's an edge
> case worth knowing about, and it's fine to ignore at first.

```go
// Refresh trades a refresh token for a fresh token pair. Tokens that came
// from the device flow can be refreshed without the client secret.
// Each refresh token works exactly once, so the result must be saved.
func Refresh(ctx context.Context, refreshToken string) (Credentials, error) {
	form := url.Values{
		"client_id":     {ClientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	var tr tokenResponse
	if err := postForm(ctx, githubURL+"/login/oauth/access_token", form, &tr); err != nil {
		return Credentials{}, fmt.Errorf("refresh token: %w", err)
	}
	if tr.Error != "" {
		return Credentials{}, fmt.Errorf("refresh token: %s: %s", tr.Error, tr.ErrorDescription)
	}
	return tr.credentials(time.Now()), nil
}
```

Two helpers on `Credentials` answer "is it expired?" and "can I refresh?":

```go
// skew treats a token as expired slightly early, so it can't lapse between
// our check and GitHub receiving the request.
const skew = time.Minute

// AccessExpired reports whether the access token is past (or near) its expiry.
// Tokens without an expiry never expire.
func (c Credentials) AccessExpired(now time.Time) bool {
	return !c.ExpiresAt.IsZero() && now.Add(skew).After(c.ExpiresAt)
}

// CanRefresh reports whether the refresh token is present and still usable.
func (c Credentials) CanRefresh(now time.Time) bool {
	return c.RefreshToken != "" && (c.RefreshExpiresAt.IsZero() || now.Before(c.RefreshExpiresAt))
}
```

### Putting it together: `Resolve`

At startup the TUI needs a single question answered: *do I have a working
token, or does the user need to sign in?* `Resolve` hides the whole decision
tree:

```mermaid
flowchart TD
    S([Start]) --> L{Saved<br/>credentials?}
    L -- no --> N([ErrNeedLogin])
    L -- yes --> E{Access token<br/>expired?}
    E -- no --> V
    E -- yes --> C{Refresh token<br/>usable?}
    C -- no --> N
    C -- yes --> R[Refresh + Save]
    R -- failed --> N
    R -- ok --> V{GET /user}
    V -- 401 --> N
    V -- 200 --> OK([Credentials + login])
```

```go
// ErrNeedLogin means there's no usable token: the caller should start the
// device flow.
var ErrNeedLogin = errors.New("GitHub sign-in required")

// Resolve returns working credentials without user interaction, refreshing
// if needed, or ErrNeedLogin when the user has to sign in again.
func Resolve(ctx context.Context) (Credentials, error) {
	creds, err := Load()
	if errors.Is(err, ErrNoCredentials) {
		return Credentials{}, ErrNeedLogin
	}
	if err != nil {
		return Credentials{}, err
	}

	if creds.AccessExpired(time.Now()) {
		if !creds.CanRefresh(time.Now()) {
			return Credentials{}, ErrNeedLogin
		}
		login := creds.Login
		if creds, err = Refresh(ctx, creds.RefreshToken); err != nil {
			// A failed refresh (e.g. bad_refresh_token) isn't fatal: signing
			// in again fixes it.
			return Credentials{}, ErrNeedLogin
		}
		creds.Login = login
		if err := Save(creds); err != nil {
			return Credentials{}, err
		}
	}

	// Expiry looks fine locally, but the user may have revoked the app.
	login, err := CurrentUser(ctx, creds.AccessToken)
	if errors.Is(err, ErrUnauthorized) {
		return Credentials{}, ErrNeedLogin
	}
	if err != nil {
		return Credentials{}, err
	}
	creds.Login = login
	return creds, nil
}
```

Note the split between `ErrNeedLogin` ("recoverable: show the sign-in
screen") and other errors ("something is actually broken, e.g. no network or
an unreadable file"). The TUI treats them differently.

---

## 12. Step 7: Wire it into Bubble Tea

### The Elm Architecture in one paragraph

Bubble Tea programs have a **model** (all state), **`Update(msg)`** (the
*only* place state changes, returning a new model and optionally a
**command**), and **`View()`** (a pure function from model to screen).
**Commands** (`tea.Cmd`, i.e. `func() tea.Msg`) are where I/O happens. Bubble
Tea runs them on a separate goroutine, and their return value is fed back
into `Update` as a message.

The rule that follows: **never block in `Update`.** An HTTP call in `Update`
freezes the UI, including the spinner and the keyboard. Every network call
from the previous steps therefore goes inside a `tea.Cmd`.

### States

```mermaid
stateDiagram-v2
    [*] --> Checking
    Checking --> SignedIn: signedInMsg (saved token OK)
    Checking --> Requesting: needLoginMsg
    Checking --> Failed: errMsg
    Requesting --> Waiting: deviceCodeMsg
    Requesting --> Failed: errMsg
    Waiting --> Waiting: Enter / open browser
    Waiting --> SignedIn: signedInMsg (approved)
    Waiting --> Failed: errMsg (expired, denied, network)
    Failed --> Requesting: r (retry)
    SignedIn --> [*]
```

Each arrow is a **message**. Each state is a value of one field in the model.

### Model

```go
type authState int

const (
	stateChecking   authState = iota // loading/validating saved credentials
	stateRequesting                  // asking GitHub for a device code
	stateWaiting                     // code on screen, polling in the background
	stateSignedIn
	stateFailed
)

type model struct {
	state   authState
	spinner spinner.Model

	// ctx is cancelled when the user quits, which stops any in-flight
	// HTTP request or polling loop.
	ctx    context.Context
	cancel context.CancelFunc

	code      auth.DeviceCode
	expiresAt time.Time
	copied    bool

	login    string
	installs int
	err      error
}

func newModel() model {
	ctx, cancel := context.WithCancel(context.Background())
	return model{
		state:   stateChecking,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(accent)),
		ctx:     ctx,
		cancel:  cancel,
	}
}
```

Storing a `context.Context` in a struct is usually discouraged in Go, but a
Bubble Tea model is a common exception. The model *is* the lifetime of the
program, and commands need a context they can be cancelled through.

### Messages

One small type per event. They're plain structs, and the type itself carries
the meaning:

```go
type needLoginMsg struct{}

type deviceCodeMsg struct{ code auth.DeviceCode }

type signedInMsg struct {
	login    string
	installs int
}

type errMsg struct{ err error }
```

### Commands

Each command wraps one blocking step from the `auth` package and turns its
result into a message:

```go
func checkCredentials(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		creds, err := auth.Resolve(ctx)
		if errors.Is(err, auth.ErrNeedLogin) {
			return needLoginMsg{}
		}
		if err != nil {
			return errMsg{err}
		}
		return finishSignIn(ctx, creds)
	}
}

func requestCode(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		code, err := auth.RequestDeviceCode(ctx)
		if err != nil {
			return errMsg{err}
		}
		return deviceCodeMsg{code}
	}
}

func waitForApproval(ctx context.Context, code auth.DeviceCode) tea.Cmd {
	return func() tea.Msg {
		creds, err := auth.PollToken(ctx, code)
		if err != nil {
			return errMsg{err}
		}
		login, err := auth.CurrentUser(ctx, creds.AccessToken)
		if err != nil {
			return errMsg{err}
		}
		creds.Login = login
		if err := auth.Save(creds); err != nil {
			return errMsg{err}
		}
		return finishSignIn(ctx, creds)
	}
}

// finishSignIn runs inside a command, so it may block.
func finishSignIn(ctx context.Context, creds auth.Credentials) tea.Msg {
	n, err := auth.InstallationCount(ctx, creds.AccessToken)
	if err != nil {
		return errMsg{err}
	}
	return signedInMsg{login: creds.Login, installs: n}
}
```

`waitForApproval` can run for up to 15 minutes. That's fine: it's on its own
goroutine, and when the user quits, `m.cancel()` makes `PollToken` return
`context.Canceled` right away.

### Init and Update

```go
func (m model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, checkCredentials(m.ctx))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.cancel()
			return m, tea.Quit
		case "enter":
			if m.state == stateWaiting {
				return m, openBrowser(m.code.VerificationURI)
			}
		case "r":
			if m.state == stateFailed {
				m.state, m.err = stateRequesting, nil
				return m, requestCode(m.ctx)
			}
		}

	case needLoginMsg:
		m.state = stateRequesting
		return m, requestCode(m.ctx)

	case deviceCodeMsg:
		m.state = stateWaiting
		m.code = msg.code
		m.expiresAt = time.Now().Add(time.Duration(msg.code.ExpiresIn) * time.Second)
		m.copied = true
		// Start polling right away: the user may type the code on another
		// device without ever pressing Enter here.
		return m, tea.Batch(
			tea.SetClipboard(msg.code.UserCode),
			waitForApproval(m.ctx, msg.code),
		)

	case signedInMsg:
		m.state = stateSignedIn
		m.login, m.installs = msg.login, msg.installs

	case errMsg:
		m.state, m.err = stateFailed, msg.err

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}
```

Things to notice:

- **`tea.Batch`** runs several commands concurrently. Here it sets the
  clipboard *and* starts polling.
- **The spinner is a sub-model.** It needs its `TickMsg`s forwarded to its own
  `Update`, which returns the command for the next tick. If you forget the
  `spinner.TickMsg` case, the spinner freezes on its first frame.
- **Polling starts before Enter is pressed.** Opening the browser is
  optional, because the user may approve on their phone.
- **`m.copied = true` is optimistic.** OSC52 gives no confirmation. To be
  strictly honest, phrase the hint as "copied, if your terminal supports it",
  or drop it.

### View

```go
var (
	accent  = lipgloss.NewStyle().Foreground(lipgloss.Color("#7D56F4"))
	muted   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	good    = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	bad     = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true)
	codeBox = lipgloss.NewStyle().
		Bold(true).
		Padding(1, 4).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7D56F4"))
)

func (m model) View() tea.View {
	var b strings.Builder
	b.WriteString("\n")

	switch m.state {
	case stateChecking:
		fmt.Fprintf(&b, "  %s Checking GitHub credentials…\n", m.spinner.View())

	case stateRequesting:
		fmt.Fprintf(&b, "  %s Requesting a sign-in code from GitHub…\n", m.spinner.View())

	case stateWaiting:
		b.WriteString("  Sign in to GitHub\n\n")
		b.WriteString("  1. Copy your one-time code")
		if m.copied {
			b.WriteString(muted.Render(" (copied to clipboard)"))
		}
		b.WriteString("\n\n")
		b.WriteString(indent(codeBox.Render(m.code.UserCode), "     "))
		b.WriteString("\n\n")
		fmt.Fprintf(&b, "  2. Press %s to open %s\n\n", accent.Render("Enter"), accent.Render(m.code.VerificationURI))
		// The spinner ticks ~10×/s and every tick re-renders, so this
		// countdown stays live without a timer of its own.
		left := time.Until(m.expiresAt).Round(time.Second)
		fmt.Fprintf(&b, "  %s Waiting for approval… %s\n", m.spinner.View(), muted.Render("code expires in "+left.String()))

	case stateSignedIn:
		fmt.Fprintf(&b, "  %s Signed in as %s\n", good.Render("✓"), accent.Render("@"+m.login))
		if m.installs == 0 {
			fmt.Fprintf(&b, "\n  The nightshift app isn't installed on any of your accounts yet.\n  Install it: %s\n", accent.Render(auth.InstallURL()))
		}

	case stateFailed:
		fmt.Fprintf(&b, "  %s %v\n\n  %s\n", bad.Render("✗"), m.err, muted.Render("r retry"))
	}

	b.WriteString("\n" + muted.Render("  q quit") + "\n")

	v := tea.NewView(b.String())
	v.WindowTitle = "nightshift"
	return v
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}
```

- `lipgloss.Color` accepts hex (`"#7D56F4"`) or ANSI-256 numbers (`"245"`).
  Lip Gloss downsamples automatically on terminals with fewer colors.
- The countdown reads `time.Until` inside `View`, which strictly speaking
  makes `View` impure. It's a pragmatic shortcut. The "pure" alternative is a
  once-per-second `tea.Tick` that stores `now` in the model.
- **Polish ideas:** map sentinel errors to friendlier text in the `Failed`
  state (`errors.Is(m.err, auth.ErrAccessDenied)` → "You cancelled sign-in.
  Press r to try again."); render the URL as a clickable OSC8 hyperlink;
  center everything with `lipgloss.Place` using `tea.WindowSizeMsg`.

### main

```go
func main() {
	if _, err := tea.NewProgram(newModel()).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "nightshift: %v\n", err)
		os.Exit(1)
	}
}
```

### Dependencies

```sh
go get charm.land/bubbles/v2 charm.land/lipgloss/v2
go mod tidy   # also promotes bubbletea from "// indirect" to a direct dependency
```

The `auth` package itself uses only the standard library: `net/http`,
`net/url`, `encoding/json`, `os`, and `time`.

---

## 13. Testing

Network code is easiest to test when it's split into:

1. **pure functions** (`nextInterval`, `tokenResponse.credentials`,
   `AccessExpired`), which get table tests and need no network, and
2. **thin I/O wrappers**, which get tested against `httptest.Server`. That's a
   real HTTP server on a random localhost port, and it's why `apiURL` and
   `githubURL` are variables.

```go
func TestNextInterval(t *testing.T) {
	tests := []struct {
		name      string
		current   time.Duration
		suggested int
		want      time.Duration
	}{
		{"no suggestion adds 5s", 5 * time.Second, 0, 10 * time.Second},
		{"larger suggestion wins", 5 * time.Second, 15, 15 * time.Second},
		{"smaller suggestion ignored", 10 * time.Second, 5, 15 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextInterval(tt.current, tt.suggested); got != tt.want {
				t.Errorf("nextInterval(%v, %d) = %v, want %v", tt.current, tt.suggested, got, tt.want)
			}
		})
	}
}

func TestCredentialsExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	c := tokenResponse{AccessToken: "ghu_x", ExpiresIn: 28800}.credentials(now)

	if c.AccessExpired(now) {
		t.Error("fresh token reported as expired")
	}
	if !c.AccessExpired(now.Add(8 * time.Hour)) {
		t.Error("8-hour-old token not reported as expired")
	}
	if (Credentials{AccessToken: "x"}).AccessExpired(now.Add(100 * 24 * time.Hour)) {
		t.Error("token without expiry reported as expired")
	}
}

func TestCurrentUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"login":"octocat"}`))
	}))
	defer srv.Close()

	old := apiURL
	apiURL = srv.URL
	defer func() { apiURL = old }()

	login, err := CurrentUser(context.Background(), "good")
	if err != nil || login != "octocat" {
		t.Fatalf("CurrentUser(good) = %q, %v; want octocat, nil", login, err)
	}
	if _, err := CurrentUser(context.Background(), "bad"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("CurrentUser(bad) error = %v; want ErrUnauthorized", err)
	}
}
```

**Exercises:**

- Test `PollToken` with a fake server that answers `authorization_pending`
  twice and then a token. With `Interval: 1` it takes about 3 seconds. To make
  it instant, change the poll delay from `time.Second` units to an injectable
  duration.
- Test `Save`/`Load` round-trips with `t.Setenv("XDG_CONFIG_HOME", t.TempDir())`,
  and assert the file mode is `0600`.
- Test the Bubble Tea `Update` directly: build a model, call
  `m.Update(deviceCodeMsg{...})`, and assert on the returned model's `state`.
  No terminal is needed.

**Manual checks:**

1. Delete `credentials.json` → device code screen → approve → "Signed in as …".
   Check `ls -l` shows `-rw-------`.
2. Run again → straight to signed in, with no code screen.
3. Edit the access token in the file → `GET /user` returns 401 → back to the
   code screen.
4. Click **Cancel** on github.com → "sign-in was cancelled" → `r` restarts.
5. Wait 15 minutes → "code expired".
6. Revoke the app at github.com → Settings → Applications → Authorized GitHub
   Apps → next launch asks you to sign in again.

---

## 14. Security notes

- **Never log or print tokens**, not even in debug output. If you add
  logging, redact anything starting with `ghu_` or `ghr_`.
- **`0600` / `0700`** on the credentials file and directory. A `0644` token
  file on a shared machine is a leaked token.
- **Least privilege.** Only grant the app the repository permissions nightshift
  actually uses. You can add more later, and users will be asked to approve the
  change.
- **Keep token expiry on.** An 8-hour access token that leaks is far less
  dangerous than a non-expiring one.
- **Sign out = delete the file.** For a true sign-out, also have the user
  revoke the authorization on github.com. Revoking it via the API requires the
  client secret, which a CLI shouldn't hold.
- **Phishing awareness.** The device flow can be abused by someone who sends a
  victim a code to approve. GitHub mitigates this by showing the app name and
  permissions on the approval screen. This is another reason to keep the
  permissions minimal and the app name clear.

---

## 15. Further reading

- GitHub: [Generating a user access token for a GitHub App](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app) (device flow section)
- GitHub: [Refreshing user access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens)
- GitHub: [Authorizing OAuth apps: device flow](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow)
- [RFC 8628: OAuth 2.0 Device Authorization Grant](https://datatracker.ietf.org/doc/html/rfc8628)
- Bubble Tea v2: [pkg.go.dev/charm.land/bubbletea/v2](https://pkg.go.dev/charm.land/bubbletea/v2)
- Bubbles spinner: [pkg.go.dev/charm.land/bubbles/v2/spinner](https://pkg.go.dev/charm.land/bubbles/v2/spinner)
- Lip Gloss v2: [pkg.go.dev/charm.land/lipgloss/v2](https://pkg.go.dev/charm.land/lipgloss/v2)
- Prior art: [`cli/oauth`](https://github.com/cli/oauth), the Go device-flow library behind `gh auth login`

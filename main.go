package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/birdbox/nightshift/internal/github"
)

type (
	AuthenticationStatus   int
	needsAuthenticationMsg struct{}
	deviceCodeMsg          struct{ code github.DeviceCode }
	errorMsg               struct{ err error }
	signedInMsg            struct {
		login    string
		installs int
	}
)

const (
	AuthenticationStatusChecking AuthenticationStatus = iota
	AuthenticationStatusRequesting
	AuthenticationStatusWaiting
	AuthenticationStatusSuccess
	AuthenticationStatusFailure
)

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

type model struct {
	ctx       context.Context
	cancel    context.CancelFunc
	status    AuthenticationStatus
	spinner   spinner.Model
	code      github.DeviceCode
	expiresAt time.Time
	copied    bool
	login     string
	installs  int
	error     error
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, checkCredentials(m.ctx))
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyPressMsg:
		{
			switch message.String() {
			case "ctrl+c", "q":
				m.cancel()
				return m, tea.Quit
		case "enter":
			if m.status == AuthenticationStatusWaiting {
				return m, openBrowser(m.code.VerificationURI)
			}
			}
		}
	case needsAuthenticationMsg:
		{
			m.status = AuthenticationStatusRequesting
			return m, requestDeviceCode(m.ctx)

		}
	case deviceCodeMsg:
		{
			m.status = AuthenticationStatusWaiting
			m.code = message.code
			m.expiresAt = time.Now().Add(time.Duration(message.code.ExpiresIn) * time.Second)
			m.copied = true
			// Copy the code to the clipboard and start polling for updates.
			return m, tea.Batch(
				tea.SetClipboard(message.code.UserCode),
				waitForApproval(m.ctx, message.code),
			)
		}
	case signedInMsg:
		m.status = AuthenticationStatusSuccess
		m.login, m.installs = message.login, message.installs

	case errorMsg:
		m.status, m.error = AuthenticationStatusFailure, message.err

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(message)
		return m, cmd
	}
	return m, nil
}

func (m model) View() tea.View {
	var b strings.Builder
	b.WriteString("\n")

	switch m.status {
	case AuthenticationStatusChecking:
		fmt.Fprintf(&b, "  %s Checking GitHub credentials…\n", m.spinner.View())

	case AuthenticationStatusRequesting:
		fmt.Fprintf(&b, "  %s Requesting device code from GitHub…\n", m.spinner.View())

	case AuthenticationStatusWaiting:
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

	case AuthenticationStatusSuccess:
		fmt.Fprintf(&b, "  %s Signed in as %s\n", good.Render("✓"), accent.Render("@"+m.login))
		if m.installs == 0 {
			fmt.Fprintf(&b, "\n  The nightshift app isn't installed on any of your accounts yet.\n  Install it: %s\n", accent.Render(github.InstallURL()))
		}

	case AuthenticationStatusFailure:
		fmt.Fprintf(&b, "  %s %v\n\n  %s\n", bad.Render("✗"), m.error, muted.Render("r retry"))
	}

	b.WriteString("\n")
	b.WriteString(muted.Render("  q quit"))
	b.WriteString("\n")

	v := tea.NewView(b.String())
	v.WindowTitle = "nightshift"
	return v
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

func checkCredentials(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		credentials, err := github.Authenticate(ctx)
		if errors.Is(err, github.ErrAuthenticationNeeded) {
			return needsAuthenticationMsg{}
		}
		return finishSignIn(ctx, credentials)
	}
}

func requestDeviceCode(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		code, err := github.RequestDeviceCode(ctx)
		if err != nil {
			return errorMsg{err}
		}
		return deviceCodeMsg{code}
	}
}

func waitForApproval(ctx context.Context, code github.DeviceCode) tea.Cmd {
	return func() tea.Msg {
		creds, err := github.PollToken(ctx, code)
		if err != nil {
			return errorMsg{err}
		}
		login, err := github.CurrentUser(ctx, creds.AccessToken)
		if err != nil {
			return errorMsg{err}
		}
		creds.Login = login
		if err := creds.Save(); err != nil {
			return errorMsg{err}
		}
		return finishSignIn(ctx, creds)
	}
}

// finishSignIn runs inside a command, so it may block.
func finishSignIn(ctx context.Context, creds github.Credentials) tea.Msg {
	n, err := github.Installations(ctx, creds.AccessToken)
	if err != nil {
		return errorMsg{err}
	}
	return signedInMsg{login: creds.Login, installs: n}
}

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
			go cmd.Wait() // nolint:errcheck
		}
		return nil
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	m := model{
		ctx:     ctx,
		cancel:  cancel,
		status:  AuthenticationStatusChecking,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(accent)),
	}

	//credentials, err := github.Authenticate(m.ctx)
	//if err != nil {
	//	fmt.Printf("Unable to authenticate: %s", err)
	//}

	program := tea.NewProgram(m)
	if _, err := program.Run(); err != nil {
		fmt.Printf("Alas, there has been an error: %v", err)
	}
}

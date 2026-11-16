package hub

import (
	"strings"

	"github.com/term-llm/term-llm/internal/htmlesc"
)

// ShellView holds presentation values for the Hub and passkey browser shell.
type ShellView struct {
	Title        string
	StyleURL     string
	ScriptURL    string
	ConfigJSON   string
	Dashboard    bool
	BearerLogin  bool
	InvalidToken bool
	FormAction   string
}

// hubDashboardIcon pairs the terminal prompt with a four-square workspace grid.
const hubDashboardIcon = `<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http%3A//www.w3.org/2000/svg' viewBox='0 0 64 64'%3E%3Crect width='64' height='64' rx='14' fill='%2316634e'/%3E%3Cpath d='m16 20 12 12-12 12' fill='none' stroke='%23ffffff' stroke-width='5' stroke-linecap='round' stroke-linejoin='round'/%3E%3Crect x='37' y='29' width='6' height='6' rx='1.3' fill='%23ffffff'/%3E%3Crect x='37' y='39' width='6' height='6' rx='1.3' fill='%23ffffff'/%3E%3Crect x='47' y='29' width='6' height='6' rx='1.3' fill='%23ffffff'/%3E%3Crect x='47' y='39' width='6' height='6' rx='1.3' fill='%23ffffff'/%3E%3C/svg%3E">`

// RenderShell renders the Hub and passkey browser shell. It is written by
// hand rather than with html/template, whose reflection-based execution
// defeats the linker's dead-code elimination (see internal/htmlesc).
func RenderShell(v ShellView) string {
	var b strings.Builder
	b.WriteString(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="dark light">
  <title>` + htmlesc.Text(v.Title) + `</title>
  `)
	if v.Dashboard {
		b.WriteString(hubDashboardIcon)
	}
	b.WriteString(`
  <link rel="stylesheet" href="` + htmlesc.URL(v.StyleURL) + `">
  <script type="module" src="` + htmlesc.URL(v.ScriptURL) + `"></script>
</head>
<body>
  <div id="root" data-hub-config="` + htmlesc.Text(v.ConfigJSON) + `"></div>
  `)
	if v.BearerLogin {
		b.WriteString(`
  <noscript>
    <main>
      <h1>term-llm Hub</h1>
      <p>Enter your hub access token to continue.</p>
      `)
		if v.InvalidToken {
			b.WriteString(`<p role="alert">That hub token was not accepted.</p>`)
		}
		b.WriteString(`
      <form method="get" action="` + htmlesc.URL(v.FormAction) + `">
        <label for="noscript-hub-token">Hub token</label>
        <input id="noscript-hub-token" name="token" type="password" autocomplete="current-password" required>
        <button type="submit">Connect to Hub</button>
      </form>
    </main>
  </noscript>
  `)
	}
	b.WriteString(`
</body>
</html>
`)
	return b.String()
}

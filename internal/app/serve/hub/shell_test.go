package hub

import (
	"encoding/xml"
	"net/url"
	"strings"
	"testing"
)

func TestDashboardFaviconTerminalGrid(t *testing.T) {
	shell := RenderShell(ShellView{Dashboard: true})
	_, after, ok := strings.Cut(shell, `<link rel="icon" href="data:image/svg+xml,`)
	if !ok {
		t.Fatal("dashboard missing inline SVG favicon")
	}
	encoded, _, ok := strings.Cut(after, `"`)
	if !ok {
		t.Fatal("unterminated favicon URL")
	}
	svg, err := url.PathUnescape(encoded)
	if err != nil {
		t.Fatalf("decode favicon: %v", err)
	}
	var root struct{ XMLName xml.Name }
	if err := xml.Unmarshal([]byte(svg), &root); err != nil {
		t.Fatalf("parse favicon SVG: %v", err)
	}
	if root.XMLName.Local != "svg" {
		t.Fatalf("favicon root = %q, want svg", root.XMLName.Local)
	}
	for _, fragment := range []string{
		`viewBox='0 0 64 64'`,
		`width='64' height='64' rx='14' fill='#16634e'`,
		`d='m16 20 12 12-12 12'`,
		`x='37' y='29' width='6' height='6' rx='1.3'`,
		`x='37' y='39' width='6' height='6' rx='1.3'`,
		`x='47' y='29' width='6' height='6' rx='1.3'`,
		`x='47' y='39' width='6' height='6' rx='1.3'`,
	} {
		if !strings.Contains(svg, fragment) {
			t.Errorf("dashboard favicon missing %q", fragment)
		}
	}
}

func TestNonDashboardShellOmitsDashboardFavicon(t *testing.T) {
	if strings.Contains(RenderShell(ShellView{}), `rel="icon"`) {
		t.Fatal("non-dashboard shell unexpectedly includes dashboard favicon")
	}
}

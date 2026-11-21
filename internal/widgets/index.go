package widgets

import (
	"fmt"
	"html"
	"net/http"
)

// ServeIndex writes the widget registry and its load errors as HTML.
// Launch links are relative to the widget index so they survive reverse proxying.
func (m *Manager) ServeIndex(w http.ResponseWriter) {
	statuses := m.Status()
	errs := m.LoadErrors()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>Widgets</title></head><body>`)
	fmt.Fprintf(w, `<h1>Widgets</h1>`)

	if len(errs) > 0 {
		fmt.Fprintf(w, `<h2>Load Errors</h2><ul>`)
		for _, e := range errs {
			fmt.Fprintf(w, `<li>%s</li>`, html.EscapeString(e.Error()))
		}
		fmt.Fprintf(w, `</ul>`)
	}

	if len(statuses) == 0 {
		fmt.Fprintf(w, `<p>No widgets loaded.</p>`)
	} else {
		fmt.Fprintf(w, `<table border="1" cellpadding="4"><tr><th>Mount</th><th>Title</th><th>State</th><th>Link</th></tr>`)
		for _, st := range statuses {
			fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td>`,
				html.EscapeString(st.Mount),
				html.EscapeString(st.Title),
				html.EscapeString(st.State),
			)
			if st.State != "disabled" {
				fmt.Fprintf(w, `<a href="%s">open</a>`, html.EscapeString("widgets/"+st.Mount+"/"))
			}
			fmt.Fprint(w, `</td></tr>`)
		}
		fmt.Fprintf(w, `</table>`)
	}
	fmt.Fprintf(w, `</body></html>`)
}

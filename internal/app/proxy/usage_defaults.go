package proxy

import "github.com/term-llm/term-llm/internal/app/proxy/usagetap"

// defaultRouteUsage enables observation on new and existing canonical routes.
// An explicit usage: off is never overridden.
func defaultRouteUsage(r *Route) {
	if r.Usage == "" && (usagetap.IsVeniceRoot(r.Upstream) || usagetap.IsExaRoot(r.Upstream) || usagetap.IsTypeSafeRoot(r.Upstream)) {
		r.Usage = "auto"
	}
}

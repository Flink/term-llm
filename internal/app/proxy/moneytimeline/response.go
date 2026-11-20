package moneytimeline

import "time"

type Response struct {
	Timezone   string    `json:"timezone"`
	Range      string    `json:"range"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Currencies []string  `json:"currencies"`
	Buckets    []Bucket  `json:"buckets"`
	// Previous is a single ungrouped total, omitted when comparable history
	// is unavailable.
	Previous *Series `json:"previous,omitempty"`
}

type Bucket struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Label  string    `json:"label"`
	Series []Series  `json:"series"`
}

type Series struct {
	Key           string      `json:"key"`
	Label         string      `json:"label"`
	Costs         []Cost      `json:"costs"`
	Tokens        *int64      `json:"tokens,omitempty"`
	TokenParts    *TokenParts `json:"token_parts,omitempty"`
	UnknownCost   int64       `json:"unknown_cost"`
	UnknownTokens int64       `json:"unknown_tokens"`
	Plan          int64       `json:"plan"`
}

// TokenParts contains the normalized components of known tokens. Input excludes cache reads.
type TokenParts struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

type Cost struct {
	Currency           string `json:"currency"`
	Micros             int64  `json:"micros"`
	Estimated          bool   `json:"estimated"`
	PlanEstimateMicros *int64 `json:"plan_estimate_micros,omitempty"`
}

package usagetap

import (
	jsonv2 "encoding/json/v2"
	"math"
	"net/http"
)

const TypeSafeSystemOne Protocol = "typesafe_systemone"

// IsTypeSafeSystemOneJSON matches only successful first-party classification responses.
func IsTypeSafeSystemOneJSON(upstream string, resp *http.Response) bool {
	return isOfficialJSON(upstream, resp, "api.typesafe.ai", "/v1/systemone")
}

func (p *Parser) typeSafe(data []byte) {
	var response struct {
		Model string `json:"model"`
		Usage struct {
			Input  *int64 `json:"input_tokens"`
			Output *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if jsonv2.Unmarshal(data, &response) != nil || response.Usage.Input == nil || response.Usage.Output == nil || *response.Usage.Input < 0 || *response.Usage.Output < 0 {
		p.MarkUnknown()
		return
	}
	u := emptyUsage()
	u.Model = safeModel(response.Model)
	u.Input, u.Output = *response.Usage.Input, *response.Usage.Output
	if u.Input <= math.MaxInt64-u.Output {
		u.Total = u.Input + u.Output
	}
	p.set(u)
}

package clibin

import (
	"encoding/json"
	"github.com/term-llm/term-llm/internal/llm/core"
	"os"
	"testing"
)

func TestClaudeGeneratedMCPConfigAllowsLongRunningTools(t *testing.T) {
	provider := newClaudeBridgeTestProvider(t)
	endpoint := prepareTestBridge(t, provider, core.Request{Tools: []core.ToolSpec{testMCPTool("spawn_agent")}})
	data, err := os.ReadFile(endpoint.configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Servers map[string]struct {
			Timeout int `json:"timeout"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if got := config.Servers["term-llm"].Timeout; got != claudeMCPToolTimeoutMillis {
		t.Fatalf("timeout = %d, want %d", got, claudeMCPToolTimeoutMillis)
	}
}

package mcp

import (
	"encoding/json"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// What tools/list may weigh (name, description and inputSchema of every tool, as JSON): every connected client pays it
// on each session, so it is pinned and only moves on purpose. Each is the measured size plus about 5%.
const (
	toolsListBudget      = 13950 // local client, all tools
	cloudToolsListBudget = 9990  // cloud client, the tools it may call
)

func toolsListWeight(t *testing.T, s *sdk.ClientSession) int {
	t.Helper()
	res, err := s.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, tl := range res.Tools {
		b, _ := json.Marshal(struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			InputSchema any    `json:"inputSchema"`
		}{tl.Name, tl.Description, tl.InputSchema})
		total += len(b)
		t.Logf("%-16s %5d", tl.Name, len(b))
	}
	t.Logf("total %d over %d tools", total, len(res.Tools))
	return total
}

func TestToolsListStaysLight(t *testing.T) {
	if total := toolsListWeight(t, newOpsRig(t).session(t)); total > toolsListBudget {
		t.Errorf("tools/list weighs %d bytes, the budget is %d", total, toolsListBudget)
	}
}

// the cloud list is cut from the same definitions, so it shrinks with them; this keeps it from growing on its own.
func TestCloudToolsListStaysLight(t *testing.T) {
	_, cloud, _, _ := cloudRig(t)
	if total := toolsListWeight(t, cloud); total > cloudToolsListBudget {
		t.Errorf("the cloud tools/list weighs %d bytes, the budget is %d", total, cloudToolsListBudget)
	}
}

package builtin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/apiclient"
	"github.com/PeterGuy326/mem/server/internal/tools"
)

func TestMemDurableContextRecallPinsContract(t *testing.T) {
	server := newFakeServer(
		`{
			"contract":"durable-context.v1",
			"principal":"alice",
			"hits":[{
				"memory":{"id":"`+lifecycleTestMemoryID+`","excerpt":"resumed"},
				"locator":"mem://memories/`+lifecycleTestMemoryID+`@3",
				"state_version":3,
				"provenance":{
					"workspace_id":"11111111-1111-4111-8111-111111111111",
					"source_type":"agent",
					"source_locator":{},
					"producer_agent":"codex"
				}
			}]
		}`,
		http.StatusOK,
		"application/json",
	)
	defer server.Close()
	registry := tools.New()
	if err := registerDurableContextRecall(registry, apiclient.New(server.URL, "token")); err != nil {
		t.Fatal(err)
	}

	output, err := registry.Call(context.Background(), "mem_durable_context_recall", map[string]any{
		"principal":   "alice",
		"session_ref": "session-2",
		"limit":       float64(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	if server.lastMethod != http.MethodPost || server.lastPath != "/v1/durable-context/recall" {
		t.Fatalf("request = %s %s", server.lastMethod, server.lastPath)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(server.lastBody), &body); err != nil {
		t.Fatal(err)
	}
	if body["contract"] != "durable-context.v1" ||
		body["principal"] != "alice" ||
		body["session_ref"] != "session-2" ||
		body["limit"] != float64(10) {
		t.Fatalf("body = %#v", body)
	}
	result, ok := output.(*apiclient.DurableContextRecallResult)
	if !ok || result.Contract != "durable-context.v1" || len(result.Hits) != 1 ||
		result.Hits[0].Locator != "mem://memories/"+lifecycleTestMemoryID+"@3" ||
		result.Hits[0].StateVersion != 3 {
		t.Fatalf("output = %#v", output)
	}
}

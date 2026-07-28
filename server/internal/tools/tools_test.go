package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestRegistry_RegisterAndCall(t *testing.T) {
	r := New()

	err := r.Register(Tool{
		Name:        "echo",
		Description: "echo args back",
		InputSchema: Schema{
			Type:     "object",
			Required: []string{"msg"},
			Properties: map[string]Property{
				"msg": {Type: "string"},
			},
		},
		Run: func(_ context.Context, args map[string]any) (any, error) {
			return map[string]any{"got": args["msg"]}, nil
		},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	out, err := r.Call(context.Background(), "echo", map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	got, _ := out.(map[string]any)
	if got["got"] != "hi" {
		t.Fatalf("want hi, got %v", got["got"])
	}
}

func TestRegistry_DuplicateRegistration(t *testing.T) {
	r := New()
	tool := Tool{Name: "x", Run: func(context.Context, map[string]any) (any, error) { return nil, nil }}
	if err := r.Register(tool); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(tool); err == nil {
		t.Fatal("expected duplicate-registration error")
	}
}

func TestRegistry_RejectsEmptyNameOrNilRun(t *testing.T) {
	r := New()
	if err := r.Register(Tool{}); err == nil {
		t.Fatal("expected empty-name error")
	}
	if err := r.Register(Tool{Name: "x"}); err == nil {
		t.Fatal("expected nil-Run error")
	}
}

func TestRegistry_UnknownTool(t *testing.T) {
	r := New()
	_, err := r.Call(context.Background(), "missing", nil)
	var ute *UnknownToolError
	if !errors.As(err, &ute) {
		t.Fatalf("want *UnknownToolError, got %T (%v)", err, err)
	}
	if ute.Name != "missing" {
		t.Fatalf("want missing, got %q", ute.Name)
	}
}

func TestRegistry_ListIsSortedAndStable(t *testing.T) {
	r := New()
	for _, n := range []string{"banana", "apple", "cherry"} {
		_ = r.Register(Tool{Name: n, Run: func(context.Context, map[string]any) (any, error) { return nil, nil }})
	}
	out := r.List()
	want := []string{"apple", "banana", "cherry"}
	for i, t2 := range out {
		if t2.Name != want[i] {
			t.Fatalf("at %d: got %s want %s", i, t2.Name, want[i])
		}
	}
}

func TestRegistry_CallPassesContextCancellation(t *testing.T) {
	r := New()
	_ = r.Register(Tool{
		Name: "ctx",
		Run: func(ctx context.Context, _ map[string]any) (any, error) {
			return nil, ctx.Err()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Call(ctx, "ctx", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestSchemaMarshalsRecursiveObjectAndArrayProperties(t *testing.T) {
	noAdditionalProperties := false
	minimum := 1
	maximum := 200
	schema := Schema{
		Type:                 "object",
		Required:             []string{"handoff"},
		AdditionalProperties: &noAdditionalProperties,
		Properties: map[string]Property{
			"handoff": {
				Type:                 "object",
				Required:             []string{"schema_version", "state"},
				AdditionalProperties: &noAdditionalProperties,
				Properties: map[string]Property{
					"schema_version": {Type: "integer", Const: 1},
					"sequence": {
						Type:    "integer",
						Minimum: &minimum,
						Maximum: &maximum,
					},
					"state": {
						Type:     "object",
						Required: []string{"completed"},
						Properties: map[string]Property{
							"completed": {
								Type:     "array",
								MaxItems: 100,
								Items:    &Property{Type: "string", MinLength: 1},
							},
						},
					},
					"base_checkpoint_id": {
						Type:   []string{"string", "null"},
						Format: "uuid",
					},
				},
			},
		},
	}

	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	properties := got["properties"].(map[string]any)
	handoff := properties["handoff"].(map[string]any)
	handoffProperties := handoff["properties"].(map[string]any)
	if handoffProperties["schema_version"].(map[string]any)["const"] != float64(1) {
		t.Fatalf("recursive const missing: %s", encoded)
	}
	sequence := handoffProperties["sequence"].(map[string]any)
	if sequence["minimum"] != float64(1) || sequence["maximum"] != float64(200) {
		t.Fatalf("numeric constraints missing: %s", encoded)
	}
	state := handoffProperties["state"].(map[string]any)
	completed := state["properties"].(map[string]any)["completed"].(map[string]any)
	if completed["items"].(map[string]any)["minLength"] != float64(1) {
		t.Fatalf("recursive array item constraints missing: %s", encoded)
	}
	nullable := handoffProperties["base_checkpoint_id"].(map[string]any)["type"].([]any)
	if len(nullable) != 2 || nullable[1] != "null" {
		t.Fatalf("nullable type missing: %s", encoded)
	}
	if got["additionalProperties"] != false || handoff["additionalProperties"] != false {
		t.Fatalf("additionalProperties constraints missing: %s", encoded)
	}
}

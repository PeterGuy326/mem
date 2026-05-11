package tools

import (
	"context"
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

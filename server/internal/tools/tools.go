// Package tools defines the Agent-facing registry exposed by mem-mcp.
//
// Why a registry? SPEC §0 promises "CLI + MCP + API 三位一体". The HTTP API
// is the canonical surface; CLI and MCP are alternate frontends. MCP tools
// register here, while the Cobra CLI declares its own commands. Both adapters
// must call the same typed apiclient methods, and parity tests must cover each
// public capability across those explicit surfaces.
//
// A Tool is intentionally minimal: a name, a description, a JSON Schema for
// inputs, and a Run function that returns a serializable result. The schema
// doubles as MCP `input_schema` (SPEC §8) and as the contract that argument
// converters use to keep CLI flags and MCP JSON in lockstep.
package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Tool is one registerable capability. Implementations should be tiny: a
// thin call into the apiclient with light input normalization.
//
// Name MUST be MCP-friendly (snake_case, no spaces) — SPEC §8 expects
// `mem_search`, `mem_put`, etc.
type Tool struct {
	// Name is the canonical identifier. MCP exposes this verbatim;
	// CLI may map it to a different command name if it preserves
	// the input shape.
	Name string

	// Description is human + agent readable; surfaced as MCP tool
	// description and as the CLI subcommand short help.
	Description string

	// InputSchema is a JSON Schema (draft 2020-12 subset) describing
	// the accepted arguments. It is used by the MCP `tools/list` response
	// and LLM-side function-call schema generation. CLI validation is explicit
	// in the corresponding Cobra command and shares the typed apiclient call.
	InputSchema Schema

	// Run executes the tool against memd. The args map matches
	// InputSchema. Implementations should treat ctx cancellation as a
	// hard stop and return ctx.Err() promptly.
	Run func(ctx context.Context, args map[string]any) (any, error)
}

// Schema is a JSON-Schema-compatible shape. We intentionally type the
// commonly-used parts and leave room for raw extras under Extra so unusual
// shapes (e.g. `oneOf`) remain expressible.
type Schema struct {
	Type                 string              `json:"type"`
	Properties           map[string]Property `json:"properties,omitempty"`
	Required             []string            `json:"required,omitempty"`
	Description          string              `json:"description,omitempty"`
	AdditionalProperties *bool               `json:"additionalProperties,omitempty"`
	Extra                map[string]any      `json:"-"`
}

// Property is one field of a Schema.
type Property struct {
	// Type is usually a JSON Schema type string. It is any so nullable fields
	// can use the standard union form, for example []string{"string", "null"}.
	Type                 any                 `json:"type,omitempty"`
	Description          string              `json:"description,omitempty"`
	Enum                 []string            `json:"enum,omitempty"`
	Const                any                 `json:"const,omitempty"`
	Default              any                 `json:"default,omitempty"`
	Format               string              `json:"format,omitempty"`
	Pattern              string              `json:"pattern,omitempty"`
	MinLength            int                 `json:"minLength,omitempty"`
	MaxLength            int                 `json:"maxLength,omitempty"`
	Minimum              *int                `json:"minimum,omitempty"`
	Maximum              *int                `json:"maximum,omitempty"`
	MaxItems             int                 `json:"maxItems,omitempty"`
	Properties           map[string]Property `json:"properties,omitempty"`
	Required             []string            `json:"required,omitempty"`
	Items                *Property           `json:"items,omitempty"`
	AdditionalProperties *bool               `json:"additionalProperties,omitempty"`
}

// Registry is a goroutine-safe, append-only map of Tool definitions.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Default returns the package-level default Registry. Builtin tools
// register here in init(); callers (mem-mcp, future SDK wrappers) read from
// it. Tests should construct their own via New() to stay isolated.
var defaultRegistry = New()

func Default() *Registry { return defaultRegistry }

// Register adds a Tool. Duplicate names return an error rather than panic
// so a misconfigured plugin doesn't crash the process.
func (r *Registry) Register(t Tool) error {
	if t.Name == "" {
		return errors.New("tools: empty Name")
	}
	if t.Run == nil {
		return fmt.Errorf("tools: %s: nil Run", t.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.tools[t.Name]; dup {
		return fmt.Errorf("tools: %s already registered", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// MustRegister panics on registration failure. Suitable for package-level
// init() in builtin/.
func (r *Registry) MustRegister(t Tool) {
	if err := r.Register(t); err != nil {
		panic(err)
	}
}

// Get returns a Tool by name. The second return is false if not found.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all Tools sorted by Name (stable for MCP tools/list output).
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Call resolves a name and invokes it. Returns ErrUnknownTool if the name
// isn't registered.
func (r *Registry) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	t, ok := r.Get(name)
	if !ok {
		return nil, &UnknownToolError{Name: name}
	}
	if args == nil {
		args = map[string]any{}
	}
	return t.Run(ctx, args)
}

// UnknownToolError is returned by Call when the name isn't registered.
type UnknownToolError struct{ Name string }

func (e *UnknownToolError) Error() string { return "tools: unknown tool: " + e.Name }

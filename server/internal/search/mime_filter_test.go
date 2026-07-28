package search

import (
	"reflect"
	"strings"
	"testing"
)

func TestAppendMIMEFilterProductCategories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		kind       string
		wantWhere  string
		wantArg    any
		wantArgLen int
	}{
		{name: "empty", kind: "", wantArgLen: 1},
		{name: "any", kind: "any", wantArgLen: 1},
		{
			name: "image", kind: "image", wantWhere: "f.mime LIKE $2",
			wantArg: "image/%", wantArgLen: 2,
		},
		{
			name: "exact mime", kind: "application/pdf", wantWhere: "f.mime = $2",
			wantArg: "application/pdf", wantArgLen: 2,
		},
		{
			name: "document alias", kind: "doc", wantWhere: "f.mime LIKE ANY($2::text[])",
			wantArgLen: 2,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			args, where := appendMIMEFilter(
				[]any{"existing"},
				[]string{"f.user_id = $1"},
				test.kind,
			)
			if len(args) != test.wantArgLen {
				t.Fatalf("args = %#v", args)
			}
			if test.wantWhere != "" &&
				!strings.Contains(strings.Join(where, " "), test.wantWhere) {
				t.Fatalf("where = %#v, want %q", where, test.wantWhere)
			}
			if test.wantArg != nil && !reflect.DeepEqual(args[len(args)-1], test.wantArg) {
				t.Fatalf("last arg = %#v, want %#v", args[len(args)-1], test.wantArg)
			}
			if test.kind == "doc" {
				patterns, ok := args[len(args)-1].([]string)
				if !ok || len(patterns) < 4 ||
					patterns[0] != "text/%" ||
					patterns[1] != "application/pdf" {
					t.Fatalf("document patterns = %#v", args[len(args)-1])
				}
			}
		})
	}
}

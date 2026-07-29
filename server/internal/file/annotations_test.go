package file

import (
	"errors"
	"reflect"
	"testing"
)

func TestAnnotationTransition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		status          string
		stateVersion    int64
		decision        string
		expectedVersion int64
		wantReplayed    bool
		wantErr         error
	}{
		{
			name:            "accept pending",
			status:          AnnotationStatusPending,
			stateVersion:    3,
			decision:        AnnotationStatusAccepted,
			expectedVersion: 3,
		},
		{
			name:            "reject pending",
			status:          AnnotationStatusPending,
			stateVersion:    2,
			decision:        AnnotationStatusRejected,
			expectedVersion: 2,
		},
		{
			name:            "stale pending version",
			status:          AnnotationStatusPending,
			stateVersion:    3,
			decision:        AnnotationStatusAccepted,
			expectedVersion: 2,
			wantErr:         ErrAnnotationVersionConflict,
		},
		{
			name:            "same accepted decision replays despite stale version",
			status:          AnnotationStatusAccepted,
			stateVersion:    4,
			decision:        AnnotationStatusAccepted,
			expectedVersion: 1,
			wantReplayed:    true,
		},
		{
			name:            "same rejected decision replays",
			status:          AnnotationStatusRejected,
			stateVersion:    2,
			decision:        AnnotationStatusRejected,
			expectedVersion: 2,
			wantReplayed:    true,
		},
		{
			name:            "opposite terminal decision conflicts",
			status:          AnnotationStatusAccepted,
			stateVersion:    2,
			decision:        AnnotationStatusRejected,
			expectedVersion: 2,
			wantErr:         ErrAnnotationDecisionConflict,
		},
		{
			name:            "superseded annotation conflicts",
			status:          AnnotationStatusSuperseded,
			stateVersion:    5,
			decision:        AnnotationStatusAccepted,
			expectedVersion: 5,
			wantErr:         ErrAnnotationDecisionConflict,
		},
		{
			name:            "invalid decision",
			status:          AnnotationStatusPending,
			stateVersion:    1,
			decision:        "approve",
			expectedVersion: 1,
			wantErr:         ErrInvalidAnnotationDecision,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			replayed, err := annotationTransition(
				test.status,
				test.stateVersion,
				test.decision,
				test.expectedVersion,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if replayed != test.wantReplayed {
				t.Fatalf("replayed = %v, want %v", replayed, test.wantReplayed)
			}
		})
	}
}

func TestMergeUniqueTagsPreservesUserOrder(t *testing.T) {
	t.Parallel()

	got := mergeUniqueTags(
		[]string{"manual", "shared", ""},
		[]string{"ai-one", "shared", "ai-two", ""},
	)
	want := []string{"manual", "shared", "", "ai-one", "ai-two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeUniqueTags() = %#v, want %#v", got, want)
	}

	if got := mergeUniqueTags(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("empty merge = %#v, want non-nil empty slice", got)
	}
}

func TestAnnotationPathAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		candidate    string
		allowedPaths []string
		want         bool
	}{
		{"unrestricted", "/Private/note.txt", nil, true},
		{"root", "/Private/note.txt", []string{"/"}, true},
		{"self", "/Projects/mem", []string{"/Projects/mem"}, true},
		{"descendant", "/Projects/mem/spec.md", []string{"/Projects"}, true},
		{"segment boundary", "/Projects-old/spec.md", []string{"/Projects"}, false},
		{"outside", "/Private/spec.md", []string{"/Projects"}, false},
		{"invalid candidate fails closed", "relative", []string{"/Projects"}, false},
		{"invalid allowed path ignored", "/Projects/spec.md", []string{"relative"}, false},
		{"empty restriction ignored", "/Projects/spec.md", []string{""}, false},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := annotationPathAllowed(test.candidate, test.allowedPaths); got != test.want {
				t.Fatalf(
					"annotationPathAllowed(%q, %#v) = %v, want %v",
					test.candidate,
					test.allowedPaths,
					got,
					test.want,
				)
			}
		})
	}
}

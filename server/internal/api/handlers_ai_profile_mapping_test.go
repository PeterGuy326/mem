package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/aiprofile"
)

func TestWriteAIProfileErrorMapsSelectionConflictsToStableCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
	}{
		{
			name: "indexing in flight",
			err:  aiprofile.ErrProfileIndexingInFlight,
			code: "ai_profile_indexing_in_flight",
		},
		{
			name: "unknown corpus identity requires a generation",
			err:  aiprofile.ErrProfileCorpusIdentityUnknown,
			code: "ai_profile_generation_required",
		},
		{
			name: "different corpus provider requires a generation",
			err:  aiprofile.ErrProfileCorpusMismatch,
			code: "ai_profile_generation_required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeAIProfileError(recorder, fmt.Errorf("selection detail: %w", test.err))

			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusConflict, recorder.Body.String())
			}
			var response struct {
				Error string `json:"error"`
				Hint  string `json:"hint"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Error != test.code {
				t.Fatalf("error code = %q, want %q", response.Error, test.code)
			}
			if response.Hint == "" {
				t.Fatal("conflict response omitted its recovery hint")
			}
		})
	}
}

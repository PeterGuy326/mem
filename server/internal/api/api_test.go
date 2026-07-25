package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PeterGuy326/mem/server/internal/auth"
)

func TestRegisterDisabledReturnsClearError(t *testing.T) {
	s := &Server{Auth: auth.New(nil), RegistrationMode: "disabled", Log: slog.Default()}
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/register", strings.NewReader(`{"email":"user@example.com","password":"secret1"}`))
	rec := httptest.NewRecorder()

	s.handleRegister(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "registration_disabled") {
		t.Fatalf("body lacks clear registration error: %s", rec.Body.String())
	}
}

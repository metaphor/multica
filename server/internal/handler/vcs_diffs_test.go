package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	gl "github.com/multica-ai/multica/server/internal/integrations/gitlab"
)

// TestDiffFetchErrorStatus pins the sentinel -> status mapping for errors
// coming back from the GitLab diffs client.
func TestDiffFetchErrorStatus(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "not found", err: gl.ErrNotFound, wantStatus: http.StatusNotFound},
		{name: "unauthorized", err: gl.ErrUnauthorized, wantStatus: http.StatusBadGateway},
		{name: "forbidden", err: gl.ErrForbidden, wantStatus: http.StatusBadGateway},
		{name: "gitlab 5xx", err: &gl.ErrServer{StatusCode: 500, Body: "boom"}, wantStatus: http.StatusBadGateway},
		{name: "deadline exceeded", err: fmt.Errorf("gitlab: %w", context.DeadlineExceeded), wantStatus: http.StatusGatewayTimeout},
		{name: "unknown", err: errors.New("boom"), wantStatus: http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := diffFetchErrorStatus(tc.err)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
		})
	}
}

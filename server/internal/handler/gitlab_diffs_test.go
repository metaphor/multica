package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	gl "github.com/multica-ai/multica/server/internal/integrations/gitlab"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestResolveGitLabDiffTarget pins the DB-free guard branches of
// resolveGitLabDiffTarget: the provider gate, the NULL connection gate, and
// the happy path that hands back the connection ID.
func TestResolveGitLabDiffTarget(t *testing.T) {
	connID := pgtype.UUID{Bytes: [16]byte{0x01}, Valid: true}

	t.Run("github provider is rejected", func(t *testing.T) {
		row := db.GithubPullRequest{Provider: "github", ConnectionID: connID}
		if _, err := resolveGitLabDiffTarget(row); !errors.Is(err, errProviderNotSupported) {
			t.Fatalf("err = %v, want errProviderNotSupported", err)
		}
	})

	t.Run("null connection is rejected", func(t *testing.T) {
		row := db.GithubPullRequest{Provider: "gitlab"}
		if _, err := resolveGitLabDiffTarget(row); !errors.Is(err, errMRConnectionUnresolved) {
			t.Fatalf("err = %v, want errMRConnectionUnresolved", err)
		}
	})

	t.Run("gitlab row with connection resolves", func(t *testing.T) {
		row := db.GithubPullRequest{Provider: "gitlab", ConnectionID: connID}
		got, err := resolveGitLabDiffTarget(row)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != connID {
			t.Fatalf("connection id = %v, want %v", got, connID)
		}
	})
}

// TestWriteDiffTargetError proves the pinned HTTP status and body for each
// guard sentinel, so the 400/409 contract holds without a live stack.
func TestWriteDiffTargetError(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{name: "provider not supported", err: errProviderNotSupported, wantStatus: http.StatusBadRequest, wantBody: "{\"error\":\"provider_not_supported\"}\n"},
		{name: "connection unresolved", err: errMRConnectionUnresolved, wantStatus: http.StatusConflict, wantBody: "{\"error\":\"mr_connection_unresolved\"}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeDiffTargetError(w, tc.err)
			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantStatus)
			}
			if got := w.Body.String(); got != tc.wantBody {
				t.Fatalf("body = %q, want %q", got, tc.wantBody)
			}
		})
	}
}

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

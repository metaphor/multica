package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/diff"
)

// writeDiffsPage writes a JSON array of n GitLab diff entries whose
// new_path values are file-000.go, file-001.go, ... starting at offset.
func writeDiffsPage(w io.Writer, offset, n int) {
	_, _ = io.WriteString(w, "[")
	for i := 0; i < n; i++ {
		if i > 0 {
			_, _ = io.WriteString(w, ",")
		}
		_, _ = fmt.Fprintf(w, `{"old_path":"file-%03d.go","new_path":"file-%03d.go","diff":"@@ patch %03d"}`,
			offset+i, offset+i, offset+i)
	}
	_, _ = io.WriteString(w, "]")
}

func TestListMergeRequestDiffs_Pagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/api/v4/projects/owner%2Fname/merge_requests/7/diffs"
		if r.URL.EscapedPath() != wantPath {
			t.Errorf("path = %s, want %s", r.URL.EscapedPath(), wantPath)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Errorf("per_page = %q, want 100", got)
		}
		if tok := r.Header.Get("PRIVATE-TOKEN"); tok != "tok" {
			t.Errorf("PRIVATE-TOKEN = %q, want tok", tok)
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			writeDiffsPage(w, 0, 100)
		case "2":
			writeDiffsPage(w, 100, 2)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	files, err := c.ListMergeRequestDiffs(context.Background(), "owner/name", 7)
	if err != nil {
		t.Fatalf("ListMergeRequestDiffs: %v", err)
	}

	if len(files) != 102 {
		t.Fatalf("len(files) = %d, want 102", len(files))
	}
	for i, f := range files {
		want := fmt.Sprintf("file-%03d.go", i)
		if f.NewPath != want {
			t.Errorf("files[%d].NewPath = %q, want %q", i, f.NewPath, want)
		}
		if f.Status != diff.StatusModified {
			t.Errorf("files[%d].Status = %q, want modified", i, f.Status)
		}
	}
}

func TestListMergeRequestDiffs_StatusMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"old_path":"a.go","new_path":"a.go","diff":"@@ add","new_file":true},
			{"old_path":"b.go","new_path":"b.go","diff":"@@ del","deleted_file":true},
			{"old_path":"c.go","new_path":"d.go","diff":"","renamed_file":true},
			{"old_path":"e.go","new_path":"e.go","diff":"@@ mod","generated_file":true,"collapsed":true,"too_large":true}
		]`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	files, err := c.ListMergeRequestDiffs(context.Background(), "owner/name", 1)
	if err != nil {
		t.Fatalf("ListMergeRequestDiffs: %v", err)
	}
	if len(files) != 4 {
		t.Fatalf("len(files) = %d, want 4", len(files))
	}

	wantStatuses := []string{
		diff.StatusAdded,
		diff.StatusDeleted,
		diff.StatusRenamed,
		diff.StatusModified,
	}
	for i, want := range wantStatuses {
		if files[i].Status != want {
			t.Errorf("files[%d].Status = %q, want %q", i, files[i].Status, want)
		}
	}

	if files[2].OldPath != "c.go" || files[2].NewPath != "d.go" {
		t.Errorf("renamed paths = %q -> %q, want c.go -> d.go", files[2].OldPath, files[2].NewPath)
	}
	if files[2].Patch != "" {
		t.Errorf("renamed patch = %q, want empty", files[2].Patch)
	}
	last := files[3]
	if !last.IsGenerated || !last.Collapsed || !last.TooLarge {
		t.Errorf("flags = generated:%v collapsed:%v tooLarge:%v, want all true",
			last.IsGenerated, last.Collapsed, last.TooLarge)
	}
	if files[0].Patch != "@@ add" {
		t.Errorf("added patch = %q, want @@ add", files[0].Patch)
	}
}

func TestListMergeRequestDiffs_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"401 Unauthorized"}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	_, err := c.ListMergeRequestDiffs(context.Background(), "owner/name", 1)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("error = %v, want ErrUnauthorized", err)
	}
}

func TestListMergeRequestDiffs_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"404 Not found"}`)
	}))
	t.Cleanup(srv.Close)

	c := clientForFake(t, srv, "tok")
	_, err := c.ListMergeRequestDiffs(context.Background(), "owner/name", 99)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

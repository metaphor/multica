package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/multica-ai/multica/server/internal/integrations/diff"
)

// diffsPerPage is the page size used when paginating merge request diffs.
const diffsPerPage = 100

// gitLabDiff mirrors one entry of the GitLab
// GET /projects/:id/merge_requests/:merge_request_iid/diffs response.
type gitLabDiff struct {
	OldPath       string `json:"old_path"`
	NewPath       string `json:"new_path"`
	Diff          string `json:"diff"`
	NewFile       bool   `json:"new_file"`
	RenamedFile   bool   `json:"renamed_file"`
	DeletedFile   bool   `json:"deleted_file"`
	GeneratedFile bool   `json:"generated_file"`
	Collapsed     bool   `json:"collapsed"`
	TooLarge      bool   `json:"too_large"`
}

// ListMergeRequestDiffs returns every file changed by a merge request,
// paginating through the GitLab diffs endpoint until a short page is
// returned.
//
// projectPath is the full project path (e.g. "owner/name"); it is
// URL-escaped as required by the GitLab API.
func (c *Client) ListMergeRequestDiffs(ctx context.Context, projectPath string, mrIID int64) ([]diff.ChangedFile, error) {
	escaped := url.PathEscape(projectPath)
	files := make([]diff.ChangedFile, 0, diffsPerPage)

	for page := 1; ; page++ {
		path := fmt.Sprintf("/projects/%s/merge_requests/%d/diffs?per_page=%d&page=%d",
			escaped, mrIID, diffsPerPage, page)

		resp, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}

		if err := c.mapError(resp); err != nil {
			resp.Body.Close()
			return nil, err
		}

		var entries []gitLabDiff
		if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("gitlab: decode merge request diffs: %w", err)
		}
		resp.Body.Close()

		for _, e := range entries {
			files = append(files, toChangedFile(e))
		}
		if len(entries) < diffsPerPage {
			return files, nil
		}
	}
}

// toChangedFile maps a GitLab diff entry onto the provider-neutral model.
// The flag precedence follows the GitLab contract: a file is added when
// new_file, deleted when deleted_file, renamed when renamed_file, and
// modified otherwise.
func toChangedFile(e gitLabDiff) diff.ChangedFile {
	status := diff.StatusModified
	switch {
	case e.NewFile:
		status = diff.StatusAdded
	case e.DeletedFile:
		status = diff.StatusDeleted
	case e.RenamedFile:
		status = diff.StatusRenamed
	}
	return diff.ChangedFile{
		OldPath:     e.OldPath,
		NewPath:     e.NewPath,
		Status:      status,
		Patch:       e.Diff,
		IsGenerated: e.GeneratedFile,
		Collapsed:   e.Collapsed,
		TooLarge:    e.TooLarge,
	}
}

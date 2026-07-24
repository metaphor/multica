// Package diff defines the provider-neutral model for merge/pull request
// file diffs served by the API.
package diff

// File change statuses for ChangedFile.Status.
const (
	StatusAdded    = "added"
	StatusModified = "modified"
	StatusDeleted  = "deleted"
	StatusRenamed  = "renamed"
)

// ChangedFile is the provider-neutral representation of a single file
// changed in a merge request.
type ChangedFile struct {
	OldPath     string `json:"oldPath"`
	NewPath     string `json:"newPath"`
	Status      string `json:"status"`
	Patch       string `json:"patch"`
	IsGenerated bool   `json:"isGenerated"`
	Collapsed   bool   `json:"collapsed"`
	TooLarge    bool   `json:"tooLarge"`
}

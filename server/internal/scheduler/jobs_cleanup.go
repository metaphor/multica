package scheduler

import (
	"context"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// JobNameCleanStalePendingCheckSuites is the canonical name used in audit
// rows. Stable across releases — do not rename without a migration.
const JobNameCleanStalePendingCheckSuites = "clean_stale_pending_check_suites"

// CleanStalePendingCheckSuitesJob returns a JobSpec that purges pending
// check_suite / pipeline stash rows older than 7 days, across both GitHub
// and GitLab providers. Covers the pr_number=0 fallback rows from
// DrainPendingGitLabPipelinesForMR that were never matched to a real MR.
//
// Cadence / run-time settings are deliberately wide: the operation is a
// simple DELETE with a NOW() predicate, the table is bounded by event TTL,
// and the overhead of running it hourly with a 10-second timeout is
// negligible even at scale. There is no retry budget — if the sweeper
// fails, the next tick cleans the same window (now()-7d moves forward).
func CleanStalePendingCheckSuitesJob(queries *db.Queries) JobSpec {
	return JobSpec{
		Name:              JobNameCleanStalePendingCheckSuites,
		Cadence:           1 * time.Hour,
		ScheduleDelay:     0,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     25 * time.Hour,
		RunTimeout:        10 * time.Second,
		StaleTimeout:      30 * time.Second,
		HeartbeatInterval: 10 * time.Second,
		AllowStaleReentry: false,
		MaxAttempts:       1,
		RetryBackoff:      nil,
		Scopes:            StaticScopes(ScopeGlobal),
		Handler:           cleanStalePendingCheckSuitesHandler(queries),
	}
}

func cleanStalePendingCheckSuitesHandler(queries *db.Queries) Handler {
	return func(ctx context.Context, _ HandlerInput) (HandlerResult, error) {
		if err := queries.DeleteStalePendingCheckSuites(ctx); err != nil {
			return HandlerResult{}, err
		}
		return HandlerResult{RowsAffected: 0}, nil
	}
}

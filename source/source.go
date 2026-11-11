// Package source defines where work comes from. It is the second seam from the
// design plan.
//
// Implementations live in subpackages: source/cloud (Palate cloud, M1) and
// later source/offlinepos. Everything below this interface (queue, ladder,
// render, printer writers) must not import net/http; arch_test.go checks it.
package source

import (
	"context"

	"palate-print-agent/job"
)

// JobSource is where work comes from. One implementation today (Palate cloud);
// the offline POS is the second, and adding it changes one line.
type JobSource interface {
	// Fetch blocks until at least one job is available, the source's hold
	// time passes (returning no jobs and no error), or ctx is done.
	Fetch(ctx context.Context) ([]job.Job, error)

	// Report records a job's outcome. Reporting the same job twice is safe:
	// the source treats a repeat as a no-op.
	Report(ctx context.Context, jobID string, outcome job.Outcome) error
}

// Package jobs runs Core's background work on River, a PostgreSQL-backed queue (docs/design/05
// section 3). Any replica may run any job; periodic jobs are leader-elected by River, so two
// replicas do not double-run them. Jobs touching user data go through the store's per-user
// transaction, exactly like requests do.
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/Niboor/notekeeper/core/internal/blobs"
	"github.com/Niboor/notekeeper/core/internal/store"
)

// Retention periods (docs/design/01-data-model.md section 13).
const (
	retainChanges       = 30 * 24 * time.Hour
	retainIngestEvents  = 90 * 24 * time.Hour
	retainIdempotency   = 24 * time.Hour
	retainSessions      = 30 * 24 * time.Hour
	retainNotifications = 90 * 24 * time.Hour
	retainUnlinkedNotes = 24 * time.Hour
	retainThrottle      = time.Hour
)

// Deps are what workers need.
type Deps struct {
	Store *store.Store
	Blobs *blobs.Service
	Log   *slog.Logger
	Now   func() time.Time
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// PurgeArgs is the daily housekeeping job: it deletes what the retention table says is old.
type PurgeArgs struct{}

// Kind identifies the job type.
func (PurgeArgs) Kind() string { return "purge" }

// PurgeWorker performs PurgeArgs.
type PurgeWorker struct {
	river.WorkerDefaults[PurgeArgs]
	D Deps
}

// Work deletes expired rows in bounded steps. Everything here is idempotent.
func (w *PurgeWorker) Work(ctx context.Context, _ *river.Job[PurgeArgs]) error {
	return Purge(ctx, w.D)
}

// Purge applies the retention rules.
func Purge(ctx context.Context, d Deps) error {
	now := d.now()
	q := d.Store.Q()
	cutoff := now.Add(-retainSessions)
	if _, err := q.DeleteOldSessions(ctx, &cutoff); err != nil {
		return err
	}
	if _, err := q.PurgeThrottle(ctx, now.Add(-retainThrottle)); err != nil {
		return err
	}
	if _, err := q.PurgeIdempotency(ctx, now.Add(-retainIdempotency)); err != nil {
		return err
	}
	if _, err := q.PurgeIngestEvents(ctx, now.Add(-retainIngestEvents)); err != nil {
		return err
	}
	if _, err := q.PurgeUnlinkedSenders(ctx, now.Add(-retainUnlinkedNotes)); err != nil {
		return err
	}
	// Content tables are under row-level security, so they are purged one user at a time.
	users, err := q.ListUserIDs(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		err := d.Store.InUserTx(ctx, u, func(tx *store.UserTx) error {
			if _, err := tx.Q.PurgeChanges(ctx, dbqPurgeChanges(u, now.Add(-retainChanges))); err != nil {
				return err
			}
			_, err := tx.Q.PurgeReadNotifications(ctx, dbqPurgeNotifications(u, now.Add(-retainNotifications)))
			return err
		})
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

// ReleaseStaleUploadsArgs is the janitor for uploads: it frees the quota held by uploads that
// never finished and deletes attachments no note ever used, both older than an hour.
type ReleaseStaleUploadsArgs struct{}

// Kind identifies the job type.
func (ReleaseStaleUploadsArgs) Kind() string { return "release_stale_uploads" }

// StaleUploadAge is how old an unfinished or unused upload must be before it is removed.
const StaleUploadAge = time.Hour

// ReleaseStaleUploadsWorker performs ReleaseStaleUploadsArgs.
type ReleaseStaleUploadsWorker struct {
	river.WorkerDefaults[ReleaseStaleUploadsArgs]
	D Deps
}

// Work runs the janitor for every user.
func (w *ReleaseStaleUploadsWorker) Work(ctx context.Context, _ *river.Job[ReleaseStaleUploadsArgs]) error {
	return ReleaseStaleUploads(ctx, w.D)
}

// ReleaseStaleUploads is the body of the janitor job.
func ReleaseStaleUploads(ctx context.Context, d Deps) error {
	users, err := d.Store.Q().ListUserIDs(ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		if err := d.Blobs.ReleaseStale(ctx, u, StaleUploadAge); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}

// New builds the River client. Call Start on it in the serving process; tests and insert-only
// callers can use it without starting workers.
func New(pool *pgxpool.Pool, d Deps) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &PurgeWorker{D: d})
	river.AddWorker(workers, &ReleaseStaleUploadsWorker{D: d})
	return river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 5}},
		Workers: workers,
		Logger:  d.Log,
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(24*time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return PurgeArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(10*time.Minute),
				func() (river.JobArgs, *river.InsertOpts) { return ReleaseStaleUploadsArgs{}, nil },
				nil),
		},
	})
}

// Package jobs runs Core's background work on River, a PostgreSQL-backed queue (docs/design/05
// section 3). Any replica may run any job; periodic jobs are leader-elected by River, so two
// replicas do not double-run them. Jobs touching user data go through the store's per-user
// transaction, exactly like requests do.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/Niboor/notekeeper/core/internal/accounts"
	"github.com/Niboor/notekeeper/core/internal/blobs"
	"github.com/Niboor/notekeeper/core/internal/obs"
	"github.com/Niboor/notekeeper/core/internal/outbox"
	"github.com/Niboor/notekeeper/core/internal/reminders"
	"github.com/Niboor/notekeeper/core/internal/shares"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

// Retention periods (docs/design/01-data-model.md section 13).
const (
	retainChanges       = 30 * 24 * time.Hour
	retainIngestEvents  = 90 * 24 * time.Hour
	retainIdempotency   = 24 * time.Hour
	retainSessions      = 30 * 24 * time.Hour
	retainNotifications = 90 * 24 * time.Hour // read ones
	retainAnyNotice     = 365 * 24 * time.Hour
	// Finished outbox items are kept long enough to answer "!snooze" or "!done" on an old reminder
	// message; their text is blanked when they finish (CR-021).
	retainOutbox        = 30 * 24 * time.Hour
	retainUnlinkedNotes = 24 * time.Hour
	retainThrottle      = time.Hour
)

// failed counts a failing job run, for alerts (SEC-AUD-3, NFR-O2).
func failed(job string, err error) error {
	if err != nil {
		obs.JobFailures.WithLabelValues(job).Inc()
	}
	return err
}

// Deps are what workers need.
type Deps struct {
	Store     *store.Store
	Blobs     *blobs.Service
	Outbox    *outbox.Service
	Shares    *shares.Service
	Reminders *reminders.Service
	Accounts  *accounts.Service
	Log       *slog.Logger
	Now       func() time.Time
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
	return failed("purge", Purge(ctx, w.D))
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
	for { // in bounded batches
		cut := now.Add(-retainOutbox)
		n, err := q.PurgeOutbox(ctx, &cut)
		if err != nil {
			return err
		}
		if n < 5000 {
			break
		}
	}
	// Content tables are under row-level security, so they are purged one user at a time.
	users, err := q.ListUserIDs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, u := range users { // one failing account must not stop the sweep for everybody (CR-024)
		err := d.Store.InUserTx(ctx, u, func(tx *store.UserTx) error {
			if _, err := tx.Q.PurgeChanges(ctx, dbqPurgeChanges(u, now.Add(-retainChanges))); err != nil {
				return err
			}
			if _, err := tx.Q.PurgeOldNotifications(ctx, dbq.PurgeOldNotificationsParams{UserID: u, CreatedAt: now.Add(-retainAnyNotice)}); err != nil {
				return err
			}
			_, err := tx.Q.PurgeReadNotifications(ctx, dbqPurgeNotifications(u, now.Add(-retainNotifications)))
			return err
		})
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			errs = append(errs, fmt.Errorf("user %s: %w", u, err))
		}
	}
	return errors.Join(errs...)
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
	return failed("release_stale_uploads", ReleaseStaleUploads(ctx, w.D))
}

// ReleaseStaleUploads is the body of the janitor job.
func ReleaseStaleUploads(ctx context.Context, d Deps) error {
	users, err := d.Store.Q().ListUserIDs(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, u := range users {
		if err := d.Blobs.ReleaseStale(ctx, u, StaleUploadAge); err != nil && !errors.Is(err, store.ErrNotFound) {
			errs = append(errs, fmt.Errorf("user %s: %w", u, err))
		}
	}
	return errors.Join(errs...)
}

// ExpireOutboxArgs gives up on outbox items that waited too long and tells their users.
type ExpireOutboxArgs struct{}

// Kind identifies the job type.
func (ExpireOutboxArgs) Kind() string { return "expire_outbox" }

// ExpireOutboxWorker performs ExpireOutboxArgs.
type ExpireOutboxWorker struct {
	river.WorkerDefaults[ExpireOutboxArgs]
	D Deps
}

// Work expires stale items.
func (w *ExpireOutboxWorker) Work(ctx context.Context, _ *river.Job[ExpireOutboxArgs]) error {
	return failed("expire_outbox", w.D.Outbox.Expire(ctx))
}

// DeleteUsersArgs removes the data of accounts that are being deleted (AUTH-U9).
type DeleteUsersArgs struct{}

// Kind identifies the job type.
func (DeleteUsersArgs) Kind() string { return "delete_users" }

// DeleteUsersWorker performs DeleteUsersArgs.
type DeleteUsersWorker struct {
	river.WorkerDefaults[DeleteUsersArgs]
	D Deps
}

// Work finishes pending deletions; it can run again after a crash and continues where it stopped.
func (w *DeleteUsersWorker) Work(ctx context.Context, _ *river.Job[DeleteUsersArgs]) error {
	_, err := w.D.Accounts.ProcessDeletions(ctx)
	return failed("delete_users", err)
}

// FireDueRemindersArgs fires reminders that are due (docs/design/05 section 5.1).
type FireDueRemindersArgs struct{}

// Kind identifies the job type.
func (FireDueRemindersArgs) Kind() string { return "fire_due_reminders" }

// FireDueRemindersWorker performs FireDueRemindersArgs.
type FireDueRemindersWorker struct {
	river.WorkerDefaults[FireDueRemindersArgs]
	D Deps
}

// Work fires what is due.
func (w *FireDueRemindersWorker) Work(ctx context.Context, _ *river.Job[FireDueRemindersArgs]) error {
	_, err := w.D.Reminders.FireDue(ctx)
	return failed("fire_due_reminders", err)
}

// PurgeShareLinksArgs deletes links that ended more than a week ago (docs/design/05 section 3).
type PurgeShareLinksArgs struct{}

// Kind identifies the job type.
func (PurgeShareLinksArgs) Kind() string { return "purge_share_links" }

// PurgeShareLinksWorker performs PurgeShareLinksArgs.
type PurgeShareLinksWorker struct {
	river.WorkerDefaults[PurgeShareLinksArgs]
	D Deps
}

// Work purges old links.
func (w *PurgeShareLinksWorker) Work(ctx context.Context, _ *river.Job[PurgeShareLinksArgs]) error {
	_, err := w.D.Shares.Purge(ctx, 7*24*time.Hour)
	return failed("purge_share_links", err)
}

// New builds the River client. Call Start on it in the serving process; tests and insert-only
// callers can use it without starting workers.
func New(pool *pgxpool.Pool, d Deps) (*river.Client[pgx.Tx], error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &PurgeWorker{D: d})
	river.AddWorker(workers, &ReleaseStaleUploadsWorker{D: d})
	river.AddWorker(workers, &ExpireOutboxWorker{D: d})
	river.AddWorker(workers, &PurgeShareLinksWorker{D: d})
	river.AddWorker(workers, &FireDueRemindersWorker{D: d})
	river.AddWorker(workers, &DeleteUsersWorker{D: d})
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
			river.NewPeriodicJob(river.PeriodicInterval(5*time.Minute),
				func() (river.JobArgs, *river.InsertOpts) { return ExpireOutboxArgs{}, nil },
				nil),
			river.NewPeriodicJob(river.PeriodicInterval(10*time.Second),
				func() (river.JobArgs, *river.InsertOpts) { return FireDueRemindersArgs{}, nil },
				nil),
			river.NewPeriodicJob(river.PeriodicInterval(15*time.Second),
				func() (river.JobArgs, *river.InsertOpts) { return DeleteUsersArgs{}, nil },
				nil),
			river.NewPeriodicJob(river.PeriodicInterval(time.Hour),
				func() (river.JobArgs, *river.InsertOpts) { return PurgeShareLinksArgs{}, nil },
				nil),
		},
	})
}

package jobs

import (
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

func dbqPurgeChanges(u uuid.UUID, before time.Time) dbq.PurgeChangesParams {
	return dbq.PurgeChangesParams{UserID: u, CreatedAt: before}
}

func dbqPurgeNotifications(u uuid.UUID, before time.Time) dbq.PurgeReadNotificationsParams {
	return dbq.PurgeReadNotificationsParams{UserID: u, ReadAt: &before}
}

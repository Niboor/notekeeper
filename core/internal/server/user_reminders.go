package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/reminders"
	"github.com/Niboor/notekeeper/core/internal/store"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

func reminderOf(r dbq.Reminder) userapi.Reminder {
	return userapi.Reminder{Id: r.ID, NoteId: r.NoteID, DueAt: r.DueAt, Rrule: r.Rrule, Tz: r.Tz, State: userapi.ReminderState(r.State),
		LastFiredAt: r.LastFiredAt, Version: int(r.Version)}
}

func reminderError(err error) *httpx.Error {
	switch {
	case errors.Is(err, reminders.ErrInvalid):
		return httpx.NewError(http.StatusBadRequest, "invalid_reminder")
	case errors.Is(err, reminders.ErrPast):
		return httpx.NewError(http.StatusBadRequest, "reminder_in_past")
	case errors.Is(err, reminders.ErrNotActive):
		return httpx.NewError(http.StatusConflict, "note_not_active")
	case errors.Is(err, reminders.ErrLimit):
		return httpx.NewError(http.StatusTooManyRequests, "reminder_limit")
	}
	return nil
}

func (u *userAPI) CreateReminder(ctx context.Context, req userapi.CreateReminderRequestObject) (userapi.CreateReminderResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	r, err := u.reminders.Create(ctx, p.UserID, req.Id, req.Body.DueAt, req.Body.Rrule)
	if err != nil {
		return nil, err
	}
	return userapi.CreateReminder201JSONResponse(reminderOf(r)), nil
}

func (u *userAPI) ListReminders(ctx context.Context, _ userapi.ListRemindersRequestObject) (userapi.ListRemindersResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := u.reminders.Upcoming(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := userapi.ListReminders200JSONResponse{Items: make([]userapi.UpcomingReminder, len(rows))}
	for i, r := range rows {
		rem := dbq.Reminder{ID: r.ID, UserID: r.UserID, NoteID: r.NoteID, DueAt: r.DueAt, Rrule: r.Rrule, Tz: r.Tz, State: r.State, LastFiredAt: r.LastFiredAt, Version: r.Version}
		out.Items[i] = userapi.UpcomingReminder{Id: rem.ID, NoteId: rem.NoteID, DueAt: rem.DueAt, Rrule: rem.Rrule, Tz: rem.Tz,
			State: userapi.UpcomingReminderState(rem.State), LastFiredAt: rem.LastFiredAt, Version: int(rem.Version), Excerpt: r.Excerpt}
	}
	return out, nil
}

func (u *userAPI) UpdateReminder(ctx context.Context, req userapi.UpdateReminderRequestObject) (userapi.UpdateReminderResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	// An empty rrule ends the repetition; a missing one leaves it alone.
	setRule, rule := req.Body.Rrule != nil, req.Body.Rrule
	if setRule && *rule == "" {
		rule = nil
	}
	out, err := u.reminders.Update(ctx, p.UserID, req.Id, req.Body.DueAt, setRule, rule)
	if err != nil {
		return nil, err
	}
	return userapi.UpdateReminder200JSONResponse(reminderOf(out)), nil
}

func (u *userAPI) DeleteReminder(ctx context.Context, req userapi.DeleteReminderRequestObject) (userapi.DeleteReminderResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.reminders.Delete(ctx, p.UserID, req.Id); err != nil {
		return nil, err
	}
	return userapi.DeleteReminder204Response{}, nil
}

func (u *userAPI) SnoozeReminder(ctx context.Context, req userapi.SnoozeReminderRequestObject) (userapi.SnoozeReminderResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	r, err := u.reminders.Snooze(ctx, p.UserID, req.Id, req.Body.Until)
	if err != nil {
		return nil, err
	}
	return userapi.SnoozeReminder200JSONResponse(reminderOf(r)), nil
}

func (u *userAPI) CompleteReminder(ctx context.Context, req userapi.CompleteReminderRequestObject) (userapi.CompleteReminderResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	r, err := u.reminders.Done(ctx, p.UserID, req.Id)
	if err != nil {
		return nil, err
	}
	return userapi.CompleteReminder200JSONResponse(reminderOf(r)), nil
}

func (u *userAPI) UpdateIdentity(ctx context.Context, req userapi.UpdateIdentityRequestObject) (userapi.UpdateIdentityResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	if req.Body.ReminderTarget != nil {
		if err := u.bots.SetReminderTarget(ctx, p.UserID, req.Id, *req.Body.ReminderTarget); err != nil {
			return nil, err
		}
	}
	rows, err := u.bots.ListIdentities(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.ID == req.Id {
			return userapi.UpdateIdentity200JSONResponse{Id: r.ID, BotInstanceName: r.InstanceName, BotType: r.BotType,
				ExternalUserId: r.ExternalUserID, ReminderTarget: r.ReminderTarget, LinkedAt: r.LinkedAt}, nil
		}
	}
	return nil, errNotFound
}

// ---- notifications ------------------------------------------------------------------------------

func (u *userAPI) ListNotifications(ctx context.Context, req userapi.ListNotificationsRequestObject) (userapi.ListNotificationsResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	unreadOnly := req.Params.Unread != nil && *req.Params.Unread
	var out userapi.ListNotifications200JSONResponse
	err = u.st.InUserRead(ctx, p.UserID, func(q *dbq.Queries) error {
		rows, err := q.ListNotifications(ctx, dbq.ListNotificationsParams{UserID: p.UserID, UnreadOnly: unreadOnly})
		if err != nil {
			return err
		}
		n, err := q.CountUnreadNotifications(ctx, p.UserID)
		if err != nil {
			return err
		}
		out.Unread = int(n)
		out.Items = make([]userapi.Notification, len(rows))
		for i, r := range rows {
			payload := map[string]interface{}{}
			_ = json.Unmarshal(r.Payload, &payload)
			out.Items[i] = userapi.Notification{Id: r.ID, Kind: userapi.NotificationKind(r.Kind), Payload: payload, CreatedAt: r.CreatedAt, ReadAt: r.ReadAt}
		}
		return nil
	})
	return out, err
}

func (u *userAPI) MarkNotificationsRead(ctx context.Context, req userapi.MarkNotificationsReadRequestObject) (userapi.MarkNotificationsReadResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID // none means all
	if req.Body != nil && req.Body.Ids != nil {
		ids = *req.Body.Ids
	}
	err = u.st.InUserTx(ctx, p.UserID, func(tx *store.UserTx) error {
		done, err := tx.Q.MarkNotificationsRead(ctx, dbq.MarkNotificationsReadParams{UserID: p.UserID, ReadAt: sPtrNow(), Ids: ids})
		if err != nil {
			return err
		}
		for _, id := range done {
			if err := tx.Change(ctx, "notification", id, "upsert", nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return userapi.MarkNotificationsRead204Response{}, nil
}

func sPtrNow() *time.Time { t := time.Now(); return &t }

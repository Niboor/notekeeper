package server

import (
	"context"
	"encoding/base64"
	"strconv"
	"time"

	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/httpx"
	"github.com/Niboor/notekeeper/core/internal/notes"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
	"github.com/Niboor/notekeeper/core/internal/version"
)

// botOnlineWithin is how recently a bot must have sent a heartbeat to count as online.
const botOnlineWithin = 90 * time.Second

func (u *userAPI) GetVersion(context.Context, userapi.GetVersionRequestObject) (userapi.GetVersionResponseObject, error) {
	return userapi.GetVersion200JSONResponse{Version: version.Version}, nil
}

// ---- chat identities ------------------------------------------------------------------------

func (u *userAPI) ListIdentities(ctx context.Context, _ userapi.ListIdentitiesRequestObject) (userapi.ListIdentitiesResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := u.bots.ListIdentities(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := userapi.ListIdentities200JSONResponse{Items: make([]userapi.Identity, len(rows))}
	for i, r := range rows {
		out.Items[i] = userapi.Identity{Id: r.ID, BotInstanceName: r.InstanceName, BotType: r.BotType,
			ExternalUserId: r.ExternalUserID, ReminderTarget: r.ReminderTarget, LinkedAt: r.LinkedAt}
	}
	return out, nil
}

func (u *userAPI) UnlinkIdentity(ctx context.Context, req userapi.UnlinkIdentityRequestObject) (userapi.UnlinkIdentityResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.bots.Unlink(ctx, actorOf(p), p.UserID, req.Id); err != nil {
		return nil, err
	}
	return userapi.UnlinkIdentity204Response{}, nil
}

func (u *userAPI) CreatePairingCode(ctx context.Context, req userapi.CreatePairingCodeRequestObject) (userapi.CreatePairingCodeResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	id := req.Body.BotInstanceId
	pc, err := u.bots.CreatePairingCode(ctx, p.UserID, "", &id)
	if err != nil {
		return nil, err
	}
	return userapi.CreatePairingCode201JSONResponse{Code: pc.Code, ExpiresAt: pc.Expires}, nil
}

func (u *userAPI) ListBotInstances(ctx context.Context, _ userapi.ListBotInstancesRequestObject) (userapi.ListBotInstancesResponseObject, error) {
	if _, err := mustPrincipal(ctx); err != nil {
		return nil, err
	}
	rows, err := u.bots.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := userapi.ListBotInstances200JSONResponse{Items: []userapi.BotInstanceSummary{}}
	now := u.accts.Now()
	for _, r := range rows {
		if r.Status != "active" {
			continue
		}
		online := r.LastSeenAt != nil && now.Sub(*r.LastSeenAt) < botOnlineWithin
		out.Items = append(out.Items, userapi.BotInstanceSummary{Id: r.ID, Name: r.Name, Type: r.Type, Online: online, Address: r.Address})
	}
	return out, nil
}

// ---- notes ----------------------------------------------------------------------------------

func noteOf(n notes.Note) userapi.Note {
	out := userapi.Note{Id: n.Note.ID, State: userapi.NoteState(n.Note.State), CreatedAt: n.Note.CreatedAt,
		UpdatedAt: n.Note.UpdatedAt, DeletedAt: n.Note.DeletedAt, Version: int(n.Note.Version),
		Parts: make([]userapi.NotePart, len(n.Parts))}
	if n.Location != nil {
		out.PreviousLocation = &userapi.PreviousLocation{CategoryId: &n.Location.CategoryID, CategoryName: &n.Location.CategoryName,
			PageId: &n.Location.PageID, PageName: &n.Location.PageName}
	}
	if n.Note.CategoryID.Valid {
		id := n.Note.CategoryID.UUID
		out.CategoryId = &id
	}
	if len(n.Reminders) > 0 {
		rs := make([]userapi.Reminder, len(n.Reminders))
		for i, r := range n.Reminders {
			rs[i] = reminderOf(r)
		}
		out.Reminders = &rs
	}
	for i, p := range n.Parts {
		part := userapi.NotePart{Id: p.ID, Kind: userapi.NotePartKind(p.Kind), Text: p.Text,
			AttachReason: userapi.NotePartAttachReason(p.AttachReason), CreatedAt: p.CreatedAt, TextEditedAt: p.TextEditedAt,
			SourceBotType: p.SourceBotType}
		if p.Attachment != nil {
			part.Attachment = &userapi.AttachmentRef{Id: p.Attachment.ID, Filename: p.Attachment.Filename, MediaType: p.Attachment.MediaType, Size: p.Attachment.Size}
		}
		if p.Kind == "failed_attachment" {
			fa := userapi.FailedAttachment{Filename: deref(p.FailedFilename), Reason: deref(p.FailedReason)}
			if p.FailedSize != nil {
				fa.Size = p.FailedSize
			}
			part.Failed = &fa
		}
		out.Parts[i] = part
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (u *userAPI) ListInbox(ctx context.Context, req userapi.ListInboxRequestObject) (userapi.ListInboxResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	limit, cursor := 0, ""
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	page, err := u.notes.ListInbox(ctx, p.UserID, limit, cursor)
	if err != nil {
		return nil, err
	}
	out := userapi.ListInbox200JSONResponse{Items: make([]userapi.Note, len(page.Items)), Total: page.Total}
	for i, n := range page.Items {
		out.Items[i] = noteOf(n)
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	return out, nil
}

func (u *userAPI) GetNote(ctx context.Context, req userapi.GetNoteRequestObject) (userapi.GetNoteResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	n, err := u.notes.Get(ctx, p.UserID, req.Id)
	if err != nil {
		return nil, err
	}
	return userapi.GetNote200JSONResponse(noteOf(n)), nil
}

// ---- change feed ----------------------------------------------------------------------------

func encodeSeq(seq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(seq, 10)))
}

func decodeSeq(c string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return 0, errBadRequest.WithDetail("invalid cursor")
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil || n < 0 {
		return 0, errBadRequest.WithDetail("invalid cursor")
	}
	return n, nil
}

func (u *userAPI) ListChanges(ctx context.Context, req userapi.ListChangesRequestObject) (userapi.ListChangesResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	limit := 100
	if req.Params.Limit != nil {
		limit = clampParam(*req.Params.Limit, limit, 200)
	}
	var out userapi.ListChanges200JSONResponse
	out.Items = []userapi.Change{}
	err = u.st.InUserRead(ctx, p.UserID, func(q *dbq.Queries) error {
		current, err := q.CurrentChangeSeq(ctx, p.UserID)
		if err != nil {
			return err
		}
		if req.Params.Cursor == nil || *req.Params.Cursor == "" {
			out.Cursor = encodeSeq(current) // "from now on"
			return nil
		}
		after, err := decodeSeq(*req.Params.Cursor)
		if err != nil {
			return err
		}
		oldest, err := q.OldestChangeSeq(ctx, p.UserID)
		if err != nil {
			return err
		}
		if changesLost(after, oldest, current) {
			return httpx.NewError(410, "cursor_expired")
		}
		rows, err := q.ListChanges(ctx, dbq.ListChangesParams{UserID: p.UserID, Seq: after, Limit: int32(limit)})
		if err != nil {
			return err
		}
		last := after
		for _, r := range rows {
			c := userapi.Change{Seq: r.Seq, EntityType: r.EntityType, EntityId: r.EntityID, Op: userapi.ChangeOp(r.Op)}
			if r.Version != nil {
				v := int(*r.Version)
				c.Version = &v
			}
			out.Items = append(out.Items, c)
			last = r.Seq
		}
		out.Cursor = encodeSeq(last)
		return nil
	})
	return out, err
}

func (u *userAPI) SearchNotes(ctx context.Context, req userapi.SearchNotesRequestObject) (userapi.SearchNotesResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	in := notes.SearchInput{Query: req.Params.Q, PageID: req.Params.PageId, CategoryID: req.Params.CategoryId,
		HasAttachment: req.Params.HasAttachment, HasReminder: req.Params.HasReminder}
	if req.Params.Scope != nil {
		in.Scope = string(*req.Params.Scope)
	}
	if req.Params.Limit != nil {
		in.Limit = *req.Params.Limit
	}
	if req.Params.Cursor != nil {
		in.Cursor = *req.Params.Cursor
	}
	page, err := u.notes.Search(ctx, p.UserID, in)
	if err != nil {
		return nil, err
	}
	out := userapi.SearchNotes200JSONResponse{Items: make([]userapi.SearchHit, len(page.Hits))}
	for i, h := range page.Hits {
		n := noteOf(h.Note)
		hit := userapi.SearchHit{Note: n, Snippet: h.Snippet}
		if n.PreviousLocation != nil {
			hit.Location = n.PreviousLocation
		}
		out.Items[i] = hit
	}
	if page.NextCursor != "" {
		c := page.NextCursor
		out.NextCursor = &c
	}
	return out, nil
}

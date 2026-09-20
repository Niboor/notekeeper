package server

import (
	"context"

	"github.com/google/uuid"

	"github.com/Niboor/notekeeper/core/internal/board"
	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/notes"
	"github.com/Niboor/notekeeper/core/internal/store/dbq"
)

func pageOf(p dbq.Page) userapi.Page {
	out := userapi.Page{Id: p.ID, Name: p.Name, Version: int(p.Version)}
	if p.ArchivedAt != nil {
		archived := true
		out.Archived = &archived
	}
	return out
}

func categoryOf(c dbq.Category) userapi.Category {
	return userapi.Category{Id: c.ID, PageId: c.PageID, Name: c.Name, Version: int(c.Version)}
}

func (u *userAPI) ListPages(ctx context.Context, _ userapi.ListPagesRequestObject) (userapi.ListPagesResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := u.board.ListPages(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	out := userapi.ListPages200JSONResponse{Items: make([]userapi.Page, len(rows))}
	for i, r := range rows {
		page := pageOf(r.Page)
		cats := make([]struct {
			Id   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		}, len(r.Categories))
		for j, c := range r.Categories {
			cats[j].Id, cats[j].Name = c.ID, c.Name
		}
		page.Categories = &cats
		out.Items[i] = page
	}
	return out, nil
}

func (u *userAPI) CreatePage(ctx context.Context, req userapi.CreatePageRequestObject) (userapi.CreatePageResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	page, err := u.board.CreatePage(ctx, p.UserID, req.Body.Id, req.Body.Name, req.Body.AfterId)
	if err != nil {
		return nil, err
	}
	return userapi.CreatePage201JSONResponse(pageOf(page)), nil
}

func (u *userAPI) UpdatePage(ctx context.Context, req userapi.UpdatePageRequestObject) (userapi.UpdatePageResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	page, err := u.board.UpdatePage(ctx, p.UserID, req.Id, board.PageUpdate{Name: req.Body.Name, Archived: req.Body.Archived,
		BeforeID: req.Body.BeforeId, AfterID: req.Body.AfterId})
	if err != nil {
		return nil, err
	}
	return userapi.UpdatePage200JSONResponse(pageOf(page)), nil
}

func (u *userAPI) DeletePage(ctx context.Context, req userapi.DeletePageRequestObject) (userapi.DeletePageResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	moved, err := u.board.DeletePage(ctx, p.UserID, req.Id)
	if err != nil {
		return nil, err
	}
	return userapi.DeletePage200JSONResponse{MovedNotes: moved}, nil
}

func (u *userAPI) GetBoard(ctx context.Context, req userapi.GetBoardRequestObject) (userapi.GetBoardResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	per := 0
	if req.Params.NotesPerCategory != nil {
		per = *req.Params.NotesPerCategory
	}
	b, err := u.board.GetBoard(ctx, p.UserID, req.Id, per)
	if err != nil {
		return nil, err
	}
	out := userapi.GetBoard200JSONResponse{Page: pageOf(b.Page), InboxTotal: b.InboxTotal, Categories: make([]userapi.BoardCategory, len(b.Categories))}
	for i, c := range b.Categories {
		bc := userapi.BoardCategory{Category: categoryOf(c.Category), Total: c.Total, Notes: make([]userapi.Note, len(c.Notes))}
		for j, n := range c.Notes {
			bc.Notes[j] = noteOf(n)
		}
		if c.NextCursor != "" {
			cursor := c.NextCursor
			bc.NextCursor = &cursor
		}
		out.Categories[i] = bc
	}
	return out, nil
}

func (u *userAPI) CreateCategory(ctx context.Context, req userapi.CreateCategoryRequestObject) (userapi.CreateCategoryResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	c, err := u.board.CreateCategory(ctx, p.UserID, req.Body.Id, req.Body.PageId, req.Body.Name, req.Body.AfterId)
	if err != nil {
		return nil, err
	}
	return userapi.CreateCategory201JSONResponse(categoryOf(c)), nil
}

func (u *userAPI) UpdateCategory(ctx context.Context, req userapi.UpdateCategoryRequestObject) (userapi.UpdateCategoryResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	c, err := u.board.UpdateCategory(ctx, p.UserID, req.Id, board.CategoryUpdate{Name: req.Body.Name, PageID: req.Body.PageId,
		BeforeID: req.Body.BeforeId, AfterID: req.Body.AfterId})
	if err != nil {
		return nil, err
	}
	return userapi.UpdateCategory200JSONResponse(categoryOf(c)), nil
}

func (u *userAPI) DeleteCategory(ctx context.Context, req userapi.DeleteCategoryRequestObject) (userapi.DeleteCategoryResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	moved, err := u.board.DeleteCategory(ctx, p.UserID, req.Id)
	if err != nil {
		return nil, err
	}
	return userapi.DeleteCategory200JSONResponse{MovedNotes: moved}, nil
}

func notePage(page notes.Page) userapi.NotePage {
	out := userapi.NotePage{Items: make([]userapi.Note, len(page.Items)), Total: page.Total}
	for i, n := range page.Items {
		out.Items[i] = noteOf(n)
	}
	if page.NextCursor != "" {
		c := page.NextCursor
		out.NextCursor = &c
	}
	return out
}

func (u *userAPI) ListCategoryNotes(ctx context.Context, req userapi.ListCategoryNotesRequestObject) (userapi.ListCategoryNotesResponseObject, error) {
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
	page, err := u.notes.ListCategoryNotes(ctx, p.UserID, req.Id, limit, cursor)
	if err != nil {
		return nil, err
	}
	return userapi.ListCategoryNotes200JSONResponse(notePage(page)), nil
}

func (u *userAPI) ListTrash(ctx context.Context, req userapi.ListTrashRequestObject) (userapi.ListTrashResponseObject, error) {
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
	page, err := u.notes.ListTrash(ctx, p.UserID, limit, cursor)
	if err != nil {
		return nil, err
	}
	return userapi.ListTrash200JSONResponse(notePage(page)), nil
}

func partInputs(in []userapi.NewPartInput) ([]notes.PartInput, error) {
	out := make([]notes.PartInput, len(in))
	for i, p := range in {
		switch p.Type {
		case userapi.NewPartInputTypeText:
			if p.AttachmentId != nil {
				return nil, errBadRequest.WithDetail("a text part cannot carry an attachment")
			}
			out[i] = notes.PartInput{Text: p.Text}
		case userapi.NewPartInputTypeAttachment:
			if p.Text != nil {
				return nil, errBadRequest.WithDetail("an attachment part cannot carry text")
			}
			out[i] = notes.PartInput{AttachmentID: p.AttachmentId}
		default:
			return nil, errBadRequest
		}
	}
	return out, nil
}

func (u *userAPI) CreateNote(ctx context.Context, req userapi.CreateNoteRequestObject) (userapi.CreateNoteResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	parts, err := partInputs(req.Body.Parts)
	if err != nil {
		return nil, err
	}
	n, err := u.notes.Create(ctx, p.UserID, notes.CreateInput{ID: req.Body.Id, CategoryID: req.Body.CategoryId,
		BeforeID: req.Body.BeforeId, AfterID: req.Body.AfterId, Parts: parts})
	if err != nil {
		return nil, err
	}
	return userapi.CreateNote201JSONResponse(noteOf(n)), nil
}

func (u *userAPI) MoveNote(ctx context.Context, req userapi.MoveNoteRequestObject) (userapi.MoveNoteResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	n, err := u.notes.Move(ctx, p.UserID, req.Id, notes.MoveInput{CategoryID: req.Body.CategoryId, BeforeID: req.Body.BeforeId, AfterID: req.Body.AfterId})
	if err != nil {
		return nil, err
	}
	return userapi.MoveNote200JSONResponse(noteOf(n)), nil
}

func (u *userAPI) DismissNote(ctx context.Context, req userapi.DismissNoteRequestObject) (userapi.DismissNoteResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	n, err := u.notes.Dismiss(ctx, p.UserID, req.Id)
	if err != nil {
		return nil, err
	}
	return userapi.DismissNote200JSONResponse(noteOf(n)), nil
}

func (u *userAPI) RestoreNote(ctx context.Context, req userapi.RestoreNoteRequestObject) (userapi.RestoreNoteResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	n, err := u.notes.Restore(ctx, p.UserID, req.Id)
	if err != nil {
		return nil, err
	}
	return userapi.RestoreNote200JSONResponse(noteOf(n)), nil
}

func (u *userAPI) DeleteNotePermanently(ctx context.Context, req userapi.DeleteNotePermanentlyRequestObject) (userapi.DeleteNotePermanentlyResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.notes.DeletePermanently(ctx, p.UserID, req.Id); err != nil {
		return nil, err
	}
	return userapi.DeleteNotePermanently204Response{}, nil
}

func (u *userAPI) AddNotePart(ctx context.Context, req userapi.AddNotePartRequestObject) (userapi.AddNotePartResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	in := notes.PartInput{Text: req.Body.Text, AttachmentID: req.Body.AttachmentId}
	if (in.Text == nil) == (in.AttachmentID == nil) {
		return nil, errBadRequest.WithDetail("a part is either text or an attachment")
	}
	n, err := u.notes.AddPart(ctx, p.UserID, req.Id, in)
	if err != nil {
		return nil, err
	}
	return userapi.AddNotePart201JSONResponse(noteOf(n)), nil
}

func (u *userAPI) EditNotePart(ctx context.Context, req userapi.EditNotePartRequestObject) (userapi.EditNotePartResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body == nil {
		return nil, errBadRequest
	}
	var base *int32
	if req.Body.BaseVersion != nil {
		v := int32(*req.Body.BaseVersion)
		base = &v
	}
	res, err := u.notes.EditPart(ctx, p.UserID, req.Id, req.PartId, req.Body.Text, base, p.SessionID)
	if err != nil {
		return nil, err
	}
	return userapi.EditNotePart200JSONResponse{Note: noteOf(res.Note), Stale: res.Stale}, nil
}

func (u *userAPI) DeleteNotePart(ctx context.Context, req userapi.DeleteNotePartRequestObject) (userapi.DeleteNotePartResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	n, err := u.notes.RemovePart(ctx, p.UserID, req.Id, req.PartId)
	if err != nil {
		return nil, err
	}
	return userapi.DeleteNotePart200JSONResponse(noteOf(n)), nil
}

func (u *userAPI) GetNoteHistory(ctx context.Context, req userapi.GetNoteHistoryRequestObject) (userapi.GetNoteHistoryResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	byPart, order, err := u.notes.History(ctx, p.UserID, req.Id)
	if err != nil {
		return nil, err
	}
	out := userapi.GetNoteHistory200JSONResponse{Parts: []struct {
		PartId   uuid.UUID             `json:"part_id"`
		Versions []userapi.PartVersion `json:"versions"`
	}{}}
	for _, id := range order {
		item := struct {
			PartId   uuid.UUID             `json:"part_id"`
			Versions []userapi.PartVersion `json:"versions"`
		}{PartId: id}
		for _, v := range byPart[id] {
			item.Versions = append(item.Versions, userapi.PartVersion{Id: v.ID, Text: v.Text, Origin: userapi.PartVersionOrigin(v.Origin), EditedAt: v.EditedAt, Applied: v.Applied})
		}
		out.Parts = append(out.Parts, item)
	}
	return out, nil
}

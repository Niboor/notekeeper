package server

import (
	"context"
	"github.com/google/uuid"
	"net/http"

	"github.com/Niboor/notekeeper/core/internal/gen/userapi"
	"github.com/Niboor/notekeeper/core/internal/httpx"
)

// exportDownload streams the archive straight into the response: nothing is buffered, so memory does
// not depend on how much the user has (AUTH-U5).
type exportDownload struct {
	u    *userAPI
	ctx  context.Context
	user profile
}

type profile map[string]any

func (d exportDownload) VisitExportMyDataResponse(w http.ResponseWriter) error {
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", `attachment; filename="notekeeper-export.zip"`)
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	if err := d.u.export.Write(d.ctx, d.u.exportUser(d.ctx), w, d.user); err != nil {
		// The status is already sent; the archive is cut short and the client sees an incomplete file.
		d.u.log.Error("export failed", "request_id", httpx.RequestIDFrom(d.ctx), "error", err)
		return err
	}
	return nil
}

func (u *userAPI) exportUser(ctx context.Context) uuid.UUID {
	p, _ := PrincipalFrom(ctx)
	return p.UserID
}

// ExportMyData starts an export of everything the caller owns (AUTH-U5, SEC-DATA-8).
func (u *userAPI) ExportMyData(ctx context.Context, _ userapi.ExportMyDataRequestObject) (userapi.ExportMyDataResponseObject, error) {
	p, err := mustPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	if err := u.export.Start(ctx, actorOf(p), p.UserID); err != nil {
		return nil, err
	}
	me, err := u.accts.Me(ctx, p)
	if err != nil {
		return nil, err
	}
	return exportDownload{u: u, ctx: ctx, user: profile{"username": me.Username, "display_name": me.DisplayName, "timezone": me.Timezone}}, nil
}

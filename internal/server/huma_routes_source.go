package server

import (
	"context"
	"net/http"

	"go.kenn.io/agentsview/internal/sourcelayout"
	"go.kenn.io/agentsview/internal/trash"
)

type trashSourceInput struct {
	ID string `path:"id" required:"true" doc:"Session ID"`
}

type trashSourceOutput struct {
	Body struct {
		Trashed bool     `json:"trashed"`
		Paths   []string `json:"paths,omitempty"`
		Reason  string   `json:"reason,omitempty"`
	}
}

// humaTrashSource moves a session's source files to the operating
// system trash (recording agentsview's restore manifest) and
// soft-deletes the archived row. App-owned layouts are refused with
// the stable report-only reason.
func (s *Server) humaTrashSource(
	ctx context.Context,
	in *trashSourceInput,
) (*trashSourceOutput, error) {
	session, err := s.db.GetSession(ctx, in.ID)
	if err != nil {
		return nil, internalError("trash source lookup", err)
	}
	if session == nil {
		return nil, apiError(http.StatusNotFound, "session not found")
	}

	decision := sourcelayout.For(session.Agent).Decide(*session)
	if !decision.Deletable {
		return nil, apiError(http.StatusConflict, decision.Reason)
	}
	if s.trashStore() == nil {
		return nil, apiError(
			http.StatusInternalServerError, "trash store unavailable")
	}
	metas := make([]trash.Meta, len(decision.Paths))
	if len(metas) > 0 {
		metas[0] = trash.Meta{SessionID: session.ID, Agent: session.Agent}
	}
	items, err := s.trashStore().Trash(decision.Paths, metas)
	if err != nil || len(items) == 0 {
		return nil, internalError("trashing session source", err)
	}

	if err := s.db.SoftDeleteSession(in.ID); err != nil {
		if handled := handleHumaReadOnly(err); handled != nil {
			return nil, handled
		}
		return nil, internalError("soft delete session", err)
	}
	s.notifySessionMutation()

	out := &trashSourceOutput{}
	out.Body.Trashed = true
	for _, item := range items {
		out.Body.Paths = append(out.Body.Paths, item.TrashedPath)
	}
	return out, nil
}

// trashStore lazily builds the trash manifest store from the server's
// data directory.
func (s *Server) trashStore() *trash.Store {
	return trash.New(s.dataDir)
}

// derefSessionPath reads an optional source-path column safely.
func derefSessionPath(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

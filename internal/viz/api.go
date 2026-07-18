package viz

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/internal/trail"
	"github.com/yasyf/cc-notes/model"
)

// RepoInfo assembles just the graph header — worktree root, resolved trunk,
// the branch HEAD points at (empty when detached), and the generation instant —
// without walking any commits, so GET /api/repo is cheap. Truncated is always
// false: no walk runs to hit a cap. Graph builds the same header from its
// topology; this is the standalone path for the header-only endpoint.
func (b *Builder) RepoInfo(ctx context.Context) (RepoInfo, error) {
	trunk, err := b.trunkName(ctx)
	if err != nil {
		return RepoInfo{}, err
	}
	head, err := b.head(ctx)
	if err != nil {
		return RepoInfo{}, err
	}
	root, err := b.store.Git.Root(ctx)
	if err != nil {
		return RepoInfo{}, fmt.Errorf("resolve repo root: %w", err)
	}
	return RepoInfo{
		Root:        root,
		Trunk:       trunk,
		Head:        head,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Truncated:   false,
	}, nil
}

func (s *Server) handleRepo(w http.ResponseWriter, r *http.Request) {
	info, err := s.builder.RepoInfo(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	var since int64
	if raw := r.URL.Query().Get("since"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid since %q: want a unix timestamp", raw))
			return
		}
		since = v
	}
	g, err := s.builder.Graph(r.Context(), since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// entityResponse is the /api/entity/{kind}/{id} payload: the entity's legend
// summary, its full folded tip snapshot, and its full change trail, oldest
// first, checkpoints included.
type entityResponse struct {
	Summary  EntitySummary  `json:"summary"`
	Snapshot model.Snapshot `json:"snapshot"`
	Trail    []trailEntry   `json:"trail"`
}

// trailEntry is one change-trail commit in the entity wire format: the commit
// identity, its writing session, its lamport clock, the entry kind
// (create|edit|checkpoint), the commits a checkpoint covers, and the field
// deltas.
type trailEntry struct {
	SHA     string        `json:"sha"`
	Author  string        `json:"author"`
	Session string        `json:"session,omitempty"`
	Time    int64         `json:"time"`
	Lamport uint64        `json:"lamport"`
	Kind    string        `json:"kind"`
	Covers  int           `json:"covers"`
	Changes []trailChange `json:"changes"`
}

// trailChange is one field delta: a scalar carries From→To with Scalar true,
// otherwise Added and Removed hold the set elements. Values are canonical-JSON
// forms — string, number, bool, null, or object — rendered by the client.
type trailChange struct {
	Field   string `json:"field"`
	Scalar  bool   `json:"scalar"`
	From    any    `json:"from"`
	To      any    `json:"to"`
	Added   []any  `json:"added"`
	Removed []any  `json:"removed"`
}

// candidate is one entity matched by an ambiguous id prefix.
type candidate struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// ambiguousResponse is the 400 body when an id prefix matches more than one
// entity: the error string plus every candidate.
type ambiguousResponse struct {
	Error      string      `json:"error"`
	Candidates []candidate `json:"candidates"`
}

func (s *Server) handleEntity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kind, ok := entityKind(r.PathValue("kind"))
	if !ok {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown kind %q: want note|doc|log|task|sprint|project|runbook|investigation", r.PathValue("kind")))
		return
	}
	ref, err := s.store.Resolve(ctx, kind, r.PathValue("id"))
	var ambig *store.AmbiguousError
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
		return
	case errors.As(err, &ambig):
		writeJSON(w, http.StatusBadRequest, ambiguousBody(ambig))
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	steps, err := s.store.History(ctx, ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	entries, err := trail.Entries(steps)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(entries) == 0 {
		writeError(w, http.StatusInternalServerError, "empty trail for "+ref)
		return
	}

	changes := make([]trailEntry, len(entries))
	for i, e := range entries {
		changes[i] = trailEntryOf(e)
	}
	tip := entries[len(entries)-1].Snapshot
	writeJSON(w, http.StatusOK, entityResponse{
		Summary:  summaryOf(tip),
		Snapshot: tip,
		Trail:    changes,
	})
}

// entityKind maps a URL kind segment to its model.Kind, reporting whether it is
// one of the entity kinds. The kind values are the URL segments, so an unknown
// segment fails the same way it always did (ok=false → 400), never panics.
func entityKind(seg string) (model.Kind, bool) {
	kind, err := model.ParseKind(seg)
	return kind, err == nil
}

// summaryOf builds the legend summary for any entity snapshot: the common
// fields from Meta, then the genuinely per-kind extras. A snapshot of an
// unhandled kind panics — a programmer error the closed kind set cannot express.
func summaryOf(snap model.Snapshot) EntitySummary {
	m := snap.Meta()
	id := snap.EntityID()
	s := EntitySummary{
		Kind:  string(m.Kind),
		ID:    id,
		Short: id.Short(),
		Title: m.Title,
	}
	switch v := snap.(type) {
	case model.Note:
		s.VerifiedAt, s.Stale, s.Superseded = v.VerifiedAt, v.StaleAt != 0, len(v.SupersededBy) > 0
	case model.Doc:
		s.VerifiedAt, s.Stale, s.Superseded = v.VerifiedAt, v.StaleAt != 0, len(v.SupersededBy) > 0
	case model.Log:
		// logs carry no summary extras.
	case model.Task:
		s.Status, s.Branch, s.Assignee = string(v.Status), string(v.Branch), string(v.Assignee)
		s.StartedAt, s.ClosedAt = v.StartedAt, v.ClosedAt
		s.Sprint, s.Project = string(v.Sprint), string(v.Project)
	case model.Sprint:
		s.Status, s.StartDate, s.EndDate, s.Project = string(v.Status), v.StartDate, v.EndDate, string(v.Project)
	case model.Project:
		s.Status = string(v.Status)
	case model.Runbook:
		s.Status = string(v.Status)
	case model.Investigation:
		s.Status, s.ClosedAt = string(v.Status), v.ClosedAt
	default:
		panic(fmt.Sprintf("viz: no summary for snapshot %T", snap))
	}
	return s
}

// trailEntryOf projects one trail.Entry into the entity wire format.
func trailEntryOf(e trail.Entry) trailEntry {
	changes := make([]trailChange, len(e.Changes))
	for i, ch := range e.Changes {
		changes[i] = trailChange{
			Field:   ch.Field,
			Scalar:  ch.Scalar,
			From:    ch.From,
			To:      ch.To,
			Added:   ch.Added,
			Removed: ch.Removed,
		}
	}
	return trailEntry{
		SHA:     string(e.Commit.SHA),
		Author:  string(e.Commit.Author),
		Session: e.Commit.Pack.Session,
		Time:    e.Commit.AuthorTime,
		Lamport: uint64(e.Commit.Pack.Lamport),
		Kind:    e.Kind,
		Covers:  e.Covers,
		Changes: changes,
	}
}

// ambiguousBody renders an AmbiguousError as its 400 wire body.
func ambiguousBody(err *store.AmbiguousError) ambiguousResponse {
	cands := make([]candidate, len(err.Candidates))
	for i, c := range err.Candidates {
		cands[i] = candidate{ID: string(c.ID), Title: c.Title}
	}
	return ambiguousResponse{Error: err.Error(), Candidates: cands}
}

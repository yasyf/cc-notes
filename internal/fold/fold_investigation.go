package fold

import (
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

type investigationFolder struct {
	inv         model.Investigation
	tags        map[string]bool
	anchors     map[model.Anchor]bool
	superseded  map[model.EntityID]bool
	attachments map[string]model.Attachment
	commits     map[model.SHA]bool
	fixCommits  map[model.SHA]bool
	followUps   map[model.EntityID]bool
	entries     []model.LogEntry
	findings    []model.Finding
}

func newInvestigationFolder() *investigationFolder {
	return &investigationFolder{
		tags:        map[string]bool{},
		anchors:     map[model.Anchor]bool{},
		superseded:  map[model.EntityID]bool{},
		attachments: map[string]model.Attachment{},
		commits:     map[model.SHA]bool{},
		fixCommits:  map[model.SHA]bool{},
		followUps:   map[model.EntityID]bool{},
	}
}

func foldInvestigation(ordered []model.PackCommit) (model.Investigation, error) {
	return run[model.Investigation](ordered, newInvestigationFolder())
}

func (f *investigationFolder) fresh(sha model.SHA, createdAt int64) {
	f.inv = model.Investigation{ID: model.EntityID(sha), CreatedAt: createdAt}
	f.entries = []model.LogEntry{}
	f.findings = []model.Finding{}
}

func (f *investigationFolder) seed(state model.Snapshot) error {
	seed, ok := state.(model.Investigation)
	if !ok {
		return fmt.Errorf("%w: checkpoint over a non-investigation folded as an investigation", ErrKindMismatch)
	}
	f.inv = seed
	f.entries = slices.Clone(seed.Entries)
	f.findings = slices.Clone(seed.Findings)
	for _, t := range seed.Tags {
		f.tags[t] = true
	}
	for _, a := range seed.Anchors {
		f.anchors[a] = true
	}
	for _, id := range seed.SupersededBy {
		f.superseded[id] = true
	}
	for _, a := range seed.Attachments {
		f.attachments[a.Name] = a
	}
	for _, sha := range seed.Commits {
		f.commits[sha] = true
	}
	for _, sha := range seed.FixCommits {
		f.fixCommits[sha] = true
	}
	for _, id := range seed.FollowUps {
		f.followUps[id] = true
	}
	return nil
}

func (f *investigationFolder) create(op model.CreateOp, author model.Actor) error {
	o, ok := op.(model.CreateInvestigation)
	if !ok {
		return fmt.Errorf("%w: %s chain folded as an investigation", ErrKindMismatch, op.OpKind())
	}
	f.inv.Title, f.inv.Premise, f.inv.Author = o.Title, o.Premise, author
	f.inv.Status = model.InvestigationOpen
	for _, t := range o.Tags {
		f.tags[t] = true
	}
	for _, a := range o.Anchors {
		f.anchors[a] = true
	}
	return nil
}

func (f *investigationFolder) apply(op model.Op, c model.PackCommit) error {
	if applyTag(f.tags, op) || applyAnchor(f.anchors, op) ||
		applySupersede(f.superseded, op) || applyAttachment(f.attachments, op) ||
		applyCommitLink(f.commits, op) {
		return nil
	}
	switch o := op.(type) {
	case model.SetTitle:
		f.inv.Title = o.Title
	case model.SetBody:
		f.inv.Body = o.Body
	case model.AppendEntry:
		f.entries = append(f.entries, model.LogEntry{Author: c.Author, TS: c.AuthorTime, Text: o.Text, Model: o.Model})
	case model.DeleteNote:
		f.inv.Deleted = true
	case model.SetInvestigationStatus:
		applyInvestigationStatus(&f.inv, o.Status, c.Author, c.AuthorTime)
	case model.SetRootCause:
		f.inv.RootCause = o.Text
	case model.AddFinding:
		if findingIndex(f.findings, o.ID) < 0 {
			f.findings = append(f.findings, model.Finding{ID: o.ID, Text: o.Text, Status: model.FindingOpen})
		}
	case model.RemoveFinding:
		if i := findingIndex(f.findings, o.ID); i >= 0 {
			f.findings = slices.Delete(f.findings, i, i+1)
		}
	case model.SetFindingText:
		if i := findingIndex(f.findings, o.ID); i >= 0 {
			f.findings[i].Text = o.Text
		}
	case model.SetFindingStatus:
		if i := findingIndex(f.findings, o.ID); i >= 0 {
			f.findings[i].Status = o.Status
			f.findings[i].Note = o.Note
		}
	case model.AddFixCommit:
		f.fixCommits[o.SHA] = true
	case model.RemoveFixCommit:
		delete(f.fixCommits, o.SHA)
	case model.AddFollowUp:
		f.followUps[o.ID] = true
	case model.RemoveFollowUp:
		delete(f.followUps, o.ID)
	default:
		return fmt.Errorf("%w: %s on an investigation", ErrKindMismatch, op.OpKind())
	}
	return nil
}

func (f *investigationFolder) touch(c model.PackCommit) {
	f.inv.UpdatedAt = c.AuthorTime
}

func (f *investigationFolder) finalize(head model.SHA) model.Investigation {
	f.inv.Tags = sortedKeys(f.tags)
	f.inv.Anchors = sortedAnchors(f.anchors)
	f.inv.SupersededBy = sortedKeys(f.superseded)
	f.inv.Attachments = sortedAttachments(f.attachments)
	f.inv.Commits = sortedKeys(f.commits)
	f.inv.FixCommits = sortedKeys(f.fixCommits)
	f.inv.FollowUps = sortedKeys(f.followUps)
	if f.entries == nil {
		f.entries = []model.LogEntry{}
	}
	if f.findings == nil {
		f.findings = []model.Finding{}
	}
	f.inv.Entries = f.entries
	f.inv.Findings = f.findings
	f.inv.Head = head
	return f.inv
}

// findingIndex returns the index of the finding with the given id in the slice,
// or -1 when absent. Finding lists are tiny, so a linear scan is the right tool;
// there is no stored lamport.
func findingIndex(findings []model.Finding, id string) int {
	for i := range findings {
		if findings[i].ID == id {
			return i
		}
	}
	return -1
}

// applyInvestigationStatus sets the LWW status and stamps ClosedAt/ClosedBy from
// the carrying commit when the status is terminal, zeroing both otherwise. The
// fold is total: it never checks transition legality.
func applyInvestigationStatus(inv *model.Investigation, status model.InvestigationStatus, by model.Actor, at int64) {
	inv.Status = status
	switch status {
	case model.InvestigationConfirmed, model.InvestigationExonerated, model.InvestigationAbandoned:
		inv.ClosedAt = at
		inv.ClosedBy = by
	default:
		inv.ClosedAt = 0
		inv.ClosedBy = ""
	}
}

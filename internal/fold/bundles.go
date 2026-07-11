package fold

import "github.com/yasyf/cc-notes/model"

// The apply* helpers fold the op bundles shared verbatim across kinds. Each
// mutates one auxiliary set and reports whether op belonged to that bundle, so a
// kind's folder chains the bundles it uses before its own op cases.

func applyTag(tags map[string]bool, op model.Op) bool {
	switch o := op.(type) {
	case model.AddTag:
		tags[o.Tag] = true
	case model.RemoveTag:
		delete(tags, o.Tag)
	default:
		return false
	}
	return true
}

func applyAnchor(anchors map[model.Anchor]bool, op model.Op) bool {
	switch o := op.(type) {
	case model.AddAnchor:
		anchors[o.Anchor] = true
	case model.RemoveAnchor:
		delete(anchors, o.Anchor)
	default:
		return false
	}
	return true
}

func applyAttachment(attachments map[string]model.Attachment, op model.Op) bool {
	switch o := op.(type) {
	case model.AddAttachment:
		attachments[o.Name] = model.Attachment(o)
	case model.RemoveAttachment:
		delete(attachments, o.Name)
	default:
		return false
	}
	return true
}

func applySupersede(superseded map[model.EntityID]bool, op model.Op) bool {
	switch o := op.(type) {
	case model.AddSupersededBy:
		superseded[o.ID] = true
	case model.RemoveSupersededBy:
		delete(superseded, o.ID)
	default:
		return false
	}
	return true
}

func applyLabel(labels map[string]bool, op model.Op) bool {
	switch o := op.(type) {
	case model.AddLabel:
		labels[o.Label] = true
	case model.RemoveLabel:
		delete(labels, o.Label)
	default:
		return false
	}
	return true
}

func applyCommitLink(commits map[model.SHA]bool, op model.Op) bool {
	switch o := op.(type) {
	case model.LinkCommit:
		commits[o.SHA] = true
	case model.UnlinkCommit:
		delete(commits, o.SHA)
	default:
		return false
	}
	return true
}

func applyComment(comments *[]model.Comment, op model.Op, c model.PackCommit) bool {
	o, ok := op.(model.AddComment)
	if !ok {
		return false
	}
	*comments = append(*comments, model.Comment{Author: c.Author, TS: c.AuthorTime, Body: o.Body})
	return true
}

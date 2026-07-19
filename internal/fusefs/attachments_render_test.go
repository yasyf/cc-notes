// Attachment render invariant: rendered documents carry no attachments
// frontmatter, so checked-out content cannot corrupt an attachment reference.
package fusefs_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/model"
)

func TestRenderIgnoresAttachments(t *testing.T) {
	atts := []model.Attachment{
		{Name: "report.pdf", OID: strings.Repeat("a", 64), Size: 12345},
		{Name: "trace.log", OID: strings.Repeat("b", 64), Size: 7},
	}
	note := model.Note{ID: "a1b2c3d4e5", Title: "Carrier", Body: "body", CreatedAt: 1735689600, UpdatedAt: 1735689600}
	doc := model.Doc{ID: "b2c3d4e5f6", Title: "Carrier", Body: "body", When: "reading", CreatedAt: 1735689600, UpdatedAt: 1735689600}
	log := model.Log{ID: "c3d4e5f6a7", Title: "Carrier", CreatedAt: 1735689600, UpdatedAt: 1735689600}

	withNote, withDoc, withLog := note, doc, log
	withNote.Attachments, withDoc.Attachments, withLog.Attachments = atts, atts, atts

	for _, tc := range []struct {
		name          string
		bare, carried []byte
	}{
		{"note", fusefs.RenderNote(note), fusefs.RenderNote(withNote)},
		{"doc", fusefs.RenderDoc(doc), fusefs.RenderDoc(withDoc)},
		{"log", fusefs.RenderLog(log), fusefs.RenderLog(withLog)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !bytes.Equal(tc.bare, tc.carried) {
				t.Fatalf("Render%s bytes changed with attachments:\nbare    %q\ncarried %q", tc.name, tc.bare, tc.carried)
			}
			if bytes.Contains(tc.carried, []byte("attachment")) {
				t.Fatalf("rendered %s mentions attachments:\n%s", tc.name, tc.carried)
			}
		})
	}
}

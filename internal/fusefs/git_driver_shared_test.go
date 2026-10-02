//go:build darwin

package fusefs

import (
	"path/filepath"
	"testing"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/sourcedriver"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/store"
)

func TestGitDriverSharedSourceIndex(t *testing.T) {
	ctx := t.Context()
	source := gittest.InitRepo(t)
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "base")
	thin := gittest.ShallowClone(t, source, 1)
	if _, err := store.Bind(ctx, thin, source); err != nil {
		t.Fatalf("Bind(thin, source): %v", err)
	}
	sourceCommon, err := filepath.EvalSymlinks(filepath.Join(source, ".git"))
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	id, err := catalog.NewTenantID("cc-notes-driver-test")
	if err != nil {
		t.Fatal(err)
	}
	tenant := Tenant{ID: id, Generation: 1, Authority: AuthorityForTenant(id), RouteName: "repo", RepoRoot: thin}
	driver, err := NewGitDriver(tenant.Authority, testDriverAuthorityGeneration, testDriverDeclarationDigest, thin)
	if err != nil {
		t.Fatal(err)
	}
	declareDriverTargetSet(t, driver, tenant.Authority, testDriverTargetSet, testDriverTargets)

	opened, index, err := driver.open(ctx, tenant.Authority)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if index.Git != gitcmd.Backend(sourceCommon, filepath.Dir(sourceCommon)) {
		t.Fatalf("index.Git = %+v, want a Backend handle on %q", index.Git, sourceCommon)
	}
	if index.Repo != opened.Repo || opened.RecordsCommonDir() != sourceCommon {
		t.Fatalf("index.Repo and the store's records repository differ (records common dir %q)", opened.RecordsCommonDir())
	}
	if opened.Git != (gitcmd.Git{Dir: thin}) {
		t.Fatalf("context handle = %+v, want the thin checkout %q", opened.Git, thin)
	}

	head := refreshDriver(t, driver, tenant.Authority)
	body := NewNoteTemplate("Created through a bound driver", nil, nil)
	request := mutationRequest(t, tenant, "33", head.Revision, testDriverCreateContext(tenant, 1), body)
	receipt, err := driver.ApplyMutation(ctx, tenant.Authority, request, newMemorySource(body))
	if err != nil {
		t.Fatalf("ApplyMutation: %v", err)
	}
	if receipt.State != sourcedriver.MutationApplied || receipt.Result == "" {
		t.Fatalf("receipt = %+v, want applied", receipt)
	}

	pin := "refs/heads/cc-notes-receipt-pins/" + request.OperationID.String()
	for _, ref := range []string{sourceindex.Ref, pin} {
		if got := gittest.Git(t, source, "for-each-ref", "--format=%(refname)", ref); got != ref {
			t.Fatalf("source for-each-ref %s = %q, want the ref itself", ref, got)
		}
		if got := gittest.Git(t, thin, "for-each-ref", "--format=%(refname)", ref); got != "" {
			t.Fatalf("thin repository holds %q; source-index refs belong to the records repository", got)
		}
	}
	if got := gittest.Git(t, thin, "for-each-ref", "--format=%(refname)", "refs/cc-notes/", "refs/cc-notes-source-v1/", "refs/heads/cc-notes-receipt-pins/"); got != "" {
		t.Fatalf("thin repository holds records state:\n%s", got)
	}
	src, err := store.Open(source)
	if err != nil {
		t.Fatalf("Open(source): %v", err)
	}
	notes, err := src.ListNotes(ctx, false, false)
	if err != nil {
		t.Fatalf("source ListNotes: %v", err)
	}
	if len(notes) != 1 || notes[0].Title != "Created through a bound driver" {
		t.Fatalf("source notes = %+v, want the one the bound driver created", notes)
	}
}

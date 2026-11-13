// SPDX-License-Identifier: Apache-2.0

package migrations

import (
	"io/fs"
	"slices"
	"strings"
	"testing"
)

// The catalog room-broker migrate ships: twelve SQL files, sorted, five of them txmode none, and
// nothing else in FS. kustomization.yaml feeds the operator and is never read at run time; atlas.sum
// is embedded apart, for its per-file hashes alone (TestAtlasHashes). This test stays Docker-free
// on purpose, so it runs everywhere.
func TestEmbeddedMigrationCatalog(t *testing.T) {
	entries, err := fs.Glob(FS, "*")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		if name == "atlas.sum" || name == "kustomization.yaml" {
			t.Fatalf("%s is embedded: migrate applies SQL files only", name)
		}
	}
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	var names, versions, noTx []string
	for _, f := range files {
		names = append(names, f.Name)
		versions = append(versions, f.Version)
		if f.NoTx {
			noTx = append(noTx, f.Name)
		}
	}
	wantNames := []string{
		"20260927120000_rooms.sql", "20260929120000_verdicts.sql", "20261001120000_driver_queue.sql",
		"20261001130000_delivery_indexes.sql", "20261001140000_brief_index.sql", "20261005120000_approvals.sql",
		"20261005130000_approval_indexes.sql", "20261008120000_task_facts_index.sql", "20261023120000_webhooks.sql", "20261028120000_slack_links.sql",
		"20261105120000_approval_binding.sql", "20261108120000_approval_grants.sql",
		"20261108130000_policy_promotions.sql", "20261112120000_intent_samples.sql",
		"20261113120000_output_bundles.sql", "20261113130000_approval_digest.sql",
	}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("files = %q, want %q", names, wantNames)
	}
	if !slices.IsSorted(versions) || versions[0] != "20260927120000" || versions[len(versions)-1] != "20261113130000" {
		t.Fatalf("versions = %q, want sorted 20260927120000 through 20261113130000", versions)
	}
	// The txmode-none files are the ones with CREATE INDEX CONCURRENTLY; a change here changes how
	// they are applied.
	wantNoTx := []string{"20260929120000_verdicts.sql", "20261001130000_delivery_indexes.sql",
		"20261001140000_brief_index.sql", "20261005130000_approval_indexes.sql", "20261008120000_task_facts_index.sql"}
	if !slices.Equal(noTx, wantNoTx) {
		t.Fatalf("txmode none = %q, want %q", noTx, wantNoTx)
	}
}

// Every file carries its atlas.sum hash, the one the Atlas operator records for it: migrate adopts
// the operator's ledger only where the two agree.
func TestAtlasHashes(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if len(f.AtlasHash) != 44 || strings.HasPrefix(f.AtlasHash, "h1:") {
			t.Errorf("%s: AtlasHash %q, want atlas.sum's base64 SHA-256 without h1:", f.Name, f.AtlasHash)
		}
	}
	if got := files[0].AtlasHash; got != "2iHh9ofAjF9QEjy2L4Nk4qPz6GPfyTyceOP/Lg2nitE=" {
		t.Fatalf("the first file's hash = %q", got)
	}
}

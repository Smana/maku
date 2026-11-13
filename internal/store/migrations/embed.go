// SPDX-License-Identifier: Apache-2.0

// Package migrations is the schema, embedded for room-broker migrate (adoption 3b). The same
// files feed cloud-native-ref's Atlas operator through kustomization.yaml.
package migrations

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

// FS holds every migration file, for Files and nothing else to read the directory at run time.
//
//go:embed *.sql
var FS embed.FS

// sum is atlas.sum, kept out of FS: migrate reads only its per-file hashes, the ones the Atlas
// operator records, to adopt a database the operator migrated (store.adopt).
//
//go:embed atlas.sum
var sum string

// File is one migration, as room-broker migrate sees it.
type File struct {
	Name    string
	Version string // the timestamp before the first underscore, the key in room_broker_revisions
	Body    string
	// AtlasHash is the file's atlas.sum hash, without its h1: prefix: what the Atlas operator's
	// ledger records as the revision's hash.
	AtlasHash string
	// NoTx is Atlas's "-- atlas:txmode none": the file runs a statement at a time, outside a
	// transaction, because CREATE INDEX CONCURRENTLY refuses one.
	NoTx bool
}

// Files lists the embedded migrations in apply order. It lives here, not in store, so its
// Docker-free test is not behind the store package's testcontainers TestMain.
func Files() ([]File, error) {
	names, err := fs.Glob(FS, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	hashes := atlasHashes(sum)
	out := make([]File, 0, len(names))
	for _, name := range names {
		body, err := fs.ReadFile(FS, name)
		if err != nil {
			return nil, err
		}
		out = append(out, File{
			Name:      name,
			Version:   strings.SplitN(name, "_", 2)[0],
			Body:      string(body),
			NoTx:      strings.HasPrefix(string(body), "-- atlas:txmode none"),
			AtlasHash: hashes[name],
		})
	}
	return out, nil
}

// atlasHashes reads atlas.sum's "<file> h1:<hash>" lines; its first line, the directory's own
// hash, names no file.
func atlasHashes(sum string) map[string]string {
	out := map[string]string{}
	for line := range strings.Lines(sum) {
		name, hash, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok {
			out[name] = strings.TrimPrefix(hash, "h1:")
		}
	}
	return out
}

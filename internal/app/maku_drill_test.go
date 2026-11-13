// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Smana/maku/internal/maku"
)

// `maku drill plant` needs no maku config; a hook-planted scenario without its hook ends with exit
// code 2, which cmd/maku returns.
func TestMakuDrillPlantExitsTwoWithoutAHook(t *testing.T) {
	var out bytes.Buffer
	err := Maku{Dir: t.TempDir(), Out: &out, Err: &out}.Run(t.Context(), []string{"drill", "plant", "--repo", "Smana/drill",
		"--factory", "https://factory.example", "--stranger-token-file", "s", "--maintainer-token-file", "m",
		"--factory-token-file", "f", "--runlore-hook", "/bin/true", "--tool-result-hook", "/bin/true"})
	var exit *maku.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("%v", err)
	}
	if err := (Maku{Dir: t.TempDir(), Out: &out, Err: &out}).Run(t.Context(), []string{"drill", "sow"}); err == nil {
		t.Fatal("an unknown drill command ran")
	}
}

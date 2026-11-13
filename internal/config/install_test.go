// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
	"testing"
)

// The install's unsafe settings (v4 D18, plan PR9): bounded codes the chart computes, shown to every
// signed-in human.
func TestInstallUnsafe(t *testing.T) {
	c, err := Load(write(t, valid))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Install.Unsafe) != 0 {
		t.Fatalf("unset is none: %v", c.Install.Unsafe)
	}
	all := []string{UnsafeAllowRunc, UnsafeNetworkPolicyNone, UnsafeSharedGitToken, UnsafeModelKeyInSandbox}
	c, err = Load(write(t, valid+"install:\n  unsafe: [unsafe_allow_runc, network_policy_none, shared_git_token, model_key_in_sandbox]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Install.Unsafe, all) {
		t.Fatalf("the four codes: %v", c.Install.Unsafe)
	}
	for _, body := range []string{"install:\n  unsafe: [bogus]\n",
		"install:\n  unsafe: [unsafe_allow_runc, unsafe_allow_runc]\n",
		"install:\n  unsafe: [private_code_unacknowledged]\n", // the factory's code, never the chart's (v4 D3)
	} {
		if _, err := Load(write(t, valid+body)); err == nil || !strings.Contains(err.Error(), "install.unsafe") {
			t.Fatalf("%q: got %v, want an error naming install.unsafe", body, err)
		}
	}
}

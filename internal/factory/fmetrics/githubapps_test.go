// SPDX-License-Identifier: Apache-2.0

package fmetrics

import (
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// D12: 1 for each permission an App's installation lacks, 0 once granted; both labels closed.
func TestGitHubAppPermissionMissingGauge(t *testing.T) {
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	s.GitHubAppPermissionMissing(t.Context(), "agents", "issues", true)
	s.GitHubAppPermissionMissing(t.Context(), "factory", "statuses", false)
	s.GitHubAppPermissionMissing(t.Context(), "someone", "workflows-and-more", true)
	body := scrape()
	for _, want := range []string{
		`maku_factory_github_app_permission_missing{app="agents",permission="issues"} 1`,
		`maku_factory_github_app_permission_missing{app="factory",permission="statuses"} 0`,
		`maku_factory_github_app_permission_missing{app="other",permission="other"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s\n%s", want, body)
		}
	}
}

// SPDX-License-Identifier: Apache-2.0

package runwatch

// The per-run token Secret (v4 step 3, D10, plan PR3): the factory's minter writes it, the
// run-controller mounts and checks it. Both sides read the contract from here.
const (
	// RunTokenSuffix ends a run's token Secret's name: <run name>-git.
	RunTokenSuffix = "-git"
	// RunTokenKey is the Secret key holding the token.
	RunTokenKey = "token"
	// RunTokenRunLabel names the run the Secret belongs to.
	RunTokenRunLabel = "maku.ogenki.io/run" //nolint:gosec // G101: a label name, not a credential
	// RunTokenManagedByLabel and RunTokenManagedBy mark the factory's Secret.
	RunTokenManagedByLabel = "app.kubernetes.io/managed-by" //nolint:gosec // G101: a label name, not a credential
	RunTokenManagedBy      = "maku-factory"
	// RunTokenExpiresAnnotation carries the token's expiry (RFC 3339).
	RunTokenExpiresAnnotation = "maku.ogenki.io/expires-at" //nolint:gosec // G101: an annotation name, not a credential
)

// RunTokenSecret is the name of run's token Secret.
func RunTokenSecret(run string) string { return run + RunTokenSuffix }

// SPDX-License-Identifier: Apache-2.0

// Package upkeep implements the autonomous upkeep routine engine's generic CI findings probe
// (actions-findings) and persistent operational state store (ConfigMap agent-factory-routine-state).
//
// Untrusted CI artifact input is strictly validated (ErrFindingsRefused on malformed input),
// sanitized with sanitize.Text, locally nonce-defanged, and fenced under FINDINGS-DATA-<nonce>.
// Artifact prose is never logged, traced, or emitted as a metric label.
package upkeep

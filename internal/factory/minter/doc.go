// SPDX-License-Identifier: Apache-2.0

// Package minter mints the factory's short-lived GitHub installation tokens for runs and for the
// publisher (v4 D10): one token per run, scoped to the run's one repository, with the permissions
// its role gets from Roles, the one role table (plan PR2, hardening ruling HR12). The table has
// two variants, TableHR12 and TableD10 (the spec's D10 as written); Roles names the one in force.
//
// No other code builds a run's permission map. Nothing here logs, counts or returns a token's
// text: errors name the HTTP status only, as forge's App calls do.
package minter

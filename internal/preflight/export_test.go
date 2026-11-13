// SPDX-License-Identifier: Apache-2.0

package preflight

// In tests a result outside the catalog panics, so a new code never ships without its catalog
// entry; production maps it to a fail undecided (TestNormalizeInProduction).
func init() { panicOnInvalid = true }

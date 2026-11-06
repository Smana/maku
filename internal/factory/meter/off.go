// SPDX-License-Identifier: Apache-2.0

package meter

import "context"

// Off is the Source of a factory with no metrics backend (demo mode): every run reads as no usage.
type Off struct{}

// RunUsage reports no usage and no error.
func (Off) RunUsage(context.Context) (map[string]Usage, error) { return map[string]Usage{}, nil }

// String names the source in logs.
func (Off) String() string { return "off (demo)" }

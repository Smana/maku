// SPDX-License-Identifier: Apache-2.0

package coordinationview

import "errors"

// The two ways a reader of the core's view fails: the task is no coordination parent (or does not
// exist), or the view could not be read. A consumer answers the first 404 and the second 503.
var (
	ErrNoCoordination          = errors.New("no coordination parent")
	ErrCoordinationUnavailable = errors.New("coordination view unavailable")
)

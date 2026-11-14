// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/go-github/v92/github"
)

// TokenSource is where a connection's GitHub token comes from at each call: the App key's
// installation tokens (Connect), or the factory's minter for the publisher (ConnectTokens). A source
// caches as it sees fit and returns a token with time left to live.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// invalidator is a TokenSource that can drop a token GitHub refused.
type invalidator interface{ Invalidate() }

// ErrNoConnection is a write with no connection to make it on: the repository has no factory App
// or publisher configured. A retry cannot change it.
var ErrNoConnection = errors.New("forge: no connection for the repository")

// Unappliable reports whether err is a refusal a retry cannot change: ErrNoConnection, a
// PermanentError, or GitHub answering 404, 410 or 422 (a validation failure). A 401, 403, 429, a
// 5xx and a network error are worth retrying.
func Unappliable(err error) bool {
	var p *PermanentError
	var rest *github.ErrorResponse
	var gql *statusError
	code := 0
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrNoConnection), errors.As(err, &p):
		return true
	case errors.As(err, &rest) && rest.Response != nil:
		code = rest.Response.StatusCode
	case errors.As(err, &gql):
		code = gql.code
	}
	return code == http.StatusNotFound || code == http.StatusGone || code == http.StatusUnprocessableEntity
}

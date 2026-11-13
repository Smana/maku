// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// maxComponentBytes bounds a component's /preflight answer: a few checks, each a short line.
const maxComponentBytes = 64 << 10

// Component is what a component serves at GET /preflight on its ops port: its name, its version and
// its checks' results, computed on demand.
type Component struct {
	Component string   `json:"component"`
	Version   string   `json:"version"`
	Checks    []Result `json:"checks"`
}

// DecodeComponent reads a /preflight answer strictly, bounded, and stamps each result with the
// component that served it.
func DecodeComponent(r io.Reader) (Component, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxComponentBytes+1))
	if err != nil {
		return Component{}, fmt.Errorf("preflight: read: %w", err)
	}
	if len(body) > maxComponentBytes {
		return Component{}, errors.New("preflight: the answer is over 64 KiB")
	}
	var c Component
	dec := json.NewDecoder(bytesReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Component{}, errors.New("preflight: the answer is not a /preflight document")
	}
	if c.Component == "" {
		return Component{}, errors.New("preflight: the answer names no component")
	}
	for i := range c.Checks {
		c.Checks[i].Component = c.Component
	}
	return c, nil
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

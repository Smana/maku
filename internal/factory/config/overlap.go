// SPDX-License-Identifier: Apache-2.0

package config

import (
	"encoding/json"
	"fmt"

	yamlv3 "go.yaml.in/yaml/v3"
)

// OverlapMode controls outcome-overlap evaluation and enforcement. Shadow records evidence without
// refusing work; enforce permits the separately selected enforcement stages.
type OverlapMode string

// The outcome-overlap rollout modes.
const (
	OverlapOff     OverlapMode = "off"
	OverlapShadow  OverlapMode = "shadow"
	OverlapEnforce OverlapMode = "enforce"
)

// UnmarshalJSON reads a mode string, or YAML 1.1's boolean false as off. Other booleans are not
// modes, so a mistaken `mode: true` cannot silently enable evaluation.
func (m *OverlapMode) UnmarshalJSON(b []byte) error {
	if string(b) == "false" {
		*m = OverlapOff
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("intake.overlap.mode is off, shadow or enforce: %s", b)
	}
	*m = OverlapMode(s)
	return nil
}

// Intake configures behavior shared by issue, sponsored API, and scheduled intake.
type Intake struct {
	Overlap Overlap `json:"overlap,omitzero"`
}

// Overlap is the outcome-overlap rollout switch. ScoperEnforcement is effective only in enforce
// mode; exact-active enforcement does not depend on it.
type Overlap struct {
	Mode              OverlapMode `json:"mode,omitempty"`
	ScoperEnforcement bool        `json:"scoperEnforcement,omitempty"`
}

// ApplyDefaults makes an omitted or zero-value mode explicitly off.
func (o *Overlap) ApplyDefaults() {
	if o.Mode == "" {
		o.Mode = OverlapOff
	}
}

// Validate accepts only modes the factory can honor. The empty mode remains valid here so a Config
// built directly, without Parse, has the same off semantics as an omitted block.
func (o Overlap) Validate(bad func(string, ...any)) {
	switch o.Mode {
	case "", OverlapOff, OverlapShadow, OverlapEnforce:
	default:
		bad("intake.overlap.mode %q is none of off, shadow, enforce", o.Mode)
	}
}

// checkOverlapBlocks protects the nested rollout switch from YAML's last-key-wins conversion.
// Malformed YAML is left to YAMLToJSON, which reports the parse error with its usual context.
func checkOverlapBlocks(raw []byte) error {
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 {
		return nil //nolint:nilerr // YAMLToJSON reports malformed input after this duplicate-key check
	}
	intake, err := uniqueMappingValue(doc.Content[0], "intake", "intake")
	if err != nil {
		return err
	}
	overlap, err := uniqueMappingValue(intake, "overlap", "intake.overlap")
	if err != nil {
		return err
	}
	for _, key := range []string{"mode", "scoperEnforcement"} {
		if _, err := uniqueMappingValue(overlap, key, "intake.overlap."+key); err != nil {
			return err
		}
	}
	return nil
}

// uniqueMappingValue finds key in a mapping and refuses a second occurrence. Non-mapping and absent
// values need no duplicate-key check here; strict JSON decoding handles their shape.
func uniqueMappingValue(mapping *yamlv3.Node, key, path string) (*yamlv3.Node, error) {
	if mapping == nil || mapping.Kind != yamlv3.MappingNode {
		return nil, nil
	}
	var value *yamlv3.Node
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != key {
			continue
		}
		if value != nil {
			return nil, fmt.Errorf("%s is written twice", path)
		}
		value = mapping.Content[i+1]
	}
	return value, nil
}

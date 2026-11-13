// SPDX-License-Identifier: Apache-2.0

package drills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/hardening"
)

// The manifest's ConfigMap: one per drill, named drill-<id>, labelled ManifestLabel, its manifest
// under manifestKey. The runner annotates it v1alpha1.AnnotationDrillScored once scored.
const (
	ManifestLabel = "agents.ogenki.io/drill-manifest"
	manifestKey   = "manifest.json"
	// maxManifest bounds a manifest: a few dozen planted tasks.
	maxManifest = 16 << 10
)

var (
	// ErrManifestExists is a second, different manifest under a drill id: each drill is accepted
	// once. The same manifest again is accepted as it was.
	ErrManifestExists = errors.New("drills: a different manifest is already accepted under this id")

	drillIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// Manifest is what a drill planter hands the factory (HR38): the drill's id and, for each scenario
// it planted, the task the planted issue became and the scenario's fresh marker.
type Manifest struct {
	ID        string    `json:"id"`
	Scenarios []Planted `json:"scenarios"`
}

// Planted is one scenario's planted task.
type Planted struct {
	Scenario string `json:"scenario"`
	Task     string `json:"task"`
	Marker   string `json:"marker"`
}

// ParseManifest decodes a manifest strictly and holds it to suite: a drill id, one to as many
// entries as the suite has scenarios, each a suite scenario once, a C2 task id and a 16-hex marker.
func ParseManifest(b []byte, suite hardening.Suite) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil || dec.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, errors.New("drills: a manifest is one JSON object of id and scenarios")
	}
	return m, m.Validate(suite)
}

// Validate holds m to suite (see ParseManifest).
func (m Manifest) Validate(suite hardening.Suite) error {
	if !drillIDRE.MatchString(m.ID) {
		return errors.New("drills: a drill id is lowercase letters, digits and dashes, at most 63")
	}
	if len(m.Scenarios) == 0 || len(m.Scenarios) > len(suite.Scenarios) {
		return fmt.Errorf("drills: a manifest plants 1 to %d scenarios", len(suite.Scenarios))
	}
	known := map[string]bool{}
	for _, sc := range suite.Scenarios {
		known[sc.ID] = true
	}
	seen := map[string]bool{}
	for _, p := range m.Scenarios {
		switch {
		case !known[p.Scenario] || seen[p.Scenario]:
			return fmt.Errorf("drills: scenario %q is not the suite's, or is planted twice", p.Scenario)
		case !runIDRE.MatchString(p.Task):
			return errors.New("drills: a planted task is a C2 task id")
		case !markerRE.MatchString(p.Marker):
			return errors.New("drills: a marker is 16 hex digits")
		}
		seen[p.Scenario] = true
	}
	return nil
}

// ConfigMapName is the drill's manifest ConfigMap.
func ConfigMapName(id string) string { return "drill-" + id }

// Accept stores m in namespace once: created reports whether this call stored it. The same
// manifest again is accepted (a planter's retry); a different one is ErrManifestExists.
func Accept(ctx context.Context, c client.Client, namespace string, m Manifest) (created bool, err error) {
	b, err := json.Marshal(m)
	if err != nil || len(b) > maxManifest {
		return false, errors.New("drills: the manifest is over 16 KiB")
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: ConfigMapName(m.ID), Namespace: namespace,
		Labels: map[string]string{ManifestLabel: "true"}}, Data: map[string]string{manifestKey: string(b)}}
	switch err := c.Create(ctx, cm); {
	case err == nil:
		return true, nil
	case !apierrors.IsAlreadyExists(err):
		return false, fmt.Errorf("drills: store the manifest: %w", err)
	}
	var have corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cm.Name}, &have); err != nil {
		return false, fmt.Errorf("drills: read the manifest: %w", err)
	}
	if have.Labels[ManifestLabel] != "true" || have.Data[manifestKey] != string(b) {
		return false, ErrManifestExists
	}
	return false, nil
}

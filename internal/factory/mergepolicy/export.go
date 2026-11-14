// SPDX-License-Identifier: Apache-2.0

package mergepolicy

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/config"
)

// ErrLegacyServed is Export's refusal of a repository whose classes the config file declares: the
// file binds central policies, which only a Repository object names.
var ErrLegacyServed = errors.New("the repository's classes are the config file's, and the file binds central policies: " +
	"migrate the repository first: maku config migrate")

// Export writes a repository's policy file in the binding shape (B15, B16): its Repository's
// bindings as written, so the file resolves to the classes the factory serves. A repository the
// config file serves is refused (ErrLegacyServed); one with no class is a fence-only file.
func Export(rc *config.Repository, c *config.Config) (*File, error) {
	f := &File{Factory: c.Factory}
	if rc.Bindings == nil && len(rc.Classes) > 0 {
		return nil, ErrLegacyServed
	}
	for i := range rc.Bindings {
		f.MergePolicies = append(f.MergePolicies, *rc.Bindings[i].DeepCopy())
	}
	return f, nil
}

// Marshal writes the file. It is read back through Parse by the caller's test, never trusted.
func Marshal(f *File) ([]byte, error) { return yaml.Marshal(f) }

// Compare lists how the repository's classes differ from the central ones, class by class, with
// path and check lists read as sets (B15, stages 1 and 3): each binding resolved for rc, against
// the class the factory serves; a binding that does not resolve is a divergence. review is
// compared on its evaluation keys alone (R30): a central review with no floor is implicit, and the
// file not binding it is no divergence.
func Compare(rc *config.Repository, f *File, c *config.Config) []string {
	classes, refused := ResolveBindings(f, rc, c)
	names := map[string]bool{}
	for n := range rc.Classes {
		names[n] = true
	}
	for _, b := range f.MergePolicies {
		names[b.Name] = true
	}
	if !declares(f, "review") && rc.Classes["review"].Harness == "" {
		delete(names, "review")
	}
	var out []string
	for _, name := range slices.Sorted(maps.Keys(names)) {
		central, inCentral := rc.Classes[name]
		r, resolved := classes[name]
		refusal, isRefused := refused[name]
		switch {
		case isRefused:
			out = append(out, fmt.Sprintf("%s: refused, %s: %s", name, refusal.Reason, refusal.Message))
		case !resolved:
			out = append(out, name+": only in the central config")
		case !inCentral:
			out = append(out, name+": only in the repository")
		default:
			if a, b := canonical(central), canonical(r.Class); a != b {
				out = append(out, fmt.Sprintf("%s: central %s, repository %s", name, a, b))
			}
		}
	}
	return out
}

// canonical is a class's JSON with its sets sorted, for comparison and for the message.
func canonical(cl config.Class) string {
	for _, list := range []*[]string{&cl.AllowedFiles, &cl.ProtectedFiles, &cl.HoldoutChecks, &cl.CompanionFiles} {
		*list = slices.Sorted(slices.Values(*list))
	}
	b, err := json.Marshal(cl)
	if err != nil { // strings, ints, bools and Durations: Marshal cannot fail
		return err.Error()
	}
	return string(b)
}

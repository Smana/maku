// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/roomview"
	"github.com/Smana/maku/internal/runrequest"
)

// ClassesList is the name of the introspection tool (P11): what a repository offers at intake.
const ClassesList = "classes_list"

// classesListSchema is the tool's input: the repository, a class for its detail, and, for a
// sponsored principal alone, the Slack user it reads for.
var classesListSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["repository"],"properties":{` +
	`"repository":{"type":"string","pattern":"^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$"},` +
	`"class":{"type":"string","pattern":"^[a-z0-9]([-a-z0-9]{0,62}[a-z0-9])?$"},` +
	`"on_behalf_of":{"type":"string","pattern":"^slack:T[A-Z0-9]{2,20}/[UW][A-Z0-9]{2,20}$"}}}`)

// classesListOutput declares the structured answer: the catalogue, or, with class, the catalogue cut
// to that class and the profiles it names.
var classesListOutput = json.RawMessage(`{"type":"object","required":["repository","maintainer","classes","profiles"],"properties":{` +
	`"repository":{"type":"string"},"maintainer":{"type":"boolean"},"approveToMerge":{"type":"boolean"},` +
	`"generations":{"type":"object","properties":{"factory":{"type":"integer"},"repository":{"type":"integer"},"mergePolicy":{"type":"integer"},"mergePolicies":{"type":"object","additionalProperties":{"type":"integer"}}}},` +
	`"evaluation":{"type":"object","properties":{"mode":{"type":"string"},"controlPercent":{"type":"integer"}}},` +
	`"classes":{"type":"array","items":{"type":"object","required":["name","mode","floor"],"properties":{"name":{"type":"string"},"description":{"type":"string"},` +
	`"mode":{"type":"string"},"ceilingUSD":{"type":"number"},"floor":{"type":"string"},"allowed":{"type":"array","items":{"type":"string"}}}}},` +
	`"profiles":{"type":"object"}}}`)

// classesListTool is the tool for p. The catalogue is the factory's, read with the caller's own
// token: a human reads it as themselves, the Slack App for the user it names, and the factory
// answers "not found" for a repository the caller cannot read, so this tool names nothing either.
func (s *Server) classesListTool() mcpTool {
	return mcpTool{name: ClassesList, call: s.classesList, schema: classesListSchema, output: classesListOutput,
		description: "What a repository offers at intake: its classes, each with how it merges and the harness profile it runs, " +
			"as lines to show as they are (text) and the same as structuredContent. With class, that class's profiles: the floor, the variants, " +
			"each profile's roles, tier, tests-first, loop and description, the escalation targets, and whether you may pin one (only a maintainer may). " +
			"A profile is pinned by naming it in a plan revision, never here; this tool changes nothing."}
}

func (s *Server) classesList(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
	var a struct {
		Repository string `json:"repository"`
		Class      string `json:"class"`
		OnBehalfOf string `json:"on_behalf_of"`
	}
	if mcpwire.DecodeArgs(args, &a) != nil || !repositoryRE.MatchString(a.Repository) || (a.Class != "" && !envelope.ValidClassName(a.Class)) {
		return nil, toolError("invalid_arguments: repository is owner/name; class is a class name")
	}
	if err := claims(p, a.OnBehalfOf, 0); err != nil {
		return nil, err
	}
	c, err := s.Catalogue.Get(ctx, p, a.Repository, a.OnBehalfOf)
	if err != nil {
		return nil, catalogueRefusal(err)
	}
	if a.Class != "" {
		cl, ok := c.Class(a.Class)
		if !ok {
			return nil, toolError("no_such_class: " + a.Repository + " offers no class of that name; call classes_list without class")
		}
		c = only(c, cl)
	}
	return viewResult{text: roomview.CatalogueText(c, a.Class), data: c}, nil
}

// only is c cut to class cl and the profiles it names.
func only(c catalogue.Catalogue, cl catalogue.Class) catalogue.Catalogue {
	out := c
	out.Classes = []catalogue.Class{cl}
	out.Profiles = map[string]catalogue.Profile{}
	for _, n := range slices.Concat([]string{cl.Floor}, cl.Allowed) {
		if p, ok := c.Profiles[n]; ok {
			out.Profiles[n] = p
		}
	}
	return out
}

// catalogueRefusal is the sentence a model may read for a failed catalogue read: a refusal the
// factory meant, in the broker's words, or catalogue_unavailable.
func catalogueRefusal(err error) error {
	var ref runrequest.ErrRefused
	switch {
	case errors.As(err, &ref) && ref.Status == http.StatusNotFound:
		return toolError("not_found: no such repository, or you cannot read it")
	case errors.As(err, &ref) && ref.Reason == "not_linked":
		return toolError("not_linked: link your GitHub account to read what the factory offers")
	case errors.As(err, &ref) && ref.Status < http.StatusInternalServerError:
		return toolError("not_permitted: the factory does not show its classes to this caller")
	case errors.Is(err, runrequest.ErrForbidden):
		return toolError("not_permitted: the factory does not show its classes to this caller")
	}
	return toolError("catalogue_unavailable: The factory's list of classes could not be read; try again in a minute.")
}

// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/factory/sanitize"
)

// Revise verifies a harness-edited candidate against the plan the broker last returned. The base
// hash is checked before candidate content is read. Material may change only by replacing existing
// clarification markers with non-empty text and adding their same-issue D/A records; all other
// material and every settled record stays verbatim. New record numbers form one global sequence in
// issue order, while each body retains only its own ascending subset.
//
// The result recomputes the remaining ranked gaps from the base, advances the round and renews the
// material hash. On the cap-reaching round it also replaces every remaining marker with its
// recommendation and appends an A record, without making a model call. A revision above the cap is
// planner_invalid; base drift is plan_changed.
func Revise(base Plan, baseHash string, candidate Plan, maxRounds int) (Plan, error) {
	if Hash(base) != baseHash {
		return Plan{}, refuse(ReasonPlanChanged, "")
	}
	if maxRounds < 1 || base.Round < 0 {
		return Plan{}, refuse(ReasonPlannerInvalid, "revision round and maxRounds must be positive")
	}
	baseRound := base.Round
	if baseRound == 0 {
		baseRound = 1
	}
	if baseRound >= maxRounds {
		return Plan{}, refuse(ReasonPlannerInvalid, "the revision would exceed maxRounds")
	}
	round := baseRound + 1
	if detail := revisionMaterialChanged(base, candidate); detail != "" {
		return Plan{}, refuse(ReasonPlannerInvalid, detail)
	}
	gaps, err := revisionBaseGaps(base)
	if err != nil {
		return Plan{}, refuse(ReasonPlannerInvalid, err.Error())
	}

	edits := make([]revisionBodyEdit, len(base.Issues))
	lastNumber := 0
	for i := range base.Issues {
		edit, err := analyzeRevisionBody(base.Issues[i].Body, candidate.Issues[i].Body)
		if err != nil {
			return Plan{}, refuse(ReasonPlannerInvalid, fmt.Sprintf("issue %d: %v", i+1, err))
		}
		edits[i] = edit
		for _, record := range edit.baseRecords {
			lastNumber = max(lastNumber, record.Number)
		}
	}

	// Records are numbered in issue order, then in their order within the body. A record's gap
	// reference is also its correlation proof: it must name a marker resolved in this same issue,
	// and every resolved marker must be named exactly once.
	for i := range edits {
		for _, record := range edits[i].newRecords {
			if record.Gap == "" || strings.ContainsAny(record.Text, "\r\n") {
				return Plan{}, refuse(ReasonPlannerInvalid, "new intake records need a gap and one-line text")
			}
			if _, resolved := edits[i].replacements[record.Gap]; !resolved {
				return Plan{}, refuse(ReasonPlannerInvalid, fmt.Sprintf("issue %d has a record without a same-issue resolved marker", i+1))
			}
			if edits[i].recorded[record.Gap] {
				return Plan{}, refuse(ReasonPlannerInvalid, fmt.Sprintf("issue %d has more than one record for a resolved marker", i+1))
			}
			edits[i].recorded[record.Gap] = true
			if lastNumber == math.MaxInt {
				return Plan{}, refuse(ReasonPlannerInvalid, "the global Decision number overflows int")
			}
			lastNumber++
			if record.Number != lastNumber {
				return Plan{}, refuse(ReasonPlannerInvalid, "new Decision numbers must follow the global sequence in issue order")
			}
		}
		for id := range edits[i].replacements {
			if !edits[i].recorded[id] {
				return Plan{}, refuse(ReasonPlannerInvalid, fmt.Sprintf("issue %d resolved a marker without its record", i+1))
			}
		}
	}

	out := cloneRevision(candidate)
	if round == maxRounds {
		var err error
		out, _, err = acceptRevisionRemainder(out, edits, gaps, lastNumber)
		if err != nil {
			return Plan{}, refuse(ReasonPlannerInvalid, err.Error())
		}
		out = WithGaps(out, nil)
	} else {
		remaining := make(map[string]bool)
		for _, edit := range edits {
			for _, marker := range edit.remaining {
				remaining[marker.ID] = true
			}
		}
		ranked := make([]Gap, 0, len(remaining))
		for _, gap := range base.Gaps {
			if remaining[gap.ID] {
				ranked = append(ranked, gap)
			}
		}
		out = WithGaps(out, ranked)
	}
	out.Round = round
	out.RoundsLeft = maxRounds - round
	out.Hash = Hash(out)
	return out, nil
}

// CheckCarryForward holds a model re-plan to the edits already verified in candidate. Replacement
// text from base's resolved markers must still occur verbatim in the same issue, and every D/A
// record present in candidate must survive unchanged and in order there. The model may rewrite
// unsettled prose and refine remaining questions; it may not rewrite intake history.
func CheckCarryForward(base, candidate, next Plan) *Refusal {
	if len(base.Issues) != len(candidate.Issues) || len(next.Issues) < len(candidate.Issues) {
		return refuse(ReasonPlannerInvalid, "the model re-plan changed the settled issue set")
	}
	for i := range candidate.Issues {
		edit, err := analyzeRevisionBody(base.Issues[i].Body, candidate.Issues[i].Body)
		if err != nil {
			return refuse(ReasonPlannerInvalid, "the verified revision could not be replayed")
		}
		nextCore := next.Issues[i].Body
		if decision, found := revisionDecisionOffset(nextCore); found {
			nextCore = nextCore[:decision]
		}
		for _, replacement := range edit.replacements {
			if !strings.Contains(nextCore, replacement) {
				return refuse(ReasonPlannerInvalid, "the model re-plan changed settled replacement text")
			}
		}
		nextRecords, err := sanitize.DecisionRecords(next.Issues[i].Body)
		if err != nil {
			return refuse(ReasonPlannerInvalid, "the model re-plan has invalid Decisions")
		}
		at := 0
		for _, settled := range edit.records {
			for at < len(nextRecords) && nextRecords[at] != settled {
				at++
			}
			if at == len(nextRecords) {
				return refuse(ReasonPlannerInvalid, "the model re-plan changed an existing D/A record")
			}
			at++
		}
	}
	return nil
}

// revisionMaterialChanged names the first material field outside a body that candidate changed.
// Display metadata is deliberately absent: Revise owns gaps, rounds and the hash, and Hash does not
// cover the remaining display fields.
func revisionMaterialChanged(base, candidate Plan) string {
	if candidate.APIVersion != base.APIVersion {
		return "candidate apiVersion differs from the verified base"
	}
	if candidate.Repository != base.Repository {
		return "candidate repository differs from the verified base"
	}
	if candidate.Budget != base.Budget {
		return "candidate budget differs from the verified base"
	}
	if len(candidate.Issues) != len(base.Issues) {
		return "candidate issue count or order differs from the verified base"
	}
	for i := range base.Issues {
		before, after := base.Issues[i], candidate.Issues[i]
		if after.Index != before.Index || after.Title != before.Title || after.Class != before.Class ||
			!slices.Equal(after.Roles, before.Roles) || !slices.Equal(after.DependsOn, before.DependsOn) {
			return fmt.Sprintf("candidate issue %d changed protected material", i+1)
		}
	}
	return ""
}

// revisionBaseGaps indexes the verified base's display recommendations and proves that its markers
// and gaps are two views of the same plan-wide set. Marker IDs are unique across issues, not merely
// within one body, because a new record's gap reference is resolved across this index.
func revisionBaseGaps(base Plan) (map[string]Gap, error) {
	gaps := make(map[string]Gap, len(base.Gaps))
	for _, gap := range base.Gaps {
		if _, duplicate := gaps[gap.ID]; duplicate {
			return nil, fmt.Errorf("the verified base repeats a gap ID")
		}
		gaps[gap.ID] = gap
	}
	markers := make(map[string]bool, len(gaps))
	for _, issue := range base.Issues {
		parsed, err := sanitize.ClarificationMarkers(issue.Body)
		if err != nil {
			return nil, fmt.Errorf("the verified base has an invalid clarification marker")
		}
		for _, marker := range parsed {
			if markers[marker.ID] {
				return nil, fmt.Errorf("the verified base repeats a marker ID across issues")
			}
			markers[marker.ID] = true
			if _, ok := gaps[marker.ID]; !ok {
				return nil, fmt.Errorf("the verified base has a marker without its ranked gap")
			}
		}
	}
	if len(markers) != len(gaps) {
		return nil, fmt.Errorf("the verified base has a ranked gap without its marker")
	}
	return gaps, nil
}

// revisionBodyEdit is the verified difference between two issue bodies. replacements contains only
// markers that disappeared; remaining points into candidate's body. records includes both settled
// and new records, while newRecords is the suffix after baseRecords.
type revisionBodyEdit struct {
	replacements map[string]string
	remaining    []sanitize.ClarificationMarker
	baseRecords  []sanitize.DecisionRecord
	records      []sanitize.DecisionRecord
	newRecords   []sanitize.DecisionRecord
	recorded     map[string]bool
}

// analyzeRevisionBody proves one body's edit. Outside a Decisions section, the base and candidate
// are the same byte string except that some base marker spans became non-empty text. An existing
// Decisions section is a verbatim prefix of candidate's; a new one may be introduced only with new
// records. Byte offsets from ClarificationMarkers keep Unicode prefixes out of the arithmetic.
func analyzeRevisionBody(baseBody, candidateBody string) (revisionBodyEdit, error) {
	edit := revisionBodyEdit{replacements: make(map[string]string), recorded: make(map[string]bool)}
	baseMarkers, err := sanitize.ClarificationMarkers(baseBody)
	if err != nil {
		return edit, fmt.Errorf("the base has an invalid clarification marker")
	}
	candidateMarkers, err := sanitize.ClarificationMarkers(candidateBody)
	if err != nil {
		return edit, fmt.Errorf("candidate has an invalid clarification marker")
	}
	edit.remaining = candidateMarkers
	baseRecords, err := sanitize.DecisionRecords(baseBody)
	if err != nil {
		return edit, fmt.Errorf("the base has invalid Decisions")
	}
	candidateRecords, err := sanitize.DecisionRecords(candidateBody)
	if err != nil {
		return edit, fmt.Errorf("candidate has invalid Decisions")
	}
	edit.baseRecords, edit.records = baseRecords, candidateRecords

	baseDecision, baseHasDecision := revisionDecisionOffset(baseBody)
	candidateDecision, candidateHasDecision := revisionDecisionOffset(candidateBody)
	if baseHasDecision && !candidateHasDecision {
		return edit, fmt.Errorf("candidate removed the Decisions section")
	}
	if len(candidateRecords) < len(baseRecords) {
		return edit, fmt.Errorf("candidate removed a settled D/A record")
	}
	for i, record := range baseRecords {
		if candidateRecords[i] != record {
			return edit, fmt.Errorf("candidate rewrote or reordered a settled D/A record")
		}
	}
	edit.newRecords = candidateRecords[len(baseRecords):]
	if baseHasDecision {
		if !strings.HasPrefix(candidateBody[candidateDecision:], baseBody[baseDecision:]) {
			return edit, fmt.Errorf("candidate changed settled Decisions text")
		}
		if candidateBody[candidateDecision:] != baseBody[baseDecision:] && len(edit.newRecords) == 0 {
			return edit, fmt.Errorf("candidate changed Decisions without adding a record")
		}
	} else if candidateHasDecision && len(edit.newRecords) == 0 {
		return edit, fmt.Errorf("candidate added an empty Decisions section")
	}

	baseCore, candidateCore := baseBody, candidateBody
	if baseHasDecision {
		baseCore = baseBody[:baseDecision]
	}
	if candidateHasDecision {
		candidateCore = candidateBody[:candidateDecision]
	}
	for _, marker := range baseMarkers {
		if marker.End > len(baseCore) {
			return edit, fmt.Errorf("a base marker inside Decisions cannot be revised")
		}
	}
	candidateByID := make(map[string]sanitize.ClarificationMarker, len(candidateMarkers))
	for _, marker := range candidateMarkers {
		if _, ok := candidateByID[marker.ID]; ok {
			return edit, fmt.Errorf("candidate repeats a marker ID")
		}
		candidateByID[marker.ID] = marker
	}
	for id := range candidateByID {
		if !slices.ContainsFunc(baseMarkers, func(marker sanitize.ClarificationMarker) bool { return marker.ID == id }) {
			return edit, fmt.Errorf("candidate introduced a clarification marker")
		}
	}

	// A newly introduced heading needs its line-break separator. Try the longest trailing newline
	// separator first so it is not captured as part of the last answer; the base's own trailing
	// newlines remain mandatory and make the shorter attempts succeed when needed.
	allowSeparator := !baseHasDecision && candidateHasDecision
	firstNewline := len(candidateCore)
	for firstNewline > 0 && candidateCore[firstNewline-1] == '\n' {
		firstNewline--
	}
	start, end := len(candidateCore), len(candidateCore)+1
	if allowSeparator {
		start = firstNewline
	}
	var lastErr error
	for coreEnd := start; coreEnd < end; coreEnd++ {
		replacements, err := matchRevisionCore(baseCore, candidateCore[:coreEnd], baseMarkers, candidateByID)
		if err == nil {
			edit.replacements = replacements
			return edit, nil
		}
		lastErr = err
	}
	return edit, lastErr
}

// matchRevisionCore matches candidateCore against a pattern made only of base literals, unchanged
// markers and one non-empty capture per resolved marker. Candidate's parsed marker set decides which
// alternative applies; unknown or reordered markers cannot be hidden inside a capture.
func matchRevisionCore(baseCore, candidateCore string, baseMarkers []sanitize.ClarificationMarker,
	candidateMarkers map[string]sanitize.ClarificationMarker) (map[string]string, error) {
	var pattern strings.Builder
	pattern.WriteString(`(?s)^`)
	position, captures := 0, 0
	for _, marker := range baseMarkers {
		pattern.WriteString(regexp.QuoteMeta(baseCore[position:marker.Start]))
		if _, remains := candidateMarkers[marker.ID]; remains {
			pattern.WriteString(regexp.QuoteMeta(baseCore[marker.Start:marker.End]))
		} else {
			pattern.WriteString(`(.+)`)
			captures++
		}
		position = marker.End
	}
	pattern.WriteString(regexp.QuoteMeta(baseCore[position:]))
	pattern.WriteString(`\z`)
	re, err := regexp.Compile(pattern.String())
	if err != nil {
		return nil, fmt.Errorf("compile the protected body comparison: %w", err)
	}
	matches := re.FindStringSubmatchIndex(candidateCore)
	if matches == nil {
		return nil, fmt.Errorf("candidate changed text outside an existing marker")
	}
	replacements := make(map[string]string, captures)
	capture := 0
	for _, marker := range baseMarkers {
		if _, remains := candidateMarkers[marker.ID]; remains {
			continue
		}
		start, end := matches[2+capture*2], matches[3+capture*2]
		replacement := candidateCore[start:end]
		if strings.TrimSpace(replacement) == "" {
			return nil, fmt.Errorf("candidate replaced a marker with empty text")
		}
		replacements[marker.ID] = replacement
		capture++
	}
	return replacements, nil
}

// revisionDecisionOffset returns the byte offset of the one Decisions heading. DecisionRecords has
// already rejected repeated or misplaced sections; trimming a line here follows that parser's own
// heading rule without searching inside record text.
func revisionDecisionOffset(body string) (int, bool) {
	for start := 0; start < len(body); {
		end := strings.IndexByte(body[start:], '\n')
		if end < 0 {
			end = len(body)
		} else {
			end += start
		}
		if strings.TrimSpace(body[start:end]) == "## Decisions" {
			return start, true
		}
		if end == len(body) {
			break
		}
		start = end + 1
	}
	return 0, false
}

// acceptRevisionRemainder applies every remaining base recommendation at the cap. Numbers continue
// the candidate's already verified global sequence. Replacements run backwards over the original
// byte offsets; records append forwards, so both positions and per-body numbering stay stable.
func acceptRevisionRemainder(out Plan, edits []revisionBodyEdit, gaps map[string]Gap, lastNumber int) (Plan, int, error) {
	type automatic struct {
		marker sanitize.ClarificationMarker
		answer string
		record string
	}
	for i := range out.Issues {
		added := make([]automatic, 0, len(edits[i].remaining))
		for _, marker := range edits[i].remaining {
			gap := gaps[marker.ID]
			if strings.TrimSpace(gap.Recommended.Answer) == "" || strings.ContainsAny(gap.Recommended.Answer+gap.Recommended.Why, "\r\n") {
				return Plan{}, lastNumber, fmt.Errorf("a remaining recommendation cannot become a one-line Assumption")
			}
			if lastNumber == math.MaxInt {
				return Plan{}, lastNumber, fmt.Errorf("the global Decision number overflows int")
			}
			lastNumber++
			text := "assumed " + gap.Recommended.Answer
			if strings.TrimSpace(gap.Recommended.Why) != "" {
				text += ", because " + gap.Recommended.Why
			}
			added = append(added, automatic{marker: marker, answer: gap.Recommended.Answer,
				record: fmt.Sprintf("A%d [%s]: %s", lastNumber, marker.ID, text)})
		}
		body := out.Issues[i].Body
		for j := len(added) - 1; j >= 0; j-- {
			marker := added[j].marker
			body = body[:marker.Start] + added[j].answer + body[marker.End:]
		}
		for _, assumption := range added {
			body = appendRevisionRecord(body, assumption.record)
		}
		if _, err := sanitize.DecisionRecords(body); err != nil {
			return Plan{}, lastNumber, fmt.Errorf("automatic Assumptions do not form valid Decisions")
		}
		markers, err := sanitize.ClarificationMarkers(body)
		if err != nil || len(markers) != 0 {
			return Plan{}, lastNumber, fmt.Errorf("a recommendation left an unresolved clarification marker")
		}
		out.Issues[i].Body = body
	}
	return out, lastNumber, nil
}

// appendRevisionRecord adds one record, creating the optional section when needed, without
// trimming or otherwise normalizing any settled byte before it.
func appendRevisionRecord(body, record string) string {
	if _, found := revisionDecisionOffset(body); !found {
		switch {
		case strings.HasSuffix(body, "\n\n"):
			body += "## Decisions\n"
		case strings.HasSuffix(body, "\n"):
			body += "\n## Decisions\n"
		default:
			body += "\n\n## Decisions\n"
		}
	} else if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + record
}

// cloneRevision gives cap conversion its own issue slice. Strings and the protected role/dependency
// slices are never written, but cloning those slices as well keeps the returned plan independent of
// later caller edits to candidate's fields.
func cloneRevision(p Plan) Plan {
	p.Issues = slices.Clone(p.Issues)
	for i := range p.Issues {
		p.Issues[i].Roles = slices.Clone(p.Issues[i].Roles)
		p.Issues[i].DependsOn = slices.Clone(p.Issues[i].DependsOn)
	}
	return p
}

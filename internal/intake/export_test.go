// SPDX-License-Identifier: Apache-2.0

package intake

import "maps"

// Sentences is the closed list's one sentence per reason, for a test that holds every one of them to
// its shape: a reason with no sentence, a sentence with no reason, or a placeholder a reader would
// see all fail there rather than in a person's Slack thread.
func Sentences() map[string]string { return maps.Clone(sentences) }

// RefusalFor is what refuse emits, so a test reads exactly the line a person would.
func RefusalFor(reason, detail string) *Refusal { return refuse(reason, detail) }

// SetPublicityMaxEntries lowers p's cache cap, so a test can fill it without 10 000 repositories.
func SetPublicityMaxEntries(p *CachedPublicity, n int) { p.maxEntries = n }

// PublicityEntries is how many answers p holds.
func PublicityEntries(p *CachedPublicity) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cache)
}

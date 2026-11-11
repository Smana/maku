// SPDX-License-Identifier: Apache-2.0

package envelope

import (
	"regexp"
	"strings"
)

// The end reasons of a run whose model provider refused it: a platform condition, not the agent's
// error. The reason names the cause so the room, the summary and the issue comment can say it.
const (
	EndModelBilling = "model_unavailable_billing"
	EndModelAuth    = "model_unavailable_auth"
	EndModelQuota   = "model_unavailable_quota"
)

// ModelUnavailable reports whether reason is one of the provider-refusal end reasons.
func ModelUnavailable(reason string) bool {
	return reason == EndModelBilling || reason == EndModelAuth || reason == EndModelQuota
}

// ModelRefusalCause is the cause word of a provider-refusal end reason: billing, auth or quota; ""
// for any other reason.
func ModelRefusalCause(reason string) string {
	if !ModelUnavailable(reason) {
		return ""
	}
	return strings.TrimPrefix(reason, "model_unavailable_")
}

var (
	billingText = regexp.MustCompile(`(?i)insufficient (balance|funds|credit)|no resource package|recharge|billing|out of credits?|payment required|\b402\b`)
	authText    = regexp.MustCompile(`(?i)\b40[13]\b|unauthori[sz]ed|forbidden|invalid api[ _-]?key|incorrect api key|authentication`)
	quotaText   = regexp.MustCompile(`(?i)\b429\b|quota|rate[ _-]?limit|too many requests`)
)

// ModelRefusal classifies the harness_error a run ended on (litellm's exception class as code, its
// message as detail) as a model provider's refusal, and returns the end reason it maps to, or ""
// when the error is not one. Billing text wins over a rate-limit class: Z.ai answers an empty wallet
// as a RateLimitError. The detail is the harness's own text and is only matched, never repeated.
func ModelRefusal(code, detail string) string {
	switch {
	case billingText.MatchString(detail):
		return EndModelBilling
	case strings.Contains(code, "AuthenticationError"), strings.Contains(code, "PermissionDeniedError"), authText.MatchString(detail):
		return EndModelAuth
	case strings.Contains(code, "RateLimitError"), quotaText.MatchString(detail):
		return EndModelQuota
	}
	return ""
}

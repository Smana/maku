// SPDX-License-Identifier: Apache-2.0

package envelope

import "testing"

func TestModelRefusalClassifiesProviderErrors(t *testing.T) {
	for _, c := range []struct{ name, code, detail, want string }{
		{"the live Z.ai wallet refusal", "LLMRateLimitError", "litellm.RateLimitError: Insufficient balance or no resource package. Please recharge.", EndModelBilling},
		{"a rate limit with no billing text", "LLMRateLimitError", "litellm.RateLimitError: slow down", EndModelQuota},
		{"a quota in the text", "LLMBadRequestError", "monthly quota exceeded", EndModelQuota},
		{"an authentication class", "LLMAuthenticationError", "bad key", EndModelAuth},
		{"a 401 in the text", "LLMServiceUnavailableError", "HTTP 401 Unauthorized", EndModelAuth},
		{"a 403", "LLMError", "403 Forbidden", EndModelAuth},
		{"an outage is not a refusal", "LLMServiceUnavailableError", "connection reset", ""},
		{"a stall is not a refusal", "harness_stalled", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := ModelRefusal(c.code, c.detail)
			if got != c.want {
				t.Fatalf("ModelRefusal(%q, %q) = %q, want %q", c.code, c.detail, got, c.want)
			}
			if (got != "") != ModelUnavailable(got) {
				t.Fatalf("ModelUnavailable(%q) disagrees", got)
			}
		})
	}
	if ModelRefusalCause(EndModelBilling) != "billing" || ModelRefusalCause("agent_error") != "" {
		t.Fatal("ModelRefusalCause")
	}
}

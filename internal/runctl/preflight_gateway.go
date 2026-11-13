// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"context"
	"fmt"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// maxRunMinutes is AgentRun's budget.maxMinutes maximum: the longest deadline a gateway token must outlive.
const maxRunMinutes = 480

// tokenTTLTolerance absorbs the apiserver's rounding and the clock between the request and its answer.
const tokenTTLTolerance = time.Minute

// PodRef is the run-controller's own pod, from the downward API (POD_NAME, POD_UID).
type PodRef struct{ Name, UID string }

// gatewayTokenTTL (GW9): one TokenRequest for the run-controller's own ServiceAccount at the longest
// run's token lifetime. An apiserver started with --service-account-max-token-expiration under it
// answers a shorter expiry, and every gateway run's token would expire before its deadline: gateway
// runs are refused, naming the cap. The lifetime asked is the one measured, so it cannot be short;
// the token is bound to the controller's own pod instead, so the apiserver stops accepting it when
// that pod goes, and nothing keeps it: its audience is one no gateway route or run-pod policy admits.
func (p *Preflight) gatewayTokenTTL(ctx context.Context) (verdict, error) {
	sa := p.Cfg.Model.Gateway.ServiceAccount
	if p.Self.Name == "" || p.Self.UID == "" {
		return verdict{refusal: "POD_NAME and POD_UID are unset: the preflight binds its token to the run-controller's own pod " +
			"(the chart's downward API env in model.mode gateway)"}, nil
	}
	want := int64(maxRunMinutes*60 + gatewayTokenSlack)
	tr := &authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{
		Audiences: []string{p.Cfg.Model.Gateway.AudiencePrefix + ".preflight"}, ExpirationSeconds: &want,
		BoundObjectRef: &authenticationv1.BoundObjectReference{APIVersion: "v1", Kind: "Pod", Name: p.Self.Name, UID: types.UID(p.Self.UID)}}}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	asked := now()
	err := p.Kube.SubResource("token").Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sa.Namespace, Name: sa.Name}}, tr)
	switch {
	case apierrors.IsForbidden(err):
		return verdict{soon: true, refusal: fmt.Sprintf("the run-controller may not request its own ServiceAccount token (%s/%s): it needs create on serviceaccounts/token "+
			"for that name (the chart's run-controller-token Role): %v", sa.Namespace, sa.Name, err)}, nil
	case err != nil:
		return verdict{}, err
	}
	need := time.Duration(want) * time.Second
	if got := tr.Status.ExpirationTimestamp.Sub(asked); got < need-tokenTTLTolerance {
		return verdict{refusal: fmt.Sprintf("the apiserver caps ServiceAccount tokens at %s; gateway runs need %s (budget.maxMinutes %d plus %d s): "+
			"raise --service-account-max-token-expiration", got.Round(time.Second), need, maxRunMinutes, gatewayTokenSlack)}, nil
	}
	return verdict{}, nil
}

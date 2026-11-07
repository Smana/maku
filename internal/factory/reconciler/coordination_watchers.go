// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	roomv1 "github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
)

// coordinationWatchers is the parent Room's watchers (MR13, MR15): every sponsored principal
// with onBehalfOf.slackLinks, so the Slack relay may read the parent's summary. The broker admits
// a sponsored principal by membership only; watcher grants read and nothing else, and what each
// child shows is still filtered per viewer downstream. No child Room ever gets one.
//
// They are set when the parent Room is created, never patched in: the factory's Role cannot patch
// a Room, and widening it for this is not worth it while every parent is created under one config.
// A principal configured after a parent's Room exists is not added to it.
func coordinationWatchers(cfg *config.Config) []roomv1.Member {
	var out []roomv1.Member
	for _, s := range cfg.API.Sponsored {
		if s.OnBehalfOf.SlackLinks {
			out = append(out, roomv1.Member{Principal: s.Principal(), Role: "watcher"})
		}
	}
	return out
}

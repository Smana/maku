// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"
	"time"

	"github.com/Smana/maku/internal/config"
)

// human.slackIntents reaches the classifier as written: the sponsored principal as its system id,
// the pinned alias on the call, and every bound the config carries.
func TestIntentClassifierFromConfig(t *testing.T) {
	sc := config.SlackIntentsConfig{Principal: "slack-app", TeamID: "T01ABC", URL: "http://gw/v1", APIKeyFile: "/k"}
	got, on := config.Config{Human: config.HumanConfig{SlackIntents: &sc}}.SlackIntents()
	if !on {
		t.Fatal("the block is set and reads as off")
	}
	c := intentClassifier(got, nil, nil)
	if c.Principal != "system:slack-app" || c.TeamID != "T01ABC" || c.Alias != config.ClassifyModel ||
		c.MinConfidence != 0.7 || c.ConfirmTTL != 10*time.Minute || c.MaxInputChars != 2000 ||
		c.DailyCalls != 500 || c.PerUserPerHour != 20 || c.RetentionDays != 30 {
		t.Fatalf("%+v", c)
	}
}

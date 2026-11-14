// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"testing"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

// PR1 gap 3, narrowed: the model Secret's optional judge_model runs the completion judge on a model
// of its own; without it the judge runs on the run's model, as before.
func TestJudgeModelFromTheSecret(t *testing.T) {
	judged := run("run-abcdefgh", func(r *v1alpha1.AgentRun) { r.Spec.Loop.JudgeModel = "tier-light" })
	data := map[string][]byte{"api_key": []byte("k"), "model": []byte("big")}
	m, err := ModelFrom(data)
	if err != nil || m.Judge != "" {
		t.Fatalf("no judge_model: %+v %v", m, err)
	}
	if env := harnessEnv(Pod(judged, testConfig(t), m, "abcdefgh")); env["JUDGE_MODEL"] != "big" {
		t.Fatalf("no judge_model: JUDGE_MODEL %q, want the run's model", env["JUDGE_MODEL"])
	}
	data["judge_model"] = []byte(" small\n")
	m, err = ModelFrom(data)
	if err != nil || m.Judge != "small" {
		t.Fatalf("judge_model: %+v %v", m, err)
	}
	env := harnessEnv(Pod(judged, testConfig(t), m, "abcdefgh"))
	if env["JUDGE_MODEL"] != "small" || env["MODEL"] != "big" || env["JUDGE_BASE_URL"] != m.BaseURL {
		t.Fatalf("judge_model: env %v", env)
	}
	if _, ok := harnessEnv(Pod(run("run-abcdefgh"), testConfig(t), m, "abcdefgh"))["JUDGE_MODEL"]; ok {
		t.Fatal("no judge on the run: no JUDGE_MODEL")
	}
}

// CP12, the portable chart: secret mode gives an internal run the light variables on its own model,
// with no key file — the harness falls back to LLM_API_KEY_FILE.
func TestSecretModeLightFallsBackToThePodsModel(t *testing.T) {
	r := goRun(func(r *v1alpha1.AgentRun) { r.Spec.DataClass = "internal" })
	env := harnessEnv(Pod(r, testConfig(t), testModel, "abcdefgh"))
	if env["LIGHT_BASE_URL"] != testModel.BaseURL || env["LIGHT_MODEL"] != "claude-sonnet-5-5" {
		t.Errorf("secret mode light: %+v", env)
	}
	if _, ok := env["LIGHT_API_KEY_FILE"]; ok {
		t.Error("secret mode light: no key file")
	}
	judged := goRun(func(r *v1alpha1.AgentRun) { r.Spec.DataClass = "internal"; r.Spec.Loop.JudgeModel = "tier-light" })
	m := testModel
	m.Judge = "big"
	if env := harnessEnv(Pod(judged, testConfig(t), m, "abcdefgh")); env["LIGHT_MODEL"] != "big" {
		t.Errorf("secret mode light: the judge_model first: %q", env["LIGHT_MODEL"])
	}
}

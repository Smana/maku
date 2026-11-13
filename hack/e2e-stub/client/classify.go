// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/intent"
)

// runClassify makes the broker's own intent_classify model call (internal/intent) against --url with
// the bearer in --token-file, read at use as the broker reads it. The k3d test points it at the model
// gateway's classifier route with a token of the broker's ServiceAccount (plan v4 step 4, Task 12),
// and reads intent=, outcome= and model= (the model the provider answered as).
func runClassify(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("classify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	url := flags.String("url", "", "the classifier's route (human.slackIntents.url)")
	tokenFile := flags.String("token-file", "", "the bearer's file (human.slackIntents.apiKeyFile)")
	reply := flags.String("reply", "", "the thread reply to classify")
	phase := flags.String("phase", "running", "the task's phase")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *url == "" || *tokenFile == "" || *reply == "" {
		return errors.New("classify requires --url, --token-file and --reply, and no positional argument")
	}
	model := intent.Model{
		URL: *url, Alias: config.ClassifyModel, KeyFile: *tokenFile,
		Deadline: config.DefaultClassifyDeadline, HC: httpx.New(10*time.Second, nil),
	}
	raw, usage, err := model.Classify(ctx, *reply, *phase)
	if err != nil {
		return fmt.Errorf("classify: %w", err)
	}
	result, outcome := intent.Parse(raw, *reply, config.DefaultMinConfidence)
	_, err = fmt.Fprintf(stdout, "intent=%s\noutcome=%s\nmodel=%s\n", result.Intent, outcome, usage.Model)
	return err
}

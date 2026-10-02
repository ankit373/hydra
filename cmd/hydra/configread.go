// SPDX-License-Identifier: MIT

package main

import (
	"errors"

	"github.com/ankit373/hydra/internal/config"
)

// reportConfig loads the config for a command that reports what a setting is.
//
// The two failures have opposite answers. A machine that has never run
// `hyctl init` has no config, and "capture was never opted into" is knowable
// without one. A config that exists and does not parse makes the same question
// unanswerable, and five commands answered it anyway from `&config.Config{}`:
// one told you to set `cache_answers = true` in a file where it was already
// set, because it could not read the line above (#1106).
func reportConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if errors.Is(err, config.ErrNotFound) {
		return &config.Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

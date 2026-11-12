// SPDX-License-Identifier: MIT

//go:build ignore

// gen_splits regenerates the committed splits from data `hyctl bench fetch` downloaded.
package main

import (
	"fmt"
	"log"

	"github.com/ankit373/hydra/internal/bench"
	"github.com/ankit373/hydra/internal/bench/suites"
	"github.com/ankit373/hydra/internal/config"
)

func main() {
	lock, err := bench.ReadLock("../../../bench/datasets.lock")
	if err != nil {
		log.Fatal(err)
	}
	excluded, err := suites.GenerateSplits(lock, config.Dir(), "../../../bench")
	if err != nil {
		log.Fatal(err)
	}
	for _, name := range bench.Names(lock) {
		fmt.Printf("%s: %d excluded as conflicting duplicates\n", name, excluded[name])
	}
}

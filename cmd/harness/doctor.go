package main

import (
	"context"
	"fmt"
	"os"

	"github.com/flakerimi/harness/config"
	"github.com/flakerimi/harness/doctor"
)

// runDoctor is the one-command diagnosis: which wires are up, which are down,
// how slow. Exits non-zero when anything fails so it scripts cleanly.
func runDoctor(_ []string) {
	cfg, _ := config.Load()
	checks := doctor.Run(context.Background(), cfg.Search.SearxngURL, os.Getenv("HARNESS_PUBLIC_URL"))
	fmt.Println(doctor.Summary(checks))
	if !doctor.Healthy(checks) {
		os.Exit(1)
	}
}

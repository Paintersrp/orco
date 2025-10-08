package main

import (
	"github.com/Paintersrp/orco/internal/cli"
	"github.com/Paintersrp/orco/internal/metrics"
)

func main() {
	metrics.EmitBuildInfo()
	cli.Execute()
}

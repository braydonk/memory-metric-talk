package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/braydonk/memory-metric-talk/mem_eviction_lab/internal/visualizer"
)

func main() {
	var outputPath string
	flag.StringVar(&outputPath, "output", "", "Output path for generated HTML dashboard (defaults to <dir>/dashboard.html or ./comparison_dashboard.html)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [--output dashboard.html] <results_dir_1> [results_dir_2 ...]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	dirs := flag.Args()
	if len(dirs) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	out, err := visualizer.Generate(dirs, outputPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote dashboard to %s\n", out)
}

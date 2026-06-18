package main

import (
	"fmt"
	"os"

	"l4d2-mod-join/internal/vpkmerge"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: vpkmerge <merge-plan.json>")
		os.Exit(2)
	}
	plan, err := vpkmerge.LoadPlan(os.Args[1])
	if err == nil {
		err = vpkmerge.Run(plan, func(p vpkmerge.Progress) {
			fmt.Printf("%s: %d files, %.2f MiB\n", p.Output, p.FileCount, float64(p.Bytes)/1024/1024)
		})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

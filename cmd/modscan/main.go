package main

import (
	"encoding/json"
	"fmt"
	"os"

	"l4d2-mod-join/internal/modscan"
)

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintln(os.Stderr, "usage: modscan <vpk-directory> [report.json]")
		os.Exit(2)
	}
	result, err := modscan.Scan(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if len(os.Args) == 3 {
		err = os.WriteFile(os.Args[2], data, 0644)
	} else {
		_, err = os.Stdout.Write(append(data, '\n'))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// +build ignore

// This program builds dereddit
package main

import (
	"bytes"
	"log"
	"os"
	"os/exec"
)

const (
	help = `
Valid targets:

	all	(default)
	deps
	clean
	help
`
)

var (
	deps = []string{"github.com/peterbourgon/diskv", "code.google.com/p/go.net/html"}
)

func fetchDeps(update bool) {
	for _, d := range deps {
		var output bytes.Buffer
		var cmd *exec.Cmd
		if update {
			cmd = exec.Command("go", "get", "-u", d)
		} else {
			cmd = exec.Command("go", "get", d)
		}
		cmd.Stdout = &output
		cmd.Stderr = &output
		if err := cmd.Run(); err != nil {
			log.Fatalf("Error fetching dependencies: %v\n%s", err, output.String())
		}
	}
}

func build() {
	var output bytes.Buffer
	cmd := exec.Command("go", "build")
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		log.Fatalf("Error building binary: %v\n%s", err, output.String())
	}
}

func clean() {
	err := os.Remove("dereddit")
	if err != nil {
		log.Fatalf("Error cleaning: %v\n", err)
	}
}

func main() {
	if len(os.Args) == 1 {
		os.Args = append(os.Args, "all")
	}

	switch os.Args[1] {
	case "all":
		fetchDeps(false)
		build()
	case "deps":
		fetchDeps(false)
	case "clean":
		clean()
	case "update":
		fetchDeps(true)
	case "help":
		log.Println(help)
	default:
		log.Fatalf("Unrecognized target: %s\n", os.Args[1])
	}

	os.Exit(0)
}

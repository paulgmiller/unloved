// unloved checks a whole Go module for unnecessarily exported declarations.
package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Usage: unloved [flags] [package patterns]")
		fmt.Fprintln(flag.CommandLine.Output(), "Defaults to packages under internal directories; explicit patterns override this scope.")
		flag.PrintDefaults()
	}
	dir := flag.String("dir", ".", "module directory to check")
	fix := flag.Bool("fix", false, "rename candidates and their references (review the diff and run tests)")
	tags := flag.String("tags", "", "comma-separated build tags")
	flag.Parse()
	findings, err := run(*dir, *tags, *fix, flag.Args()...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, f := range findings {
		fmt.Printf("%s: %s can be unexported\n", f.position, f.name)
	}
	if len(findings) > 0 && !*fix {
		os.Exit(1)
	}
}

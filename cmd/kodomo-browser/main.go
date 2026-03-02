package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"kodomo/browser"
)

func main() {
	addr := flag.String("addr", ":8080", "address to bind the browser server")
	dbPath := flag.String("db", "", "path to kodomo sqlite database (defaults to ~/.kodomo/kodomo.db)")
	flag.Parse()

	path := *dbPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "browser: home dir: %v\n", err)
			os.Exit(1)
		}
		path = filepath.Join(home, ".kodomo", "kodomo.db")
	}

	srv, err := browser.New(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "browser: %v\n", err)
		os.Exit(1)
	}
	defer srv.Close()

	fmt.Printf("kodomo browser listening on http://localhost%s\n", *addr)
	if err := srv.Serve(*addr); err != nil {
		fmt.Fprintf(os.Stderr, "browser: serve: %v\n", err)
		os.Exit(1)
	}
}

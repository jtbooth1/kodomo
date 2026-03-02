package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"kodomo/agent"
	"kodomo/cli"
	"kodomo/workflow"

	"github.com/openai/openai-go/v3/shared"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "kodomo: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("home dir: %w", err)
	}

	dir := filepath.Join(home, ".kodomo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dir, "kodomo.db")
	engine, err := workflow.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer engine.Close()

	a, err := agent.New(engine, agent.Config{
		Model:           "gpt-4.1",
		ReasoningEffort: shared.ReasoningEffortMedium,
	})
	if err != nil {
		return err
	}

	return cli.Run(context.Background(), a)
}

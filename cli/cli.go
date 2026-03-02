package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"kodomo/agent"
)

// Run starts an interactive chat REPL. It blocks until stdin is closed (Ctrl-D).
func Run(ctx context.Context, a *agent.Agent) error {
	convID := randomHex(8)
	var prevResponseID string

	fmt.Println("kodomo — type a message, Ctrl-D to quit")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("> ")
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if line == "" {
			continue
		}

		runID, err := a.Start(ctx, line, &agent.RunOpts{
			ConversationID: convID,
			PrevResponseID: prevResponseID,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}

		respID, err := a.LastResponseID(runID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading response id: %v\n", err)
		}
		prevResponseID = respID
		fmt.Println()
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

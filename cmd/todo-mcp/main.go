// Command todo-mcp exposes the Todo Board to local MCP clients over stdio.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/kcompton15/Todo-Board/internal/mcpserver"
)

func main() {
	baseURL := os.Getenv("TODO_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:7337"
	}
	board, err := mcpserver.NewBoardClient(baseURL, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		fmt.Fprintf(os.Stderr, "todo-mcp: %v\n", err)
		os.Exit(1)
	}
	if err := mcpserver.New(board).Serve(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "todo-mcp: %v\n", err)
		os.Exit(1)
	}
}

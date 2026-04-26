package main

import (
	"log"

	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "wayland-computer-use"
	serverVersion = "0.0.1"
)

func main() {
	p, err := NewPortal()
	if err != nil {
		log.Fatalf("Failed to connect to portal: %v", err)
	}
	defer p.Close()

	// Initialize portal in the background. InitSession requires
	// the user to interact with the portal UI to grant permissions.
	// Tool handlers check p.Ready() before using portal features.
	go p.InitSession()

	s := server.NewMCPServer(serverName, serverVersion, server.WithLogging())

	for _, tool := range GetTools(p) {
		s.AddTool(tool.Info, tool.Handler)
	}

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

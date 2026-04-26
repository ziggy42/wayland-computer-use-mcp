package main

import (
	"fmt"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/server"
	"github.com/yourusername/wayland-computer-use-mcp/internal/portal"
	"github.com/yourusername/wayland-computer-use-mcp/internal/tools"
)

const (
	serverName    = "wayland-computer-use"
	serverVersion = "0.0.1"
)

func main() {
	p, err := portal.NewPortal()
	if err != nil {
		log.Fatalf("Failed to connect to portal: %v", err)
	}
	defer p.Close()

	// Initialize portal in the background to avoid blocking the server startup.
	// InitSession requires the user to interact with the portal UI to grant
	// permissions to control the desktop.
	go func() {
		if err := p.InitSession(); err != nil {
			fmt.Fprintf(os.Stderr, "Portal initialization failed: %v\n", err)
		}
	}()

	s := server.NewMCPServer(serverName, serverVersion, server.WithLogging())

	for _, tool := range tools.GetTools(p) {
		s.AddTool(tool.Info, tool.Handler)
	}

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

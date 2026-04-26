package main

import (
	"log"
	"os/exec"

	"github.com/mark3labs/mcp-go/server"
)

const (
	serverName    = "wayland-computer-use"
	serverVersion = "0.0.1"
)

func main() {
	if _, err := exec.LookPath("gst-launch-1.0"); err != nil {
		log.Fatal("gst-launch-1.0 not found")
	}

	p, err := newPortal()
	if err != nil {
		log.Fatalf("Failed to connect to portal: %v", err)
	}
	defer p.close()

	// Initialize portal in the background. initSession requires the user to
	// interact with the portal UI to grant permissions. Tool handlers check
	// p.getSession() before using portal features.
	go p.initSession()

	s := server.NewMCPServer(serverName, serverVersion, server.WithLogging())

	for _, tool := range getTools(p) {
		s.AddTool(tool.info, tool.handler)
	}

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

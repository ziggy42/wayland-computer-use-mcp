package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Tool represents an MCP tool and its handler.
type Tool struct {
	Info    mcp.Tool
	Handler server.ToolHandlerFunc
}

// GetTools returns all wayland-computer-use tools.
func GetTools(p *Portal) []Tool {
	return []Tool{
		{
			Info: mcp.NewTool("screenshot",
				mcp.WithDescription("Capture the current screen state"),
			),
			Handler: screenshotHandler(p),
		},
		{
			Info: mcp.NewTool("click",
				mcp.WithDescription("Simulate a mouse click at specific coordinates"),
				mcp.WithNumber("x", mcp.Description("X coordinate (0-1)"), mcp.Required()),
				mcp.WithNumber("y", mcp.Description("Y coordinate (0-1)"), mcp.Required()),
				mcp.WithNumber(
					"button",
					mcp.Description("Button to click (1: left, 2: middle, 3: right)"),
				),
			),
			Handler: clickHandler(p),
		},
		{
			Info: mcp.NewTool("scroll",
				mcp.WithDescription("Simulate mouse wheel scrolling"),
				mcp.WithNumber("dx", mcp.Description("Horizontal scroll amount")),
				mcp.WithNumber("dy", mcp.Description("Vertical scroll amount")),
			),
			Handler: scrollHandler(p),
		},
		{
			Info: mcp.NewTool("type_text",
				mcp.WithDescription("Simulate typing a string of text"),
				mcp.WithString("text", mcp.Description("Text to type"), mcp.Required()),
			),
			Handler: typeTextHandler(p),
		},
		{
			Info: mcp.NewTool("get_system_info",
				mcp.WithDescription(
					"Get information about the current system, OS, and desktop environment",
				),
			),
			Handler: systemInfoHandler(),
		},
		{
			Info: mcp.NewTool("press_key",
				mcp.WithDescription(
					"Simulate pressing a specific key (with optional modifiers)",
				),
				mcp.WithString(
					"key",
					mcp.Description("Key name (e.g., Enter, Escape, a, b, c)"),
					mcp.Required(),
				),
				mcp.WithString(
					"modifiers",
					mcp.Description("Comma-separated modifiers (e.g., super, ctrl)"),
				),
			),
			Handler: pressKeyHandler(p),
		},
		{
			Info: mcp.NewTool("wait",
				mcp.WithDescription("Wait for a specified duration"),
				mcp.WithNumber(
					"seconds",
					mcp.Description("Number of seconds to wait"),
					mcp.Required(),
				),
			),
			Handler: waitHandler(),
		},
	}
}

func waitHandler() server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		seconds := request.GetFloat("seconds", 0)
		if seconds <= 0 {
			return mcp.NewToolResultError("Seconds must be greater than 0"), nil
		}

		time.Sleep(time.Duration(seconds * float64(time.Second)))
		return mcp.NewToolResultText(
			fmt.Sprintf("Waited for %.2f seconds", seconds),
		), nil
	}
}

func screenshotHandler(p *Portal) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		if err := p.Ready(); err != nil {
			return portalError(err)
		}

		data, err := p.Screenshot()
		if err != nil {
			return mcp.NewToolResultError(
				fmt.Sprintf("Failed to take screenshot: %v", err),
			), nil
		}

		encoded := base64.StdEncoding.EncodeToString(data)
		return mcp.NewToolResultImage(
			"Screenshot captured", encoded, "image/png",
		), nil
	}
}

func clickHandler(p *Portal) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		if err := p.Ready(); err != nil {
			return portalError(err)
		}

		x := request.GetFloat("x", -1)
		y := request.GetFloat("y", -1)
		if x < 0 || y < 0 {
			return mcp.NewToolResultError("Invalid or missing coordinates"), nil
		}

		button := uint32(request.GetFloat("button", 1))

		if err := p.MovePointer(x, y); err != nil {
			return mcp.NewToolResultError(
				fmt.Sprintf("Failed to move pointer: %v", err),
			), nil
		}

		// Press
		if err := p.Click(button, 1); err != nil {
			return mcp.NewToolResultError(
				fmt.Sprintf("Failed to press button: %v", err),
			), nil
		}
		// Release
		if err := p.Click(button, 0); err != nil {
			return mcp.NewToolResultError(
				fmt.Sprintf("Failed to release button: %v", err),
			), nil
		}

		return mcp.NewToolResultText("Clicked successfully"), nil
	}
}

func scrollHandler(p *Portal) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		if err := p.Ready(); err != nil {
			return portalError(err)
		}

		dx := request.GetFloat("dx", 0)
		dy := request.GetFloat("dy", 0)

		if err := p.Scroll(dx, dy); err != nil {
			return mcp.NewToolResultError(
				fmt.Sprintf("Failed to scroll: %v", err),
			), nil
		}

		return mcp.NewToolResultText("Scrolled successfully"), nil
	}
}

func systemInfoHandler() server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		info := make(map[string]string)
		info["os"] = runtime.GOOS
		info["arch"] = runtime.GOARCH
		info["desktop"] = os.Getenv("XDG_CURRENT_DESKTOP")
		info["session_type"] = os.Getenv("XDG_SESSION_TYPE")

		// Parse /etc/os-release
		if data, err := os.ReadFile("/etc/os-release"); err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "PRETTY_NAME=") {
					info["distro"] = strings.Trim(
						strings.TrimPrefix(line, "PRETTY_NAME="), "\"",
					)
				}
			}
		}

		var sb strings.Builder
		sb.WriteString("System Information:\n")
		for k, v := range info {
			if v != "" {
				sb.WriteString(fmt.Sprintf("- %s: %s\n", k, v))
			}
		}

		return mcp.NewToolResultText(sb.String()), nil
	}
}

func typeTextHandler(p *Portal) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		if err := p.Ready(); err != nil {
			return portalError(err)
		}

		text, err := request.RequireString("text")
		if err != nil {
			return mcp.NewToolResultError("Missing text"), nil
		}

		for _, r := range text {
			keysym := uint32(r)
			switch r {
			case '\n':
				keysym = 0xFF0D // XK_Return
			case '\t':
				keysym = 0xFF09 // XK_Tab
			}
			if err := p.TypeKey(keysym, 1); err != nil {
				return mcp.NewToolResultError(
					fmt.Sprintf("Failed to type key: %v", err),
				), nil
			}
			if err := p.TypeKey(keysym, 0); err != nil {
				return mcp.NewToolResultError(
					fmt.Sprintf("Failed to release key: %v", err),
				), nil
			}
		}

		return mcp.NewToolResultText("Typed text successfully"), nil
	}
}

func pressKeyHandler(p *Portal) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		if err := p.Ready(); err != nil {
			return portalError(err)
		}

		keyName, err := request.RequireString("key")
		if err != nil {
			return mcp.NewToolResultError("Missing key"), nil
		}

		modsStr := request.GetString("modifiers", "")
		var mods []uint32
		if modsStr != "" {
			for _, m := range strings.Split(modsStr, ",") {
				m = strings.TrimSpace(strings.ToLower(m))
				if sym := keysymFromName(m); sym != 0 {
					mods = append(mods, sym)
				}
			}
		}

		keySym := keysymFromName(keyName)
		if keySym == 0 && len(keyName) == 1 {
			keySym = uint32(keyName[0])
		}

		if keySym == 0 {
			return mcp.NewToolResultError(
				fmt.Sprintf("Unknown key: %s", keyName),
			), nil
		}

		// Press modifiers
		for _, m := range mods {
			p.TypeKey(m, 1)
		}

		// Press key
		p.TypeKey(keySym, 1)
		// Release key
		p.TypeKey(keySym, 0)

		// Release modifiers in reverse order
		for i := len(mods) - 1; i >= 0; i-- {
			p.TypeKey(mods[i], 0)
		}

		return mcp.NewToolResultText("Key pressed successfully"), nil
	}
}

func keysymFromName(name string) uint32 {
	name = strings.ToLower(name)
	switch name {
	case "enter", "return":
		return 0xFF0D
	case "escape", "esc":
		return 0xFF1B
	case "backspace":
		return 0xFF08
	case "tab":
		return 0xFF09
	case "space":
		return 0x0020
	case "super", "win", "meta":
		return 0xFFEB // Super_L
	case "ctrl", "control":
		return 0xFFE3 // Control_L
	case "alt":
		return 0xFFE9 // Alt_L
	case "shift":
		return 0xFFE1 // Shift_L
	case "left":
		return 0xFF51
	case "up":
		return 0xFF52
	case "right":
		return 0xFF53
	case "down":
		return 0xFF54
	case "page_up":
		return 0xFF55
	case "page_down":
		return 0xFF56
	case "home":
		return 0xFF50
	case "end":
		return 0xFF57
	}
	return 0
}

func portalError(err error) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(
		fmt.Sprintf("Portal not available: %v", err),
	), nil
}

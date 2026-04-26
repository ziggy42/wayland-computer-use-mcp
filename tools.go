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

// tool represents an MCP tool and its handler.
type tool struct {
	info    mcp.Tool
	handler server.ToolHandlerFunc
}

// getTools returns all wayland-computer-use tools.
func getTools(p *portal) []tool {
	return []tool{
		{
			info: mcp.NewTool("screenshot",
				mcp.WithDescription("Capture the current screen state"),
			),
			handler: screenshotHandler(p),
		},
		{
			info: mcp.NewTool("click",
				mcp.WithDescription("Simulate a mouse click at specific coordinates"),
				mcp.WithNumber("x", mcp.Description("X coordinate (0-1)"), mcp.Required()),
				mcp.WithNumber("y", mcp.Description("Y coordinate (0-1)"), mcp.Required()),
				mcp.WithNumber(
					"button",
					mcp.Description("Button to click (1: left, 2: middle, 3: right)"),
				),
			),
			handler: clickHandler(p),
		},
		{
			info: mcp.NewTool("scroll",
				mcp.WithDescription("Simulate mouse wheel scrolling"),
				mcp.WithNumber("dx", mcp.Description("Horizontal scroll amount")),
				mcp.WithNumber("dy", mcp.Description("Vertical scroll amount")),
			),
			handler: scrollHandler(p),
		},
		{
			info: mcp.NewTool("type_text",
				mcp.WithDescription("Simulate typing a string of text"),
				mcp.WithString("text", mcp.Description("Text to type"), mcp.Required()),
			),
			handler: typeTextHandler(p),
		},
		{
			info: mcp.NewTool("get_system_info",
				mcp.WithDescription(
					"Get information about the current system, OS, and desktop environment",
				),
			),
			handler: systemInfoHandler(),
		},
		{
			info: mcp.NewTool("press_key",
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
			handler: pressKeyHandler(p),
		},
		{
			info: mcp.NewTool("wait",
				mcp.WithDescription("Wait for a specified duration"),
				mcp.WithNumber(
					"seconds",
					mcp.Description("Number of seconds to wait"),
					mcp.Required(),
				),
			),
			handler: waitHandler(),
		},
	}
}

// waitHandler returns a handler for the "wait" tool.
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

// screenshotHandler returns a handler for the "screenshot" tool.
func screenshotHandler(p *portal) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		session, err := p.getSession()
		if err != nil {
			return portalError(err)
		}

		data, err := session.screenshot()
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

// clickHandler returns a handler for the "click" tool.
func clickHandler(p *portal) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		session, err := p.getSession()
		if err != nil {
			return portalError(err)
		}

		x := request.GetFloat("x", -1)
		y := request.GetFloat("y", -1)
		if x < 0 || y < 0 {
			return mcp.NewToolResultError("Invalid or missing coordinates"), nil
		}

		button := uint32(request.GetFloat("button", 1))

		if err := session.movePointer(x, y); err != nil {
			return mcp.NewToolResultError(
				fmt.Sprintf("Failed to move pointer: %v", err),
			), nil
		}

		// Press
		if err := session.click(button, 1); err != nil {
			return mcp.NewToolResultError(
				fmt.Sprintf("Failed to press button: %v", err),
			), nil
		}
		// Release
		if err := session.click(button, 0); err != nil {
			return mcp.NewToolResultError(
				fmt.Sprintf("Failed to release button: %v", err),
			), nil
		}

		return mcp.NewToolResultText("Clicked successfully"), nil
	}
}

// scrollHandler returns a handler for the "scroll" tool.
func scrollHandler(p *portal) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		session, err := p.getSession()
		if err != nil {
			return portalError(err)
		}

		dx := request.GetFloat("dx", 0)
		dy := request.GetFloat("dy", 0)

		if err := session.scroll(dx, dy); err != nil {
			return mcp.NewToolResultError(
				fmt.Sprintf("Failed to scroll: %v", err),
			), nil
		}

		return mcp.NewToolResultText("Scrolled successfully"), nil
	}
}

// systemInfoHandler returns a handler for the "get_system_info" tool.
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

// typeTextHandler returns a handler for the "type_text" tool.
func typeTextHandler(p *portal) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		session, err := p.getSession()
		if err != nil {
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
			if err := session.typeKey(keysym, 1); err != nil {
				return mcp.NewToolResultError(
					fmt.Sprintf("Failed to type key: %v", err),
				), nil
			}
			if err := session.typeKey(keysym, 0); err != nil {
				return mcp.NewToolResultError(
					fmt.Sprintf("Failed to release key: %v", err),
				), nil
			}
		}

		return mcp.NewToolResultText("Typed text successfully"), nil
	}
}

// pressKeyHandler returns a handler for the "press_key" tool.
func pressKeyHandler(p *portal) server.ToolHandlerFunc {
	return func(
		ctx context.Context,
		request mcp.CallToolRequest,
	) (*mcp.CallToolResult, error) {
		session, err := p.getSession()
		if err != nil {
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
		var lastErr error
		pressedMods := 0
		for _, m := range mods {
			if err := session.typeKey(m, 1); err != nil {
				lastErr = err
				break
			}
			pressedMods++
		}

		if lastErr == nil {
			// Press key
			if err := session.typeKey(keySym, 1); err != nil {
				lastErr = err
			} else {
				// Release key
				if err := session.typeKey(keySym, 0); err != nil {
					lastErr = err
				}
			}
		}

		// Release modifiers in reverse order (best effort)
		for i := pressedMods - 1; i >= 0; i-- {
			if err := session.typeKey(mods[i], 0); err != nil && lastErr == nil {
				lastErr = err
			}
		}

		if lastErr != nil {
			return mcp.NewToolResultError(
				fmt.Sprintf("Failed to press key sequence: %v", lastErr),
			), nil
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

// portalError is a helper that wraps initialization/readiness errors into
// a tool result error.
func portalError(err error) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(
		fmt.Sprintf("Portal not available: %v", err),
	), nil
}

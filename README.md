# Wayland Computer Use MCP Server

A high-performance, self-contained MCP (Model Context Protocol) server for Linux Wayland environments. It enables AI agents to interact with your desktop (viewing the screen and simulating input) without requiring root privileges or external tools like `ydotool`.

## Features

- **Non-Root**: Works within user session privileges using XDG Desktop Portals.
- **Wayland Native**: Designed specifically for Wayland (GNOME, KDE, Sway, etc.).
- **Persistent Access**: Supports Portal "restore tokens" so you only have to grant permission once.
- **Self-Contained**: Statically linked Go binary with zero runtime dependencies.

## Tools Provided

| Tool | Description |
| :--- | :--- |
| `get_system_info` | Returns metadata about the current OS and desktop environment. |
| `screenshot` | Captures the current screen and returns it as a PNG. |
| `click` | Simulates mouse clicks at `(x, y)` coordinates (normalized 0.0 to 1.0). |
| `scroll` | Simulates mouse wheel scrolling (horizontal/vertical). |
| `type_text` | Simulates typing a string of text. |
| `press_key` | Simulates specific keys (e.g., `Enter`, `Escape`) with optional modifiers. |

## Installation

### Prerequisites
- A Wayland desktop environment with `xdg-desktop-portal` installed (standard on most modern distros).
- Go 1.25 or later (for building).

### Build
```bash
git clone https://github.com/yourusername/wayland-computer-use-mcp
cd wayland-computer-use-mcp
go build -o wayland-mcp .
```

## Usage

### 1. Initial Setup
When you first run the server (or when an agent calls a tool), your desktop environment will show a security prompt asking to "Allow Remote Desktop" and "Allow Screen Sharing".
- **Ensure you check the "Restore Token" or "Remember this choice" option** (if your DE provides it) to avoid being prompted every time.

### 2. Configure with MCP Clients (e.g., Claude Desktop)
Add the following to your MCP configuration file:

```json
{
  "mcpServers": {
    "wayland": {
      "command": "/path/to/wayland-mcp",
      "env": {
        "DISPLAY": ":0",
        "WAYLAND_DISPLAY": "wayland-0"
      }
    }
  }
}
```

## Security Note
This server allows remote control of your computer. Only use it with trusted AI agents and clients. The `restore_token.txt` file created in the working directory contains the capability to bypass future permission prompts; keep it secure.

## Technical Details
- **D-Bus**: Uses `org.freedesktop.portal.RemoteDesktop` for input and `org.freedesktop.portal.Screenshot` for visuals.
- **Protocol**: Implements MCP over standard I/O.
- **Input Mapping**: Mouse coordinates are normalized (0.0 to 1.0). Key simulation uses standard X11 keysyms.

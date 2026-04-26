# Wayland Computer Use MCP Server

A high-performance, self-contained MCP (Model Context Protocol) server for Linux Wayland environments. It enables AI agents to interact with the desktop (viewing the screen and simulating input) without requiring root privileges or external tools.

## Features

- **Non-Root**: Works within user session privileges using XDG Desktop Portals.
- **Wayland Native**: Designed specifically for Wayland (GNOME, KDE, Sway, etc.).
- **Minimal Dependencies**: Single Go binary; only requires GStreamer at runtime for screen capture.

## Tools Provided

| Tool | Description |
| :--- | :--- |
| `get_system_info` | Returns metadata about the current OS and desktop environment. |
| `wait` | Waits for a specified number of seconds. |
| `screenshot` | Captures the current screen and returns it as a PNG. |
| `click` | Simulates mouse clicks at `(x, y)` coordinates (normalized 0.0 to 1.0). |
| `scroll` | Simulates mouse wheel scrolling (horizontal/vertical). |
| `type_text` | Simulates typing a string of text. |
| `press_key` | Simulates specific keys (e.g., `Enter`, `Escape`) with optional modifiers. |

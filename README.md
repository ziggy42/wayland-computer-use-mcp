# Wayland Computer Use MCP Server

An MCP server for controlling Linux desktops running Wayland.  
It requires no root access and depends exclusively on GStreamer at runtime. 

![Demo](assets/screencast.gif)

> **Note:** This is a weekend project and has only been tested on Ubuntu 26.04 with GNOME. Use at your own peril.

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
| `reset_screen_permission` | Resets the saved ScreenCast permission token to force a new screen selection prompt. |

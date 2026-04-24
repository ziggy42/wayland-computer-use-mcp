# Specification: Wayland Computer Use MCP Server

## Goal
To create a self-contained, high-performance MCP (Model Context Protocol) server for Linux Wayland environments. This server will enable AI agents to interact with the graphical desktop (viewing the screen and simulating input) without requiring external dependencies like `ydotool` or `grim`, and without requiring root privileges.

## Technical Principles
- **Language:** Go (Golang).
- **Self-Contained:** Statically linked binary with zero runtime dependencies beyond a standard Wayland desktop environment.
- **Non-Root:** Must operate within the user's session privileges by leveraging standard portals.
- **Protocol:** Implementation of the MCP specification over `stdio`.

## Core Technologies
### 1. D-Bus & XDG Desktop Portals
The primary interface for interaction will be the `org.freedesktop.portal.Desktop` services.
- **Remote Desktop Portal (`org.freedesktop.portal.RemoteDesktop`):** Used for input simulation (mouse clicks, movement, scrolling, and keyboard events). This portal supports "restore tokens" for persistent, unattended access after initial user approval.
- **ScreenCast Portal (`org.freedesktop.portal.ScreenCast`):** Used to obtain screen content. While this typically involves PipeWire, we will explore the most "Go-friendly" way to handle this.

### 2. D-Bus Library
- **`github.com/godbus/dbus/v5`:** The de facto standard pure-Go D-Bus library. It allows communication with the session bus without CGo or external dependencies.

### 3. MCP Library
- **`github.com/metoro-io/mcp-golang`** or **`github.com/mark3labs/mcp-go`**: These are the leading community-supported Go implementations of the Model Context Protocol.

## Planned Tools (Minimal Viable Product)
| Tool | Description | Portal Method |
| :--- | :--- | :--- |
| `screenshot` | Captures the current screen state and returns it as a Base64 encoded image. | `ScreenCast.OpenPipeWireRemote` / `Screenshot` |
| `click` | Simulates a mouse click at specific `(x, y)` coordinates. | `RemoteDesktop.NotifyPointerButton` |
| `scroll` | Simulates mouse wheel scrolling (horizontal/vertical). | `RemoteDesktop.NotifyPointerAxis` |
| `type_text` | Simulates typing a string of text. | `RemoteDesktop.NotifyKeyboard` (UTF-8) |
| `press_key` | Simulates pressing specific keys (e.g., Enter, Esc, Super+T). | `RemoteDesktop.NotifyKeyboard` (Keysyms) |

## The "PipeWire" Challenge
Screen capture on Wayland is typically handled via PipeWire streams. 
- **The Hard Path:** Interacting with PipeWire's C API (requires CGo and system headers).
- **The Go Path:** We will prioritize finding/creating a pure-Go consumer for PipeWire buffers or utilizing the simpler `org.freedesktop.portal.Screenshot` portal for the MVP, which can save a PNG to a temporary file that the server then reads.

## Future Considerations
- **Accessibility (AT-SPI):** Exploring the `org.a11y.atspi` D-Bus interfaces to provide the AI with a semantic "JSON tree" of the UI (buttons, labels, roles), similar to how web browsers expose the DOM. This would drastically improve the agent's ability to navigate without relying solely on pixel analysis.
- **Window Management:** Integrating with compositor-specific protocols (e.g., via `ext-foreign-toplevel-list-v1`) to allow the agent to focus or identify specific application windows.

## Implementation Roadmap
0. **Phase 0:** Setup git, README.md and everything else you need to work productively.
1. **Phase 1:** Basic MCP boilerplate and D-Bus connectivity.
2. **Phase 2:** Implement `RemoteDesktop` portal session handling with restore tokens.
3. **Phase 3:** Implement `Screenshot` tool (initially via the file-based Screenshot portal).
4. **Phase 4:** Implement Input tools (click, scroll, type).

## Code style
* Write simple, self explaining code
* Do not use too short variable names
* Write code using Google Go styleguide
* Write code as antirez@ would
* Don't write code unnecessarily defensively


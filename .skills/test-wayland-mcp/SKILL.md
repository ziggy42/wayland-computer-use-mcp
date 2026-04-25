---
name: test-wayland-mcp
description: Verifies the functionality of the Wayland Computer Use MCP server by performing automated UI tests on a GNOME desktop. Use this to ensure all tools are properly aligned and working correctly.
---

# Test Wayland Computer Use MCP

This skill is used to verify the functionality of the Wayland Computer Use MCP server, ensuring that all tools are working correctly and coordinates are properly aligned across different monitor configurations.

## Context
The server uses the XDG Desktop Portal for Remote Desktop and ScreenCast. When the server starts, the user will see a system dialog asking for permission to share screens.
- The user may select **one screen** or **multiple screens**.
- The coordinate space (0.0 to 1.0) for both `screenshot` and `click` is relative to the **bounding box** of the area the user shared.
- If the user shares only a secondary monitor, the agent should only see that monitor in the screenshot, and clicks at `(0,0)` should land at the top-left of that specific monitor.

## Instructions

### **Core Principle: Visual Feedback Loop**
**Never assume a tool worked just because it returned a success message.** The Wayland environment is complex, and windows might open on different workspaces, focus might be lost, or coordinates might be misaligned. 
- **Always take a screenshot after every significant action** (launching an app, clicking a button, scrolling) to confirm the visual state matches your expectation.
- If a tool returns "success" but the screenshot shows no change, the action failed in practice. Investigate and remediate immediately.

1. **System Check**:
   - Call `get_system_info` to verify the tool is connected and running on a GNOME/Wayland environment.
   
2. **Initial Capture**:
   - Take a `screenshot`. 
   - Analyze the image: Does it match the shared area? Are there any unexpected black bars or extra monitors that weren't selected?

3. **Interaction Test (Launch & Type)**:
   - **Proactive Focus**: Click once in the center of the shared area `(0.5, 0.5)` to ensure GNOME considers this monitor as "active" for new windows.
   - Press the `Super` key using `press_key(key="Super")` to open the GNOME Activities overview.
   - `wait` for 1 second.
   - Take a `screenshot` to **confirm** the Activities overview is actually visible.
   - Use `type_text` to type "Text Editor" (or "Gedit").
   - `press_key(key="Enter")` to launch it.
   - `wait` for 3 seconds for the window to appear.
   - **Confirm Launch**: Take a `screenshot`. If the window is not visible, do not proceed; use the "Window Rescue" steps below.
   - **Window Rescue (if missing)**: If the screenshot does not show the application after launching it (e.g., Terminal, Text Editor), it very likely opened on a non-shared monitor or workspace. 
     - Use `press_key(key="Right", modifiers="super,shift")` or `press_key(key="Left", modifiers="super,shift")` to move the active window between monitors until it appears in the shared area.
     - Alternatively, use `press_key(key="Page_Down", modifiers="super")` or `press_key(key="Page_Up", modifiers="super")` to switch workspaces if it might have opened on a different workspace.
     - Always take a follow-up screenshot to confirm the window rescue was successful.

4. **Coordinate Accuracy Test**:
   - Take another `screenshot` to find the text editor window.
   - Try to `click` on the editor's text area to ensure it has focus.
   - Use `type_text` to enter a long block of text that forces a scrollbar to appear. Use text with a lot of newlines to ensure scrollbar appears.
   - Take a `screenshot` to **visually confirm** the text was entered and the scrollbar is visible. If the text area is empty, re-focus and re-type.

5. **Navigation & Scrolling Test**:
   - Use `scroll(dy=20)` to verify the scrolling tool works within the editor and moves the view down. 
   - **Take a screenshot to confirm the view actually scrolled** (look at the scrollbar position or text content).
   - Use `scroll(dy=-20)` to scroll back up and take another `screenshot` to confirm the return to the top.
   - Use `press_key` with modifiers if needed (e.g., `ctrl+s`) to test modifier handling.

6. **Click Test (Closing Window)**:
   - Identify the coordinates of the 'X' (close) button on the Text Editor window from the screenshot.
   - Use `click` on those coordinates.
   - **Take a screenshot** to confirm the "Save Changes?" prompt (or the window closing) occurred.
   - If the prompt appears, use `click` on the "Close without Saving" button.
   - Take a final `screenshot` to confirm the window is gone.

7. **Reporting**:
   - Write a summary of the test results.
   - **Crucial**: Report if any clicks landed offset from where you intended, or if the screenshot revealed unintended desktop areas.

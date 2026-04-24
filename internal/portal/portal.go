package portal

import (
	"fmt"
	"image/png"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/google/uuid"
)

const (
	portalDest       = "org.freedesktop.portal.Desktop"
	portalPath       = "/org/freedesktop/portal/desktop"
	remoteDesktopIntf = "org.freedesktop.portal.RemoteDesktop"
	screenCastIntf   = "org.freedesktop.portal.ScreenCast"
	requestIntf      = "org.freedesktop.portal.Request"
	sessionIntf      = "org.freedesktop.portal.Session"
)

type Portal struct {
	conn         *dbus.Conn
	session      dbus.ObjectPath
	restoreToken string
	width        uint32
	height       uint32
}

func NewPortal() (*Portal, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session bus: %w", err)
	}

	p := &Portal{conn: conn}
	
	// Try to load restore token
	if data, err := os.ReadFile("restore_token.txt"); err == nil {
		p.restoreToken = string(data)
	}

	return p, nil
}

func (p *Portal) InitSession() error {
	// 1. Create Session
	sessionHandle, err := p.createSession()
	if err != nil {
		return err
	}
	p.session = sessionHandle

	// 2. Select Devices (Keyboard and Pointer)
	err = p.selectDevices()
	if err != nil {
		return err
	}

	// 3. Start
	err = p.start()
	if err != nil {
		return err
	}

	// 4. Detect Resolution
	if err := p.detectResolution(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to detect resolution: %v. Using default 1920x1080.\n", err)
		p.width = 1920
		p.height = 1080
	}

	return nil
}

func (p *Portal) detectResolution() error {
	data, err := p.Screenshot()
	if err != nil {
		return err
	}

	cfg, err := png.DecodeConfig(strings.NewReader(string(data)))
	if err != nil {
		return err
	}

	p.width = uint32(cfg.Width)
	p.height = uint32(cfg.Height)
	return nil
}

func (p *Portal) createSession() (dbus.ObjectPath, error) {
	obj := p.conn.Object(portalDest, portalPath)
	handleToken := "wayland_mcp_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	
	options := map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(handleToken),
		"handle_token":         dbus.MakeVariant(handleToken + "_req"),
	}

	var requestPath dbus.ObjectPath
	err := obj.Call(remoteDesktopIntf+".CreateSession", 0, options).Store(&requestPath)
	if err != nil {
		return "", fmt.Errorf("CreateSession call failed: %w", err)
	}

	response, err := p.waitForResponse(requestPath)
	if err != nil {
		return "", err
	}

	sessionHandle, ok := response["session_handle"].Value().(string)
	if !ok {
		return "", fmt.Errorf("no session_handle in response")
	}

	return dbus.ObjectPath(sessionHandle), nil
}

func (p *Portal) selectDevices() error {
	obj := p.conn.Object(portalDest, portalPath)
	handleToken := "wayland_mcp_sel_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(handleToken),
		"types":        dbus.MakeVariant(uint32(3)), // 1: Keyboard, 2: Pointer, 3: Both
	}

	if p.restoreToken != "" {
		options["restore_token"] = dbus.MakeVariant(p.restoreToken)
	}

	var requestPath dbus.ObjectPath
	err := obj.Call(remoteDesktopIntf+".SelectDevices", 0, p.session, options).Store(&requestPath)
	if err != nil {
		return fmt.Errorf("SelectDevices call failed: %w", err)
	}

	_, err = p.waitForResponse(requestPath)
	return err
}

func (p *Portal) start() error {
	obj := p.conn.Object(portalDest, portalPath)
	handleToken := "wayland_mcp_start_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(handleToken),
	}

	var requestPath dbus.ObjectPath
	err := obj.Call(remoteDesktopIntf+".Start", 0, p.session, "", options).Store(&requestPath)
	if err != nil {
		return fmt.Errorf("Start call failed: %w", err)
	}

	response, err := p.waitForResponse(requestPath)
	if err != nil {
		return err
	}

	// Save restore token if provided
	if tokenVar, ok := response["restore_token"]; ok {
		if token, ok := tokenVar.Value().(string); ok {
			p.restoreToken = token
			os.WriteFile("restore_token.txt", []byte(token), 0600)
		}
	}

	return nil
}

func (p *Portal) waitForResponse(requestPath dbus.ObjectPath) (map[string]dbus.Variant, error) {
	err := p.conn.AddMatchSignal(
		dbus.WithMatchInterface(requestIntf),
		dbus.WithMatchMember("Response"),
		dbus.WithMatchObjectPath(requestPath),
	)
	if err != nil {
		return nil, err
	}
	defer p.conn.RemoveMatchSignal(
		dbus.WithMatchInterface(requestIntf),
		dbus.WithMatchMember("Response"),
		dbus.WithMatchObjectPath(requestPath),
	)

	ch := make(chan *dbus.Signal, 10)
	p.conn.Signal(ch)

	for sig := range ch {
		if sig.Path == requestPath && sig.Name == requestIntf+".Response" {
			if len(sig.Body) < 2 {
				return nil, fmt.Errorf("invalid response body")
			}
			responseCode, ok := sig.Body[0].(uint32)
			if !ok {
				return nil, fmt.Errorf("invalid response code type")
			}
			if responseCode != 0 {
				return nil, fmt.Errorf("portal request failed with code %d", responseCode)
			}
			results, ok := sig.Body[1].(map[string]dbus.Variant)
			if !ok {
				return nil, fmt.Errorf("invalid results type")
			}
			return results, nil
		}
	}

	return nil, fmt.Errorf("signal channel closed")
}

func (p *Portal) Click(button uint32, state uint32) error {
	obj := p.conn.Object(portalDest, portalPath)
	return obj.Call(remoteDesktopIntf+".NotifyPointerButton", 0, p.session, map[string]dbus.Variant{}, button, state).Err
}

func (p *Portal) MovePointer(x, y float64) error {
	obj := p.conn.Object(portalDest, portalPath)
	absX := x * float64(p.width)
	absY := y * float64(p.height)
	return obj.Call(remoteDesktopIntf+".NotifyPointerMotionAbsolute", 0, p.session, map[string]dbus.Variant{}, absX, absY).Err
}

func (p *Portal) Scroll(dx, dy float64) error {
	obj := p.conn.Object(portalDest, portalPath)
	// dx and dy are in pixels/units. 
	// We might need to call NotifyPointerAxis twice if both are non-zero.
	if dx != 0 {
		err := obj.Call(remoteDesktopIntf+".NotifyPointerAxis", 0, p.session, map[string]dbus.Variant{}, uint32(1), dx).Err
		if err != nil {
			return err
		}
	}
	if dy != 0 {
		err := obj.Call(remoteDesktopIntf+".NotifyPointerAxis", 0, p.session, map[string]dbus.Variant{}, uint32(0), dy).Err
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *Portal) TypeKey(keysym uint32, state uint32) error {
	obj := p.conn.Object(portalDest, portalPath)
	return obj.Call(remoteDesktopIntf+".NotifyKeyboardKeysym", 0, p.session, map[string]dbus.Variant{}, int32(keysym), state).Err
}

func (p *Portal) Screenshot() ([]byte, error) {
	obj := p.conn.Object(portalDest, portalPath)
	handleToken := "wayland_mcp_shot_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(handleToken),
		"interactive":  dbus.MakeVariant(false),
	}

	var requestPath dbus.ObjectPath
	err := obj.Call("org.freedesktop.portal.Screenshot.Screenshot", 0, "", options).Store(&requestPath)
	if err != nil {
		return nil, fmt.Errorf("Screenshot call failed: %w", err)
	}

	response, err := p.waitForResponse(requestPath)
	if err != nil {
		return nil, err
	}

	uri, ok := response["uri"].Value().(string)
	if !ok {
		return nil, fmt.Errorf("no uri in response")
	}

	// URI is typically file:///tmp/...
	path := strings.TrimPrefix(uri, "file://")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read screenshot file: %w", err)
	}

	// Try to remove the temporary file
	os.Remove(path)

	return data, nil
}

func (p *Portal) Close() {
	if p.session != "" {
		p.conn.Object(portalDest, p.session).Call(sessionIntf+".Close", 0)
	}
	p.conn.Close()
}

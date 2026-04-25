package portal

import (
	"fmt"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/google/uuid"
)

const (
	portalDest        = "org.freedesktop.portal.Desktop"
	portalPath        = "/org/freedesktop/portal/desktop"
	remoteDesktopIntf = "org.freedesktop.portal.RemoteDesktop"
	screenCastIntf    = "org.freedesktop.portal.ScreenCast"
	requestIntf       = "org.freedesktop.portal.Request"
	sessionIntf       = "org.freedesktop.portal.Session"
)

// stream represents an individual video stream (e.g., a specific monitor)
// within the portal session.
type stream struct {
	// id is the unique identifier for the stream given by the portal.
	id uint32
	// x and y are the pixel coordinates of the stream's top-left corner
	// relative to the desktop origin (0,0).
	x, y int32
	// w and h are the width and height of the stream in pixels.
	w, h uint32
}

// Portal manages a connection to the XDG Desktop Portal service via DBus.
// It handles the lifecycle of a Remote Desktop and ScreenCast session,
// providing methods for taking screenshots and simulating user input
// (mouse movement, clicks, scrolling, and keyboard events).
type Portal struct {
	conn *dbus.Conn
	// TODO: implement session restoration using restore_token
	session dbus.ObjectPath
	// width is the width of the total logical desktop area (all shared monitors) in pixels.
	width uint32
	// height is the height of the total logical desktop area (all shared monitors) in pixels.
	height uint32
	// streams is a list of screen/window streams shared in the current session.
	streams []stream
}

func NewPortal() (*Portal, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session bus: %w", err)
	}

	return &Portal{conn: conn}, nil
}

func (p *Portal) InitSession() error {
	// 1. Create Session
	sessionHandle, err := p.createSession()
	if err != nil {
		return err
	}
	p.session = sessionHandle

	// 2. Perform Handshake (Select Sources, Devices, and Start)
	// We fire all three before waiting to avoid deadlocks where the OS
	// waits for the 'Start' call before showing the permission UI.
	if err = p.performHandshake(); err != nil {
		return err
	}

	// 5. Detected Resolution summary
	if p.width == 0 || p.height == 0 {
		fmt.Fprintf(os.Stderr, "Warning: failed to detect resolution from streams. Using default 1920x1080.\n")
		p.width = 1920
		p.height = 1080
	} else {
		fmt.Fprintf(os.Stderr, "Detected resolution from streams: %dx%d\n", p.width, p.height)
	}

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

func (p *Portal) requestSources() (dbus.ObjectPath, error) {
	obj := p.conn.Object(portalDest, portalPath)
	handleToken := "wayland_mcp_src_" + strings.ReplaceAll(uuid.New().String(), "-", "")

	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(handleToken),
		"types":        dbus.MakeVariant(uint32(1)), // 1: Screen, 2: Window
		"multiple":     dbus.MakeVariant(true),
		"cursor_mode":  dbus.MakeVariant(uint32(2)), // Embedded cursor
	}

	var requestPath dbus.ObjectPath
	err := obj.Call(screenCastIntf+".SelectSources", 0, p.session, options).Store(&requestPath)
	if err != nil {
		return "", fmt.Errorf("SelectSources call failed: %w", err)
	}

	return requestPath, nil
}

func (p *Portal) requestDevices() (dbus.ObjectPath, error) {
	obj := p.conn.Object(portalDest, portalPath)
	handleToken := "wayland_mcp_sel_" + strings.ReplaceAll(uuid.New().String(), "-", "")

	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(handleToken),
		"types":        dbus.MakeVariant(uint32(3)), // 1: Keyboard, 2: Pointer, 3: Both
	}

	var requestPath dbus.ObjectPath
	err := obj.Call(remoteDesktopIntf+".SelectDevices", 0, p.session, options).Store(&requestPath)
	if err != nil {
		return "", fmt.Errorf("SelectDevices call failed: %w", err)
	}

	return requestPath, nil
}

// performHandshake fires SelectSources, SelectDevices, and Start in sequence
// without waiting for intermediate responses. This is the standard way to
// trigger a single unified system dialog for all permissions.
func (p *Portal) performHandshake() error {
	// 1. Start listening BEFORE we send any requests
	fmt.Fprintf(os.Stderr, "Handshake: Setting up signal listener...\n")
	signalChan, stopListening, err := p.setupResponseListener()
	if err != nil {
		return err
	}
	defer stopListening()

	fmt.Fprintf(os.Stderr, "Handshake: Requesting sources...\n")
	srcPath, err := p.requestSources()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Handshake: Requesting devices...\n")
	devPath, err := p.requestDevices()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Handshake: Requesting start...\n")
	startPath, err := p.requestStart()
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Handshake: Waiting for responses (src=%s, dev=%s, start=%s)...\n", srcPath, devPath, startPath)
	resps, err := p.collectResponses(signalChan, srcPath, devPath, startPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Handshake: All %d responses received!\n", len(resps))

	// The last response (from Start) contains the stream information.
	// We need to find the response that belongs to startPath.
	// Note: collectResponses returns results in the order they arrive.
	// We just look for the "streams" key which is unique to the Start response.
	var startResponse map[string]dbus.Variant
	for _, r := range resps {
		if _, ok := r["streams"]; ok {
			startResponse = r
			break
		}
	}

	if startResponse == nil {
		return fmt.Errorf("no streams found in start response")
	}

	return p.parseStreams(startResponse)
}

func (p *Portal) requestStart() (dbus.ObjectPath, error) {
	obj := p.conn.Object(portalDest, portalPath)
	handleToken := "wayland_mcp_start_" + strings.ReplaceAll(uuid.New().String(), "-", "")

	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(handleToken),
	}

	var requestPath dbus.ObjectPath
	err := obj.Call(remoteDesktopIntf+".Start", 0, p.session, "", options).Store(&requestPath)
	if err != nil {
		return "", fmt.Errorf("Start call failed: %w", err)
	}

	return requestPath, nil
}

func (p *Portal) parseStreams(response map[string]dbus.Variant) error {
	streamsVar, ok := response["streams"]
	if !ok {
		return nil
	}

	v := streamsVar.Value()
	s, ok := v.([][]interface{})
	if !ok {
		return nil
	}

	for _, streamInterface := range s {
		if len(streamInterface) < 2 {
			continue
		}
		streamID, ok1 := streamInterface[0].(uint32)
		lastIdx := len(streamInterface) - 1
		options, ok2 := streamInterface[lastIdx].(map[string]dbus.Variant)

		if ok1 && ok2 {
			s := stream{id: streamID}
			if pos, ok := options["position"]; ok {
				pv := pos.Value()
				if p, ok := pv.([]int32); ok && len(p) >= 2 {
					s.x, s.y = p[0], p[1]
				} else if p, ok := pv.([]int64); ok && len(p) >= 2 {
					s.x, s.y = int32(p[0]), int32(p[1])
				} else if p, ok := pv.([]interface{}); ok && len(p) >= 2 {
					if x, ok := p[0].(int32); ok {
						s.x = x
					}
					if y, ok := p[1].(int32); ok {
						s.y = y
					}
				}
			}
			if size, ok := options["size"]; ok {
				sv := size.Value()
				if sizeArr, ok := sv.([]int32); ok && len(sizeArr) >= 2 {
					s.w, s.h = uint32(sizeArr[0]), uint32(sizeArr[1])
				} else if sizeArr, ok := sv.([]int64); ok && len(sizeArr) >= 2 {
					s.w, s.h = uint32(sizeArr[0]), uint32(sizeArr[1])
				} else if sizeArr, ok := sv.([]interface{}); ok && len(sizeArr) >= 2 {
					if w, ok := sizeArr[0].(int32); ok {
						s.w = uint32(w)
					}
					if h, ok := sizeArr[1].(int32); ok {
						s.h = uint32(h)
					}
				}
			}
			p.streams = append(p.streams, s)

			// Update total desktop dimensions
			if s.x+int32(s.w) > int32(p.width) {
				p.width = uint32(s.x + int32(s.w))
			}
			if s.y+int32(s.h) > int32(p.height) {
				p.height = uint32(s.y + int32(s.h))
			}
		}
	}
	fmt.Fprintf(os.Stderr, "Remote Desktop session started with %d streams\n", len(p.streams))

	return nil
}

func (p *Portal) setupResponseListener() (chan *dbus.Signal, func(), error) {
	err := p.conn.AddMatchSignal(
		dbus.WithMatchInterface(requestIntf),
		dbus.WithMatchMember("Response"),
	)
	if err != nil {
		return nil, nil, err
	}

	ch := make(chan *dbus.Signal, 100)
	p.conn.Signal(ch)

	stop := func() {
		p.conn.RemoveSignal(ch)
		p.conn.RemoveMatchSignal(
			dbus.WithMatchInterface(requestIntf),
			dbus.WithMatchMember("Response"),
		)
	}

	return ch, stop, nil
}

func (p *Portal) collectResponses(ch chan *dbus.Signal, paths ...dbus.ObjectPath) ([]map[string]dbus.Variant, error) {
	results := make([]map[string]dbus.Variant, 0, len(paths))
	pendingPaths := make(map[dbus.ObjectPath]bool)
	for _, path := range paths {
		pendingPaths[path] = true
	}

	for sig := range ch {
		fmt.Fprintf(os.Stderr, "Signal received: Path=%s, Name=%s\n", sig.Path, sig.Name)
		if sig.Name != requestIntf+".Response" {
			continue
		}

		if pendingPaths[sig.Path] {
			fmt.Fprintf(os.Stderr, "Matched pending path: %s\n", sig.Path)
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
			resultsMap, ok := sig.Body[1].(map[string]dbus.Variant)
			if !ok {
				return nil, fmt.Errorf("invalid results type")
			}

			results = append(results, resultsMap)
			delete(pendingPaths, sig.Path)

			if len(pendingPaths) == 0 {
				return results, nil
			}
		}
	}

	return nil, fmt.Errorf("signal channel closed")
}

func (p *Portal) waitForResponse(requestPath dbus.ObjectPath) (map[string]dbus.Variant, error) {
	ch, stop, err := p.setupResponseListener()
	if err != nil {
		return nil, err
	}
	defer stop()

	resps, err := p.collectResponses(ch, requestPath)
	if err != nil {
		return nil, err
	}
	return resps[0], nil
}

func (p *Portal) Click(button uint32, state uint32) error {
	obj := p.conn.Object(portalDest, portalPath)
	return obj.Call(remoteDesktopIntf+".NotifyPointerButton", 0, p.session, map[string]dbus.Variant{}, int32(button), state).Err
}

func (p *Portal) MovePointer(x, y float64) error {
	obj := p.conn.Object(portalDest, portalPath)
	absX := x * float64(p.width)
	absY := y * float64(p.height)

	// Try using stream 0 (session-relative) first
	err := obj.Call(remoteDesktopIntf+".NotifyPointerMotionAbsolute", 0, p.session, map[string]dbus.Variant{}, uint32(0), absX, absY).Err
	if err == nil {
		return nil
	}

	// Fallback to targeted stream
	var targetStreamID uint32 = 0
	var relX, relY float64 = absX, absY

	for _, s := range p.streams {
		if int32(absX) >= s.x && int32(absX) < s.x+int32(s.w) &&
			int32(absY) >= s.y && int32(absY) < s.y+int32(s.h) {
			targetStreamID = s.id
			relX = absX - float64(s.x)
			relY = absY - float64(s.y)
			break
		}
	}

	// If no stream found, but we have streams, pick the first one and clamp
	if targetStreamID == 0 && len(p.streams) > 0 {
		targetStreamID = p.streams[0].id
		relX = absX - float64(p.streams[0].x)
		relY = absY - float64(p.streams[0].y)

		// Clamp relX and relY to stream bounds
		if relX < 0 {
			relX = 0
		} else if relX >= float64(p.streams[0].w) {
			relX = float64(p.streams[0].w) - 1
		}
		if relY < 0 {
			relY = 0
		} else if relY >= float64(p.streams[0].h) {
			relY = float64(p.streams[0].h) - 1
		}
	}

	return obj.Call(remoteDesktopIntf+".NotifyPointerMotionAbsolute", 0, p.session, map[string]dbus.Variant{}, targetStreamID, relX, relY).Err
}

func (p *Portal) Scroll(dx, dy float64) error {
	obj := p.conn.Object(portalDest, portalPath)
	if dx != 0 {
		err := obj.Call(remoteDesktopIntf+".NotifyPointerAxis", 0, p.session, map[string]dbus.Variant{}, dx, 0.0).Err
		if err != nil {
			return err
		}
	}
	if dy != 0 {
		err := obj.Call(remoteDesktopIntf+".NotifyPointerAxis", 0, p.session, map[string]dbus.Variant{}, 0.0, dy).Err
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

	path := strings.TrimPrefix(uri, "file://")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read screenshot file: %w", err)
	}

	os.Remove(path)
	return data, nil
}

func (p *Portal) Close() {
	if p.session != "" {
		p.conn.Object(portalDest, p.session).Call(sessionIntf+".Close", 0)
	}
	p.conn.Close()
}

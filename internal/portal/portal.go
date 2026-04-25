package portal

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/google/uuid"
)

const (
	// D-Bus addresses and interfaces
	portalDestination = "org.freedesktop.portal.Desktop"
	portalPath        = "/org/freedesktop/portal/desktop"

	remoteDesktopInterface = "org.freedesktop.portal.RemoteDesktop"
	screenCastInterface    = "org.freedesktop.portal.ScreenCast"
	screenshotInterface    = "org.freedesktop.portal.Screenshot"
	sessionInterface       = "org.freedesktop.portal.Session"
	requestInterface       = "org.freedesktop.portal.Request"

	// Method names
	methodCreateSession               = remoteDesktopInterface + ".CreateSession"
	methodSelectDevices               = remoteDesktopInterface + ".SelectDevices"
	methodStart                       = remoteDesktopInterface + ".Start"
	methodNotifyPointerMotionAbsolute = remoteDesktopInterface + ".NotifyPointerMotionAbsolute"
	methodNotifyPointerButton         = remoteDesktopInterface + ".NotifyPointerButton"
	methodNotifyPointerAxis           = remoteDesktopInterface + ".NotifyPointerAxis"
	methodNotifyKeyboardKeysym        = remoteDesktopInterface + ".NotifyKeyboardKeysym"

	methodSelectSources = screenCastInterface + ".SelectSources"
	methodScreenshot    = screenshotInterface + ".Screenshot"
	methodSessionClose  = sessionInterface + ".Close"

	// Signals
	responseMember = "Response"
	signalResponse = requestInterface + "." + responseMember
)

const (
	// Enum values
	sourceTypeScreen   uint32 = 1
	cursorModeEmbedded uint32 = 2
	deviceTypeKeyboard uint32 = 1
	deviceTypePointer  uint32 = 2
)

var (
	ErrDimensionsUnknown   = errors.New("screen dimensions unknown")
	ErrInvalidResponseBody = errors.New("invalid response body")
	ErrInvalidResultsType  = errors.New("invalid results type")
	ErrSignalChannelClosed = errors.New("signal channel closed")
	ErrNoURIInResponse     = errors.New("no uri in response")
	ErrNoStreamsInResponse = errors.New("no streams found in start response")
)

// Portal manages a connection to the XDG Desktop Portal service via DBus.
// It handles the lifecycle of a Remote Desktop and ScreenCast session,
// providing methods for taking screenshots and simulating user input.
type Portal struct {
	connection *dbus.Conn
	session    dbus.ObjectPath
	width      int32
	height     int32
	streams    []stream
}

type stream struct {
	id   uint32
	x, y int32
	w, h uint32
}

// NewPortal creates a new Portal instance.
func NewPortal() (*Portal, error) {
	connection, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session bus: %w", err)
	}

	return &Portal{connection: connection}, nil
}

// InitSession initializes the portal session by performing the handshake.
func (p *Portal) InitSession() error {
	sessionHandle, err := p.createSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	p.session = sessionHandle

	if err := p.startSession(); err != nil {
		return fmt.Errorf("session startup failed: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Portal initialized: %dx%d (%d streams)\n", p.width, p.height, len(p.streams))
	return nil
}

// Close closes the portal session and the DBus connection.
func (p *Portal) Close() {
	if p.session != "" {
		p.connection.Object(portalDestination, p.session).
			Call(methodSessionClose, 0)
	}
	p.connection.Close()
}

// Screenshot takes a screenshot and returns the raw bytes.
func (p *Portal) Screenshot() ([]byte, error) {
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(newToken("wayland_mcp_shot_")),
		"interactive":  dbus.MakeVariant(false),
	}

	responses, err := p.awaitResponses(func() ([]dbus.ObjectPath, error) {
		var requestPath dbus.ObjectPath
		if err := p.call(methodScreenshot, "", options).
			Store(&requestPath); err != nil {
			return nil, err
		}
		return []dbus.ObjectPath{requestPath}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("screenshot failed: %w", err)
	}

	var response map[string]dbus.Variant
	for _, r := range responses {
		response = r
		break
	}

	uri, ok := response["uri"].Value().(string)
	if !ok {
		return nil, ErrNoURIInResponse
	}

	path := strings.TrimPrefix(uri, "file://")
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read screenshot file: %w", err)
	}

	return data, nil
}

// MovePointer simulates mouse movement to absolute coordinates (0.0 to 1.0).
func (p *Portal) MovePointer(x, y float64) error {
	if p.width == 0 || p.height == 0 {
		return ErrDimensionsUnknown
	}

	absoluteX := x * float64(p.width)
	absoluteY := y * float64(p.height)

	// Try session-relative motion (stream 0)
	err := p.call(
		methodNotifyPointerMotionAbsolute, p.session,
		map[string]dbus.Variant{}, uint32(0), absoluteX, absoluteY,
	).Err
	if err == nil {
		return nil
	}

	// Fallback: target a specific stream
	s, relativeX, relativeY := p.findStream(absoluteX, absoluteY)
	return p.call(
		methodNotifyPointerMotionAbsolute, p.session,
		map[string]dbus.Variant{}, s.id, relativeX, relativeY,
	).Err
}

// Click simulates a mouse button press or release.
func (p *Portal) Click(button, state uint32) error {
	return p.call(
		methodNotifyPointerButton, p.session,
		map[string]dbus.Variant{}, int32(button), state,
	).Err
}

// Scroll simulates a mouse wheel scroll.
func (p *Portal) Scroll(deltaX, deltaY float64) error {
	return p.call(
		methodNotifyPointerAxis, p.session,
		map[string]dbus.Variant{}, deltaX, deltaY,
	).Err
}

// TypeKey simulates a keyboard key press or release using a keysym.
func (p *Portal) TypeKey(keysym, state uint32) error {
	return p.call(
		methodNotifyKeyboardKeysym, p.session,
		map[string]dbus.Variant{}, int32(keysym), state,
	).Err
}

func (p *Portal) startSession() error {
	responses, err := p.awaitResponses(func() ([]dbus.ObjectPath, error) {
		sourcesPath, err := p.requestSources()
		if err != nil {
			return nil, err
		}
		devicesPath, err := p.requestDevices()
		if err != nil {
			return nil, err
		}
		startPath, err := p.requestStart()
		if err != nil {
			return nil, err
		}
		return []dbus.ObjectPath{sourcesPath, devicesPath, startPath}, nil
	})
	if err != nil {
		return err
	}

	var rawStreams [][]any
	for _, response := range responses {
		if v, ok := response["streams"]; ok {
			if s, ok := v.Value().([][]any); ok {
				rawStreams = s
				break
			}
		}
	}

	streams, err := parseStreams(rawStreams)
	if err != nil {
		return err
	}
	if len(streams) == 0 {
		return ErrNoStreamsInResponse
	}
	p.streams = streams

	p.width, p.height = totalBounds(p.streams)
	return nil
}

func totalBounds(streams []stream) (width, height int32) {
	for _, s := range streams {
		if right := s.x + int32(s.w); right > width {
			width = right
		}
		if bottom := s.y + int32(s.h); bottom > height {
			height = bottom
		}
	}
	return
}

func (p *Portal) createSession() (dbus.ObjectPath, error) {
	token := newToken("wayland_mcp_")
	options := map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(token),
		"handle_token":         dbus.MakeVariant(token + "_req"),
	}

	var requestPath dbus.ObjectPath
	if err := p.call(methodCreateSession, options).
		Store(&requestPath); err != nil {
		return "", fmt.Errorf("CreateSession call failed: %w", err)
	}

	response, err := p.waitForResponse(requestPath)
	if err != nil {
		return "", err
	}

	sessionHandle, ok := response["session_handle"].Value().(string)
	if !ok {
		return "", errors.New("no session_handle in response")
	}

	return dbus.ObjectPath(sessionHandle), nil
}

func (p *Portal) requestSources() (dbus.ObjectPath, error) {
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(newToken("wayland_mcp_src_")),
		"types":        dbus.MakeVariant(sourceTypeScreen),
		"multiple":     dbus.MakeVariant(true),
		"cursor_mode":  dbus.MakeVariant(cursorModeEmbedded),
	}

	var requestPath dbus.ObjectPath
	if err := p.call(methodSelectSources, p.session, options).
		Store(&requestPath); err != nil {
		return "", fmt.Errorf("SelectSources call failed: %w", err)
	}

	return requestPath, nil
}

func (p *Portal) requestDevices() (dbus.ObjectPath, error) {
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(newToken("wayland_mcp_sel_")),
		"types":        dbus.MakeVariant(deviceTypeKeyboard | deviceTypePointer),
	}

	var requestPath dbus.ObjectPath
	if err := p.call(methodSelectDevices, p.session, options).
		Store(&requestPath); err != nil {
		return "", fmt.Errorf("SelectDevices call failed: %w", err)
	}

	return requestPath, nil
}

func (p *Portal) requestStart() (dbus.ObjectPath, error) {
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(newToken("wayland_mcp_start_")),
	}

	var requestPath dbus.ObjectPath
	if err := p.call(methodStart, p.session, "", options).
		Store(&requestPath); err != nil {
		return "", fmt.Errorf("Start call failed: %w", err)
	}

	return requestPath, nil
}

func (p *Portal) call(method string, args ...any) *dbus.Call {
	return p.connection.Object(portalDestination, portalPath).
		Call(method, 0, args...)
}

func parseStreams(rawStreams [][]any) ([]stream, error) {
	var streams []stream
	for _, streamData := range rawStreams {
		if len(streamData) < 2 {
			continue
		}

		id, ok1 := streamData[0].(uint32)
		options, ok2 := streamData[len(streamData)-1].(map[string]dbus.Variant)
		if !ok1 || !ok2 {
			continue
		}

		s := stream{id: id}
		if v, ok := options["position"]; ok {
			if coords, ok := variantToInt32Slice(v); ok && len(coords) >= 2 {
				s.x, s.y = coords[0], coords[1]
			}
		}
		if v, ok := options["size"]; ok {
			if dims, ok := variantToInt32Slice(v); ok && len(dims) >= 2 {
				s.w, s.h = uint32(dims[0]), uint32(dims[1])
			} else {
				return nil, fmt.Errorf("stream %d has invalid 'size' property", id)
			}
		} else {
			return nil, fmt.Errorf("stream %d is missing required 'size' property", id)
		}

		streams = append(streams, s)
	}
	return streams, nil
}

func variantToInt32Slice(v dbus.Variant) ([]int32, bool) {
	var out []int32
	if v.Store(&out) == nil {
		return out, true
	}
	// Try as struct (ii) or similar
	var s struct {
		X, Y int32
	}
	if v.Store(&s) == nil {
		return []int32{s.X, s.Y}, true
	}
	return nil, false
}

func (p *Portal) waitForResponse(
	requestPath dbus.ObjectPath,
) (map[string]dbus.Variant, error) {
	responses, err := p.awaitResponses(func() ([]dbus.ObjectPath, error) {
		return []dbus.ObjectPath{requestPath}, nil
	})
	if err != nil {
		return nil, err
	}
	return responses[requestPath], nil
}

func (p *Portal) awaitResponses(
	issue func() ([]dbus.ObjectPath, error),
) (map[dbus.ObjectPath]map[string]dbus.Variant, error) {
	signalChan, stop, err := p.setupResponseListener()
	if err != nil {
		return nil, err
	}
	defer stop()

	paths, err := issue()
	if err != nil {
		return nil, err
	}

	return collectResponses(signalChan, paths...)
}

func collectResponses(
	signalChan chan *dbus.Signal,
	paths ...dbus.ObjectPath,
) (map[dbus.ObjectPath]map[string]dbus.Variant, error) {
	results := make(map[dbus.ObjectPath]map[string]dbus.Variant)
	pending := make(map[dbus.ObjectPath]bool)
	for _, path := range paths {
		pending[path] = true
	}

	for signal := range signalChan {
		if signal.Name != signalResponse {
			continue
		}

		if !pending[signal.Path] {
			continue
		}

		if len(signal.Body) < 2 {
			return nil, ErrInvalidResponseBody
		}

		code, ok := signal.Body[0].(uint32)
		if !ok || code != 0 {
			return nil, fmt.Errorf(
				"portal request failed (path=%s, code=%d)", signal.Path, code,
			)
		}

		result, ok := signal.Body[1].(map[string]dbus.Variant)
		if !ok {
			return nil, ErrInvalidResultsType
		}

		results[signal.Path] = result
		delete(pending, signal.Path)

		if len(pending) == 0 {
			return results, nil
		}
	}

	return nil, ErrSignalChannelClosed
}

func (p *Portal) setupResponseListener() (chan *dbus.Signal, func(), error) {
	err := p.connection.AddMatchSignal(
		dbus.WithMatchInterface(requestInterface),
		dbus.WithMatchMember(responseMember),
	)
	if err != nil {
		return nil, nil, err
	}

	signalChan := make(chan *dbus.Signal, 100)
	p.connection.Signal(signalChan)

	stop := func() {
		p.connection.RemoveSignal(signalChan)
		p.connection.RemoveMatchSignal(
			dbus.WithMatchInterface(requestInterface),
			dbus.WithMatchMember(responseMember),
		)
	}

	return signalChan, stop, nil
}

func (p *Portal) findStream(
	absoluteX,
	absoluteY float64,
) (s stream, relativeX, relativeY float64) {
	if len(p.streams) == 0 {
		return stream{}, absoluteX, absoluteY
	}

	for _, candidate := range p.streams {
		if int32(absoluteX) >= candidate.x &&
			int32(absoluteX) < candidate.x+int32(candidate.w) &&
			int32(absoluteY) >= candidate.y &&
			int32(absoluteY) < candidate.y+int32(candidate.h) {
			return candidate,
				absoluteX - float64(candidate.x),
				absoluteY - float64(candidate.y)
		}
	}

	s = p.streams[0]
	relativeX = clamp(absoluteX-float64(s.x), 0, float64(s.w)-1)
	relativeY = clamp(absoluteY-float64(s.y), 0, float64(s.h)-1)
	return
}

func newToken(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.New().String(), "-", "")
}

func clamp(v, minV, maxV float64) float64 {
	return min(maxV, max(minV, v))
}

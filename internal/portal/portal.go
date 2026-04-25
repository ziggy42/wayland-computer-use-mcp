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
	portalDestination = "org.freedesktop.portal.Desktop"
	portalPath        = "/org/freedesktop/portal/desktop"

	remoteDesktopInterface            = "org.freedesktop.portal.RemoteDesktop"
	methodCreateSession               = remoteDesktopInterface + ".CreateSession"
	methodSelectDevices               = remoteDesktopInterface + ".SelectDevices"
	methodStart                       = remoteDesktopInterface + ".Start"
	methodNotifyPointerMotionAbsolute = remoteDesktopInterface + ".NotifyPointerMotionAbsolute"
	methodNotifyPointerButton         = remoteDesktopInterface + ".NotifyPointerButton"
	methodNotifyPointerAxis           = remoteDesktopInterface + ".NotifyPointerAxis"
	methodNotifyKeyboardKeysym        = remoteDesktopInterface + ".NotifyKeyboardKeysym"

	screenCastInterface = "org.freedesktop.portal.ScreenCast"
	methodSelectSources = screenCastInterface + ".SelectSources"

	requestInterface = "org.freedesktop.portal.Request"
	responseMember   = "Response"
	signalResponse   = requestInterface + "." + responseMember

	sessionInterface   = "org.freedesktop.portal.Session"
	methodSessionClose = sessionInterface + ".Close"

	screenshotInterface = "org.freedesktop.portal.Screenshot"
	methodScreenshot    = screenshotInterface + ".Screenshot"
)

const (
	sourceTypeScreen   uint32 = 1
	cursorModeEmbedded uint32 = 2
	deviceTypeKeyboard uint32 = 1
	deviceTypePointer  uint32 = 2
)

var (
	ErrDimensionsUnknown = errors.New(
		"screen dimensions unknown; ensure portal session is active and screenshot has been taken",
	)
	ErrInvalidResponseBody = errors.New("invalid response body")
	ErrInvalidResultsType  = errors.New("invalid results type")
	ErrSignalChannelClosed = errors.New("signal channel closed")
	ErrNoUriInResponse     = errors.New("no uri in response")
	ErrNoStreamsInResponse = errors.New("no streams found in start response")
	ErrInvalidResponse     = errors.New("invalid results type")
)

// Portal manages a connection to the XDG Desktop Portal service via DBus.
// It handles the lifecycle of a Remote Desktop and ScreenCast session,
// providing methods for taking screenshots and simulating user input.
type Portal struct {
	connection *dbus.Conn
	session    dbus.ObjectPath
	width      uint32
	height     uint32
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

	if err := p.performHandshake(); err != nil {
		return fmt.Errorf("handshake failed: %w", err)
	}

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

	var requestPath dbus.ObjectPath
	if err := p.call(methodScreenshot, "", options).
		Store(&requestPath); err != nil {
		return nil, fmt.Errorf("screenshot call failed: %w", err)
	}

	response, err := p.waitForResponse(requestPath)
	if err != nil {
		return nil, err
	}

	uri, ok := response["uri"].Value().(string)
	if !ok {
		return nil, ErrNoUriInResponse
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
	if deltaX != 0 {
		if err := p.call(
			methodNotifyPointerAxis, p.session,
			map[string]dbus.Variant{}, deltaX, 0.0,
		).Err; err != nil {
			return err
		}
	}
	if deltaY != 0 {
		if err := p.call(
			methodNotifyPointerAxis, p.session,
			map[string]dbus.Variant{}, 0.0, deltaY,
		).Err; err != nil {
			return err
		}
	}
	return nil
}

// TypeKey simulates a keyboard key press or release using a keysym.
func (p *Portal) TypeKey(keysym, state uint32) error {
	return p.call(
		methodNotifyKeyboardKeysym, p.session,
		map[string]dbus.Variant{}, int32(keysym), state,
	).Err
}

func (p *Portal) performHandshake() error {
	signalChan, stopListening, err := p.setupResponseListener()
	if err != nil {
		return err
	}
	defer stopListening()

	sourcesPath, err := p.requestSources()
	if err != nil {
		return err
	}
	devicesPath, err := p.requestDevices()
	if err != nil {
		return err
	}
	startPath, err := p.requestStart()
	if err != nil {
		return err
	}

	responses, err := collectResponses(signalChan, sourcesPath, devicesPath, startPath)
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

	p.streams = parseStreams(rawStreams)
	if len(p.streams) == 0 {
		return ErrNoStreamsInResponse
	}

	// Calculate total dimensions from streams
	for _, s := range p.streams {
		if s.x+int32(s.w) > int32(p.width) {
			p.width = uint32(s.x + int32(s.w))
		}
		if s.y+int32(s.h) > int32(p.height) {
			p.height = uint32(s.y + int32(s.h))
		}
	}

	return nil
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

func parseStreams(rawStreams [][]any) []stream {
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
		if position, ok := options["position"]; ok {
			var coords []int32
			if err := position.Store(&coords); err == nil && len(coords) >= 2 {
				s.x, s.y = coords[0], coords[1]
			}
		}
		if size, ok := options["size"]; ok {
			var dims []int32
			if err := size.Store(&dims); err == nil && len(dims) >= 2 {
				s.w, s.h = uint32(dims[0]), uint32(dims[1])
			}
		} else {
			// TODO: The 'size' property is optional. If missing, we should detect
			// dimensions from the actual screenshot or another reliable source.
		}

		streams = append(streams, s)
	}
	return streams
}

func (p *Portal) waitForResponse(
	requestPath dbus.ObjectPath,
) (map[string]dbus.Variant, error) {
	signalChan, stopListening, err := p.setupResponseListener()
	if err != nil {
		return nil, err
	}
	defer stopListening()

	responses, err := collectResponses(signalChan, requestPath)
	if err != nil {
		return nil, err
	}
	return responses[0], nil
}

func collectResponses(
	signalChan chan *dbus.Signal,
	paths ...dbus.ObjectPath,
) ([]map[string]dbus.Variant, error) {
	results := make([]map[string]dbus.Variant, 0, len(paths))
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

		results = append(results, result)
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

	for _, s := range p.streams {
		if int32(absoluteX) >= s.x && int32(absoluteX) < s.x+int32(s.w) &&
			int32(absoluteY) >= s.y && int32(absoluteY) < s.y+int32(s.h) {
			return s, absoluteX - float64(s.x), absoluteY - float64(s.y)
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

func clamp[T ~float64 | ~int32](v, min_v, max_v T) T {
	return min(max_v, max(min_v, v))
}

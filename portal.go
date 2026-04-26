package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/google/uuid"
)

const (
	// D-Bus destination and path
	portalDestination = "org.freedesktop.portal.Desktop"
	portalPath        = "/org/freedesktop/portal/desktop"

	// Interface names
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
	methodSelectSources               = screenCastInterface + ".SelectSources"
	methodScreenshot                  = screenshotInterface + ".Screenshot"
	methodSessionClose                = sessionInterface + ".Close"

	// Signal names
	responseMember = "Response"
	signalResponse = requestInterface + "." + responseMember

	// noParentWindow is passed to portal methods that take an optional parent
	// window handle; an empty string means "no parent".
	noParentWindow = ""
)

const (
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
	ErrSubImageCrop        = errors.New("image does not support sub-image cropping")
	ErrCropOutsideBounds   = errors.New("crop rectangle is outside image bounds")
	ErrNoSessionHandle     = errors.New("no session_handle in response")
)

// Portal manages a connection to the XDG Desktop Portal service via D-Bus.
// It handles the lifecycle of a Remote Desktop and ScreenCast session,
// providing methods for taking screenshots and simulating user input.
type Portal struct {
	connection *dbus.Conn
	session    dbus.ObjectPath
	minX       int32 // left edge of the shared area bounding box
	minY       int32 // top edge of the shared area bounding box
	width      int32 // width of the shared area bounding box
	height     int32 // height of the shared area bounding box
	streams    []stream
}

// stream describes one screen-cast source returned by the portal.
// Position (x, y) may be negative in multi-monitor setups; size (w, h) is
// always positive.
type stream struct {
	id   uint32
	x, y int32
	w, h uint32
}

// NewPortal creates a new Portal instance connected to the session D-Bus.
func NewPortal() (*Portal, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session bus: %w", err)
	}
	return &Portal{connection: conn}, nil
}

// InitSession performs the XDG portal handshake and starts the session.
func (p *Portal) InitSession() error {
	sessionHandle, err := p.createSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	p.session = sessionHandle
	return p.startSession()
}

// Close terminates the portal session and the underlying D-Bus connection.
func (p *Portal) Close() {
	if p.session != "" {
		p.connection.Object(portalDestination, p.session).
			Call(methodSessionClose, 0)
	}
	p.connection.Close()
}

// Screenshot captures the screen and returns the image as PNG bytes.
// If the session covers only part of the desktop, the image is cropped to
// the shared area so callers see only what was granted.
func (p *Portal) Screenshot() ([]byte, error) {
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(newToken("wayland_mcp_shot_")),
		"interactive":  dbus.MakeVariant(false),
	}
	responses, err := p.awaitResponses(func() ([]dbus.ObjectPath, error) {
		var requestPath dbus.ObjectPath
		err := p.dbusCall(methodScreenshot, "", options).Store(&requestPath)
		if err != nil {
			return nil, err
		}
		return []dbus.ObjectPath{requestPath}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("screenshot failed: %w", err)
	}

	// There is exactly one response.
	response := first(responses)

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

	// Crop to the shared-streams bounding box so the agent only sees the portion
	// of the desktop the user explicitly granted access to.
	if p.width > 0 && p.height > 0 {
		cropped, err := cropImage(
			data,
			int(p.minX),
			int(p.minY),
			int(p.width),
			int(p.height),
		)
		if err == nil {
			return cropped, nil
		}
		// Decoding or cropping failed; fall back to the original image.
		// Coordinates may be misaligned in this case.
	}
	return data, nil
}

// cropImage decodes src, crops it to the rectangle defined by (x, y, w, h),
// and re-encodes the result as PNG. It returns an error if decoding  encoding
// fails, or if the crop rectangle does not intersect the image.
func cropImage(src []byte, x, y, w, h int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	si, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return nil, ErrSubImageCrop
	}
	rect := image.Rect(x, y, x+w, y+h).Intersect(img.Bounds())
	if rect.Empty() {
		return nil, ErrCropOutsideBounds
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, si.SubImage(rect)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MovePointer moves the pointer to normalized coordinates in [0, 1].
func (p *Portal) MovePointer(x, y float64) error {
	if p.width == 0 || p.height == 0 {
		return ErrDimensionsUnknown
	}
	absX := x*float64(p.width) + float64(p.minX)
	absY := y*float64(p.height) + float64(p.minY)

	// Try session-relative motion first (stream ID 0).
	err := p.call(
		methodNotifyPointerMotionAbsolute,
		map[string]dbus.Variant{}, uint32(0), absX, absY,
	)
	if err == nil {
		return nil
	}
	// Fall back to targeting the specific stream under the cursor.
	s, relX, relY := p.findStream(absX, absY)
	return p.call(
		methodNotifyPointerMotionAbsolute,
		map[string]dbus.Variant{}, s.id, relX, relY,
	)
}

// Click simulates a mouse button press (state=1) or release (state=0).
func (p *Portal) Click(button, state uint32) error {
	return p.call(
		methodNotifyPointerButton,
		map[string]dbus.Variant{}, int32(button), state,
	)
}

// Scroll simulates a mouse wheel event with the given axis deltas.
func (p *Portal) Scroll(deltaX, deltaY float64) error {
	return p.call(
		methodNotifyPointerAxis,
		map[string]dbus.Variant{}, deltaX, deltaY,
	)
}

// TypeKey simulates a key press (state=1) or release (state=0) by keysym.
func (p *Portal) TypeKey(keysym, state uint32) error {
	return p.call(
		methodNotifyKeyboardKeysym,
		map[string]dbus.Variant{}, int32(keysym), state,
	)
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
	p.minX, p.minY, p.width, p.height = calculateBounds(p.streams)
	return nil
}

// calculateBounds returns the bounding box that encompasses all shared streams.
// Needed to map normalized agent coordinates (0–1) to the granted desktop
// region.
func calculateBounds(streams []stream) (minX, minY, width, height int32) {
	if len(streams) == 0 {
		return 0, 0, 0, 0
	}
	minX = streams[0].x
	minY = streams[0].y
	maxX := minX + int32(streams[0].w)
	maxY := minY + int32(streams[0].h)
	for _, s := range streams[1:] {
		minX = min(minX, s.x)
		minY = min(minY, s.y)
		maxX = max(maxX, s.x+int32(s.w))
		maxY = max(maxY, s.y+int32(s.h))
	}
	return minX, minY, maxX - minX, maxY - minY
}

func (p *Portal) createSession() (dbus.ObjectPath, error) {
	token := newToken("wayland_mcp_")
	options := map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(token),
		"handle_token":         dbus.MakeVariant(token + "_req"),
	}
	var requestPath dbus.ObjectPath
	err := p.dbusCall(methodCreateSession, options).Store(&requestPath)
	if err != nil {
		return "", fmt.Errorf("CreateSession call failed: %w", err)
	}
	response, err := p.waitForResponse(requestPath)
	if err != nil {
		return "", err
	}
	sessionHandle, ok := response["session_handle"].Value().(string)
	if !ok {
		return "", ErrNoSessionHandle
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
	requestPath, err := p.request(methodSelectSources, options)
	if err != nil {
		return "", fmt.Errorf("SelectSources call failed: %w", err)
	}
	return requestPath, nil
}

func (p *Portal) requestDevices() (dbus.ObjectPath, error) {
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(newToken("wayland_mcp_sel_")),
		"types":        dbus.MakeVariant(deviceTypeKeyboard | deviceTypePointer),
	}
	requestPath, err := p.request(methodSelectDevices, options)
	if err != nil {
		return "", fmt.Errorf("SelectDevices call failed: %w", err)
	}
	return requestPath, nil
}

func (p *Portal) requestStart() (dbus.ObjectPath, error) {
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(newToken("wayland_mcp_start_")),
	}
	requestPath, err := p.request(methodStart, noParentWindow, options)
	if err != nil {
		return "", fmt.Errorf("Start call failed: %w", err)
	}
	return requestPath, nil
}

func (p *Portal) call(method string, args ...any) error {
	return p.dbusCall(method, append([]any{p.session}, args...)...).Err
}

func (p *Portal) request(method string, args ...any) (dbus.ObjectPath, error) {
	var path dbus.ObjectPath
	err := p.dbusCall(method, append([]any{p.session}, args...)...).Store(&path)
	return path, err
}

func (p *Portal) dbusCall(method string, args ...any) *dbus.Call {
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
			dims, ok := variantToInt32Slice(v)
			if !ok || len(dims) < 2 {
				return nil, fmt.Errorf("stream %d has invalid 'size' property", id)
			}
			s.w, s.h = uint32(dims[0]), uint32(dims[1])
		} else {
			return nil, fmt.Errorf("stream %d missing required 'size' property", id)
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
	var s struct{ X, Y int32 }
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
	submit func() ([]dbus.ObjectPath, error),
) (map[dbus.ObjectPath]map[string]dbus.Variant, error) {
	signalChan, stop, err := p.setupResponseListener()
	if err != nil {
		return nil, err
	}
	defer stop()

	paths, err := submit()
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
	pending := make(map[dbus.ObjectPath]bool, len(paths))
	for _, path := range paths {
		pending[path] = true
	}

	for signal := range signalChan {
		if signal.Name != signalResponse || !pending[signal.Path] {
			continue
		}
		if len(signal.Body) < 2 {
			return nil, ErrInvalidResponseBody
		}
		code, ok := signal.Body[0].(uint32)
		if !ok {
			return nil, ErrInvalidResponseBody
		}
		if code != 0 {
			return nil, fmt.Errorf(
				"portal request cancelled or denied (path=%s, code=%d)",
				signal.Path, code,
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

func (p *Portal) findStream(absX, absY float64) (stream, float64, float64) {
	for _, s := range p.streams {
		if absX >= float64(s.x) && absX < float64(s.x+int32(s.w)) &&
			absY >= float64(s.y) && absY < float64(s.y+int32(s.h)) {
			return s, absX - float64(s.x), absY - float64(s.y)
		}
	}
	// Point is outside all streams; clamp to the first stream.
	s := p.streams[0]
	return s,
		clamp(absX-float64(s.x), 0, float64(s.w)-1),
		clamp(absY-float64(s.y), 0, float64(s.h)-1)
}

// newToken generates a unique D-Bus handle token with the given prefix.
func newToken(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.New().String(), "-", "")
}

func clamp(v, lo, hi float64) float64 {
	return max(lo, min(v, hi))
}

// first returns an arbitrary value from m. Intended for single-entry maps.
func first[K comparable, V any](m map[K]V) V {
	for _, v := range m {
		return v
	}
	var zero V
	return zero
}

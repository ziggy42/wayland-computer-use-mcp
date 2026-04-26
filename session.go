package main

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"strings"

	"github.com/godbus/dbus/v5"
)

// Session holds an active Remote Desktop + ScreenCast session. All fields are
// immutable after construction.
type Session struct {
	connection *dbus.Conn
	handle     dbus.ObjectPath
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

// Screenshot captures the screen and returns the image as PNG bytes.
// If the session covers only part of the desktop, the image is cropped to
// the shared area so callers see only what was granted.
func (s *Session) Screenshot() ([]byte, error) {
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(newToken("wayland_mcp_shot_")),
		"interactive":  dbus.MakeVariant(false),
	}
	responses, err := awaitResponses(
		s.connection,
		func() ([]dbus.ObjectPath, error) {
			var requestPath dbus.ObjectPath
			err := portalCall(
				s.connection, methodScreenshot, "", options,
			).Store(&requestPath)
			if err != nil {
				return nil, err
			}
			return []dbus.ObjectPath{requestPath}, nil
		},
	)
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
	if s.width > 0 && s.height > 0 {
		cropped, err := cropImage(
			data,
			int(s.minX),
			int(s.minY),
			int(s.width),
			int(s.height),
		)
		if err == nil {
			return cropped, nil
		}
		// Decoding or cropping failed; fall back to the original image.
		// Coordinates may be misaligned in this case.
	}
	return data, nil
}

// MovePointer moves the pointer to normalized coordinates in [0, 1].
func (s *Session) MovePointer(x, y float64) error {
	if s.width == 0 || s.height == 0 {
		return ErrDimensionsUnknown
	}
	absX := x*float64(s.width) + float64(s.minX)
	absY := y*float64(s.height) + float64(s.minY)

	// Try session-relative motion first (stream ID 0).
	err := s.call(
		methodNotifyPointerMotionAbsolute,
		map[string]dbus.Variant{}, uint32(0), absX, absY,
	)
	if err == nil {
		return nil
	}
	// Fall back to targeting the specific stream under the cursor.
	st, relX, relY := s.findStream(absX, absY)
	return s.call(
		methodNotifyPointerMotionAbsolute,
		map[string]dbus.Variant{}, st.id, relX, relY,
	)
}

// Click simulates a mouse button press (state=1) or release (state=0).
func (s *Session) Click(button, state uint32) error {
	return s.call(
		methodNotifyPointerButton,
		map[string]dbus.Variant{}, int32(button), state,
	)
}

// Scroll simulates a mouse wheel event with the given axis deltas.
func (s *Session) Scroll(deltaX, deltaY float64) error {
	return s.call(
		methodNotifyPointerAxis,
		map[string]dbus.Variant{}, deltaX, deltaY,
	)
}

// TypeKey simulates a key press (state=1) or release (state=0) by keysym.
func (s *Session) TypeKey(keysym, state uint32) error {
	return s.call(
		methodNotifyKeyboardKeysym,
		map[string]dbus.Variant{}, int32(keysym), state,
	)
}

func (s *Session) call(method string, args ...any) error {
	return portalCall(
		s.connection, method, append([]any{s.handle}, args...)...,
	).Err
}

func (s *Session) findStream(
	absX, absY float64,
) (stream, float64, float64) {
	for _, st := range s.streams {
		if absX >= float64(st.x) && absX < float64(st.x+int32(st.w)) &&
			absY >= float64(st.y) && absY < float64(st.y+int32(st.h)) {
			return st, absX - float64(st.x), absY - float64(st.y)
		}
	}
	// Point is outside all streams; clamp to the first stream.
	st := s.streams[0]
	return st,
		clamp(absX-float64(st.x), 0, float64(st.w)-1),
		clamp(absY-float64(st.y), 0, float64(st.h)-1)
}

// cropImage decodes src, crops it to the rectangle defined by (x, y, w, h),
// and re-encodes the result as PNG. It returns an error if decoding/encoding
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

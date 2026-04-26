package main

import (
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
)

// session holds an active Remote Desktop + ScreenCast session. All fields are
// immutable after construction.
type session struct {
	connection *dbus.Conn
	handle     dbus.ObjectPath
	minX       int32 // left edge of the shared area bounding box
	minY       int32 // top edge of the shared area bounding box
	width      int32 // width of the shared area bounding box
	height     int32 // height of the shared area bounding box
	streams    []stream
	pwRemote   *os.File // PipeWire remote FD for screen capture
}

func (s *session) close() error {
	if s.pwRemote != nil {
		s.pwRemote.Close()
	}
	return nil
}

// stream describes one screen-cast source returned by the portal.
// Position (x, y) may be negative in multi-monitor setups; size (w, h) is
// always positive.
type stream struct {
	id   uint32
	x, y int32
	w, h uint32
}

// screenshot captures the shared screens via PipeWire and returns a composited
// PNG. For multi-monitor setups, each stream is captured separately and
// painted onto a single canvas matching the layout.
func (s *session) screenshot() ([]byte, error) {
	return captureScreenshot(
		s.pwRemote, s.streams,
		s.minX, s.minY, s.width, s.height,
	)
}

// movePointer moves the pointer to normalized coordinates in [0, 1].
func (s *session) movePointer(x, y float64) error {
	if s.width == 0 || s.height == 0 {
		return errDimensionsUnknown
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

// click simulates a mouse button press (state=1) or release (state=0).
func (s *session) click(button, state uint32) error {
	return s.call(
		methodNotifyPointerButton,
		map[string]dbus.Variant{}, int32(button), state,
	)
}

// scroll simulates a mouse wheel event with the given axis deltas.
func (s *session) scroll(deltaX, deltaY float64) error {
	return s.call(
		methodNotifyPointerAxis,
		map[string]dbus.Variant{}, deltaX, deltaY,
	)
}

// typeKey simulates a key press (state=1) or release (state=0) by keysym.
func (s *session) typeKey(keysym, state uint32) error {
	return s.call(
		methodNotifyKeyboardKeysym,
		map[string]dbus.Variant{}, int32(keysym), state,
	)
}

func (s *session) call(method string, args ...any) error {
	return portalCall(
		s.connection, method, append([]any{s.handle}, args...)...,
	).Err
}

func (s *session) findStream(
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

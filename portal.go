package main

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	sourceTypeScreen   uint32 = 1
	cursorModeEmbedded uint32 = 2
	deviceTypeKeyboard uint32 = 1
	deviceTypePointer  uint32 = 2
)

// Portal manages the D-Bus connection and the lifecycle of a Remote Desktop /
// ScreenCast session. Use Session() to obtain the active Session.
type Portal struct {
	connection *dbus.Conn

	// ready is closed by InitSession when the portal handshake completes
	// successfully or not. Closing the channel establishes a happens-before
	// edge per the Go memory model, so the write to session/initErr is
	// visible to any goroutine that reads from this channel. This lets tool
	// handlers call Session() to safely obtain the Session without a mutex.
	ready   chan struct{}
	initErr error    // set before ready is closed; read-only after
	session *Session // set before ready is closed; nil on failure
}

// NewPortal creates a new Portal instance connected to the session D-Bus.
func NewPortal() (*Portal, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session bus: %w", err)
	}
	return &Portal{connection: conn, ready: make(chan struct{})}, nil
}

// InitSession performs the XDG portal handshake. It closes the ready channel
// when done, storing either a usable Session or an error.
func (p *Portal) InitSession() {
	defer close(p.ready)
	handle, err := p.createSession()
	if err != nil {
		p.initErr = fmt.Errorf("failed to create session: %w", err)
		return
	}
	session, err := p.startSession(handle)
	if err != nil {
		p.initErr = err
		return
	}
	p.session = session
}

// Session returns the active Session if initialization succeeded. Returns nil
// and ErrPortalNotReady if initialization is in progress, or the init error if
// it failed.
func (p *Portal) Session() (*Session, error) {
	select {
	case <-p.ready:
		return p.session, p.initErr
	default:
		return nil, ErrPortalNotReady
	}
}

// Close terminates the portal session and the underlying D-Bus connection.
func (p *Portal) Close() {
	if p.session != nil {
		p.connection.Object(portalDestination, p.session.handle).
			Call(methodSessionClose, 0)
	}
	p.connection.Close()
}

func (p *Portal) createSession() (dbus.ObjectPath, error) {
	token := newToken("wayland_mcp_")
	options := map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(token),
		"handle_token":         dbus.MakeVariant(token + "_req"),
	}
	var requestPath dbus.ObjectPath
	err := portalCall(
		p.connection, methodCreateSession, options,
	).Store(&requestPath)
	if err != nil {
		return "", fmt.Errorf("CreateSession call failed: %w", err)
	}
	response, err := waitForResponse(p.connection, requestPath)
	if err != nil {
		return "", err
	}
	sessionHandle, ok := response["session_handle"].Value().(string)
	if !ok {
		return "", ErrNoSessionHandle
	}
	return dbus.ObjectPath(sessionHandle), nil
}

func (p *Portal) startSession(
	handle dbus.ObjectPath,
) (*Session, error) {
	responses, err := awaitResponses(
		p.connection,
		func() ([]dbus.ObjectPath, error) {
			sourcesPath, err := p.requestSources(handle)
			if err != nil {
				return nil, err
			}
			devicesPath, err := p.requestDevices(handle)
			if err != nil {
				return nil, err
			}
			startPath, err := p.requestStart(handle)
			if err != nil {
				return nil, err
			}
			return []dbus.ObjectPath{
				sourcesPath, devicesPath, startPath,
			}, nil
		},
	)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	if len(streams) == 0 {
		return nil, ErrNoStreamsInResponse
	}

	minX, minY, width, height := calculateBounds(streams)
	return &Session{
		connection: p.connection,
		handle:     handle,
		minX:       minX,
		minY:       minY,
		width:      width,
		height:     height,
		streams:    streams,
	}, nil
}

func (p *Portal) requestSources(
	handle dbus.ObjectPath,
) (dbus.ObjectPath, error) {
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(newToken("wayland_mcp_src_")),
		"types":        dbus.MakeVariant(sourceTypeScreen),
		"multiple":     dbus.MakeVariant(true),
		"cursor_mode":  dbus.MakeVariant(cursorModeEmbedded),
	}
	requestPath, err := portalRequest(
		p.connection, handle, methodSelectSources, options,
	)
	if err != nil {
		return "", fmt.Errorf("SelectSources call failed: %w", err)
	}
	return requestPath, nil
}

func (p *Portal) requestDevices(
	handle dbus.ObjectPath,
) (dbus.ObjectPath, error) {
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(newToken("wayland_mcp_sel_")),
		"types":        dbus.MakeVariant(deviceTypeKeyboard | deviceTypePointer),
	}
	requestPath, err := portalRequest(
		p.connection, handle, methodSelectDevices, options,
	)
	if err != nil {
		return "", fmt.Errorf("SelectDevices call failed: %w", err)
	}
	return requestPath, nil
}

func (p *Portal) requestStart(
	handle dbus.ObjectPath,
) (dbus.ObjectPath, error) {
	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(newToken("wayland_mcp_start_")),
	}
	requestPath, err := portalRequest(
		p.connection, handle, methodStart, noParentWindow, options,
	)
	if err != nil {
		return "", fmt.Errorf("Start call failed: %w", err)
	}
	return requestPath, nil
}

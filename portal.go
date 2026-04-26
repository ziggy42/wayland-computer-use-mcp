package main

import (
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
)

const (
	sourceTypeScreen   uint32 = 1
	cursorModeEmbedded uint32 = 2
	deviceTypeKeyboard uint32 = 1
	deviceTypePointer  uint32 = 2
)

// portal manages the D-Bus connection and the lifecycle of a Remote Desktop /
// ScreenCast session.
type portal struct {
	connection *dbus.Conn

	// ready is closed by initSession when the portal handshake completes
	// successfully or not. Closing the channel establishes a happens-before
	// edge per the Go memory model, so the write to session/initErr is
	// visible to any goroutine that reads from this channel. This lets tool
	// handlers call getSession() to safely obtain the session without a mutex.
	ready   chan struct{}
	initErr error    // set before ready is closed; read-only after
	session *session // set before ready is closed; nil on failure
}

// newPortal creates a new portal instance connected to the session D-Bus.
func newPortal() (*portal, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session bus: %w", err)
	}
	return &portal{connection: conn, ready: make(chan struct{})}, nil
}

// initSession performs the XDG portal handshake. It closes the ready channel
// when done, storing either a usable session or an error.
func (p *portal) initSession() {
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

// getSession returns the active session if initialization succeeded. Returns
// nil and errPortalNotReady if initialization is in progress, or the init error
// if it failed.
func (p *portal) getSession() (*session, error) {
	select {
	case <-p.ready:
		return p.session, p.initErr
	default:
		return nil, errPortalNotReady
	}
}

// close terminates the portal session and the underlying D-Bus connection.
func (p *portal) close() {
	if p.session != nil {
		p.session.close()
		p.connection.Object(portalDestination, p.session.handle).
			Call(methodSessionClose, 0)
	}
	p.connection.Close()
}

func (p *portal) createSession() (dbus.ObjectPath, error) {
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
		return "", errNoSessionHandle
	}
	return dbus.ObjectPath(sessionHandle), nil
}

func (p *portal) startSession(
	handle dbus.ObjectPath,
) (*session, error) {
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
		return nil, errNoStreamsInResponse
	}

	pwRemote, err := p.openPipeWireRemote(handle)
	if err != nil {
		return nil, fmt.Errorf("failed to open PipeWire remote: %w", err)
	}

	minX, minY, width, height := calculateBounds(streams)
	return &session{
		connection: p.connection,
		handle:     handle,
		minX:       minX,
		minY:       minY,
		width:      width,
		height:     height,
		streams:    streams,
		pwRemote:   pwRemote,
	}, nil
}

func (p *portal) openPipeWireRemote(
	handle dbus.ObjectPath,
) (*os.File, error) {
	var fd dbus.UnixFD
	err := portalCall(
		p.connection,
		methodOpenPipeWireRemote,
		handle,
		map[string]dbus.Variant{},
	).Store(&fd)
	if err != nil {
		return nil, fmt.Errorf("OpenPipeWireRemote call failed: %w", err)
	}
	return os.NewFile(uintptr(fd), "pipewire-remote"), nil
}

func (p *portal) requestSources(
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

func (p *portal) requestDevices(
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

func (p *portal) requestStart(
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

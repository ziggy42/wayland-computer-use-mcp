package main

import (
	"errors"
	"fmt"
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
	ErrPortalNotReady      = errors.New("waiting for user to grant permissions, please retry")
)

func portalCall(
	conn *dbus.Conn, method string, args ...any,
) *dbus.Call {
	return conn.Object(portalDestination, portalPath).
		Call(method, 0, args...)
}

func portalRequest(
	conn *dbus.Conn,
	handle dbus.ObjectPath,
	method string,
	args ...any,
) (dbus.ObjectPath, error) {
	var path dbus.ObjectPath
	err := portalCall(
		conn, method, append([]any{handle}, args...)...,
	).Store(&path)
	return path, err
}

func waitForResponse(
	conn *dbus.Conn,
	requestPath dbus.ObjectPath,
) (map[string]dbus.Variant, error) {
	responses, err := awaitResponses(
		conn,
		func() ([]dbus.ObjectPath, error) {
			return []dbus.ObjectPath{requestPath}, nil
		},
	)
	if err != nil {
		return nil, err
	}
	return responses[requestPath], nil
}

func awaitResponses(
	conn *dbus.Conn,
	submit func() ([]dbus.ObjectPath, error),
) (map[dbus.ObjectPath]map[string]dbus.Variant, error) {
	signalChan, stop, err := setupResponseListener(conn)
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

func setupResponseListener(
	conn *dbus.Conn,
) (chan *dbus.Signal, func(), error) {
	err := conn.AddMatchSignal(
		dbus.WithMatchInterface(requestInterface),
		dbus.WithMatchMember(responseMember),
	)
	if err != nil {
		return nil, nil, err
	}
	signalChan := make(chan *dbus.Signal, 100)
	conn.Signal(signalChan)
	stop := func() {
		conn.RemoveSignal(signalChan)
		conn.RemoveMatchSignal(
			dbus.WithMatchInterface(requestInterface),
			dbus.WithMatchMember(responseMember),
		)
	}
	return signalChan, stop, nil
}

// newToken generates a unique D-Bus handle token with the given prefix.
func newToken(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.New().String(), "-", "")
}

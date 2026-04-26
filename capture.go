package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// captureScreenshot grabs one frame from each PipeWire stream via GStreamer
// and returns the result as PNG bytes.
//
// For multi-monitor setups the portal returns one stream per shared screen,
// each with a compositor-space position (x, y) and size (w, h). The bounding
// box of all streams is described by minX, minY, width and height (computed by
// calculateBounds). We create a canvas of that size and paint each captured
// image at its position relative to the top-left corner of the bounding box.
func captureScreenshot(
	pwRemote *os.File,
	streams []stream,
	minX, minY, width, height int32,
) ([]byte, error) {
	if len(streams) == 0 {
		return nil, errNoStreamsInResponse
	}

	// Single stream fast path: no compositing needed.
	if len(streams) == 1 {
		img, err := captureStream(pwRemote, streams[0])
		if err != nil {
			return nil, err
		}
		return encodePNG(img)
	}

	// Multi-monitor: capture each stream, then composite.
	images := make([]image.Image, len(streams))
	for i, st := range streams {
		img, err := captureStream(pwRemote, st)
		if err != nil {
			return nil, fmt.Errorf("failed to capture stream %d: %w", st.id, err)
		}
		images[i] = img
	}

	return compositeStreams(images, streams, minX, minY, width, height)
}

func captureStream(pwRemote *os.File, st stream) (image.Image, error) {
	// Duplicate the PipeWire FD because gst-launch-1.0 will inherit and
	// potentially close it. We need the original FD to survive for future
	// calls.
	dupFd, err := dupFD(pwRemote)
	if err != nil {
		return nil, fmt.Errorf("failed to dup PipeWire FD: %w", err)
	}
	defer dupFd.Close()

	tmpFile, err := os.CreateTemp("", "wayland-mcp-*.png")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		"gst-launch-1.0",
		"pipewiresrc",
		fmt.Sprintf("fd=%d", 3),
		fmt.Sprintf("path=%d", st.id),
		"num-buffers=1",
		"!", "videoconvert",
		"!", "pngenc",
		"!", "filesink",
		fmt.Sprintf("location=%s", tmpPath),
	)

	// ExtraFiles[0] becomes FD 3 in the child process (after stdin=0, stdout=1,
	// stderr=2).
	cmd.ExtraFiles = []*os.File{dupFd}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf(
			"gst-launch-1.0 failed: %w\noutput: %s",
			err, string(output),
		)
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open screenshot: %w", err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("failed to decode screenshot: %w", err)
	}
	return img, nil
}

func compositeStreams(
	images []image.Image,
	streams []stream,
	minX, minY, width, height int32,
) ([]byte, error) {
	canvas := image.NewRGBA(
		image.Rect(0, 0, int(width), int(height)),
	)

	for i, img := range images {
		st := streams[i]
		// Offset each image by its position relative to the bounding box origin.
		destX := int(st.x - minX)
		destY := int(st.y - minY)
		draw.Draw(
			canvas,
			image.Rect(
				destX, destY,
				destX+img.Bounds().Dx(),
				destY+img.Bounds().Dy(),
			),
			img,
			img.Bounds().Min,
			draw.Src,
		)
	}

	return encodePNG(canvas)
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func dupFD(f *os.File) (*os.File, error) {
	newFd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(newFd), f.Name()), nil
}

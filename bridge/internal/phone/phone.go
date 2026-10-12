// Package phone shows an Android device adb reaches, over USB or Wi-Fi, through the pinned scrcpy server.
package phone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/adb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/scrcpy"
)

const (
	// The server needs a moment to start listening; until it does, adb forward accepts and closes at once.
	connectTimeout = 15 * time.Second
	connectRetry   = 100 * time.Millisecond
	cleanupTimeout = 5 * time.Second
	// A control message the server does not take within this long counts it gone.
	writeTimeout = 2 * time.Second
	// Over the network the video gets half of scrcpy's default 8 Mbps, plenty for a phone screen at the stream's size.
	wifiBitRate = 4_000_000
)

var (
	sizeLine     = regexp.MustCompile(`(?m)^(?:Physical|Override) size: ([0-9]+)x([0-9]+)\s*$`)
	rotationLine = regexp.MustCompile(`(?m)^\s*mRotation=([0-3])\b`)
	// ErrNotStreaming is returned for input before the scrcpy server is connected, or after it went away.
	ErrNotStreaming = errors.New("the phone's screen is not streaming yet")
)

// keycodes are Android's KeyEvent codes for the key names the live view and Claude's tool send.
var keycodes = map[string]int32{
	"GoHome": 3, "GoBack": 4, "AppSwitch": 187, "Power": 26,
	"Enter": 66, "Backspace": 67, "Delete": 112, "Tab": 61,
	"ArrowLeft": 21, "ArrowRight": 22, "ArrowUp": 19, "ArrowDown": 20,
	"AudioVolumeUp": 24, "AudioVolumeDown": 25,
}

var _ device.Device = (*Phone)(nil)

// Options say where the phone's adb and the server's local copy are.
type Options struct {
	ADB    string
	Logger *slog.Logger
	// CacheDir holds the server jar the bridge pushes.
	CacheDir string
}

// Phone is a device.Device for one phone adb reaches.
type Phone struct {
	shell   adb.Device
	isWiFi  bool
	model   string
	panel   device.Size
	options Options

	mu      sync.Mutex
	control net.Conn
	video   device.Size
	// sending keeps control messages whole and in order, and guards isPressed, without holding up the video.
	sending   sync.Mutex
	isPressed bool
	// rotation is the display's as of the newest config packet.
	rotation atomic.Int32
	// Rotate reads the rotation before setting it, so two at once would turn the phone once.
	rotating sync.Mutex
}

// Dial reads the phone's model and screen size; Stream then starts the scrcpy server on it.
func Dial(ctx context.Context, listed adb.Listed, options Options) (*Phone, error) {
	shell := adb.Device{Path: options.ADB, Serial: listed.Serial}
	answer, err := shell.Run(ctx, "shell", "getprop ro.product.model; wm size")
	if err != nil {
		return nil, err
	}
	model, sizes, _ := strings.Cut(answer, "\n")
	panel, err := parseSize(sizes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", listed.Serial, err)
	}

	return &Phone{shell: shell, isWiFi: IsWiFi(listed), model: strings.TrimSpace(model), panel: panel, options: options}, nil
}

// parseSize reads `wm size`: the override when one is set, since the display then draws at it, else the physical size.
func parseSize(answer string) (device.Size, error) {
	matches := sizeLine.FindAllStringSubmatch(answer, -1)
	if matches == nil {
		return device.Size{}, fmt.Errorf("wm size answered %q", strings.TrimSpace(answer))
	}
	last := matches[len(matches)-1]
	width, _ := strconv.Atoi(last[1])
	height, _ := strconv.Atoi(last[2])

	return device.Size{Width: width, Height: height}, nil
}

func (p *Phone) Serial() string { return p.shell.Serial }

func (p *Phone) Kind() device.Kind { return device.Phone }

func (p *Phone) Name() string { return p.model }

func (p *Phone) Panel() device.Size { return p.panel }

// IsWiFi reports whether adb reaches the phone over the network.
func (p *Phone) IsWiFi() bool { return p.isWiFi }

func (p *Phone) Close() error { return nil }

// maxSize is the video's long side for a short side of width, never above the panel's.
func (p *Phone) maxSize(width int) int {
	short, long := min(p.panel.Width, p.panel.Height), max(p.panel.Width, p.panel.Height)
	return int(math.Round(float64(min(width, short)) * float64(long) / float64(short)))
}

// Stream runs the scrcpy server on the phone and hands on its H.264 packets until ctx ends or the server goes away.
func (p *Phone) Stream(ctx context.Context, width int, onFrame func(device.Frame)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jar, err := p.serverJar()
	if err != nil {
		return err
	}
	if _, err := p.shell.Run(ctx, "push", jar, scrcpy.DevicePath); err != nil {
		return err
	}
	server := scrcpy.Options{SCID: rand.Uint32() >> 1, MaxSize: p.maxSize(width)}
	if p.isWiFi {
		server.BitRate = wifiBitRate
	}
	forwarded, err := p.shell.Run(ctx, "forward", "tcp:0", "localabstract:"+server.SocketName())
	if err != nil {
		return err
	}
	port := strings.TrimSpace(forwarded)
	if _, err := strconv.Atoi(port); err != nil {
		return fmt.Errorf("adb forward answered %q instead of a port", port)
	}
	defer p.removeForward(port)

	running := exec.CommandContext(ctx, p.shell.Path, "-s", p.shell.Serial, "shell", server.Command())
	log := &serverLog{logger: p.options.Logger}
	running.Stdout, running.Stderr = log, log
	running.WaitDelay = cleanupTimeout
	if err := running.Start(); err != nil {
		return err
	}
	exited := make(chan struct{})
	go func() {
		running.Wait()
		close(exited)
	}()
	defer func() {
		cancel()
		<-exited
	}()

	address := net.JoinHostPort("127.0.0.1", port)
	video, err := connectVideo(ctx, address, exited)
	if err != nil {
		return fmt.Errorf("%w; the server said: %s", err, log.last())
	}
	control, err := net.Dial("tcp", address)
	if err != nil {
		video.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		video.Close()
		control.Close()
	}()
	// The server writes device messages back; draining them keeps it from blocking.
	go io.Copy(io.Discard, control)
	p.sending.Lock()
	p.isPressed = false
	p.sending.Unlock()
	p.mu.Lock()
	p.control = control
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.control = nil
		p.mu.Unlock()
	}()

	err = p.readVideo(ctx, video, onFrame)
	if ctx.Err() != nil {
		return ctx.Err()
	}

	return err
}

func (p *Phone) readVideo(ctx context.Context, video net.Conn, onFrame func(device.Frame)) error {
	if _, err := scrcpy.ReadMeta(video); err != nil {
		return err
	}
	var seq uint32
	for {
		packet, err := scrcpy.ReadPacket(video)
		if err != nil {
			return err
		}
		if packet.IsConfig {
			sps, err := scrcpy.ParseConfig(packet.Data)
			if err != nil {
				return err
			}
			size := device.Size{Width: sps.Width, Height: sps.Height}
			if size != p.videoSize() {
				p.rotation.Store(int32(p.readRotation(ctx, size)))
				p.mu.Lock()
				p.video = size
				p.mu.Unlock()
			}
		}
		seq++
		size := p.videoSize()
		onFrame(device.Frame{
			Seq: seq, Width: size.Width, Height: size.Height, Rotation: p.Rotation(),
			Video: &device.Packet{Data: packet.Data, IsConfig: packet.IsConfig, IsKeyFrame: packet.IsKeyFrame},
		})
	}
}

// connectVideo connects to the forwarded port until the server answers with its dummy byte, or gives up.
func connectVideo(ctx context.Context, address string, exited <-chan struct{}) (net.Conn, error) {
	deadline := time.Now().Add(connectTimeout)
	for {
		conn, err := net.Dial("tcp", address)
		if err == nil {
			conn.SetReadDeadline(time.Now().Add(time.Second))
			var dummy [1]byte
			if _, err := io.ReadFull(conn, dummy[:]); err == nil {
				conn.SetReadDeadline(time.Time{})
				return conn, nil
			}
			conn.Close()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-exited:
			return nil, errors.New("the scrcpy server exited before it listened")
		case <-time.After(connectRetry):
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the scrcpy server did not listen within %s", connectTimeout)
		}
	}
}

func (p *Phone) removeForward(port string) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	p.shell.Run(ctx, "forward", "--remove", "tcp:"+port)
}

// serverJar writes the embedded server where adb can push it from, once per bridge build.
func (p *Phone) serverJar() (string, error) {
	path := filepath.Join(p.options.CacheDir, filepath.Base(scrcpy.DevicePath))
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, scrcpy.Server) {
		return path, nil
	}
	if err := os.MkdirAll(p.options.CacheDir, 0o755); err != nil {
		return "", err
	}
	staged, err := os.CreateTemp(p.options.CacheDir, "scrcpy-server-*.jar")
	if err != nil {
		return "", err
	}
	_, err = staged.Write(scrcpy.Server)
	if closeErr := staged.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(staged.Name(), path)
	}
	if err != nil {
		os.Remove(staged.Name())
		return "", err
	}

	return path, nil
}

func (p *Phone) videoSize() device.Size {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.video
}

// readRotation asks the window manager how the display is turned; a video that disagrees with it goes by its shape.
func (p *Phone) readRotation(ctx context.Context, video device.Size) device.Rotation {
	isTurned := (video.Width > video.Height) != (p.panel.Width > p.panel.Height)
	rotation, err := p.displayRotation(ctx)
	if err == nil && (rotation&1 == 1) == isTurned {
		return rotation
	}
	if isTurned {
		return device.Landscape
	}

	return device.Portrait
}

func (p *Phone) displayRotation(ctx context.Context) (device.Rotation, error) {
	answer, err := p.shell.Run(ctx, "shell", "dumpsys", "window", "displays")
	if err != nil {
		return 0, err
	}
	match := rotationLine.FindStringSubmatch(answer)
	if match == nil {
		return 0, errors.New("dumpsys window names no display rotation")
	}
	rotation, _ := strconv.Atoi(match[1])

	return device.Rotation(rotation), nil
}

// lockedRotation is user_rotation while auto-rotate is off, else the display's rotation.
func (p *Phone) lockedRotation(ctx context.Context) (device.Rotation, error) {
	answer, err := p.shell.Run(ctx, "shell", "settings get system accelerometer_rotation; settings get system user_rotation")
	if err != nil {
		return 0, err
	}
	values := strings.Fields(answer)
	if len(values) == 2 && values[0] == "0" {
		if locked, err := strconv.Atoi(values[1]); err == nil && locked >= 0 && locked < 4 {
			return device.Rotation(locked), nil
		}
	}

	return p.displayRotation(ctx)
}

// Rotation is the display's rotation as the newest config packet found it.
func (p *Phone) Rotation() device.Rotation { return device.Rotation(p.rotation.Load()) }

func (p *Phone) Upright() device.Size {
	if p.Rotation()&1 == 1 {
		return device.Size{Width: p.panel.Height, Height: p.panel.Width}
	}

	return p.panel
}

// send writes control messages to the server, in order and whole.
func (p *Phone) send(ctx context.Context, messages ...[]byte) error {
	p.sending.Lock()
	defer p.sending.Unlock()

	return p.sendLocked(ctx, messages...)
}

// sendLocked is send for a caller that holds p.sending.
func (p *Phone) sendLocked(ctx context.Context, messages ...[]byte) error {
	p.mu.Lock()
	control := p.control
	p.mu.Unlock()
	if control == nil {
		return ErrNotStreaming
	}
	deadline := time.Now().Add(writeTimeout)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	control.SetWriteDeadline(deadline)
	for _, message := range messages {
		if _, err := control.Write(message); err != nil {
			return err
		}
	}

	return nil
}

// Touch presses, moves or lifts one finger at fractions of the upright screen, in the current video's pixels.
func (p *Phone) Touch(ctx context.Context, x, y float64, isDown bool) error {
	video := p.videoSize()
	p.sending.Lock()
	defer p.sending.Unlock()
	action := byte(scrcpy.ActionUp)
	switch {
	case isDown && p.isPressed:
		action = scrcpy.ActionMove
	case isDown:
		action = scrcpy.ActionDown
	case !p.isPressed:
		return nil
	}
	if video.Width <= 0 || video.Height <= 0 {
		return ErrNotStreaming
	}
	touch := scrcpy.Touch{
		Action:  action,
		Pointer: scrcpy.PointerFinger,
		X:       device.ToPixel(x, video.Width),
		Y:       device.ToPixel(y, video.Height),
		Width:   uint16(video.Width),
		Height:  uint16(video.Height),
	}
	if err := p.sendLocked(ctx, touch.Message()); err != nil {
		return err
	}
	p.isPressed = isDown

	return nil
}

func (p *Phone) Key(ctx context.Context, key string) error {
	code, ok := keycodes[key]
	if !ok {
		return fmt.Errorf("%w %q", device.ErrUnknownKey, key)
	}

	return p.send(ctx, scrcpy.KeyMessage(scrcpy.ActionDown, code), scrcpy.KeyMessage(scrcpy.ActionUp, code))
}

// Text types through the phone's key map, which has no keys for characters outside it.
func (p *Phone) Text(ctx context.Context, text string) error {
	return p.send(ctx, scrcpy.TextMessages(text)...)
}

// ResetVideo asks the server for a fresh config packet and key frame.
func (p *Phone) ResetVideo() error {
	return p.send(context.Background(), scrcpy.ResetVideoMessage())
}

// Rotate turns the phone's locked rotation a quarter further, auto-rotate off, as turning the phone in hand turns a
// sensor: an app locked to portrait stays so. scrcpy's own rotate message only toggles.
func (p *Phone) Rotate(ctx context.Context, quarters int) error {
	p.rotating.Lock()
	defer p.rotating.Unlock()
	current, err := p.lockedRotation(ctx)
	if err != nil {
		return err
	}
	next := ((int(current)+quarters)%4 + 4) % 4
	_, err = p.shell.Run(ctx, "shell", fmt.Sprintf("settings put system accelerometer_rotation 0 && settings put system user_rotation %d", next))

	return err
}

// Screenshot returns the PNG screencap encodes, the display as it is turned now.
func (p *Phone) Screenshot(ctx context.Context) ([]byte, error) {
	answer, err := p.shell.Run(ctx, "exec-out", "screencap", "-p")
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(answer, "\x89PNG") {
		return nil, fmt.Errorf("screencap answered %d bytes that are not a PNG", len(answer))
	}

	return []byte(answer), nil
}

// ScreenshotAt scales the screencap down to fit size.
func (p *Phone) ScreenshotAt(ctx context.Context, size device.Size) (device.Frame, error) {
	encoded, err := p.Screenshot(ctx)
	if err != nil {
		return device.Frame{}, err
	}
	picture, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		return device.Frame{}, err
	}
	frame := fitRGB(picture, size)
	frame.Rotation = p.Rotation()

	return frame, nil
}

func (p *Phone) Booted(ctx context.Context) (bool, error) {
	answer, err := p.shell.Run(ctx, "shell", "getprop", "sys.boot_completed")
	if err != nil {
		return false, err
	}

	return strings.TrimSpace(answer) == "1", nil
}

func (p *Phone) Shutdown(context.Context) error { return device.ErrCannotShutDown }


// fitRGB scales picture down to size, averaging the source pixels under each one; a picture turned the other way
// than size (the phone turned since) is fitted inside it instead, keeping its shape.
func fitRGB(picture image.Image, size device.Size) device.Frame {
	pixels := toRGBA(picture)
	sourceWidth, sourceHeight := pixels.Rect.Dx(), pixels.Rect.Dy()
	width, height := min(size.Width, sourceWidth), min(size.Height, sourceHeight)
	if (sourceWidth > sourceHeight) != (size.Width > size.Height) {
		scale := min(1, float64(size.Width)/float64(sourceWidth), float64(size.Height)/float64(sourceHeight))
		width = max(1, int(math.Round(float64(sourceWidth)*scale)))
		height = max(1, int(math.Round(float64(sourceHeight)*scale)))
	}
	rgb := make([]byte, 0, width*height*3)
	for y := range height {
		top, bottom := y*sourceHeight/height, max((y+1)*sourceHeight/height, y*sourceHeight/height+1)
		for x := range width {
			left, right := x*sourceWidth/width, max((x+1)*sourceWidth/width, x*sourceWidth/width+1)
			var r, g, b, count int
			for sy := top; sy < bottom; sy++ {
				row := pixels.Pix[sy*pixels.Stride:]
				for sx := left; sx < right; sx++ {
					r, g, b, count = r+int(row[4*sx]), g+int(row[4*sx+1]), b+int(row[4*sx+2]), count+1
				}
			}
			rgb = append(rgb, byte(r/count), byte(g/count), byte(b/count))
		}
	}

	return device.Frame{Width: width, Height: height, RGB: rgb}
}

// toRGBA gives the picture's pixels premultiplied, opaque as a screen is, at origin 0, 0.
func toRGBA(picture image.Image) *image.RGBA {
	if pixels, ok := picture.(*image.RGBA); ok && pixels.Rect.Min == (image.Point{}) {
		return pixels
	}
	bounds := picture.Bounds()
	pixels := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(pixels, pixels.Rect, picture, bounds.Min, draw.Src)

	return pixels
}

// serverLog passes the server's output to the debug log and keeps its newest line for errors.
type serverLog struct {
	logger *slog.Logger
	mu     sync.Mutex
	line   string
}

func (l *serverLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			l.line = line
			l.logger.Debug("scrcpy server", "line", line)
		}
	}

	return len(data), nil
}

func (l *serverLog) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.line == "" {
		return "nothing"
	}

	return l.line
}

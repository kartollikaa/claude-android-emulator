package phone

import (
	"image"
	"image/color"
	"testing"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/adb"
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
)

const (
	usbSerial  = "5C111JEBF10513"
	wifiSerial = "192.168.1.20:5555"
	mdnsSerial = "adb-5C111JEBF10513-Ab12Cd._adb-tls-connect._tcp"
)

func TestChoose(t *testing.T) {
	serialNumbers := map[string]string{
		usbSerial: usbSerial, wifiSerial: usbSerial, mdnsSerial: usbSerial,
		"R5CT30ABCDE": "R5CT30ABCDE", "10.0.0.7:5555": "OTHER",
	}
	usb := adb.Listed{Serial: usbSerial, State: "device", IsUSB: true}
	wifi := adb.Listed{Serial: wifiSerial, State: "device"}
	mdns := adb.Listed{Serial: mdnsSerial, State: "device"}
	other := adb.Listed{Serial: "10.0.0.7:5555", State: "device"}
	emulator := adb.Listed{Serial: "emulator-5554", State: "device"}
	unauthorized := adb.Listed{Serial: "R5CT30ABCDE", State: "unauthorized", IsUSB: true}
	offline := adb.Listed{Serial: "10.0.0.9:5555", State: "offline"}
	isDiscovered := func(serial string) bool { return serial == "emulator-5554" }

	for _, step := range []struct {
		name   string
		listed []adb.Listed
		want   Choice
	}{
		{"nothing", nil, Choice{}},
		{"a phone over USB", []adb.Listed{usb}, Choice{Phone: usb, IsFound: true}},
		{"a phone over Wi-Fi", []adb.Listed{wifi}, Choice{Phone: wifi, IsFound: true}},
		{"wireless debugging through mDNS", []adb.Listed{mdns}, Choice{Phone: mdns, IsFound: true}},
		{"the same phone twice shows over USB", []adb.Listed{wifi, usb}, Choice{Phone: usb, IsFound: true}},
		{"the same phone three times shows over USB", []adb.Listed{mdns, wifi, usb}, Choice{Phone: usb, IsFound: true}},
		{"USB listed first stays", []adb.Listed{usb, wifi}, Choice{Phone: usb, IsFound: true}},
		{"two phones: the first", []adb.Listed{other, usb}, Choice{Phone: other, IsFound: true}},
		{"a discovered emulator is skipped", []adb.Listed{emulator, wifi}, Choice{Phone: wifi, IsFound: true}},
		{"only a discovered emulator", []adb.Listed{emulator}, Choice{}},
		{"unauthorized", []adb.Listed{offline, unauthorized}, Choice{Unauthorized: "R5CT30ABCDE"}},
		{"unauthorized beside a phone", []adb.Listed{unauthorized, wifi}, Choice{Phone: wifi, IsFound: true, Unauthorized: "R5CT30ABCDE"}},
	} {
		asked := 0
		got := Choose(step.listed, isDiscovered, func(serial string) string {
			asked++
			return serialNumbers[serial]
		})
		if got != step.want {
			t.Errorf("%s: chose %+v, want %+v", step.name, got, step.want)
		}
		if len(step.listed) <= 1 && asked > 0 {
			t.Errorf("%s: asked for %d serial numbers with nothing to tell apart", step.name, asked)
		}
	}
}

func TestIsWiFi(t *testing.T) {
	for serial, want := range map[adb.Listed]bool{
		{Serial: usbSerial, IsUSB: true}: false,
		{Serial: wifiSerial}:             true,
		{Serial: mdnsSerial}:             true,
		{Serial: "emulator-5554"}:        false,
	} {
		if got := IsWiFi(serial); got != want {
			t.Errorf("IsWiFi(%+v) = %v, want %v", serial, got, want)
		}
	}
}

func TestParseSize(t *testing.T) {
	for answer, want := range map[string]device.Size{
		"Physical size: 1080x2424\n":                          {Width: 1080, Height: 2424},
		"Physical size: 1080x2424\nOverride size: 720x1616\n": {Width: 720, Height: 1616},
		"Physical size: 2560x1600\r\n":                        {Width: 2560, Height: 1600},
	} {
		if got, err := parseSize(answer); err != nil || got != want {
			t.Errorf("parseSize(%q) = %+v, %v, want %+v", answer, got, err, want)
		}
	}
	if _, err := parseSize("Error: no display\n"); err == nil {
		t.Error("an answer with no size parsed")
	}
}

func TestMaxSizeKeepsTheShortSide(t *testing.T) {
	phone := &Phone{panel: device.Size{Width: 1080, Height: 2424}}
	if got := phone.maxSize(540); got != 1212 {
		t.Errorf("maxSize(540) = %d, want 1212", got)
	}
	if got := phone.maxSize(4000); got != 2424 {
		t.Errorf("maxSize(4000) = %d, want the panel's 2424", got)
	}
}

func TestEveryKeyTheLiveViewAndToolSendHasACode(t *testing.T) {
	for _, key := range []string{"GoHome", "GoBack", "AppSwitch", "Power", "Enter", "Backspace", "Delete", "Tab",
		"ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "AudioVolumeUp", "AudioVolumeDown"} {
		if _, ok := keycodes[key]; !ok {
			t.Errorf("no Android key code for %s", key)
		}
	}
}

func TestFitRGBAveragesDownToTheSize(t *testing.T) {
	picture := image.NewNRGBA(image.Rect(0, 0, 4, 8))
	for y := range 8 {
		for x := range 4 {
			shade := uint8(0)
			if x >= 2 {
				shade = 200
			}
			picture.Set(x, y, color.NRGBA{R: shade, G: uint8(10 * y), B: 7, A: 255})
		}
	}

	frame := fitRGB(picture, device.Size{Width: 2, Height: 4})

	if frame.Width != 2 || frame.Height != 4 || len(frame.RGB) != 2*4*3 {
		t.Fatalf("scaled to %d×%d with %d bytes, want 2×4", frame.Width, frame.Height, len(frame.RGB))
	}
	if got := frame.RGB[:6]; got[0] != 0 || got[1] != 5 || got[2] != 7 || got[3] != 200 || got[4] != 5 {
		t.Errorf("first row %v, want averages 0,5,7 and 200,5,7", got)
	}
}

func TestFitRGBKeepsTheShapeOfAPictureTurnedSince(t *testing.T) {
	picture := image.NewRGBA(image.Rect(0, 0, 16, 8))
	frame := fitRGB(picture, device.Size{Width: 4, Height: 8})
	if frame.Width != 4 || frame.Height != 2 {
		t.Errorf("a landscape picture in a portrait size scaled to %d×%d, want 4×2", frame.Width, frame.Height)
	}
}

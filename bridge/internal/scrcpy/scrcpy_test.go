package scrcpy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// The scrcpy-server asset of the v3.3.3 GitHub release.
const releaseDigest = "7e70323ba7f259649dd4acce97ac4fefbae8102b2c6d91e2e7be613fd5354be0"

// A Pixel 9a's config packet at max_size=1212: High profile, level 3.2, 536×1208 out of 544×1216 macroblocks.
const pixelConfig = "0000000167640020acb404404cf2cb35010101078a15500000000168ee0d8b"

func TestServerIsTheReleaseAsset(t *testing.T) {
	digest := sha256.Sum256(Server)
	if got := hex.EncodeToString(digest[:]); got != releaseDigest {
		t.Fatalf("the embedded server's sha256 is %s, want the v%s release's %s", got, Version, releaseDigest)
	}
}

func TestCommandRunsTheServerOfThisVersion(t *testing.T) {
	command := Options{SCID: 0x1a2b3c, MaxSize: 1212}.Command()
	for _, want := range []string{"CLASSPATH=" + DevicePath + " ", " com.genymobile.scrcpy.Server 3.3.3 ", " scid=001a2b3c ", " tunnel_forward=true ", " audio=false ", " control=true ", " video_codec=h264 ", " max_size=1212 ", " send_dummy_byte=true ", " clipboard_autosync=false "} {
		if !strings.Contains(command+" ", want) {
			t.Errorf("command %q lacks %q", command, want)
		}
	}
	if strings.Contains(command, "video_bit_rate") {
		t.Errorf("command %q sets a bit rate nobody asked for", command)
	}
	if command := (Options{SCID: 1, MaxSize: 800, BitRate: 4_000_000}).Command(); !strings.HasSuffix(command, " video_bit_rate=4000000") {
		t.Errorf("command %q does not set the bit rate", command)
	}
	if name := (Options{SCID: 0x1a2b3c}).SocketName(); name != "scrcpy_001a2b3c" {
		t.Errorf("socket name %q, want scrcpy_001a2b3c", name)
	}
}

func TestParseConfigOfARealPhone(t *testing.T) {
	data, _ := hex.DecodeString(pixelConfig)
	sps, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if sps != (SPS{Width: 536, Height: 1208, Codec: "avc1.640020"}) {
		t.Fatalf("parsed %+v, want 536×1208 avc1.640020", sps)
	}
}

// bitWriter builds synthetic SPS bodies.
type bitWriter struct {
	data []byte
	bits int
}

func (w *bitWriter) bit(value uint) {
	if w.bits%8 == 0 {
		w.data = append(w.data, 0)
	}
	w.data[len(w.data)-1] |= byte(value&1) << (7 - w.bits%8)
	w.bits++
}

func (w *bitWriter) ue(value uint) {
	code := value + 1
	length := 0
	for c := code; c > 1; c >>= 1 {
		length++
	}
	for range length {
		w.bit(0)
	}
	for i := length; i >= 0; i-- {
		w.bit(code >> i)
	}
}

func (w *bitWriter) se(value int) {
	if value > 0 {
		w.ue(uint(2*value - 1))
	} else {
		w.ue(uint(-2 * value))
	}
}

// escape adds the emulation prevention byte the encoder puts after each 00 00 followed by a byte up to 3.
func escape(data []byte) []byte {
	var out []byte
	zeros := 0
	for _, b := range data {
		if zeros >= 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

func annexB(profile, constraints, level byte, body []byte) []byte {
	return append([]byte{0, 0, 0, 1, 0x67, profile, constraints, level}, append(escape(body), 0, 0, 0, 1, 0x68, 0xce, 0x3c, 0x80)...)
}

func TestParseConfigCropsABaselinePicture(t *testing.T) {
	w := &bitWriter{}
	w.ue(0)       // sps id
	w.ue(0)       // log2_max_frame_num_minus4
	w.ue(0)       // pic_order_cnt_type
	w.ue(0)       // log2_max_pic_order_cnt_lsb_minus4
	w.ue(1)       // max_num_ref_frames
	w.bit(0)      // gaps
	w.ue(120 - 1) // 1920 wide
	w.ue(68 - 1)  // 1088 high
	w.bit(1)      // frame_mbs_only
	w.bit(1)      // direct_8x8
	w.bit(1)      // cropping
	w.ue(0)
	w.ue(0)
	w.ue(0)
	w.ue(4) // 8 rows off the bottom in 4:2:0
	w.bit(1)

	sps, err := ParseConfig(annexB(66, 0xc0, 0x28, w.data))
	if err != nil {
		t.Fatal(err)
	}
	if sps != (SPS{Width: 1920, Height: 1080, Codec: "avc1.42c028"}) {
		t.Fatalf("parsed %+v, want 1920×1080 avc1.42c028", sps)
	}
}

func TestParseConfigSkipsHighProfileFields(t *testing.T) {
	w := &bitWriter{}
	w.ue(0) // sps id
	w.ue(1) // chroma_format_idc 4:2:0
	w.ue(0) // bit depths
	w.ue(0)
	w.bit(0) // transform bypass
	w.bit(1) // scaling matrix present
	for i := range 8 {
		if i == 0 || i == 6 {
			w.bit(1)
			w.se(-8) // the next scale turns 0 at once, which ends the list
		} else {
			w.bit(0)
		}
	}
	w.ue(2) // log2_max_frame_num_minus4
	w.ue(1) // pic_order_cnt_type 1
	w.bit(0)
	w.se(-3)
	w.se(2)
	w.ue(2)
	w.se(1)
	w.se(-1)
	w.ue(3)      // max_num_ref_frames
	w.bit(0)     // gaps
	w.ue(45 - 1) // 720 wide
	w.ue(45 - 1) // 45 map units of fields: 1440 high
	w.bit(0)     // not frame_mbs_only
	w.bit(0)     // mb_adaptive_frame_field
	w.bit(1)     // direct_8x8
	w.bit(1)     // cropping
	w.ue(2)
	w.ue(2) // 8 columns off
	w.ue(1)
	w.ue(0) // 4 rows off: 2 × 2 for fields in 4:2:0
	w.bit(1)

	sps, err := ParseConfig(annexB(100, 0, 0x29, w.data))
	if err != nil {
		t.Fatal(err)
	}
	if sps != (SPS{Width: 712, Height: 1436, Codec: "avc1.640029"}) {
		t.Fatalf("parsed %+v, want 712×1436 avc1.640029", sps)
	}
}

func TestParseConfigUnescapesTheBody(t *testing.T) {
	w := &bitWriter{}
	w.ue(0)
	w.ue(0)
	w.ue(2) // pic_order_cnt_type 2: nothing follows
	w.ue(0)
	w.bit(0)
	w.ue(1<<23 - 1) // 23 leading zeros: 00 00 then a byte of 02 or 03, which the encoder escapes
	w.ue(64 - 1)
	w.bit(1)
	w.bit(1)
	w.bit(0)
	w.bit(1)
	if escaped := escape(w.data); len(escaped) == len(w.data) {
		t.Fatal("the synthetic body has nothing to escape")
	}

	sps, err := ParseConfig(annexB(66, 0, 0x1f, w.data))
	if err != nil {
		t.Fatal(err)
	}
	if sps.Width != 1<<23*16 || sps.Height != 64*16 {
		t.Fatalf("parsed %+v, want %d×%d", sps, 1<<23*16, 64*16)
	}
}

func TestParseConfigRefusesWhatIsNotAnSPS(t *testing.T) {
	data, _ := hex.DecodeString(pixelConfig)
	for name, packet := range map[string][]byte{
		"no SPS":    {0, 0, 0, 1, 0x68, 0xee, 0x0d, 0x8b},
		"truncated": data[:12],
		"empty":     nil,
	} {
		if sps, err := ParseConfig(packet); err == nil {
			t.Errorf("%s parsed as %+v", name, sps)
		}
	}
}

func TestReadMetaAndPackets(t *testing.T) {
	stream := []byte{0x68, 0x32, 0x36, 0x34, 0, 0, 2, 0x18, 0, 0, 4, 0xb8}
	stream = append(stream, 0x80, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0xaa, 0xbb)
	stream = append(stream, 0x40, 0, 0, 0, 0, 0, 0x30, 0x39, 0, 0, 0, 1, 0xcc)
	stream = append(stream, 0, 0, 0, 0, 0, 0, 0x30, 0x3a, 0, 0, 0, 1, 0xdd)
	reader := bytes.NewReader(stream)

	meta, err := ReadMeta(reader)
	if err != nil || meta != (Meta{Width: 536, Height: 1208}) {
		t.Fatalf("meta %+v, %v, want 536×1208", meta, err)
	}
	for _, want := range []Packet{
		{IsConfig: true, Data: []byte{0xaa, 0xbb}},
		{IsKeyFrame: true, PTS: 12345, Data: []byte{0xcc}},
		{PTS: 12346, Data: []byte{0xdd}},
	} {
		packet, err := ReadPacket(reader)
		if err != nil || packet.IsConfig != want.IsConfig || packet.IsKeyFrame != want.IsKeyFrame || packet.PTS != want.PTS || !bytes.Equal(packet.Data, want.Data) {
			t.Fatalf("packet %+v, %v, want %+v", packet, err, want)
		}
	}
	if _, err := ReadPacket(reader); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last packet: %v, want EOF", err)
	}
}

func TestReadRefusesOtherCodecsAndCutPackets(t *testing.T) {
	if _, err := ReadMeta(bytes.NewReader([]byte{0x68, 0x32, 0x36, 0x35, 0, 0, 1, 0, 0, 0, 1, 0})); err == nil {
		t.Error("H.265 metadata was accepted")
	}
	if _, err := ReadPacket(bytes.NewReader([]byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 4, 1, 2})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a cut packet read as %v, want an unexpected EOF", err)
	}
}

func TestControlMessagesAreByteExact(t *testing.T) {
	for _, step := range []struct {
		name string
		got  []byte
		want string
	}{
		{"home down", KeyMessage(ActionDown, 3), "00" + "00" + "00000003" + "00000000" + "00000000"},
		{"home up", KeyMessage(ActionUp, 3), "00" + "01" + "00000003" + "00000000" + "00000000"},
		{"touch down", Touch{Action: ActionDown, Pointer: PointerFinger, X: 268, Y: 604, Width: 536, Height: 1208}.Message(),
			"02" + "00" + "fffffffffffffffe" + "0000010c" + "0000025c" + "0218" + "04b8" + "ffff" + "00000000" + "00000000"},
		{"touch up", Touch{Action: ActionUp, Pointer: PointerFinger, X: 1, Y: 2, Width: 536, Height: 1208}.Message(),
			"02" + "01" + "fffffffffffffffe" + "00000001" + "00000002" + "0218" + "04b8" + "0000" + "00000000" + "00000000"},
		{"reset video", ResetVideoMessage(), "11"},
	} {
		if got := hex.EncodeToString(step.got); got != step.want {
			t.Errorf("%s encoded %s, want %s", step.name, got, step.want)
		}
	}
}

func TestTextIsSplitOnCharacterBoundaries(t *testing.T) {
	if got := hex.EncodeToString(TextMessages("hé")[0]); got != "01"+"00000003"+"68c3a9" {
		t.Errorf("text encoded %s", got)
	}
	long := strings.Repeat("a", MaxTextBytes-1) + "é" + "bc"
	messages := TextMessages(long)
	if len(messages) != 2 {
		t.Fatalf("%d messages, want 2", len(messages))
	}
	var joined []byte
	for _, message := range messages {
		if len(message)-5 > MaxTextBytes {
			t.Errorf("a message carries %d bytes", len(message)-5)
		}
		joined = append(joined, message[5:]...)
	}
	if string(joined) != long || len(messages[0]) != 5+MaxTextBytes-1 {
		t.Errorf("split into %d+%d bytes, rejoined %q", len(messages[0])-5, len(messages[1])-5, joined)
	}
	if TextMessages("") != nil {
		t.Error("empty text made a message")
	}
}

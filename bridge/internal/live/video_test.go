package live

import (
	"slices"
	"testing"

	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
)

func packet(name string, size int, isConfig, isKeyFrame bool) *device.Frame {
	data := make([]byte, size)
	copy(data, name)
	return &device.Frame{Video: &device.Packet{Data: data, IsConfig: isConfig, IsKeyFrame: isKeyFrame}}
}

func names(packets []*device.Frame) []string {
	var named []string
	for _, packet := range packets {
		end := slices.Index(packet.Video.Data, 0)
		if end < 0 {
			end = len(packet.Video.Data)
		}
		named = append(named, string(packet.Video.Data[:end]))
	}
	return named
}

func expect(t *testing.T, what string, packets []*device.Frame, needsReset bool, want []string, wantReset bool) {
	t.Helper()
	if got := names(packets); !slices.Equal(got, want) || needsReset != wantReset {
		t.Errorf("%s: sent %v, reset %v; want %v, reset %v", what, got, needsReset, want, wantReset)
	}
}

func TestViewersFollowThePacketsInOrder(t *testing.T) {
	var video videoLog
	var early, late videoCursor

	packets, reset := video.follow(&early)
	expect(t, "before any packet", packets, reset, nil, false)
	video.add(packet("config", 30, true, false))
	video.add(packet("key", 100, false, true))
	video.add(packet("p1", 10, false, false))
	packets, reset = video.follow(&early)
	expect(t, "first viewer", packets, reset, []string{"config", "key", "p1"}, false)
	video.add(packet("p2", 10, false, false))
	packets, reset = video.follow(&early)
	expect(t, "first viewer, next packet", packets, reset, []string{"p2"}, false)
	packets, reset = video.follow(&early)
	expect(t, "first viewer, nothing new", packets, reset, nil, false)

	packets, reset = video.follow(&late)
	expect(t, "late joiner", packets, reset, []string{"config", "key", "p1", "p2"}, false)

	video.add(packet("key2", 100, false, true))
	video.add(packet("p3", 10, false, false))
	packets, reset = video.follow(&late)
	expect(t, "after a periodic key frame", packets, reset, []string{"key2", "p3"}, false)
	var third videoCursor
	packets, reset = video.follow(&third)
	expect(t, "joiner after a periodic key frame", packets, reset, []string{"config", "key2", "p3"}, false)

	video.add(packet("config2", 30, true, false))
	video.add(packet("key3", 100, false, true))
	packets, reset = video.follow(&early)
	expect(t, "a viewer behind by a key frame starts over from the newest", packets, reset, []string{"config2", "key3"}, false)
	packets, reset = video.follow(&late)
	expect(t, "after a rotation", packets, reset, []string{"config2", "key3"}, false)
}

func TestAnOverflowingBacklogAsksForAResetOnce(t *testing.T) {
	var video videoLog
	var steady, late, later videoCursor
	video.add(packet("config", 30, true, false))
	video.add(packet("key", 100, false, true))
	video.follow(&steady)
	for range 5 {
		video.add(packet("big", maxBacklogBytes/4, false, false))
		video.follow(&steady)
	}
	video.add(packet("last", 10, false, false))
	packets, reset := video.follow(&steady)
	expect(t, "a viewer keeping up", packets, reset, []string{"last"}, false)

	packets, reset = video.follow(&late)
	expect(t, "a joiner once the key frame was dropped", packets, reset, nil, true)
	packets, reset = video.follow(&later)
	expect(t, "a second joiner while the reset is on its way", packets, reset, nil, false)

	video.add(packet("config2", 30, true, false))
	video.add(packet("key2", 100, false, true))
	packets, reset = video.follow(&late)
	expect(t, "the joiner once the reset came", packets, reset, []string{"config2", "key2"}, false)
	packets, reset = video.follow(&steady)
	expect(t, "the steady viewer through the reset", packets, reset, []string{"config2", "key2"}, false)
}

func TestAViewerFarBehindWaitsForAFreshKeyFrame(t *testing.T) {
	var video videoLog
	var slow videoCursor
	video.add(packet("config", 30, true, false))
	video.add(packet("key", 100, false, true))
	video.follow(&slow)
	for range 3 {
		video.add(packet("big", maxViewerLagBytes/2, false, false))
	}

	packets, reset := video.follow(&slow)
	expect(t, "a viewer too far behind", packets, reset, nil, true)
	video.add(packet("p", 10, false, false))
	packets, reset = video.follow(&slow)
	expect(t, "still behind, the reset asked already", packets, reset, nil, false)
	video.add(packet("config2", 30, true, false))
	video.add(packet("key2", 100, false, true))
	packets, reset = video.follow(&slow)
	expect(t, "after the reset", packets, reset, []string{"config2", "key2"}, false)
}

func TestANewDeviceStartsTheViewersOver(t *testing.T) {
	video := videoLog{generation: 1}
	var viewer videoCursor
	video.add(packet("config", 30, true, false))
	video.add(packet("key", 100, false, true))
	video.follow(&viewer)

	video = videoLog{generation: 2}
	video.add(packet("configB", 30, true, false))
	video.add(packet("keyB", 100, false, true))
	packets, reset := video.follow(&viewer)
	expect(t, "the next device's video", packets, reset, []string{"configB", "keyB"}, false)
}

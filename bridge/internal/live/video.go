package live

import (
	"github.com/kartollikaa/claude-android-emulator/bridge/internal/device"
)

const (
	// A backlog past this loses its oldest packets, down to half, and a viewer can no longer start from it.
	maxBacklogBytes = 4 << 20
	// A viewer this far behind starts over from a fresh key frame instead of draining what it missed.
	maxViewerLagBytes = 2 << 20
)

// videoLog holds a phone's packets as its viewers need them: the newest config packet, and the packets since the
// newest key frame (or config packet), numbered so that each viewer follows them in order.
type videoLog struct {
	// generation changes with every device attached, so a viewer starts over on a new one.
	generation uint64
	config     *device.Frame
	backlog    []*device.Frame
	// first numbers backlog[0], next the packet to come.
	first, next uint64
	bytes       int
	// isStartable says a viewer can start from the backlog: it opens with a key frame, or a config packet and one.
	isStartable bool
	// isResetAsked holds from a reset request until the key frame it brings.
	isResetAsked bool
}

// videoCursor is one viewer's place in the video.
type videoCursor struct {
	generation uint64
	isStarted  bool
	// position numbers the next packet to send.
	position uint64
	// minStart is the oldest backlog a viewer that fell behind may start over from.
	minStart uint64
}

// add appends a packet; the caller holds the bridge's lock.
func (v *videoLog) add(frame *device.Frame) {
	index := v.next
	v.next++
	packet := frame.Video
	followsConfig := len(v.backlog) == 1 && v.backlog[0].Video.IsConfig && v.first+1 == index
	if packet.IsConfig || packet.IsKeyFrame && !followsConfig {
		v.backlog, v.first, v.bytes = nil, index, 0
	}
	if packet.IsConfig {
		v.config, v.isStartable = frame, false
	}
	if packet.IsKeyFrame {
		v.isStartable, v.isResetAsked = true, false
	}
	v.backlog = append(v.backlog, frame)
	v.bytes += len(packet.Data)
	if v.bytes > maxBacklogBytes {
		v.isStartable = false
		for v.bytes > maxBacklogBytes/2 {
			v.bytes -= len(v.backlog[0].Video.Data)
			v.backlog = v.backlog[1:]
			v.first++
		}
	}
}

// follow answers the packets the viewer sends next, in order, and whether it needs a reset of the video to start;
// the caller holds the bridge's lock and, when told to, asks the device for the reset.
func (v *videoLog) follow(cursor *videoCursor) (packets []*device.Frame, needsReset bool) {
	if cursor.generation != v.generation {
		*cursor = videoCursor{generation: v.generation}
	}
	if cursor.isStarted && cursor.position >= v.first {
		packets = v.backlog[cursor.position-v.first:]
		if lag(packets) <= maxViewerLagBytes {
			cursor.position = v.next
			return packets, false
		}
		cursor.minStart = v.next
	}
	cursor.isStarted = false
	if v.config == nil {
		return nil, false
	}
	if !v.isStartable || v.first < cursor.minStart {
		needsReset = !v.isResetAsked
		v.isResetAsked = true
		return nil, needsReset
	}
	packets = v.backlog
	if !packets[0].Video.IsConfig {
		packets = append([]*device.Frame{v.config}, packets...)
	}
	cursor.isStarted, cursor.position = true, v.next

	return packets, false
}

func lag(packets []*device.Frame) int {
	total := 0
	for _, packet := range packets {
		total += len(packet.Video.Data)
	}

	return total
}

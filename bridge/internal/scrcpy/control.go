package scrcpy

import (
	"encoding/binary"
	"unicode/utf8"
)

// Control message types, as the server's ControlMessage numbers them.
const (
	typeInjectKeycode = 0
	typeInjectText    = 1
	typeInjectTouch   = 2
	typeResetVideo    = 17
)

// Key and touch actions, Android's KeyEvent and MotionEvent ones.
const (
	ActionDown = 0
	ActionUp   = 1
	ActionMove = 2
)

// PointerFinger is scrcpy's generic finger: a touch, never a mouse.
const PointerFinger = int64(-2)

// MaxTextBytes is the most UTF-8 one text message carries; the server refuses a longer one.
const MaxTextBytes = 300

// KeyMessage injects one key action.
func KeyMessage(action byte, keycode int32) []byte {
	message := make([]byte, 14)
	message[0] = typeInjectKeycode
	message[1] = action
	binary.BigEndian.PutUint32(message[2:], uint32(keycode))

	return message
}

// TextMessages splits text into messages of at most MaxTextBytes each, never inside a character.
func TextMessages(text string) [][]byte {
	var messages [][]byte
	for text != "" {
		end := min(len(text), MaxTextBytes)
		for end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		message := make([]byte, 5+end)
		message[0] = typeInjectText
		binary.BigEndian.PutUint32(message[1:], uint32(end))
		copy(message[5:], text[:end])
		messages = append(messages, message)
		text = text[end:]
	}

	return messages
}

// Touch is one finger event at a pixel of a video of Width×Height, which must be the current video's size.
type Touch struct {
	Action  byte
	Pointer int64
	X, Y    int32
	Width   uint16
	Height  uint16
}

// Message encodes the touch, full pressure unless the finger lifts.
func (t Touch) Message() []byte {
	message := make([]byte, 32)
	message[0] = typeInjectTouch
	message[1] = t.Action
	binary.BigEndian.PutUint64(message[2:], uint64(t.Pointer))
	binary.BigEndian.PutUint32(message[10:], uint32(t.X))
	binary.BigEndian.PutUint32(message[14:], uint32(t.Y))
	binary.BigEndian.PutUint16(message[18:], t.Width)
	binary.BigEndian.PutUint16(message[20:], t.Height)
	if t.Action != ActionUp {
		binary.BigEndian.PutUint16(message[22:], 0xffff)
	}

	return message
}

// ResetVideoMessage asks the server to restart the encoder: a new config packet and key frame follow.
func ResetVideoMessage() []byte { return []byte{typeResetVideo} }

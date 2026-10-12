package scrcpy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	codecH264      = 0x68323634
	isConfigBit    = uint64(1) << 63
	isKeyFrameBit  = uint64(1) << 62
	ptsMask        = isKeyFrameBit - 1
	maxPacketBytes = 16 << 20
)

// Meta is what the video socket sends once, before its first packet.
type Meta struct {
	Width  int
	Height int
}

// Packet is one Annex B packet: a config packet carries SPS and PPS, any other a frame.
type Packet struct {
	IsConfig   bool
	IsKeyFrame bool
	// PTS is in microseconds; a config packet has none.
	PTS  uint64
	Data []byte
}

// ReadMeta reads the 12-byte codec metadata: codec id, width and height, big-endian.
func ReadMeta(r io.Reader) (Meta, error) {
	var header [12]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Meta{}, err
	}
	if codec := binary.BigEndian.Uint32(header[0:]); codec != codecH264 {
		return Meta{}, fmt.Errorf("the scrcpy server streams codec %#08x, not H.264", codec)
	}

	return Meta{Width: int(binary.BigEndian.Uint32(header[4:])), Height: int(binary.BigEndian.Uint32(header[8:]))}, nil
}

// ReadPacket reads one packet behind its 12-byte header: flags and PTS in a u64, then the size in a u32.
func ReadPacket(r io.Reader) (Packet, error) {
	var header [12]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Packet{}, err
	}
	flags := binary.BigEndian.Uint64(header[0:])
	size := binary.BigEndian.Uint32(header[8:])
	if size == 0 || size > maxPacketBytes {
		return Packet{}, fmt.Errorf("the scrcpy server sent a packet of %d bytes", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return Packet{}, err
	}
	packet := Packet{IsConfig: flags&isConfigBit != 0, IsKeyFrame: flags&isKeyFrameBit != 0, Data: data}
	if !packet.IsConfig {
		packet.PTS = flags & ptsMask
	}

	return packet, nil
}

package scrcpy

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
)

const nalTypeSPS = 7

// SPS is what the bridge reads from an H.264 sequence parameter set.
type SPS struct {
	Width  int
	Height int
	// Codec is the WebCodecs codec string, avc1.PPCCLL from the profile, constraint flags and level.
	Codec string
}

var errShortSPS = errors.New("the SPS ends early")

// highProfiles carry chroma format, bit depths and scaling lists before the size.
var highProfiles = []byte{100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135}

// ParseConfig finds the SPS in an Annex B config packet and reads the picture size it gives, cropping applied.
func ParseConfig(annexB []byte) (SPS, error) {
	for _, unit := range nalUnits(annexB) {
		if len(unit) > 0 && unit[0]&0x1f == nalTypeSPS {
			return parseSPS(unit)
		}
	}

	return SPS{}, errors.New("the config packet carries no SPS")
}

// nalUnits splits Annex B data at its 3- or 4-byte start codes.
func nalUnits(data []byte) [][]byte {
	var units [][]byte
	start := -1
	for i := 0; i+2 < len(data); i++ {
		if data[i] != 0 || data[i+1] != 0 || data[i+2] != 1 {
			continue
		}
		if start >= 0 {
			units = append(units, bytes.TrimRight(data[start:i], "\x00"))
		}
		start = i + 3
		i += 2
	}
	if start >= 0 && start <= len(data) {
		units = append(units, data[start:])
	}

	return units
}

func parseSPS(unit []byte) (sps SPS, err error) {
	if len(unit) < 4 {
		return SPS{}, errShortSPS
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if recovered != errShortSPS {
				panic(recovered)
			}
			sps, err = SPS{}, errShortSPS
		}
	}()
	profile := unit[1]
	sps.Codec = fmt.Sprintf("avc1.%02x%02x%02x", unit[1], unit[2], unit[3])
	r := &bitReader{data: unescape(unit[4:])}
	r.ue() // seq_parameter_set_id
	chromaFormat := uint(1)
	isSeparateColourPlane := false
	if slices.Contains(highProfiles, profile) {
		chromaFormat = r.ue()
		if chromaFormat == 3 {
			isSeparateColourPlane = r.bit() == 1
		}
		r.ue()  // bit_depth_luma_minus8
		r.ue()  // bit_depth_chroma_minus8
		r.bit() // qpprime_y_zero_transform_bypass_flag
		if r.bit() == 1 {
			lists := 8
			if chromaFormat == 3 {
				lists = 12
			}
			for i := range lists {
				if r.bit() == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					r.skipScalingList(size)
				}
			}
		}
	}
	r.ue() // log2_max_frame_num_minus4
	switch r.ue() {
	case 0:
		r.ue() // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		r.bit() // delta_pic_order_always_zero_flag
		r.se()  // offset_for_non_ref_pic
		r.se()  // offset_for_top_to_bottom_field
		for range r.ue() {
			r.se() // offset_for_ref_frame
		}
	}
	r.ue()  // max_num_ref_frames
	r.bit() // gaps_in_frame_num_value_allowed_flag
	widthInMacroblocks := r.ue() + 1
	heightInMapUnits := r.ue() + 1
	isFrameOnly := r.bit() == 1
	if !isFrameOnly {
		r.bit() // mb_adaptive_frame_field_flag
	}
	r.bit() // direct_8x8_inference_flag
	fieldFactor := uint(1)
	if !isFrameOnly {
		fieldFactor = 2
	}
	width := widthInMacroblocks * 16
	height := fieldFactor * heightInMapUnits * 16
	if r.bit() == 1 {
		left, right, top, bottom := r.ue(), r.ue(), r.ue(), r.ue()
		cropX, cropY := uint(1), fieldFactor
		if !isSeparateColourPlane && chromaFormat != 0 {
			subWidth, subHeight := uint(2), uint(1)
			if chromaFormat == 3 {
				subWidth = 1
			}
			if chromaFormat == 1 {
				subHeight = 2
			}
			cropX, cropY = subWidth, subHeight*fieldFactor
		}
		width -= (left + right) * cropX
		height -= (top + bottom) * cropY
	}
	sps.Width, sps.Height = int(width), int(height)
	if sps.Width <= 0 || sps.Height <= 0 {
		return SPS{}, fmt.Errorf("the SPS gives a %d×%d picture", sps.Width, sps.Height)
	}

	return sps, nil
}

// unescape drops the emulation prevention byte from each 00 00 03.
func unescape(data []byte) []byte {
	out := make([]byte, 0, len(data))
	zeros := 0
	for _, b := range data {
		if zeros >= 2 && b == 3 {
			zeros = 0
			continue
		}
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, b)
	}

	return out
}

// bitReader reads the SPS's bits and exp-Golomb codes; reading past the end panics with errShortSPS.
type bitReader struct {
	data []byte
	at   int
}

func (r *bitReader) bit() uint {
	if r.at >= 8*len(r.data) {
		panic(errShortSPS)
	}
	value := uint(r.data[r.at/8]>>(7-r.at%8)) & 1
	r.at++

	return value
}

func (r *bitReader) ue() uint {
	zeros := 0
	for r.bit() == 0 {
		zeros++
		if zeros > 31 {
			panic(errShortSPS)
		}
	}
	value := uint(1)
	for range zeros {
		value = value<<1 | r.bit()
	}

	return value - 1
}

func (r *bitReader) se() int {
	code := r.ue()
	if code%2 == 1 {
		return int(code+1) / 2
	}

	return -int(code / 2)
}

func (r *bitReader) skipScalingList(size int) {
	last, next := 8, 8
	for range size {
		if next != 0 {
			next = (last + r.se() + 256) % 256
		}
		if next != 0 {
			last = next
		}
	}
}

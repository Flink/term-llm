package wire

import (
	"errors"
	"unicode/utf16"
	"unicode/utf8"
)

// UTF16OffsetToByteOffset translates a browser caret, rejecting split code points.
func UTF16OffsetToByteOffset(value string, offset int) (int, error) {
	if offset < 0 {
		return 0, errors.New("cursor_utf16 is out of range")
	}
	units := 0
	for byteOffset, r := range value {
		if units == offset {
			return byteOffset, nil
		}
		width := 1
		if utf16.RuneLen(r) == 2 {
			width = 2
		}
		if offset > units && offset < units+width {
			return 0, errors.New("cursor_utf16 splits a surrogate pair")
		}
		units += width
	}
	if units == offset {
		return len(value), nil
	}
	return 0, errors.New("cursor_utf16 is out of range")
}

// ByteOffsetToUTF16Offset translates a UTF-8 byte offset for a browser.
func ByteOffsetToUTF16Offset(value string, offset int) (int, error) {
	if offset < 0 || offset > len(value) || !utf8.ValidString(value) {
		return 0, errors.New("byte offset is out of range")
	}
	units := 0
	for byteOffset, r := range value {
		if byteOffset == offset {
			return units, nil
		}
		if byteOffset > offset {
			return 0, errors.New("byte offset splits a UTF-8 sequence")
		}
		if utf16.RuneLen(r) == 2 {
			units += 2
		} else {
			units++
		}
	}
	if offset == len(value) {
		return units, nil
	}
	return 0, errors.New("byte offset is out of range")
}

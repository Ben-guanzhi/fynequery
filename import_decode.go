package main

// Import-file decoding: the bytes a CSV/JSON import receives are transparently
// decoded from the common non-UTF-8 encodings before parsing, so a
// Chinese-language export never arrives as mojibake. Ported from
// TableProGo/internal/importfile (the same algorithm, adapted to this client's
// read-the-whole-file shape): a BOM picks UTF-8 (stripped) or UTF-16; without
// a BOM the leading bytes are probed, and bytes that are not valid UTF-8 fall
// back to GB18030 (a superset of GBK and GB2312, the usual encoding of
// Chinese-language exports). A BOM-less UTF-16 file is left alone: guessing it
// from NUL bytes alone has too many false positives.

import (
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// looksLikeUTF16NoBOM reports whether data looks like UTF-16 without a BOM.
// UTF-16 ASCII text has NUL at every other byte position: UTF-16LE puts the
// ASCII char in the low byte (NUL at odd indices), UTF-16BE puts it in the
// high byte (NUL at even indices). If more than 70% of the pairs have a NUL
// on one side, the file is likely UTF-16.
func looksLikeUTF16NoBOM(data []byte) (le bool, ok bool) {
	if len(data) < 8 {
		return false, false
	}
	evenNULs, oddNULs := 0, 0
	pairs := len(data) / 2
	for i := 0; i < len(data)-1; i += 2 {
		if data[i] == 0 {
			evenNULs++
		}
		if data[i+1] == 0 {
			oddNULs++
		}
	}
	if oddNULs > pairs*7/10 {
		return true, true // UTF-16LE: NULs at odd positions
	}
	if evenNULs > pairs*7/10 {
		return false, true // UTF-16BE: NULs at even positions
	}
	return false, false
}

// decodeImportBytes decodes an import file's bytes to UTF-8 text. The sniff
// covers the whole buffer (the import already holds the file in memory), and a
// multi-byte rune split by a chunk boundary is not a concern here — the buffer
// is the whole file.
func decodeImportBytes(data []byte) []byte {
	switch {
	case len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF:
		return data[3:] // UTF-8 BOM: stripped, the rest is already UTF-8
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE:
		out, _, err := transform.Bytes(unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder(), data[2:])
		if err != nil {
			return data // undecodable: hand the bytes through, parse errors surface downstream
		}
		return out
	case len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF:
		out, _, err := transform.Bytes(unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder(), data[2:])
		if err != nil {
			return data
		}
		return out
	}
	// A UTF-16 file without a BOM passes utf8.Valid (NUL is valid UTF-8), so
	// it would come back as NUL-separated lines. Detect it by NUL density:
	// UTF-16 ASCII text has NUL at every other byte position.
	if le, ok := looksLikeUTF16NoBOM(data); ok {
		decoder := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)
		if !le {
			decoder = unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM)
		}
		out, _, err := transform.Bytes(decoder.NewDecoder(), data)
		if err != nil {
			return data
		}
		return out
	}
	if utf8.Valid(data) {
		return data
	}
	// A multi-byte rune truncated at the very end of the buffer would fail
	// utf8.Valid on a genuine UTF-8 file; drop up to three trailing bytes
	// before concluding GB18030. When the head validates, the truncated tail
	// bytes are dropped too — there is no stream behind them to continue.
	head := data
	if !utf8.Valid(head) {
		for k := 1; k <= 3 && k < len(head); k++ {
			if utf8.Valid(head[:len(head)-k]) {
				head = head[:len(head)-k]
				break
			}
		}
	}
	if utf8.Valid(head) {
		return head
	}
	out, _, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), data)
	if err != nil {
		return data
	}
	return out
}

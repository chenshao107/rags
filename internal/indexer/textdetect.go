package indexer

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const textSampleSize = 8192

// maxNonTextRatio is the allowed share of suspicious ASCII control bytes in a sample.
const maxNonTextRatio = 0.05

// knownBinaryExt skips content sniffing for obviously binary extensions.
var knownBinaryExt = map[string]bool{
	".7z": true, ".a": true, ".apk": true, ".avi": true, ".bin": true, ".bmp": true,
	".bz2": true, ".class": true, ".deb": true, ".dll": true, ".dmg": true,
	".doc": true, ".docx": true, ".elf": true, ".exe": true, ".gif": true, ".gz": true,
	".ico": true, ".img": true, ".iso": true, ".jar": true, ".jpeg": true, ".jpg": true,
	".lz": true, ".mkv": true, ".mov": true, ".mp3": true, ".mp4": true,
	".o": true, ".obj": true, ".ogg": true, ".pdb": true, ".pdf": true,
	".png": true, ".ppt": true, ".pptx": true, ".pyc": true, ".pyo": true, ".rar": true,
	".rpm": true, ".so": true, ".tar": true, ".tgz": true, ".wav": true,
	".webm": true, ".webp": true, ".woff": true, ".woff2": true, ".xls": true, ".xlsx": true,
	".xz": true, ".zip": true, ".z": true, ".zlib": true,
}

// shouldIndexFile decides whether a file should be indexed using extension hints and content sniffing.
func shouldIndexFile(path string, info os.FileInfo) bool {
	if info.Size() == 0 {
		return false
	}

	ext := strings.ToLower(filepath.Ext(info.Name()))
	if knownBinaryExt[ext] {
		return false
	}

	return looksLikeTextFile(path, info.Size())
}

// looksLikeTextFile reads a prefix of the file and checks for binary markers.
func looksLikeTextFile(path string, size int64) bool {
	sample, err := readFilePrefix(path, size, textSampleSize)
	if err != nil || len(sample) == 0 {
		return false
	}
	return isTextContent(sample)
}

func readFilePrefix(path string, size int64, max int) ([]byte, error) {
	n := max
	if size < int64(n) {
		n = int(size)
	}
	if n <= 0 {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, n)
	read, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}
	return buf[:read], nil
}

// isTextContent reports whether a byte sample looks like text (UTF-8 or ASCII).
func isTextContent(data []byte) bool {
	// NUL bytes are a strong binary signal.
	for _, b := range data {
		if b == 0 {
			return false
		}
	}

	// A prefix sample may end in the middle of a multi-byte rune. If the
	// sample fails UTF-8 validation, accept it only when trimming a valid
	// partial rune prefix from the tail (<= 3 bytes) makes the rest valid.
	if !utf8.Valid(data) {
		ok := false
		n := len(data)
		for cut := 0; cut <= 3 && n-cut > 0; cut++ {
			head := data[:n-cut]
			if utf8.Valid(head) {
				ok = isPartialRunePrefix(data[n-cut:])
				break
			}
		}
		if !ok {
			return false
		}
	}

	var suspicious int
	for _, b := range data {
		if isAllowedTextByte(b) {
			continue
		}
		suspicious++
	}

	return float64(suspicious)/float64(len(data)) <= maxNonTextRatio
}

// isPartialRunePrefix reports whether b is the leading bytes of a multi-byte
// rune cut short by sampling (e.g. 0xE4 0xB8 for a truncated CJK character).
func isPartialRunePrefix(b []byte) bool {
	if len(b) == 0 || len(b) > 3 {
		return false
	}
	if b[0] < 0xC0 || b[0] > 0xF4 {
		return false
	}
	for i := 1; i < len(b); i++ {
		if b[i]&0xC0 != 0x80 {
			return false
		}
	}
	need := 2
	if b[0] >= 0xE0 {
		need = 3
	}
	if b[0] >= 0xF0 {
		need = 4
	}
	return need > len(b)
}

func isAllowedTextByte(b byte) bool {
	if b == '\n' || b == '\r' || b == '\t' {
		return true
	}
	if b >= 0x20 && b != 0x7f {
		return true
	}
	return false
}

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

	if !utf8.Valid(data) {
		return false
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

func isAllowedTextByte(b byte) bool {
	if b == '\n' || b == '\r' || b == '\t' {
		return true
	}
	if b >= 0x20 && b != 0x7f {
		return true
	}
	return false
}

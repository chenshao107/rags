package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsTextContent(t *testing.T) {
	if !isTextContent([]byte("hello world\n中文测试")) {
		t.Fatal("expected UTF-8 text")
	}
	if isTextContent([]byte{0x00, 0x01, 0x02}) {
		t.Fatal("NUL should be binary")
	}
	if isTextContent([]byte{0xff, 0xfe, 0xfd}) {
		t.Fatal("invalid UTF-8 should be binary")
	}
}

func TestShouldIndexFileByContent(t *testing.T) {
	dir := t.TempDir()

	textPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(textPath, []byte("# hello\nOTA 升级\n"), 0644); err != nil {
		t.Fatal(err)
	}
	binaryAsText := filepath.Join(dir, "fake.txt")
	if err := os.WriteFile(binaryAsText, []byte{0x00, 0x01, 0x02, 0x03, 'a'}, 0644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(dir, "firmware.bin")
	if err := os.WriteFile(binPath, []byte{0x7f, 0x45, 0x4c, 0x46}, 0644); err != nil {
		t.Fatal(err)
	}
	noExtText := filepath.Join(dir, "readme")
	if err := os.WriteFile(noExtText, []byte("plain log line\n"), 0644); err != nil {
		t.Fatal(err)
	}

	textInfo, _ := os.Stat(textPath)
	if !shouldIndexFile(textPath, textInfo) {
		t.Fatal("expected real text file")
	}
	fakeInfo, _ := os.Stat(binaryAsText)
	if shouldIndexFile(binaryAsText, fakeInfo) {
		t.Fatal("expected binary with .txt extension to be skipped")
	}
	binInfo, _ := os.Stat(binPath)
	if shouldIndexFile(binPath, binInfo) {
		t.Fatal("expected .bin to be skipped")
	}
	noExtInfo, _ := os.Stat(noExtText)
	if !shouldIndexFile(noExtText, noExtInfo) {
		t.Fatal("expected extensionless text file")
	}
}

package indexer

import (
	"crypto/sha256"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenshao/rags/internal/config"
	"github.com/chenshao/rags/internal/embedder"
	"github.com/chenshao/rags/internal/vectordb"
)

// Indexer handles file scanning, chunking, embedding, and upserting
type Indexer struct {
	VDB      *vectordb.Client
	Embedder *embedder.Client
}

// FileInfo holds metadata about a scanned file
type FileInfo struct {
	Path    string
	RelPath string
	Hash    string
	Size    int64
}

// Chunk represents a text chunk with position metadata
type Chunk struct {
	Text      string
	StartLine int
	EndLine   int
	Index     int // chunk index within the file
}

// New creates an Indexer
func New(vdb *vectordb.Client, emb *embedder.Client) *Indexer {
	return &Indexer{VDB: vdb, Embedder: emb}
}

// ScanFiles walks the configured paths and returns all matching files
func (idx *Indexer) ScanFiles(root string, cfg config.ProjectConfig) ([]FileInfo, error) {
	var files []FileInfo
	excludeSet := makeExcludeSet(cfg.Exclude)

	for _, p := range cfg.Paths {
		scanPath := filepath.Join(root, p)

		err := filepath.Walk(scanPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip files we can't read
			}
			if info.IsDir() {
				if excludeSet[info.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if !shouldIndexFile(path, info) {
				return nil
			}
			if excludeSet[info.Name()] {
				return nil
			}

			relPath, _ := filepath.Rel(root, path)
			hash, err := fileHash(path)
			if err != nil {
				return nil
			}

			files = append(files, FileInfo{
				Path:    path,
				RelPath: relPath,
				Hash:    hash,
				Size:    info.Size(),
			})
			return nil
		})

		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", p, err)
		}
	}

	return files, nil
}

// ComputeDiff compares scanned files with stored state and returns actions
func ComputeDiff(scanned []FileInfo, state config.ProjectState) (newFiles, modifiedFiles []FileInfo, deletedFiles []string) {
	scannedMap := make(map[string]string, len(scanned))
	for _, f := range scanned {
		scannedMap[f.RelPath] = f.Hash
	}

	// Find new and modified files
	for _, f := range scanned {
		oldHash, exists := state.Files[f.RelPath]
		if !exists {
			newFiles = append(newFiles, f)
		} else if oldHash != f.Hash {
			modifiedFiles = append(modifiedFiles, f)
		}
	}

	// Find deleted files (in state but not in scanned)
	for path := range state.Files {
		if _, exists := scannedMap[path]; !exists {
			deletedFiles = append(deletedFiles, path)
		}
	}

	return
}

// IndexFiles chunks and indexes a list of files
func (idx *Indexer) IndexFiles(root string, collection string, projectName string, files []FileInfo, chunkSize, overlap int) error {
	total := len(files)
	for i, f := range files {
		fmt.Printf("  [%d/%d] %s\n", i+1, total, f.RelPath)

		chunks, err := ChunkFile(f.Path, f.RelPath, chunkSize, overlap)
		if err != nil {
			return fmt.Errorf("chunk %s: %w", f.RelPath, err)
		}
		if len(chunks) == 0 {
			continue
		}

		if err := idx.indexChunks(collection, projectName, f, chunks); err != nil {
			return err
		}
	}
	return nil
}

func (idx *Indexer) indexChunks(collection, projectName string, f FileInfo, chunks []Chunk) error {
	batchSize := 32
	for i := 0; i < len(chunks); i += batchSize {
		end := min(i+batchSize, len(chunks))
		batch := chunks[i:end]

		texts := make([]string, len(batch))
		for j, c := range batch {
			t := strings.TrimSpace(c.Text)
			if t == "" {
				t = " "
			}
			texts[j] = t
		}

		vectors, err := idx.Embedder.EmbedBatch(texts)
		if err != nil {
			return fmt.Errorf("embed %s batch [%d:%d]: %w", f.RelPath, i, end, err)
		}

		points := make([]vectordb.Point, len(batch))
		for j, vec := range vectors {
			c := batch[j]
			chunkID := chunkUUID(f.RelPath, c.Index)
			points[j] = vectordb.Point{
				ID:     chunkID,
				Vector: vec,
				Payload: map[string]any{
					"project":     projectName,
					"file_path":   f.RelPath,
					"file_hash":   f.Hash,
					"start_line":  c.StartLine,
					"end_line":    c.EndLine,
					"chunk_index": c.Index,
					"text":        c.Text,
				},
			}
		}

		if err := idx.VDB.Upsert(collection, points); err != nil {
			return fmt.Errorf("upsert %s batch [%d:%d]: %w", f.RelPath, i, end, err)
		}
	}
	return nil
}

// DeleteFiles removes chunks for a list of file paths from the vector DB
func (idx *Indexer) DeleteFiles(collection string, files []string) error {
	for _, fp := range files {
		fmt.Printf("  remove %s\n", fp)
		if err := idx.VDB.DeleteByFilePath(collection, fp); err != nil {
			return fmt.Errorf("delete %s: %w", fp, err)
		}
	}
	return nil
}

// UpdateState merges the scan results into the state
func UpdateState(state config.ProjectState, scanned []FileInfo, deleted []string) config.ProjectState {
	newState := config.ProjectState{Files: make(map[string]string)}
	// Add/update scanned files
	for _, f := range scanned {
		newState.Files[f.RelPath] = f.Hash
	}
	// deleted files are simply not added
	_ = deleted
	return newState
}

// chunkPara is a paragraph with source line numbers for chunking
type chunkPara struct {
	text      string
	startLine int
	endLine   int
}

// ChunkFile splits a file into overlapping chunks by paragraphs
func ChunkFile(absPath, relPath string, chunkSize, overlap int) ([]Chunk, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}

	text := string(data)
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}

	lines := strings.Split(text, "\n")

	// Build line-numbered paragraphs
	var paragraphs []chunkPara
	currentPara := strings.Builder{}
	paraStart := 1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		lineNum := i + 1

		if trimmed == "" {
			// Empty line = paragraph boundary
			if currentPara.Len() > 0 {
				paragraphs = append(paragraphs, chunkPara{
					text:      strings.TrimSpace(currentPara.String()),
					startLine: paraStart,
					endLine:   lineNum - 1,
				})
				currentPara.Reset()
			}
			// Skip setting paraStart here — we'll set it when we start the next paragraph
		} else {
			if currentPara.Len() == 0 {
				paraStart = lineNum
			} else {
				currentPara.WriteString(" ")
			}
			currentPara.WriteString(trimmed)
		}
	}

	// Don't forget the last paragraph
	if currentPara.Len() > 0 {
		paragraphs = append(paragraphs, chunkPara{
			text:      strings.TrimSpace(currentPara.String()),
			startLine: paraStart,
			endLine:   len(lines),
		})
	}

	if len(paragraphs) == 0 {
		return nil, nil
	}

	paragraphs = splitLongParagraphs(paragraphs, chunkSize)

	// Merge paragraphs into chunks of approximately chunkSize characters
	var chunks []Chunk
	currentChunk := strings.Builder{}
	chunkStart := paragraphs[0].startLine
	chunkEnd := paragraphs[0].endLine
	chunkIdx := 0

	for _, p := range paragraphs {
		if currentChunk.Len() > 0 && currentChunk.Len()+len(p.text) > chunkSize {
			// Finalize current chunk
			chunks = append(chunks, Chunk{
				Text:      strings.TrimSpace(currentChunk.String()),
				StartLine: chunkStart,
				EndLine:   chunkEnd,
				Index:     chunkIdx,
			})
			chunkIdx++

			// Start new chunk with overlap: keep last `overlap` runes
			prev := currentChunk.String()
			if len(prev) > overlap {
				prev = safeLastRunes(prev, overlap)
			}
			currentChunk.Reset()
			if prev != "" {
				currentChunk.WriteString(prev)
				currentChunk.WriteString(" ")
			}
			chunkStart = p.startLine
		} else if currentChunk.Len() > 0 {
			currentChunk.WriteString("\n\n")
		}
		currentChunk.WriteString(p.text)
		chunkEnd = p.endLine
	}

	// Final chunk
	if currentChunk.Len() > 0 {
		chunks = append(chunks, Chunk{
			Text:      strings.TrimSpace(currentChunk.String()),
			StartLine: chunkStart,
			EndLine:   chunkEnd,
			Index:     chunkIdx,
		})
	}

	return chunks, nil
}

// splitLongParagraphs breaks paragraphs longer than chunkSize into smaller pieces
func splitLongParagraphs(paragraphs []chunkPara, chunkSize int) []chunkPara {
	var out []chunkPara
	for _, p := range paragraphs {
		runes := []rune(p.text)
		if len(runes) <= chunkSize {
			out = append(out, p)
			continue
		}
		for i := 0; i < len(runes); i += chunkSize {
			end := min(i+chunkSize, len(runes))
			out = append(out, chunkPara{
				text:      string(runes[i:end]),
				startLine: p.startLine,
				endLine:   p.endLine,
			})
		}
	}
	return out
}

// --- helpers ---

func makeExcludeSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	// Always exclude .rags
	set[".rags"] = true
	return set
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

var _ hash.Hash = sha256.New() // ensure import used

// safeLastRunes returns the last n runes of s without breaking UTF-8 boundaries
func safeLastRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[len(runes)-n:])
}

// chunkUUID generates a deterministic UUID from file path and chunk index
func chunkUUID(filePath string, chunkIndex int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", filePath, chunkIndex)))
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

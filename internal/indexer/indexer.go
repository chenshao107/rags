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
			if !isTextFile(info.Name()) {
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
	var allChunks []Chunk
	var metaList []chunkMeta // tracks which file each chunk belongs to

	for _, f := range files {
		chunks, err := ChunkFile(f.Path, f.RelPath, chunkSize, overlap)
		if err != nil {
			return fmt.Errorf("chunk %s: %w", f.RelPath, err)
		}
		for _, c := range chunks {
			allChunks = append(allChunks, c)
			metaList = append(metaList, chunkMeta{
				RelPath:    f.RelPath,
				FileHash:   f.Hash,
				ChunkIndex: c.Index,
				StartLine:  c.StartLine,
				EndLine:    c.EndLine,
			})
		}
	}

	if len(allChunks) == 0 {
		return nil
	}

	// Embed in batches of 32
	batchSize := 32
	for i := 0; i < len(allChunks); i += batchSize {
		end := min(i+batchSize, len(allChunks))
		batch := allChunks[i:end]
		meta := metaList[i:end]

		texts := make([]string, len(batch))
		for j, c := range batch {
			texts[j] = c.Text
		}

		vectors, err := idx.Embedder.EmbedBatch(texts)
		if err != nil {
			return fmt.Errorf("embed batch [%d:%d]: %w", i, end, err)
		}

		points := make([]vectordb.Point, len(batch))
		for j, vec := range vectors {
			m := meta[j]
			chunkID := chunkUUID(m.RelPath, m.ChunkIndex)
			points[j] = vectordb.Point{
				ID:     chunkID,
				Vector: vec,
				Payload: map[string]any{
					"project":    projectName,
					"file_path":  m.RelPath,
					"file_hash":  m.FileHash,
					"start_line": m.StartLine,
					"end_line":   m.EndLine,
					"chunk_index": m.ChunkIndex,
					"text":       batch[j].Text,
				},
			}
		}

		if err := idx.VDB.Upsert(collection, points); err != nil {
			return fmt.Errorf("upsert batch [%d:%d]: %w", i, end, err)
		}
	}

	return nil
}

// DeleteFiles removes chunks for a list of file paths from the vector DB
func (idx *Indexer) DeleteFiles(collection string, files []string) error {
	for _, fp := range files {
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

// chunkMeta ties a chunk back to its source file
type chunkMeta struct {
	RelPath    string
	FileHash   string
	ChunkIndex int
	StartLine  int
	EndLine    int
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
	type para struct {
		text      string
		startLine int
		endLine   int
	}

	var paragraphs []para
	currentPara := strings.Builder{}
	paraStart := 1

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		lineNum := i + 1

		if trimmed == "" {
			// Empty line = paragraph boundary
			if currentPara.Len() > 0 {
				paragraphs = append(paragraphs, para{
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
		paragraphs = append(paragraphs, para{
			text:      strings.TrimSpace(currentPara.String()),
			startLine: paraStart,
			endLine:   len(lines),
		})
	}

	if len(paragraphs) == 0 {
		return nil, nil
	}

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

			// Start new chunk with overlap: keep last `overlap` chars
			prev := currentChunk.String()
			if len(prev) > overlap {
				prev = prev[len(prev)-overlap:]
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

// chunkUUID generates a deterministic UUID from file path and chunk index
func chunkUUID(filePath string, chunkIndex int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", filePath, chunkIndex)))
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// isTextFile checks if a file extension suggests it's a text file
func isTextFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	textExts := map[string]bool{
		".txt": true, ".md": true, ".markdown": true,
		".go": true, ".py": true, ".js": true, ".ts": true,
		".jsx": true, ".tsx": true, ".vue": true, ".svelte": true,
		".rs": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true,
		".java": true, ".kt": true, ".swift": true,
		".rb": true, ".php": true, ".pl": true, ".sh": true, ".bash": true,
		".yaml": true, ".yml": true, ".json": true, ".toml": true, ".ini": true,
		".xml": true, ".html": true, ".css": true, ".scss": true, ".less": true,
		".sql": true, ".graphql": true, ".proto": true,
		".tex": true, ".rst": true, ".org": true,
		".cfg": true, ".conf": true, ".env": true,
		".log": true,
	}
	if textExts[ext] {
		return true
	}
	// No extension = likely text (Makefile, Dockerfile, etc.)
	if ext == "" || ext == name {
		return true
	}
	return false
}

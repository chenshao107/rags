package main

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenshao/rags/internal/config"
	"github.com/chenshao/rags/internal/embedder"
	"github.com/chenshao/rags/internal/indexer"
	"github.com/chenshao/rags/internal/project"
	"github.com/chenshao/rags/internal/query"
	"github.com/chenshao/rags/internal/reranker"
	"github.com/chenshao/rags/internal/vectordb"

	"github.com/spf13/cobra"
)

// bge-m3 vector dimension
const bgeM3VectorSize = 1024

func main() {
	root := &cobra.Command{
		Use:   "rags",
		Short: "Local Semantic Index System",
		Long: `rags is a local semantic index system that enables semantic search
over your project files, powered by vector embeddings and Qdrant.`,
	}

	root.AddCommand(initCmd())
	root.AddCommand(updateCmd())
	root.AddCommand(grepCmd())
	root.AddCommand(filesCmd())
	root.AddCommand(configCmd())
	root.AddCommand(projectsCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize rags in the current directory",
		Long: `Creates .rags/config.yaml, registers the project, initializes a Qdrant
collection, and performs the first full index.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get current directory: %w", err)
			}

			// Check if already initialized
			if config.HasRagsProject(cwd) {
				return fmt.Errorf("rags project already exists in %s", cwd)
			}

			// Load or create global settings
			settings, err := config.LoadSettings()
			if err != nil {
				return fmt.Errorf("load settings: %w", err)
			}

			if settings.Embedding.APIKey == "" {
				settings = promptSettings(settings)
				if err := config.SaveSettings(settings); err != nil {
					return fmt.Errorf("save settings: %w", err)
				}
			}

			// Determine project name from directory
			projectName := filepath.Base(cwd)
			collectionName := "rags_" + sanitizeName(projectName)

			// Create Qdrant collection
			vdb := vectordb.NewClient(settings.Qdrant.URL)
			if err := vdb.EnsureCollection(collectionName, bgeM3VectorSize); err != nil {
				return fmt.Errorf("create collection: %w", err)
			}

			// Write project config
			pcfg := config.DefaultProjectConfig(projectName)
			if err := config.SaveProjectConfig(cwd, pcfg); err != nil {
				return fmt.Errorf("save project config: %w", err)
			}

			// Register project
			absCwd, _ := filepath.Abs(cwd)
			if err := config.RegisterProject(config.ProjectEntry{
				Name:       projectName,
				Path:       absCwd,
				Collection: collectionName,
			}); err != nil {
				return fmt.Errorf("register project: %w", err)
			}

			fmt.Printf("✓ Project '%s' initialized\n", projectName)
			fmt.Printf("  Collection: %s\n", collectionName)

			// Full index
			fmt.Println("Indexing files...")
			if err := runFullIndex(cwd, pcfg, collectionName, &settings, vdb); err != nil {
				return fmt.Errorf("initial index: %w", err)
			}

			return nil
		},
	}
}

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Incrementally update the index",
		Long: `Scans for new, modified, and deleted files, updates the vector index
accordingly, and runs cross-project garbage collection.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get current directory: %w", err)
			}

			// Load project config
			pcfg, err := config.LoadProjectConfig(cwd)
			if err != nil {
				return fmt.Errorf("not a rags project (run 'rags init' first): %w", err)
			}

			// Load global settings
			settings, err := config.LoadSettings()
			if err != nil {
				return fmt.Errorf("load settings: %w", err)
			}

			if settings.Embedding.APIKey == "" {
				return fmt.Errorf("no API key configured. Run 'rags init' first or edit ~/.rags/settings.yaml")
			}

			collectionName := "rags_" + sanitizeName(pcfg.Name)

			// Load current state
			state, err := config.LoadProjectState(cwd)
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}

			// Create clients
			vdb := vectordb.NewClient(settings.Qdrant.URL)
			emb := embedder.NewClient(settings.Embedding.BaseURL, settings.Embedding.APIKey, settings.Embedding.Model)
			idx := indexer.New(vdb, emb)

			// Scan files
			fmt.Println("Scanning files...")
			files, err := idx.ScanFiles(cwd, pcfg)
			if err != nil {
				return fmt.Errorf("scan: %w", err)
			}

			// Compute diff
			newFiles, modifiedFiles, deletedFiles := indexer.ComputeDiff(files, state)

			fmt.Printf("  %d new, %d modified, %d removed\n", len(newFiles), len(modifiedFiles), len(deletedFiles))

			// Handle deleted files
			if len(deletedFiles) > 0 {
				fmt.Printf("Removing %d files...\n", len(deletedFiles))
				if err := idx.DeleteFiles(collectionName, deletedFiles); err != nil {
					return fmt.Errorf("delete files: %w", err)
				}
			}

			// Index new files
			toIndex := append(newFiles, modifiedFiles...)
			if len(toIndex) > 0 {
				fmt.Printf("Indexing %d files...\n", len(toIndex))
				if err := idx.IndexFiles(cwd, collectionName, pcfg.Name, toIndex, pcfg.Index.ChunkSize, pcfg.Index.Overlap); err != nil {
					return fmt.Errorf("index files: %w", err)
				}
			}

			// Update state
			newState := indexer.UpdateState(state, files, deletedFiles)
			if err := config.SaveProjectState(cwd, newState); err != nil {
				return fmt.Errorf("save state: %w", err)
			}

			// Cross-project GC
			fmt.Println("Running cross-project GC...")
			cleaned, err := project.GarbageCollect(vdb)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: GC error: %v\n", err)
			}
			for _, name := range cleaned {
				fmt.Printf("  Cleaned orphan project '%s'\n", name)
			}

			fmt.Println("✓ Update complete")
			return nil
		},
	}
}

func grepCmd() *cobra.Command {
	var topK int

	cmd := &cobra.Command{
		Use:   "grep <query>",
		Short: "Semantic search over indexed files",
		Long:  `Embeds the query, searches the vector index, optionally re-ranks, and returns top results.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			queryStr := args[0]

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get current directory: %w", err)
			}

			pcfg, err := config.LoadProjectConfig(cwd)
			if err != nil {
				return fmt.Errorf("not a rags project: %w", err)
			}

			settings, err := config.LoadSettings()
			if err != nil {
				return fmt.Errorf("load settings: %w", err)
			}

			if settings.Embedding.APIKey == "" {
				return fmt.Errorf("no API key configured")
			}

			collectionName := "rags_" + sanitizeName(pcfg.Name)

			vdb := vectordb.NewClient(settings.Qdrant.URL)
			emb := embedder.NewClient(settings.Embedding.BaseURL, settings.Embedding.APIKey, settings.Embedding.Model)

			var rerank *reranker.Client
			if settings.Rerank != nil && settings.Rerank.APIKey != "" {
				rerank = reranker.NewClient(settings.Rerank.BaseURL, settings.Rerank.APIKey, settings.Rerank.Model)
			}

			searcher := query.New(vdb, emb, rerank)
			results, err := searcher.Search(collectionName, pcfg.Name, queryStr, topK)
			if err != nil {
				return fmt.Errorf("search: %w", err)
			}

			if len(results) == 0 {
				fmt.Println("No results found.")
				return nil
			}

			fmt.Print(query.FormatResults(results))
			return nil
		},
	}

	cmd.Flags().IntVarP(&topK, "top", "n", 5, "Number of top results to return")
	return cmd
}

func filesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "files",
		Short: "List indexed files",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get current directory: %w", err)
			}

			state, err := config.LoadProjectState(cwd)
			if err != nil {
				return fmt.Errorf("not a rags project: %w", err)
			}

			if len(state.Files) == 0 {
				fmt.Println("No indexed files.")
				return nil
			}

			for path, hash := range state.Files {
				fmt.Printf("  %s  (%s)\n", path, hash[:8])
			}
			fmt.Printf("\nTotal: %d files\n", len(state.Files))
			return nil
		},
	}
}

func configCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get current directory: %w", err)
			}

			// Global settings
			settings, err := config.LoadSettings()
			if err != nil {
				return fmt.Errorf("load settings: %w", err)
			}

			fmt.Println("── Global (~/.rags/settings.yaml) ──")
			fmt.Printf("  Qdrant URL:       %s\n", settings.Qdrant.URL)
			fmt.Printf("  Embedding model:  %s\n", settings.Embedding.Model)
			fmt.Printf("  Embedding API:    %s\n", maskKey(settings.Embedding.APIKey))
			if settings.Rerank != nil {
				fmt.Printf("  Rerank model:     %s\n", settings.Rerank.Model)
				fmt.Printf("  Rerank API:       %s\n", maskKey(settings.Rerank.APIKey))
			} else {
				fmt.Println("  Rerank:           (disabled)")
			}
			fmt.Println()

			// Project config
			if config.HasRagsProject(cwd) {
				pcfg, err := config.LoadProjectConfig(cwd)
				if err == nil {
					fmt.Println("── Project (.rags/config.yaml) ──")
					fmt.Printf("  Name:       %s\n", pcfg.Name)
					fmt.Printf("  Paths:      %v\n", pcfg.Paths)
					fmt.Printf("  Exclude:    %v\n", pcfg.Exclude)
					fmt.Printf("  Chunk size: %d\n", pcfg.Index.ChunkSize)
					fmt.Printf("  Overlap:    %d\n", pcfg.Index.Overlap)
				}
			} else {
				fmt.Println("(no rags project in current directory)")
			}

			return nil
		},
	}
}

func projectsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "projects",
		Short: "List all registered projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := config.LoadRegistry()
			if err != nil {
				return fmt.Errorf("load registry: %w", err)
			}

			if len(registry.Projects) == 0 {
				fmt.Println("No registered projects.")
				return nil
			}

			for _, p := range registry.Projects {
				exists := "✓"
				if _, err := os.Stat(p.Path); os.IsNotExist(err) {
					exists = "✗ (missing)"
				}
				fmt.Printf("  %-20s  %s  %-30s  %s\n", p.Name, exists, p.Collection, p.Path)
			}
			fmt.Printf("\nTotal: %d projects\n", len(registry.Projects))
			return nil
		},
	}
}

// --- helpers ---

func runFullIndex(cwd string, pcfg config.ProjectConfig, collection string, settings *config.GlobalSettings, vdb *vectordb.Client) error {
	emb := embedder.NewClient(settings.Embedding.BaseURL, settings.Embedding.APIKey, settings.Embedding.Model)
	idx := indexer.New(vdb, emb)

	files, err := idx.ScanFiles(cwd, pcfg)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		fmt.Println("  No files found to index.")
		return nil
	}

	fmt.Printf("  Found %d files, indexing...\n", len(files))
	if err := idx.IndexFiles(cwd, collection, pcfg.Name, files, pcfg.Index.ChunkSize, pcfg.Index.Overlap); err != nil {
		return err
	}

	// Save initial state
	state := config.ProjectState{Files: make(map[string]string)}
	for _, f := range files {
		state.Files[f.RelPath] = f.Hash
	}
	if err := config.SaveProjectState(cwd, state); err != nil {
		return err
	}

	fmt.Printf("  Indexed %d files\n", len(files))
	return nil
}

func promptSettings(settings config.GlobalSettings) config.GlobalSettings {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("── First-time setup ──")

	// Qdrant URL
	fmt.Printf("Qdrant URL [%s]: ", settings.Qdrant.URL)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input != "" {
		settings.Qdrant.URL = input
	}

	// Embedding
	fmt.Printf("Embedding API base URL [%s]: ", settings.Embedding.BaseURL)
	input, _ = reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input != "" {
		settings.Embedding.BaseURL = input
	}

	fmt.Printf("Embedding model [%s]: ", settings.Embedding.Model)
	input, _ = reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input != "" {
		settings.Embedding.Model = input
	}

	fmt.Print("API key: ")
	input, _ = reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input != "" {
		settings.Embedding.APIKey = input
	}

	// Rerank
	fmt.Print("Configure rerank? (y/N): ")
	input, _ = reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if strings.ToLower(input) == "y" {
		rr := &config.RerankConfig{
			Provider: "siliconflow",
			BaseURL:  settings.Embedding.BaseURL,
			APIKey:   settings.Embedding.APIKey,
			Model:    "BAAI/bge-reranker-v2-m3",
		}
		fmt.Printf("Rerank model [%s]: ", rr.Model)
		input, _ = reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input != "" {
			rr.Model = input
		}
		settings.Rerank = rr
	}

	return settings
}

func sanitizeName(name string) string {
	// Keep valid ASCII chars, hash the original for uniqueness if truncated
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "_")
	name = strings.ReplaceAll(name, "-", "_")
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	result := b.String()
	// If the result is too short (Chinese-heavy name got stripped), use a hash
	if len(result) < 3 {
		h := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
		result = h[:8]
	}
	return result
}

func maskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + "****" + key[len(key)-4:]
}

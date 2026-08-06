package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chenshao/rags/internal/app"
	"github.com/chenshao/rags/internal/client"
	"github.com/chenshao/rags/internal/config"
	"github.com/chenshao/rags/internal/embedder"
	"github.com/chenshao/rags/internal/indexer"
	"github.com/chenshao/rags/internal/project"
	"github.com/chenshao/rags/internal/publicrepo"
	"github.com/chenshao/rags/internal/server"
	"github.com/chenshao/rags/internal/vectordb"

	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "rags",
		Short: "Local Semantic Index System — semantic search over your project files",
		Long: `rags is a local-first CLI tool that adds semantic (meaning-based) search
to your project files. It uses:

  • Qdrant (vector database) — stores embeddings locally at http://localhost:6333
  • SiliconFlow API — bge-m3 model for text embeddings, bge-reranker-v2-m3 for reranking

== Quick Start ==

  1. rags init        Create a .rags project and index all files
  2. rags grep "..."   Search with natural language
  3. rags update       Re-index after changing files

== Project discovery & directory scope ==

  grep, update, files, and config walk up parent directories to find .rags.
  You can run them from any subdirectory inside a project.

  grep scopes search to the current directory (like rg): cd into a subfolder
  to search only that folder and its children. Works for private and public repos.

== Configuration ==

  Global:  ~/.rags/settings.yaml   (API keys, Qdrant URL, model names)
           ~/.rags/projects.yaml   (registry of all initialized projects)
  Local:   .rags/config.yaml       (project paths, exclude rules, chunk settings)
           .rags/state.json        (file hash manifest for incremental updates)

== How It Works ==

  init:   walk files → chunk into paragraphs → embed with bge-m3 → upsert to Qdrant
  grep:   embed query → Qdrant vector search (top50) → rerank (top10) → print top5
  update: scan files → compare hashes → add/modify/delete chunks → cross-project GC

  Searching is semantic — "如何配置 OTA 分区" can match flash layout docs even
  when those exact words never appear in the text.`,
	}

	root.AddCommand(initCmd())
	root.AddCommand(updateCmd())
	root.AddCommand(grepCmd())
	root.AddCommand(filesCmd())
	root.AddCommand(configCmd())
	root.AddCommand(projectsCmd())
	root.AddCommand(serveCmd())
	root.AddCommand(dockerInitCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func initCmd() *cobra.Command {
	var public bool
	var publicName string

	cmd := &cobra.Command{
		Use:   "init [PATH]",
		Short: "Create a .rags project and perform the first full index",
		Long: `Initialize rags in the current directory or PATH. This will:

  1. Check/create ~/.rags/settings.yaml (interactive first-time setup for API key)
  2. Create .rags/config.yaml with default chunk settings (800 chars, 150 overlap)
  3. Create a Qdrant collection named rags_<project_name>
  4. Register the project in ~/.rags/projects.yaml
  5. Scan all text files, chunk into paragraphs, embed, and upsert to Qdrant
  6. Write .rags/state.json with file hashes for future incremental updates

Public mode (root only):

  sudo rags init --public [--name NAME] [PATH]

  Creates a shared repository under /srv/rags/<name>, writes public metadata into
  .rags/config.yaml, installs a systemd service, and starts a read-only search server.
  Other users can run rags grep inside the repo, but cannot update the index.

  Re-running init after deleting .rags automatically reclaims the previous Qdrant
  collection for this path (no manual curl DELETE needed).

Default exclude list: .git .rags node_modules __pycache__ .venv vendor .idea .vscode
Edit .rags/config.yaml after init to customize paths and chunk settings.

Examples:
  rags init                          Initialize in current directory
  sudo rags init --public            Create public repo from current directory
  sudo rags init --public --name bk7258-docs /path/to/docs`,

		Example: `  rags init
  sudo rags init --public
  sudo rags init --public --name bk7258-docs /path/to/docs`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sourcePath, err := resolveInitSourcePath(args)
			if err != nil {
				return err
			}

			if public {
				settings, err := loadSettingsForPublicInit()
				if err != nil {
					return err
				}
				vdb := vectordb.NewClient(settings.Qdrant.URL)
				_, err = publicrepo.Init(publicrepo.InitOptions{
					Name:       publicName,
					SourcePath: sourcePath,
					Settings:   settings,
					VDB:        vdb,
					IndexFn:    runFullIndex,
				})
				return err
			}

			cwd := sourcePath
			if config.HasRagsProject(cwd) {
				return fmt.Errorf("rags project already exists in %s", cwd)
			}

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

			projectName := filepath.Base(cwd)
			absCwd, _ := filepath.Abs(cwd)
			collectionName := config.CollectionNameForPath(absCwd, projectName)

			vdb := vectordb.NewClient(settings.Qdrant.URL)

			if err := project.ReclaimResourcesForPath(absCwd, collectionName, vdb); err != nil {
				return fmt.Errorf("reclaim previous resources: %w", err)
			}

			if err := vdb.EnsureCollection(collectionName, config.EmbeddingVectorSize(settings.Embedding)); err != nil {
				return fmt.Errorf("create collection: %w", err)
			}

			pcfg := config.DefaultProjectConfig(projectName)
			pcfg.Collection = collectionName
			if err := config.SaveProjectConfig(cwd, pcfg); err != nil {
				return fmt.Errorf("save project config: %w", err)
			}

			if err := config.RegisterProject(config.ProjectEntry{
				Name:       projectName,
				Path:       absCwd,
				Collection: collectionName,
			}); err != nil {
				return fmt.Errorf("register project: %w", err)
			}

			fmt.Printf("✓ Project '%s' initialized\n", projectName)
			fmt.Printf("  Collection: %s\n", collectionName)
			fmt.Println("Indexing files...")
			if err := runFullIndex(cwd, pcfg, collectionName, &settings, vdb); err != nil {
				return fmt.Errorf("initial index: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&public, "public", false, "Create a shared public repository under /srv/rags/<name> (requires root)")
	cmd.Flags().StringVar(&publicName, "name", "", "Public repository name (default: derived from source folder name)")
	return cmd
}

func updateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Incrementally sync the index with file changes",
		Long: `Scan the project for file changes and update only what's needed:

  1. Walk configured paths, compute SHA256 hash of each file
  2. Compare against .rags/state.json to detect: new / modified / deleted files
  3. Deleted files → delete all chunks from Qdrant by file_path filter
  4. New & modified files → re-chunk, re-embed, upsert (file-level invalidation)
  5. Update .rags/state.json with new hashes
  6. Cross-project GC: check ~/.rags/projects.yaml, if any registered project
     directory no longer exists, delete its Qdrant collection and unregister it

  Only files that actually changed are re-indexed. Unchanged files are skipped.

  Walks up to find .rags from the current directory. Update always covers
  the whole project, not just the current subdirectory.

Examples:
  rags update                        Update current project incrementally
  cd project/subdir && rags update   Same — updates the whole project`,

		Example: "  rags update",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get current directory: %w", err)
			}

			projectRoot, _, err := resolveProjectContext(cwd)
			if err != nil {
				return err
			}

			pcfg, err := config.LoadProjectConfig(projectRoot)
			if err != nil {
				return fmt.Errorf("not a rags project (run 'rags init' first): %w", err)
			}
			if err := server.RequireRootForPublicWrite(pcfg); err != nil {
				return err
			}

			settings, err := loadSettingsForProject(pcfg)
			if err != nil {
				return fmt.Errorf("load settings: %w", err)
			}

			if settings.Embedding.APIKey == "" {
				return fmt.Errorf("no API key configured. Run 'rags init' first or edit ~/.rags/settings.yaml")
			}

			collectionName, err := config.ResolveCollection(projectRoot, pcfg)
			if err != nil {
				return fmt.Errorf("resolve collection: %w", err)
			}

			// Load current state
			state, err := config.LoadProjectState(projectRoot)
			if err != nil {
				return fmt.Errorf("load state: %w", err)
			}

			// Create clients
			vdb := vectordb.NewClient(settings.Qdrant.URL)
			emb := embedder.NewClient(settings.Embedding.BaseURL, settings.Embedding.APIKey, settings.Embedding.Model, config.EmbeddingVectorSize(settings.Embedding))
			idx := indexer.New(vdb, emb)

			// Scan files
			fmt.Println("Scanning files...")
			files, err := idx.ScanFiles(projectRoot, pcfg)
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
				if project.NeedsFullReindexReset(state, len(files), len(toIndex)) {
					if err := project.ResetCollection(vdb, collectionName); err != nil {
						return err
					}
				}
				if err := vdb.EnsureCollection(collectionName, config.EmbeddingVectorSize(settings.Embedding)); err != nil {
					return fmt.Errorf("ensure collection: %w", err)
				}
				fmt.Printf("Indexing %d files...\n", len(toIndex))
				if err := idx.IndexFiles(projectRoot, collectionName, pcfg.Name, toIndex, pcfg.Index.ChunkSize, pcfg.Index.Overlap); err != nil {
					return fmt.Errorf("index files: %w", err)
				}
			}

			// Update state
			newState := indexer.UpdateState(state, files, deletedFiles)
			if err := config.SaveProjectState(projectRoot, newState); err != nil {
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
	var keywords []string
	var dedup, explore bool

	cmd := &cobra.Command{
		Use:   "grep [query]",
		Short: "Semantic search — find files by meaning, not exact words",
		Long: `Search indexed files using natural language. The pipeline:

  1. Embed the query with the configured embedding model (default Qwen3-Embedding-8B)
  2. Qdrant cosine-similarity search → top 50 candidate chunks
  3. Optional reranker (default bge-reranker-v2-m3) → refined top results

Output format: score, file path, line range, and chunk text preview.

== Directory scope ==

  Walks up parent directories to find .rags — no need to cd to the project root.
  Search is scoped to the current directory, like rg or grep in a tree:

    cd project/              → search entire project
    cd project/已完成/       → search only files under 已完成/

  Works for private repos and public repos (remote search server).

This is SEMANTIC search — ask in natural language; exact keywords are not required.

== Query Reformulation Tips ==

Semantic search works best when you describe the MEANING, not exact terms.
For complex searches, use -k to break the question into orthogonal aspects.
Each keyword searches independently, then results are merged (best score wins).

  ✅ Good:  rags grep "BK7258 如何进入 ATE 测试模式"
            rags grep "哈基米是什么梗" -k "来源" -k "为什么火"
            rags grep -k "GPIO 配置" -k "中断处理"
  ❌ Bad:   rags grep "OTA + flash"              (use natural language, not operators)
            rags grep "配置"                      (too short, no semantic context)

Flags:
  -k, --keyword       Additional keyword to search (repeatable). Each keyword
                      is embedded and searched independently, then merged.
  -n, --top int       Number of results to return (default 5)
  -d, --dedup         Only show best chunk per file (useful for overview)
  -e, --explore       Show agent-friendly reading path with categories

Examples:
  rags grep "如何烧录固件"                 Top 5 results
  rags grep "哈基米" -n 10                 Top 10 results
  cd docs/api && rags grep "OTA 分区"      Search only under docs/api/
  rags grep "启动流程" -d -n 8             Best chunk per file
  rags grep "模块架构" -e -n 10            Agent reading path
  rags grep "电源管理" -k "功耗" -k "休眠"  Multi-keyword merged search
  rags grep -k "WiFi 配网" -k "BLE 配对"   Pure keyword search (no main query)`,

		Example: `  rags grep "如何烧录固件"
  rags grep "哈基米" -n 10
  cd docs/api && rags grep "OTA 分区"
  rags grep "启动流程" -d -n 8
  rags grep "模块架构" -e
  rags grep "电源管理" -k "功耗" -k "休眠"
  rags grep -k "WiFi 配网" -k "BLE 配对"`,
		Args: cobra.MinimumNArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Build query list: positional arg + -k flags
			var queries []string
			if len(args) > 0 && args[0] != "" {
				queries = append(queries, args[0])
			}
			queries = append(queries, keywords...)

			if len(queries) == 0 {
				return fmt.Errorf("no query provided: use a positional query or -k flag(s)")
			}

			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get current directory: %w", err)
			}

			projectRoot, pathPrefix, err := resolveProjectContext(cwd)
			if err != nil {
				return err
			}

			pcfg, err := config.LoadProjectConfig(projectRoot)
			if err != nil {
				return fmt.Errorf("not a rags project: %w", err)
			}

			if pcfg.IsPublic() && pcfg.Public != nil {
				output, err := client.RemoteSearch(pcfg.Public.Endpoint, client.SearchRequest{
					Queries:    queries,
					TopK:       topK,
					Dedup:      dedup,
					Explore:    explore,
					PathPrefix: pathPrefix,
				})
				if err != nil {
					return err
				}
				fmt.Print(output)
				return nil
			}

			settings, err := config.LoadSettings()
			if err != nil {
				return fmt.Errorf("load settings: %w", err)
			}
			if settings.Embedding.APIKey == "" {
				return fmt.Errorf("no API key configured")
			}

			result, err := app.RunSearch(pcfg, settings, app.SearchOptions{
				ProjectRoot: projectRoot,
				Queries:     queries,
				TopK:        topK,
				Dedup:       dedup,
				Explore:     explore,
				PathPrefix:  pathPrefix,
			})
			if err != nil {
				return err
			}
			fmt.Print(result.Output)
			return nil
		},
	}

	cmd.Flags().IntVarP(&topK, "top", "n", 5, "Number of top results to return")
	cmd.Flags().StringArrayVarP(&keywords, "keyword", "k", nil, "Additional keyword to search (repeatable, merged with main query)")
	cmd.Flags().BoolVarP(&dedup, "dedup", "d", false, "Only show best chunk per file")
	cmd.Flags().BoolVarP(&explore, "explore", "e", false, "Show agent-friendly reading path with categories")
	return cmd
}

func filesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "files",
		Short: "List all indexed files with their current hashes",
		Long: `Read .rags/state.json and display every file tracked in the index.
Shows relative path and the first 8 characters of the SHA256 hash.

Walks up to find .rags. When run from a subdirectory, lists only files
under the current directory (same scope rule as grep).

Use this to verify what's in the index, or before running rags update
to see what will be checked.

Examples:
  rags files                          List indexed files
  cd project/已完成 && rags files     List files under 已完成/`,
		Example: "  rags files",
		RunE: func(cmd *cobra.Command, args []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get current directory: %w", err)
			}

			projectRoot, pathPrefix, err := resolveProjectContext(cwd)
			if err != nil {
				return err
			}

			state, err := config.LoadProjectState(projectRoot)
			if err != nil {
				return fmt.Errorf("not a rags project: %w", err)
			}

			if len(state.Files) == 0 {
				fmt.Println("No indexed files.")
				return nil
			}

			count := 0
			for path, hash := range state.Files {
				if pathPrefix != "" && !strings.HasPrefix(path, pathPrefix) {
					continue
				}
				fmt.Printf("  %s  (%s)\n", path, hash[:8])
				count++
			}
			fmt.Printf("\nTotal: %d files\n", count)
			return nil
		},
	}
}

func configCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Show current global and project configuration",
		Long: `Display:

  Global (~/.rags/settings.yaml):
    • Qdrant URL, embedding model, rerank model
    • API keys are masked (first 4 + last 4 characters)

  Project (.rags/config.yaml, if in a rags project):
    • Project name, scanned paths, exclude patterns
    • Chunk size and overlap settings
    • Current search scope when run from a subdirectory

  Walks up to find .rags from the current directory.

Examples:
  rags config                         Show all configuration`,
		Example: "  rags config",
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
			fmt.Printf("  Vector dimension: %d\n", config.EmbeddingVectorSize(settings.Embedding))
			fmt.Printf("  Embedding API:    %s\n", maskKey(settings.Embedding.APIKey))
			if settings.Rerank != nil {
				fmt.Printf("  Rerank model:     %s\n", settings.Rerank.Model)
				fmt.Printf("  Rerank API:       %s\n", maskKey(settings.Rerank.APIKey))
			} else {
				fmt.Println("  Rerank:           (disabled)")
			}
			fmt.Println()

			// Project config
			projectRoot, pathPrefix, err := resolveProjectContext(cwd)
			if err == nil {
				pcfg, err := config.LoadProjectConfig(projectRoot)
				if err == nil {
					fmt.Println("── Project (.rags/config.yaml) ──")
					fmt.Printf("  Root:       %s\n", projectRoot)
					if pathPrefix != "" {
						fmt.Printf("  Scope:      %s\n", pathPrefix)
					}
					fmt.Printf("  Name:       %s\n", pcfg.Name)
					fmt.Printf("  Mode:       %s\n", pcfg.Mode)
					if pcfg.IsPublic() && pcfg.Public != nil {
						fmt.Printf("  Repo root:  %s\n", pcfg.Public.RepoRoot)
						fmt.Printf("  Docs root:  %s\n", pcfg.Public.DocsRoot)
						fmt.Printf("  Endpoint:   %s\n", pcfg.Public.Endpoint)
						fmt.Printf("  Service:    %s\n", pcfg.Public.Service)
						fmt.Printf("  Collection: %s\n", pcfg.Public.Collection)
					}
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
		Short: "List all registered rags projects and their status",
		Long: `Read ~/.rags/projects.yaml and display every registered project.
Shows project name, existence status (✓ or ✗ missing), collection name, and path.

Projects are auto-registered on rags init and auto-removed by cross-project GC
when their directories are deleted and another project runs rags update.

Examples:
  rags projects                       List all projects
  rags projects                       (check which projects exist)
  rags projects                       (find orphaned projects to clean up)`,
		Example: "  rags projects",
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

func resolveProjectContext(cwd string) (projectRoot string, pathPrefix string, err error) {
	projectRoot, err = config.FindProjectRoot(cwd)
	if err != nil {
		return "", "", err
	}
	pathPrefix, err = config.SearchPathPrefix(projectRoot, cwd)
	if err != nil {
		return "", "", err
	}
	return projectRoot, pathPrefix, nil
}

func serveCmd() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:    "serve",
		Short:  "Run the public rags search server (systemd or Docker)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				name = os.Getenv("RAGS_NAME")
			}
			if name == "" {
				return fmt.Errorf("--name or RAGS_NAME is required")
			}

			repoRoot := config.PublicRepoPath(name)
			pcfg, err := config.LoadProjectConfig(repoRoot)
			if err != nil {
				return fmt.Errorf("load public repo config: %w", err)
			}
			settings, err := loadSettingsForServe(pcfg)
			if err != nil {
				return err
			}
			if settings.Embedding.APIKey == "" {
				return fmt.Errorf("no API key configured for serve")
			}

			srv, err := server.New(repoRoot, pcfg, settings)
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return srv.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Public repository name")
	return cmd
}

func dockerInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "docker-init",
		Short:  "Initialize a public repository for Docker Compose (internal)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := os.Getenv("RAGS_NAME")
			if name == "" {
				return fmt.Errorf("RAGS_NAME is required")
			}

			port := 18787
			if v := os.Getenv("RAGS_PORT"); v != "" {
				p, err := parsePort(v)
				if err != nil {
					return fmt.Errorf("invalid RAGS_PORT: %w", err)
				}
				port = p
			}

			publicHost := os.Getenv("RAGS_PUBLIC_HOST")
			if publicHost == "" {
				publicHost = "127.0.0.1"
			}

			docsPath := os.Getenv("DOCS_PATH")
			if docsPath == "" {
				docsPath = config.DockerDocsRoot
			}

			settingsDir := os.Getenv("RAGS_SETTINGS_DIR")
			if settingsDir == "" {
				settingsDir = config.DockerSettingsDir
			}

			settings, err := config.WriteSettingsFromEnv(settingsDir)
			if err != nil {
				return err
			}

			vdb := vectordb.NewClient(settings.Qdrant.URL)
			_, err = publicrepo.InitDocker(publicrepo.DockerInitOptions{
				Name:        name,
				DocsPath:    docsPath,
				PublicHost:  publicHost,
				Port:        port,
				Settings:    settings,
				SettingsDir: settingsDir,
				VDB:         vdb,
				IndexFn:     runFullIndex,
			})
			return err
		},
	}
}

func parsePort(s string) (int, error) {
	var port int
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &port)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("invalid port %q", s)
	}
	return port, nil
}

func loadSettingsForServe(pcfg config.ProjectConfig) (config.GlobalSettings, error) {
	if dir := os.Getenv("RAGS_SETTINGS_DIR"); dir != "" {
		return config.LoadSettingsFromDir(dir)
	}
	if pcfg.Public != nil && pcfg.Public.SettingsDir != "" {
		settings, err := config.LoadSettingsFromDir(pcfg.Public.SettingsDir)
		if err == nil && settings.Embedding.APIKey != "" {
			return settings, nil
		}
	}
	if pcfg.Public != nil && pcfg.Public.Deploy == config.DeployDocker {
		return config.LoadSettingsFromDir(config.DockerSettingsDir)
	}
	return config.LoadRootSettings()
}

func resolveInitSourcePath(args []string) (string, error) {
	if len(args) == 1 {
		return filepath.Abs(args[0])
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get current directory: %w", err)
	}
	return filepath.Abs(cwd)
}

func loadSettingsForPublicInit() (config.GlobalSettings, error) {
	settings, err := config.LoadRootSettings()
	if err != nil {
		return config.GlobalSettings{}, fmt.Errorf("load root settings: %w", err)
	}
	if settings.Embedding.APIKey == "" {
		return config.GlobalSettings{}, fmt.Errorf("no API key in %s/settings.yaml; configure SiliconFlow credentials for root first", filepath.Join(config.RootHome, config.RagsDir))
	}
	return settings, nil
}

func loadSettingsForProject(pcfg config.ProjectConfig) (config.GlobalSettings, error) {
	if pcfg.IsPublic() && pcfg.Public != nil {
		if pcfg.Public.Deploy == config.DeployDocker && pcfg.Public.SettingsDir != "" {
			return config.LoadSettingsFromDir(pcfg.Public.SettingsDir)
		}
		return config.LoadRootSettings()
	}
	return config.LoadSettings()
}

// --- helpers ---

func runFullIndex(cwd string, pcfg config.ProjectConfig, collection string, settings *config.GlobalSettings, vdb *vectordb.Client) error {
	emb := embedder.NewClient(settings.Embedding.BaseURL, settings.Embedding.APIKey, settings.Embedding.Model, config.EmbeddingVectorSize(settings.Embedding))
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
			Model:    config.DefaultRerankModel,
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

func maskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + "****" + key[len(key)-4:]
}

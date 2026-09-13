package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
)

// backupManifest describes one completed backup directory.
type backupManifest struct {
	CreatedAt   string             `json:"created_at"`
	Mode        string             `json:"mode"`
	ToolVersion string             `json:"tool_version"`
	Complete    bool               `json:"complete"`
	Archive     *archiveBackupInfo `json:"archive,omitempty"`
	Sources     []sourceBackupInfo `json:"sources,omitempty"`
}

type archiveBackupInfo struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type sourceBackupInfo struct {
	Agent   string `json:"agent"`
	Machine string `json:"machine,omitempty"`
	Dir     string `json:"dir"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
}

const backupDirPrefix = "agentsview-backup-"

func newBackupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "backup",
		Short:        "Back up the archive and agent source directories",
		GroupID:      groupData,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newBackupCreateCommand())
	cmd.AddCommand(newBackupListCommand())
	return cmd
}

func newBackupCreateCommand() *cobra.Command {
	var dest, mode string
	var keep int
	var dryRun, forceYes bool
	cmd := &cobra.Command{
		Use:          "create --dest <dir> [--archive|--sources|--all]",
		Short:        "Create a backup",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackupCreate(cmd, backupCreateOptions{
				Dest: dest, Mode: mode, Keep: keep,
				DryRun: dryRun, Yes: forceYes,
			})
		},
	}
	cmd.Flags().StringVar(&dest, "dest", "", "Backup destination directory (required)")
	cmd.Flags().StringVar(&mode, "mode", "archive", "What to back up: archive, sources, or all")
	cmd.Flags().IntVar(&keep, "keep", 0, "Keep only the N newest backups (0 keeps everything)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Estimate the backup without copying")
	cmd.Flags().BoolVar(&forceYes, "yes", false, "Skip confirmation prompt")
	_ = cmd.MarkFlagRequired("dest")
	return cmd
}

func newBackupListCommand() *cobra.Command {
	var dest string
	cmd := &cobra.Command{
		Use:          "list --dest <dir>",
		Short:        "List backups in a destination directory",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBackupList(dest)
		},
	}
	cmd.Flags().StringVar(&dest, "dest", "", "Backup destination directory (required)")
	_ = cmd.MarkFlagRequired("dest")
	return cmd
}

type backupCreateOptions struct {
	Dest   string
	Mode   string
	Keep   int
	DryRun bool
	Yes    bool
}

func runBackupCreate(cmd *cobra.Command, opts backupCreateOptions) error {
	switch opts.Mode {
	case "archive", "sources", "all":
	default:
		return fmt.Errorf("unknown mode %q; use archive, sources, or all", opts.Mode)
	}
	cfg, err := config.LoadMinimal()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if opts.DryRun {
		return reportBackupEstimate(cfg, opts)
	}

	stamp := timeNowBackupStamp()
	backupDir := filepath.Join(opts.Dest,
		fmt.Sprintf("%s%s", backupDirPrefix, stamp))
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return fmt.Errorf("creating backup directory: %w", err)
	}

	manifest := backupManifest{
		CreatedAt:   stamp,
		Mode:        opts.Mode,
		ToolVersion: version,
	}

	if opts.Mode == "archive" || opts.Mode == "all" {
		archiveDir := filepath.Join(backupDir, "archive")
		if err := os.MkdirAll(archiveDir, 0o755); err != nil {
			return err
		}
		snapshot := filepath.Join(archiveDir, "sessions.db")
		if err := db.BackupArchiveTo(
			context.Background(), cfg.DBPath, snapshot); err != nil {
			return err
		}
		info, err := archiveInfo(snapshot)
		if err != nil {
			return err
		}
		manifest.Archive = info

		// The asset store holds tool-result images referenced by the
		// archive; back it up alongside the database.
		if err := copyDirContents(
			filepath.Join(cfg.DataDir, "assets"),
			filepath.Join(backupDir, "assets"),
		); err != nil {
			return fmt.Errorf("copying asset store: %w", err)
		}
	}

	if opts.Mode == "sources" || opts.Mode == "all" {
		for _, src := range cfg.SessionSources {
			info, err := copySourceRoot(
				filepath.Join(backupDir, "sources"),
				string(src.Agent), src.Machine, src.Dir,
			)
			if err != nil {
				return fmt.Errorf("backing up %s sources at %s: %w",
					src.Agent, src.Dir, err)
			}
			manifest.Sources = append(manifest.Sources, *info)
		}
	}

	manifest.Complete = true
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(
		filepath.Join(backupDir, "manifest.json"), payload, 0o644,
	); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}

	fmt.Printf("Backup written to %s\n", backupDir)
	return rotateBackups(opts.Dest, opts.Keep)
}

// reportBackupEstimate prints what a backup would contain without
// touching the destination.
func reportBackupEstimate(cfg config.Config, opts backupCreateOptions) error {
	if opts.Mode == "archive" || opts.Mode == "all" {
		info, err := os.Stat(cfg.DBPath)
		if err == nil {
			fmt.Printf("archive: %s (%s)\n",
				cfg.DBPath, formatBytes(info.Size()))
		}
	}
	if opts.Mode == "sources" || opts.Mode == "all" {
		for _, src := range cfg.SessionSources {
			var files int
			var bytes int64
			_ = filepath.WalkDir(src.Dir, func(path string, entry os.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return nil
				}
				files++
				if info, statErr := entry.Info(); statErr == nil {
					bytes += info.Size()
				}
				return nil
			})
			fmt.Printf("sources %s [%s]: %d files (%s)\n",
				src.Agent, src.Machine, files, formatBytes(bytes))
		}
	}
	fmt.Println("Dry run: no backup written.")
	return nil
}

func runBackupList(dest string) error {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dest, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), backupDirPrefix) {
			continue
		}
		path := filepath.Join(dest, entry.Name(), "manifest.json")
		payload, err := os.ReadFile(path)
		if err != nil {
			fmt.Printf("%s  (incomplete)\n", entry.Name())
			continue
		}
		var manifest backupManifest
		if err := json.Unmarshal(payload, &manifest); err != nil {
			fmt.Printf("%s  (unreadable manifest)\n", entry.Name())
			continue
		}
		fmt.Printf("%s  mode=%s complete=%t\n",
			entry.Name(), manifest.Mode, manifest.Complete)
	}
	return nil
}

// rotateBackups deletes the oldest backup directories beyond keep.
func rotateBackups(dest string, keep int) error {
	if keep <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return err
	}
	type backupDir struct {
		path      string
		createdAt string
	}
	var dirs []backupDir
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), backupDirPrefix) {
			continue
		}
		createdAt := entry.Name()[len(backupDirPrefix):]
		if payload, err := os.ReadFile(filepath.Join(
			dest, entry.Name(), "manifest.json")); err == nil {
			var manifest backupManifest
			if json.Unmarshal(payload, &manifest) == nil &&
				manifest.CreatedAt != "" {
				createdAt = manifest.CreatedAt
			}
		}
		dirs = append(dirs, backupDir{
			path:      filepath.Join(dest, entry.Name()),
			createdAt: createdAt,
		})
	}
	if len(dirs) <= keep {
		return nil
	}
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].createdAt > dirs[j].createdAt
	})
	for _, dir := range dirs[keep:] {
		if err := os.RemoveAll(dir.path); err != nil {
			return fmt.Errorf("rotating out %s: %w", dir.path, err)
		}
		fmt.Printf("Rotated out old backup %s\n", filepath.Base(dir.path))
	}
	return nil
}

func archiveInfo(path string) (*archiveBackupInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return nil, err
	}
	return &archiveBackupInfo{
		Path:   path,
		Bytes:  info.Size(),
		SHA256: hex.EncodeToString(digest.Sum(nil)),
	}, nil
}

// copySourceRoot copies one agent source root into the backup,
// skipping SQLite sidecar files. App-owned containers (state.vscdb,
// opencode.db) are copied together with their -wal/-shm siblings so
// the manifest can honestly report them as best-effort.
func copySourceRoot(
	backupSourcesRoot, agent, machine, sourceDir string,
) (*sourceBackupInfo, error) {
	target := filepath.Join(backupSourcesRoot, agent)
	if machine != "" {
		target = filepath.Join(target, machine)
	}
	info := &sourceBackupInfo{
		Agent: agent, Machine: machine, Dir: sourceDir,
	}
	err := filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(target, rel), 0o755)
		}
		if strings.HasSuffix(rel, "-wal") || strings.HasSuffix(rel, "-shm") {
			return nil
		}
		if err := copyFileContents(
			path, filepath.Join(target, rel)); err != nil {
			return err
		}
		if fileInfo, statErr := entry.Info(); statErr == nil {
			info.Files++
			info.Bytes += fileInfo.Size()
		}
		return nil
	})
	return info, err
}

func copyDirContents(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		src := filepath.Join(srcDir, entry.Name())
		dst := filepath.Join(dstDir, entry.Name())
		if entry.IsDir() {
			if err := copyDirContents(src, dst); err != nil {
				return err
			}
			continue
		}
		if err := copyFileContents(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyFileContents(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func timeNowBackupStamp() string {
	return time.Now().UTC().Format("20060102T150405")
}

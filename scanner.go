package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Category groups cleanup targets in the UI.
type Category int

const (
	CategoryNodeModules    Category = iota
	CategoryProjectArtifacts
	CategoryPackageManager
	CategoryBuildCache
	CategoryDocker
	CategoryToolchains
	CategoryMobile
	CategoryUnusedApps
	CategoryAppData
	CategoryUserData
	CategorySystemCache
	CategorySystemHotspots
	CategoryLogs
	CategoryTrash
	CategoryLargeFiles
)

var categoryNames = [...]string{
	"NODE MODULES — Stale Projects",
	"PROJECT ARTIFACTS",
	"PACKAGE MANAGER CACHES",
	"BUILD CACHES & ARTIFACTS",
	"DOCKER",
	"RUNTIMES & TOOLCHAINS",
	"MOBILE DEV DATA",
	"UNUSED APPS",
	"APP DATA & SUPPORT",
	"USER DATA HOTSPOTS",
	"SYSTEM CACHES",
	"SYSTEM HOTSPOTS",
	"LOGS",
	"TRASH",
	"LARGE FILES",
}

func (c Category) String() string {
	if int(c) < len(categoryNames) {
		return categoryNames[c]
	}
	return "UNKNOWN"
}

type DeleteMode int

const (
	DeleteModePath DeleteMode = iota
	DeleteModeCommand
	DeleteModeTrash
)

// Target is a single directory that can be deleted.
type Target struct {
	ID         string
	ParentID   string
	Path       string
	Category   Category
	Label      string
	Size       int64
	LastMod    time.Time
	Info       string
	Selected   bool
	Locked     bool
	Group      bool
	Expandable bool
	Expanded   bool
	Deleted    bool
	Error      string
	DeleteMode DeleteMode
	Command    []string
}

type cacheDef struct {
	relPath  string
	category Category
	label    string
	safe     bool // selected by default
}

type pathDef struct {
	path     string
	category Category
	label    string
	safe     bool
	locked   bool
}

type childScanDef struct {
	base      string
	category  Category
	label     string
	safe      bool
	locked    bool
	minSize   int64
	dirsOnly  bool
	includeIn map[string]bool
}

type appRelatedPathDef struct {
	label    string
	path     string
	selected bool
	info     string
}

const unusedAppDays = 30

var cacheDefs = []cacheDef{
	// Package manager caches — safe to delete
	{".npm", CategoryPackageManager, "npm cache", true},
	{".yarn/berry/cache", CategoryPackageManager, "Yarn Berry cache", true},
	{"Library/Caches/Yarn", CategoryPackageManager, "Yarn v1 cache", true},
	{".pnpm-store", CategoryPackageManager, "pnpm store", true},
	{"Library/pnpm/store", CategoryPackageManager, "pnpm store (Library)", true},
	{".bun/install/cache", CategoryPackageManager, "Bun cache", true},
	{"Library/Caches/pip", CategoryPackageManager, "pip cache", true},
	{"Library/Caches/CocoaPods", CategoryPackageManager, "CocoaPods cache", true},
	{"Library/Caches/Homebrew", CategoryPackageManager, "Homebrew cache", true},
	{"go/pkg/mod/cache", CategoryPackageManager, "Go module cache", true},
	// Expensive to rebuild — off by default
	{".cargo/registry/cache", CategoryPackageManager, "Cargo registry cache", false},
	{".gradle/caches", CategoryPackageManager, "Gradle cache", false},
	{".m2/repository", CategoryPackageManager, "Maven local repo", false},
	// Build caches
	{"Library/Developer/Xcode/DerivedData", CategoryBuildCache, "Xcode DerivedData", true},
	{"Library/Developer/CoreSimulator/Caches", CategoryBuildCache, "iOS Simulator caches", true},
	{"Library/Caches/com.apple.dt.Xcode", CategoryBuildCache, "Xcode caches", true},
	{".cache/go-build", CategoryBuildCache, "Go build cache", true},
	// System caches
	{"Library/Caches/com.spotify.client", CategorySystemCache, "Spotify cache", true},
	{"Library/Caches/com.microsoft.VSCode", CategorySystemCache, "VS Code cache", true},
	{"Library/Caches/com.microsoft.VSCode.ShipIt", CategorySystemCache, "VS Code update cache", true},
	{"Library/Caches/Google/Chrome", CategorySystemCache, "Chrome cache", true},
	{"Library/Caches/Firefox", CategorySystemCache, "Firefox cache", true},
	{"Library/Caches/com.docker.docker", CategorySystemCache, "Docker cache", false},
	// Logs
	{"Library/Logs/DiagnosticReports", CategoryLogs, "Diagnostic reports", true},
	{"Library/Logs/CoreSimulator", CategoryLogs, "Simulator logs", true},
	// Trash
	{".Trash", CategoryTrash, "Trash", true},
}

var fixedPathDefs = []pathDef{
	// Mobile and Apple tooling
	{"~/Library/Developer/Xcode/iOS DeviceSupport", CategoryMobile, "Xcode DeviceSupport", false, false},
	{"~/Library/Developer/Xcode/Archives", CategoryMobile, "Xcode Archives", false, false},
	{"~/Library/Developer/CoreSimulator/Devices", CategoryMobile, "iOS Simulator devices", false, false},
	{"~/Library/Developer/Xcode/UserData/Previews", CategoryMobile, "SwiftUI previews", true, false},
	{"~/Library/Android/sdk/system-images", CategoryMobile, "Android SDK system images", false, false},
	{"~/Library/Android/sdk/emulator", CategoryMobile, "Android emulator data", false, false},

	// Runtime and package managers
	{"~/miniconda3/pkgs", CategoryPackageManager, "Conda package cache", true, false},
	{"~/anaconda3/pkgs", CategoryPackageManager, "Conda package cache", true, false},
	{"~/miniforge3/pkgs", CategoryPackageManager, "Conda package cache", true, false},
	{"~/mambaforge/pkgs", CategoryPackageManager, "Conda package cache", true, false},

	// Risky but useful to surface
	{"~/Library/Mail", CategoryUserData, "Mail data", false, true},
	{"~/Library/Messages", CategoryUserData, "Messages data", false, true},
	{"/private/var/vm/sleepimage", CategorySystemHotspots, "Sleep image", false, true},
	{"/private/var/log", CategorySystemHotspots, "System logs", false, true},
	{"/private/var/tmp", CategorySystemHotspots, "System temp", false, true},
	{"/Library/Logs", CategorySystemHotspots, "Library logs", false, true},
	{"~/Library/Containers/com.docker.docker/Data/vms/0/data/Docker.raw", CategorySystemHotspots, "Docker disk image", false, true},
	{"~/Library/Containers/com.docker.docker/Data/vms/0/Docker.raw", CategorySystemHotspots, "Docker disk image", false, true},
}

var childScanDefs = []childScanDef{
	// Runtime managers
	{base: "~/.nvm/versions/node", category: CategoryToolchains, label: "nvm", minSize: 100 << 20, dirsOnly: true},
	{base: "~/.pyenv/versions", category: CategoryToolchains, label: "pyenv", minSize: 100 << 20, dirsOnly: true},
	{base: "~/.rustup/toolchains", category: CategoryToolchains, label: "rustup", minSize: 100 << 20, dirsOnly: true},
	{base: "~/.rbenv/versions", category: CategoryToolchains, label: "rbenv", minSize: 100 << 20, dirsOnly: true},
	{base: "~/.rvm/rubies", category: CategoryToolchains, label: "rvm", minSize: 100 << 20, dirsOnly: true},
	{base: "~/.sdkman/candidates/java", category: CategoryToolchains, label: "sdkman java", minSize: 100 << 20, dirsOnly: true},
	{base: "~/.sdkman/candidates/kotlin", category: CategoryToolchains, label: "sdkman kotlin", minSize: 100 << 20, dirsOnly: true},
	{base: "~/miniconda3/envs", category: CategoryToolchains, label: "conda env", minSize: 100 << 20, dirsOnly: true},
	{base: "~/anaconda3/envs", category: CategoryToolchains, label: "conda env", minSize: 100 << 20, dirsOnly: true},
	{base: "~/miniforge3/envs", category: CategoryToolchains, label: "conda env", minSize: 100 << 20, dirsOnly: true},
	{base: "~/mambaforge/envs", category: CategoryToolchains, label: "conda env", minSize: 100 << 20, dirsOnly: true},

	// Generic hidden storage
	{base: "~/Library/Caches", category: CategorySystemCache, label: "Cache", minSize: 100 << 20},
	{base: "~/Library/Application Support", category: CategoryAppData, label: "App Support", minSize: 100 << 20, locked: true},
	{base: "~/Library/Containers", category: CategoryAppData, label: "Container", minSize: 100 << 20, locked: true},
	{base: "~/Downloads", category: CategoryUserData, label: "Downloads", minSize: 100 << 20, locked: true},
}

var projectArtifactLabels = map[string]string{
	"target":        "Rust target",
	".venv":         "Python virtualenv",
	"venv":          "Python virtualenv",
	".tox":          "tox environments",
	"dist":          "dist output",
	"build":         "build output",
	".next":         "Next.js build output",
	".nuxt":         "Nuxt build output",
	".svelte-kit":   "SvelteKit build output",
	".output":       "Nitro output",
	".turbo":        "Turbo cache",
	".parcel-cache": "Parcel cache",
	"coverage":      "Coverage output",
	".angular":      "Angular cache",
	".vite":         "Vite cache",
}

var largeScanSkip = map[string]bool{
	".git": true, "node_modules": true, ".Trash": true,
	".hg": true, ".svn": true,
}

// scanLargeFiles finds individual files at or above threshold bytes.
// Top-level subdirectories are walked in parallel for speed.
func scanLargeFiles(scanRoot string, threshold int64) []Target {
	var (
		mu      sync.Mutex
		targets []Target
		wg      sync.WaitGroup
	)

	entries, err := os.ReadDir(scanRoot)
	if err != nil {
		return nil
	}

	sem := make(chan struct{}, runtime.NumCPU())

	for _, e := range entries {
		path := filepath.Join(scanRoot, e.Name())

		if !e.IsDir() {
			if info, err := e.Info(); err == nil && info.Size() >= threshold {
				targets = append(targets, Target{
					Path:     path,
					Category: CategoryLargeFiles,
					Label:    shortenHome(path),
					Size:     info.Size(),
					LastMod:  info.ModTime(),
				})
			}
			continue
		}

		if largeScanSkip[e.Name()] {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return fs.SkipDir
				}
				if d.IsDir() {
					if largeScanSkip[d.Name()] {
						return fs.SkipDir
					}
					return nil
				}
				info, err := d.Info()
				if err != nil {
					return nil
				}
				if info.Size() >= threshold {
					t := Target{
						Path:     p,
						Category: CategoryLargeFiles,
						Label:    shortenHome(p),
						Size:     info.Size(),
						LastMod:  info.ModTime(),
					}
					mu.Lock()
					targets = append(targets, t)
					mu.Unlock()
				}
				return nil
			})
		}()
	}

	wg.Wait()
	return targets
}

// scanProjectArtifacts finds large build outputs and virtual environments under scanRoot.
// Items are preselected only when the surrounding project appears stale.
func scanProjectArtifacts(scanRoot string, staleDays int, minSize int64) []Target {
	threshold := time.Now().Add(-time.Duration(staleDays) * 24 * time.Hour)
	var targets []Target

	_ = filepath.WalkDir(scanRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		if !d.IsDir() {
			return nil
		}

		name := d.Name()
		if name == ".git" || name == "node_modules" {
			return fs.SkipDir
		}

		label, ok := projectArtifactLabels[name]
		if !ok {
			return nil
		}

		size := dirSize(path)
		if size < minSize {
			return fs.SkipDir
		}

		projectRoot := filepath.Dir(path)
		lastMod, stale := checkStaleness(projectRoot, threshold)
		targets = append(targets, Target{
			Path:     path,
			Category: CategoryProjectArtifacts,
			Label:    fmt.Sprintf("%s: %s", label, shortenHome(path)),
			Size:     size,
			LastMod:  lastMod,
			Selected: stale,
		})
		return fs.SkipDir
	})

	return targets
}

type dockerSummaryRow struct {
	Type        string `json:"Type"`
	TotalCount  string `json:"TotalCount"`
	Active      string `json:"Active"`
	Size        string `json:"Size"`
	Reclaimable string `json:"Reclaimable"`
}

type dockerContainerRow struct {
	ID     string `json:"ID"`
	Names  string `json:"Names"`
	Image  string `json:"Image"`
	Status string `json:"Status"`
	Size   string `json:"Size"`
}

type dockerImageRow struct {
	ID         string `json:"ID"`
	Repository string `json:"Repository"`
	Tag        string `json:"Tag"`
	Size       string `json:"Size"`
}

func scanDockerTargets() []Target {
	if _, err := exec.LookPath("docker"); err != nil {
		return []Target{dockerInfoTarget("Docker unavailable", "docker CLI not found on PATH")}
	}

	rows, err := dockerSystemDF()
	if err != nil {
		if strings.Contains(err.Error(), "daemon unreachable") {
			return []Target{dockerInfoTarget("Docker daemon not running", "start Docker Desktop or dockerd to enable cleanup")}
		}
		return []Target{dockerInfoTarget("Docker unavailable", "docker CLI is installed but not usable")}
	}

	byType := make(map[string]dockerSummaryRow, len(rows))
	for _, row := range rows {
		byType[row.Type] = row
	}

	var targets []Target

	if row, ok := byType["Build Cache"]; ok {
		size := parseHumanBytesField(row.Reclaimable)
		if size > 0 {
			targets = append(targets, Target{
				ID:         "docker://build-cache",
				Path:       "docker://build-cache",
				Category:   CategoryDocker,
				Label:      "Docker build cache",
				Size:       size,
				Info:       "safe cache cleanup",
				Selected:   true,
				DeleteMode: DeleteModeCommand,
				Command:    []string{"docker", "builder", "prune", "-a", "-f"},
			})
		}
	}

	if row, ok := byType["Containers"]; ok {
		size := parseHumanBytesField(row.Reclaimable)
		children := scanDockerContainerChildren("docker://group/stopped-containers", true)
		if len(children) > 0 {
			targets = append(targets, Target{
				ID:         "docker://group/stopped-containers",
				Path:       "docker://group/stopped-containers",
				Category:   CategoryDocker,
				Label:      "Docker stopped containers",
				Size:       size,
				Info:       pluralize(len(children), "container"),
				Group:      true,
				Expandable: true,
			})
			targets = append(targets, children...)
		} else if size > 0 {
			targets = append(targets, Target{
				ID:         "docker://stopped-containers",
				Path:       "docker://stopped-containers",
				Category:   CategoryDocker,
				Label:      "Docker stopped containers",
				Size:       size,
				Info:       "stopped containers",
				Selected:   true,
				DeleteMode: DeleteModeCommand,
				Command:    []string{"docker", "container", "prune", "-f"},
			})
		}
	}

	if row, ok := byType["Local Volumes"]; ok {
		size := parseHumanBytesField(row.Reclaimable)
		children := scanDockerVolumeChildren("docker://group/unused-volumes", false)
		if len(children) > 0 {
			targets = append(targets, Target{
				ID:         "docker://group/unused-volumes",
				Path:       "docker://group/unused-volumes",
				Category:   CategoryDocker,
				Label:      "Docker unused volumes",
				Size:       size,
				Info:       pluralize(len(children), "volume"),
				Group:      true,
				Expandable: true,
			})
			targets = append(targets, children...)
		} else if size > 0 {
			targets = append(targets, Target{
				ID:         "docker://unused-volumes",
				Path:       "docker://unused-volumes",
				Category:   CategoryDocker,
				Label:      "Docker unused volumes",
				Size:       size,
				Info:       "unused volumes",
				DeleteMode: DeleteModeCommand,
				Command:    []string{"docker", "volume", "prune", "-f"},
			})
		}
	}

	if row, ok := byType["Images"]; ok {
		size := parseHumanBytesField(row.Reclaimable)
		children, childErr := scanDockerImageChildren("docker://group/unused-images", false)
		if childErr == nil && len(children) > 0 {
			targets = append(targets, Target{
				ID:         "docker://group/unused-images",
				Path:       "docker://group/unused-images",
				Category:   CategoryDocker,
				Label:      "Docker unused images",
				Size:       size,
				Info:       pluralize(len(children), "image") + "  re-pull may be needed",
				Group:      true,
				Expandable: true,
			})
			targets = append(targets, children...)
		} else {
			total := atoi(row.TotalCount)
			active := atoi(row.Active)
			unused := total - active
			if size > 0 || unused > 0 {
				info := "unused images  re-pull may be needed"
				if unused > 0 {
					info = pluralize(unused, "image") + "  re-pull may be needed"
				}
				targets = append(targets, Target{
					ID:         "docker://unused-images",
					Path:       "docker://unused-images",
					Category:   CategoryDocker,
					Label:      "Docker unused images",
					Size:       size,
					Info:       info,
					DeleteMode: DeleteModeCommand,
					Command:    []string{"docker", "image", "prune", "-a", "-f"},
				})
			}
		}
	}

	return targets
}

func scanDockerContainerChildren(parentID string, selected bool) []Target {
	rows, err := dockerContainerRows()
	if err != nil {
		return nil
	}

	children := make([]Target, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.ID) == "" {
			continue
		}
		label := row.Names
		if label == "" {
			label = shortDockerID(row.ID)
		}
		info := row.Status
		if row.Image != "" {
			info = row.Image + "  " + row.Status
		}
		children = append(children, Target{
			ID:         "docker://container/" + row.ID,
			ParentID:   parentID,
			Path:       "docker://container/" + row.ID,
			Category:   CategoryDocker,
			Label:      label,
			Size:       parseDockerContainerSize(row.Size),
			Info:       info,
			Selected:   selected,
			DeleteMode: DeleteModeCommand,
			Command:    []string{"docker", "rm", row.ID},
		})
	}
	return children
}

func scanDockerVolumeChildren(parentID string, selected bool) []Target {
	names := dockerDanglingVolumeNames()
	children := make([]Target, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		children = append(children, Target{
			ID:         "docker://volume/" + name,
			ParentID:   parentID,
			Path:       "docker://volume/" + name,
			Category:   CategoryDocker,
			Label:      name,
			Info:       "unused volume",
			Selected:   selected,
			DeleteMode: DeleteModeCommand,
			Command:    []string{"docker", "volume", "rm", name},
		})
	}
	return children
}

func scanDockerImageChildren(parentID string, selected bool) ([]Target, error) {
	rows, err := dockerImageRows()
	if err != nil {
		return nil, err
	}

	used, err := dockerUsedImageIDs()
	if err != nil {
		return nil, err
	}
	type imageGroup struct {
		size int64
		tags []string
	}
	grouped := make(map[string]*imageGroup)
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		if id == "" || used[id] {
			continue
		}

		group := grouped[id]
		if group == nil {
			group = &imageGroup{}
			grouped[id] = group
		}

		size := parseHumanBytesField(row.Size)
		if size > group.size {
			group.size = size
		}

		tag := dockerImageTag(row.Repository, row.Tag)
		if tag != "" {
			group.tags = appendUnique(group.tags, tag)
		}
	}

	var children []Target
	for id, group := range grouped {
		label := shortDockerID(id)
		info := "unused image  re-pull may be needed"
		if len(group.tags) > 0 {
			label = group.tags[0]
			if len(group.tags) > 1 {
				info = fmt.Sprintf("%d tags  re-pull may be needed", len(group.tags))
			} else {
				info = "re-pull may be needed"
			}
		}

		children = append(children, Target{
			ID:         "docker://image/" + id,
			ParentID:   parentID,
			Path:       "docker://image/" + id,
			Category:   CategoryDocker,
			Label:      label,
			Size:       group.size,
			Info:       info,
			Selected:   selected,
			DeleteMode: DeleteModeCommand,
			Command:    []string{"docker", "image", "rm", id},
		})
	}
	return children, nil
}

func dockerInfoTarget(label, info string) Target {
	return Target{
		Path:     "docker://info/" + strings.ToLower(strings.ReplaceAll(label, " ", "-")),
		Category: CategoryDocker,
		Label:    label,
		Info:     info,
		Locked:   true,
	}
}

func scanRuntimeData(threshold int64) []Target {
	var targets []Target
	targets = append(targets, scanPathDefs(fixedPathDefs, threshold, func(def pathDef) bool {
		return def.category == CategoryPackageManager
	})...)
	targets = append(targets, scanChildDefs(childScanDefs, func(def childScanDef) bool {
		return def.category == CategoryToolchains
	}, threshold)...)
	return targets
}

func scanMobileData(threshold int64) []Target {
	return scanPathDefs(fixedPathDefs, threshold, func(def pathDef) bool {
		return def.category == CategoryMobile
	})
}

func scanUnusedApps(days int) []Target {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	threshold := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	roots := []string{"/Applications", filepath.Join(home, "Applications")}
	appPaths := findAppBundles(roots)
	if len(appPaths) == 0 {
		return nil
	}

	out := make(chan []Target, len(appPaths))
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup

	for _, appPath := range appPaths {
		appPath := appPath
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			targets := buildUnusedAppTargets(appPath, threshold)
			if len(targets) > 0 {
				out <- targets
			}
		}()
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	var targets []Target
	for group := range out {
		targets = append(targets, group...)
	}
	return targets
}

func findAppBundles(roots []string) []string {
	seen := make(map[string]bool)
	var apps []string

	for _, root := range roots {
		root = expandPath(root)
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}

		rootDepth := strings.Count(filepath.Clean(root), string(os.PathSeparator))
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return fs.SkipDir
			}
			if !d.IsDir() {
				return nil
			}
			if path != root {
				name := d.Name()
				if strings.HasPrefix(name, ".") {
					return fs.SkipDir
				}
				if strings.HasSuffix(name, ".app") {
					if !seen[path] {
						seen[path] = true
						apps = append(apps, path)
					}
					return fs.SkipDir
				}
			}

			depth := strings.Count(filepath.Clean(path), string(os.PathSeparator)) - rootDepth
			if depth > 3 {
				return fs.SkipDir
			}
			return nil
		})
	}

	return apps
}

func buildUnusedAppTargets(appPath string, threshold time.Time) []Target {
	appName := strings.TrimSuffix(filepath.Base(appPath), ".app")
	bundleID := appBundleID(appPath)
	lastActive, activityLabel, known := appActivity(appPath, appName, bundleID)
	if !known || !lastActive.Before(threshold) {
		return nil
	}

	groupID := "app://group/" + appPath
	children := scanAppRelatedTargets(groupID, appPath, appName, bundleID)
	if len(children) == 0 {
		return nil
	}

	var total int64
	for _, child := range children {
		total += child.Size
	}

	group := Target{
		ID:         groupID,
		Path:       groupID,
		Category:   CategoryUnusedApps,
		Label:      appName,
		Size:       total,
		LastMod:    lastActive,
		Info:       activityLabel + " " + timeAgo(lastActive),
		Group:      true,
		Expandable: true,
	}

	return append([]Target{group}, children...)
}

func scanAppRelatedTargets(parentID, appPath, appName, bundleID string) []Target {
	var defs []appRelatedPathDef

	defs = append(defs, appRelatedPathDef{
		label:    "App bundle",
		path:     appPath,
		selected: true,
		info:     "move to Trash",
	})

	for _, name := range uniqueNonEmptyStrings(bundleID, appName) {
		defs = append(defs,
			appRelatedPathDef{label: "Cache data: " + name, path: filepath.Join("~/Library/Caches", name), selected: true, info: "safe cleanup"},
			appRelatedPathDef{label: "Logs: " + name, path: filepath.Join("~/Library/Logs", name), selected: true, info: "safe cleanup"},
			appRelatedPathDef{label: "Application Support: " + name, path: filepath.Join("~/Library/Application Support", name), info: "optional app data"},
		)
	}

	if bundleID != "" {
		defs = append(defs,
			appRelatedPathDef{label: "Preferences", path: filepath.Join("~/Library/Preferences", bundleID+".plist"), info: "settings"},
			appRelatedPathDef{label: "Containers", path: filepath.Join("~/Library/Containers", bundleID), info: "sandbox data"},
		)
	}

	var children []Target
	seen := make(map[string]bool)
	for _, def := range defs {
		path := expandPath(def.path)
		if seen[path] {
			continue
		}
		seen[path] = true

		target, ok := targetFromPath(path, CategoryUnusedApps, def.label, def.selected, false, 0)
		if !ok {
			continue
		}

		target.ParentID = parentID
		target.Info = def.info
		if path == appPath {
			target.DeleteMode = DeleteModeTrash
		}
		children = append(children, target)
	}

	return children
}

func appActivity(appPath, appName, bundleID string) (time.Time, string, bool) {
	if t, ok := appLastUsed(appPath); ok {
		return t, "last used", true
	}
	if t, ok := appFallbackActivity(appName, bundleID); ok {
		return t, "last active", true
	}
	return time.Time{}, "", false
}

func appLastUsed(appPath string) (time.Time, bool) {
	raw := mdlsRaw("kMDItemLastUsedDate", appPath)
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "(null)" {
		return time.Time{}, false
	}

	t, err := time.Parse("2006-01-02 15:04:05 -0700", raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func appFallbackActivity(appName, bundleID string) (time.Time, bool) {
	var latest time.Time
	for _, path := range appActivityPaths(appName, bundleID) {
		if t, ok := pathActivityTime(expandPath(path)); ok && t.After(latest) {
			latest = t
		}
	}
	if latest.IsZero() {
		return time.Time{}, false
	}
	return latest, true
}

func appActivityPaths(appName, bundleID string) []string {
	var paths []string
	for _, name := range uniqueNonEmptyStrings(bundleID, appName) {
		paths = append(paths,
			filepath.Join("~/Library/Caches", name),
			filepath.Join("~/Library/Logs", name),
			filepath.Join("~/Library/Application Support", name),
		)
	}
	if bundleID != "" {
		paths = append(paths,
			filepath.Join("~/Library/Preferences", bundleID+".plist"),
			filepath.Join("~/Library/Saved Application State", bundleID+".savedState"),
			filepath.Join("~/Library/Containers", bundleID),
		)
	}
	return uniqueNonEmptyStrings(paths...)
}

func pathActivityTime(path string) (time.Time, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}

	latest := info.ModTime()
	if !info.IsDir() {
		return latest, true
	}

	rootDepth := strings.Count(filepath.Clean(path), string(os.PathSeparator))
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}

		depth := strings.Count(filepath.Clean(p), string(os.PathSeparator)) - rootDepth
		if depth > 2 && d.IsDir() {
			return fs.SkipDir
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})

	return latest, true
}

func appBundleID(appPath string) string {
	raw := mdlsRaw("kMDItemCFBundleIdentifier", appPath)
	raw = strings.TrimSpace(raw)
	if raw == "(null)" {
		return ""
	}
	return raw
}

func mdlsRaw(name, path string) string {
	out, err := exec.Command("mdls", "-raw", "-name", name, path).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func uniqueNonEmptyStrings(values ...string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func scanAppData(threshold int64) []Target {
	return scanChildDefs(childScanDefs, func(def childScanDef) bool {
		return def.category == CategoryAppData
	}, threshold)
}

func scanUserData(threshold int64) []Target {
	targets := scanPathDefs(fixedPathDefs, threshold, func(def pathDef) bool {
		return def.category == CategoryUserData
	})
	targets = append(targets, scanChildDefs(childScanDefs, func(def childScanDef) bool {
		return def.category == CategoryUserData
	}, threshold)...)
	return targets
}

func scanSystemHotspots(threshold int64) []Target {
	targets := scanPathDefs(fixedPathDefs, threshold, func(def pathDef) bool {
		return def.category == CategorySystemHotspots
	})
	targets = append(targets, scanTimeMachineSnapshots()...)
	return targets
}

func scanGenericCaches(threshold int64) []Target {
	return scanChildDefs(childScanDefs, func(def childScanDef) bool {
		return def.category == CategorySystemCache
	}, threshold)
}

func scanPathDefs(defs []pathDef, minSize int64, include func(pathDef) bool) []Target {
	var targets []Target
	for _, def := range defs {
		if include != nil && !include(def) {
			continue
		}
		target, ok := targetFromPath(expandPath(def.path), def.category, def.label, def.safe, def.locked, minSize)
		if ok {
			targets = append(targets, target)
		}
	}
	return targets
}

func scanChildDefs(defs []childScanDef, include func(childScanDef) bool, threshold int64) []Target {
	var targets []Target
	for _, def := range defs {
		if include != nil && !include(def) {
			continue
		}
		minSize := def.minSize
		if threshold > minSize {
			minSize = threshold
		}
		targets = append(targets, scanHeavyChildren(expandPath(def.base), def.category, def.label, minSize, def.safe, def.locked, def.dirsOnly)...)
	}
	return targets
}

func scanHeavyChildren(root string, category Category, label string, minSize int64, safe, locked, dirsOnly bool) []Target {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	results := make([]Target, 0, len(entries))
	out := make(chan Target, len(entries))
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup

	for _, entry := range entries {
		entry := entry
		path := filepath.Join(root, entry.Name())
		if dirsOnly && !entry.IsDir() {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			target, ok := targetFromPath(path, category, fmt.Sprintf("%s: %s", label, entry.Name()), safe, locked, minSize)
			if ok {
				out <- target
			}
		}()
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	for target := range out {
		results = append(results, target)
	}

	return results
}

func targetFromPath(path string, category Category, label string, safe, locked bool, minSize int64) (Target, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return Target{}, false
	}

	size := info.Size()
	if info.IsDir() {
		size = dirSize(path)
	}
	if size < minSize {
		return Target{}, false
	}

	return Target{
		Path:     path,
		Category: category,
		Label:    label,
		Size:     size,
		LastMod:  info.ModTime(),
		Selected: safe && !locked,
		Locked:   locked,
	}, true
}

func scanTimeMachineSnapshots() []Target {
	out, err := exec.Command("tmutil", "listlocalsnapshots", "/").Output()
	if err != nil {
		return nil
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	count := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	if count == 0 {
		return nil
	}

	return []Target{{
		Path:     "/",
		Category: CategorySystemHotspots,
		Label:    "Time Machine local snapshots",
		Info:     fmt.Sprintf("%d snapshots", count),
		Locked:   true,
	}}
}

// scanNodeModules finds stale JS/TS projects and collects their node_modules.
func scanNodeModules(scanRoot string, staleDays int) []Target {
	roots := findProjectRoots(scanRoot)
	if len(roots) == 0 {
		return nil
	}

	threshold := time.Now().Add(-time.Duration(staleDays) * 24 * time.Hour)

	type result struct {
		targets []Target
	}

	ch := make(chan result, len(roots))
	sem := make(chan struct{}, runtime.NumCPU())

	for _, root := range roots {
		root := root
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()

			if !fileExists(filepath.Join(root, "package.json")) {
				ch <- result{}
				return
			}

			lastMod, stale := checkStaleness(root, threshold)
			if !stale {
				ch <- result{}
				return
			}

			nmDirs := findNodeModules(root)
			var targets []Target
			for _, nm := range nmDirs {
				size := dirSize(nm)
				if size < 1<<20 { // skip < 1 MB
					continue
				}
				targets = append(targets, Target{
					Path:     nm,
					Category: CategoryNodeModules,
					Label:    shortenHome(nm),
					Size:     size,
					LastMod:  lastMod,
					Selected: true,
				})
			}
			ch <- result{targets}
		}()
	}

	var all []Target
	for range roots {
		r := <-ch
		all = append(all, r.targets...)
	}
	return all
}

// findProjectRoots locates project root directories under scanRoot.
// A project root is identified by having .git + package.json, or being a
// direct child of scanRoot with package.json.
func findProjectRoots(scanRoot string) []string {
	var roots []string

	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > 10 {
			return
		}

		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}

		var hasGit, hasPkgJSON bool
		for _, e := range entries {
			if e.IsDir() && e.Name() == ".git" {
				hasGit = true
			}
			if !e.IsDir() && e.Name() == "package.json" {
				hasPkgJSON = true
			}
		}

		// Git repo with package.json → JS/TS project root
		if hasGit && hasPkgJSON {
			roots = append(roots, dir)
			return
		}
		// Direct child of scan root with package.json (no git)
		if depth == 1 && hasPkgJSON {
			roots = append(roots, dir)
			return
		}
		// Git repo without package.json → not a JS project, stop
		if hasGit {
			return
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if len(name) > 0 && name[0] == '.' {
				continue
			}
			if name == "node_modules" {
				continue
			}
			walk(filepath.Join(dir, name), depth+1)
		}
	}

	walk(scanRoot, 0)
	return roots
}

// Directories to ignore when checking if a project has been modified.
var staleDirSkip = map[string]bool{
	"node_modules": true, ".git": true, "dist": true, "build": true,
	".next": true, ".nuxt": true, ".output": true, ".cache": true,
	".turbo": true, ".parcel-cache": true, "coverage": true,
	".nyc_output": true, ".svelte-kit": true, ".vercel": true,
	".expo": true, ".angular": true, ".vite": true, ".docusaurus": true,
	"__pycache__": true, ".pytest_cache": true, ".mypy_cache": true,
	".sass-cache": true, ".webpack": true,
}

var staleFileSkip = map[string]bool{
	".DS_Store": true, "Thumbs.db": true, ".gitkeep": true,
}

// checkStaleness walks a project tree and returns the most recent file
// modification time and whether ALL source files are older than threshold.
func checkStaleness(root string, threshold time.Time) (time.Time, bool) {
	var mostRecent time.Time
	stale := true

	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		if d.IsDir() {
			if staleDirSkip[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if staleFileSkip[d.Name()] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		mt := info.ModTime()
		if mt.After(mostRecent) {
			mostRecent = mt
		}
		if mt.After(threshold) {
			stale = false
			return fs.SkipAll
		}
		return nil
	})

	return mostRecent, stale
}

// findNodeModules returns all node_modules directories within a project root.
func findNodeModules(root string) []string {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		if !d.IsDir() {
			return nil
		}
		switch d.Name() {
		case "node_modules":
			dirs = append(dirs, path)
			return fs.SkipDir
		case ".git":
			return fs.SkipDir
		}
		return nil
	})
	return dirs
}

// dirSize calculates the total size of all files in a directory tree.
func dirSize(path string) int64 {
	var size int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size
}

// scanCaches checks known cache locations on disk.
func scanCaches() []Target {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	type cacheResult struct {
		target Target
		found  bool
	}

	results := make([]cacheResult, len(cacheDefs))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())

	for i, def := range cacheDefs {
		i, def := i, def
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			full := filepath.Join(home, def.relPath)
			info, err := os.Stat(full)
			if err != nil || !info.IsDir() {
				return
			}
			size := dirSize(full)
			if size < 1<<20 { // skip < 1 MB
				return
			}
			results[i] = cacheResult{
				found: true,
				target: Target{
					Path:     full,
					Category: def.category,
					Label:    def.label,
					Size:     size,
					Selected: def.safe,
				},
			}
		}()
	}

	wg.Wait()

	var targets []Target
	for _, r := range results {
		if r.found {
			targets = append(targets, r.target)
		}
	}
	return targets
}

func dockerSystemDF() ([]dockerSummaryRow, error) {
	out, err := exec.Command("docker", "system", "df", "--format", "{{json .}}").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		msgLower := strings.ToLower(msg)
		switch {
		case strings.Contains(msgLower, "cannot connect to the docker daemon"),
			strings.Contains(msgLower, "is the docker daemon running"),
			strings.Contains(msgLower, "docker daemon"),
			strings.Contains(msgLower, "docker desktop is not running"),
			strings.Contains(msgLower, "context deadline exceeded"):
			return nil, fmt.Errorf("daemon unreachable")
		default:
			return nil, fmt.Errorf("docker unavailable: %s", msg)
		}
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	rows := make([]dockerSummaryRow, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var row dockerSummaryRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func dockerContainerRows() ([]dockerContainerRow, error) {
	out, err := exec.Command(
		"docker", "ps", "-a", "--size", "--no-trunc",
		"--filter", "status=exited",
		"--filter", "status=created",
		"--filter", "status=dead",
		"--format", "{{json .}}",
	).CombinedOutput()
	if err != nil {
		return nil, err
	}
	return parseDockerJSONLines[dockerContainerRow](string(out))
}

func dockerImageRows() ([]dockerImageRow, error) {
	out, err := exec.Command(
		"docker", "image", "ls", "-a", "--no-trunc",
		"--format", "{{json .}}",
	).CombinedOutput()
	if err != nil {
		return nil, err
	}
	return parseDockerJSONLines[dockerImageRow](string(out))
}

func dockerDanglingVolumeNames() []string {
	out := runCommandOutput("docker", "volume", "ls", "-q", "-f", "dangling=true")
	if strings.TrimSpace(out) == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	names := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names
}

func dockerUsedImageIDs() (map[string]bool, error) {
	out, err := exec.Command("docker", "ps", "-a", "-q", "--no-trunc").CombinedOutput()
	if err != nil {
		return nil, err
	}
	raw := string(out)
	if strings.TrimSpace(raw) == "" {
		return map[string]bool{}, nil
	}

	containerIDs := strings.Split(strings.TrimSpace(raw), "\n")
	args := append([]string{"inspect", "--format", "{{.Image}}"}, containerIDs...)
	inspectOut, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return nil, err
	}
	used := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(inspectOut)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			used[line] = true
		}
	}
	return used, nil
}

func parseDockerContainerSize(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if idx := strings.Index(raw, "("); idx >= 0 {
		raw = strings.TrimSpace(raw[:idx])
	}
	return parseHumanBytesField(raw)
}

func dockerImageTag(repo, tag string) string {
	repo = strings.TrimSpace(repo)
	tag = strings.TrimSpace(tag)
	if repo == "" || repo == "<none>" {
		return ""
	}
	if tag == "" || tag == "<none>" {
		return repo
	}
	return repo + ":" + tag
}

func shortDockerID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func appendUnique(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func parseDockerJSONLines[T any](raw string) ([]T, error) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	rows := make([]T, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var row T
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseHumanBytesField(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	raw = strings.Fields(raw)[0]
	if raw == "0B" || raw == "0" {
		return 0
	}

	i := 0
	for i < len(raw) && ((raw[i] >= '0' && raw[i] <= '9') || raw[i] == '.') {
		i++
	}
	if i == 0 {
		return 0
	}

	value, err := strconv.ParseFloat(raw[:i], 64)
	if err != nil {
		return 0
	}

	unit := strings.ToUpper(strings.TrimSpace(raw[i:]))
	multiplier := float64(1)
	switch unit {
	case "B", "":
		multiplier = 1
	case "KB", "KIB":
		multiplier = 1 << 10
	case "MB", "MIB":
		multiplier = 1 << 20
	case "GB", "GIB":
		multiplier = 1 << 30
	case "TB", "TIB":
		multiplier = 1 << 40
	case "PB", "PIB":
		multiplier = 1 << 50
	default:
		return 0
	}

	return int64(value * multiplier)
}

func runCommandOutput(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func countLines(s string) int {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(s), "\n"))
}

func pluralize(n int, singular string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %ss", n, singular)
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// deleteTarget removes a target from disk.
// For .Trash it clears contents only; for everything else it removes the dir.
func deleteTarget(target Target) error {
	switch target.DeleteMode {
	case DeleteModeCommand:
		if len(target.Command) == 0 {
			return fmt.Errorf("missing command for %s", target.Label)
		}
		out, err := exec.Command(target.Command[0], target.Command[1:]...).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				return fmt.Errorf("%v: %s", err, msg)
			}
			return err
		}
		return nil
	case DeleteModeTrash:
		return moveToTrash(target.Path)
	default:
		if filepath.Base(target.Path) == ".Trash" {
			return deleteContents(target.Path)
		}
		return os.RemoveAll(target.Path)
	}
}

func moveToTrash(path string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	trashDir := filepath.Join(home, ".Trash")
	if err := os.MkdirAll(trashDir, 0o755); err != nil {
		return err
	}

	base := filepath.Base(path)
	dest := filepath.Join(trashDir, base)
	if fileExists(dest) {
		dest = filepath.Join(trashDir, fmt.Sprintf("%s-%d", base, time.Now().Unix()))
	}

	return os.Rename(path, dest)
}

func deleteContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var firstErr error
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func dedupeTargets(targets []Target) []Target {
	seen := make(map[string]int, len(targets))
	out := make([]Target, 0, len(targets))

	for _, target := range targets {
		key := targetKey(target)
		if idx, ok := seen[key]; ok {
			existing := &out[idx]
			if existing.Label == shortenHome(existing.Path) && target.Label != existing.Label {
				existing.Label = target.Label
			}
			if target.Size > existing.Size {
				existing.Size = target.Size
			}
			if target.LastMod.After(existing.LastMod) {
				existing.LastMod = target.LastMod
			}
			if existing.Info == "" {
				existing.Info = target.Info
			}
			existing.Selected = existing.Selected || target.Selected
			existing.Locked = existing.Locked && target.Locked
			existing.Group = existing.Group || target.Group
			existing.Expandable = existing.Expandable || target.Expandable
			existing.Expanded = existing.Expanded || target.Expanded
			if existing.ID == "" {
				existing.ID = target.ID
			}
			if existing.ParentID == "" {
				existing.ParentID = target.ParentID
			}
			if existing.DeleteMode == DeleteModePath && target.DeleteMode != DeleteModePath {
				existing.DeleteMode = target.DeleteMode
				existing.Command = target.Command
			}
			continue
		}

		seen[key] = len(out)
		out = append(out, target)
	}

	return out
}

func targetKey(target Target) string {
	if target.ID != "" {
		return target.ID
	}
	if target.Path != "" {
		return target.Path
	}
	return target.Label
}

func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

func humanBytes(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	fb := float64(b)
	for _, unit := range []string{"KB", "MB", "GB", "TB"} {
		fb /= 1024
		if fb < 1024 {
			if fb >= 100 {
				return fmt.Sprintf("%.0f %s", fb, unit)
			}
			if fb >= 10 {
				return fmt.Sprintf("%.1f %s", fb, unit)
			}
			return fmt.Sprintf("%.2f %s", fb, unit)
		}
	}
	return fmt.Sprintf("%.2f PB", fb/1024)
}

func timeAgo(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	days := int(time.Since(t).Hours() / 24)
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "1 day ago"
	case days < 30:
		return fmt.Sprintf("%d days ago", days)
	case days < 365:
		m := days / 30
		if m == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", m)
	default:
		y := days / 365
		if y == 1 {
			return "1 year ago"
		}
		return fmt.Sprintf("%d years ago", y)
	}
}

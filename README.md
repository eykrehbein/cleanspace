# cleanspace

A small terminal tool that finds what's eating your disk — stale `node_modules`, forgotten build artifacts, bloated Docker volumes, old toolchain versions — and lets you clean it up with a keypress.

Primarily for macOS. Most of the paths it knows about live under `~/Library`, Xcode, and the various package manager caches a dev machine accumulates. It runs on Linux too, but most categories will turn up empty.

## Install

Grab a prebuilt binary (macOS and Linux, amd64 and arm64):

```sh
curl -fsSL https://raw.githubusercontent.com/eykrehbein/cleanspace/main/install.sh | sh
```

The script downloads the latest release from GitHub and drops a single binary into `~/.local/bin`. Set `CLEANSPACE_INSTALL_DIR` to install somewhere else.

Prefer to download by hand? Releases are on [the releases page](https://github.com/eykrehbein/cleanspace/releases) — grab the tarball for your platform, extract it, and put `cleanspace` on your `PATH`.

Or build from source (requires Go 1.22+):

```sh
git clone https://github.com/eykrehbein/cleanspace
cd cleanspace
go build -o cleanspace
```

## Usage

```sh
cleanspace                       # scan your home directory
cleanspace ~/code                # scan a specific directory
cleanspace --days 30 --size 500  # only 30-day-stale projects, files >500 MB
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-d`, `--dir` | `~` | Directory to scan |
| `--days` | `7` | Days of inactivity before a project counts as stale |
| `--size` | `100` | Minimum file size in MB to flag as a large file |

Arrow keys (or `hjkl`) to move, `space` to toggle, `a` select all, `n` none, `enter` to delete, `q` to quit. Nothing is deleted without a confirmation prompt.

## What it looks for

- Stale projects with a large `node_modules`
- Build output: `target`, `dist`, `.next`, `.turbo`, Xcode DerivedData, Go build cache…
- Package manager caches: npm, yarn, pnpm, bun, pip, Homebrew, Cargo, Gradle, Maven, CocoaPods…
- Docker images, containers, and build cache
- Toolchain versions from nvm, pyenv, rustup, rbenv, sdkman, conda
- iOS simulators and Xcode archives
- App data, system caches, logs, the Trash
- Plain large files anywhere under the scan root

Categories that are expensive to rebuild (Cargo registry, Gradle, Maven, Docker) are surfaced but **off by default**. You opt in before anything there is touched.

## Safety

Nothing is deleted until you confirm. Review the selection before pressing enter. There is no undo.

A few sensitive paths (Mail, Messages, system logs, Docker disk image) are locked by default — you have to explicitly unlock them before they can be selected. If you're unsure what something is, leave it alone.

## Contributing

Issues and PRs welcome. New cache paths and categories are a few lines at the top of `scanner.go` — look for `cacheDefs`, `fixedPathDefs`, and `childScanDefs`.

## License

MIT

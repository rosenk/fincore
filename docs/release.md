# Releases

A release is a GitHub Release of an annotated tag `vX.Y.Z` with one
archive per platform and a `SHA256SUMS` file. Users download the
compiler and do not have to run the bootstrap themselves.

## Archive layout

`fincore-<version>-<os>-<arch>.tar.gz` (version without the leading `v`,
e.g. `fincore-0.3.0-linux-amd64.tar.gz`) unpacks into one directory:

```text
fincore/
  bin/finc                 the self-hosted compiler: finc2 of a fresh `make bootstrap`
  go.mod                   `module fincore`: the Go module the generated code builds against
  runtime/fincrt/*.go      the runtime sources (without tests)
  std/<pkg>/*.fc           the standard library sources
  examples/tcp_echo.fc
  docs/                    language spec, v0_2.md, stdlib.md, runtime_api.md,
                           semantics_decisions.md, release.md
  VERSION                  the release tag; marks the directory as an installed release
  README.md, CHANGELOG.md  (and LICENSE/NOTICE when the tree has one)
```

`finc` needs a Go toolchain on `PATH` at run time (the version in
`go.mod` or newer): it runs `go build` and gofmt (`go run cmd/gofmt`) on
the generated code. `FINC_GO` / `FINC_GOFMT` name other binaries.

## How finc finds the runtime and std/

- **Runtime (the fincore Go module).** `$FINCORE_ROOT` if set; else the
  first directory at or above `$PWD`, then at or above the running
  executable, then above `Arg(0)` when it is a path, whose `go.mod` says
  `module fincore`. The executable comes from
  `Env.GetEnv("FINCRT_EXECUTABLE")`, which the runtime answers itself with
  the symlink-resolved `os.Executable()` (docs/runtime_api.md): no new
  capability method, so compiler/ can use it while being compiled by the
  v0.2.0 seed, and seed-generated code keeps working. For an installed
  release, `fincore/bin/finc` walks up to `fincore/go.mod`, also when it is
  started through a symlink (e.g. `/usr/local/bin/finc`).
- **std/.** `$FINCROOT/std/...` if `FINCROOT` is set and non-empty; else
  `./std/...` when the working directory has a `std/` directory (a
  fincore checkout); else `<root>/std/...` with the module root above,
  if that root is an installed release, marked by its `VERSION` file
  (docs/stdlib.md, rule 3). The source tree has no `VERSION`, so std/
  resolution and diagnostics of a checkout are unchanged (finc0's).

`FINCORE_ROOT` and `FINCROOT` keep overriding the discovery. Inside a
fincore checkout the checkout wins over the installed directory, as
before.

Compilers released before this mechanism (v0.2.0) cannot locate
themselves. Their archive holds the compiler as `libexec/finc` and a
POSIX sh launcher `bin/finc` that sets `FINCROOT` (if unset) to the
install directory and runs `libexec/finc` by absolute path, from which
it finds `go.mod`. `scripts/dist` chooses this when
`compiler/build.fc` of the packaged tree does not mention
`FINCRT_EXECUTABLE`.

## Building and testing an archive

```sh
make bootstrap        # the full gate (optional before dist)
make dist             # fresh bootstrap (-skip-integration), then dist/fincore-<version>-<os>-<arch>.tar.gz
make dist-test        # smoke test of that archive
```

`VERSION` defaults to the exact tag of `HEAD`, else `git describe --tags
--always --dirty`; override with `make dist VERSION=v0.3.0`. The tool is
`scripts/dist` (`go run ./scripts/dist -h`).

`make dist-test` unpacks the archive with `tar` into a temporary
directory, puts its `bin/` first on `PATH` (then the Go toolchain's
`bin/`), removes `FINCROOT`, `FINCORE_ROOT`, `FINC_GO` and `FINC_GOFMT`,
and from a different temporary project directory: builds and runs a hello
program that imports `std/strconv` and uses `formatTo`, runs it with `finc
run` (also through a symlink to `bin/finc`), builds
`examples/tcp_echo.fc`, and runs `finc test` on copies of
`pass_std_strconv`, `pass_extra_format` and `fail_conn_leak`.

## Reproducibility

Archives are reproducible: building the same commit with the same Go
toolchain on the same platform gives a byte-identical `.tar.gz`.

- The compiler binary is: the bootstrap reaches a fixed point, every stage
  builds with `--trimpath` and `CGO_ENABLED=0`, and the Go build is
  deterministic for a fixed toolchain.
- The archive is written by `scripts/dist` (Go `archive/tar` +
  `compress/gzip`), not by the platform's `tar`: entries sorted by name,
  directories listed explicitly, every mtime `$SOURCE_DATE_EPOCH` (default:
  the commit time of `HEAD`), uid/gid 0 with no user or group names, modes
  0755 (directories, `bin/finc`) or 0644, and no name or time in the gzip
  header.

`setup-go` installs the latest patch release of the `go.mod` version, so
two workflow runs months apart may use different toolchains and produce
different binaries; set the same toolchain to reproduce a published
archive and compare it with `SHA256SUMS`.

## The release workflow

`.github/workflows/release.yml` runs on a pushed tag `v*` and on
`workflow_dispatch` with a `tag` input (an existing tag). For each of
linux/amd64 (ubuntu-latest), linux/arm64 (ubuntu-24.04-arm),
darwin/arm64 (macos-latest) and, best effort, darwin/amd64
(macos-15-intel), it checks out the tag with full history, sets up Go from
`go.mod`, runs `make bootstrap`, `make dist`, `make dist-test` and
uploads the archive. A tag that predates `make dist` (v0.2.0) is built
and bootstrapped from the tag and packaged with `scripts/dist` from the
commit the workflow runs on. The last job writes `SHA256SUMS` and creates
the release (or uploads to an existing one) with `gh`; the notes are the
tag's `## vX.Y.Z` section of `CHANGELOG.md` (from the workflow's commit),
else the tag annotation.

Cutting a release:

1. Add a `## vX.Y.Z` section to CHANGELOG.md and merge it.
2. `git tag -a vX.Y.Z -m "..." && git push origin vX.Y.Z`.

Releasing an existing tag: Actions → Release → Run workflow, on `main`,
with `tag` = `v0.2.0`.

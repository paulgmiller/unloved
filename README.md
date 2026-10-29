# unloved

Find Go declarations in `internal` packages that are exported but never referenced
by another package in the module. Keep implementation details private and the
package API small. Explicit package patterns let you check other packages too.

## Why use it with coding agents?

Coding agents can introduce exported helpers, types, constants, and variables
while solving a local problem. Those exports expand the API that future changes
must consider, even when no other package needs them. Running `unloved` during
review or in CI helps agents spend their complexity budget on behavior callers
actually use, and keeps accidental exports from becoming compatibility promises
that they later have to support.

Use it alongside a statement or complexity budget: a smaller API reduces the
surface an agent must understand and maintain, while the budget tracks the cost
of the implementation. `unloved` checks visibility; it does not measure complexity
or establish whether a public library has consumers outside the module.

## Prior art

Several tools address related questions about exported Go identifiers:

- [bep/punused](https://github.com/bep/punused) uses `gopls` to find exported
  symbols with no references in the project. It documents limitations around
  external consumers, reflection, and interfaces. `unloved` focuses on the
  different case where a symbol is used, but only inside its own package.
- [GoLand's “Unnecessarily exported identifier” inspection](https://www.jetbrains.com/help/inspectopedia/GoUnnecessarilyExportedIdentifiers.html)
  reports exported identifiers used only in their defining package. The
  inspection is available in GoLand and Qodana; `unloved` provides a standalone
  command-line check that can run with Go tooling in CI.
- [gounexport](https://github.com/dooman87/gounexport) is an earlier command-line
  tool for finding and renaming exported symbols that are not used outside a
  package. `unloved` is a separate implementation built around current Go package
  loading and type information.
- [Staticcheck issue #288](https://github.com/dominikh/go-tools/issues/288)
  discusses why detecting unused struct fields is difficult: reflection, cgo,
  and syscalls can make writes meaningful without visible Go reads. The discussion
  also notes that finding unused exported APIs needs a distinct analysis mode.
  `unloved` checks whether exported declarations are used across package
  boundaries; it deliberately does not judge struct fields.

## Install and run

```sh
go install github.com/paulgmiller/unloved@latest
unloved -dir /path/to/module
unloved -dir /path/to/module -fix
# Explicitly opt into other packages (patterns are relative to -dir):
unloved -dir /path/to/module ./api ./pkg/...
# Explicitly opt into the entire module:
unloved -dir /path/to/module -fix ./...
```

From a checkout, use `go run .` or `go install .`. Review fixes and run your normal
tests before committing them.

For a reproducible CI check, pin a release or commit using Go's tool dependencies:

```sh
go get -tool github.com/paulgmiller/unloved@<version>
go tool unloved
```

The check exits with status 1 for findings, 2 for loading or fixing errors, and 0
when clean (or after applying fixes). It loads and type-checks `./...`, including
tests, through `golang.org/x/tools/go/packages`. The target needs working Go
module dependencies. No language server or network service is required beyond
normal Go dependency downloads.

## What is checked

By default, only packages whose directory path within the module contains an
`internal` component are candidates, including nested `internal` directories.
Public packages remain unchanged. Positional Go package patterns override this
default and select exactly those packages. The tool still analyzes the whole
module for cross-package consumers, even when explicit patterns select fewer
packages.

Package-level types (including interfaces), functions, concrete methods,
constants, and variables. References use Go type information, including generic
instantiations, aliases, and test package
variants. Same-package tests do not justify an export; external test packages
do. Fixes update references in same-package tests too.

Struct fields are outside the check's scope. They keep their names and
visibility, including untagged fields consumed by JSON, YAML, templates, or
other reflection. Embedded types also remain exported
because renaming them would change the name of their implicit struct field.

Interface methods and concrete methods that satisfy interfaces remain exported,
including promoted methods from embedded types. Generated declarations are
excluded; references in generated files also keep the referenced declarations
unchanged.

## Dynamic entry points

Static references cannot capture template method lookups, plugin symbols, or
other reflection. Document these explicitly, with a required reason:

```go
//unexport:ignore loaded as a plugin symbol
func EntryPoint() {}
```

The directive applies to its type, function, method, constant, or variable
declaration. Struct fields never need annotations.

## Fixes and limits

`-fix` uses type-resolved references and lowers the initial acronym (`HTTPClient`
becomes `httpClient`). It rejects keywords, existing package/member names, and
local shadowing before writing any files. Resolve conflicting names manually
and rerun. It formats replacements and updates leading doc-comment identifiers.
Review the diff and run your normal tests after fixing: dynamic entry points
must be annotated by the caller, and unexporting wholly unused declarations may
expose dead code to Staticcheck.

Analysis covers the active build configuration only. Use `-tags` and the normal
`GOOS`/`GOARCH` environment for other build configurations. Run the check
separately for each module; nested modules are not loaded by `./...`. The tool
cannot detect consumers outside the analyzed module. The default `internal`
scope leaves public packages unchanged, but does not prove all consumers were
loaded. Go allows an `internal` package to be imported from within its parent
directory tree, which can include other modules. Use the tool on applications
or internal packages whose consumers are all included in the analysis.
Explicitly selecting public packages can break dependent modules and requires
knowing those exports are safe to rename.

## Development

```sh
go test ./...
go vet ./...
```

Tests exercise cross-package use, same-package and external tests, interfaces,
promoted methods, generics, generated code, dynamic entry point exceptions,
field preservation, build tags, load failures, name collisions, and fix
idempotence.

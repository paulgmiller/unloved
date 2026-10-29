# go-unexport

Find Go declarations that are exported but never referenced by another package
in the module. This is a standalone command and module: copy this directory to
its own repository without any Careme source or dependencies.

From this directory:

```sh
go install .
go-unexport -dir /path/to/module
go-unexport -dir /path/to/module -fix
```

From the Careme repository:

```sh
./task.sh unexport
./task.sh unexport-fix
```

The check exits with status 1 for findings, 2 for loading or fixing errors, and 0
when clean (or after applying fixes). It loads and type-checks `./...`, including
tests, through `golang.org/x/tools/go/packages`. The target needs working Go
module dependencies. No language server or network service is required beyond
normal Go dependency downloads.

## What is checked

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
cannot detect consumers outside the analyzed module, so use it on applications
or internal packages, rather than assuming a public library's exports are safe
to rename.

## Development

```sh
go test ./...
go vet ./...
```

Tests exercise cross-package use, same-package and external tests, interfaces,
promoted methods, generics, generated code, dynamic entry point exceptions,
field preservation, build tags, load failures, name collisions, and fix
idempotence.

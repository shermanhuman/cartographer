# extract-elixir

Writes one [facts v1](../../docs/facts-format.md) document for an Elixir
repository to stdout. It parses source with `Code.string_to_quoted/2` and
never compiles or runs project code. It needs Elixir 1.19+ and no
dependencies.

Parsing does not create atoms for names found in the repository. A static
atoms encoder maps a name to an existing atom when there is one and keeps
it as a string otherwise, so a large or hostile repository cannot exhaust
the VM's atom table. As a backstop, a file is skipped with an `atom_budget`
note if the atom table is ever 90% full.

```bash
mise exec -- elixir plugins/extract-elixir/extract.exs \
  --repo shop-app --root /path/to/snapshot --revision "$SHA" \
  [--source-roots lib,test] \
  [--schema-facts schema.json] [--ref-roots lib,priv/repo/migrations,scripts]
```

| Argument | Default | Meaning |
|---|---|---|
| `--repo` | required | Repository name; ids are `<repo>:<Module>` and `<repo>:<Module>.<fun>/<arity>`. |
| `--root` | required | Directory to read. No git operations; the core passes a `git archive` snapshot. |
| `--revision` | none | Copied to `revision`. |
| `--source-roots` | `lib` | Comma-separated directories (relative to root) whose `.ex`/`.exs` files are parsed. Paths are literal (no globbing); symlinks are never followed; dot-files and dot-directories are skipped. |
| `--schema-facts` | none | A facts document from `extract-postgres-schema`. Enables `table_refs`. |
| `--ref-roots` | `lib,priv/repo/migrations,scripts` | Directories scanned for table names (`.ex .exs .sql .sh`). |

Exit status 2 means bad arguments; nothing is written to stdout.

## Output

- `files`: every parsed or scanned file; category `source`, `test` (under
  `test/`), `migration` or `script`.
- `modules`: `namespace` is the module name split on `.`. `defprotocol P`
  is module `P`. `defimpl P, for: T` is module `P.T` (one per entry of a
  `for:` list, and `for:` defaults to the enclosing module), and its
  functions belong to it, not to the module it is nested in.
- `functions`: clauses merged per id. `min_arity` accounts for default
  arguments, `clauses` lists clause lines, and `fingerprint` is the sha256 of
  a structural rendering of each clause's AST with metadata stripped.
  Whitespace and comment edits keep it; body changes do not.
- `calls`: a `target` (`module`, `name`, `arity`) and never `to`. The core
  resolves targets. Kinds:

  | Kind | Meaning | `target.module` |
  |---|---|---|
  | `remote` | `Mod.fun(...)` after alias expansion; Erlang modules are `:mod` | yes |
  | `local` | unqualified call to a function defined in the caller's module | caller's module |
  | `import` | unqualified call matched to exactly one imported module in this repo (honouring `only`/`except`), or to `Kernel` | yes |
  | `local_or_import` | unqualified call the walker cannot attribute (imports from dependencies, `use`-injected functions) | omitted |
  | `capture` | `&Mod.fun/1` or `&fun/1` (attributed like local/import) | when known |
  | `delegate` | `defdelegate ... to:` | yes |
  | `apply` | `apply(Mod, :fun, [args])` with literal parts | yes |
  | `dynamic` | `var.fun()`, `fun.()`, `apply/2,3` with non-literal parts | omitted; never resolved |

  Operators, special forms, string-interpolation `to_string`, field access
  (`map.field`) and `quote` bodies are not calls. A pipe adds one to the
  arity.
- `table_refs` (with `--schema-facts`): lines naming a captured table,
  plus Ecto `schema "table"` declarations. `#` and `--` comment lines are
  skipped. In `.sql` and `.sh` files any word counts. In `.ex`/`.exs` files
  `@moduledoc`/`@doc`/`@typedoc`/`@shortdoc` (heredocs included) are
  skipped, and only code forms count: an atom (`:t`, `table(:t)`,
  `references(:t)`), a whole string (`"t"`, `from x in "t"`), or a word
  after a SQL keyword (`FROM`, `JOIN`, `INTO`, `UPDATE`, `TABLE`,
  `REFERENCES`). `function` is set when the line is inside a function
  clause. These are text matches (`candidate` precision).
- `notes`:

  | Kind | Meaning |
  |---|---|
  | `parse_error` | The file is listed but adds nothing else. |
  | `read_error` | A file or directory could not be read. |
  | `invalid_utf8` | The file had invalid UTF-8; it was parsed with U+FFFD in its place. Every string in the output is valid UTF-8. |
  | `atom_budget` | The file was not parsed because the atom table passed its budget. |
  | `dynamic_definition` | A definition the walker cannot name, e.g. `def unquote(name)()` or `defimpl P, for: some_var`. |
  | `duplicate_module` / `duplicate_definition` | The same module or function id in a second place. |
  | `symlink_skipped` | A symlinked file or directory, noted once and never followed. |
  | `empty_root` | A source or ref root has no matching files (or does not exist). |
  | `unknown_table` | An Ecto schema whose table is not in the schema facts. |

Every array is sorted, and keys are written in a fixed order, so the same
input gives byte-identical output.

## Tests

```bash
mise exec -- elixir plugins/extract-elixir/test/extract_test.exs
mise exec -- go test ./internal/facts -run Contract
```

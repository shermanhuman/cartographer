# Coverage research

Scripts behind the honest-coverage release (map-coverage-1). Each reads a
store read-only: `CARTO_DB` (default the user store) and `CARTO_SNAPSHOT`
(default the newest snapshot holding the repo).

- `bare.py <repo>`: bare (unqualified) unresolved Elixir calls, and how many
  share a name and arity with a public project function.
- `deps.py <repo> <dep-dirs,...>`: which of those names the given dependency
  sources define (the evidence that they are library calls).
- `unresolved.py <repo> [limit]`: what a map still leaves unresolved, by
  target, with an example site.
- `lockcov.py <repo>=<lock path>...`: each `mix.lock`'s Hex packages and
  how many are in the Hex cache at the exact version.
- `index_deps.exs <lock> <hex dir>`: a name-level index of the locked
  packages' modules (exports and `__using__` definitions), read from the
  cached tarballs in memory.
- `reach.exs <lock> <hex dir> <Module[:opt=Mod]>...`: the superset library
  reader's probe: what a `use` of each module can generate or import.
- `confirm.py <repo> <index> <store>`: how many of a map's `external` rows
  the index confirms, and the unconfirmed by cause.
- `remote.py`: whether remote calls to non-project modules are provable
  from the index, the runtime's exports or OTP.

To compare two builds without touching a registered project, give each its
own registry, store and cache (cartographer#34):

```sh
CARTOGRAPHER_STATE=$d/state CARTOGRAPHER_DATA=$d/data XDG_CACHE_HOME=$d/cache \
  cartographer project add <anchor> --yes && cartographer refresh <name>
```

## The compiler oracle (map-coverage-2)

Checks the Elixir extractor's `external` labels against the compiler, once,
on a scratch copy (never a user's checkout):

```sh
git -C ~/github/<repo> archive origin/main | tar -x -C $scratch/app
elixir stage_deps.exs $scratch/app ~/.hex/packages/hexpm   # verified tarballs into deps/<lock key>
cd $scratch/app && env -u XDG_CACHE_HOME MIX_ENV=dev elixir ../trace.exs   # deps.compile, compile with a tracer, debug info
elixir $copy/extract.exs ... > ext.json   # a scratch copy whose facts.exs keeps `ext` on each call
python3 compare.py ext.json $scratch/app/oracle.json
```

The scratch copy of the plugin differs in one line: `calls/4` in
`facts.exs` returns `calls` instead of
`Enum.map(calls, &Map.delete(&1, :ext))` as its first element, so each
emitted call keeps its `ext` outcome (facts decoding would reject the
field, so the plugin never emits it).

`extract_shapes.exs` collected the regression corpus's shapes from the
review probes.

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

To compare two builds without touching a registered project, give each its
own registry, store and cache (cartographer#34):

```sh
CARTOGRAPHER_STATE=$d/state CARTOGRAPHER_DATA=$d/data XDG_CACHE_HOME=$d/cache \
  cartographer project add <anchor> --yes && cartographer refresh <name>
```

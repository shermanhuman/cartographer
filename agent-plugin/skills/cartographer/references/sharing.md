# Sharing a view

- **Issue/PR comment (automatable):** `map_embed` (or `cartographer embed <ref>`),
  then `gh issue comment N --body-file FILE`. GitHub renders the Mermaid block;
  keep diagrams at 40 nodes or fewer (`max`).
- **PDF of a view:** `cartographer export --pdf --snippet --around <ref> -o view.pdf`
  (or `--page`, `--context`, `--table`, `--annotation`). Needs Chrome or
  Chromium (`CHROME_PATH`). Attach it to a private issue by dragging it into
  the comment box; GitHub has no attachment API.
- **SVG of the graph:** `cartographer export --svg --around <ref> -o graph.svg`.
- **Interactive:** `cartographer serve --open` (127.0.0.1 only); every view
  has a deep link and Save as PDF / Download SVG buttons.

Every export names the snapshot and repository revisions it came from.
Shared files can't be revoked: prefer a snippet over a whole map.

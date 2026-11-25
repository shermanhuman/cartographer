# The `serve` API

`cartographer serve` answers on 127.0.0.1 only. The map UI reads the JSON endpoints under `/api/`. Other local tools can read them too: Sugarhouse frames the UI and lists projects through them. Every endpoint except `/api/snapshots` and `/api/projects` answers for one snapshot.

## Choosing the snapshot

| Parameter | Snapshot used |
|---|---|
| `?snapshot=ID` | that snapshot; 404 if it isn't imported |
| `?project=NAME` | the newest **main** build of project NAME (the only builds a registered project shows; a newer published or head snapshot of the same name is skipped) |
| neither | the default: the map of the directory `serve` started in, else the newest snapshot |

When both parameters are given, `snapshot` wins.

A `?project=` that resolves to nothing is a 404 that says why:

- **`no project NAME`:** NAME isn't registered with this store, and the store holds no main build under that name.
- **`no map of project NAME yet: …`:** the project is registered but has no build. The rest of the message says whether its first build is running, has just started (the lookup starts a due build), failed (with the reason), or is off.

With `?project=`, `/api/status`'s `default_snapshot` is the project's newest main build. `/api/snapshots?project=NAME` lists only that project's main builds, newest first. Each entry carries `source` (`main`, `head` or `published`).

## The UI's address

The UI keeps the same choice in its hash: `#project=NAME` or `#snapshot=ID`. It sends the choice on every API call and keeps it while you move around the map. Changing either one reloads the page onto that snapshot's data.

A `#project=` page pins the build it loaded: later calls also send that `snapshot`. A background rebuild therefore never mixes two builds on one page. Reloading opens the newest build again.

**Copy link:**
- **On a `#project=` page:** the link keeps `project` and pins a snapshot only when you picked an older build. It keeps opening the newest build after rebuilds.
- **Without a project:** the link pins the snapshot, as before.

## `GET /api/projects`

Lists the projects registered with the store `serve` uses, as `cartographer project list` shows them for this store:

```json
{
  "projects": [
    {
      "name": "breakdown",
      "anchor": "Applesauce-Labs/breakdown-admin",
      "members": ["Applesauce-Labs/breakdown-admin", "Applesauce-Labs/breakdown-sites"],
      "last_snapshot": "snapshot-20261124T231630Z-…",
      "last_cycle": {"state": "built", "at": "2026-11-24T23:16:30Z"}
    }
  ],
  "problems": [{"file": "…/projects/old.json", "error": "…"}]
}
```

- **`last_snapshot`** is the newest main build, or `""` before the first one.
- **`last_cycle.state`** is `running`, `failed` (with `error`), `built` or `never`.
- **`problems`** lists registrations that couldn't be read. Its store is unknown, so a problem may belong to another store's project. The readable projects still come back.
- **A registry that can't be read at all** is a 500 with the cause, never an empty list.

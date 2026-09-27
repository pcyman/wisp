# Git worktrees

Create linked worktrees with Git on the host, then run Wisp in each worktree:

```sh
git worktree add -b feature-one ../repo-feature-one
wisp ../repo-feature-one
```

Each worktree has its own Wisp sandbox, lock, and agent data. For standard linked worktrees, Wisp mounts the shared Git directory **writable** in the sandbox and overlays container-specific `.git` and worktree-backlink pointers. The original host pointers are not changed. Git commands such as `status`, `add`, and `commit` can use this shared metadata.

The shared Git directory also holds refs, objects, configuration, and administrative data for *other* worktrees. A sandbox with write access can change those shared resources. Use Git on the host to create, repair, move, and remove worktrees; container paths are not host worktree paths. Unusual metadata layouts (including absolute `commondir` pointers), external object alternates, and nested submodule worktrees are not supported by this mount planning.

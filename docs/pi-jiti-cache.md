# Pi Jiti cache

## Why this cache exists

Pi loads TypeScript extensions through [Jiti](https://github.com/unjs/jiti). In a
normal Wisp Pi profile, packages such as `pi-mcp-adapter` and `pi-subagents`
contain large TypeScript module graphs. Loading them without a compiled cache can
take several seconds. A measured cold launch spent about 7.8 seconds creating the
Pi runtime, almost entirely importing those two extensions.

Jiti normally stores its compiled files in a `jiti` directory under the process
temporary directory. Wisp sets `TMPDIR=/run/wisp/tmp`, an executable tmpfs that is
created fresh for every sandbox. This is intentional for general temporary files,
but it meant Jiti discarded its compilation cache whenever a sandbox exited and
recompiled every extension on the next launch.

Wisp therefore persists only Jiti's cache. The rest of `/run/wisp/tmp` remains an
ephemeral, narrowly scoped executable tmpfs.

## Layout and lifecycle

For a shared Pi profile, the host cache is located under:

```text
$XDG_CACHE_HOME/wisp/pi-jiti/<profile-key>/<image-key>/
```

If `XDG_CACHE_HOME` is unset, Wisp uses `$HOME/.cache`. The keys are SHA-256
hexadecimal strings:

- `profile-key` identifies the physical Pi profile path. Sandboxes using the same
  host Pi profile share compiled extension output across projects.
- `image-key` identifies the immutable Docker image ID used by the sandbox.

When no host Pi profile is shared, Wisp derives the profile scope from the
project hash instead. This keeps project-local Pi state and its cache isolated.

At launch Wisp:

1. Creates and validates the user-private profile cache directory on the host.
2. Mounts that directory read-write at `/run/wisp/jiti-cache`.
3. Ensures the sandbox image exists, then inspects its immutable Docker image ID.
4. Derives the image key and creates its `0700` cache directory on the host.
5. Replaces `WISP_IMAGE` with the inspected immutable image ID for the final
   Compose invocation.
6. Passes the image key as `WISP_PI_JITI_CACHE_KEY`.
7. The entrypoint validates the key and directory, then links
   `${TMPDIR}/jiti` to `/run/wisp/jiti-cache/<image-key>` before starting Pi.

The first launch for a profile/image combination populates the cache. Subsequent
launches reuse it. Jiti records a source hash in each cache entry, so edits or
package updates invalidate affected compiled output.

## Why the image is pinned by ID

Docker image tags are mutable. Keying the cache by a tag such as `wisp:local`
would reuse compiled output after that tag was rebuilt with a different Node,
Pi, or Jiti runtime.

There is also a concurrency race if Wisp inspects a tag and another Wisp process
retags it before Compose creates the container. The cache could then be selected
for image A while the container runs image B.

To prevent both cases, Wisp inspects the image after image preparation, keys the
cache from that immutable ID, and launches Compose with the same ID rather than
the mutable tag. A concurrent rebuild may change the tag, but it cannot silently
change the runtime selected for an already prepared launch.

## Sharing and project-local extensions

The shared-profile cache deliberately assumes that project-local TypeScript
extensions are not used. Every Wisp project is mounted at `/workspace/current`,
so identically named project extensions have the same container path. A global
Jiti cache could make different projects contend for the same cache filenames.
Jiti checks source hashes, but its cache writes are not designed as a
cross-project isolation boundary.

If Wisp gains support for project-local extensions, revisit this design. Safe
options include:

- restoring project-scoped Jiti caches;
- separating trusted package compilation from project extension compilation; or
- adding loader support for distinct cache directories by source scope.

Do not simply place project-local extension output in the current shared cache.

## Security and ownership

Compiled extension output is executable code and is treated like the source Pi
profile:

- Cache directories must be real directories, not symlinks.
- Wisp verifies ownership against the launching UID.
- Profile and image directories use mode `0700`.
- Bind mounts disable implicit host-path creation.
- The container receives only the selected profile cache root, not the whole
  host cache directory.
- The entrypoint accepts only a 64-character lowercase hexadecimal image key and
  rejects missing, symlinked, or non-directory runtime cache entries.

The cache is shared only within one host user's cache tree. It must not be made
system-wide or shared between users.

## Invalidation and cleanup

A new cache namespace is selected when either of these changes:

- the physical shared Pi profile path;
- the immutable sandbox image ID.

Within a namespace, Jiti's source hashes handle extension and dependency source
changes. A rebuild that changes the sandbox image selects a new image directory
even if the configured image tag is unchanged.

Wisp currently does not garbage-collect old Pi Jiti cache directories. Any
future cleanup must preserve ownership checks and avoid deleting a directory in
use by another Wisp process.

Deleting the cache is safe when no Wisp sandbox is using it; the next Pi launch
will recreate and warm it. Expect that first launch to be slower.

## Diagnosing startup performance

Run Wisp with startup timing enabled:

```sh
WISP_STARTUP_TIMING=1 wisp --agent pi
```

This emits host, entrypoint, and Pi lifecycle timestamps and enables Pi's native
startup timings. Important Pi output includes `createAgentSessionRuntime` and
per-extension module import durations.

Useful boundaries are:

- `host.sandbox.run` to `container.entrypoint.start`: Docker/Compose startup;
- `container.entrypoint.start` to `container.exec`: entrypoint setup;
- `container.exec` to `pi.status_extension.module_loaded`: Node/Pi bootstrap;
- extension import timings: Jiti compilation or cache reuse;
- `pi.status_extension.module_loaded` to `pi.session_start`: remaining Pi runtime
  initialization.

A healthy warm cache should make TypeScript package imports substantially faster
than the first launch. If every launch is cold, verify that `${TMPDIR}/jiti` is a
symlink into `/run/wisp/jiti-cache/<image-key>` and that cache files survive after
the container exits.

## Implementation map

- `internal/app/plan.go`: records the resolved Pi profile identity.
- `internal/app/agent_data.go`: hashes cache identities and creates private cache
  directories and the bind mount.
- `internal/app/run.go`: prepares the mount, resolves the final image ID, creates
  the runtime cache namespace, and pins the Compose launch to that image ID.
- `internal/docker/inspect.go`: reads and validates the Docker image ID.
- `internal/agent/pi.go`: defines the fixed container cache target.
- `compose.yaml`: passes the cache key into the sandbox.
- `container/entrypoint.sh`: validates and links the selected cache directory.

Relevant tests are in `internal/app/agent_data_test.go`,
`internal/app/run_lifecycle_test.go`, `internal/docker/inspect_test.go`, and
`runtimeassets_test.go`.

After changing runtime assets, rebuild the Wisp binary before rebuilding the
image because `Dockerfile`, `compose.yaml`, and the entrypoint are embedded in
the Go binary:

```sh
go build -trimpath -o ~/.local/bin/wisp ./cmd/wisp
~/.local/bin/wisp --rebuild --agent pi
```

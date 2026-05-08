# Hula builder images

Hula builds sites by running `hulabuild` inside a Docker container that has
the requested static-site generator pre-installed. This directory contains
the Dockerfiles for those images and the `build-images.sh` script that
cross-builds them for `linux/amd64` and `linux/arm64`.

The images are referenced from `sitebuild.yaml`'s `builder_image:` field
(default: `default`, which resolves to `hula-builder-alpine-default`).

## Available images

| Image | Tag | Base | When to use |
|-------|-----|------|-------------|
| **alpine-default** | `hula-builder-alpine-default:latest`<br>`hula-builder-default:latest` | `alpine:3.19` | Default. Smaller, faster cold start. Recommended for almost all sites. |
| **ubuntu22.04** | `hula-builder-ubuntu22.04:latest` | `ubuntu:22.04` | Use when a site needs a glibc-only binary or apt-only package not available in alpine. Larger image, slower pull. |

Both images carry the same toolchain — pick on base-OS compatibility, not
features.

## Baked-in tooling

| Tool | Version | Pinned by |
|------|---------|-----------|
| `hulabuild` | from-source | built per-arch by `build-images.sh` |
| Hugo (extended) | `0.147.6` | `ARG HUGO_VERSION` |
| Node.js | `22.x` (ubuntu only — alpine uses `apk` Node) | `ARG NODE_MAJOR` (ubuntu) / apk (alpine) |
| MkDocs | `1.6.1` | `ARG MKDOCS_VERSION` |
| mkdocs-material | `9.5.49` | `ARG MKDOCS_MATERIAL_VERSION` |
| Astro CLI | latest at image-build time | `npm install -g astro` (unpinned) |
| Gatsby CLI | latest at image-build time | `npm install -g gatsby-cli` (unpinned) |

To bump a version in the baked image, edit the `ARG` default in the
Dockerfile and rebuild — that's the deliberate version-bump path.

> **Note:** Astro and Gatsby CLIs are intentionally unpinned today. Sites
> that need a specific Astro or Gatsby version should pin them in
> `package.json` and let `npm install` resolve in the build container. A
> baked-in pin would constrain every site that uses the image; transitive
> Node deps are better managed by the site itself.

## Building images

```bash
./build-images.sh                    # build both images for amd64 + arm64
./build-images.sh --push             # push to the configured registry
./build-images.sh --platform linux/amd64    # single arch
```

Override a baked-in version at image-build time:

```bash
docker build \
    --build-arg HUGO_VERSION=0.150.0 \
    --build-arg MKDOCS_VERSION=1.7.0 \
    --build-arg MKDOCS_MATERIAL_VERSION=9.6.0 \
    -t hula-builder-alpine-default:custom \
    alpine-default/
```

`build-images.sh` does not currently forward `--build-arg`s — use `docker build`
directly when overriding.

## Per-site version overrides (no image rebuild)

Sites override the baked-in tooling **without rebuilding the builder image**
by declaring versions in `.hula/sitebuild.yaml`. Hula synthesises a derived
image (cached by content hash) that pip-installs the requested versions FROM
the base builder image.

### Hugo

Today, `sitebuild.yaml`'s `hugo:` block parses but is not wired into
image derivation. Hugo version pinning at the per-site level is on the
roadmap (see issue tracker); for now bake the version into a custom
builder image or use `dockerfile_prebuild` to download a different Hugo
binary.

### MkDocs

Fully wired:

```yaml
mkdocs:
  version: "1.6.1"           # or at_least: "1.5.0"
  material: "9.5.49"
  extra_packages:
    - pymdown-extensions==10.11.2
    - mkdocs-mermaid2-plugin
    - mike==2.1.3

configs:
  production:
    commands: |
      WORKDIR /builder
      MKDOCS build --strict --site-dir _hula_out
      FINALIZE /builder/site/_hula_out
```

`mkdocs:` may also be set per-profile, in which case the profile-level
config wins.

`extra_packages` accepts any pip specifier — exact pins (`pkg==X.Y.Z`),
ranges (`pkg>=1.0,<2.0`), extras (`pkg[extra]`), and VCS URLs.

### Arbitrary tooling — `dockerfile_prebuild`

For anything pip can't install (system packages, custom binaries),
`dockerfile_prebuild` is appended to the synthesised mkdocs install lines:

```yaml
configs:
  production:
    dockerfile_prebuild: |
      RUN apk add --no-cache imagemagick
    commands: |
      WORKDIR /builder
      RUN imagemagick-postprocess.sh
      HUGO --minify
      FINALIZE /builder/site/public
```

## Auto-detection

Sites with no `.hula/sitebuild.yaml` get a sensible default profile based
on marker files in the repo root. Precedence (highest first):

1. `mkdocs.yml` / `mkdocs.yaml` → MkDocs default
2. `astro.config.{mjs,ts,js}` → Astro default
3. `gatsby-config.{js,ts}` → Gatsby default
4. `hugo.toml` / `hugo.yaml` / `config.toml` / `config.yaml` → Hugo default

When two markers from different generators are present (e.g. a stale
`config.toml` left over from a Hugo→MkDocs migration), the higher-priority
one wins and the loser shows up in the build log as `ignored`.

To force a different default, write an explicit `configs:` block in
`sitebuild.yaml`.

## Trust model

`sitebuild.yaml` runs in the operator's trust domain. Anyone who can land
a `sitebuild.yaml` change in the source repo can:

- Install arbitrary pip packages (`mkdocs.extra_packages`)
- Run arbitrary shell (`dockerfile_prebuild`, `RUN` in the commands list)

The build container is ephemeral and isolated, but it inherits whatever
secrets the build environment has access to (`build_env:` in
`config.yaml`, the Docker socket if Hula runs inside Docker, etc.).
Review `sitebuild.yaml` changes the same way you review Dockerfiles.

## `ENV PIP_BREAK_SYSTEM_PACKAGES=1`

Both images set this env so pip-driven derived images install cleanly
regardless of whether the base Python enforces PEP-668. Honored by
pip 23+; older pip ignores the env var. No site action required.

## Pinning policy

- **Hugo, mkdocs, mkdocs-material:** pinned, bumped deliberately. Open a
  PR against this directory.
- **Astro, Gatsby CLIs:** unpinned in the image; sites pin via
  `package.json`.
- **Alpine / Ubuntu base versions:** pinned in `FROM`. Bump alongside a
  base-OS security review.

When bumping, run the e2e build suite against both fixtures
(`test/e2e/fixtures/`) before tagging the new image as `latest`.

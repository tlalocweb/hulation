# MkDocs Builder — Plan

**Status:** Draft v0.1
**Driver:** `hulation-docs` (the public docs site). Persona-B Hugo flow is mature; we now
need the same first-class experience for MkDocs so we can dogfood Hula on its own docs.
**Scope:** small. MKDOCS support is already plumbed end-to-end at the COMMANDLIST and
builder-image layers. This plan closes the remaining gaps and adds tests.

## 1. What's already in main

The bones are all here, hidden in plain sight:

- `sitedeploy/commandlist.go:14` — `MKDOCS` is in `validCommands`.
- `model/tools/hulabuild/commands.go:38–39, 63–64` — `MKDOCS` dispatches to the same
  `cmdStaticGen("mkdocs", args)` helper used by `HUGO`/`ASTRO`/`GATSBY`. Build-time and
  staging `build_command` paths both accept it.
- `builder-images/alpine-default/Dockerfile:37` and
  `builder-images/ubuntu22.04/Dockerfile:42` — both images already
  `pip3 install mkdocs mkdocs-material`. No image change needed for the v1 docs site.

What this means: a site with a `sitebuild.yaml` that says `MKDOCS` in its commands list
will build today, with no code change. The next sections close everything else.

## 2. Gaps to close

### 2.1 Default profile when no `sitebuild.yaml` is present

`SiteBuildConfig.GetProfile` (`sitedeploy/sitebuild_config.go:70–85`) returns a hard-coded
Hugo default when `Configs` is empty:

```go
return &BuildProfile{
    Commands: "WORKDIR /builder\nHUGO --minify\nCP -r public/* site/\nFINALIZE /site\n",
}, nil
```

This is fine for Hugo repos but actively wrong for an mkdocs repo (no `hugo` will fail,
or worse, succeed against a stray Hugo config). Two options:

- **Option A (preferred): generator auto-detection.** When `sitebuild.yaml` is absent,
  inspect the cloned repo for marker files and return a default profile keyed off what's
  present:
  - `mkdocs.yml` (or `mkdocs.yaml`) → MKDOCS default
  - `config.toml` / `hugo.toml` / `hugo.yaml` / `config/_default/` → HUGO default (current
    behaviour)
  - `astro.config.mjs` / `astro.config.ts` → ASTRO default
  - `gatsby-config.js` / `gatsby-config.ts` → GATSBY default
  - none of the above → return a typed `ErrNoBuilderDetected` so the caller can surface a
    useful error in `hulactl build-status`.

- **Option B:** require a `sitebuild.yaml` in every non-Hugo repo. Cheaper to implement
  but a worse first-run experience for the docs site (and every future mkdocs adopter).

**Decision:** Option A. The auto-detection is ~30 lines and keeps the "drop a Hugo
repo, point Hula at it, done" promise extending naturally to mkdocs.

The default mkdocs profile, derived from how `mkdocs build` actually behaves
(see §2.2):

```go
&BuildProfile{
    Commands: "WORKDIR /builder\n" +
              "MKDOCS build --strict --site-dir _hula_out\n" +
              "FINALIZE _hula_out\n",
}
```

Implementation lives next to `GetProfile`. Detection can be either pushed into
`ParseSiteBuildConfig`'s caller (it has the repo path) or — cleaner — `GetProfile`
gains a `repoDir string` parameter so the default-profile branch can stat the repo.
The latter requires touching every call site of `GetProfile`; about 4 call sites in
`sitedeploy.go` and `staging.go`. Worth doing — implicit defaults that ignore the
repo are a bug magnet.

### 2.2 The `site/` collision

Hugo's default profile runs in `<workdir>/site/` and Hugo writes to
`<workdir>/site/public/`, then `CP -r public/* site/` copies into the workdir's `site`
dir for `FINALIZE`. The convention is: source under `<workdir>/site`, output also
under `<workdir>/site` (Hugo's own `public/` ends up flattened in by the CP step).

MkDocs is different. By default `mkdocs build` writes to `./site/` *relative to
`mkdocs.yml`*. With our existing `cmdStaticGen` setting `cmd.Dir = <workdir>/site`,
mkdocs would write to `<workdir>/site/site/`. Two paths handle this:

- **Use `--site-dir`** to redirect mkdocs's output to a sibling directory we control
  (`_hula_out`), then `FINALIZE _hula_out`. This is what §2.1's default profile does.
  No changes to `cmdStaticGen` required.
- Special-case mkdocs in `cmdStaticGen` to pass `--site-dir` automatically when the
  user didn't supply one. Cleaner from a "Just Works" perspective but couples
  generator-specific behaviour into `cmdStaticGen`. **Not preferred.**

**Decision:** rely on `--site-dir` in the profile. Document it. Add one defensive
check in `cmdStaticGen` for mkdocs: if the args contain neither `-d` nor `--site-dir`,
log a warning that the build will write into `site/site/` and the FINALIZE step
won't pick it up. (No hard failure — operators may have their own conventions.)

### 2.3 MkDocs version pinning

`SiteBuildConfig` has `HugoVersionConfig` with `at_least` / `version` fields. There is
no analog for mkdocs. Today the version is whatever `pip3 install` resolved when the
builder image was last built — that's an unpinned moving target.

Add:

```go
type MkDocsVersionConfig struct {
    AtLeast        string `yaml:"at_least,omitempty"`
    Version        string `yaml:"version,omitempty"`
    Material       string `yaml:"material,omitempty"`        // mkdocs-material version
    ExtraPackages  []string `yaml:"extra_packages,omitempty"` // arbitrary pip packages
}

type SiteBuildConfig struct {
    // ...
    MkDocs *MkDocsVersionConfig `yaml:"mkdocs,omitempty"`
    // ...
}

type BuildProfile struct {
    // ...
    MkDocs *MkDocsVersionConfig `yaml:"mkdocs,omitempty"`
    // ...
}
```

When `MkDocs` resolves to non-default values, the existing `DockerfilePrebuild`
mechanism (`builder.go:97`'s `buildDerivedImage`) generates a derived image that
`pip install`s the pinned versions. Same content-hash caching as the Hugo path.

We'll likely synthesise the prebuild commands inside `ResolveMkDocsConfig` rather than
asking operators to write `dockerfile_prebuild` blocks by hand:

```dockerfile
RUN pip3 install --no-cache-dir --break-system-packages \
    mkdocs==1.6.1 \
    mkdocs-material==9.5.49 \
    pymdown-extensions
```

This keeps parity with how `HugoVersionConfig` is handled (operators specify
`at_least: "0.159.1"`, hulabuild does the rest).

`extra_packages` covers our `hulation-docs` need for `pymdown-extensions`,
`mkdocs-mermaid2-plugin` (if we end up wanting Mermaid), and `mike` (when we adopt
versioned docs).

### 2.4 Builder-image hygiene

Two issues with the current builder Dockerfiles:

- The `pip3 install mkdocs mkdocs-material` lines pin nothing. A rebuild of the
  builder image silently shifts versions. Pin to specific releases and bump
  deliberately:
  ```dockerfile
  ARG MKDOCS_VERSION=1.6.1
  ARG MKDOCS_MATERIAL_VERSION=9.5.49
  RUN pip3 install --no-cache-dir --break-system-packages \
      mkdocs==${MKDOCS_VERSION} \
      mkdocs-material==${MKDOCS_MATERIAL_VERSION}
  ```
- `--break-system-packages` is fine on alpine where there's no PEP-668 enforcement to
  break; on ubuntu22.04 we're already shipping a fairly recent Python and the same
  flag is needed there too. Confirm against the actual `python3` version in that
  image — if it's pre-PEP-668, the flag is harmless; if not, this change is required
  for any pinned version bump.
- Document the baked-in versions in `builder-images/README.md` (file does not
  currently exist — small, useful add).

### 2.5 Logging quality

`cmdStaticGen` sends every line through `proto.sendLog("[%s] %s", generator, line)`.
mkdocs is much chattier than hugo on `--strict` failures (each warning gets a full
traceback when promoted to error). This is fine but worth confirming with one
realistic build that the log buffer in `hulabuild`'s protocol layer doesn't choke.
*(Action: throwaway test once §3.2 lands.)*

## 3. Work breakdown

### 3.1 Code changes (in this repo, `hulation`)

| File | Change |
|------|--------|
| `sitedeploy/sitebuild_config.go` | Add `MkDocsVersionConfig`, hang it off `SiteBuildConfig` and `BuildProfile`. Add `ResolveMkDocsConfig`. |
| `sitedeploy/sitebuild_config.go` | `GetProfile(repoDir string)` — extend signature, add generator auto-detection branch when `Configs` is empty. New `ErrNoBuilderDetected`. |
| `sitedeploy/sitebuild_config.go` | When `MkDocs` is non-default, synthesise pip-install lines and merge into `DockerfilePrebuild`. |
| `sitedeploy/sitedeploy.go`, `sitedeploy/staging.go` | Pass repoDir into `GetProfile` calls (~4 sites). |
| `model/tools/hulabuild/commands.go` | Defensive log when `MKDOCS` args miss `--site-dir` / `-d`. |
| `builder-images/alpine-default/Dockerfile` | Pin mkdocs / mkdocs-material via build args. |
| `builder-images/ubuntu22.04/Dockerfile` | Same. |
| `builder-images/README.md` (new) | Document baked-in versions and override mechanism. |

Estimated diff: ~250 lines of Go, ~20 lines of Dockerfile, ~80 lines of new docs.

### 3.2 Tests

- `sitedeploy/sitebuild_config_test.go` — auto-detection picks mkdocs over hugo when
  both markers exist (define explicit precedence: explicit `Configs` > `mkdocs.yml`
  > Hugo markers > Astro > Gatsby), unit tests for `ResolveMkDocsConfig`, the synth-
  prebuild output, and `ErrNoBuilderDetected`.
- `sitedeploy/builder_test.go` (or a sibling integration test) — content-hash cache
  hits on identical mkdocs prebuilds; second build of the same site reuses the
  derived image.
- E2E suite — add a test that mirrors the Hugo e2e: clone a small fixture mkdocs
  site (committed under `test/fixtures/mkdocs-min/`), trigger a build through
  `BuildManager`, assert FINALIZE produced an `index.html` and a `sitemap.xml` (both
  default mkdocs outputs).

### 3.3 Documentation hooks

The hulation-docs PRD §6.4 (config reference) and §6.7 (cookbook) reference these
features. Once §3.1 lands, the docs PRD's M4 dependency on the mkdocs builder is
unblocked. Coordinate so the cookbook page "Hugo + GitHub autodeploy" gets a sibling
"MkDocs + GitHub autodeploy" entry referring back to the canonical sitebuild.yaml
shape that lands here.

## 4. Worked example: `hulation-docs/sitebuild.yaml`

After §3.1 ships, the docs site can rely on auto-detection and need no
`sitebuild.yaml` at all. We commit one anyway so future mkdocs adopters have a
copyable reference, and so we can pin versions:

```yaml
mkdocs:
  version: "1.6.1"
  material: "9.5.49"
  extra_packages:
    - pymdown-extensions==10.11.2
    # - mike==2.1.3        # uncomment when versioned docs land

configs:
  production:
    commands: |
      WORKDIR /builder
      MKDOCS build --strict --site-dir _hula_out
      FINALIZE _hula_out
```

Staging variant (drives `hula_build: staging`):

```yaml
configs:
  staging:
    servedir: /builder/site/_hula_out
    build_command: MKDOCS build --site-dir _hula_out
    commands: |
      WORKDIR /builder
      MKDOCS build --site-dir _hula_out
```

Both go in the same file under `configs:`; Hula picks the one matching `hula_build`.

## 5. Phasing

| Phase | Scope | Status |
|-------|-------|--------|
| **P0** | Pin builder-image versions (§2.4) | ✅ landed — `MKDOCS_VERSION=1.6.1`, `MKDOCS_MATERIAL_VERSION=9.5.49` via `ARG` in both Dockerfiles |
| **P1** | Auto-detection (§2.1) + `--site-dir` defensive log (§2.2) + tests (§3.2) | ✅ landed — `DetectGenerator`, `defaultProfileFor` (production + staging shapes), `GetProfile(name, repoDir)` signature change, missing-`sitebuild.yaml` fallthrough in both build and staging paths, `hasMkdocsSiteDir` preflight in `cmdStaticGen`, full table-driven test suite |
| **P2** | `MkDocsVersionConfig` + synth-prebuild integration (§2.3) | ✅ landed — `MkDocsVersionConfig`, `ResolveMkDocsConfig`, `synthMkDocsPrebuild`, `EffectivePrebuild` (synth precedes operator prebuild), wired into both production and staging paths via `StartStaging(siteCfg, …)`, `ENV PIP_BREAK_SYSTEM_PACKAGES=1` on both base images |
| **P3** | Builder-images README (§2.4) + cookbook entry in hulation-docs | planned — aligned with hulation-docs M3 |

P0–P2 land before hulation-docs M4 — that's the gating dependency the docs PRD
identified.

## 6. Decided

All five originally-open questions are resolved:

1. **Auto-detection precedence.** `mkdocs.yml` / `mkdocs.yaml` wins over Hugo
   markers when both are present. Full order: explicit `sitebuild.yaml configs` >
   mkdocs > astro > gatsby > hugo > `ErrNoBuilderDetected`. When detection picks
   mkdocs over a stale Hugo config, log loudly: `"auto-detected mkdocs from
   mkdocs.yml; ignored hugo.toml — set sitebuild.yaml configs to override"`.
2. **`mkdocs.yaml` vs `mkdocs.yml`.** Accept both. If both somehow exist, prefer
   `.yml` to match the dominant convention; emit the same kind of "ignored X in
   favor of Y" log.
3. **`extra_packages` security.** Ship as proposed — same authority `RUN` /
   `dockerfile_prebuild` already grant. Add a one-line operator-doc note: the
   `sitebuild.yaml` runs in the operator's trust domain; review changes to it the
   same way you review Dockerfiles.
4. **Mermaid / diagrams.** Use mkdocs-material's built-in
   `pymdownx.superfences` custom-fence integration. **Self-host `mermaid.min.js`** —
   no CDN dependency. The `hulation-docs` repo vendors the file under
   `docs/assets/javascripts/` and references it via `extra_javascript:` in
   `mkdocs.yml`. Do **not** list `mkdocs-mermaid2-plugin` in `extra_packages` —
   that plugin is for non-Material themes. No builder-image changes needed.
5. **Dedicated `cmdMkdocs` function?** No. Keep the shared `cmdStaticGen`, add an
   inline mkdocs-specific preflight check that warns when args lack `--site-dir` /
   `-d`. Split into a dedicated function only when a second or third
   mkdocs-specific behaviour appears.

## 7. Risks

- **Auto-detection misfires.** A repo with stray marker files from a half-finished
  migration could pick the wrong default. Mitigation: explicit precedence + clear
  log line at build start ("auto-detected mkdocs from mkdocs.yml; override with a
  sitebuild.yaml configs block").
- **PEP-668 friction on ubuntu22.04 image.** If `pip install --break-system-packages`
  stops being accepted in a future ubuntu base, switch to a venv inside the image.
  Defer until it bites.
- **MkDocs major-version churn.** mkdocs-material has had aggressive 9.x point
  releases. Pinning is the answer; bumping the baked-in versions is a deliberate
  builder-image release.

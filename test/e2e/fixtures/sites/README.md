# Self-contained builder fixtures

Two minimal site sources used by `test/e2e/suites/07a-builders-min.sh`
to exercise the production build pipeline end-to-end against each
supported generator without touching any external repo.

| Fixture       | Generator | Sitebuild config | Purpose                                              |
| ------------- | --------- | ---------------- | ---------------------------------------------------- |
| `hugo-min/`   | hugo      | explicit         | "Operator hand-wrote sitebuild.yaml" code path.      |
| `mkdocs-min/` | mkdocs    | none — auto-det. | Auto-detection from `mkdocs.yml` (P1 default-profile path). |

`prepare_min_sites()` in `lib/setup.sh` initialises a git repo from
each fixture, creates a bare clone next to it, and exposes both as
file-mounted bare repos to the hula container. The hula config maps
`hugo-min.test.local` and `mkdocs-min.test.local` to the
corresponding bare repos via `root_git_autodeploy`.

Adding a new fixture (gatsby, astro, ...) is six steps:

1. Drop the source under `sites/<name>-min/`.
2. Add a server block in `fixtures/hula-config.yaml.tmpl` pointing at
   `file:///var/hula/<name>-min-bare.git`.
3. Add a bind-mount in `fixtures/docker-compose.yaml`.
4. Extend `prepare_min_sites()` to seed the new fixture.
5. Add `<name>-min.test.local` to the runner's hosts file (rendered
   into the hulactl-runner via setup.sh).
6. Extend `07a-builders-min.sh` with the new generator's expected
   output paths.

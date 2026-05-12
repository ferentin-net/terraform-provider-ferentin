# Release runbook

The Ferentin Terraform provider follows tag-driven releases. Pushing a
`vX.Y.Z` tag invokes [`.github/workflows/release.yml`](.github/workflows/release.yml),
which runs goreleaser, signs the checksum file with the org GPG key, and
creates a GitHub release that the Terraform registry ingests.

## One-time setup (registry publishing)

1. Generate a GPG key for the provider (RSA 4096):

   ```sh
   gpg --full-generate-key
   gpg --armor --export-secret-keys <fingerprint> > private.asc
   gpg --armor --export <fingerprint> > public.asc
   ```

2. Add **repo secrets** at `Settings → Secrets and variables → Actions`:
   - `GPG_PRIVATE_KEY` — full contents of `private.asc`
   - `PASSPHRASE` — the GPG key passphrase

3. Upload the **public key** to the Terraform registry (`Users → API tokens`).

4. Set up the registry listing under the `ferentin-net` namespace and point it
   at this repo. The registry watches GitHub release events for new versions.

## Cutting a release

1. Update [`CHANGELOG.md`](CHANGELOG.md): move `[Unreleased]` entries under a
   new `[X.Y.Z] - YYYY-MM-DD` heading.
2. Commit & push the changelog:

   ```sh
   git add CHANGELOG.md
   git commit -m "Release vX.Y.Z"
   git push origin main
   ```

3. Tag and push:

   ```sh
   git tag -a vX.Y.Z -m "vX.Y.Z"
   git push origin vX.Y.Z
   ```

4. Watch the **Release** workflow on GitHub Actions. When green, the GitHub
   release will appear at `https://github.com/ferentin-net/terraform-provider-ferentin/releases/tag/vX.Y.Z`
   and the Terraform registry will pick it up within a few minutes.

## Versioning

SemVer per [CHANGELOG.md](CHANGELOG.md):

- **Patch (`X.Y.Z+1`)** — bug fixes, doc updates, no schema changes.
- **Minor (`X.Y+1.0`)** — additive schema changes (new attributes, new
  resources, new validators that previously-valid configs still satisfy).
- **Major (`X+1.0.0`)** — breaking changes: removed/renamed attributes,
  stricter validators that reject previously-accepted values, state-shape
  changes that require migration.

The provider is `0.x` until it covers the full v1 entity surface in the
design doc and has shipped at least one major-customer install.

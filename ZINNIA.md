# Zinnia fork operations

This repository is the source fork for Zinnia's Beta9 gateway. The fork keeps
the upstream chart and worker release model, while carrying the Vast provider
changes required to launch Beta9 workers as GPU-capable Ubuntu KVM instances.

## Published artifact

Merges to `main` that touch gateway inputs run `Publish Zinnia gateway` and
publish two tags:

- `ghcr.io/bodyiq/beta9-gateway:sha-<main-commit>` is the production artifact.
- `ghcr.io/bodyiq/beta9-gateway:main` is a convenience tag and is not deployed.

The workflow uses the repository-scoped `GITHUB_TOKEN`; it needs no host,
Kubernetes, or cloud-provider credentials. The GHCR package must be public so
the cluster can pull immutable gateway images without a long-lived image-pull
credential.

The current package predates this workflow. In its package settings, connect
`BodyIQ/beta9` and grant that repository Actions write access before the first
merge publication.

Production deployment state does not live in this fork. The pinned chart,
gateway image tag, public endpoint configuration, and operator deployment
script live under `deploy/zinnia-apps/beta9` in `BodyIQ/zinnia-apps`.

## Updating from upstream

Keep the published `main` history stable. Bring upstream changes into a branch,
run the full test suite, and merge through a pull request:

```sh
git fetch upstream
git switch --create bodyiq/sync-YYYYMMDD bodyiq/main
git merge --no-ff upstream/main
make test-pkg
git push --set-upstream bodyiq bodyiq/sync-YYYYMMDD
gh pr create --repo BodyIQ/beta9 --base main --head bodyiq/sync-YYYYMMDD --draft
```

After that PR merges, wait for the gateway publication workflow, copy its full
immutable `sha-...` tag into the zinnia-apps values file through a second pull
request, and run that repository's plan/apply commands. This makes source sync,
image publication, and production rollout three observable checkpoints instead
of one coupled operation.

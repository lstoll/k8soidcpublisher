# clusterpub

simple tool to serve cluster-related public data.

https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry#authenticating-to-the-container-registry

```
docker buildx build --push --platform linux/arm64,linux/amd64 --tag ghcr.io/lstoll/infra/clusterpub:latest .
```

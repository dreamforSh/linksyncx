# Release matrix

上游 `release.yml`（`v*` / GoReleaser / Docker Hub）已从本仓库移除，这里的脚本不再由 CI 调用。

发布使用 `.github/workflows/custom-release.yml`，标签为 `custom-v*`，镜像为 `ghcr.io/dreamforsh/linksyncx`。

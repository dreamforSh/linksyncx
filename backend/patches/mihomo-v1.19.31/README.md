# Reviewed mihomo log overlay

CRS production builds and tests must run through `python3 scripts/crs_go.py build ...`
or `python3 scripts/crs_go.py test ...` from the backend directory. `CRS_GO` can select
the Go executable; normal Go environment settings, including cross-compilation,
are preserved. The backend Makefile, Dockerfile and custom release workflow use
this wrapper. Direct `go build` bypasses this patch and is not a CRS release build.

The wrapper verifies the exact module version, original file SHA256, replacement
SHA256 and patch SHA256 in `manifest.json`. It fails on drift. Go forbids overlays
inside GOMODCACHE, so the wrapper copies only the mihomo module to a temporary
directory and uses a temporary `-modfile` replacement with the single-file overlay.
The copy and manifests are removed on exit. The shared Go module cache, original
go.mod/go.sum and all other dependencies remain unchanged. Go build metadata
records the temporary replacement path; the release manifest must record the
original module version and all overlay hashes.

Source: https://github.com/MetaCubeX/mihomo/blob/v1.19.31/log/log.go
License: upstream GPL v3 text is included in `LICENSE`.

The original package reads/writes a global `level` without synchronization.
`executor.ApplyConfig` writes it on every hot reload while background proxy
cleanup goroutines call `log.print`. The local routing test reproduced this race.
Only access to that existing scalar is changed to atomic Load/Store; thresholds,
subscribers, formatting and routing remain identical. `log.go.patch` records the
complete change. `log.go.txt` is the replacement source; its suffix keeps `go test
./...` from treating the patch directory as a standalone Go package. This does not
claim other third-party code is free of races.

Verification: `python3 scripts/test_crs_go.py`, then run the wrapper with
`test -race -short -tags=unit ./internal/clashcore ./internal/clashruntime`.

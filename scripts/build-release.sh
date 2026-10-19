#!/bin/sh
# Build self-contained release archives after make webui-build webui-verify.
set -eu

: "${RELEASE_TAG:?RELEASE_TAG is required}"
case "$RELEASE_TAG" in
	*[!a-zA-Z0-9._-]*|'') echo 'error: invalid RELEASE_TAG' >&2; exit 1 ;;
esac

# Refuse to mix new archives with stale artifacts from an earlier run.
mkdir release
work_dir=$(mktemp -d)
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

for target_os in linux darwin windows; do
	for target_arch in amd64 arm64; do
		name="bqagent_${RELEASE_TAG}_${target_os}_${target_arch}"
		stage="$work_dir/$name"
		mkdir "$stage"
		binary=bqagent
		if [ "$target_os" = windows ]; then binary=bqagent.exe; fi
		CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" \
			go build -trimpath -ldflags '-s -w' -o "$stage/$binary" ./cmd/agent
		cp README.md README_CN.md "$stage/"
		if [ "$target_os" = windows ]; then
			archive="$PWD/release/$name.zip"
			(cd "$stage" && zip -q "$archive" "$binary" README.md README_CN.md)
		else
			COPYFILE_DISABLE=1 tar -czf "release/$name.tar.gz" -C "$stage" "$binary" README.md README_CN.md
		fi
	done
done

(cd release && shasum -a 256 ./*.tar.gz ./*.zip > SHA256SUMS)

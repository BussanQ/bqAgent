import hashlib
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
import zipfile

from release_version import next_version


class ReleaseVersionTest(unittest.TestCase):
    def test_first_release(self):
        self.assertEqual(next_version([]), "v0.0.1")

    def test_increment_highest_numeric_version(self):
        self.assertEqual(next_version(["v0.0.9", "v0.0.10", "v0.0.2"]), "v0.0.11")
        self.assertEqual(next_version(["v0.9.99", "v1.0.0"]), "v1.0.1")

    def test_ignore_legacy_and_non_version_tags(self):
        self.assertEqual(next_version(["build-20261019-123-1", "v1.0.0-rc1", "other"]), "v0.0.1")

    def test_combined_tags_and_draft_releases(self):
        self.assertEqual(next_version(["v0.0.1\n", "v0.0.1", "v0.0.2\n"]), "v0.0.3")

    def test_cli_reads_tags_from_stdin(self):
        result = subprocess.run(
            [os.sys.executable, str(Path(__file__).with_name("release_version.py"))],
            input="v0.0.1\nv0.0.2\n", capture_output=True, text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "v0.0.3\n")


SCRIPT = Path(__file__).with_name("build-release.sh").resolve()


class ReleasePackagingTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        tools = self.root / "tools"
        tools.mkdir()
        # Substitute only the compiler; exercise real archives and checksums.
        compiler = tools / "go"
        compiler.write_text(
            '#!/bin/sh\nset -eu\n'
            '[ "${FAIL_BUILD:-0}" != 1 ] || exit 7\n'
            'while [ "$1" != -o ]; do shift; done\n'
            'printf "%s/%s cgo=%s" "$GOOS" "$GOARCH" "$CGO_ENABLED" > "$2"\n'
            'chmod +x "$2"\n'
        )
        compiler.chmod(0o755)
        for name in ("README.md", "README_CN.md"):
            (self.root / name).write_text(name)
        self.env = dict(os.environ, PATH=f"{tools}:{os.environ['PATH']}",
                        RELEASE_TAG="build-test")

    def run_build(self):
        return subprocess.run(["sh", str(SCRIPT)], cwd=self.root,
                              env=self.env, capture_output=True, text=True)

    def test_archives_contain_each_platform_and_valid_checksums(self):
        result = self.run_build()
        self.assertEqual(result.returncode, 0, result.stderr)
        release = self.root / "release"
        self.assertEqual(len(list(release.iterdir())), 7)
        for target_os in ("linux", "darwin", "windows"):
            for arch in ("amd64", "arm64"):
                name = f"bqagent_build-test_{target_os}_{arch}"
                expected = f"{target_os}/{arch} cgo=0".encode()
                if target_os == "windows":
                    with zipfile.ZipFile(release / f"{name}.zip") as archive:
                        self.assertEqual(set(archive.namelist()),
                                         {"bqagent.exe", "README.md", "README_CN.md"})
                        self.assertEqual(archive.read("bqagent.exe"), expected)
                else:
                    with tarfile.open(release / f"{name}.tar.gz") as archive:
                        self.assertEqual(set(archive.getnames()),
                                         {"bqagent", "README.md", "README_CN.md"})
                        self.assertEqual(archive.extractfile("bqagent").read(), expected)
                        self.assertTrue(archive.getmember("bqagent").mode & 0o111)
        entries = (release / "SHA256SUMS").read_text().splitlines()
        self.assertEqual(len(entries), 6)
        for entry in entries:
            digest, filename = entry.split()
            self.assertEqual(digest, hashlib.sha256((release / filename).read_bytes()).hexdigest())

    def test_compiler_failure_stops_packaging(self):
        self.env["FAIL_BUILD"] = "1"
        result = self.run_build()
        self.assertEqual(result.returncode, 7)
        self.assertEqual(list((self.root / "release").iterdir()), [])

    def test_existing_output_is_preserved_and_rejected(self):
        (self.root / "release").mkdir()
        marker = self.root / "release" / "keep"
        marker.write_text("existing")
        self.assertNotEqual(self.run_build().returncode, 0)
        self.assertEqual(marker.read_text(), "existing")

    def test_invalid_or_missing_tag_is_rejected(self):
        for tag in ("", "../bad", "bad tag"):
            with self.subTest(tag=tag):
                self.env["RELEASE_TAG"] = tag
                self.assertNotEqual(self.run_build().returncode, 0)
                self.assertFalse((self.root / "release").exists())


if __name__ == "__main__":
    unittest.main()

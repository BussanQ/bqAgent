"""Choose the next patch version from existing Git tags and Release tags."""
import re
import sys


def next_version(tags):
    versions = []
    for tag in tags:
        match = re.fullmatch(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", tag.strip())
        if match:
            versions.append(tuple(map(int, match.groups())))
    major, minor, patch = max(versions, default=(0, 0, 0))
    return f"v{major}.{minor}.{patch + 1}"


if __name__ == "__main__":
    print(next_version(sys.stdin))

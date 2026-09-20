#!/usr/bin/env python3
"""Fails when an environment variable read by the code is not documented in docs/configuration.md,
or when the documentation names a variable no code reads (NFR-O5). Run by `make docs-check`."""
import pathlib
import re
import sys

root = pathlib.Path(__file__).resolve().parents[3]
doc = (root / "docs" / "configuration.md").read_text()
documented = set(re.findall(r"`((?:NK|MX)_[A-Z0-9_]+|APP_HOST|SHARE_HOST|CORE_USER_UPSTREAM|CORE_PUBLIC_UPSTREAM)`", doc))

read = set()
for base in (root / "core", root / "bots"):
    for path in base.rglob("*.go"):
        if path.name.endswith("_test.go") or "/gen/" in str(path) or "botclient" in str(path):
            continue
        text = path.read_text()
        read |= set(re.findall(r'"((?:NK|MX)_[A-Z0-9_]+)"', text))
# The web image's variables are read by the nginx templates.
for tpl in (root / "deploy" / "nginx").glob("*.template"):
    read |= set(re.findall(r"\$\{([A-Z_]+)\}", tpl.read_text()))

# Variables that only tests and scripts read are documented in their own section.
tooling = {"NK_TEST_LOG", "NK_TEST_VERBOSE", "NK_TEST_PG_IMAGE", "NK_TEST_PG_IMAGES", "NK_TEST_SYNAPSE_IMAGE", "NK_PERF_NOTES"}
missing = sorted(v for v in read if v not in documented)
unknown = sorted(v for v in documented if v not in read and v not in tooling)
if missing:
    print("Read by the code but not documented in docs/configuration.md:", ", ".join(missing))
if unknown:
    print("Documented in docs/configuration.md but read by no code:", ", ".join(unknown))
if missing or unknown:
    sys.exit(1)
print(f"configuration.md covers all {len(read)} variables")

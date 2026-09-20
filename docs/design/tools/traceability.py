#!/usr/bin/env python3
"""Generate docs/design/09-traceability.md.

Reads every requirement ID (with its priority) from requirements.md and
security-requirements.md and joins it with the MAP below: which design section
implements it and which test tier verifies it. Exits non-zero if a requirement
has no mapping, or a Must has no design reference, so the design cannot silently
fall behind the requirements.

Usage: python3 docs/design/tools/traceability.py
"""
import re
import sys
from pathlib import Path

DOCS = Path(__file__).resolve().parents[2]
OUT = DOCS / "design" / "09-traceability.md"

# Test tiers
U, I, E, A, C, R = "Unit", "Integration", "E2E", "Authz matrix", "CI check", "Review"

DEFERRED = "deferred"  # design leaves room; not designed in v1 (Could / later)

# id -> (design reference, tiers)  -- filled by rows() below
MAP = {}


def rows(ids, ref, tiers):
    for i in ids.split():
        MAP[i] = (ref, tiers)


# ------------------------------------------------------------------ functional
rows("CORE-N1", "04 §2.1; 02 §1.2", f"{I}")
rows("CORE-N2", "04 §2.1; 01 §4.1", f"{I}, {E}")
rows("CORE-N3", "02 §1.2; 01 §4.1, §7", f"{I}, {E}")
rows("CORE-N4", "01 §4.1, §7", f"{U}, {I}")
rows("CORE-N5", "07 §5; 02 §1.2", f"{U}, {E}")
rows("CORE-N6 CORE-N7 CORE-N8 CORE-N9", "01 §4.1; 02 §1.2; 07 §4", f"{I}, {E}")
rows("CORE-N10", "02 §1.2; 01 §6.2", f"{I}")
rows("CORE-N11", "01 §13 (no purge job exists)", f"{R}")
rows("CORE-N13", "01 §8; 02 §1.2", f"{I}")
rows("CORE-N14", "02 §1.2", f"{I}")
rows("CORE-N15", DEFERRED + ": batch endpoints can be added to 02 §1.2", "-")
rows("CORE-N17", "07 §5; 01 §4.2 (history coalescing)", f"{U}, {E}")
rows("CORE-N18", "04 §2.1; 01 §4.1", f"{U}, {I}")
rows("CORE-P1 CORE-P2 CORE-P3 CORE-P5", "02 §1.2; 01 §4.1", f"{I}, {E}")
rows("CORE-P4", "01 §4.1 (FK set null); 02 §1.2", f"{I}")
rows("CORE-P6", "01 §4.1 (`pages.archived_at`)", f"{I}")
rows("CORE-P7", DEFERRED + " (Could)", "-")
rows("CORE-A1", "02 §1.3; 01 §6", f"{A}")
rows("CORE-A2 CORE-A8", "01 §6, §6.1", f"{I}")
rows("CORE-A3", "01 §6.2", f"{I}")
rows("CORE-A4", "02 §1.3 (no processing)", f"{R}")
rows("CORE-A5 CORE-A7", "01 §6.3", f"{I}")
rows("CORE-A6", "01 §6.1; 02 §4", f"{I} (streaming memory test)")
rows("CORE-A9", "04 §2.1", f"{I}")
rows("CORE-S1 CORE-S2", "05 §1, §2", f"{I}, {E}")
rows("CORE-S3", "01 §5; 02 §1.5", f"{I}")
rows("CORE-S4", "README §3; 02 §1.2", f"{I}")
rows("CORE-S5", "README §3; 01 §11", f"{I}")
rows("CORE-R1", "02 §1.4; 01 §9", f"{I}")
rows("CORE-R2", "01 §2; 04 §3.1", f"{U}")
rows("CORE-R3 CORE-R5 CORE-R10", "05 §5.1", f"{I}")
rows("CORE-R4", "05 §4.2", f"{I}, {E}")
rows("CORE-R6", "05 §5.3; 06 §7", f"{E}")
rows("CORE-R7", "05 §5.2", f"{I}")
rows("CORE-R8", "05 §5.4; 02 §1.4", f"{I}")
rows("CORE-R9", "05 §5; 07 §4", f"{E}")
rows("CORE-R11", "04 §3", f"{I}, {E}")
rows("CORE-R13", "04 §3.1", f"{U}")
rows("CORE-SH1 CORE-SH2", "03 §6; 01 §10; 02 §1.4", f"{I}")
rows("CORE-SH3", "02 §1.4; 03 §6", f"{I}, {E}")
rows("CORE-SH4", "02 §3; 07 §7", f"{I}, {E}")
rows("CORE-SH5", "01 §10 (live join)", f"{I}")
rows("CORE-SH6", "03 §6; 02 §3", f"{I}")
rows("CORE-SH7", "02 §3; 03 §6", f"{A}, {I}")
rows("CORE-SH8", "07 §7; 08 §1", f"{I}, {E}")
rows("CORE-SH9", "02 §3; README §3", f"{I}")
rows("CORE-SH10", "07 §7; 08 §1", f"{E}")
rows("CORE-SH11", "08 §3 (`NK_SHARE_ENABLED`)", f"{I}")
rows("CORE-SH12", "03 §8; 01 §10", f"{I}")
rows("CORE-SH13", DEFERRED + " (Could)", "-")
rows("CORE-SH14", "02 §1.4", f"{I}")

rows("AUTH-U1", "README §3 (RLS); 01 §1", f"{A}, {I}")
rows("AUTH-U2", "01 §2", f"{I}")
rows("AUTH-U3", "03 §4.2", f"{I}")
rows("AUTH-U4", "02 §1.1; 03 §2.5", f"{I}, {E}")
rows("AUTH-U5", "02 §5", f"{I}")
rows("AUTH-U6", "02 §1.6; 03 §4", f"{I}, {E}")
rows("AUTH-U7", "03 §4.1; 01 §2", f"{I}")
rows("AUTH-U8", "03 §4.2", f"{I}, {E}")
rows("AUTH-U9", "03 §4.3", f"{I}")
rows("AUTH-U10", "03 §2.5; 02 §1.1", f"{I}, {E}")
rows("AUTH-U11", "03 §7", f"{I}, {E}")
rows("AUTH-C1", "03 §3, §4.2", f"{I}")
rows("AUTH-C3", "03 §2.1, §2.2", f"{I}")
rows("AUTH-C4", "03 §2.2, §2.4", f"{I}")
rows("AUTH-C5", "03 §2.1", f"{I}")
rows("AUTH-C6", "03 §3", f"{I}")
rows("AUTH-C7 AUTH-C8", DEFERRED + ": the login flow (03 §3) has a single verification step to extend", "-")
rows("AUTH-C9 AUTH-C11", "03 §2.2", f"{I}, {E}")
rows("AUTH-C10", "03 §2.3; 07 §3", f"{I}, {E}")
rows("AUTH-B1 AUTH-B2", "03 §5.1; 01 §3", f"{A}, {I}")
rows("AUTH-B3 AUTH-B5", "03 §5.2", f"{I}, {E}")
rows("AUTH-B4", "01 §3; 03 §5.2", f"{I}")
rows("AUTH-B6", "03 §5.3", f"{I}")
rows("AUTH-B7", "01 §4.2 (source references)", f"{I}")
rows("AUTH-B8", "03 §8", f"{I}")
rows("AUTH-B9", DEFERRED + " (Could)", "-")

rows("BOT-1", "02 §2; README §2", f"{A}")
rows("BOT-2", "02 §2; 03 §5.3", f"{I}")
rows("BOT-3 BOT-4 BOT-5", "04 §1", f"{I}")
rows("BOT-6", "02 §2; 01 §6.2", f"{I}")
rows("BOT-7", "04 §2; 01 §11", f"{I}")
rows("BOT-8", "02 §2 (feedback object)", f"{I}")
rows("BOT-9", "04 §2; README §3 (user lock)", f"{I}")
rows("BOT-10", "02 §2 (`/conversations/{id}/cursor`); 01 §4.2 (index)", f"{I}")
rows("BOT-11", "05 §4.2; 02 §2", f"{I}")
rows("BOT-12", "01 §3; 05 §4.1", f"{I}")
rows("BOT-13", "05 §4.3; 06 §7", f"{I}, {E}")
rows("BOT-14", "04 §3; 02 §2", f"{I}")
rows("BOT-15", "05 §5.3; 02 §2", f"{A}, {I}")
rows("BOT-16", "05 §4.1; 03 §4.3", f"{I}, {E}")
rows("BOT-B1", "04; 06 (bots contain platform logic only)", f"{R}")
rows("BOT-B2 BOT-B4", "06 §3; 04 §2", f"{E}")
rows("BOT-B3", "06 §6", f"{E}")
rows("BOT-B5", "06 §1; 01 §12", f"{E}")
rows("BOT-B6", "06 §9", f"{I}")
rows("BOT-B7", "06 §7; 05 §4.1", f"{E}")

rows("GRP-1 GRP-2 GRP-3 GRP-4 GRP-5 GRP-6 GRP-7", "04 §2.1, §4", f"{U}, {I}")
rows("GRP-8", "01 §4.2 (`attach_reason`)", f"{U}")
rows("GRP-9", "04 §5", f"{U}")
rows("GRP-10", "02 §1.2 (merge, split)", f"{I}")
rows("GRP-11", DEFERRED + " (Could)", "-")
rows("EDT-1", "01 §4.2; 04 §2.1", f"{I}")
rows("EDT-2 EDT-5 EDT-6 EDT-7", "04 §2.2", f"{U}, {I}")
rows("EDT-3", "01 §4.2; 04 §2.2", f"{I}")
rows("EDT-4", "04 §2.3", f"{I}")
rows("EDT-8", "04 §2.2", f"{I}")

rows("MX-1 MX-2", "06 §4", f"{E}")
rows("MX-3", "06 §2, §8", f"{E}")
rows("MX-4 MX-5 MX-6", "06 §5", f"{U}, {E}")
rows("MX-7 MX-8", "06 §6", f"{E}")
rows("MX-9", "06 §3", f"{E}")
rows("MX-10", "01 §3; 04 §2", f"{I}")
rows("MX-11", DEFERRED + " (Could)", "-")
rows("MX-12", "06 §7; 05 §5.3", f"{E}")
rows("MX-13", "06 §7", f"{E}")
rows("MX-N1", "06 §1, §2, §8", f"{E}")
rows("MX-N2", "06 §2", f"{E}")
rows("MX-N3", "06 §10 (Synapse is the reference server)", f"{E}")

rows("WEB-1 WEB-2", "07 §2", f"{E}")
rows("WEB-3 WEB-5", "07 §4, §6", f"{E}")
rows("WEB-4", "07 §6; 02 §1.2", f"{E}")
rows("WEB-6 WEB-7", "07 §4", f"{E}")
rows("WEB-8", "07 §2, §5", f"{U}, {E}")
rows("WEB-9", "02 §1.2, §1.3", f"{E}")
rows("WEB-10", "02 §1.2", f"{E}")
rows("WEB-11", "07 §4; 05 §2", f"{E}")
rows("WEB-12", "07 §1; 02 §1.1", f"{E}")
rows("WEB-13", "02 §1.2 (history); 07 §1", f"{E}")
rows("WEB-14", "01 §8; 02 §1.2", f"{I}, {E}")
rows("WEB-15", "02 §1.2", f"{E}")
rows("WEB-16", "07 §2", f"{E}")
rows("WEB-17", DEFERRED + " (Could)", "-")
rows("WEB-18", "05 §5; 02 §1.4", f"{E}")
rows("WEB-19", "02 §1.6; 07 §1", f"{E}")
rows("WEB-20", "07 §5", f"{E}")
rows("WEB-21", "02 §1.4; 03 §6", f"{E}")
rows("WEB-N1", "07 §2", f"{E}")
rows("WEB-N2", "02 §1; 07 §1", f"{R}")
rows("WEB-N3", "07 §9; 02 §1.2 (board endpoint)", f"{E}")
rows("WEB-N4", "07 §6", f"{E} (axe)")
rows("WEB-N5", "07 §5; 02 §1.3", f"{U}, {I}")
rows("WEB-N6", "07 §10", f"{E}")
rows("WEB-N7", "not a requirement", "-")
rows("WEB-N8", "07 §8", f"{E}")

rows("AND-1", "02 (all functionality in the user API)", f"{R}")
rows("AND-2", "03 §2.2 (native token mode reserved)", f"{R}")
rows("AND-3", "01 §5; 02 §1.5", f"{R}")
rows("AND-4", "05 §2", f"{R}")
rows("AND-5", "02 §1.3", f"{R}")
rows("AND-6", "02 §1.2 (note creation with attachments)", f"{R}")
rows("AND-7", "02 (versioning)", f"{R}")

# ------------------------------------------------------------------ non-functional
rows("NFR-D1 NFR-D2", "README §1; 05", f"{I}")
rows("NFR-D3 NFR-D6", "08 §2", f"{C}")
rows("NFR-D4", "01 §12; 08 §2", f"{I}")
rows("NFR-D5", "README §2; 08 §1", f"{R}")
rows("NFR-D7", "02 (independent API versions)", f"{R}")
rows("NFR-D8", "01 §1; 08 §5", f"{I} (16 and newest)")
rows("NFR-X1", "02 §2 (bot contract); tech-stack §2 (`bots/sdk`)", f"{R}")
rows("NFR-X2", "01 §3", f"{I}")
rows("NFR-X3", "01 §4.2 (`note_parts.kind`)", f"{R}")
rows("NFR-S1", "08 §1; 03 §1", f"{C}")
rows("NFR-S2", "08 §2; 01 §12", f"{C}, {I}")
rows("NFR-S3", "README §3 (RLS)", f"{A}, {I}")
rows("NFR-S4", "README §3; 02", f"{I}")
rows("NFR-S5", "documentation deliverable (user docs state that the server can read notes)", f"{R}")
rows("NFR-S6", "08 §6; 03 §8", f"{C}")
rows("NFR-S7", "README §3 (logging); 02 §1.1, §5", f"{I}")
rows("NFR-R1", "04 §2; 06 §3", f"{I}, {E}")
rows("NFR-R2", "04 §2", f"{I}")
rows("NFR-R3", "01 §4.1", f"{I}")
rows("NFR-R4", "08 §4", f"{R}")
rows("NFR-R5", "08 §2 (two replicas)", f"{R}")
rows("NFR-P1", "04; 05", f"{E}")
rows("NFR-P2", "01 (indexes); 02 §1.2", f"{I}")
rows("NFR-P3", "README §3; 01", f"{R}")
rows("NFR-P4", "README §3 (pagination)", f"{I}")
rows("NFR-O1", "README §3; 08 §7", f"{I}")
rows("NFR-O2", "08 §7", f"{I}")
rows("NFR-O4", "08 §2", f"{I}")
rows("NFR-O5", "08 §3", f"{R}")
rows("NFR-API1", "02", f"{C}")
rows("NFR-API2", "02 (versioning)", f"{R}")
rows("NFR-API3", "README §3", f"{I}")
rows("NFR-API4", "02 §1.3, §4", f"{I}")
rows("NFR-Q1", "04 §4; 08 §5", f"{U}")
rows("NFR-Q2", "08 §5", f"{C}")
rows("NFR-Q3", "07 §9", f"{R}")
rows("NFR-Q4", "documentation deliverable (bot-writing guide, deployment, backup)", f"{R}")
rows("NFR-Q5", "08 §5; tech-stack §9", f"{C}")

# ------------------------------------------------------------------ security
rows("SEC-AUTH-1 SEC-AUTH-2 SEC-AUTH-3", "03 §3", f"{I}")
rows("SEC-AUTH-4 SEC-AUTH-5 SEC-AUTH-11", "03 §4.2", f"{I}")
rows("SEC-AUTH-6", "03 §2.1, §2.5", f"{I}")
rows("SEC-AUTH-7 SEC-AUTH-15", "03 §2.3", f"{I}, {E}")
rows("SEC-AUTH-8", "03 §2.4", f"{I}")
rows("SEC-AUTH-9", "03 §2.2", f"{I}")
rows("SEC-AUTH-10", "03 §2.2 (no OAuth endpoints exist in v1)", f"{R}")
rows("SEC-AUTH-12", "03 §4.2; 07 §1 (fragments)", f"{I}")
rows("SEC-AUTH-13", "03 §4.1; 01 §2", f"{I}")
rows("SEC-AUTH-14", DEFERRED + " with AUTH-C7", "-")
rows("SEC-AUTH-16", "03 §2.5; 02 §1.1", f"{I}")
rows("SEC-ISO-1", "08 §5 (generated matrix); README §3", f"{A}")
rows("SEC-ISO-2 SEC-ISO-3 SEC-ISO-4", "01 §1 (composite keys); README §3", f"{A}, {I}")
rows("SEC-ISO-5", "05 §2", f"{I}")
rows("SEC-ISO-6", "02 (request schemas carry only client-settable fields)", f"{I}")
rows("SEC-ISO-7", "03 §1", f"{A}")
rows("SEC-ISO-8", "README §3 (idempotency scoped per caller)", f"{I}")
rows("SEC-ISO-9", "01 §6.3", f"{I}")
rows("SEC-ISO-10", "README §3 (RLS)", f"{I}")
rows("SEC-ADM-1", "02 §1.6; README §3", f"{A}")
rows("SEC-ADM-2", "03 §4.2", f"{I}")
rows("SEC-ADM-3", "03 §8", f"{I}")
rows("SEC-ADM-4", "02 §1.6; 03 §1", f"{A}")
rows("SEC-BOT-1 SEC-BOT-2", "03 §5.1; README §2", f"{A}")
rows("SEC-BOT-3", "03 §5.3; 05 §5.3", f"{A}, {I}")
rows("SEC-BOT-4 SEC-BOT-5 SEC-BOT-6", "03 §5.2", f"{I}")
rows("SEC-BOT-7", "03 §5.2; 04 §2", f"{I}")
rows("SEC-BOT-8", "03 §5.1", f"{I}")
rows("SEC-BOT-9", "04 §2", f"{I}")
rows("SEC-BOT-10", "README §3 (rate limits)", f"{I}")
rows("SEC-BOT-11", "01 §3; 04 §2", f"{I}")
rows("SEC-BOT-12", "04 §3; 06 §6", f"{U}, {E}")
rows("SEC-BOT-13", "03 §4.3; 05 §4.2", f"{I}")
rows("SEC-SHR-1", "01 §10", f"{I}")
rows("SEC-SHR-2 SEC-SHR-3", "03 §6; 02 §3", f"{I}")
rows("SEC-SHR-4", "02 §3", f"{A}")
rows("SEC-SHR-5", "08 §1; 07 §7", f"{E}, {I} (headers)")
rows("SEC-SHR-6 SEC-SHR-7", "03 §6; 07 §7", f"{I}")
rows("SEC-SHR-8 SEC-SHR-9", "02 §3, §1.4", f"{I}")
rows("SEC-SHR-10", "07 §7", f"{E}")
rows("SEC-SHR-11", "03 §6", f"{I}")
rows("SEC-CNT-1 SEC-CNT-2", "07 §5", f"{U}, {E}")
rows("SEC-CNT-3 SEC-CNT-4", "02 §1.3", f"{I}")
rows("SEC-CNT-5", "02 §1.3 (no media processing)", f"{R}")
rows("SEC-CNT-6", "01 §6.2", f"{I} (races)")
rows("SEC-CNT-7", "05 §5.3", f"{U}, {E}")
rows("SEC-CNT-8", "06 §5", f"{E}")
rows("SEC-CNT-9", DEFERRED + ": an upload-completion hook point exists in 01 §6.2 step 4", "-")
rows("SEC-API-1", "01 §8 (query builder); sqlc", f"{I}")
rows("SEC-API-2", "README §2; 08 §5", f"{A}")
rows("SEC-API-3", "04 §1; README §3", f"{I}")
rows("SEC-API-4", "README §3; 05 §2; 03 §3", f"{I}")
rows("SEC-API-5", "README §3", f"{I}")
rows("SEC-API-6", "03 §2.4", f"{I}")
rows("SEC-API-7", "README §3; 01 §6.2", f"{I}")
rows("SEC-API-8", "07 §7; 08 §1", f"{I}")
rows("SEC-DATA-1", "README §3", f"{I} (log scan)")
rows("SEC-DATA-2", "03 §1; 06 §8", f"{I}")
rows("SEC-DATA-3 SEC-DATA-4", "08 §1", f"{C}")
rows("SEC-DATA-5", "03 §4.3", f"{I}")
rows("SEC-DATA-6", "02 §1.3; 07 §8", f"{I}")
rows("SEC-DATA-7", "08 §4", f"{R}")
rows("SEC-DATA-8", "02 §5", f"{I}")
rows("SEC-MX-1", "06 §4", f"{E}")
rows("SEC-MX-2", "06 §7", f"{E}")
rows("SEC-MX-3", "06 §8; 06 §1", f"{I} (log scan)")
rows("SEC-MX-4", "06 §2", f"{E}")
rows("SEC-MX-5", "06 §8", f"{R}")
rows("SEC-MX-6", "06 §8", f"{I}")
rows("SEC-OPS-1 SEC-OPS-7", "08 §2", f"{C}")
rows("SEC-OPS-2 SEC-OPS-3 SEC-OPS-8", "08 §2, §1", f"{C}")
rows("SEC-OPS-4", "README §2; 08 §1", f"{C}")
rows("SEC-OPS-5", "01 §12", f"{I}")
rows("SEC-OPS-6", "08 §6", f"{C}")
rows("SEC-AUD-1 SEC-AUD-2", "03 §8; 01 §11", f"{I}")
rows("SEC-AUD-3", "08 §2, §7", f"{R}")
rows("SEC-AUD-4", "03 §7", f"{I}, {E}")
rows("SEC-BASE-1", "03 §3", f"{U}")
rows("SEC-BASE-2", "03 §2, §5.2, §6", f"{U}")
rows("SEC-BASE-3", "08 §1", f"{C}")
rows("SEC-BASE-4", "08 §3 (key ids, credential rotation)", f"{R}")
rows("SEC-BASE-5", "08 §3", f"{R}")


def read_requirements():
    found = {}
    for name in ("requirements.md", "security-requirements.md"):
        text = (DOCS / name).read_text()
        for m in re.finditer(
            r"^\| ((?:CORE|AUTH|BOT|GRP|EDT|MX|WEB|AND|NFR|SEC)-[A-Za-z0-9-]+) \| (?:(Must|Should|Could|—) \| )?(?:(unimplemented|implemented|fully tested) \| )?",
            text,
            re.M,
        ):
            found[m.group(1)] = (name, m.group(2) or "—", m.group(3) or "-")
    return found


REPO = DOCS.parent
TEST_GLOBS = ("**/*_test.go", "**/*.test.ts", "**/*.test.tsx", "e2e/**/*.spec.ts")
SKIP_DIRS = {"node_modules", ".git", ".bin", "dist"}


def test_files():
    for pattern in TEST_GLOBS:
        for f in REPO.glob(pattern):
            if not (SKIP_DIRS & set(f.parts)):
                yield f


DECLARATION = re.compile(r"^\s*(?:func Test\w+|(?:test|it|describe)(?:\.\w+)?\(|t\.Run\()")
COMMENT = re.compile(r"^\s*(?://|#|\*|/\*)")


def citations(ids):
    """requirement id -> number of test files that cite it where it counts: in a test's name or title, in the
    comment block directly above it, or inside its body (comments and assertion messages). An ID that only
    appears in a file-level comment or in a helper is not evidence that a test checks it."""
    counts = {i: 0 for i in ids}
    alternation = "|".join(re.escape(i) for i in sorted(ids, key=len, reverse=True))
    pattern = re.compile(r"(?<![A-Za-z0-9-])(" + alternation + r")(?![A-Za-z0-9-])")
    for f in test_files():
        try:
            lines = f.read_text().splitlines()
        except (UnicodeDecodeError, OSError):
            continue
        found = set()
        block = []
        in_test = False
        for line in lines:
            if line.startswith("func "):  # a Go function: inside a test only when it is one
                in_test = line.startswith("func Test")
            elif not line.startswith(("\t", " ", "}", ")", "//", "#", "/*", "*")) and line.strip():
                in_test = in_test and f.suffix in (".ts", ".tsx")
            if COMMENT.match(line):
                block.append(line)
                if in_test:
                    found.update(pattern.findall(line))
                continue
            if DECLARATION.match(line):
                in_test = in_test or not line.lstrip().startswith("func ")
                found.update(pattern.findall("\n".join(block + [line])))
            elif in_test:
                found.update(pattern.findall(line))  # assertions and their messages inside a test
            block = []
        for i in found:
            counts[i] += 1
    return counts


def sort_key(i):
    parts = re.split(r"(\d+)", i)
    return [int(p) if p.isdigit() else p for p in parts]


def main():
    reqs = {i: (a, b) for i, (a, b, _) in read_requirements().items()}
    states = {i: st for i, (_, _, st) in read_requirements().items()}
    cited = citations(list(reqs))
    uncited = sorted((i for i, st in states.items() if st == "fully tested" and cited[i] == 0), key=sort_key)
    unmapped = sorted(i for i in reqs if i not in MAP)
    stale = sorted(i for i in MAP if i not in reqs)
    orphan_musts = sorted(
        i for i, (_, pri) in reqs.items()
        if pri == "Must" and i in MAP and MAP[i][0].startswith(DEFERRED)
    )

    lines = []
    w = lines.append
    w("# 9. Traceability: requirement → design → test")
    w("")
    w("Generated by `docs/design/tools/traceability.py`; edit the mapping in that script, not this file. "
      "Run it after changing requirements or the design: it fails if a requirement has no mapping, "
      "if a **Must** is only \"deferred\", if the mapping names a requirement that no longer exists, "
      "or if a requirement marked `fully tested` is cited by no test file (an ID named in a test's comment, name or message).")
    w("")
    w("**Implemented in** points at design sections (`01` = [01-data-model.md](01-data-model.md), and so on; "
      "`README` = [README.md](README.md)). **Verified by** names the test tier from "
      "[08](08-deployment-and-testing.md) §5. `Review` means verified by inspection of the design or "
      "documentation, not by an automated test; `CI check` means a lint, scan or manifest check in `make check`. "
      "`deferred` means the design keeps room for it but does not design it in v1 (only allowed for Should and Could).")
    w("")
    counts = {}
    for i, (_, pri) in reqs.items():
        counts[pri] = counts.get(pri, 0) + 1
    w(f"Requirements: {len(reqs)} ({counts.get('Must', 0)} Must, {counts.get('Should', 0)} Should, "
      f"{counts.get('Could', 0)} Could, {counts.get('—', 0)} without priority). "
      f"Deferred in v1: {sum(1 for i in reqs if i in MAP and MAP[i][0].startswith(DEFERRED))}.")
    w("")

    def section(title, prefixes, filename):
        ids = sorted((i for i in reqs if reqs[i][0] == filename and i.startswith(prefixes)), key=sort_key)
        if not ids:
            return
        w(f"## {title}")
        w("")
        w("| ID | Pri | State | Implemented in | Verified by | Tests citing it |")
        w("|---|---|---|---|---|---|")
        for i in ids:
            ref, tiers = MAP.get(i, ("**MISSING**", "-"))
            w(f"| {i} | {reqs[i][1]} | {states[i]} | {ref} | {tiers} | {cited[i]} |")
        w("")

    section("Core", ("CORE-",), "requirements.md")
    section("Accounts and authentication", ("AUTH-",), "requirements.md")
    section("Bot contract, grouping and edits", ("BOT-", "GRP-", "EDT-"), "requirements.md")
    section("Matrix bot", ("MX-",), "requirements.md")
    section("Web app", ("WEB-",), "requirements.md")
    section("Android constraints", ("AND-",), "requirements.md")
    section("Non-functional", ("NFR-",), "requirements.md")
    section("Security", ("SEC-",), "security-requirements.md")

    OUT.write_text("\n".join(lines) + "\n")

    problems = False
    if unmapped:
        print("Requirements without a mapping:", ", ".join(unmapped))
        problems = True
    if stale:
        print("Mapping entries for unknown requirements:", ", ".join(stale))
        problems = True
    if orphan_musts:
        print("Musts mapped only as deferred:", ", ".join(orphan_musts))
        problems = True
    if uncited:
        print("Marked `fully tested` but cited by no test (name the ID in a test comment or name):", ", ".join(uncited))
        problems = True
    print(f"wrote {OUT.relative_to(DOCS.parent)}: {len(reqs)} requirements")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())

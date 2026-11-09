# Redacted Malenia collaboration session

## Scope

This document summarizes a recent development session about Malenia, a deterministic CI-failure diagnosis tool with optional AI-assisted analysis and experimental repair.

No passwords, API keys, access tokens, private repository data, or personal contact information are included.

## How the developer works

- Starts with a broad product goal, then pressure-tests the implementation against the actual repository.
- Cares strongly about honest product claims and rejects unsupported labels such as deterministic simulation testing when the implementation only performs repeated reproduction.
- Prefers deterministic diagnosis as the foundation, with LLM assistance optional and evidence-constrained.
- Focuses on real GitHub Actions behavior: failed jobs, original logs, exact base and head revisions, artifacts, PR comments, permissions, and untrusted-code isolation.
- Wants onboarding to be usable by another developer, not merely a repository someone must copy.
- Iterates quickly from observed CLI or workflow output, asking why the output is incomplete and what evidence is missing.
- Uses self-testing deliberately, including seeded failures in Malenia itself, while distinguishing a successful self-test from proof of general accuracy.
- Values small, reviewable changes, tests, external validation, and documentation that does not overclaim.

## Product direction discussed

Malenia should:

1. Reproduce a CI failure in an isolated environment.
2. Compare base and head revisions.
3. Preserve original CI logs as evidence.
4. Classify failures deterministically.
5. Localize likely commits or files.
6. Abstain when evidence is insufficient.
7. Optionally run controlled causal experiments for narrow candidates.
8. Optionally ask an LLM to propose a patch.
9. Apply and test that patch in isolation.
10. Report the patch and verification result to the user rather than silently opening a new PR.

## Important distinctions established

- A suspected cause is not an evidence-backed cause.
- Causal attribution is separate from mechanism verification.
- Passing a patched test does not prove the patch is universally correct.
- Repeated execution is not deterministic simulation testing.
- A seeded intentional failure validates integration plumbing, not broad diagnosis accuracy.
- Repair is experimental and optional; diagnosis remains deterministic and diagnosis-first.

## Evaluation work

The repository gained a frozen evaluation-corpus structure with strict metadata validation, a runner input that cannot read expected answers, scorer outcomes for correct, wrong, justified abstention, and unjustified abstention, and a freeze command.

The first three manually documented cases cover:

- A pre-existing failure in a Rust repository.
- A Bazel timeout after a large storage change.
- A dependency lockfile update whose failing log does not establish causality.

The cases preserve uncertainty instead of forcing unsupported root-cause claims.

## Working principles

- Inspect the real repository before designing abstractions.
- Verify workflow payloads and command provenance instead of guessing.
- Do not expose secrets to untrusted code.
- Do not overwrite user files without permission.
- Do not treat a green structural test as proof of a live external integration.
- Prefer evidence and reproducible tests over impressive descriptions.
- Keep the public README concise, accurate, and clear about limitations.

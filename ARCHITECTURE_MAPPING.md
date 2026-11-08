# Malenia architecture mapping

The existing repository already provides a Go CLI, SQLite persistence, content
addressed artifacts, durable lifecycle events, isolated execution, deterministic
reproduction/classification/localization, optional model analysis, and an
experimental repair path. Those remain the product's working foundation.

This implementation adds the missing explicit boundaries from the architecture
specification:

| Specification area | Existing implementation | v1 addition |
| --- | --- | --- |
| Repository/revision model | Git helpers and diagnosis report | `domain.Repository`, `Revision`, `PullRequest`, `CIExecution` |
| Failure understanding | Bounded diagnosis evidence and Python frames | `FailureEpisode` persistence model |
| Investigation graph | Evidence and hypotheses | typed `Relationship`, `CodeLocation`, `CandidateLocation` |
| Causal graph | Hypotheses and experiment history | hypothesis/finding references and claim validation |
| Activity graph | Lifecycle events and experiments | durable typed `Activity` records |
| Candidate ranking | File/history localization | revision-aware score vectors and interpretable ranking policy |
| Reporting trust | Evidence IDs and deterministic reports | `DiagnosticFinding` validation before publication |

The implementation uses SQLite JSON payloads for the new domain records while
keeping entity kind, identity, relationship type, and investigation indexes in
relational columns. This preserves typed relationships without introducing a
graph or vector database.

The existing diagnosis workflow still owns reproduction and CI-independent
classification. GitHub ingestion/comment publication remains a follow-on
integration boundary; the new entities are ready to receive those records.

## Repository intelligence slice

`internal/repository` builds a bounded snapshot for Go, Python, and
JavaScript/TypeScript files. It records revision-scoped file, symbol, class,
test, and dependency nodes, plus typed `DEFINES`, `IMPORTS`, `CALLS`,
`REFERENCES`, and `TESTS` edges. `Compare` detects added, removed, and changed
symbol identities between snapshots.

`internal/localization` consumes stack frames, failing test text, changed
identities, and program edges. It generates candidates, expands them through a
bounded graph traversal, records failure-to-candidate relationships, and ranks
the result with separate execution, change, dependency, behavioural,
historical, and runtime signal evaluators. Ranking is a prioritization value,
not a causal probability.

The normal diagnosis path now persists the repository, revision, CI execution,
failure episodes, report evidence, program graph, candidate locations, and
failure relationships when a SQLite store is configured. The CLI configures a
store by default, so this path is active for normal CLI diagnosis runs.

Known limits: extraction is syntax-shaped and currently strongest for Go and
Python; JavaScript/TypeScript support is basic. Cross-revision comparison is
available as a repository package operation, while the diagnosis integration
currently marks changed symbols from the existing localized changed-file
evidence. The pipeline does not claim causal root causes from ranking alone.

## Relationship and reasoning slice

`internal/relationships` groups failure episodes only when they share an
evidence-backed candidate relationship. Groups are marked `potentially_related`
or `independent`; shared structure carries `causal_claim=false` metadata.

`internal/reasoning` creates open-world candidate explanations, keeps
competing hypotheses in the same group, assesses supporting and contradicting
evidence, and persists hypothesis versions through the existing hypothesis
history mechanism plus migration 6 details. Assessment may produce
`under_investigation`, `weakened`, `contradicted`, or `inconclusive`; candidate
relevance remains separate from causal probability.

`internal/investigation` defines durable activity states, action contracts,
registry lookup, dependency readiness, and completion/failure transitions. The
diagnosis path records completed repository, candidate, and relationship
activities. Full uncertainty-driven scheduling remains future work.

# Diagnosis evaluation

Evaluate the report at the level of the claim. A correct failure name, a
correct culprit file, and a verified mechanism answer different questions.
Check whether the evidence supports what Ceaser says and record unsupported
claims and abstentions separately.

## Score saved Go reports

Use [the corpus](../evaluation/corpus/) and a JSON object that maps case IDs
to saved report paths. For example:

```json
{
  "go-001-koanf-float64-overflow": "/path/to/ceaser-report.json"
}
```

From the Ceaser checkout:

```sh
go run ./cmd/corpus-score --corpus evaluation/corpus --reports /path/to/index.json
```

The scorer prints per-case results, aggregate counts, and case IDs missing a
report. Upstream labels and fixes belong on the scoring side. Give diagnosis
only the source revision, failing command, and evidence for the mode under test.

Record which revision built Ceaser, which fixture ran, and whether the
diagnosis used raw evidence, a model, or a corrective diff. Compare modes
using the same cases and limits. Record localization, preservation, invocation
correlation, and mechanism verification independently of elapsed time or cost.

## Corpus review

Keep original logs and result files with their revisions. If you repair a
fixture, record the patch and new hashes, then confirm the intended assertion
fails on base and the fixed control passes. [The MinIO repair record](../evaluation/fixture-repairs/minio-2342/README.md)
shows one example.

[Preflight results](../evaluation/go-probe-preflight.md) compare file
localization with raw log locations. [Probe rerun results](../evaluation/probe-rerun/README.md)
show which stage each case reached. The [protocol and tools](../evaluation/README.md)
provide the full evaluator entry points.

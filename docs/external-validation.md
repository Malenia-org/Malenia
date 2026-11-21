# External validation

## Live checks completed

| Check | What was observed |
| --- | --- |
| [Caeser draft PR #25](https://github.com/caeser-org/ceaser/pull/25#issuecomment-6980363714) | The workflow read a failing Go job log, posted one comment, and updated that comment on a subsequent run. It showed the exact assertion. This was a deliberate fixture in Caeser's own repository. |
| [Kubernetes PR #143709 diagnosis](https://github.com/caeser-org/ceaser/actions/runs/44322156729) | Caeser analyzed the original Prow race report with reproduction and model analysis off. It connected the runtime markers to a likely WaitGroup ordering problem and gave the observed locations. The exact call and PR attribution remained unresolved. |
| [Controlled 16-case Go run](../evaluation/probe-rerun/README.md) | Repaired fixtures and fixed-head controls passed qualification. One probe preserved the failing check, and none verified an implementation mechanism. |

The Kubernetes run produced a report in Caeser’s Actions workflow. It did not
post on the upstream PR. It demonstrates analysis of an external failure,
rather than installation or adoption by an upstream maintainer.

## Open checks

We have not measured how quickly maintainers find the right code with the
comment compared with raw CI output. Fork-PR execution, runner-specific
TypeScript behavior, and a live model comparison need separate validation.
The [evaluation guide](diagnosis-evaluation.md) describes how to record those
results without mixing evidence-only, model-assisted, and fix-assisted runs.

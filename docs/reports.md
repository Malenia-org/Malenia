# Reports and investigation briefs

## Pick the output you need

| Output | Where it appears | What to use it for |
| --- | --- | --- |
| Short PR comment | The pull request associated with the failed run | See the failure, a supported explanation when available, and where to start. |
| Diagnosis details | Expand the section in the comment or set `comment_detail: detailed` | Review the evidence behind the leads and the next checks. |
| `ceaser-brief.md` | Download the workflow artifact | Read or share a self-contained Markdown brief with source and log excerpts. |
| `ceaser-report.json` | The same artifact | Inspect the structured report, including evidence IDs and limitations. |

Open the Ceaser Actions run linked from the comment. Under **Artifacts**, download
`ceaser-diagnosis-<run-id>` for a reusable workflow run. A public PR test uses
an artifact named `ceaser-pr-diagnosis-<repo>-<pr>-<run-id>`. Unzip the artifact
and open the Markdown file.

## If the brief is missing

1. Check that the run completed diagnosis and uploaded an artifact. If it
   stopped before diagnosis, check the failed job step for the reason.
2. Check the **Export investigation brief** step. Older binaries may have
   produced the JSON report without supporting the `brief` command.
3. If `ceaser-report.json` is available, use a current
   [source build](installation.md#build-from-source) to export it locally.
   This reads the JSON and does not rerun CI or call a model.

```sh
ceaser brief --report ceaser-report.json > ceaser-brief.md
ceaser brief --report ceaser-report.json --prompt > ceaser-prompt.md
```

`--prompt` adds instructions for another assistant. Review both exports
before sharing them. They can contain excerpts from private code and CI logs.

## Read the evidence

- **Observed** means the CI output or an executed check recorded the event.
  A stack frame identifies a location the runtime reported.
- **Inferred** means source inspection or a call relationship pointed to a lead.
  Start there if it helps and check it against the failure.
- **Established** is reserved for mechanisms supported by the required evidence.
  Establishing a mechanism and attributing it to a PR are separate findings.

An evidence-first run labels reproduction as deferred. Logs and tests from the
original CI run are observations, even when their failure count is nonzero.
When an assertion is the only usable location, the comment still shows it.

Optional model interpretation appears separately at the end of the comment
when enabled and available. See [the workflow options](ci-integration.md#inputs).

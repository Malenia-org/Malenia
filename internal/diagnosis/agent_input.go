package diagnosis

import (
	"fmt"
	"strings"

	"github.com/caeser-org/ceaser/internal/agent"
)

// BuildAgentInput reuses source excerpts in the report so artifact-only
// analysis can run without a checkout or access to the test executor.
func BuildAgentInput(report Report) agent.Input {
	input := agent.Input{
		Classification: string(report.Classification),
		Command:        report.Command,
	}
	var context strings.Builder
	fmt.Fprintf(&context, "revision=%q reproduction=%q\n",
		bound(report.Environment.HeadCommit, 128), bound(report.ReproductionStatus, 128))
	for _, episode := range report.FailureEpisodes[:min(len(report.FailureEpisodes), 3)] {
		fmt.Fprintf(&context, "failed test=%q message=%q\n",
			bound(episode.TestIdentifier, 160), bound(episode.FailureMessage, 384))
	}
	for _, candidate := range report.Candidates[:min(len(report.Candidates), 5)] {
		location := candidate.Location
		ids := candidate.SupportingEvidenceIDs
		fmt.Fprintf(&context, "candidate file=%q line=%d symbol=%q provenance=%q evidence=%q\n",
			bound(location.FilePath, 192), location.StartLine, bound(location.SymbolIdentity, 192),
			bound(location.Provenance, 100), bound(fmt.Sprint(ids[:min(len(ids), 4)]), 192))
	}
	if len(report.Candidates) == 0 {
		for _, file := range report.Localization.LikelyFiles[:min(len(report.Localization.LikelyFiles), 5)] {
			fmt.Fprintf(&context, "candidate file=%q\n", bound(file, 192))
		}
	}
	for _, match := range report.HistoricalContext[:min(len(report.HistoricalContext), 3)] {
		fmt.Fprintf(&context, "historical lead investigation=%q previous_revision=%q evidence=%q\n",
			bound(string(match.InvestigationID), 128), bound(match.RevisionID, 128), bound(string(match.EvidenceID), 128))
	}
	input.Context = bound(context.String(), 6<<10)
	for _, evidence := range report.Evidence {
		input.Evidence = append(input.Evidence, agent.Evidence{
			ID:     evidence.ID,
			Kind:   evidence.Kind,
			Detail: fmt.Sprintf("source=%q revision=%q provenance=%q\n%s", evidence.Source, evidence.Revision, evidence.Provenance, evidence.Detail),
		})
	}
	return input
}

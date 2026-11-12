package diagnosis

// Evidence basis values are deliberately more specific than the historical
// SourceVerified label.  A producer must choose the strongest basis it can
// actually justify; consumers must not treat textual discovery as semantic
// verification.
const (
	BasisObserved         = "Observed"
	BasisTextualDiscovery = "TextualDiscovery"
	BasisASTVerified      = "ASTVerified"
	BasisTypeChecked      = "TypeChecked"
	BasisDataflowVerified = "DataflowVerified"
	BasisRuntimeMapped    = "RuntimeMapped"
	BasisExperimental     = "Experimental"
)

// IsSemanticBasis reports whether a basis is strong enough for a semantic
// requirement.  The legacy SourceVerified value is intentionally not accepted
// by new relationship checks; existing reports retain the value for
// compatibility but cannot use it to satisfy a new strict proof.
func IsSemanticBasis(value string) bool {
	switch value {
	case BasisASTVerified, BasisTypeChecked, BasisDataflowVerified, BasisRuntimeMapped, BasisExperimental:
		return true
	default:
		return false
	}
}

// EvidenceItemContract contains relationship facts that cannot be represented
// safely by free-form Detail text. Empty fields mean that the relationship is
// not established.
type EvidenceItemContract struct {
	RelatedAccessIDs         []string `json:"related_access_ids,omitempty"`
	PredecessorAccessID      string   `json:"predecessor_access_id,omitempty"`
	SuccessorAccessID        string   `json:"successor_access_id,omitempty"`
	SynchronizationMechanism string   `json:"synchronization_mechanism,omitempty"`
	MemoryModel              string   `json:"memory_model,omitempty"`
	RuntimeAddress           string   `json:"runtime_address,omitempty"`
	StaticMappingStatus      string   `json:"static_mapping_status,omitempty"`
}

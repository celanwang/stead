package rsi

// ExampleDataset constructs a synthetic, labeled notification experiment.
// The starting rules deliberately reproduce over-alerting from the fixtures.
func ExampleDataset() (Dataset, Policy) {
	return Dataset{
		Development: []Example{
			{ID: "paper-spelling-edit", Family: "papers-dev", Pair: "paper-revision",
				Signal: Signal{Cosmetic: true}, Expected: Digest, Feedback: true},
			{ID: "software-repository-migrated", Family: "software-dev", Pair: "software-change",
				Signal: Signal{Material: true}, Expected: Digest, Feedback: true},
			{ID: "paper-cited-number", Family: "papers-dev", Pair: "paper-revision",
				Signal: Signal{Cosmetic: true, ImpactsConclusion: true}, Expected: Immediate, Feedback: true},
			{ID: "paper-new-in-catalog", Family: "papers-dev", Pair: "paper-revision",
				Signal: Signal{Cosmetic: true, FirstSeen: true}, Expected: Immediate},
			{ID: "paper-removed", Family: "papers-dev", Pair: "paper-revision",
				Signal: Signal{Cosmetic: true, Withdrawn: true}, Expected: Immediate},
			{ID: "software-dependency-obsolete", Family: "software-dev", Pair: "software-change",
				Signal: Signal{Material: true, ImpactsConclusion: true}, Expected: Immediate},
			{ID: "software-new-library", Family: "software-dev", Pair: "software-change",
				Signal: Signal{Material: true, FirstSeen: true}, Expected: Immediate},
			{ID: "software-release-withdrawn", Family: "software-dev", Pair: "software-change",
				Signal: Signal{Material: true, Withdrawn: true}, Expected: Immediate},
		},
		Validation: []Example{
			{ID: "confirmed-withdrawal", Family: "confirmations-val", Signal: Signal{Withdrawn: true}, Expected: Immediate},
			{ID: "checked-reference", Family: "documents-val", Signal: Signal{ImpactsConclusion: true}, Expected: Immediate},
			{ID: "new-index-entry", Family: "index-val", Signal: Signal{FirstSeen: true}, Expected: Immediate},
			{ID: "unchanged-index-entry", Family: "index-val", Signal: Signal{}, Expected: Digest},
		},
		Holdout: []Example{
			{ID: "calendar-format-edit", Family: "calendar-held", Signal: Signal{Cosmetic: true}, Expected: Digest},
			{ID: "calendar-time-correction", Family: "calendar-held", Signal: Signal{Cosmetic: true, ImpactsConclusion: true}, Expected: Immediate},
			{ID: "catalog-supplier-move", Family: "catalog-held", Signal: Signal{Material: true}, Expected: Digest},
			{ID: "catalog-pinned-listing-changed", Family: "catalog-held", Signal: Signal{Material: true, ImpactsConclusion: true}, Expected: Immediate},
			{ID: "catalog-pinned-listing-removed", Family: "catalog-held", Signal: Signal{Material: true, Withdrawn: true}, Expected: Immediate},
			{ID: "catalog-new-supplier", Family: "catalog-held", Signal: Signal{Material: true, FirstSeen: true}, Expected: Immediate},
		},
	}, Policy{
		Rules: append([]Rule{
			{Require: []Feature{Cosmetic}, Decide: Immediate},
			{Require: []Feature{Material}, Decide: Immediate},
		}, DefaultPolicy().Rules...),
	}
}

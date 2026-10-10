package observer

type factClassification struct {
	source string
	truth  string
}

// factClassifications is the observer protocol's closed evidence vocabulary.
// Semantic case names belong in FactFields.SemanticKind, never in this table.
var factClassifications = map[string]factClassification{
	"runtime.turn.started":     {source: "runtime", truth: "observation"},
	"runtime.hook.cue":         {source: "runtime", truth: "observation"},
	"runtime.delegate.invoked": {source: "runtime", truth: "observation"},
	"runtime.domain.operation": {source: "runtime", truth: "observation"},
	"runtime.view.received":    {source: "runtime", truth: "derived_projection"},
	"runtime.intent.denied":    {source: "runtime", truth: "observation"},
	"runtime.intent.submitted": {source: "runtime", truth: "observation"},
	"runtime.turn.ended":       {source: "runtime", truth: "observation"},
	"runtime.turn.timed_out":   {source: "runtime", truth: "observation"},
	"system.node.restarted":    {source: "runner", truth: "observation"},
	"r7.receipt.accepted":      {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.receipt.rejected":      {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.receipt.replayed":      {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.event.accepted":        {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.handling.created":      {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.handling.advanced":     {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.handling.resolved":     {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.reference.published":   {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.reference.superseded":  {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.reference.retracted":   {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.delivery.pending":      {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.delivery.readmitted":   {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.delivery.settled":      {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.delivery.expired":      {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.artifact.captured":     {source: "r7_authority", truth: "accepted_local_fact"},
	"r7.artifact.read":         {source: "runtime", truth: "observation"},
	"r7.artifact.verified":     {source: "r7_authority", truth: "accepted_local_fact"},
	"test.attention.wave":      {source: "oracle", truth: "assertion"},
	"test.attention.outcome":   {source: "oracle", truth: "assertion"},
	"test.attention.exhausted": {source: "oracle", truth: "assertion"},
	"test.attention.quiescent": {source: "oracle", truth: "assertion"},
	"test.attention.occupied":  {source: "oracle", truth: "assertion"},
	"test.gate.checked":        {source: "oracle", truth: "assertion"},
}

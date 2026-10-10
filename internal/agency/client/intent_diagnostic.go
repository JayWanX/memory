package agencyclient

import "github.com/JayWanX/memory/internal/agency"

// Intent diagnostics are an exact, static allowlist. Unknown validation text
// never crosses the Agent terminal boundary and cannot become an input echo or
// an authority-probing surface.
type validationDiagnosticKey struct {
	field   string
	problem string
}

var intentValidationDiagnostics = map[validationDiagnosticKey]string{
	{"Intent JSON", "omits a required field"}:                                  "required",
	{"Agent Intent JSON", "contains a duplicate object key"}:                   "duplicate_json",
	{"Intent JSON", "contains a non-canonical field name"}:                     "noncanonical_field",
	{"Intent successor", "contains a non-canonical field name"}:                "successor_noncanonical_field",
	{"Intent Artifact", "contains a non-canonical field name"}:                 "artifact_noncanonical_field",
	{"Intent consequence", "is not a closed consequence"}:                      "closed_consequence",
	{"root Intent", "cannot name a subject Handling or Reference"}:             "root_shape",
	{"root Intent", "must create at least one successor"}:                      "root_shape",
	{"subject Intent", "requires one subject and no Reference"}:                "subject_shape",
	{"Reference publish", "requires one new key and one Artifact only"}:        "reference_publish_shape",
	{"Reference supersede", "requires one offered head and one Artifact only"}: "reference_supersede_shape",
	{"Reference retract", "requires one offered head and no Artifact"}:         "reference_retract_shape",
	{"Intent target", "must contain exactly one of self or alias"}:             "target_shape",
	{"Intent Artifact kind", "is not candidate or view_handle"}:                "artifact_kind",
}

func intentValidationDiagnostic(validation *agency.ValidationError) string {
	if validation == nil {
		return ""
	}
	return intentValidationDiagnostics[validationDiagnosticKey{
		field: validation.Field, problem: validation.Problem}]
}

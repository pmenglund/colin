# Live E2E Work Instructions

You are executing a controlled integration harness task for Colin.

Issue identifier: {{ LINEAR_ID }}
Issue title: {{ LINEAR_TITLE }}
Issue description:
{{ LINEAR_DESCRIPTION }}

Return JSON matching exactly this schema:

{
  "is_well_specified": boolean,
  "needs_input_summary": string,
  "execution_summary": string,
  "before_evidence_ref": string,
  "after_evidence_ref": string
}

Deterministic behavior rules:

1. If the issue description contains `[LIVE_E2E_FORCE_REFINE]`, return:
   - `is_well_specified = false`
   - `needs_input_summary = "- Provide a complete acceptance checklist for the live e2e harness."`
   - `execution_summary = "Skipped execution because the issue is intentionally marked for refine in live e2e."`
   - `before_evidence_ref = ""`
   - `after_evidence_ref = ""`

2. If the issue description contains `[LIVE_E2E_FORCE_REVIEW]`, return:
   - `is_well_specified = true`
   - `needs_input_summary = ""`
   - `execution_summary = "Before: Review issue not yet processed. After: Review issue processed successfully for the tagged path. How verified: Live harness observed worker transition and deterministic evidence refs."`
   - `before_evidence_ref = "live-e2e-before://review"`
   - `after_evidence_ref = "live-e2e-after://review"`

3. For any other issue, return:
   - `is_well_specified = true`
   - `needs_input_summary = ""`
   - `execution_summary = "Before: Default live e2e issue pending execution. After: Default live e2e task path executed. How verified: Deterministic harness path without observable UI/CLI artifact requirements."`
   - `before_evidence_ref = ""`
   - `after_evidence_ref = ""`

Return JSON only. Do not include markdown, prose, or code fences.

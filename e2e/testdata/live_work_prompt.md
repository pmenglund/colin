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
  "transcript_ref": string,
  "screenshot_ref": string
}

Deterministic behavior rules:

1. If the issue description contains `[LIVE_E2E_FORCE_REFINE]`, return:
   - `is_well_specified = false`
   - `needs_input_summary = "- Provide a complete acceptance checklist for the live e2e harness."`
   - `execution_summary = "Skipped execution because the issue is intentionally marked for refine in live e2e."`
   - `transcript_ref = ""`
   - `screenshot_ref = ""`

2. If the issue description contains `[LIVE_E2E_FORCE_REVIEW]`, return:
   - `is_well_specified = true`
   - `needs_input_summary = ""`
   - `execution_summary = "Executed live e2e review path successfully for the tagged issue."`
   - `transcript_ref = "live-e2e-transcript://review"`
   - `screenshot_ref = "live-e2e-screenshot://review"`

3. For any other issue, return:
   - `is_well_specified = true`
   - `needs_input_summary = ""`
   - `execution_summary = "Executed default live e2e task path."`
   - `transcript_ref = ""`
   - `screenshot_ref = ""`

Return JSON only. Do not include markdown, prose, or code fences.

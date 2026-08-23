# Ox Alpha: refresh the B_PRED exact-RD acceptance golden

Repository: `m31labs.dev/tqwebp`, MIT licensed at the repository root.
Return only a complete valid unified diff. Do not use Markdown fences or prose.

The fixed-Q8 B_PRED scorer is correct. Independent diagnostics established that
the expected count of 51 was introduced with the provisional coefficient-count
proxy in commit `5a17ca2`. Commit `893578b` intentionally replaced that proxy
with exact fixed-Q8 syntax-rate RD scoring but retained the old golden.

The exact policy selects 55 B_PRED macroblocks. Relative to the legacy proxy it:

- lowers aggregate luma SSE from 222,804 to 221,832;
- lowers modeled luma rate from 15,309,820 to 15,226,534 Q8;
- lowers aggregate modeled RD from 133,586,924 to 132,921,662; and
- reduces the encoded frame from 11,058 to 11,018 bytes.

The four primary new decisions all have strictly lower exact RD and clear the
existing quantizer-derived reconstruction margin under identical entering
skip, Y2, token, and B-mode contexts. The net count change also includes
deterministic raster-order reconstruction cascades. There is no evidence of a
production invariant violation.

Make exactly this bounded acceptance refresh:

- Modify only `internal/encoder/bpred_selector_test.go`.
- Change `wantSelected` from 51 to 55.
- Do not modify production code, add a compensating margin, restore proxy
  admission, weaken another assertion, or change any other test.
- Keep the patch minimal.

The response must begin with `diff --git`, contain ordinary `---`, `+++`, and
numbered `@@ -old,+new @@` unified-diff headers, and end immediately after the
diff. It must apply with `git apply --check` to the pinned clean HEAD.

The patch must be designed to pass:

```bash
go test ./internal/encoder -run 'TestBPredSelectorEffortBoundary|TestBPredSelectorKeepsSmoothWholePath|TestPreferBPredRDBoundaries|TestBPredReconstructionPruneBoundaries|TestBPredMacroblockCostMatchesSyntaxAndContexts|TestY2ContextsPreserveIndependentBPredInputs' -count=20
go test ./... -count=1
go vet ./...
go build ./...
```

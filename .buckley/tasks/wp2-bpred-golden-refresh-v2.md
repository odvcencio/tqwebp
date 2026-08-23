# Ox Alpha retry: exact one-line B_PRED golden refresh

Repository: `m31labs.dev/tqwebp`, MIT licensed at the repository root.
Return only a complete valid unified diff. Do not use Markdown fences or prose.

The fixed-Q8 B_PRED scorer is correct and independently validated. Its exact
policy selects 55 macroblocks with lower luma SSE, modeled rate, aggregate RD,
and final frame size than the retired coefficient-count proxy. The old golden
of 51 is stale.

A prior response was rejected because it invented a nonexistent top-of-file
comment and function shape, did not change the actual golden, and returned a
truncated hunk. Do not repeat that response.

The only permitted source change is this exact replacement in
`internal/encoder/bpred_selector_test.go`:

```diff
-	const wantSelected = 51
+	const wantSelected = 55
```

The real pinned source context is:

```go
			if mode >= predict.NumBModes {
				t.Fatalf("macroblock %d block %d selected invalid mode %d", i, block, mode)
			}
		}
	}
	const wantSelected = 51
	if selected != wantSelected {
		t.Fatalf("method 1 selected %d B_PRED macroblocks, want %d", selected, wantSelected)
	}
```

Requirements:

- Modify only `internal/encoder/bpred_selector_test.go`.
- Change only the numeric literal on `const wantSelected = 51` from 51 to 55.
- Do not add comments or touch production code or any other test line.
- Begin with `diff --git`, use ordinary `---`, `+++`, and a numbered unified
  hunk header, include enough exact surrounding context to apply, and terminate
  the response with a newline immediately after the complete diff.
- The patch must pass `git apply --check` against the pinned clean HEAD.

The applied one-line patch must pass:

```bash
go test ./internal/encoder -run 'TestBPredSelectorEffortBoundary|TestBPredSelectorKeepsSmoothWholePath|TestPreferBPredRDBoundaries|TestBPredReconstructionPruneBoundaries|TestBPredMacroblockCostMatchesSyntaxAndContexts|TestY2ContextsPreserveIndependentBPredInputs' -count=20
go test ./... -count=1
go vet ./...
go build ./...
```

# RealityCheck — Agent-Focused Improvements

Prioritized list of improvements for the primary use case: an agent (Claude Code,
Codex) runs `realitycheck check` at a phase boundary, reads the report, and
self-corrects. Every item targets at least one of **Accuracy**, **Speed**, or
**Tokens** (prompt tokens the tool spends, and report tokens the agent spends
reading the result).

## Measured baseline (this repo, 2026-10-06)

One run of `check --spec specs/SPEC.md --plan specs/PLAN.md --code-root .` with
`--provider google` (gemini flash class, max-tokens 16384):

| Measure | Value |
|---|---|
| Wall time | 44 s (all of it in the single LLM call) |
| Prompt size | 58 KB ≈ 14.5K tokens |
| …of which PLAN.md | 29.5 KB (51%) |
| …of which code inventory | 17 KB (symbols 7.0 KB, tests 5.9 KB, go.mod 2.5 KB) |
| Spec / plan items sent | 101 / 186 |
| Coverage entries returned | **7 / 9** (all IMPLEMENTED, all HIGH) |
| Report size | 13 KB ≈ 3.3K tokens (8.6 KB compact) |
| `meta.model` returned by the model | `"realitycheck"` (hallucinated) |
| Spec items under 50 chars | 91 of 101 (headings, `⸻`, prose) |
| Inventory noise | gitignored `realitycheck` binary, `go.sum`, `LICENSE`, `testdata/*/SPEC.md` fixtures |

Two conclusions drive the ordering below:

1. The verdict and score are currently computed on whatever subset of items the
   model felt like returning. 271 of 287 items were silently dropped and nothing
   noticed. Accuracy fixes come first.
2. Output tokens dominate latency (≈3.3K output tokens at flash-class speeds is
   most of the 44 s). Anything that shrinks the JSON the model must emit is both
   a token and a speed win.

---

## P0 — Accuracy (the verdict must be trustworthy)

### 1. Enforce coverage completeness
**Status: done** (065f229)

**Accuracy · Tokens**
- After validation, compare returned coverage IDs against the parsed spec/plan
  IDs. Reject IDs that were never sent. Fill every missing ID with `UNCLEAR`
  (`NOT_IMPLEMENTED` under `--strict`) and a note `"not evaluated by model"`.
- Set `meta.coverage_complete: false` and print a stderr warning when any ID was
  filled in. An agent must be able to see that the score is provisional.
- Use the repair slot for a *targeted* follow-up that asks only for the missing
  IDs, instead of re-sending the whole prompt and whole bad response.
- Why: today 16/287 items were returned and the run reported `score=86`,
  `DRIFT_DETECTED`, with no indication that 94% of the spec was never assessed.

### 2. Stop asking the model for fields the tool already knows
**Status: done** (5a940ee)

**Tokens · Speed · Accuracy**
- Remove `spec_reference`, `plan_reference`, `quote`, and `meta` from the LLM
  output schema. The tool owns the ID → line-range mapping; fill these locally
  after the call. `meta.model`/`temperature` come from `Options`.
- For violations, ask for `spec_id` (e.g. `SPEC-012`) and derive the reference.
  Validate the ID exists; drop or downgrade the finding if it does not.
- Why: removes ~40% of per-entry output tokens, removes the quote-fabrication
  surface, and fixes the hallucinated `meta.model` seen in the baseline.

### 3. Native structured output + truncation detection
**Status: done** (98c4eda)

**Accuracy · Speed**
- Anthropic: force a tool call with the report JSON schema (or the structured
  output API). OpenAI: `response_format: json_schema` (strict). Gemini:
  `ResponseSchema` in addition to the existing `ResponseMIMEType`.
- Check the provider stop reason. If the response was cut at `max_tokens`, do not
  attempt JSON parse → repair → exit 5; instead continue/append or fail with a
  specific exit-4 message ("response truncated at N tokens; raise --max-tokens").
- Raise the default `--max-tokens` from 4096 to 16384. 4096 cannot hold a report
  for any real spec (287 entries × ~120 tokens). The env override in this
  environment already works around it; the default should not need working around.
- Why: markdown-fence stripping and the escape-fixing regex are symptoms of
  free-text JSON. Structured output makes exit 5 rare and the repair call
  mostly unnecessary.

### 4. Segment only normative items
**Status: done.** Plan steps under a "Step N:" header are still separate items (header, bullets, done-when); grouping a whole step into one item would cut plan items further.

**Accuracy · Tokens · Speed**
- Classify each parsed item as *normative* (requirement/step) or
  *informational* (title, heading-like line, separator, prose intro such as
  "RealityCheck determines:"). Heuristics: contains a modal (`must`, `shall`,
  `should`, `will`, `may not`), is a list/step item, or is inside a
  Requirements/Constraints/Behavior/Phase section; short Title-Case lines and
  single-glyph lines (`⸻`) are informational.
- Send informational items as context (no IDs) and require coverage entries only
  for normative IDs. Offer `--all-items` to restore the current behaviour.
- Fix two parser bugs seen in the baseline: a lone `⸻`/`—` line becomes an item
  (IsDecorator requires ≥3 repeats), and non-ATX headings in exported specs
  ("Purpose", "Core Philosophy") become items.
- Why: 91 of 101 spec items are noise. Each noise item costs ~120 output tokens
  and invites the model to mark "Purpose" as IMPLEMENTED/HIGH.

### 5. Inventory hygiene
**Status: done.** Markdown files are kept: the inventory holds only their paths, and docs such as README.md can be legitimate evidence. The fake spec/plan files that caused noise live under `testdata/`, which is excluded.

**Accuracy · Tokens**
- Respect `.gitignore` (the baseline shipped the gitignored `realitycheck`
  binary to the model). Use `git ls-files` when inside a repo, fall back to the
  walker otherwise.
- Default-exclude: `testdata/`, `fixtures/`, `*.sum`, `LICENSE*`, `*.md` other
  than the spec/plan, images/binaries. Expose `--ignore <glob>` (Build already
  accepts ignore patterns; main passes nil) and `--include-tests=false`.
- Why: `testdata/aligned/SPEC.md`, `testdata/drift/store.go`, etc. are fake
  spec/plan/code pairs that look exactly like drift to the model.

### 6. Quick correctness fixes
**Status: done.** The Gemini client change landed with item 3.

- Accept `gemini` as an alias for `google` (the env in this environment sets
  `REALITYCHECK_LLM_PROVIDER=gemini`, so every agent invocation currently exits 3
  until the flag is overridden). Also accept `claude` → anthropic.
- Validate evidence `(path, symbol)` pairs against the index, not just `path`.
  Unknown symbol → `LOW` confidence, same as unknown path.
- Create the Gemini client once per provider, not once per `Complete` call.

---

## P1 — Speed and tokens (make re-runs cheap)

### 7. Content-addressed result cache
**Speed · Tokens**
- Key: hash(spec text, plan text, inventory summary, profile, strict, provider,
  model, temperature, tool version). Store under `$XDG_CACHE_HOME/realitycheck/`.
  On hit, re-run the deterministic steps (strict escalation, scoring, filtering,
  rendering) and return in milliseconds with `meta.cached: true`.
- `--no-cache` to bypass; `realitycheck cache clear` to purge (mirrors `prism cache`).
- Why: agents re-run after touching files that do not change the inventory
  (comments, formatting, docs) and re-run to "confirm" a fix. Those runs should
  cost nothing.

### 8. Chunked, parallel coverage + single drift pass
**Speed · Accuracy**
- Split normative items into batches (~40 items) and classify coverage per
  batch concurrently, each with the full inventory. Run one separate pass for
  drift + violations with the full spec/plan. Merge deterministically; IDs are
  renumbered locally.
- `--concurrency N` (default 4). Per-batch output is small, so truncation and
  item-dropping (item 1) largely disappear as a side effect.
- Optional model routing: `--coverage-model` (fast/cheap) and `--model`
  (strong) for the drift/violation pass.
- Why: 44 s → roughly 10–15 s for the same report; smaller outputs per call are
  also more reliably complete.

### 9. Provider prompt caching
**Speed · Tokens**
- Order the prompt so the stable prefix (system prompt, output schema, spec,
  plan) comes first and the inventory last. Anthropic: mark the prefix with
  `cache_control: ephemeral`. Gemini: context caching for the same prefix.
  OpenAI caches automatically on a stable prefix.
- The repair/follow-up call and every batch in item 8 then reuse the ~14K-token
  prefix instead of paying for it again.

### 10. Compact inventory encoding
**Tokens**
**Status: done.** On this repo the inventory went from 36.9 KB to 24.2 KB with
full Go signatures (symbols 21.1 → 15.2 KB, tests 12.5 → 7.0 KB, go.mod
1.8 → 0.3 KB). Non-code files collapse only in directories with more than 8 of
them, so root docs such as README.md stay citable. Lockfiles were already
excluded by item 5. The cap is now 80 KB and always holds: signatures go first; then the file
tree and tests get a quarter of the cap each, manifests an eighth, the config
list a sixteenth, and symbols the rest, each keeping a counted prefix.
Fallbacks are reported in `meta.inventory_signatures_omitted`,
`meta.inventory_truncated`, and `meta.inventory_{symbols,tests,files}_omitted`. Still open: very large repos spend most of the
budget on the file tree, which lists code files that the symbol and test
groups name again.
- Group symbols and tests by file: `path: a, b, c` instead of one `path: sym`
  line per symbol. Measured on this repo: symbols 7.0 KB → 2.7 KB, tests
  5.9 KB → 3.2 KB.
- Collapse non-code files to per-directory counts (`docs/ (4 Markdown)`).
- Strip the `// indirect` block from go.mod and lockfile content from manifests.
- Raise the 40 KB summary cap only after the above; today truncation silently
  drops symbols, which guarantees false "NOT_IMPLEMENTED" on large repos. When
  truncation does happen, report it in `meta.inventory_truncated`.

### 11. Agent-oriented output format
**Tokens**
- Add `--format agent` (or `--only-findings`): compact JSON containing summary,
  non-IMPLEMENTED coverage entries only, drift, violations, and a short
  `next_actions` list derived from findings (file, symbol, action verb). Omit
  quotes, cap notes at ~200 chars.
- Make `--format md` default to hiding the IMPLEMENTED rows (`--show-aligned`
  to restore). Today the agent reads a 16-row table of "IMPLEMENTED" to find two
  WARNs.
- Always print a one-line stderr summary (`verdict=… score=… critical=… warn=…`)
  even when `--out` is used, so an agent can gate without re-reading the file.
- Why: 13 KB → ~3 KB per read; the self-correction loop reads the report two or
  three times per phase.

### 12. Scope the check to what the agent is working on
**Speed · Tokens · Accuracy**
- `--phase <n|name>`: restrict plan coverage to one `## Phase` section; later
  phases are not sent and not marked NOT_IMPLEMENTED. The documented workflow is
  phase-by-phase, so every mid-project run today lands on PARTIALLY_ALIGNED and
  spends output tokens on phases that have not started.
- `--since <git-ref>` / `--changed-only`: run the drift/violation pass only over
  files changed since the ref (inventory still sent in full for context, but the
  model is told which files are new). Coverage still runs over the full scope.

---

## P2 — Deeper accuracy and loop ergonomics

### 13. Richer evidence without sending source
**Accuracy**
**Status: signatures done.** Go functions, methods, and types are listed with
their `go/parser` declaration signatures; on a names-only inventory gpt-6.1-sol
could not confirm receivers and left plan steps PARTIAL. Imports, doc comments,
and `--include-source=cited` are still open. With item 10's grouping, the
inventory with signatures is 24.2 KB on this repo (80 KB cap), and it falls
back to names before dropping symbols.
- Go: use `go/parser` for signatures, receiver types, exported doc comments, and
  the import list per file. Cost is small and the privacy promise ("no raw code
  to the LLM") still holds.
- Imports and signatures are what the `strict-api` and `data-pipeline` profiles
  need: today they ask the model to flag "outbound HTTP calls" and "writes to an
  external store" while it can only see function names.
- Opt-in `--include-source=cited`: a second, cheap verification pass that sends
  the bodies of symbols cited in CRITICAL findings and asks the model to
  confirm or withdraw. Off by default to preserve the privacy guarantee.

### 14. Stable finding fingerprints and a baseline file
**Accuracy · Tokens**
- Add `fingerprint` = hash(kind, evidence paths+symbols, normalized description
  prefix). IDs like `DRIFT-001` are renumbered every run and cannot be tracked.
- `--baseline <file>` suppresses fingerprints the user has accepted
  (authorized maintenance drift, test helpers). `--write-baseline` creates it.
- Diff against the previous cached report: mark each finding `new`,
  `persisting`, or omit `resolved`. The agent can then confirm a fix without
  re-reading the whole report. This directly addresses the "same drift keeps
  coming back" pitfall in WORKFLOW.md.

### 15. Resilient provider calls
**Speed**
- Bounded retry with backoff on 429/5xx/connection reset (3 attempts), and a
  `--timeout` (default ~120 s). Agents today get exit 4 on any transient error
  and either give up or re-run the full 44 s call.

### 16. Token and cost visibility
**Tokens**
- Record provider usage (`input_tokens`, `output_tokens`, `cache_read_tokens`,
  calls) in `meta` and in `--verbose`. Without this there is no way to see
  whether items 7–12 are working in a given environment.

### 17. One-shot consensus
**Accuracy**
- `--compare provider:model[,…]` runs the configured providers in parallel and
  merges: findings present in all → `consensus: true`; present in one → kept
  with `consensus: false` and LOW confidence. The skill already recommends
  manual cross-provider re-runs; doing it in one invocation halves the wall
  time and lets the agent act on agreement rather than two reports.

### 18. In-process / MCP surface
**Speed**
- The untracked `pkg/realitycheck` library API (spec/plan from text, ignore
  patterns, structured errors) is the right direction. Finishing it enables an
  MCP tool or a harness-side call that skips file juggling and lets the result
  cache live for the session. Low priority relative to the above; the CLI start
  cost is negligible (index build measured at 5 ms).

---

## Suggested order of work

1. Items 1, 2, 3, 6 together (one change to the LLM contract and validator).
2. Item 4 and 5 (parsers and inventory).
3. Items 7, 9, 10, 11 (cheap, independent, large token wins).
4. Item 8 and 12 (parallelism and scoping; largest speed win).
5. Items 13–18 as needed.

Re-measure the baseline table after each step; the targets are
< 15 s wall time, < 8K prompt tokens, < 1K report tokens for an aligned run on
this repo, with 100% of normative items accounted for in every report.

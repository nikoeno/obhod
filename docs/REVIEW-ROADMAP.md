# Test and review roadmap: internal/pc

The review is split by who can do it:

1. `REVIEW-AI.md`: checks an AI agent does on its own (code against contracts, tests). No admin rights, no network changes. Writes findings to `REVIEW-NOTES.md`.
2. `REVIEW-DECIDE.md`: the agent prepares facts and options, the human decides (DNS leak, heuristics, G6, config keys, open items).
3. `REVIEW-MANUAL.md`: human only. CLI runs, `nft -c`, your own read of `procman` and the lifecycle, and the real runs on a VM or spare PC (recovery: `docs/RECOVERY.md`).

Suggested order: AI pass → read `REVIEW-NOTES.md` → decisions → manual sessions.

About 60% of the 10k lines is tests, golden files and docs. Real code to review is roughly 4,500 lines.

## After the review
- Sort `docs/REVIEW-NOTES.md` by severity; findings get fixed in small batches.
- Items already known to be open: G6 (final route is always `tunnel`), M12 (control API placement), the unverified `--fwmark` flag of nfqws2, and the unverified hostlist re-read behaviour (G13). All are tracked in `REVIEW-DECIDE.md`.

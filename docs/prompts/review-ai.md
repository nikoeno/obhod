You are reviewing the `internal/pc` part of this Go repo (traffic-splitter). The code was written by AI agents and has not been reviewed by a human. Your job is to find problems, not to fix them.

## Read first
1. `AGENTS.md`: repo rules. Its hard rules apply in full: no admin/root commands, no changes to system DNS, proxy, firewall, routes or services, no real-network tests.
2. `docs/REVIEW-AI.md`: your checklist. Do Sessions 3 to 10 in order; Session 1 is already done.
3. Before each session, read that module's `API.md`. It is the contract you check the code against.

## How to work
- For each checklist item, read the code it names, compare it with `API.md` and the tests, and decide: OK, or a finding.
- Read the test names (`go test -v ./<pkg> | grep -- "--- "`) and check that each test really tests what its name says. A test that passes without checking the promised behaviour is a finding.
- Ground every claim in code. Cite `file:line`. If you suspect a bug, try to confirm it: trace the call path, or write a throwaway test in your scratchpad (never in the repo) and run it. If you cannot confirm it, still record it, marked `unconfirmed`.
- Report anything else you notice outside the checklist too, as long as it is in `internal/pc`.
- Session 6, zapret2 docs: you may read the zapret2 README and docs on the web. Record what the docs say and whether the code matches, and mark it "documented, not verified on a real system".
- Session 8, status page: you may start it on 127.0.0.1 only, from a throwaway harness in your scratchpad, and take one screenshot of the layout.

## What you may change
- Create `docs/REVIEW-NOTES.md` and write findings there.
- Tick boxes in `docs/REVIEW-AI.md` for items you have finished.
- Nothing else. Do not edit code, tests, golden files, `API.md` files or the other review files. Do not run `-update` on golden tests. Do not commit.

## REVIEW-NOTES.md format
Group findings by session, with one line per finding:

`- [high|medium|low] path/file.go:123: what is wrong and what it can cause. (confirmed|unconfirmed)`

Severity:
- high: can leave the user without internet, leak a secret, kill the wrong process, start with a bad config, or route traffic other than configured.
- medium: wrong behaviour in an edge case, a missing test for a risky path, a contract mismatch.
- low: unclear naming, docs drift, a misleading test name.

After the findings, add two sections:
- **Input for decisions**: facts for `docs/REVIEW-DECIDE.md`. For D1, list which query types of a tunnel domain can reach the direct DoH server, with code locations. For D2, list normal traffic that would trigger each monitor heuristic. Do not decide; just give the facts.
- **Missing tests**: scenarios with no test, especially in `app_test.go` and `procman`.

## Done when
- Every item in Sessions 3 to 10 of `docs/REVIEW-AI.md` is ticked, or left unticked with a note saying why.
- `go vet ./...` and `go test ./...` still pass (you changed no code, so this is a sanity check).
- Your final message gives: the number of findings by severity, the top 5 high findings in one line each, and anything you could not check and why.

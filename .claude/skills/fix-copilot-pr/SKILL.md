---
name: fix-copilot-pr
description: "Use when someone asks to fix copilot PR comments, address copilot suggestions, review copilot feedback on a PR, or handle copilot review comments."
argument-hint: "[PR URL or leave blank to paste]"
disable-model-invocation: true
---

## What This Skill Does

Reviews GitHub Copilot's review comments on a pull request and addresses each suggestion by editing the relevant code. Works in two modes: fetch comments via `gh` CLI from a PR URL, or let the user paste the PR comment thread directly.

## Steps

### Step 1: Get PR Comments

**If the user provides a PR URL as ``:**

1. Extract the owner, repo, and PR number from the URL.
2. Fetch review comments using `gh`:
   ```
   gh api repos/{owner}/{repo}/pulls/{pr_number}/comments
   ```
3. If the `gh` command fails (auth error, not installed, etc.), tell the user:
   > "Couldn't fetch PR comments via gh CLI. Please paste the full PR comment thread below instead."
   Then proceed to the paste flow.

**If no URL is provided (or gh failed):**

1. Detect the repo from the working directory:
   ```
   gh repo view --json nameWithOwner -q .nameWithOwner
   ```
2. If detected, confirm with the user: "Is this for **{owner}/{repo}**?" and ask for the PR number.
3. If the user confirms, fetch comments using `gh api repos/{owner}/{repo}/pulls/{pr_number}/comments`.
4. If `gh` fails, repo detection fails, or the user declines, fall back to paste:
   > "Please paste the full PR comment/conversation thread. Include all Copilot comments and the file paths they reference."
5. Wait for the user to paste the content.

### Step 2: Parse Copilot Comments

Filter comments to only those authored by **Copilot** (look for author `copilot` or `github-copilot` in API responses, or "Copilot" attribution in pasted text).

For each Copilot comment, extract:
- **File path** and **line number(s)** the comment refers to
- **The suggestion** — either a code block with the proposed change, or a natural language description
- **The reason** — why Copilot is suggesting the change

If no Copilot comments are found, tell the user and stop.

### Step 3: Process Each Suggestion — ONE AT A TIME

**CRITICAL: Process exactly ONE suggestion per response. Do NOT batch multiple suggestions. After presenting one suggestion and getting user approval (or skip), apply the fix, output the COMMIT MESSAGE, then STOP and present the next suggestion. Wait for the user's response before moving to the next one.**

For the **first** (or next) unprocessed Copilot suggestion:

1. **Present it to the user:**
   ```
   ## Suggestion N of M
   **File:** path/to/file.swift:42
   **Copilot says:** [summary of the suggestion]
   **Code/diff:** [show the suggested change if Copilot provided one]
   ```

2. **Assess the suggestion.** Read the relevant file and surrounding context. Determine if the suggestion is valid and safe to apply.

3. **If Claude disagrees** (the suggestion would break something, is incorrect, or doesn't apply):
   ```
   ⚠️ SKIPPING — [explain why this suggestion should not be applied]
   ```
   Then STOP. Wait for user acknowledgment before presenting the next suggestion.

4. **Ask the user:** "Apply this fix? (approve/skip)" — Then STOP and wait for their answer.

5. **If approved:** Apply the change to the file using the Edit tool. Be precise — match the exact code Copilot references and make the suggested modification. For natural language suggestions, use your judgment to implement what Copilot describes.

6. **MANDATORY — After EVERY applied fix, output a commit message on its own line:**
   ```
   COMMIT MESSAGE: [concise description of what was changed and why]
   ```
   This line MUST appear after every fix. Do not skip it. Do not combine it with other output.

7. **STOP here.** Do not continue to the next suggestion until the user responds. Present the next suggestion only after the user has seen the commit message and replies.

### Step 4: Final Summary

After all suggestions have been processed, output a summary:

```
## Summary
- **Total Copilot suggestions:** N
- **Applied:** X
- **Skipped (user):** Y
- **Skipped (disagreed):** Z

### Changes made:
- file1.swift:42 — [what changed]
- file2.swift:17 — [what changed]
```

## Notes

- **Copilot only.** Do not process comments from human reviewers or other bots. Only address comments attributed to GitHub Copilot.
- **ONE AT A TIME — this is mandatory.** Never process more than one suggestion per response. Present one, wait for user input, apply if approved, output `COMMIT MESSAGE:`, then STOP. Do not continue to the next suggestion until the user replies. This applies regardless of how the input was provided (pasted or fetched).
- **Read before editing.** Always read the target file before applying a fix. Never edit a file you haven't read.
- **Preserve intent.** When Copilot gives a natural language suggestion (not a code diff), implement the spirit of the suggestion without over-engineering. Make the minimal change that addresses Copilot's concern.
- **Don't add extras.** Only change what the suggestion asks for. Don't refactor surrounding code, add comments, or make "improvements" beyond the suggestion.
- **Commit messages are REQUIRED.** After every applied fix, you MUST output a line starting with `COMMIT MESSAGE:` followed by a concise description. Format: `fix: [what was fixed]` or `refactor: [what was changed]`. Never skip this step.


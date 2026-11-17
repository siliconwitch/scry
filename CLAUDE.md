# CLAUDE.md

Rules for agents changing scry. Product behaviour lives in `docs/product.md`.

## Product spec

- `docs/product.md` belongs to Raj. Edit it only when asked.
- When the code and the spec disagree, say so and change neither.
- When a change would make the spec wrong, point out the exact lines.

## Where code lives

scry stays small so an agent can find any behaviour by following package and
function names. Put code where an agent would look for it first, next to the
code it relates to.

- `main.go` reads the config, opens the sync log, starts the viewer server,
  launches the browser and runs the TUI. It holds no application logic.
- `tui`: every screen, key and click.
- `mailbox`: accounts, messages, folder actions, search, the Outbox, and sync
  decisions such as which folders, how much history and which images to fetch.
- `imapsync`: IMAP login, folder roles, search, flags, fetch, IDLE, MIME
  parsing and connection timeouts.
- `mirror`: encrypted files on disk, keyed from the account password.
- `viewer`: the local HTTP server and the browser page.
- `synclog`: daily log files, their retention, and the in-memory entries the
  log view reads.

Imports point downward. `tui` imports `mailbox`, `viewer` and `synclog`, which
never import each other or `tui`. `mailbox` imports `imapsync` and `mirror`,
which import nothing from scry. `viewer` defines its own `Page` type, so it
knows nothing about mail.

## Style

- Prefer a `switch` to an `if` / `else if` chain.
- Don't write 1 to 3 line functions, unless one wraps something likely to
  change, such as a hardcoded path.
- `cmd` names a `tea.Cmd` and `msg` names a `tea.Msg`. Any other command is
  `command`.

## TUI

- Follow Bubble Tea v2. State lives in the model, transitions happen in
  `Update`, and side effects are `Cmd`s. Never block in `Update` and never
  start a goroutine from the TUI.
- Add an action as a key in `handleKey`, then render it with `button`. The
  button is a bubblezone `key:<key>` zone, so clicks call `handleKey` too.
- Give each account its colour with `accountColor`, including on the viewer
  page.
- Resolve colours through `paint` and `style`, which pick the light or dark
  half of each pair. Never use a fixed ANSI colour.
- Lip Gloss v2 counts borders inside `Width` and `Height`. A bordered box with
  inner width `w` needs `Width(w + 2)`.
- Don't bind Shift+Backspace. It arrives as plain Backspace in foot inside
  herdr.

## Privacy and security

Never loosen any of these.

- The viewer listens on 127.0.0.1 only, requires its random token in every
  path and rejects any other `Host` header.
- Email bodies are served with a CSP that blocks scripts and every network
  request, and sandboxes the document. Images come only from copies made at
  sync. Attachments are served as `application/octet-stream` downloads.
- Sync downloads every remote image in a new email, read or not, so a tracking
  image only confirms delivery.
- Never fetch anything for Spam. Never send read receipts.
- Passwords come only from each account's `password-cmd`. They never appear in
  the config, logs, cache files or on-screen errors.
- Sync log lines record events and counts tagged with the account. They never
  record subjects, addresses or bodies.

## Build and test

Build, vet and test with cgo disabled:

```sh
CGO_ENABLED=0 go build -o scry .
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./...
```

To check a layout without a terminal, write a temporary test in `tui`. Send a
`tea.WindowSizeMsg`, print `m.View().Content` through `ansi.Strip` and check
each line's `lipgloss.Width`. Delete the test afterwards.

## Releasing

When Raj asks for a release, do all of it. Raj's request to release covers
the commit, push and tag below.

The version lives only in `const version` in `main.go`. `flake.nix` reads it
from there, and `release.yml` refuses a tag that doesn't match it. Pushing a
`v*` tag on `main` runs `release.yml`, which uses GoReleaser to:

- build static `linux/amd64` and `linux/arm64` tarballs,
- publish the GitHub release with an empty body,
- push the `scry-bin` PKGBUILD to the AUR.

Nix users build from `flake.nix` at the tag, so Nix needs no publishing step.
A tag with a suffix, such as `v0.2.0-rc1`, is marked as a prerelease and skips
the AUR.

1. Confirm `main` is clean and current. If `git status --porcelain` prints
   anything, stop and ask Raj, because the uncommitted work belongs to Raj.

   ```sh
   git switch main
   git pull --ff-only
   git status --porcelain
   ```

2. List the changes since the last release:

   ```sh
   last=$(git describe --tags --abbrev=0 2>/dev/null)
   git log --oneline ${last:+"$last"..}HEAD
   ```

3. Propose the version and draft the release notes from the template below.
   Below 1.0, bump the minor version for new features or breaking changes and
   the patch version for fixes only. Wait for Raj to approve both.

4. Set `const version` in `main.go` to the approved version, without the `v`.

5. Run every check. Stop and report any failure.

   ```sh
   gofmt -l .
   go mod tidy
   git status --porcelain -- go.mod go.sum
   CGO_ENABLED=0 go vet ./...
   CGO_ENABLED=0 go test ./...
   CGO_ENABLED=0 go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
   nix build path:.#scry --no-link
   nix develop path:. -c goreleaser check
   ```

6. Commit and push the version bump:

   ```sh
   git add main.go
   git commit -m "Version 0.2.0"
   git push origin main
   ```

7. Tag the commit and push the tag:

   ```sh
   tag="v$(sed -n 's/^const version = "\(.*\)"$/\1/p' main.go)"
   git tag "$tag"
   git push origin "$tag"
   ```

8. Watch the release run until it finishes. If `gh run list` prints nothing,
   the run hasn't registered yet, so run it again.

   ```sh
   tag="v$(sed -n 's/^const version = "\(.*\)"$/\1/p' main.go)"
   run=$(gh run list --workflow release.yml --branch "$tag" --limit 1 --json databaseId --jq '.[0].databaseId')
   echo "$run"
   gh run watch "$run" --exit-status
   ```

9. If the run fails, read the failed step:

   ```sh
   gh run view <run id> --log-failed
   ```

   - For a missing `AUR_KEY`, ask Raj to add it, then rerun with
     `gh run rerun <run id>`.
   - For a network or service error, rerun with `gh run rerun <run id>`.
   - For a fault in the code or config, never move or delete a published tag.
     Fix it on `main` and release the next patch version from step 4.

10. Write the approved notes into the release. Keep the notes file in the
    scratchpad, not the repository.

    ```sh
    gh release edit <tag> --notes-file <notes file>
    ```

11. Confirm the release has both tarballs and `checksums.txt`, and that the
    AUR shows the new version:

    ```sh
    gh release view <tag> --json assets --jq '.assets[].name'
    curl -s 'https://aur.archlinux.org/rpc/v5/info?arg[]=scry-bin' | jq -r '.results[0].Version'
    ```

GoReleaser names the release after the tag, such as `v0.2.0`. It keeps an
existing body, so a rerun never overwrites the notes.

### Release notes

Write for someone deciding whether to upgrade. Lead with what changed for
them, ordered by what they would notice first. Never list commits.

```markdown
**One-line headline of the release** {emoji}

- Top feature or change
- Top feature or change
- Other changes users will notice
- Security fixes, with their GO- or CVE identifiers

**Breaking changes**

- What broke, and what to do about it
```

Leave out the breaking changes section when nothing broke. Leave out the
security bullet when nothing was fixed. Mention a config change, such as a
renamed setting, as a breaking change with the line to write instead.

## Dependencies and security

`dependabot.yml` opens weekly pull requests for Go modules and GitHub Actions.
`vulncheck.yml` runs govulncheck every Monday against `main` and the newest
release. Fix and merge only when Raj asks.

### A dependency update

A change to `go.sum` changes the Nix `vendorHash`, so `flake.yml` fails on
every Go module update until the hash is updated.

1. Check out the pull request:

   ```sh
   gh pr checkout <number>
   ```

2. Read the module's changelog between the two versions. Report any breaking
   change, or any change to TLS, IMAP, MIME or HTML parsing, before you go on.

3. Recompute `vendorHash`. The second command prints the new hash. Put it in
   `flake.nix` in place of `nixpkgs.lib.fakeHash`.

   ```sh
   sed -i 's|vendorHash = ".*";|vendorHash = nixpkgs.lib.fakeHash;|' flake.nix
   nix build path:.#scry --no-link 2>&1 | sed -n 's/.*got: *//p'
   ```

4. Run the checks from step 5 of the release list.

5. Commit `flake.nix` to the pull request branch with the message
   `Updated the Nix vendor hash`, and push.

### A vulnerability

govulncheck prints each finding's ID, the affected module and the version
that fixes it.

1. Reproduce the finding locally:

   ```sh
   CGO_ENABLED=0 go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
   ```

2. Apply the fix:

   - For a module, raise it to the fixed version:

     ```sh
     go get <module>@<fixed version>
     go mod tidy
     ```

   - For the standard library, raise the minimum Go version:

     ```sh
     go mod edit -go=<fixed version>
     ```

     When the Go minor version changes, also change `buildGo127Module` and
     `pkgs.go_1_27` in `flake.nix` to match, such as `buildGo128Module` and
     `pkgs.go_1_28`. If the Go that Nix builds with is older than the fix,
     update the locked nixpkgs:

     ```sh
     nix eval --raw path:.#scry.go.version
     nix flake update
     ```

3. Recompute `vendorHash` as in step 3 of the dependency update list.

4. Run the checks from step 5 of the release list.

5. Tell Raj what was fixed. A finding that affects the newest release needs a
   patch release with the fix in the security bullet of its notes.

# CLAUDE.md

Guidance for working in this repository. This file holds coding and
operational principles, plus a short project state. Product behaviour lives in
`docs/product.md`.

## Product spec

`docs/product.md` is Raj's document. Never edit it unless he asks. When the
code and the spec disagree, tell him and change neither on your own. If a
requested change would make the spec wrong, point out the exact lines so he
can update them.

## Coding principles

General and meant to be reused verbatim across projects.

- **Complete names.** Use descriptive, whole-word names for non-trivial
  variables (`listWidth`, not `lw`). Short names are acceptable only for
  receivers, loop indices, `err`, `ok`, and a framework's own idiomatic short
  names: `cmd` for a `tea.Cmd`, `msg` for a `tea.Msg`, `w` and `r` for an
  `http.ResponseWriter` and `*http.Request`. Keep those reserved for that exact
  type: a command that is not a `tea.Cmd` is `command`, not `cmd`.
- **Breathing room.** Separate a statement that produces a value from the
  statement that consumes it with a blank line. For example: an assignment, a
  blank line, then the `if err != nil` check. Group code into readable
  paragraphs.
- **Guard clauses.** Handle edge cases and errors first and return early, so the
  happy path stays unindented and reads straight down the function.
- **No comments.** Code must explain itself through naming and structure.
  (Struct tags are not comments.) The rare exception is a constraint the code
  cannot express on its own: an external or internal protocol, not a
  restatement of what the code does.
- **Procedural code.** Always inline simple logic so readers don't have to jump
  around to see what small functions do. 1-3 line functions shouldn't exist
  unless there's a very good reason, such as wrapping something that could
  change like a hardcoded filepath. Never create a function that is only used
  once. Long procedural functions are fine; reading one top to bottom should
  describe its entire behaviour with minimal jumping around.
- **Functional code.** Prefer functional, stateless code. Some libraries demand
  statefulness and it's fine to follow their style, but everywhere else avoid
  mutable state.
- **Switch over ladders.** Prefer a `switch` (including a type switch) to a long
  `if` / `else if` chain.
- **Co-location.** A self-contained unit lives entirely in its own package: its
  types, behaviour, and rendering together. Its only references from elsewhere
  are where it is wired in at the composition root.
- **Table-driven tests.** Express tests as a table of input → expected cases
  iterated in a loop, not as repeated near-identical assertions.

## Architecture principles

- **Composition root.** `main.go` reads the config, opens the sync log, starts
  the viewer server, launches the viewer browser command and runs the TUI. It
  holds no application logic.
- **Packages point downward.** `tui` imports `mailbox`, `viewer` and `synclog`.
  Those three never import each other or `tui`. `viewer` defines its own `Page`
  type, so it knows nothing about mail. `mailbox` imports `imapsync` and
  `mirror`, which import nothing from scry.
  - `mailbox`: accounts, messages, folder actions, search, and the sync that
    decides which folders, how much history and which images to fetch.
  - `imapsync`: IMAP login, folder roles, batched search, flags and fetch, and
    MIME parsing.
  - `mirror`: encrypted files on disk, keyed from the account password.
  - `viewer`: the local HTTP server and the browser page.
  - `synclog`: daily log files and their retention.
  - `tui`: every screen, key and click.
- **The Elm Architecture (Bubble Tea v2).** State lives in the model,
  transitions happen in `Update`, side effects are `Cmd`s. Never block in
  `Update` and never spawn goroutines from the TUI. Loading an email's body
  from the mirror is a `Cmd`. The viewer's HTTP server runs in its own
  goroutine, started from `main.go`, with its state behind a mutex. Sync
  downloads images for up to 8 emails at once.
- **One path for keys and clicks.** Every clickable element is a bubblezone
  `key:<key>` zone, and a click calls `handleKey` with that key. Add a new
  action as a key first, then render it with `button`, and the click comes for
  free.
- **Account colours come from config order.** `accountColor` maps an account's
  position to the palette. Use it wherever an account appears, including the
  viewer page.
- **Colours adapt to the terminal.** Colours are light and dark pairs, resolved
  through `paint` and `style` once `tea.BackgroundColorMsg` arrives. Never use
  a fixed ANSI colour.
- **The viewer is locked down.** It listens on 127.0.0.1 only, requires the
  random token in every path and rejects any other `Host` header. Email bodies
  are served with a CSP that blocks scripts and every network request and
  sandboxes the document. Images come only from local copies made at sync.
  Attachments are served as `application/octet-stream` downloads. Never loosen
  any of this.
- **Senders learn nothing from reading.** Sync downloads every remote image in
  a new email into the mirror, read or not, so a tracking image only confirms
  delivery. Never fetch anything for Spam. Never send read receipts.
- **Secrets and content stay private.** Passwords come only from each
  account's `password-cmd`. They never appear in the config, logs, cache files
  or on-screen errors. Sync logs record events and counts, never subjects,
  addresses or bodies.

## Operational principles

- Build, vet and test with cgo disabled: `CGO_ENABLED=0 go build -o scry .`,
  `CGO_ENABLED=0 go vet ./...`, `CGO_ENABLED=0 go test ./...`.
- Keep dependencies pure Go and small. Evaluate a candidate by running it.
- Lip Gloss v2 counts borders inside `Width` and `Height`. A bordered box with
  inner width `w` needs `Width(w + 2)`.
- To check a layout without a terminal, write a temporary test in `tui` that
  sends a `tea.WindowSizeMsg`, prints `m.View().Content` through `ansi.Strip`
  and checks every line's `lipgloss.Width`. Delete the test afterwards.
- Raj runs foot inside the herdr multiplexer. Shift+Backspace arrives as plain
  Backspace there, so avoid bindings that depend on it.

## Project state

`main.go` loads the `[[account]]` blocks in file order and exits when there
are none. `mailbox.New` adds Inbox, Sent, Archive, Spam and Trash ahead of each
account's configured folders. There is no sample mail.

Sync mirrors each account into `~/.local/state/scry/mirror/<hash of email>`.
It runs `password-cmd` once per session and keeps the password in memory.
Archive and Sent keep `history-days` (default 365). Other folders are mirrored
in full. Archive is the server's `\Archive` folder, otherwise one named
`Archive`. Remote images are inlined as `data:` URIs, never for Spam and never
from private, loopback or CGNAT addresses. Reading marks the email read on the
server at the next sync. Moving and sending return an error until they are
built.

Sync runs in two passes. The first logs in to every account, applies flag
changes and removals, and counts the emails to download. The second downloads
them. `SyncProgress` returns the downloaded and total counts for the
percentage beside the logo, and each folder writes its counts to the sync log.
The index is saved after every batch of downloads.

Next, in rough order:

1. The offline action queue with its server-side check, including persisting
   pending reads across restarts.
2. SMTP sending, the Markdown subset to HTML, and HTML signatures.
3. Email details and attachments in the viewer page.
4. Release setup for the AUR and Nix, following battui.

## Maintenance

After adding or changing a major feature, re-read this file and update it so the
principles and project state stay accurate.

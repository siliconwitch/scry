# scry

scry is a terminal email client that keeps every account in one place. You
browse, file and write in the terminal. The email you have selected shows in a
browser window beside it.

## Philosophy

- **Configured in one text file.** Every setting lives in
  `~/.config/scry/config.toml`. scry has no settings screen and saves no
  preferences of its own, so the file can live in a dotfiles repo.
- **Unified inbox, but everything else stays separate.** Mail from all accounts
  appears in one inbox. Each email stays in the account it arrived in. Its
  archive, folders, spam and trash belong to that account. scry never moves
  mail between accounts.
- **Archive by default.** The usual flow is read, then archive. Trash is rare.
- **Mail on this machine, encrypted.** scry keeps a local mirror of every
  account. Nobody can read the mirror without the account password.
- **Plain writing.** Emails use one font and one size, with bold and bullet
  lists when they help.
- **Terminal for control, browser for viewing.** Navigation, actions and filing
  happen in the terminal, by keyboard or mouse. HTML email, such as tickets and
  travel bookings, renders in a real browser engine.

## Glossary

- **Account:** one email address, set by one `[[account]]` block in the
  config. Accounts are numbered by their order in the config, starting at 1.
- **Server:** the account's mail provider, such as Gmail. It holds the real
  copy of every email.
- **Mirror:** the encrypted copy of each account's mail on this machine.
- **Sync:** one exchange with an account's server. It sends your changes and
  fetches the server's.
- **Viewer:** the browser window that shows the selected email's content.
  scry serves it from this machine.
- **Draft:** the one email you are writing. It lives in scry, not on the
  server.
- **Outbox:** an account's emails that are waiting to be sent.
- **Status light:** the scry logo in the top left, coloured by sync state.
- **Sync log:** the record of what each sync did.

## Strict feature list

Everything you can see and do in scry. Anything not listed here is out.

### Accounts and folders

- scry works with any number of IMAP accounts, including Gmail.
- The sidebar starts with one Inbox that shows mail from every account.
- An account with `combined-inbox = false` in its config stays out of that
  Inbox. Its mail still shows in its own Inbox.
- Below it, each account shows Inbox, Sent, Archive, Spam and Trash, plus the
  folders the config lists for it, such as `Events` and `Reference`. Other
  folders on the server, including Drafts, stay hidden.
- An account's Outbox shows below its Inbox while it holds unsent email. It is
  hidden when empty.
- The number keys along the top of the keyboard jump to each account's Inbox.
  1 jumps to the first account's Inbox, 2 to the second, and so on up to 9.
- Each folder shows its unread count.
- Each account has its own colour, set by its order in the config. The colour
  marks the account everywhere: the sidebar, the email list, the draft, the
  sync log and the viewer.

### Email list

- Each row shows an unread dot in the account's colour, the sender, the
  subject, an attachment mark and the date.
- The combined Inbox and search results also show each email's account. Search
  results also show its folder.
- The date shows the time for today, the weekday for the last 6 days, the day
  and month for this year, and the month and year before that.

### Reading

- Moving to an email shows it in the viewer and marks it read.
- The config can name a browser command that opens the viewer at startup, such
  as `chromium --app=%s`. The sync log view shows the viewer's address for any
  browser.
- The viewer header shows the sender and recipients. Clicking a name or
  address copies it.
- The viewer lists attachments. Clicking one downloads it.
- `y` copies the sender's address.

### Organising

- `A` archives an email to its account's Archive folder.
- `M` moves an email to one of its account's folders. The list leaves out the
  current folder, Sent, Spam and Trash.
- `!` moves an email to its account's Spam folder.
- `D` moves an email to its account's Trash folder. Deleting from Spam does the
  same.
- An email in the Outbox can't be archived, moved or marked as spam.

### Searching

- `/` or a click on the search box starts a search. Esc clears it.
- Search is fuzzy and covers every account and folder.
- Filters narrow a search to the subject, the body or a person, such as
  `flight subject:booking person:anna`. A person filter matches the sender and
  the recipients. Quotes keep words together, such as `subject:"team lunch"`.

### Writing

- `n` starts a new email. `R` replies to the sender. `r` replies to the sender
  and every recipient.
- scry opens the email in `$EDITOR`. Header lines set From, To, Cc, Bcc,
  Subject and Attach, then a blank line starts the body.
- Bcc recipients get the email, but no recipient sees the Bcc line.
- A reply quotes the original, adds `Re:` to the subject and threads under the
  original.
- A reply always goes from the account the original email arrived in.
- You write plain text, with `**bold**` and `-` bullet lists.
- Each account can name an HTML file as its signature.
- Images and files can be attached, one Attach line each.

### The draft

- scry holds one draft at a time: the email you are writing. It never uses the
  server's Drafts folder.
- The draft lives only in memory and is gone when scry closes.
- `e` edits the draft, `h` hides it to browse mail, `w` returns to it and `x`
  discards it.
- The draft shows what blocks sending: an unknown header line, an unknown
  account, a missing attachment or an empty To line.
- Starting a new email or reply while a draft exists asks before discarding
  the draft.

### Sending

- `s` starts the send countdown.
- `u` stops the countdown and returns the email to the draft.
- `D` on an Outbox email returns it to the draft, so you can change it, send it
  from another account or discard it. If a draft exists, scry asks before
  discarding it.

### Sync status and log

- The scry logo is the status light. It shows the highlight colour when idle,
  yellow while syncing and red after an error.
- While syncing, the logo shows how much of the new mail has downloaded.
- Ctrl+R syncs every account.
- `L` or a click on the logo opens the sync log.
- Each log line shows its time, its account and what happened, coloured by
  account.
- The log updates live and follows new lines while scrolled to the bottom.
- In the log, an account's number key shows only that account's lines. The
  same key again shows every account.

### Keyboard and mouse

- Every action has a keybind and a mouse equivalent. The footer shows the keys
  for the current view as buttons.
- `j` and `k`, the arrow keys, Page Up, Page Down, Home and End move through a
  list. Tab, `h` and `l` switch between the sidebar and the email list.
- Clicking an email selects it and shows it in the viewer. Clicking a folder,
  button or icon runs it.
- The mouse wheel scrolls lists.
- `q` quits.

## Strict behaviour list

Everything scry does that you don't see or drive directly.

### Offline mirror

- scry mirrors every folder it shows, including attachments, onto this machine.
- The mirror holds emails for offline reading and search, plus Outbox emails
  and changes waiting for a sync. It holds nothing else.
- Archive and Sent keep the last `history-days` of mail, 365 by default. Every
  other folder is mirrored in full.
- Each account's mirror is encrypted with a key derived from that account's
  password.
- When an account's password changes, scry deletes that account's mirror and
  downloads it again. The server always holds the real copy.
- Reading, archive, move, spam and delete work offline. scry applies them to
  the local mirror at once and starts a sync to send them to the server.
  Without a connection, they wait and go out once the connection returns.
- Before applying a queued action, scry checks the email's current state on
  the server. If another client already moved or deleted the email, scry drops
  the action and notes it in the sync log. A dropped action is not an error.
- Moving email needs a server with the UIDPLUS extension, which reports where
  a moved email lands. Gmail, Fastmail and Dovecot have it. Without it, scry
  would have to download the email again and fetch its images a second time.

### Privacy

- scry downloads every remote image in a new email during sync, whether or not
  you read it. A tracking image then shows the sender only that the email was
  delivered, not whether or when you read it.
- scry fetches nothing for Spam, so spammers can't confirm your address is
  live. Images in Spam stay blank.
- scry never fetches an image from a private or local network address.
- scry never sends read receipts.
- Only this machine can reach the viewer, and its address includes a secret
  token.
- Scripts in email never run. The viewer shows images from the mirror and
  never contacts the network.

### Passwords

- scry gets each password by running a command from the config, such as
  `pass mail/personal`. It never stores passwords.
- scry runs the command once per session and keeps the password in memory.
- If the command fails, scry doesn't run it again until Ctrl+R, so a locked
  password store doesn't prompt over and over.

### Sync

- Every sync works the same way. It sends the Outbox, applies your moves,
  pushes your reads, then fetches new mail and changes made in other clients.
- Each account syncs on its own, so a slow or offline account never holds up
  the others.
- scry syncs when it opens, after you read, archive, move, spam, delete or send
  an email, and every 10 minutes.
- scry keeps a connection open to each account's Inbox. The server reports new
  mail on it, and scry syncs at once.
- An action during a sync queues another sync, which starts when the current
  one ends. One queued sync covers every action taken while it waits.

### Network

- Sync expects a flaky network: sleep and wake, switching networks, phone
  tethering and airline wifi.
- A dead connection never stalls scry. A connection that goes quiet for a
  minute in the middle of a step is dropped and the sync fails.
- After a failed sync, scry retries after 10 seconds, doubling the wait up to
  5 minutes, until the server answers.
- scry syncs as soon as the machine wakes.

### Sending

- **No accidental sends.** Every send waits 5 seconds before it moves to the
  Outbox.
- When the countdown ends, the email moves to its account's Outbox. scry reads
  the signature and attachments at that moment.
- Sync sends the Outbox. An email that fails to send stays there and is retried
  on every sync, until it is sent or returned to the draft.
- Before sending, scry checks the Sent folder for the email, so an email that
  an interrupted earlier try already sent isn't sent twice.
- scry sends a plain text version and an HTML version of the same email.
- A sent email lands in its account's Sent folder.

### Sync log

- The sync log records every step and decision a sync takes, tagged with its
  account, so a problem can be traced afterwards.
- Logs contain no email content.
- scry stores sync logs in `~/.local/state/scry/logs`. It deletes logs older
  than `log-days`, 7 by default.

## Configuration example

```toml
viewer = "chromium --app=%s"
log-days = 7
history-days = 365

[[account]]
name = "Personal"
email = "me@example.com"
imap = "imap.gmail.com:993"
smtp = "smtp.gmail.com:465"
password-cmd = "pass mail/personal"
signature = "~/.config/scry/personal.html"
folders = ["Events", "Reference"]
```

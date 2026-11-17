# scry

scry is a terminal email client that keeps every account in one place. You
browse, file and write in the terminal. The email you have selected shows in a
browser window beside it.

## Philosophy

- **Configured in one text file.** Every setting lives in
  `~/.config/scry/config.toml`, or the file that `SCRY_CONFIG` names. scry
  has no settings screen and saves no preferences of its own, so the file can
  live in a dotfiles repo. scry refuses to start when the config has a
  mistake, and says what to fix.
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
- The number keys along the top of the keyboard jump to an Inbox. 0 jumps to
  All inboxes, 1 to the first account's Inbox, 2 to the second, and so on up
  to 9.
- Each folder shows its unread count. Spam shows its count only while selected.
- Each account has its own colour, set by its order in the config. The colour
  marks the account everywhere: the sidebar, the email list, the draft, the
  sync log and the viewer.

### Email list

- Each row shows an unread dot in the account's colour, the sender, the
  subject, an attachment mark and the date.
- The combined Inbox and search results also show each email's account. Search
  results also show its folder.
- Dates are short and relative to today.

### Reading

- The selected email shows in the viewer.
- An email is marked read when you look at it in the email list, not when you
  pass it from the sidebar.
- Choosing a folder selects its first email.
- scry opens on the combined Inbox with its first email selected.
- The config can name a browser command that opens the viewer at startup, such
  as `chromium --app=%s`. The sync log view shows the viewer's address for any
  browser.
- The viewer window closes when scry quits.
- The viewer header shows the sender and the To, Cc and Bcc recipients. Bcc
  shows when the email carries one, which is normally only email you sent.
  Clicking a name or address copies it.
- The viewer lists attachments. Clicking one of these shows it in place of the
  email:
  - images and PDFs
  - Word, Excel and PowerPoint files (docx, xlsx and pptx)
  - video and audio clips
  - text files, such as txt, csv and md
- Clicking any other attachment downloads it.
- Text in PDFs and Office files can be selected and copied. In a PDF, a double
  click selects a word and a triple click selects a line.
- You can step through an email's viewable attachments without leaving the
  viewer.
- Clicking a link in an email opens it with the browser command that `links`
  names in the config, such as `firefox %s`. Only `http` and `https` links
  open.
- `y` copies the sender's address.

### Organising

- `A` archives an email to its account's Archive folder.
- `M` moves an email to one of its account's folders. The list leaves out the
  current folder, Sent, Spam and Trash.
- `!` moves an email to its account's Spam folder. In Spam, `!` moves it back
  to the Inbox.
- `D` moves an email to its account's Trash folder. Deleting from Spam does the
  same.
- `E` in an account's Trash deletes every email in it for good, after asking.
  It deletes only the emails scry has downloaded, so email that reaches Trash
  on the server after the last sync stays.
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
- scry opens the email in your editor. Header lines set From, To, Cc, Bcc, Subject and Attach, then a blank
  line starts the body.
- Bcc recipients get the email, but no recipient sees the Bcc line.
- A reply quotes the original email unchanged, below your signature, adds
  `Re:` to the subject and threads under the original.
- The editor shows one line in place of the quoted email. Deleting the line
  leaves the quote out.
- A reply starts from the account the original email arrived in.
- You write plain text, with `**bold**`, `-` bullet lists and `![alt](path)`
  inline images.
- Each account can name an HTML file as its signature.
- Each account can set the name recipients see beside its address.
- Images and files can be attached, one Attach line each.

### The draft

- scry holds one draft at a time: the email you are writing. It never uses the
  server's Drafts folder.
- The draft lives only in memory and is gone when scry closes.
- `e` edits the draft, `h` hides it to browse mail, `w` returns to it and `x`
  discards it.
- The draft shows anything that would stop it sending.
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

- Every action in the terminal has a keybind and a mouse equivalent. The
  viewer is mouse only.
- The footer shows the essential keys for the current view. `?` shows them
  all.
- `j` and `k`, Up and Down, Page Up, Page Down, Home and End move through a
  list.
- Tab switches between the sidebar and the email list. `h` or Left focuses
  the sidebar. `l`, Right or Enter focuses the email list.
- Clicking an email selects it and shows it in the viewer. Clicking a folder,
  button or icon runs it.
- The mouse wheel scrolls lists.
- `q` quits. Ctrl+C quits from anywhere, including while typing a search.

## Strict behaviour list

Everything scry does that you don't see or drive directly.

### Offline mirror

- scry mirrors every folder it shows, including attachments, onto this machine.
- The mirror holds emails for offline reading and search, plus Outbox emails
  and changes waiting for a sync. It holds nothing else.
- Archive and Sent keep the last `history-days` of mail, 365 by default. Every
  other folder is mirrored in full.
- When an account's password changes, scry deletes that account's mirror and
  downloads it again from the server. The account's Outbox and any changes
  waiting for a sync are lost, because only the mirror holds them.
- Reading, archive, move, spam, delete and emptying Trash work offline. scry
  applies them to the local mirror at once and starts a sync to send them to
  the server.
  Without a connection, they wait and go out once the connection returns.
- Before applying a queued action, scry checks the email's current state on
  the server. If another client already moved or deleted the email, scry drops
  the action and notes it in the sync log. A dropped action is not an error.
- Moving and deleting email need a server with the UIDPLUS extension. Gmail,
  Fastmail and Dovecot have it.

### Privacy

- scry downloads every remote image in a new email during sync, whether or not
  you read it. A tracking image then shows the sender only that the email was
  delivered, not whether or when you read it.
- scry fetches nothing for Spam or Trash, so spammers can't confirm your
  address is live. Images in Spam and Trash stay blank.
- An email moved out of Spam or Trash into another folder gets its images at
  the next sync.
- scry never fetches an image from a private or local network address.
- scry never sends read receipts.
- Only this machine can reach the viewer, and its address includes a secret
  token.
- Scripts in email never run. The viewer shows images from the mirror and
  never contacts the network.
- The viewer never opens a link itself. Links go to the `links` browser.
- Scripts and macros in attachments never run.

### Passwords

- scry gets each password by running a command from the config, such as
  `pass mail/personal`. It never stores passwords.
- scry runs the command once per session and keeps the password in memory.
- If the command fails, scry doesn't run it again until Ctrl+R, so a locked
  password store doesn't prompt over and over.

### Sync

- Each account syncs on its own, so a slow or offline account never holds up
  the others.
- New mail and changes from other clients show up within seconds.

### Network

- Sync expects a flaky network: sleep and wake, switching networks, phone
  tethering and airline wifi.
- A dead connection never stalls scry.
- scry keeps retrying a failed sync, waiting longer each time.
- scry syncs as soon as the machine wakes.

### Sending

- **No accidental sends.** Every send waits 5 seconds before it moves to the
  Outbox.
- When the countdown ends, the email moves to its account's Outbox.
- Sync sends the Outbox. An email that fails to send stays there and is retried
  on every sync, until it is sent or returned to the draft.
- An interrupted send is never sent twice.
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
links = "firefox %s"
log-days = 7
history-days = 365

[[account]]
name = "Personal"
display-name = "Kit Hale"
email = "me@example.com"
imap = "imap.gmail.com:993"
smtp = "smtp.gmail.com:465"
password-cmd = "pass mail/personal"
signature = "~/.config/scry/personal.html"
folders = ["Events", "Reference"]
```

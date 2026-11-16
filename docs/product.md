# scry

scry is a terminal email client that keeps every account in one place. You
browse, file and write in the terminal. The email you have selected shows in a
browser window beside it.

## Philosophy

- **Configured in one text file.** Every setting lives in
  `~/.config/scry/config.toml`. scry has no settings screen and saves no
  preferences of its own, so the file can live in a dotfiles repo.
- **Only what email needs.** scry reads, replies, writes, archives, files and
  searches. Everything else stays out.
- **One place for every account.** Mail from all accounts appears in one list.
  Each email stays in the account it arrived in. Its archive, folders, spam and
  trash belong to that account. scry never moves mail between accounts.
- **Archive by default.** The usual flow is read, then archive. Trash is rare.
- **Mail on this machine, encrypted.** scry keeps a local mirror of every
  account, so reading and searching work offline. Nobody can read the mirror
  without the account password.
- **Plain writing.** Emails use one font and one size, with bold and bullet
  lists when they help.
- **No accidental sends.** Every send waits a few seconds and can be stopped.
- **Terminal for control, browser for viewing.** Navigation, actions and filing
  happen in the terminal, by keyboard or mouse. HTML email, such as tickets and travel
  bookings, renders in a real browser engine.

## Accounts and folders

- scry works with any number of IMAP accounts, including Gmail.
- Moving email needs a server with the UIDPLUS extension, which reports where
  a moved email lands. Gmail, Fastmail and Dovecot have it. Without it, scry
  would have to download the email again and fetch its images a second time.
- The inbox shows mail from every account together. Each email shows which
  account it belongs to.
- Each account has its own colour, set by its order in the config. The colour
  marks the account everywhere: the sidebar, the email list, the draft and the
  viewer.
- Each account shows Inbox, Sent, Archive, Spam and Trash, plus the folders the
  config lists for it, such as `Events` and `Reference`. Other folders on the
  server, including Drafts, stay hidden.
- scry gets each password by running a command from the config, such as
  `pass mail/personal`. It never stores passwords.

## Offline mirror

- scry mirrors every folder it shows, including attachments, onto this machine.
  Reading and searching work without a network connection.
- scry also downloads every remote image in a new email during sync, whether
  or not you read it. A tracking image then shows the sender only that the
  email was delivered, not whether or when you read it. Spam is the exception:
  scry fetches nothing for it, so spammers can't confirm your address is live,
  and images in Spam stay blank.
- Each account's mirror is encrypted with a key derived from that account's
  password. The files on disk can't be read without it.
- When an account's password changes, scry deletes that account's mirror and
  downloads it again. The server always holds the real copy.
- Archive, move, spam and delete work offline. scry applies them to the local
  mirror at once and queues them for the server.
- Before applying a queued action, scry checks the email's current state on
  the server. If another client already moved or deleted the email, scry drops
  the action and notes it in the sync log. A dropped action is not an error.

## Sync status

- The scry logo is the status light. It shows the highlight colour when idle,
  yellow while syncing and red after an error.
- A keybind or a click on the logo opens the sync log.
- Ctrl+R starts a sync.
- scry stores sync logs in `~/.local/state/scry/logs`. Logs contain no email
  content. scry deletes logs older than 7 days by default.

## Reading

- scry starts a viewer server when it opens. The selected email renders in the
  viewer as soon as you move to it.
- The config can name a browser command that opens the viewer at startup, such
  as `chromium --app=%s`. Without one, scry prints the viewer's address for any
  browser.
- Only this machine can reach the viewer, and its address includes a secret
  token.
- Scripts in email never run. The viewer shows images from the mirror and
  never contacts the network.
- scry never sends read receipts.
- Attachments are listed in the viewer. Clicking one downloads it.

## Organising

- Archive moves an email to its account's Archive folder. It is the main action.
- Move files an email in one of its account's folders.
- Spam moves an email to its account's Spam folder.
- Delete moves an email to its account's Trash folder. Deleting from Spam does
  the same.

## Searching

- Search is fuzzy and covers every account and folder.
- Filters narrow a search to the subject, the body or a person, such as
  `flight subject:booking person:anna`. A person filter matches the sender and
  the recipients.

## Writing

- scry opens new emails and replies in `$EDITOR`.
- You write plain text, with `**bold**` and `-` bullet lists. scry sends a plain
  text version and an HTML version of the same email.
- Each account can name an HTML file as its signature.
- Images and files can be attached.
- A reply always goes from the account the original email arrived in.

## The draft

- scry holds one draft at a time: the email you are writing. It never uses the
  server's Drafts folder.
- The draft lives only in memory and is gone when scry closes.
- You can hide the draft to browse mail, then return to it.
- Starting a new email or reply while a draft exists warns that the draft will
  be discarded.

## Sending

- Sending starts a 5 second countdown. The email goes out when it ends.
- A button or keybind stops the countdown and returns the email to the draft.
- A sent email lands in its account's Sent folder.

## Keyboard and mouse

- Every action in the terminal has a keybind and a mouse equivalent.
- Clicking an email selects it and shows it in the viewer. Clicking the search
  box starts a search. Clicking a folder, button or icon runs it.
- The mouse wheel scrolls lists.

## Configuration example

```toml
viewer = "chromium --app=%s"
log-days = 7

[[account]]
name = "Personal"
email = "me@example.com"
imap = "imap.gmail.com:993"
smtp = "smtp.gmail.com:465"
password-cmd = "pass mail/personal"
signature = "~/.config/scry/personal.html"
folders = ["Events", "Reference"]
```

## Not included

scry has no settings screen, templates, rules, calendar, contacts, plugins,
font choices or text colours. It never moves mail between accounts.

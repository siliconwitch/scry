# scry

A terminal email client that keeps every account in one place. You browse,
file and write mail in the terminal. The selected email renders in a browser
window beside it.

- One unified inbox. Each account keeps its own folders, archive, spam and
  trash.
- An encrypted mirror of every account on your machine, so reading, filing and
  search work offline.
- Configured in one text file that can live in your dotfiles.
- Every remote image downloads at sync, so tracking pixels can't tell when you
  read an email. Scripts never run, and nothing loads for Spam or Trash.
- Write in `$EDITOR` with `**bold**`, `-` bullet lists and `![alt](path)`
  inline images. scry sends plain text and HTML versions of each email.
- Open attachments in the viewer without saving them: images, PDFs, Word,
  Excel and PowerPoint files, video and audio, and text files such as CSV and
  Markdown. Text in PDFs and Office files can be selected and copied.
  Attachments never contact the network, and scripts and macros in them
  never run.

Built in Go with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss). The viewer renders PDFs
with [EmbedPDF](https://github.com/embedpdf/embed-pdf-viewer) and Office files
with [@silurus/ooxml](https://github.com/yukiyokotani/office-open-xml-viewer).
It ships as a single static binary with both built in.

## Requirements

- A web browser for the viewer. scry is tested with Chromium, opened as an app
  window with `chromium --app=%s`. Playing MP4 video needs a browser built with
  H.264 support, which most are.
- A password manager with a command line, or a script that prints each
  password. See [Passwords](#passwords).
- An editor in `$EDITOR` for writing email.

scry needs nothing else at runtime. The PDF and Office viewers are part of the
binary.

## Install

### Arch

Install from the [AUR](https://aur.archlinux.org/packages/scry-bin) with your
favourite helper:

```sh
paru -S scry-bin
```

### Nix

Try it without installing:

```sh
nix run github:siliconwitch/scry
```

Install it into your profile:

```sh
nix profile install github:siliconwitch/scry
```

Or add the flake as an input to your NixOS configuration:

```nix
{
  inputs.scry.url = "github:siliconwitch/scry";

  # In your system configuration:
  environment.systemPackages = [ scry.packages.${pkgs.stdenv.hostPlatform.system}.default ];
}
```

### Prebuilt binary

Every release ships static `amd64` and `arm64` tarballs on the
[releases page](https://github.com/siliconwitch/scry/releases):

```sh
tar xzf scry_*_linux_amd64.tar.gz
sudo install -Dm755 scry /usr/local/bin/scry
```

## Build it yourself

You need [Go](https://go.dev) 1.27 or newer. The viewer's PDF and Office
libraries are checked in under `viewer/`, so nothing else is needed.

```sh
git clone https://github.com/siliconwitch/scry.git
cd scry
CGO_ENABLED=0 go build -o scry .
sudo install -Dm755 scry /usr/local/bin/scry
```

On Nix, run `nix develop` for the same toolchain, or `direnv allow` once with
[direnv](https://direnv.net) hooked into your shell.

## Configure

scry reads `~/.config/scry/config.toml`. Set `SCRY_CONFIG` to read another
path. Start from [`config.example.toml`](config.example.toml). The Arch and Nix
packages install a copy under `share/doc/scry/`.

```toml
viewer = "chromium --app=%s"
links = "firefox %s"
notify = 'notify-send -a scry "%s | %s" "%s"'

[[account]]
name = "Personal"
email = "me@example.com"
imap = "imap.gmail.com:993"
smtp = "smtp.gmail.com:465"
password-cmd = "pass mail/personal"

[[account]]
name = "Work"
email = "me@example.org"
imap = "imap.fastmail.com:993"
smtp = "smtp.fastmail.com:465"
password-cmd = "pass mail/work"
signature = "~/.config/scry/work.html"
folders = ["Events", "Reference"]
```

### Settings

| Setting        | Default | Purpose                                                                 |
| -------------- | ------- | ----------------------------------------------------------------------- |
| `viewer`       |         | Browser command that opens the viewer. `%s` is the viewer's address.    |
| `links`        |         | Browser command that opens a link clicked in an email. `%s` is the link. |
| `notify`       |         | Command that shows a notification for a new email. Its first, second and third `%s` are the sender, the subject and the start of the text. |
| `log-days`     | `7`     | Days to keep sync logs                                                  |
| `history-days` | `365`   | Days of Archive and Sent to keep on this machine. Older mail stays on the server. |

Leave out `viewer` and press `L` in scry to see the viewer's address. Open it
in any browser.

While scry runs, it keeps the viewer's address in
`$XDG_RUNTIME_DIR/scry/viewer` and deletes the file when it quits. A script can
reopen a closed viewer from it:

```sh
chromium --app="$(cat "$XDG_RUNTIME_DIR/scry/viewer")"
```

The address includes the viewer's secret token, so the file has mode `0600`.
scry writes no file when `XDG_RUNTIME_DIR` is unset.

The viewer never opens links itself, so it never reaches the network. Set
`links` to send them to another browser. `firefox %s` opens each link as a tab
in a running Firefox, or in a new window.

scry runs `notify` once for each unread email that reaches an Inbox within two
minutes of the server receiving it.

scry splits `viewer`, `links`, `notify` and `password-cmd` into arguments the
way a shell does, so quotes keep spaces inside one argument. It runs each
command without a shell, so `$HOME`, `~` and `$(…)` stay as written. To add
logic, run `sh -c`. Pass the values as arguments and read them as `$1`, `$2`
and `$3`, so email text never reaches the shell as code. For example, this
skips notifications while the screen is locked:

```toml
notify = '''sh -c 'loginctl show-session "$XDG_SESSION_ID" -p LockedHint | grep -q yes || notify-send -a scry "$1" "$2"' scry "%s" "%s"'''
```

### Accounts

Add one `[[account]]` block per email address. Their order sets each account's
colour and its number key, so `1` jumps to the first account's Inbox.

| Field          | Purpose                                                                 |
| -------------- | ----------------------------------------------------------------------- |
| `name`         | The account's label in scry                                             |
| `display-name` | Optional name recipients see beside your address                       |
| `email`        | The address you log in and send with                                    |
| `imap`         | `host:port`. Port 993 uses TLS. Other ports use STARTTLS.               |
| `smtp`         | `host:port`. Port 465 uses TLS. Other ports use STARTTLS.               |
| `password-cmd` | Command that prints the account's password                              |
| `signature`    | Optional HTML file appended to every email you send                     |
| `folders`      | Optional server folders to show beside Inbox, Sent, Archive, Spam and Trash |
| `combined-inbox` | Optional. Set to `false` to keep the account out of the combined Inbox. Its mail still shows in its own Inbox. |

Gmail needs an [app password](https://myaccount.google.com/apppasswords), not
your account password.

### Passwords

scry never stores passwords. It runs each account's `password-cmd` once per
session and uses the first line of its output. With
[pass](https://www.passwordstore.org), store the password and point scry at
it:

```sh
pass insert mail/personal
```

```toml
password-cmd = "pass mail/personal"
```

Any password manager with a command line works the same way:

| Manager                        | `password-cmd`                                  |
| ------------------------------ | ----------------------------------------------- |
| [gopass](https://www.gopass.pw) | `gopass show -o mail/personal`                 |
| [1Password](https://developer.1password.com/docs/cli/) | `op read op://Private/personal-mail/password` |
| [rbw](https://github.com/doy/rbw) (Bitwarden) | `rbw get personal-mail`          |
| GNOME Keyring or KWallet       | `secret-tool lookup service scry account personal` |

scry splits the command on spaces and runs it without a shell, so quotes,
pipes and `~` don't work. For anything more, write a script that prints the
password and point `password-cmd` at it.

If the command fails, such as when your password store is locked, scry waits
until you press Ctrl+R before trying again.

## Data

scry keeps its encrypted mirror in `~/.local/state/scry/mirror` and its sync
logs in `~/.local/state/scry/logs`, or under `$XDG_STATE_HOME` when set. The
mirror also holds unsent Outbox email and changes waiting for a sync.

## Contributing

Contributions are welcome. Open an issue or a pull request. You're welcome to
contribute with AI assistance too. Just read and test your code before
submitting.

## License

[MIT](LICENSE) © Raj Nakarja

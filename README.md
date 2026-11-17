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
  read an email. Scripts never run, and nothing loads for Spam.
- Write in `$EDITOR` with `**bold**` and `-` bullet lists. scry sends plain
  text and HTML versions of each email.

Built in Go with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss). It ships as a single
static binary.

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

You need [Go](https://go.dev) 1.27 or newer.

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
| `log-days`     | `7`     | Days to keep sync logs                                                  |
| `history-days` | `365`   | Days of Archive and Sent to keep on this machine. Older mail stays on the server. |

Leave out `viewer` and press `L` in scry to see the viewer's address. Open it
in any browser.

### Accounts

Add one `[[account]]` block per email address. Their order sets each account's
colour and its number key, so `1` jumps to the first account's Inbox.

| Field          | Purpose                                                                 |
| -------------- | ----------------------------------------------------------------------- |
| `name`         | The account's label in scry                                             |
| `email`        | The address you log in and send with                                    |
| `imap`         | `host:port`. Port 993 uses TLS. Other ports use STARTTLS.               |
| `smtp`         | `host:port`. Port 465 uses TLS. Other ports use STARTTLS.               |
| `password-cmd` | Command that prints the account's password                              |
| `signature`    | Optional HTML file appended to every email you send                     |
| `folders`      | Optional server folders to show beside Inbox, Sent, Archive, Spam and Trash |

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

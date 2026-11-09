# dev — mechanics sheet

What the original (mise 2026.11.3, Windows x64) does, read from its code and embedded help text,
and the rules this rebuild ("dev") follows. Sources are function addresses (Ghidra) or string
addresses in `.rdata`. "estimated" means it was looked for and not found in the code.

## Process and exit codes

| Rule | Value | Source |
|---|---|---|
| Success exit code | 0 | Rust main `FUN_147378390` (returns 0 when the command returns Ok) |
| Error exit code | 1 | `FUN_147378390`: error path sets `uVar6 = 1`, logged via `mise_util::exit` (`crates\mise-util\src\exit.rs`) |
| Child process exit code | passed through | `FUN_147378390` → `FUN_1488e0f10(code)` |
| Bad command-line usage | 2 | clap default (estimated) |
| Error line format | `dev ERROR <message>` then `dev ERROR Run with --verbose or DEV_VERBOSE=1 for more information` | hint string at `0x14c71e690`; prefix built at runtime (estimated) |
| CRT entry | `entry 0x14a7596e0` → `0x14a759560` → `0x147413d00` | Ghidra |

## Version line

`<version> <os>-<arch> (<build date>)`, e.g. `2026.11.3 windows-x64 (2026-11-06)`.
`--version`, `-V`, `-v` alone and `version` print the same line; `version --json` adds fields.
Source: help text of `mise version` (strings dump line 22992).

## Directories (Windows)

| What | Default | Override | Source |
|---|---|---|---|
| Data (installs) | `%LOCALAPPDATA%\dev` | `DEV_DATA_DIR` | install-script template (`src\cli\generate\install_script.rs`) |
| Installs | `<data>\installs\<tool>\<version>` | | help of `mise install` |
| Global config | `%USERPROFILE%\.config\dev\config.toml` (or `XDG_CONFIG_HOME`) | `DEV_CONFIG_DIR` | install-script template |
| Cache | `<data>\cache` | `DEV_CACHE_DIR` | `--localize` layout |
| State | `<data>\state` | `DEV_STATE_DIR` | `--localize` layout |

On Linux and macOS: data `$XDG_DATA_HOME/dev` or `~/.local/share/dev`, config `~/.config/dev`
(same install-script template). The program itself goes to `%LOCALAPPDATA%\dev\bin` on Windows and
`~/.local/bin` elsewhere (`DEV_INSTALL_DIR`) — estimated (new in dev).

## Maven

The original has no Maven code of its own and no `MAVEN_HOME`/keystore handling, but
its **built-in registry** maps `maven` → `aqua:apache/maven` (then `asdf:mise-plugins/mise-maven`,
`vfox:jdx/vfox-maven`; string `0x14b63a850`), and
its **embedded aqua registry** carries the download record (read from the binary's data):

| Rule | Value | Source |
|---|---|---|
| Maven download | `https://archive.apache.org/dist/maven/maven-{major}/{version}/binaries/apache-maven-{version}-bin.{format}` | aqua record `apache/maven` at `0x14bed45d7` (9 045 bytes, table entry `0x14c46bc68`) |
| Maven version filter | `not (Version matches "-(rc|alpha|beta)")` | same record |
| Maven checksum | `.sha1` (`^(\b[A-Fa-f0-9]{40}\b)`) for versions `<= 3.2.5` and `<= 3.3.1`; `.sha512` (`{128}`) after | same record, version overrides `0x14bed4b45`, `0x14bed5080`, `0x14bed5151` |
| Maven program | `apache-maven-{v}/bin/mvn`; Windows `mvn.bat` (`<= 3.2.5`) / `mvn.cmd` | same record |
| Maven check | `mvn --version` → `Apache Maven {{version}}` | registry test `0x14b63aa88` |
| Archive format | zip on Windows, tar.gz elsewhere | estimated: the record's format field is stored in a form these tools can't print |
| Maven version list | Maven Central `maven-metadata.xml` (3.x+) with the filter above | estimated (the record's version source isn't readable) |
| Home variable | `MAVEN_HOME` = install folder, set like `JAVA_HOME` (prompt hook, exec/run) | person's request (not in the original) |
| Windows user environment (JAVA/MAVEN_HOME + `%VAR%\bin`) | global version (`use -g`, or `use` writing the global config): always written; `install` (also when already installed) and project `use`: written only when the variable is missing or its folder no longer exists | person's report (round 8) |
| PATH | `<install>/bin` | record's `bin/mvn` |

## Python

| Rule | Value | Source |
|---|---|---|
| Build list | `https://mise-versions.jdx.dev/python-precompiled-<arch>-<os>.gz`: gzip, one file name per line | string `python-precompiled-` `0x14c48bb5e`; versions host `https://mise-versions.jdx.dev/` |
| Platform | `<arch>-<os>`: `x86_64`/`aarch64` + `pc-windows-msvc` / `apple-darwin` / `unknown-linux-gnu`; `python.precompiled_arch`/`_os` override (dev: `DEV_PYTHON_PRECOMPILED_ARCH`/`_OS`) | strings `-unknown-linux` `-apple-darwin` `-pc-windows` (strings dump line 13410), settings schema |
| File name | `cpython-<version>+<tag>-<arch>-<os>-<flavor>.tar.gz` | strings `cpython-`, `install_only.tar.gz` |
| Download | `https://github.com/astral-sh/python-build-standalone/releases/download/<tag>/<file>` | string (strings dump line 1604) |
| Choice per version | free-threaded builds only when the flavor asks; no flavor set: `install_only_stripped` (0) < `install_only` (1) < other (2); flavor set: ends with it (0) < other (1); then newest tag | `FUN_144173b20` (Ghidra): `"freethreaded"` `0x14c4f9f2d`, `"install_only_stripped"`, `"install_only"`, date tag parsed and negated |
| Checksum | release `SHA256SUMS` (the original verifies GitHub attestations instead) | estimated |
| Windows extras | `python3.exe` hard link to `python.exe` (copy when linking fails); `pip.cmd` and `pip3.cmd` = `@echo off\r\n"%~dp0python.exe" -m pip %*\r\n` written only when `Scripts\pip.exe` is missing | `FUN_1442c2420` (Ghidra); strings "python: hardlink … failed (…); copying executable" |
| PATH | Windows: install folder and `Scripts\`; elsewhere `bin/` | estimated from the build layout |
| Prereleases | `3.15.0a1`, `3.14.0b2`, `rc` are skipped for `latest` and prefixes | estimated |

## Tool and version as two words (`dev install java 21`)

| Rule | Value | Source |
|---|---|---|
| When | exactly **two** tool arguments, **neither** with `@version` | `FUN_144275920` (Ghidra): `if (param_3 != 2) return`; both version fields checked empty (`local_a08 == -1 && local_540 == -1`) |
| Second word must match | `^\d+(\.\d+)*$` (e.g. `21`, `21.0.5`; not `lts`, `latest`, `v1.22`, `zulu-17`) | regex built by `FUN_1452cd510` (Ghidra) from string `0x14c57d478` (13 bytes), initialised through `0x14a881e99`, called at `0x144275fcf` |
| Result | the first tool gets the second word as its version; the second is removed | `FUN_144275920`: version string copied into the first argument, then `memmove` drops the second |
| Source file | `src\args\tool_arg.rs` | panic locations `0x14c4f5fb0`, `0x14c4f5fe0` |

dev adds one thing the original doesn't have: a version left on its own (`dev install 21`,
`dev install node lts`) stops with "is a version, not a tool; join it to its tool with @" instead of
trying to install a tool called `21`.

## Java

| Rule | Value | Source |
|---|---|---|
| Version list | `https://mise-java.jdx.dev/jvm/{release_type}/{os}/{arch}.json` with release type `ga` | template `0x14b6fda80` (`/`, `/`, `.json` pieces), built at `0x143ec15fb` |
| `{os}` | `windows`, `linux`, `macosx` | checked against the service (macos → 404, macosx → 200); estimated |
| `{arch}` | `x86_64`, `aarch64` (arm64 is sent as `aarch64`) | `0x143ec158a`–`0x143ec15ad` |
| Vendor for a bare version (`java@21`) | `temurin` | settings schema `java.shorthand_vendor` default |
| Other vendors | `java@zulu-21`, `java@corretto-17`, ... (`<vendor>-<version>`) | same metadata (`vendor` field) |
| JAVA_HOME | the active JDK's install folder (macOS bundles: `Contents/Home`) | `0x143ef69f3` (`JAVA_HOME`, value from the install path); `Contents/Home` estimated |
| Download check | sha256 from the metadata `checksum` field | metadata |
| Entries used | `image_type = jdk`, no `features` (no JavaFX/musl/...), zip on Windows, tar.gz elsewhere, HotSpot before OpenJ9 | estimated |

### Decompiled with Ghidra (native_function_pseudocode)

| Function | What it is | What it settles |
|---|---|---|
| `FUN_143fe8650` | create a command for a program (empty argument list, fresh environment changes) | every installer above starts from it: cargo-binstall at `0x14589f54c`, npm at `0x143757b9a`, java tools at `0x143ef698a` |
| `FUN_149ad3ec0` | append one argument to a command | `install`, `-g`, `--prefix`, `--locked`, `--root`, `-y`, `--disable-strategies` are separate arguments, in the order they're pushed |
| `FUN_149ad5880` | set one environment variable on a command (also notes when the name is `PATH`) | `JAVA_HOME` (`0x143ef6a03`), `UV_TOOL_DIR`/`UV_TOOL_BIN_DIR` (`0x14374012b`, `0x143740311`), `NPM_CONFIG_UPDATE_NOTIFIER=false` (`0x143759ad5`, value `false`, 5 bytes), `PIPX_DEFAULT_BACKEND=pip` (`0x1436844cb`, value 3 bytes) are environment variables of the child process, as dev sets them |
| `FUN_1430e5da0` | a tool version's install path (`src\toolset\tool_version.rs`): `<installs>\<tool>\<version>`, cached after the first lookup | `JAVA_HOME` = that path (passed straight into `FUN_149ad5880` at `0x143ef69f3`) |

Ghidra did not answer for the large async functions that call these (Java list `0x143ec1410`,
cargo install `0x14589e230`); each request waited 15 minutes and failed. Those were read as
disassembly with Rizin instead, at the addresses given in the tables.

### Shell hook (embedded scripts in the original)

| Rule | Value | Source |
|---|---|---|
| Original PATH saved on first activation only | `if [ -z "${__MISE_ORIG_PATH:-}" ]; then export __MISE_ORIG_PATH="$PATH"; fi` | bash script string `0x14b5e28c0`; pwsh `0x14c533af5` |
| Hook keeps the last command's exit status | `local previous_exit_status=$?` … `return $previous_exit_status` | `0x14b5e28c0` |
| `PROMPT_COMMAND` as array or string | array: prepend `_mise_hook_prompt_command`; string: `name;$PROMPT_COMMAND` | `0x14b5e28c0` |
| Hook runs once at activation | `_mise_hook --force` | `0x14b5e28c0` |
| Variables to undo are tracked in the shell | `__MISE_DIFF`, `__MISE_SESSION` | strings `0x14b5dfdc0` |

dev follows the first four (its PowerShell hook also keeps `$LASTEXITCODE`). Instead of the original's
encoded `__MISE_DIFF`, dev keeps `__DEV_VARS` (names it set) and `__DEV_SAVED_<NAME>` (the value
before), which is enough to drop `JAVA_HOME` or give the old one back on leaving a folder.

`dev use java@21` prints `JAVA_HOME=...`; shells with dev turned on (`dev activate`) set it at the
next prompt and drop it (or give the old value back) on leaving the folder. `dev use -g java@21` on
Windows also saves `JAVA_HOME` and java's `bin` in the user environment for cmd.exe and IDEs
(new in dev).

## Windows user environment (new in dev)

| Rule | Value | Source |
|---|---|---|
| When `dev use java` saves JAVA_HOME for all of Windows | `-g`, or when the file written is the global config (e.g. `dev use` run in the home folder) | person's report (round 4) |
| What is saved | `HKCU\Environment\JAVA_HOME` = install folder; `%JAVA_HOME%\bin` added once to the user `Path` (older dev Java folders removed) | estimated (new) |
| How | PowerShell's registry API with `DoNotExpandEnvironmentNames` (keeps `%VARS%`), written back as `ExpandString`, then `WM_SETTINGCHANGE` broadcast (`SendMessageTimeout`, `HWND_BROADCAST`, `SMTO_ABORTIFHUNG`, 5000 ms) | the removed `install.ps1`; tested by `user_environment_round_trip` |
| Why not from dev's own code | a build that wrote the registry and broadcast directly (winreg + windows-sys) was blocked by Smart App Control on this computer; the PowerShell route runs | observed 2026-11-06 |
| PowerShell location | `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe` first (PATH may not have it) | observed: "program not found" with a reduced PATH |
| Old install folder cleaned by `dev setup` | `%LOCALAPPDATA%\Programs\dev\bin` (removed from PATH, old dev.exe deleted) | the removed `install.ps1` |

## Updates and releases (new in dev)

| Rule | Value | Source |
|---|---|---|
| Update check interval | **86 400 s (24 h)** | `mov qword [rdx], 0x15180` at `0x145fbfafe` in the warning function `0x145fbfa10` (`src\cli\version.rs`) |
| Warning can be turned off | setting `disable_update_warning` (default false); dev also `DEV_DISABLE_UPDATE_WARNING`, `DEV_NO_UPDATE_CHECK` | settings schema; flag read at `0x145fbfac7` |
| Warning text | `<name> version {} available` then `To update, run <name> self-update` (warn level) | templates `0x14c5f4730`, `0x14c5f476d`, used at `0x145fbfbaf`, `0x145fbfcb0` |
| When self-update isn't allowed | the second line is replaced by "self-update is disabled for this install; update … the same way you installed it" | string `0x14b5f746f`; check `FUN_1434a3700` before the second line (`0x145fbfc2e`) |
| Self-update allowed? | `FUN_1434a3700` (Ghidra): a cached override (`MISE_SELF_UPDATE_AVAILABLE`, dev: `DEV_SELF_UPDATE_AVAILABLE`) wins; otherwise not allowed when `FUN_1434a3580` finds the program in a package manager's folder | Ghidra |
| Package manager folders | path with `\` → `/` (`FUN_1434a3010`, constants `0x14aa18d40`/`0x14aa18d50`), lower-cased, containing `{d}/packages/` or `{d}/links/` for `d` in `microsoft/winget`, `program files/winget`; checked on the path and the path with links resolved (`FUN_1434a3580`) | table `0x14b5fe340`, templates `0x14b5f7458`, `0x14b5f7465` |
| Minimum release age | releases younger than **24 h** are skipped unless a version is named; `self_update.minimum_release_age`, then `minimum_release_age`, else 24h; `--minimum-release-age` | help text of `self-update`; message "newer {} release {} ignored by minimum_release_age" `0x14c5f46d8` |
| Named version | `self-update VERSION` installs that release, skipping the age rule | help text of `self-update` |
| Confirmation | asks first; without `--yes` and no answer: "self-update cancelled; use --yes to update non-interactively" | string `0x14c5cff44` |
| Release source | `self_update.api_url` (default `https://api.github.com`) + `self_update.repository` | settings schema |
| Check timeout | 3 s | estimated |
| Release list (GitHub source) | `{api_url}/repos/{repo}/releases?per_page=100&page={n}`, page counted up from 1 (`page + 1` at `0x145fc1367`); dev stops at a short page, at most 10 pages (estimated); drafts and prereleases skipped | template `0x14c5f4878` (`{}` `/repos/` `{}` `/releases?per_page=100&page=` `{}`) |
| Release list (original's own source) | `https://mise.jdx.dev/releases.tsv` (33 bytes) when the original checks its own repository; not used by dev | `0x145fc0f34` |
| Choosing the release | rows "version`\t`unix-seconds"; keep those published **at or before now − minimum age**, strip leading `v`, sort, take the **highest version**; a row without a time is invalid (dev: a release without `published_at` is never eligible) | `FUN_1471f2430` (Ghidra): split on `\n`, drop `\r`, two fields or "invalid mise release index row", `time <= cutoff`, sort, last |
| Nothing old enough | "no mise release satisfies minimum release age; lower self_update.minimum_release_age or specify a version" (dev: "no dev release …") | string `0x14c709f8f`, used in `FUN_1471f2430` |
| Named version | `{api_url}/repos/{repo}/releases/tags/v{VERSION}` | GitHub API |
| Default repository | `jpsdm/dev` (the original's own default is `jdx/mise`) | person's request; settings schema `self_update.repository` |
| Asset for this computer | `dev-<windows|linux|macos>-<x86_64|aarch64>[.exe]`, checked against `SHA256SUMS.txt` | release workflow |
| Self-replace | running program renamed to `dev.exe.old` / `dev.old`, new one moved in | estimated |
| Release version | `Cargo.toml` version; if tag `vX.Y.Z` exists, `X.Y.(last+1)` | release workflow |

## Shells and installers (new in dev)

| Shell | Activation | Startup file the installer edits |
|---|---|---|
| bash | `eval "$(dev activate bash)"` → `PROMPT_COMMAND` hook | `~/.bashrc`, plus `~/.bash_profile` (macOS) or `~/.profile` |
| zsh | `eval "$(dev activate zsh)"` → `precmd` + `chpwd` hooks | `$ZDOTDIR/.zshrc` |
| fish | `dev activate fish \| source` → `fish_prompt` + `PWD` events | `~/.config/fish/conf.d/dev.fish` |
| PowerShell | `Invoke-Expression (dev activate pwsh \| Out-String)` → `prompt` | both profile files (7 and 5.1) on Windows, `~/.config/powershell` elsewhere |
| Git Bash (Windows) | as bash, with `/c/...` paths joined by `:` | `~/.bashrc` |

Lines go between `# >>> dev >>>` and `# <<< dev <<<`, so running the installer again replaces them
and `dev setup --uninstall` removes them. Windows: `dev-setup.exe` is dev itself; started under that
name it installs and waits for Enter. Linux/macOS: `install.sh` finds or builds the program and runs
`dev setup`.

## Config files

Looked for in the current directory and every parent, nearest wins:
`dev.local.toml` > `dev.toml` > `.dev.toml` > `.tool-versions`; then the global config.
(Original: `mise.local.toml`, `mise.toml`, `.mise.toml`, `.tool-versions`; help of `mise use`/`mise local`.)

Sections: `[tools]` (`node = "24"`, `"github:cli/cli" = "latest"`), `[env]` (strings, `_.path` list),
`[tasks.NAME]` (`run`, `description`, `depends`, `dir`, `env`; or `NAME = "cmd"`), `[settings]`.

## Version requests

* `latest` → newest stable remote version; `lts` (node) → newest LTS.
* A prefix such as `24` or `1.22` → newest version that starts with `prefix.` or equals it.
* `use` without a version requests `latest` but reuses the newest installed one (help of `mise use`).
* `use --pin` writes the resolved version; otherwise the request as written.
* `ls` marks requested versions that are not installed with `(missing)` (string at line 31009).

## Backends in this rebuild

| Tool | Where versions come from | Archive |
|---|---|---|
| `node` | `https://nodejs.org/dist/index.json` | `node-v<V>-win-x64.zip` |
| `go` | `https://go.dev/dl/?mode=json&include=all` | `go<V>.windows-amd64.zip` |
| `github:OWNER/REPO` | GitHub releases API | asset whose name has windows + x86_64/amd64/x64 |
| `npm:PKG` | `https://registry.npmjs.org/<pkg>` (Accept header below) | see "npm" below |
| `cargo:CRATE` | crates.io sparse index `https://index.crates.io/<prefix>/<crate>` (yanked skipped) — estimated | see "cargo" below |
| `pypi:PKG` (old name `pipx:PKG`) | `https://pypi.org/pypi/{}/json` (releases with files) | see "pypi" below |
| anything | `dev link tool@ver DIR` | |

### Package backends, read from the original's code

Ghidra timed out on these functions (each is a very large async state machine; every request waited
15 minutes and failed), so they were read as disassembly with Rizin (`pD`) at the addresses given.
Strings named by address are in `.rdata`. Short arguments are stored inside the instructions
(`mov rcx, '--locked'`), which is how they were found.

**npm** (`src\backend\npm.rs`)

| Rule | Value | Source |
|---|---|---|
| Default registry | `https://registry.npmjs.org` (trailing `/` trimmed before comparing) | `0x142d6d990`–`0x142d6da64` |
| Metadata Accept header | `application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*` | string `0x14aa6a83e` |
| Install command (npm package manager) | `npm install -g <pkg>@<ver> --prefix <install>` | `0x14375978a` (`install`), `0x143759831` (`-g`), `0x1437598b6` (spec), `0x1437598f6` (`--prefix`), `0x143759998` (path) |
| Environment | `NPM_CONFIG_UPDATE_NOTIFIER=false` | `0x143759ac1` |
| Default package manager | `auto` (built-in `aube` client; `npm`, `bun`, `pnpm` selectable) | settings schema `npm.package_manager` |

dev always uses the npm CLI (the original's built-in client is out of scope). Programs live in
`<install>` (npm's Windows `.cmd` launchers).

**cargo** (`src\backend\cargo.rs`, install function around `0x14589e230`)

| Rule | Value | Source |
|---|---|---|
| Try cargo-binstall first | when installed and `cargo.binstall` = true (default) | `0x14589f54c` (`cargo-binstall`), schema default `true` |
| binstall arguments | `cargo-binstall -y [--disable-strategies quick-install] <crate>@<ver>` + the shared options below | `-y` at `0x14589f716`; `--disable-strategies` `0x14589f8c6`; `quick-install` chosen at `0x14589f7bd` when `binstall_quickinstall` is false; spec template `{}@{}` at `0x14c5afb98` used at `0x14589efd4` |
| binstall environment | `GITHUB_TOKEN` passed through | `0x14589f960` |
| "no prebuilt binary" exit code | **94** → warn and fall back to `cargo install` | `cmp edx, 0x5e` at `0x14589fbef`, message `0x14c5afd98` |
| Shared options (both commands) | `--bin=<b>`, `--locked` (option `locked`, default true), `--features=<f>`, `--no-default-features`, `--registry=<r>`, then `--root <install>` | helper `0x1458a4370`: `--locked` at `0x1458a469b`, templates at `0x14c5aff68`–`0x14c5aff94`, `--root` at `0x1458a4c36` |
| Rust missing | "To use cargo packages with mise, you need to install Rust first: … Or install Rust via https://rustup.rs/" | `0x14589ed9a` (string `0x14c5afb00`) |
| Git versions | `rev:`, `branch:`, `tag:` → `--git= --rev= --branch= --tag=` | strings dump lines 19000–19006 (not built in dev) |
| crates.io per-version URL | `https://crates.io/api/v1/crates/{}/{}` | `0x145914667` |

dev runs `cargo install <crate>@<ver> --locked --root <install>`; the order of `install` and the
spec in the cargo command is estimated (the helper only showed the options). Programs live in
`<install>\bin`. `DEV_CARGO_BINSTALL=0` and `DEV_CARGO_BINSTALL_QUICKINSTALL=1` stand in for the
original's two settings.

**pypi** (`src\backend\pipx`)

| Rule | Value | Source |
|---|---|---|
| Default version list | `https://pypi.org/pypi/{}/json` when `pypi.registry_url` is unset | `0x144db9319`–`0x144db9344` |
| Installer | uv when available, pipx otherwise (`pypi.uvx`) | settings schema |
| uv environment | `UV_TOOL_DIR=<install>`, `UV_TOOL_BIN_DIR=<install>\bin`, `UV_INDEX` when a registry is set | `0x14374011b`, `0x1437402ad`+`0x143740301`, `0x1437403aa` |
| pipx environment | `PIPX_DEFAULT_BACKEND=pip`, `PIP_INDEX_URL` when a registry is set, `PIPX_HOME`, `PIPX_BIN_DIR` | `0x1436844b7`, `0x143684538`; names at `0x14b6bc568` |
| uv / pipx command words | `uv tool install pkg==V`, `pipx install pkg==V` | estimated |

`PIPX_HOME=<install>`, `PIPX_BIN_DIR=<install>\bin` are estimated by analogy with uv.

Downloads use the system `curl.exe`, extraction the system `tar.exe` (estimated approach; the original
bundles its own HTTP client and unpacker).

## Tasks

`dev run TASK [ARGS]` runs `depends` first (once each), then the task's `run` commands through
`cmd /C` on Windows, with the tools' bin folders first on PATH and `[env]` applied. Task failure
exits with the task's code. With no task, runs `default` (help of `mise run`).

## Workspace (new in dev — not in the original)

| Rule | Value | Source |
|---|---|---|
| Workspace root | `%USERPROFILE%\dev-workspace` | estimated (new feature) |
| Layout | `projects\`, `scratch\`, `repos\<host>\<owner>\<repo>` | estimated |
| Registry | `<root>\.dev-workspace.toml` | estimated |
| Auto-clean | on by default, can be turned off | person's request |
| Inactive after | 30 days | estimated |
| Check interval | 24 hours | estimated |
| What is cleaned | only files ignored by the repo's `.gitignore` (dependencies, build output) | person's request |
| Never cleaned | pinned entries, `.env*`, `*.local.*`, `.idea`, `.vscode` | estimated (safety) |
| Activity | newest of: last `dev ws` use, newest non-ignored file, `.git` index/HEAD | estimated |
| Import sources | `https://…`, `git@…`, `github:o/r`, `gitlab:g/…/r`, `gh:`, `gl:`; `--all` imports a user's/group's repos | person's request |
| Tokens | `GITHUB_TOKEN`, `GITLAB_TOKEN` | estimated |

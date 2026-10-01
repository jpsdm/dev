# Security Policy

## Why this matters for `dev`

`dev` downloads and extracts archives from external services (nodejs.org,
Eclipse Temurin/Adoptium, go.dev/dl) and updates itself from GitHub
Releases. The security properties that matter most here are:

- **Checksum verification before any download is used** — every archive is
  verified against a checksum before extraction (`internal/downloader`).
- **Path-safety validation** on every externally-sourced string that
  becomes part of a filesystem path (`internal/runtime.ValidVersionName`).
- **Zip-slip-safe, atomic archive extraction** — extraction never trusts
  an archive entry's path without validating it stays inside the
  destination directory (`internal/installer.ExtractAtomic`).
- **Correct shell quoting of every value that reaches an `eval`.** `dev`
  manages `PATH` by generating shell code that the `dev` shell function
  installed in your rc file deliberately evaluates — `eval "$(command dev
  env)"` under POSIX shells, `eval (command dev env | string collect)`
  under Fish, `Invoke-Expression` under PowerShell. That is a real eval
  sink: every value `internal/shell` interpolates into that output
  (`DEV_HOME`, and every single `PATH` entry) would be executed as shell
  code if it weren't quoted correctly. `internal/shell`'s `shellQuote`
  (POSIX), `fishQuote` (Fish — whose single-quote escaping rules differ
  from POSIX's) and `escapePowerShellDoubleQuoted` are the enforcement
  point, and nothing may be interpolated into generated shell code
  without going through one of them. There is no shim dispatch anymore —
  that mechanism, and the "no shell interpolation at all" property it
  used to give this project, were removed by the shell-function `PATH`
  redesign.

If you find a way to defeat any of these — a crafted API response that
escapes a destination directory, a way to have an unverified or
mismatched-checksum archive get extracted, a value that survives
`internal/shell`'s quoting and executes when the generated lines are
evaluated, or a way to get the self-updater to run something other than
what it claims to — that's a security report, not a regular bug report.

## Supported Versions

`dev` is a small, actively developed project with a single maintainer.
Only the latest release receives security fixes.

| Version | Supported |
| ------- | --------- |
| Latest release | ✅ |
| Anything older | ❌ |

## Reporting a Vulnerability

**Please do not open a public GitHub issue for a security vulnerability.**

Preferred: use GitHub's private vulnerability reporting for this
repository — **Security** tab → **Report a vulnerability**
(`https://github.com/jpsdm/dev/security/advisories/new`).

If that's unavailable for any reason, email **dev.jpsdm@gmail.com** with:

- A description of the vulnerability and its impact
- Steps to reproduce (the exact `dev` commands, platform, and version)
- Any proof-of-concept you're comfortable sharing

### What to expect

This is a solo-maintained project — there's no formal SLA, but security
reports are prioritized over regular issues and get a response as soon as
possible. You'll get an acknowledgment, an assessment of the report, and
(if confirmed) a timeline for a fix, coordinated with you before any
public disclosure.

Please don't publicly disclose a vulnerability before a fix is released
and you've heard back from the maintainer about coordinated disclosure
timing.

## Scope

In scope: `dev` itself (this repository) — its providers
(`internal/runtime/*`), the self-updater (`internal/update`), archive
extraction and download verification, the shell code `internal/shell`
generates for `dev env`/`dev setup` (see the quoting point above), and
the install/setup flow.

Out of scope: vulnerabilities in Node.js, the JDK, the Go toolchain, or
any other language runtime `dev` installs — report those to the
respective upstream project instead.

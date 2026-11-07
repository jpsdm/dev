# dev

**dev** gerencia as versões das suas ferramentas de desenvolvimento (Node, Go, Java, programas do
GitHub, pacotes npm/cargo/pip), as variáveis de ambiente e as tarefas de cada pasta, e organiza um
**workspace** com projetos, pastas de rascunho (scratch) e repositórios importados do GitHub e do
GitLab, com limpeza automática (que pode ser desligada) dos repositórios parados há muito tempo.

Funciona no **Windows, Linux e macOS**, nos terminais **PowerShell, cmd, Git Bash, bash, zsh e fish**.

```sh
dev use node@lts        # instala o Node LTS e grava em dev.toml
dev use java 21         # Java 21 (Temurin); JAVA_HOME passa a apontar para ele
dev run test            # roda a tarefa "test" com as ferramentas certas no PATH
dev workspace import github:usuario/projeto
```

> Repositório oficial: **https://github.com/jpsdm/dev** — releases, instaladores e issues ficam lá.
> O `dev` procura atualizações nele por padrão (mude com `self_update.repository` ou `DEV_REPO`).

---

## Sumário

- [Instalação](#instalação)
- [Primeiros passos](#primeiros-passos)
- [Como funciona](#como-funciona)
- [Comandos](#comandos)
  - [Ferramentas](#ferramentas): `use`, `install`, `ls`, `ls-remote`, `latest`, `current`, `where`, `which`, `uninstall`, `unuse`, `upgrade`, `outdated`, `prune`, `link`
  - [Execução e ambiente](#execução-e-ambiente): `exec`, `run`, `tasks`, `env`, `set`, `unset`, `bin-paths`
  - [Terminal e instalação do dev](#terminal-e-instalação-do-dev): `activate`, `setup`, `self-update`, `doctor`, `version`
  - [Configuração](#configuração-de-arquivos): `edit`, `config`, `settings`, `cache`
  - [Workspace](#workspace): `workspace init|status|ls|new|scratch|promote|import|sync|path|rm|pin|unpin|clean|auto-clean`
- [Ferramentas suportadas](#ferramentas-suportadas)
- [Java e JAVA_HOME](#java-e-java_home)
- [Arquivos de configuração](#arquivos-de-configuração)
- [Variáveis de ambiente](#variáveis-de-ambiente)
- [Pastas usadas](#pastas-usadas)
- [Atualizações](#atualizações)
- [Releases e CI/CD](#releases-e-cicd)
- [Problemas, bugs e sugestões](#problemas-bugs-e-sugestões)
- [Desenvolvimento](#desenvolvimento)

---

## Instalação

### Windows

1. Baixe **`dev-setup-windows-x86_64.exe`** na [última release](https://github.com/jpsdm/dev/releases/latest)
   (`dev-setup-windows-aarch64.exe` em PCs ARM).
2. Abra o arquivo. Não precisa de administrador: o dev é instalado em `%LOCALAPPDATA%\dev\bin`,
   essa pasta entra no PATH do seu usuário e o dev é ligado no PowerShell (7 e 5.1) e no Git Bash.
3. Abra um terminal novo e rode `dev doctor`.

> **Smart App Control / SmartScreen:** programas novos e sem assinatura digital podem ser bloqueados
> pelo Windows. Se acontecer, clique em "Mais informações → Executar assim mesmo", ou use o
> binário direto (abaixo). Assinar os arquivos na release resolve de vez (veja [Releases](#releases-e-cicd)).

### Linux e macOS

```sh
curl -fsSL https://github.com/jpsdm/dev/releases/latest/download/install.sh | sh
```

O script detecta o sistema e o processador, baixa o arquivo certo da release, confere o SHA-256,
coloca o `dev` em `~/.local/bin` e liga o dev no bash, zsh, fish e PowerShell que existirem.
Opções (variáveis antes do comando):

| Variável | Efeito |
|---|---|
| `DEV_VERSION=2026.11.4` | instala essa versão em vez da última |
| `DEV_INSTALL_DIR=/opt/bin` | outra pasta para o programa |
| `DEV_NO_MODIFY_PATH=1` | não mexe no PATH |
| `DEV_NO_SHELLS=1` | não mexe nos arquivos do terminal (`.bashrc`, `.zshrc`...) |
| `DEV_BINARY=/caminho/dev` | usa um programa que você já baixou |
| `DEV_FROM_SOURCE=1` | compila a partir do código (rodando o script dentro da pasta do projeto) |

Para remover: `sh install.sh --uninstall` (acrescente `--purge` para apagar também as ferramentas instaladas).

### Binário direto (sem instalador)

Cada release traz o programa puro para quem prefere não instalar nada:

| Sistema | Arquivo |
|---|---|
| Windows x64 / ARM | `dev-windows-x86_64.exe` / `dev-windows-aarch64.exe` |
| Linux x64 / ARM | `dev-linux-x86_64` / `dev-linux-aarch64` (também `.tar.gz`) |
| macOS Intel / Apple Silicon | `dev-macos-x86_64` / `dev-macos-aarch64` (também `.tar.gz`) |

Renomeie para `dev` (`dev.exe` no Windows), coloque numa pasta do PATH e, se quiser o terminal
configurado, rode `dev setup`. Confira os arquivos com `SHA256SUMS.txt`.

---

## Primeiros passos

```sh
dev use node@lts              # na pasta do projeto: instala e grava em ./dev.toml
dev use -g java@21            # para o usuário todo (config global)
dev ls                        # o que está instalado e o que cada pasta pede
dev exec -- node --version    # roda um comando com as ferramentas da pasta
dev edit                      # cria um dev.toml de exemplo com uma tarefa
dev run hello                 # roda a tarefa
dev workspace new meu-app -t node@lts
```

## Como funciona

- Cada pasta pode ter um **`dev.toml`** dizendo quais ferramentas e versões usar, quais variáveis de
  ambiente definir e quais tarefas existem. O dev procura esse arquivo na pasta atual e em todas as
  pastas acima; o mais próximo vale. Por último vem a configuração global (`~/.config/dev/config.toml`).
- As ferramentas ficam em `…/dev/installs/<ferramenta>/<versão>`; várias versões convivem.
- Com o dev **ligado no terminal** (`dev setup` faz isso), a cada prompt o dev ajusta o PATH e as
  variáveis (como `JAVA_HOME`) para a pasta em que você está, e desfaz ao sair dela, devolvendo o
  valor que você tinha antes.
- Sem o terminal ligado, use `dev exec`, `dev run` ou `dev env`.

---

## Comandos

Opções que valem para todos os comandos:

| Opção | Efeito |
|---|---|
| `-C, --cd <PASTA>` | age como se estivesse nessa pasta |
| `-q, --quiet` | mostra menos mensagens |
| `-y, --yes` | responde "sim" às perguntas |
| `--verbose` | mostra detalhes (também `DEV_VERBOSE=1`) |
| `-V, --version` | mostra a versão |

Códigos de saída: `0` deu certo, `1` erro, `2` uso errado das opções; `exec`/`run` devolvem o código do comando executado.

### Ferramentas

#### `dev use [FERRAMENTA@VERSÃO]...` (atalho `u`)
Instala (se faltar) e grava a ferramenta no arquivo de configuração.
`dev use node@24`, `dev use java 21` (ferramenta e versão separadas por espaço também valem quando
são exatamente duas palavras e a segunda é um número como `21` ou `21.0.5`).

| Opção | Efeito |
|---|---|
| `-g, --global` | grava na config global (`~/.config/dev/config.toml`) |
| `--pin` | grava a versão exata resolvida (`21.0.13+9.0.LTS`) em vez do pedido (`21`) |
| `-p, --path <ARQ>` | grava neste arquivo/pasta |
| `-e, --env <NOME>` | grava em `dev.NOME.toml` |
| `--rm <FERRAMENTA>` | remove a ferramenta do arquivo |

Pedidos de versão: `latest` (mais nova estável), `lts` (Node), um prefixo (`21` → a 21.x mais nova,
`1.22` → 1.22.x) ou a versão exata.

#### `dev install [FERRAMENTA@VERSÃO]...` (atalho `i`)
Sem argumentos instala tudo o que a configuração pede. `-f, --force` reinstala; `-n, --dry-run` só mostra.

#### `dev ls [FERRAMENTA]` (atalho `list`)
Lista versões instaladas, de onde vem o pedido e o que foi pedido. `-c, --current` só as pedidas;
`-m, --missing` só as que faltam instalar (marcadas `(missing)`); `-J, --json` em JSON.

#### `dev ls-remote <FERRAMENTA> [PREFIXO]`
Versões disponíveis para instalar. `dev ls-remote java 21`, `dev ls-remote java zulu-21`.

#### `dev latest <FERRAMENTA[@PREFIXO]>`
A versão mais nova que atende ao pedido. `-i, --installed` considera só as instaladas.

#### `dev current [FERRAMENTA]`
A versão ativa nesta pasta.

#### `dev where <FERRAMENTA[@VERSÃO]>`
Pasta de instalação. `dev which <PROGRAMA>` mostra qual executável seria usado (`--version`, `--plugin`).

#### `dev uninstall <FERRAMENTA@VERSÃO>...` (atalho `remove`)
Apaga versões instaladas. `-a, --all` apaga todas as versões da ferramenta.

#### `dev unuse <FERRAMENTA>...`
Tira a ferramenta do arquivo de configuração (`-g` na global). Não apaga a instalação.

#### `dev upgrade [FERRAMENTA]...` (atalho `up`) e `dev outdated [FERRAMENTA]...`
`outdated` mostra o que tem versão mais nova dentro do pedido; `upgrade` instala (`-n` só mostra).

#### `dev prune`
Apaga versões que nenhuma configuração conhecida usa (`-n` só mostra).

#### `dev link <FERRAMENTA@VERSÃO> <PASTA>` (atalho `ln`)
Registra uma instalação que já existe no computador. `dev link java@corp C:\jdks\jdk-21`. `-f` substitui.

### Execução e ambiente

#### `dev exec [FERRAMENTA@VERSÃO]... -- COMANDO` (atalho `x`)
Roda um comando com as ferramentas (da pasta + as que você passar) no PATH e as variáveis definidas.
Instala o que faltar. `-c "linha de comando"` roda pelo shell.

#### `dev run [TAREFA] [ARGS]...` (atalho `r`)
Roda uma tarefa de `[tasks]` e, antes, as dependências (`depends`), uma vez cada. Sem nome roda
`default`. `-n, --dry-run` mostra a ordem.

#### `dev tasks ls` / `dev tasks info <TAREFA>` (atalho `t`)
Lista as tarefas ou mostra uma.

#### `dev env [FERRAMENTA@VERSÃO]...` (atalho `e`)
Imprime as variáveis e o PATH para um shell: `-s pwsh|bash|zsh|fish|cmd`, ou `-J` em JSON.
Ex.: `dev env -s bash > env.sh`.

#### `dev set [CHAVE=VALOR]...` / `dev unset <CHAVE>...`
Grava/remove variáveis em `[env]` do `dev.toml` (`-g` global, `--file` outro arquivo). `dev set`
sem argumentos lista as variáveis.

#### `dev bin-paths`
As pastas de programas das ferramentas ativas.

### Terminal e instalação do dev

#### `dev activate [pwsh|bash|zsh|fish]`
Imprime o código que liga o dev no terminal (o `dev setup` já coloca isso nos arquivos de início):

```sh
eval "$(dev activate bash)"                          # ~/.bashrc
eval "$(dev activate zsh)"                           # ~/.zshrc
dev activate fish | source                           # ~/.config/fish/config.fish
Invoke-Expression (& dev activate pwsh | Out-String) # $PROFILE
```

O gancho roda a cada prompt (e ao trocar de pasta no zsh/fish), preserva o código de saída do
último comando e funciona com `PROMPT_COMMAND` em texto ou lista.

#### `dev setup` (atalho `self-install`)
Instala o dev neste computador: copia o programa, põe no PATH do usuário e configura os terminais
entre as marcas `# >>> dev >>>` e `# <<< dev <<<` (rodar de novo não duplica nada). No Windows
também remove uma cópia antiga em `%LOCALAPPDATA%\Programs\dev\bin`.

| Opção | Efeito |
|---|---|
| `--uninstall` | desfaz tudo (ferramentas e configurações ficam) |
| `--purge` | com `--uninstall`, apaga também ferramentas, cache e configurações |
| `--no-modify-path` / `--no-shells` | não mexe no PATH / nos arquivos do terminal |
| `--dir <PASTA>` | instala o programa em outra pasta |

O instalador do Windows (`dev-setup-*.exe`) é o próprio dev: aberto com esse nome, ele roda `dev setup`.

#### `dev self-update [VERSÃO]`
Baixa a release mais nova do GitHub para o seu sistema, confere o SHA-256 e substitui o programa.
Pede confirmação (use `-y` em scripts). Releases publicadas há menos de **24 h** são puladas por
segurança, a não ser que você diga a versão (`dev self-update 2026.11.5`).

| Opção | Efeito |
|---|---|
| `--check` | só avisa se existe versão nova |
| `--minimum-release-age <IDADE>` | outra idade mínima (`7d`, `0` para nenhuma) |
| `-f, --force` | reinstala mesmo se já estiver atualizado (ou se um gerenciador de pacotes instalou o dev) |

Se o dev foi instalado por um gerenciador de pacotes (pastas do winget), o self-update fica desligado:
atualize pelo mesmo gerenciador.

#### `dev doctor` (atalho `dr`)
Verifica a instalação: arquivos de configuração, ferramentas faltando, `git`/`curl`/`tar`, no Windows
o `JAVA_HOME` e o PATH salvos para o usuário, e o workspace. Sai com `1` se achar problemas.

#### `dev version` (atalho `v`)
`2026.11.3 windows-x64 (2026-11-06)`; `-J` em JSON.

### Configuração de arquivos

| Comando | Efeito |
|---|---|
| `dev edit [ARQ]` (atalho `init`) | cria um `dev.toml` de exemplo (`-f` substitui, `-n` só mostra) |
| `dev config ls` (atalho `cfg`) | arquivos de configuração que valem aqui e as ferramentas de cada um |
| `dev settings ls\|get\|set\|unset` | lê e grava `[settings]` na config global |
| `dev cache path\|clear` | mostra / apaga o cache (listas de versões, downloads) |

### Workspace

Uma pasta (padrão `~/dev-workspace`) com `projects/`, `scratch/` e `repos/<host>/<dono>/<repo>`.
Atalho: `dev ws`.

| Comando | O que faz |
|---|---|
| `init [PASTA]` | cria as pastas e lembra onde ficam |
| `status` | pasta, quantidade de itens e configuração da limpeza |
| `ls [-k projects\|scratch\|repos] [-J]` | lista tudo com a última atividade |
| `new <NOME> [-t node@24]... [--no-git]` | cria um projeto com `dev.toml`, `.gitignore` e `git init` |
| `scratch [NOME]` | cria uma pasta de rascunho com a data (`2026-11-06-teste`) e mostra o caminho |
| `promote <SCRATCH> [NOME]` | transforma um rascunho em projeto |
| `import <ORIGEM>` (atalho `clone`) | clona `github:dono/repo`, `gitlab:grupo/sub/repo`, `gh:`/`gl:`, URL `https://` ou `git@` |
| `sync [NOME]` (atalho `pull`) | busca e avança (fast-forward) os repositórios importados |
| `path <NOME>` | imprime a pasta (e conta como uso): `cd $(dev ws path app)` |
| `rm <NOME> [-f]` | apaga a pasta do item |
| `pin <NOME>` / `unpin <NOME>` | protege / libera o item da limpeza automática |
| `clean [NOME] [--older-than 30d] [--all] [-n]` | limpa agora |
| `auto-clean [on\|off] [--days N] [--interval H]` | liga/desliga e ajusta a limpeza automática |

Opções do `import`: `--name`, `-b/--branch`, `--depth N`, `--ssh`, `--host gitlab.minhaempresa.com`,
`--all` (todos os repositórios de um usuário/organização/grupo), `--include-forks`,
`--include-archived`, `-n/--dry-run`. Repositórios privados usam `GITHUB_TOKEN` / `GITLAB_TOKEN`.

**Limpeza automática:** ligada por padrão. No máximo uma vez a cada 24 h (ao rodar qualquer comando
do dev), os itens sem uso há 30 dias perdem **apenas os arquivos que o `.gitignore` ignora**
(dependências, resultados de compilação). Nunca apaga código, itens fixados (`pin`), nem `.env`,
`.env.*`, `*.local`, `*.local.*`, `.idea`, `.vscode`. Desligue com `dev ws auto-clean off` ou
`DEV_WORKSPACE_AUTO_CLEAN=0`.

---

## Ferramentas suportadas

| Nome | De onde vem |
|---|---|
| `node` | nodejs.org (`lts` aceito) |
| `go` | go.dev |
| `java` | JDKs dos fornecedores (Temurin por padrão; `zulu-`, `corretto-`, `oracle-`, `microsoft-`, `graalvm-`...) |
| `python` | CPython pré-compilado (python-build-standalone), com `pip` |
| `github:dono/repo` | releases do GitHub (atalhos: `gh`, `ripgrep`/`rg`, `fd`, `bat`, `jq`, `just`, `deno`, `bun`, `uv`, `zig`, `fzf`, `yq`, `hugo`, `lazygit`, `starship`, `watchexec`, `terraform-ls`) |
| `npm:pacote` | npm (precisa do Node: `dev use -g node@lts`) |
| `cargo:crate` | crates.io (usa cargo-binstall se houver, senão `cargo install`; precisa do Rust) |
| `pypi:pacote` (ou `pipx:`) | PyPI, instalado com uv (ou pipx) |
| qualquer outra | registre uma instalação existente com `dev link` |

## Java e JAVA_HOME

```sh
dev use java 21            # Temurin 21 mais nova nesta pasta
dev use java@zulu-17       # outro fornecedor
dev use -g java@21         # para tudo
dev ls-remote java 21      # versões disponíveis
```

- `JAVA_HOME` aponta para a pasta da versão ativa (no macOS, `Contents/Home`).
- Em terminais com o dev ligado, muda sozinho ao entrar/sair de uma pasta.
- **Windows:** quando o Java é o global (`-g`, ou `dev use` rodado onde a config global é a usada,
  como na sua pasta de usuário), o dev grava `JAVA_HOME` nas **variáveis de ambiente do usuário** e
  coloca `%JAVA_HOME%\bin` no PATH do usuário. O cmd, o Explorer e as IDEs abertos depois já veem.
  Confira com `dev doctor`.
- O download é conferido pelo SHA-256 informado pelo fornecedor.

## Python

```sh
dev use python 3.12        # Python 3.12 mais novo nesta pasta
dev use -g python@3.13     # para tudo
dev ls-remote python 3.13  # versões disponíveis
dev exec -- python -m venv .venv
```

- Baixa o CPython pronto do projeto python-build-standalone (o mesmo que o uv usa), conferido pelo
  SHA-256 da release. Nada é compilado; funciona no Windows (x64/ARM), Linux e macOS.
- Vêm `python`, `python3` e `pip` (no Windows também `pip.cmd`/`pip3.cmd` quando a build não traz
  `Scripts\pip.exe`). Programas instalados com `pip install` ficam no PATH (`Scripts\` no Windows, `bin/` no resto).
- Versões em teste (`3.15.0a1`, `rc`) só são usadas se você pedir a versão exata.
- Opções: `DEV_PYTHON_PRECOMPILED_FLAVOR` (ex. `freethreaded-install_only_stripped` para o Python
  sem GIL), `DEV_PYTHON_PRECOMPILED_ARCH`, `DEV_PYTHON_PRECOMPILED_OS` (ex. `unknown-linux-musl`).

## Arquivos de configuração

Procurados na pasta atual e acima (o mais próximo vence): `dev.local.toml` › `dev.toml` › `.dev.toml`
› `.tool-versions`; depois a global `~/.config/dev/config.toml`.

```toml
[tools]
node = "lts"
java = "21"
"github:cli/cli" = "latest"
"npm:prettier" = "3"

[env]
DATABASE_URL = "postgres://localhost/app"
_.path = ["./node_modules/.bin", "{{config_root}}/scripts"]

[tasks.build]
description = "Compila"
run = "npm run build"

[tasks.test]
depends = ["build"]
run = ["npm test", "echo ok"]
dir = "{{config_root}}"
env = { NODE_ENV = "test" }

[settings]
# chaves livres, lidas com `dev settings get`
```

Workspace (na config global):

```toml
[workspace]
root = "~/dev-workspace"
auto_clean = true
inactive_days = 30
check_interval_hours = 24
keep = [".env", ".env.*", "*.local.*", "*.local", ".idea", ".vscode"]
```

## Variáveis de ambiente

| Variável | Efeito |
|---|---|
| `DEV_DATA_DIR` | pasta de dados (instalações, cache, estado) |
| `DEV_CONFIG_DIR` | pasta da configuração global |
| `DEV_CACHE_DIR` / `DEV_STATE_DIR` | cache / estado em outro lugar |
| `DEV_INSTALL_DIR` | onde `dev setup` coloca o programa |
| `DEV_VERBOSE=1` | mensagens detalhadas |
| `DEV_YES=1` | responde "sim" às perguntas |
| `DEV_WORKSPACE` | pasta do workspace |
| `DEV_WORKSPACE_AUTO_CLEAN=0\|1` | desliga/liga a limpeza automática |
| `DEV_JAVA_SHORTHAND_VENDOR` | fornecedor para `java@21` (padrão `temurin`) |
| `DEV_PIPX_UVX=0` | instala `pypi:` com pipx em vez de uv |
| `DEV_PYTHON_PRECOMPILED_FLAVOR` / `_ARCH` / `_OS` | qual build do Python baixar |
| `DEV_CARGO_BINSTALL=0` | não usa cargo-binstall |
| `DEV_CARGO_BINSTALL_QUICKINSTALL=1` | deixa o cargo-binstall usar o cargo-quickinstall |
| `DEV_NO_MODIFY_PATH=1` / `DEV_NO_SHELLS=1` | `dev setup` não mexe no PATH / nos terminais |
| `DEV_REPO=dono/repo` | de onde vêm as atualizações e o `install.sh` baixa o dev (padrão `jpsdm/dev`) |
| `DEV_NO_UPDATE_CHECK=1` / `DEV_DISABLE_UPDATE_WARNING=1` | desliga o aviso de versão nova |
| `DEV_MINIMUM_RELEASE_AGE=7d` | idade mínima das releases para o self-update (padrão 24h) |
| `DEV_SELF_UPDATE_AVAILABLE=0\|1` | força desligar/ligar o self-update |
| `GITHUB_TOKEN` / `GITLAB_TOKEN` | repositórios privados e mais limite na API |
| `XDG_CONFIG_HOME` / `XDG_DATA_HOME` | respeitadas no Linux/macOS |

Variáveis que o dev define: `PATH`, `JAVA_HOME`, `GOROOT`, as de `[env]`, e internas
(`__DEV_ORIG_PATH`, `__DEV_VARS`, `__DEV_SAVED_*`) usadas para desfazer ao sair da pasta.

## Pastas usadas

| | Windows | Linux / macOS |
|---|---|---|
| Programa | `%LOCALAPPDATA%\dev\bin\dev.exe` | `~/.local/bin/dev` |
| Ferramentas | `%LOCALAPPDATA%\dev\installs` | `~/.local/share/dev/installs` |
| Cache / estado | `%LOCALAPPDATA%\dev\cache`, `…\state` | `~/.local/share/dev/cache`, `…/state` |
| Config global | `%USERPROFILE%\.config\dev\config.toml` | `~/.config/dev/config.toml` |
| Workspace | `%USERPROFILE%\dev-workspace` | `~/dev-workspace` |

## Atualizações

Uma vez por dia, ao rodar um comando, o dev consulta a última release (no máximo 3 s, sem atrapalhar
se estiver offline) e avisa:

```
dev WARN  dev version 2026.11.5 available
dev WARN  To update, run dev self-update
```

Só conta releases com pelo menos 24 h. O aviso não aparece em scripts/CI (saída não é terminal ou
`CI` definido). Para desligar: `dev settings set disable_update_warning true` ou
`DEV_DISABLE_UPDATE_WARNING=1`.

Configurações (em `[settings]` da config global):

```toml
[settings]
disable_update_warning = false
self_update.repository = "jpsdm/dev"         # de onde vêm as releases
self_update.api_url = "https://api.github.com" # GitHub Enterprise: https://ghe.example.com/api/v3
self_update.minimum_release_age = "24h"
```

## Releases e CI/CD

O repositório tem dois workflows em `.github/workflows/`:

- **`ci.yml`** — em todo pull request e push em outra branch: compila e testa no Windows, Linux e
  macOS, testa a ativação dos terminais e confere o `install.sh` (sh, bash, dash, shellcheck).
- **`release.yml`** — em todo **merge/push na `main`**: roda os testes, escolhe a versão, compila para
  Windows (x64, ARM), Linux (x64, ARM, estático/musl) e macOS (Intel, Apple Silicon) e publica uma
  **GitHub Release** com:
  - `dev-setup-windows-*.exe` (instalador), `dev-windows-*.exe` (binário)
  - `install.sh` (instalador Linux/macOS, já apontando para o repositório)
  - `dev-linux-*`, `dev-macos-*` e os `.tar.gz`
  - `SHA256SUMS.txt`

**Versão:** a do `Cargo.toml`. Se a tag `vX.Y.Z` já existir, o último número sobe sozinho (cada merge
vira uma release). Para começar uma série nova, mude a versão no `Cargo.toml`.

Requisitos no GitHub: Actions habilitado e permissão de escrita (o workflow já pede
`contents: write`). O runner `ubuntu-24.04-arm` é gratuito em repositórios públicos; em privados,
remova a linha `linux-aarch64` do workflow ou use um plano que o tenha.

**Assinatura (recomendado):** para o Windows não bloquear o instalador (SmartScreen / Smart App
Control), assine os `.exe` na release (por exemplo com Azure Trusted Signing) e, no macOS, assine e
notarize os binários.

## Problemas, bugs e sugestões

Abra uma [issue](https://github.com/jpsdm/dev/issues/new/choose) — há modelos prontos:

- **Bug:** inclua `dev version`, sistema, terminal, o comando exato, o que esperava, o que aconteceu
  (rode de novo com `--verbose`) e a saída de `dev doctor`. Tire tokens e senhas antes de colar.
- **Sugestão:** descreva o problema que quer resolver e o comando que imagina.

Antes de abrir: veja se já existe uma issue parecida, rode `dev doctor` e tente `dev self-update`.

## Desenvolvimento

```sh
cargo build                 # compila
cargo test                  # testes
cargo run -- use node@lts   # roda sem instalar
sh install.sh               # instala a partir do código (DEV_FROM_SOURCE=1)
```

- `src/main.rs` comandos · `src/tools.rs` ferramentas e downloads · `src/runtime.rs` ambiente e
  execução · `src/config.rs` arquivos `dev.toml` · `src/workspace.rs` workspace · `src/setup.rs`
  instalação e terminais · `src/update.rs` atualizações
- `scripts/build-installer.ps1` gera `dist\dev-setup.exe` localmente; `scripts/build-release.sh` gera o
  `.tar.gz` no Linux/macOS.
- `MECHANICS.md` / `mechanics.json`: regras e valores estudados na ferramenta de referência.

Pull requests são bem-vindos: abra contra a `main`; o CI precisa passar. Ao fazer o merge, uma release
nova é publicada automaticamente.

[English](README.md) | **日本語**

# jind-ai

**10 のエージェントセッションを、1 つの画面で。**

- 自分待ちのエージェントを逃さない
- セッションごとに独立した git worktree
- マシンを再起動しても続きから
- エージェントがエージェントを操作する

tmux の上で動くので、既存の設定はそのまま。SSH 越しでも同じ画面に戻れます。

![8 つのエージェントセッションが 1 つの一覧に並ぶ。カーソルを下へ動かすと下のペインが追随して各セッションを説明し、切り替えは起きない。許可待ちのセッションには印が付き、その許可ダイアログが右に開いている。](https://github.com/takaaki-s/jind-ai/releases/download/v0.9.0/demo.gif)

## インストール

### GitHub Releases からダウンロード

[Releases ページ](https://github.com/takaaki-s/jind-ai/releases)からお使いの OS/アーキテクチャに合ったバイナリをダウンロードしてください。

```bash
# 例: Linux amd64
curl -Lo jind-ai.tar.gz https://github.com/takaaki-s/jind-ai/releases/latest/download/jind-ai_0.7.0_linux_amd64.tar.gz
tar xzf jind-ai.tar.gz
sudo mv jin /usr/local/bin/
```

### Go install

```bash
go install github.com/takaaki-s/jind-ai/cmd/jin@latest
```

### ソースからビルド

```bash
git clone https://github.com/takaaki-s/jind-ai.git
cd jind-ai
make build    # bin/jin にビルド
make install  # $GOPATH/bin にインストール
```

## できること

**どれが自分待ちか分かる。** 状態はエージェント自身が報告します（thinking / idle /
許可待ち）。画面を読んで推測しているのではありません。

**エージェント同士がぶつからない。** `--worktree` を付けると、セッション起動と同時に
git worktree とブランチを切ります。並列で走らせても作業ツリーを共有しません。

**いつでも戻れる。** セッションは daemon より、エージェントより、マシンの再起動より
長生きします。開き直せば会話は止まったところから続きます。

**移動が速い。** `Ctrl+]` でデタッチ。`M-f` でセッション名・ディレクトリ・ブランチ・
フリート・エージェント種別を横断するファジー検索が開きます。

**エージェントを混ぜられる。** Claude Code / Codex / opencode をセッション単位で
選び、同じ画面に並べられます。

**スクリプトから、あるいは別のエージェントから動かせる。** 作成・送信・待機・結果の
読み出しが、いずれも `--json` で行えます。

**拡張できる。** 状態変化やオンデマンドで任意のプラグインを実行できます。デスクトップ
通知は今すぐ入れられるものの一例です。

ロジックは全て daemon 側にあり、TUI は Unix socket 越しの薄いクライアントです。
別のフロントエンドから同じ IPC を叩けます（[アーキテクチャ](docs/architecture.md) /
[IPC プロトコル](docs/ipc-protocol.md)）。

それぞれの具体的な使い方は、下の「CLI コマンド」「設定」「TUI キーバインド」
「プラグイン」の各セクションを参照してください。

## プロジェクトの状態

jind-ai は個人プロジェクトです。開発者が自身の日常利用のために開発・メンテナンス
しており、他の方にも役立つことを期待して公開しています。以下を前提としてご利用
ください。

- **サポートの保証はありません。** Issue / Pull Request に返信できないことがあります。
- **1.0 未満です。** 設定・IPC・plugin manifest に対する破壊的変更が任意の
  リリースで入り得ます。
- **スコープは意図的に狭く保っています。** Issue / PR を作成する前に
  [CONTRIBUTING.ja.md](CONTRIBUTING.ja.md) をご覧ください。

再現手順つきのバグ報告が最も歓迎される貢献です。

## 対応エージェント

| Kind | CLI | 備考 |
|---|---|---|
| `claude` (デフォルト) | [Claude Code](https://claude.com/product/claude-code) 2.x | first-class サポート。`--session-id` / `--resume` と CC のネイティブ hook で状態追跡。 |
| `codex` | [OpenAI Codex CLI](https://github.com/openai/codex) 0.144+ | spawn ごとに `-c hooks.X=[...]` で hook を注入。初回のみ `/hooks` ダイアログでトラスト承認が必要 (詳細: [docs/gotchas.md](docs/gotchas.md#codex-adapter))。Codex には `--session-id` 相当がないため、session UUID は `SessionStart` hook で受け取って daemon 側に書き戻す。 |
| `opencode` | [opencode](https://github.com/sst/opencode) 1.17+ | **Experimental。** 状態通知は `jin` バイナリに埋め込んだ TypeScript plugin が担当する。plugin は jind-ai の state 配下に展開し、`OPENCODE_CONFIG_DIR` で opencode に読ませる。この env は検索パスへの**加算**なので `~/.config/opencode` は汚さない (詳細: [docs/gotchas.md](docs/gotchas.md#opencode-adapter))。外部の bun インストールは不要。opencode にも `--session-id` 相当がないため、resume 用 ID は plugin の `SessionStart` で受け取る。opencode 自身の会話は SQLite に入っているため、`jin session result` は都度 `opencode export --pure <session id>` を実行してその出力を読む。jind-ai 側には何も記録しないが、その代わり `opencode` が daemon の PATH 上にある必要がある。 |

Claude Code を first-class citizen としてサポートしています。他エージェントは
`internal/agent/<kind>/` にアダプタを追加することで拡張可能です。

セッションごとに adapter を選ぶ:

```bash
jin session new --agent codex --workdir ~/repos/myrepo
```

`~/.config/jind-ai/config.yaml` に `default_agent: codex` を書けば常時デフォルトを切り替えられる。

TUI の作成フォームには、adapter が2つ以上登録されている場合に **agent picker のステップ**が出る。↑↓ / j/k + Enter でセッションごとに選べる。初期選択は `--agent` > `default_agent` > `"claude"` の順で決まる。`jin ui --agent codex` を使えば、その TUI 起動中だけ Codex を初期選択にできる（config は書き換えない）。

```bash
jin ui --agent codex   # 一時的なデフォルト。TUI 終了で戻る
```

## クイックスタート

### 1. デーモンを起動

```bash
jin daemon start
```

### 2. TUI を起動

```bash
jin ui
```

### 3. セッションを作成・アタッチ

TUI 内で `n` キーを押してセッション作成、`Enter` でアタッチ。

`Ctrl+]` でデタッチして TUI に戻ります。

## セッションステータス

セッションの状態は Claude Code の [hooks](https://docs.anthropic.com/en/docs/claude-code/hooks) によりイベントドリブンで検知されます。

| ステータス | アイコン | 検知方法 | 説明 |
|-----------|---------|---------|------|
| `thinking` | ⚡ | `UserPromptSubmit` hook | 処理中 |
| `permission` | ? | `Notification` hook | 許可待ち |
| `running` | ▶ | 内部設定 | 実行中 |
| `creating` | + | 内部設定 | 作成中（CC起動中） |
| `idle` | ○ | `Stop` hook / 30秒間 hook が来なければ fallback | 入力待ち |
| `stopped` | ■ | プロセス死亡検知 | 停止済み |

## CLI コマンド

### デーモン管理

```bash
jin daemon start   # デーモン起動
jin daemon stop    # デーモン停止
jin daemon status  # 状態確認
```

### セッション管理

```bash
# セッション作成（TUI で対話的に作成 - 推奨）
jin session new

# セッション作成（作業ディレクトリ指定）
jin session new --workdir ~/repos/myrepo

# セッション一覧
jin session list

# JSON形式で出力（スクリプト / LLM連携用）
jin session list --json

# セッションにアタッチ
jin session attach <session-name>

# セッションの詳細情報を取得
jin session info <session-name>

# セッションにプロンプトを送信（idle のセッションのみ）
jin session send <session-name> "プロンプト"

# プロンプト待ちで止まったセッションに回答する（Claude Code セッション）
jin session respond <session-name> --option 1
jin session respond <session-name> --text "bun を使って"

# セッションが idle になるまで待機（デフォルトタイムアウト: 300秒）
jin session wait <session-name>
jin session wait <session-name> --timeout 600

# 最後のアシスタントメッセージを取得
jin session output <session-name>

# 直近 N 往復の会話を取得
jin session output <session-name> --last 3

# セッション終了
jin session kill <session-name>

# セッション削除
jin session delete <session-name>

# 停止済みセッションの一括削除
jin cleanup stopped
jin cleanup stopped --dry-run   # 削除対象の確認
```

> **エイリアス**: `session` は `sess` でも可（例: `jin sess list`）。`list` は `ls`、`delete` は `rm` でも可。

### LLM API（スクリプト / 自動化）

以下のコマンドは `--json` フラグに対応しており、スクリプトや他の LLM エージェントとの連携が可能です。

```bash
# 全セッションコマンドが --json に対応
jin session list --json
jin session new --workdir ~/repos/myrepo --json
jin session info <session-name> --json
jin session kill <session-name> --json

# プロンプト送信 → 完了待機 → 出力取得
jin session send <session-name> "テストを修正して" --wait-running --json
jin session wait <session-name> --timeout 120 --json
jin session output <session-name> --json

# パイプライン例: プロンプト送信 → 待機 → 出力取得
# --wait-running はエージェントがターンを開始したことまで確認します。付けない場合、
# send は「入力欄に届いた」ところまでしか保証しません。未送信のまま残ったプロンプトでも
# 終了コードは 0 になり、続く wait は idle のままのセッションに対して即座に返ります。
jin session send my-session "main.go をリファクタリング" --wait-running
jin session wait my-session --timeout 300
jin session output my-session --last 1

# wait が `permission` を返した場合、子はプロンプト待ちで止まっています。send は
# idle のセッションしか受け付けません。respond はダイアログを操作し、それが消えて
# から返ります。選択肢は pane ではなく transcript から読んでください。
jin session result my-session --json
jin session respond my-session --option 1 --json
```

`respond` が対応するのは Claude Code セッションです。それ以外の agent kind、
複数の質問をまとめて尋ねるフォーム、およびそのフォームの送信確認画面は、
いずれもキーを送らずに拒否されます。これらは attach して自分で回答してください。

#### 終了コード

| コード | 意味 |
|--------|------|
| 0 | 成功 |
| 1 | 一般エラー（デーモンに接続できない場合を含む） |
| 2 | セッションが見つからない |
| 3 | 予約済み（デーモン未起動）。**現在は返されません** |
| 4 | タイムアウト（`session wait` / `send --wait-running` / `respond`） |
| 5 | worktree に未コミットの変更がある |
| 6 | セレクタが複数のセッションに一致 |

デーモンの停止を 3 で判定しないでください。代わりに
`jin daemon status >/dev/null 2>&1 || jin daemon start` で事前に確認します。

各コードへの対処は `jin docs show exit-codes` を参照してください。

### ユーティリティ

```bash
jin session workdir <session-name>    # セッションの作業ディレクトリパスを出力
jin session edit <session-name>       # EDITOR でセッションの作業ディレクトリを開く
```

以下のシェル関数を定義すると便利です：

```bash
# セッションの作業ディレクトリに移動
cc-cd() { cd "$(jin session workdir "$1")"; }

# fzf でセッションを選択して作業ディレクトリに移動
cc-cdf() {
  local session
  session=$(jin session list | tail -n +2 | fzf --height 40% --reverse | awk '{print $1}')
  [[ -n "$session" ]] && cd "$(jin session workdir "$session")"
}

# fzf でセッションを選択してアタッチ
cc-attach() {
  local session
  session=$(jin session list | tail -n +2 | fzf --height 40% --reverse | awk '{print $1}')
  [[ -n "$session" ]] && jin session attach "$session"
}
```

### シェル補完

```bash
# bash
source <(jin completion bash)

# zsh
source <(jin completion zsh)

# fish
jin completion fish | source
```

## 設定

[XDG Base Directory Specification](https://specifications.freedesktop.org/basedir-spec/) に準拠して、ファイルが config / state / runtime に分かれて保存されます:

```
$XDG_CONFIG_HOME/jind-ai/      （デフォルト: ~/.config/jind-ai）
└── config.yaml                # 設定ファイル

$XDG_STATE_HOME/jind-ai/       （デフォルト: ~/.local/state/jind-ai）
├── state.yaml                 # 状態ファイル（前回使用したリポジトリ等）
├── sessions/                  # セッションデータ
├── hooks-settings.json        # Claude Code フック設定（自動生成）
├── plugins.lock.yaml          # インストール済みプラグイン台帳（下記のプラグイン節を参照）
├── plugin-logs/               # プラグインごとの dispatch/run とビルド出力
├── daemon-debug.log           # デーモンデバッグログ（JIN_DEBUG=1 時）
├── hook-debug.log             # フックデバッグログ（JIN_DEBUG=1 時）
└── plugin-debug.log           # プラグインディスパッチャーのデバッグログ（JIN_DEBUG=1 時）

$XDG_DATA_HOME/jind-ai/        （デフォルト: ~/.local/share/jind-ai）
└── plugins/                   # インストール済みプラグイン（下記のプラグイン節を参照）

$XDG_RUNTIME_DIR/jind-ai/      （未設定時のフォールバック: $TMPDIR/jind-ai-<uid>）
└── daemon.sock                # デーモンソケット
```

### 設定例 (`~/.config/jind-ai/config.yaml`)

```yaml
# キーバインドのカスタマイズ（省略時はデフォルト値を使用）
keybindings:
  # セッション一覧画面
  up: ["up", "k"]
  down: ["down", "j"]
  attach: ["enter"]
  new: ["n"]
  kill: ["x"]
  delete: ["d"]
  refresh: ["r"]
  search: ["M-f"]         # セッション切り替えピッカー（ファジー検索）を開く
                          # デフォルトは M-f (Alt+f)。修飾キー必須 — 素の文字を
                          # 割り当てると表示ペインに届かず、エージェント側の
                          # スラッシュコマンド等を奪ってしまう。
                          # 旧デフォルトの `/` に戻したい場合は ["/"]
                          # （ただしペイン内 `/` 入力が奪われる）
  vscode: ["v"]
  quit: ["q", "ctrl+c"]
  help: ["?"]
  # セッション作成フォーム
  next_field: ["tab"]
  prev_field: ["shift+tab"]
  submit: ["enter"]
  cancel_form: ["esc"]
  # アタッチ中
  detach: ["ctrl+]"]  # デフォルト: ctrl+]
                       # サポートキー: ctrl+^, ctrl+], ctrl+\, ctrl+g
  # outer tmux (jin-mgr) — プラグイン action 単位のトリガー（左右両ペイン）
  # デフォルト無し。プラグイン x action 単位でユーザーがオプトインする。
  # tmux `run-shell -b` (バックグラウンド、ペインに出力を出さない) 経由で
  # `jin plugin run <name> <action>` を発火するため、popup を開くかどうかは
  # action 側の責務（`jin pane popup --here` モデル準拠）。未インストールの
  # プラグインは silent skip（log 1 行）。core outer-tmux バインドとのキー
  # 衝突は warning のみ、tmux 後勝ちに従う。
  # outer-tmux 系キーは tmux 記法 (`M-n`, `C-f`) と "+" 記法 (`alt+n`,
  # `ctrl+f`) の両方を受け付け、読み込み時に tmux 記法へ正規化される。
  # ※ 0.7.x の `plugins.<name>.keys` 形式は 0.8.0 で無効化される（起動時
  # に WARN が 1 回出て binding が drop される）。下記の
  # `plugins.<name>.actions.<id>.keys` 形式へ手動で書き直すこと。
  plugins:
    # notifier:
    #   actions:
    #     default: { keys: ["M-n"] }              # 1 打鍵で default action
    #     send-dm: { keys: ["M-d", "C-M-d"] }     # 複数キー / 別 action
    # worktree-cleanup:
    #   actions:
    #     default: { keys: ["M-w"] }
    # journal:
    #   actions:
    #     default: { keys: ["ctrl+f"] }           # "+" 記法も可

# ポップアップサイズ (percent、1-100 の int)。省略した項目はデフォルト値
# (create/session_filter/action = 70-80) が使われます。全項目と経路は
# docs/tui-guide.md#popup-sizes を参照。
popups:
  create:         { width: 80, height: 80 }
  session_filter: { width: 70, height: 70 }
  help:           { width: 60, height: 60 }
  action:         { width: 70, height: 70 }
  confirm:        { width: 50, height: 50 }  # Kill/Delete の確認 (既定は 48x10 セル)
  plugin_default: { width: 70, height: 70 }
  plugins:                                # プラグイン単位の上書き
    # my-notifier:  { width: 40, height: 20 }
```

### Worktree の作成先

`jin session new --worktree` はデフォルトで `$XDG_STATE_HOME/jind-ai/worktrees/{name}`（通常 `~/.local/state/jind-ai/worktrees/` 配下）に worktree を作成します。`config.yaml` の `worktree.base_dir` で任意の場所に変更できます:

```yaml
worktree:
  # リポジトリ単位でまとめて配置
  base_dir: "${HOME}/ghq/worktrees/{repo}/{name}"
```

その他の配置例:

```yaml
# 開発ディレクトリ配下にフラットに置く
worktree:
  base_dir: "${HOME}/dev/worktrees/{name}"

# 固定ルート配下（{repo} を使わない）
worktree:
  base_dir: "/mnt/fast/worktrees/{name}"
```

テンプレート変数:

| 変数 | 展開結果 |
|------|----------|
| `{name}` | worktree 名（例: `jin-abcd1234` / `--name` で指定した名前） |
| `{repo}` | 元リポジトリのベース名 |
| `${VAR}` | 環境変数（`os.ExpandEnv` に準拠） |

展開結果は絶対パスである必要があります。未知の `{xxx}` はセッション作成時にエラーになります。

### Worktree のブランチ命名

worktree 作成時には対応するブランチも自動生成されます。命名を制御する 2 つの設定:

```yaml
worktree:
  branch_prefix: "topic/"   # デフォルト: "jin/"。"" にするとプレフィックス無し。
  default_branch: "main"    # 起点ブランチのフォールバック。デフォルト: ""（フォールバック無し）
```

- **`branch_prefix`** — 自動生成された worktree 名の前に付与されてブランチ名になります。worktree 名先頭の `jin-` は事前に除去されるため、デフォルト設定では `jin-abcd1234` は `jin/jin-abcd1234` ではなく `jin/abcd1234` になります。`jin session new --worktree-branch <name>` でブランチを明示指定した場合は無視されます。
- **`default_branch`** — リポジトリの起点ブランチを自動検出**できなかった場合のみ**使用されます。検出は `refs/remotes/origin/HEAD` を参照するため、origin/HEAD が未設定のクローン（一部の tarball、`git clone --no-checkout`、古いクローン等）ではフォールバックが発動します。検出も失敗し `default_branch` も空だと、`cannot detect default branch` エラーでセッション作成が失敗します。

worktree 作成自体は**完全オフライン**で動作します — ローカルの `origin/<base>` からブランチを切るだけでネットワークアクセスは行いません。重いリポジトリでもセッション作成のたびに fetch のコストを払わずに済みます。最新のリモート tip から worktree を切り出したい場合は、`jin session new --worktree` の前に元リポジトリで `git fetch origin <base>` を実行するか、下記の [Worktree Post-Create Hook](#worktree-post-create-hook) 内で fetch を仕込んでください。

## TUI キーバインド

### セッション一覧画面

| キー | 動作 |
|------|------|
| `↑/k` | 上に移動 |
| `↓/j` | 下に移動 |
| `←/h` | 前のページ |
| `→/l` | 次のページ |
| `M-f` | セッション切り替えを開く（ファジー検索ポップアップ）— 詳細は [アウター tmux — セッション切り替え](#アウター-tmux--セッション切り替え) |
| `Enter` | セッションにアタッチ |
| `n` | 新規セッション作成 |
| `x` | セッション終了 |
| `d` | セッション削除 |
| `r` | 一覧更新 |
| `v` | VS Codeで開く |
| `?` | ヘルプ表示 |
| `q` | 終了 |

### セッション作成フォーム

| キー | 動作 |
|------|------|
| `Tab` | 次のフィールドへ移動 |
| `Shift+Tab` | 前のフィールドへ移動 |
| `Enter` | セッション作成 |
| `Esc` | キャンセル |

アタッチ中は `Ctrl+]`（デフォルト）でデタッチして TUI に戻ります。
`config.yaml` の `keybindings.detach` で変更可能です。

サポートされるデタッチキー:

| キー | 説明 |
|------|------|
| `ctrl+]` | デフォルト |
| `ctrl+^` | Ctrl+Shift+6 |
| `ctrl+\` | Ctrl+バックスラッシュ |
| `ctrl+g` | Ctrl+G |

### アウター tmux — セッション切り替え

`M-f`（Alt+f、デフォルト）でセッション切り替えピッカーを開きます。ファジー検索ポップ
アップで、数文字入力して `Enter` を押せば即座にそのセッションへアタッチでき
ます。outer tmux（`jin-mgr`）のルートキーテーブルにバインドされるため、
セッション一覧（左ペイン）・アタッチ中のエージェント（右ペイン）どちらからでも
起動できます。検索対象はセッションの説明・作業ディレクトリ・現在の作業ディレ
クトリ・git ブランチ・フリート・エージェント種別の 6 フィールド（
[sahilm/fuzzy](https://github.com/sahilm/fuzzy) による subsequence マッチ、
smart-case、スコア順ランキング）。`Esc` でポップアップを閉じても TUI の状態
は変わりません。`↑`/`↓` または `Ctrl+P`/`Ctrl+N` でカーソル移動します。

デフォルトが `/` から `M-f` に変更されたのは、outer tmux のルートに素の文字を
バインドすると表示ペインでの `/` 入力（Claude Code のスラッシュコマンド、less
や vim の検索など）を奪ってしまうためです。アクションパレット（`M-p`）にも
「switch session」エントリがあるため、ショートカット未設定でも起動できます。

```yaml
keybindings:
  search: ["ctrl+p"]      # Ctrl+p に変更
  # search: ["/"]         # 旧デフォルトの `/` に戻す（表示ペインの `/` が奪われる）
  # search: []            # 完全に無効化（bind-key を発行しない）
```

## Claude Code Hooks

jind-ai はセッションの状態検知に Claude Code の hooks を使用します。**Hooks は自動で設定されます** — 手動設定は不要です。

セッション起動時に jind-ai が `$XDG_STATE_HOME/jind-ai/hooks-settings.json`（デフォルト `~/.local/state/jind-ai/hooks-settings.json`）を生成し、`claude --settings` 経由で Claude Code に渡します。

各 hook の役割:

| Hook Event | 役割 |
|-----------|------|
| `UserPromptSubmit` | ユーザーがプロンプトを送信 → セッションを `thinking` に |
| `PostToolUse` | ツール実行完了 → セッションを `thinking` に（`permission` 状態からの復帰） |
| `Stop` | Claude のターン終了 → セッションを `idle` に（`JIN_NOTIFY_KIND=task-complete` をプラグインに配信） |
| `Notification` | 権限要求等 → セッションを `permission` に（`JIN_NOTIFY_KIND=permission` をプラグインに配信） |

## Worktree Post-Create Hook

`jin session new --worktree` でセッションを作成した際、worktree 生成直後にセットアップ用スクリプトを自動実行できます。依存関係のインストール、`.env` のコピー、submodule の初期化など、worktree を作るたびに手作業でやっていた手順を丸ごと自動化できます。

### スクリプトの配置

**元リポジトリ**（worktree 側ではなく）の `.jin/worktree-post-create.sh` に置きます。常に `bash` 経由で起動されるため `chmod +x` は不要。ファイルが存在しなければ hook は無音でスキップされます。

```bash
#!/usr/bin/env bash
set -euo pipefail

# 親リポジトリの .env をコピー（git 管理外）
cp "$JIN_REPO_ROOT/.env" "$JIN_WORKTREE_PATH/.env" 2>/dev/null || true

# 依存関係インストール
pnpm install
```

### 環境変数

| 変数 | 内容 |
|------|------|
| `JIN_WORKTREE_PATH` | 作成された worktree の絶対パス |
| `JIN_WORKTREE_BRANCH` | worktree でチェックアウトされているブランチ |
| `JIN_WORKTREE_BASE` | worktree の起点となったベースブランチ |
| `JIN_SESSION_ID` | 作成中セッションの UUID |
| `JIN_SESSION_NAME` | `--name` で指定されたセッション名（省略時は空。自動導出名は hook 実行後に確定するため） |
| `JIN_REPO_ROOT` | 元リポジトリの絶対パス |

### セキュリティ: allowlist

スクリプトはリポジトリにチェックインされる shell script なので、jind-ai は明示的に信頼されたリポジトリでない限り実行しません（direnv 流の allow モデル）。信頼はスクリプトの SHA256 で紐付けされ、スクリプトを編集すると再度信頼が必要になります。

```bash
jin worktree allow    # カレントリポジトリを信頼（スクリプト全文表示 + 確認プロンプト）
jin worktree revoke   # 信頼を取り消し
jin worktree status   # カレントリポジトリの信頼状態を表示
jin worktree list     # 信頼済みリポジトリを一覧
```

スクリプトが存在するが未信頼（または変更検知された）場合、hook は警告付きでスキップされ、worktree 自体は作成されて Claude は通常通り起動します。TUI からセッション作成した場合は popup 上で「許可する / スキップして作成 / やめる」の 3 択が表示されます。

### hook のスキップ

- `jin session new --worktree --no-hook` — このセッションだけ hook をスキップ
- `~/.config/jind-ai/config.yaml` に `worktree.hook_enabled: false` — 全リポジトリで hook を無効化
- `worktree.hook_timeout: <秒>` — タイムアウト変更（デフォルト `300`）。超過時はプロセスグループに `SIGTERM` を送り、5 秒の grace 後に生存していれば `SIGKILL`。

### 失敗時の挙動

hook が非ゼロ終了またはタイムアウトすると、worktree とブランチは rollback され、`jin session new` は非ゼロ exit code で失敗します。stdout/stderr は `~/.local/state/jind-ai/hook-logs/<session-id>.log` に保存され、rollback 後も診断のために残ります。

## プラグイン

jind-ai では、セッションのステータス変化に反応して、あるいはオンデマンドで、任意のシェル実行可能なプラグインを実行できます。プラグインはマニフェストとエントリーポイントのスクリプトを持つディレクトリです。jind-ai はスクリプトが何をするかには関与せず、いつ実行されどんな環境を受け取るかだけを管理します。

コミュニティプラグインは [plugin registry](docs/plugin-registry.md) から発見できます。`jin plugin ls-remote` で一覧、`jin plugin install <name>` でレジストリ名指定インストール（コミット SHA ピン + 同意画面付き）が可能です。

### 2 通りの実行方式

- **Event listener（イベントリスナー）** — マニフェストの各 action がその `on:` マッチャー経由で `status_changed` を購読します。通知、ロギング、CI トリガーなど、非対話的な用途に向いています。注意: イベントはステータスが実際に変化した時のみ発火します。ステータス遷移を伴わない通知（既に idle の状態での再停止など）は dispatch されません。プラグインが複数の action を宣言している場合、それぞれ独立に match / debounce されるため、同一イベントで同じプラグイン内の複数 action が同時に fan-out することがあります。
- **Action（アクション）** — `jin plugin run <name> [action] [--session <selector>]` で明示的に起動します。ポップアップベースの diff レビュー UI のような、対話的なワークフローに向いています。`[action]` を省略するとプラグインの default action（`actions[0]`）が走り、action ID を渡すとその action を選択します。ある action の `on: []` を指定するとその action は action 専用になります。`--session` を省略すると **グローバル action** になり、セッション由来の環境変数はすべて空になります。action 実行時は (global・session 指定を問わず) 呼び出し元の CLI が tmux クライアント内にいた場合、`JIN_CALLER_TMUX_SOCKET` / `JIN_CALLER_TMUX_PANE` が起動元を示します。

どちらのエントリーポイントも同じ action の `entrypoint` を同じ環境で実行します。違いはトリガーだけです。

### マニフェスト（`jind-ai-plugin.yaml`）

このファイルをプラグインディレクトリのルートに配置します。ランタイム（ディスパッチャー）と publish 時（レジストリクローラー）が同じマニフェストを読みます。単一ファイル、単一の真実です。

```yaml
schema_version: 2
name: notifier
version: 0.2.0
description: Desktop notifications for jin sessions
license: MIT
homepage: https://github.com/foo/notifier
jin: ">=0.8.0"
install:
  source:
    build:
      - go build -o bin/notifier ./cmd/notifier
    entrypoint: ./bin/notifier
actions:
  - id: default                                      # actions[0] が暗黙の default
    entrypoint: ./bin/notifier notify
    on: ["status_changed:idle", "status_changed:permission"]
    label: "Desktop notification"
    timeout: 30s
  - id: send-dm                                      # `jin plugin run notifier send-dm`
    entrypoint: ./bin/notifier send-dm
    on: []                                           # action 専用（イベント購読なし）
    label: "Send DM to teammate"
    popup: { width: 60, height: 30 }
```

既存の v1 マニフェスト（`schema_version: 1` + top-level `entrypoint` / `on` / `timeout` / `popup`）はそのまま動きます。parse 時に単一 action 形式へ normalize されるため、プラグイン作者側の対応は不要です。新規は v2 で書いてください。

| フィールド | 必須 | 説明 |
|-----------|------|------|
| `schema_version` | あり | マニフェスト世代。`1` または `2`。v1 は parse 時に単一 action の v2 相当へ自動 normalize |
| `name` | あり | `[a-z][a-z0-9-]{1,63}`。レジストリ内で unique。jind-ai がインストールするディレクトリ名と一致 |
| `version` | あり | プラグイン自身の semver（`X.Y.Z`。pre-release / build metadata 可） |
| `description` | あり | `jin plugin ls-remote` の検索結果に表示される一行説明 |
| `license` / `homepage` | なし | 任意メタデータ。レジストリエントリに載る |
| `jin` | あり | jin バイナリに対する semver 制約（`">=0.8.0"`、`"^0.8"`、`">=0.8 <0.10"`）。install 時と毎 dispatch 時にチェック |
| `install.source.build` | なし | ビルドコマンド配列（各要素が独立の `bash -c`。要素をまたぐパイプは不可） — [言語別ガイド](#言語別ガイド) を参照。直接実行可能な entrypoint を同梱するプラグイン（shell script、リポジトリに commit した prebuilt バイナリ等）では省略可 |
| `install.source.entrypoint` | 条件付き | 個々の action が entrypoint を宣言しなかった時に使われるデフォルト entrypoint。`install.source` 使用時は必須 |
| `install.release_asset.pattern` | 条件付き | `install.source` の代替。最新 GitHub Release から prebuilt asset をダウンロード。プレースホルダ: `{os}` / `{arch}` |
| `actions[]` | v2 のみ | プラグインが公開する action のリスト。`actions[0]` が暗黙の default。各要素が `id` / `entrypoint` / `on` / `label` / `timeout` / `popup` を持つ（以下の行を参照） |
| `actions[].id` | あり | `[a-z][a-z0-9-]{0,63}`。プラグイン内で unique。パレット / keybindings / `jin plugin run` すべてが ID で action を参照するため、明示 ID を強く推奨 |
| `actions[].entrypoint` | 条件付き | この action の実行パス（プラグインディレクトリ相対）。`install.source.entrypoint` でカバーされる場合は省略可 |
| `actions[].on` | なし | この action の `status_changed` マッチャー。v1 の top-level `on` と同じ構文。空または省略時は action 専用。match / debounce は action 単位で独立 |
| `actions[].label` | なし | パレット / help popup に表示される人間可読ラベル。空の場合パレットは `<plugin>:<action-id>` を表示（default action の ID が `default` のときは plugin 名のみ） |
| `actions[].timeout` | なし | この action 個別の `timeout` 上書き。デフォルト `30s` |
| `actions[].popup.width` / `.height` | なし | `jin pane popup --here` の action 単位のサイズヒント（1–100、%） |
| `actions[].listener` | なし | この action を「イベント購読専用」とマーク。`on:` にマッチしたときは通常通り発火するが、ユーザー向けサーフェス（パレット / help popup / shell 補完）からは非表示になる。`jin plugin run <plugin> <action>` による直接起動は debug 目的で許可されたまま。`on:` 非空必須（listener with no events は無意味） |
| `on` / `timeout` / `popup`（top-level） | v1 のみ | v1 レガシーフィールド。v2 では validate エラーになるため `actions[]` 側に書く |

`install.source` と `install.release_asset` は排他です。

`config.yaml` はプラグインの有効/無効切り替えと dispatch タイミングの調整（下記）のみを行います — マニフェストのフィールドを重複して持つことはありません。

**Listener action** はイベント購読と UI を両方持つプラグインの定番パターンです。責務を分けて、UI 側だけをパレットに露出させます:

```yaml
actions:
  - id: list                        # ユーザー向け: パレットに表示
    entrypoint: ./notifier.sh list
    label: "Show pending sessions"
  - id: listen                      # event listener: パレット非表示
    entrypoint: ./notifier.sh listen
    on: ["status_changed"]
    listener: true                  # on: 非空必須
```

### プラグインが受け取る情報

環境変数:

| 変数 | 説明 |
|------|------|
| `JIN_EVENT` | `status_changed` または `action` |
| `JIN_ACTION_ID` | この実行を発火させたマニフェスト action の ID（v1 マニフェストや v2 default action の合成時は `default`）。共通 entrypoint を書く場合、argv 分岐の代わりにこの env で action を識別できる |
| `JIN_SESSION_ID` | セッション ID |
| `JIN_STATUS` | 現在のステータス |
| `JIN_PREV_STATUS` | 直前のステータス（`action` 実行時は空） |
| `JIN_AGENT_KIND` | アダプタの種類（`claude` など） |
| `JIN_WORKDIR` | セッションの作業ディレクトリ |
| `JIN_TMUX_PANE_ID` | tmux ペイン ID（判明している場合） |
| `JIN_NOTIFY_KIND` | この遷移の通知種別: `task-complete`、`error`、`permission`。通知を伴わない遷移では空 |
| `JIN_PLUGIN_DEPTH` | チェーンの深さ — [制約](#制約) を参照 |
| `JIN_SOCKET` | デーモンソケットのパス。プラグインが呼び出す `jin` CLI はこれを自動的に読み取ります |
| `JIN_BIN` | 稼働中のデーモンと一致する `jin` の絶対パス。jind-ai が state ディレクトリ配下に保持するコピーを指すため、デーモンの起動元バイナリが再ビルド・削除されても有効なままです。PATH 上の `jin` は新しいサブコマンドを持たない古いインストールである可能性があるため、素の `jin` より `"${JIN_BIN:-jin}"` を優先してください |
| `JIN_DEBUG` | デーモンがデバッグログ有効で動作している場合に `1`。プラグインが呼び戻す `jin` も自身の動作を記録します。無効時は `0` ではなく未設定 |
| `JIN_CALLER_TMUX_SOCKET` | action 実行時のみ: 呼び出し元 CLI がいた tmux サーバのソケットパス（呼び出し元の `$TMUX` 由来）。呼び出し元が tmux 外の場合は未設定（空文字ではない） |
| `JIN_CALLER_TMUX_PANE` | action 実行時のみ: 呼び出し元 CLI のペイン ID（`$TMUX_PANE` 由来）。不明な場合は未設定 |

同じデータは **stdin に JSON としても** 書き込まれます（フィールドは同一、snake_case。caller tmux コンテキストは環境変数のみ）。

この薄いペイロード以上の情報が必要な場合は、jind-ai に問い合わせます:

```bash
jin session info "$JIN_SESSION_ID" --json    # セッションの詳細情報を取得
jin session send "$JIN_SESSION_ID" "..."     # プロンプトを送信
jin session result "$JIN_SESSION_ID" --json  # 構造化された transcript エントリを取得
jin session focus "$JIN_SESSION_ID"          # 起動中の TUI にこのセッションを表示させる
jin pane popup "$JIN_SESSION_ID" -- <cmd>    # セッションのペインに tmux popup を重ねる
jin pane popup --here -- <cmd>               # 呼び出し元自身のペインに tmux popup を重ねる（$TMUX を優先、無ければ JIN_CALLER_TMUX_SOCKET へフォールバック）
jin pane split "$JIN_SESSION_ID" -- <cmd>
jin pane capture "$JIN_SESSION_ID"
jin pane send-keys "$JIN_SESSION_ID" <keys>
```

**互換性の契約**: 見覚えのない環境変数・JSON フィールド・CLI フラグはエラーではなく無視すべきものとして扱ってください。jind-ai は同一 `schema_version` 内でこのサーフェスへ追加することはあっても、削除・改名することはありません。破壊的削除は `schema_version` の bump（pre-1.0 では jin の minor リリース）でのみ発生します。

### インストール / 更新 / 削除 / 一覧

```bash
# レジストリから (docs/plugin-registry.md 参照)
jin plugin ls-remote                              # レジストリのプラグイン一覧
jin plugin install jind-ai-notifier               # latest release、以降 `plugin update` が追従
jin plugin install jind-ai-notifier -v 0.2.0      # バージョンピン、以降 `plugin update` は据え置き
jin plugin install jind-ai-notifier --force       # jin 互換範囲外でも強制インストール

# git ソースから (github.com/、gitlab.com/、self-hosted、ssh URL ...)
jin plugin install github.com/owner/repo          # default branch、以降 `plugin update` は最大 semver tag に追従
jin plugin install github.com/owner/repo@v1.2.0   # tag / branch / SHA でピン、以降 `plugin update` は据え置き

# ローカルディレクトリから（開発時、symlink）
jin plugin install --link ./my-plugin

jin plugin update <name>
jin plugin remove <name>
jin plugin list          # NAME / VERSION / STATE / SOURCE を表示。--json でスクリプト連携可

# マニフェストの validate — レジストリクローラーと同じチェック
jin plugin validate                               # デフォルトはカレントディレクトリ
jin plugin validate --github-actions              # ::error / ::warning annotation 形式で出力
```

git からの install/update では、何かに触れる前にマニフェスト（`name`、`version`、`on`、`entrypoint`、`build`）と解決したコミット SHA を表示し、確認を求めます（`--yes` でスキップ可）。承認されたコミット SHA は `plugins.lock.yaml` に記録されるため、以降の `install`/`update` が確認時と異なるコミットへ黙って進むことはありません。`--link` したプラグインはこの確認をスキップします — ローカルパスをリンクすること自体が信頼の意思表示であり、jind-ai はリンクされたプラグインに対してビルドを実行しません。

**`jin plugin update <name>` は unpin なインストールのみ latest release を再解決します**: registry 経由でインストールした plugin は registry の `latest_version` を、raw git URL でインストールした plugin は `git ls-remote --tags` で最大 semver tag を選択します（semver tag が無い場合は lock ref にフォールバック）。 `-v <ver>` (registry) や `@<ref>` (git URL) でピンして install した plugin は `plugin update` では動きません（据え置き旨のメッセージが出て終了、動かしたければ再 install してください）。install 時の「latest 追従 vs 据え置き」意図が lock に刻まれ、以降の update がそれを守ります。

### 言語別ガイド

- **Shell / 単一ファイル** — スクリプトを repo に commit し `entrypoint` から直接指して `install.source.build` は省略。スクリプトが生成物であるか実行権限が git 上で保持されない場合のみ、`chmod +x` の 1 要素を build に追加してください。
- **Node.js / TypeScript** — `dist/`（esbuild 等）にバンドルするビルドステップを 1 つ書いてください。ランタイム依存解決（bun/deno）も動作しますが、dispatch は fail-open のため初回 dispatch 時のネットワーク取得が黙って失敗することがあります — 事前ビルド済みバンドルの方が予測可能です。
- **Go / Rust などのコンパイル言語** — `install.source.build` にビルド手順を宣言してください。各要素は独立プロセスとして実行され（要素をまたぐパイプは不可）、ユーザーのプラットフォーム/アーキテクチャに合わせたバイナリを生成できます（`go.sum` / `Cargo.lock` は再現性のために有用）。ビルドは install/update ごとに一度だけ実行されます。jind-ai は依存解決やツールチェーンの検出を代行しないため、必要なものはプラグイン自身の README に明記してください。非ゼロ終了した場合 install/update はアトミックに失敗し（中途半端な状態は残りません）、出力は `~/.local/state/jind-ai/plugin-logs/<name>-build.log` に保存されます。制限時間はステップごとではなくシーケンス全体で 1 つです（`plugins.build_timeout`、既定 300 秒）。

  **ビルド環境は allowlist です。** ビルドステップに渡るのは、起動元プロセスから引き継いだ `PATH` / `HOME` / `USER` / `SHELL` / `LANG` / `TERM` / `LC_*` と、`npm_config_ignore_scripts=true`（サプライチェーン対策で、自分のビルドステップ内で上書き可能）だけです。`JAVA_HOME` や `CARGO_HOME`、レジストリのトークンなどが必要な場合は、継承を当てにせずビルドステップ内で導出してください。`jin plugin validate --run-build` は同じフィルタと同じ既定予算を適用し、あなた自身の `plugins.build_timeout`（この設定はプラグインを入れる側のものです）も manifest の `timeout:`（これは dispatch の応答時間であってコンパイル時間ではありません）も参照しません。したがって install が許す以上を必要とするビルドは、ユーザーの手元ではなくあなたの手元で落ちます。ただし通過した変数の**中身**までは保証できません。`PATH` はあなたのものなので、あなたにしか入っていないツールチェーンはローカルでは問題なくビルドできてしまいます。プラグインが必要とするものは自分の README に明記してください。ビルド自体はサンドボックス化されておらず、ユーザー自身の権限で実行されます。

### 制約

- **永続プロセスは不可。** jind-ai はイベント/アクションごとにプラグインを起動し、終了後は破棄します。長時間稼働するデーモンを `entrypoint` に組み込まないでください。常駐プロセスが必要な場合は自分で起動し（手動、または systemd user unit として）、プラグイン自体はそこへの薄いクライアント（例: `curl`）にとどめてください。
- **ポップアップは `JIN_*` 環境変数を継承しません。** `jin pane popup` / `jin pane split` は tmux が新規に spawn したプロセスでコマンドを実行するため、ポップアップに必要なデータはコマンドライン引数として渡してください（またはコマンド文字列内の env 代入プレフィックスとして。例: `jin pane popup "$JIN_SESSION_ID" -- "JIN_BIN=$JIN_BIN inner.sh --id $JIN_SESSION_ID"`）。
- **Fail-open。** エラー・タイムアウト・ハングしたプラグインがセッションのステータスパイプラインをブロックすることはありません — ログに記録され、パイプラインは処理を続行します。タイムアウトのデフォルトは 30s（マニフェストの `timeout:`）。
- **ループの残存リスク。** jind-ai は同一の (plugin, session, event) の短時間内での繰り返し dispatch をデバウンスし（デフォルト 3s、下記の `plugins.debounce`）、プラグインが別のプラグイン実行を 1 ホップを超えて連鎖させることを拒否します（`JIN_PLUGIN_DEPTH`）。ただしどちらも *遅い* ピンポン（例: プラグインが送信したプロンプトへの応答が数秒後に同じプラグインを再トリガーする）は捕捉できません — これを避けるのはプラグイン作者の責任です。

### 設定（`~/.config/jind-ai/config.yaml`）

```yaml
plugins:
  enabled: true          # デフォルト true。false にすると全プラグインのディスパッチを無効化
  disabled: ["notifier"] # 個別プラグインを名前で無効化
  build_timeout: 300  # 秒。install/update のビルドシーケンス全体の予算（デフォルト 300）
  debounce: 3          # 秒。ディスパッチのデバウンス窓（デフォルト 3）
```

### 互換性

プラグインは `jin:` に jin バイナリの semver 制約（例: `">=0.7.0"`、`"^0.7"`）を宣言します。チェックは install/update 時（fail-closed — 範囲外のプラグインは何も書き込まれる前に拒否されます）と、dispatch のたびに再度行われます（fail-open — 互換性のないインストール済みプラグインはスキップされ、一度だけログに記録され、`jin plugin list` で `incompatible` と表示されます。`jin plugin run` は `jin plugin update <name>` を促します）。開発ビルド（`jin --version` が `dev` や未 stamp を返す場合）は制約を無条件で満たしたものとして扱われるため、ローカルでのプラグイン作業を阻害しません。

`schema_version` フィールドは `jin` とは直交する概念で、マニフェスト世代を識別します。jind-ai は `[min, current]` のウィンドウで schema をサポートし、現行は両方とも `1` です。将来 bump が始まっても 2 世代前までは受け付けます。

### プラグインのデバッグ

```bash
export JIN_DEBUG=1
tail -f ~/.local/state/jind-ai/plugin-debug.log        # ディスパッチャーの判断ログ
tail -f ~/.local/state/jind-ai/plugin-logs/<name>.log  # プラグイン自身の stdout/stderr
```

フラグは `JIN_DEBUG=1` としてプラグインにも届くため、プラグインが呼び戻した
`jin` も記録を残します — `jin hook` なら `hook-debug.log`、それ以外のデーモン側は
`daemon-debug.log` です。

## デバッグ

```bash
# デバッグログを有効化
export JIN_DEBUG=1

# デーモン起動
jin daemon start

# ログ確認
tail -f ~/.local/state/jind-ai/daemon-debug.log
```

## 必要要件

- Go 1.26+
- tmux 3.5+（3.3a はセッションに再アタッチできません。3.6a と 3.7a にはそれぞれ表示バグがあります — 詳細は [docs/gotchas.md](docs/gotchas.md) を参照）
- Claude Code CLI がインストールされていること

## ライセンス

MIT

# ccvalet (claude-code-valet)

複数の Claude Code セッションを同時に稼働させ、一元管理するための CLI ツール。

## 特長

- **複数セッション管理**: 複数の Claude Code セッションをバックグラウンドで同時実行
- **TUI**: セッション一覧・状態確認・操作を対話的に行えるターミナル UI
- **アタッチ/デタッチ**: セッション間を素早く切り替え（`Ctrl+]` でデタッチ）
- **デスクトップ通知**: 許可待ち・タスク完了をOS通知でお知らせ（macOS / Linux対応）
- **リモートホスト対応**: SSH / Docker 経由でリモートのセッションも統合管理

## インストール

### Go install

```bash
go install github.com/takaaki-s/claude-code-valet/cmd/ccvalet@latest
```

### ソースからビルド

```bash
git clone https://github.com/takaaki-s/claude-code-valet.git
cd claude-code-valet
make build    # bin/ccvalet にビルド
make install  # $GOPATH/bin にインストール
```

## クイックスタート

### 1. デーモンを起動

```bash
ccvalet daemon start
```

### 2. TUI を起動

```bash
ccvalet ui
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
| `idle` | ○ | `Stop` hook | 入力待ち |
| `stopped` | ■ | プロセス死亡検知 | 停止済み |

## CLI コマンド

### デーモン管理

```bash
ccvalet daemon start   # デーモン起動
ccvalet daemon stop    # デーモン停止
ccvalet daemon status  # 状態確認
```

### セッション管理

```bash
# セッション作成（TUI で対話的に作成 - 推奨）
ccvalet new

# セッション作成（作業ディレクトリ指定）
ccvalet new --workdir ~/repos/myrepo

# セッション一覧
ccvalet list

# セッションにアタッチ
ccvalet session attach <session-name>

# セッション終了
ccvalet kill <session-name>

# セッション削除
ccvalet delete <session-name>

# 停止済みセッションの一括削除
ccvalet cleanup stopped
ccvalet cleanup stopped --dry-run   # 削除対象の確認
```

### ユーティリティ

```bash
ccvalet session workdir <session-name>    # セッションの作業ディレクトリパスを出力
ccvalet session edit <session-name>       # EDITOR でセッションの作業ディレクトリを開く
```

> **注意**: `workdir` / `edit` はローカルセッション（host種類が `local`）でのみ正しく動作します。

以下のシェル関数を定義すると便利です：

```bash
# セッションの作業ディレクトリに移動
cc-cd() { cd "$(ccvalet session workdir "$1")"; }

# fzf でセッションを選択して作業ディレクトリに移動
cc-cdf() {
  local session
  session=$(ccvalet list | tail -n +2 | fzf --height 40% --reverse | awk '{print $1}')
  [[ -n "$session" ]] && cd "$(ccvalet session workdir "$session")"
}

# fzf でセッションを選択してアタッチ
cc-attach() {
  local session
  session=$(ccvalet list | tail -n +2 | fzf --height 40% --reverse | awk '{print $1}')
  [[ -n "$session" ]] && ccvalet session attach "$session"
}
```

### シェル補完

```bash
# bash
source <(ccvalet completion bash)

# zsh
source <(ccvalet completion zsh)

# fish
ccvalet completion fish | source
```

## 設定

設定ファイルとデータは `~/.ccvalet/` に保存されます。

```
~/.ccvalet/
├── config.yaml      # 設定ファイル
├── state.yaml       # 状態ファイル（前回使用したリポジトリ等）
├── sessions/        # セッションデータ
└── run/
    └── daemon.sock  # デーモンソケット
```

### 設定例 (`~/.ccvalet/config.yaml`)

```yaml
# キーバインドのカスタマイズ（省略時はデフォルト値を使用）
keybindings:
  # セッション一覧画面
  up: [up, k]
  down: [down, j]
  attach: [enter]
  new: ["n"]
  kill: [x]
  delete: [d]
  refresh: [r]
  quit: [q, ctrl+c]
  help: ["?"]
  vscode: ["v"]
  # セッション作成フォーム
  next_field: [tab]
  prev_field: [shift+tab]
  submit: [enter]
  cancel_form: [esc]
  # アタッチ中
  detach: [ctrl+]]  # デフォルト: ctrl+]
                     # サポートキー: ctrl+^, ctrl+], ctrl+\, ctrl+g
```

## TUI キーバインド

### セッション一覧画面

| キー | 動作 |
|------|------|
| `↑/k` | 上に移動 |
| `↓/j` | 下に移動 |
| `Enter` | セッションにアタッチ |
| `n` | 新規セッション作成 |
| `x` | セッション終了 |
| `d` | セッション削除 |
| `r` | 一覧更新 |
| `v` | VS Codeで開く |
| `!` | 通知履歴 |
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

## Claude Code Hooks 設定

ccvalet はセッションの状態検知に Claude Code の hooks を使用します。以下の設定を `~/.claude/settings.json` に追加してください。

```json
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/ccvalet hook",
            "timeout": 5
          }
        ]
      }
    ],
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/ccvalet hook",
            "timeout": 5
          }
        ]
      }
    ],
    "Notification": [
      {
        "matcher": "permission_prompt|elicitation_dialog|idle_prompt",
        "hooks": [
          {
            "type": "command",
            "command": "/absolute/path/to/ccvalet hook",
            "timeout": 5
          }
        ]
      }
    ]
  }
}
```

`/absolute/path/to/ccvalet` は `ccvalet` バイナリのフルパスに置き換えてください（`which ccvalet` で確認可能）。

各 hook の役割:

| Hook Event | 役割 |
|-----------|------|
| `UserPromptSubmit` | ユーザーがプロンプトを送信 → セッションを `thinking` に |
| `Stop` | Claude のターン終了 → セッションを `idle` に（タスク完了通知を送信） |
| `Notification` | 権限要求等 → セッションを `permission` に（権限要求通知を送信） |

## リモートホスト（EC2 / Docker）

ローカルの Mac だけでなく、EC2 インスタンスや Docker コンテナ上の CC セッションも統合管理できます。

### アーキテクチャ

Mac 上の Master デーモンが、リモートの Slave デーモンと SSH トンネル（または Docker volume mount）経由で通信します。Slave は通常の `ccvalet daemon` と同一バイナリです。

### EC2 セットアップ

**1. EC2 に ccvalet と tmux をインストール（初回のみ）**

```bash
# EC2 にログイン
ssh my-ec2-instance

# ccvalet インストール
go install github.com/takaaki-s/claude-code-valet/cmd/ccvalet@latest

# tmux インストール（未インストールの場合）
sudo apt install -y tmux  # Ubuntu/Debian
```

**2. Mac の config.yaml にホスト設定を追加**

```yaml
hosts:
  - id: ec2
    type: ssh
    host: my-ec2-instance
    ssh_opts:          # SSH接続の最適化（推奨）
      - "-o"
      - "ControlMaster=auto"
      - "-o"
      - "ControlPath=~/.ssh/sockets/%r@%h-%p"
      - "-o"
      - "ControlPersist=600"
```

**3. Master 起動で自動接続**

```bash
ccvalet daemon start  # Slave 自動起動 + トンネル確立
ccvalet ui            # TUI でローカル + EC2 を統合管理
```

Master 起動時に SSH 経由で EC2 の Slave デーモンを自動起動します。ccvalet が未インストールの場合はエラーメッセージが表示されます。

### Docker セットアップ

**1. コンテナに ccvalet と tmux を含める**

```dockerfile
# Dockerfile に追加
RUN apt-get update && apt-get install -y tmux
RUN go install github.com/takaaki-s/claude-code-valet/cmd/ccvalet@latest
```

**2. ソケット共有用 volume mount を設定してコンテナを起動**

ローカル側のソケットパスは `/tmp/ccvalet-tunnels/{hostID}/daemon.sock` に自動計算されます（SSH と同じ規約）。volume mount はこのディレクトリとコンテナ内のソケットディレクトリを対応させます。

```bash
# root ユーザーの場合
docker run -v /tmp/ccvalet-tunnels/docker-dev:/root/.ccvalet/run my-image

# non-root ユーザー（app）の場合
docker run -v /tmp/ccvalet-tunnels/docker-dev:/home/app/.ccvalet/run my-image

# socket_path をオーバーライドする場合
docker run -v /tmp/ccvalet-tunnels/docker-dev:/var/run/ccvalet my-image
```

**3. Mac の config.yaml にホスト設定を追加**

```yaml
hosts:
  # 基本設定（デフォルトソケットパス: ~/.ccvalet/run/daemon.sock）
  - id: docker-dev
    type: docker
    container: my-container

  # socket_path オーバーライド（コンテナ内パスを指定）
  - id: docker-ci
    type: docker
    container: ci-runner
    socket_path: /var/run/ccvalet/daemon.sock
```

`socket_path` はコンテナ内（リモート側）のソケットパスを指定します。省略時は `~/.ccvalet/run/daemon.sock` が使用されます。

**4. Master 起動**

```bash
ccvalet daemon start  # docker exec で Slave 自動起動
ccvalet ui
```

> **注意**: コンテナを再作成（`docker rm`）すると ccvalet が消えます。Dockerfile に含めるか、バイナリを volume mount で永続化してください。

## デバッグ

```bash
# デバッグログを有効化
export CCVALET_DEBUG=1

# デーモン起動
ccvalet daemon start

# ログ確認
tail -f ~/.ccvalet/debug.log
```

## 必要要件

- Go 1.21+
- Claude Code CLI がインストールされていること

## ライセンス

MIT

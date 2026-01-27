# Claude Pilot (ccvalet) 設計書 v3

## 1. 概要

複数のClaude Codeセッションを同時に稼働させ、一元管理するためのCLIツール。
git worktreeを活用した並行開発と、プロンプト自動注入機能による自動化をサポートする。

## 2. 目的

- 複数のタスクを並行してClaude Codeに実行させる
- セッション間を素早く切り替える
- 各セッションの状態（許可待ち、タスク完了待ち等）を一覧で把握する
- git worktreeでセッションごとに独立したワークディレクトリを提供する
- 初回trust確認の自動応答でセットアップを効率化する
- プロンプトの自動注入でワークフローを効率化する

## 3. 技術スタック

| コンポーネント | 技術 | 理由 |
|---------------|------|------|
| 言語 | **Go** | シングルバイナリ、高速、クロスコンパイル |
| PTY制御 | **creack/pty** | 成熟したGoのPTYライブラリ |
| ターミナルエミュレーション | **go-term** or 自前実装 | セッション状態解析用 |
| TUI | **bubbletea** | Elmアーキテクチャ、豊富なコンポーネント |
| スタイリング | **lipgloss** | bubbletea公式のスタイリングライブラリ |
| 設定管理 | **viper** | 柔軟な設定ファイル管理 |
| Git操作 | **go-git** or CLI | worktree管理 |

## 4. アーキテクチャ

```
┌─────────────────────────────────────────────────────────────────┐
│                        ccvalet (Go)                             │
├─────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐             │
│  │   TUI層     │  │   CLI層     │  │   API層     │             │
│  │ (bubbletea) │  │  (cobra)    │  │  (将来)     │             │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘             │
│         └────────────────┴────────────────┘                     │
│                          │                                      │
│  ┌───────────────────────┴───────────────────────┐             │
│  │              Core Services                     │             │
│  │  ┌─────────────┐  ┌─────────────┐             │             │
│  │  │  Session    │  │   Status    │             │             │
│  │  │  Manager    │  │   Monitor   │             │             │
│  │  └──────┬──────┘  └──────┬──────┘             │             │
│  │         │                │                     │             │
│  │  ┌──────┴────────────────┴──────┐             │             │
│  │  │         PTY Manager          │             │             │
│  │  │       (creack/pty)           │             │             │
│  │  └──────────────────────────────┘             │             │
│  └───────────────────────────────────────────────┘             │
│                          │                                      │
│  ┌───────────────────────┴───────────────────────┐             │
│  │              New Features                      │             │
│  │  ┌─────────────┐  ┌─────────────┐             │             │
│  │  │  Worktree   │  │ Repository  │             │             │
│  │  │  Manager    │  │  Registry   │             │             │
│  │  └─────────────┘  └─────────────┘             │             │
│  │  ┌─────────────┐                              │             │
│  │  │  Prompt     │                              │             │
│  │  │  Injector   │                              │             │
│  │  └─────────────┘                              │             │
│  └───────────────────────────────────────────────┘             │
└─────────────────────────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────────────────┐
│                    PTY Sessions                                 │
│  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐               │
│  │  Session 1  │ │  Session 2  │ │  Session 3  │               │
│  │ claude code │ │ claude code │ │ claude code │               │
│  │   (PTY)     │ │   (PTY)     │ │   (PTY)     │               │
│  │ worktree A  │ │ worktree B  │ │ 既存dir     │               │
│  └─────────────┘ └─────────────┘ └─────────────┘               │
└─────────────────────────────────────────────────────────────────┘
```

## 5. ディレクトリ構造

```
ccvalet/
├── cmd/
│   └── ccvalet/
│       ├── main.go              # エントリーポイント
│       └── cmd/
│           ├── root.go          # ルートコマンド
│           ├── daemon.go        # デーモン管理
│           ├── new.go           # セッション作成
│           ├── list.go          # セッション一覧
│           ├── attach.go        # セッションアタッチ
│           ├── kill.go          # セッション終了
│           ├── tui.go           # TUI起動
│           ├── repository.go    # リポジトリ管理
│           ├── worktree.go      # worktree管理
│           └── prompt.go        # プロンプト管理
├── internal/
│   ├── session/
│   │   ├── manager.go           # セッション管理 + 通知統合
│   │   ├── session.go           # セッション構造体、ScreenBuffer、Broadcaster
│   │   └── store.go             # セッション永続化
│   ├── status/
│   │   ├── detector.go          # 状態検知ロジック
│   │   └── patterns.go          # Claude Code出力パターン
│   ├── notify/
│   │   └── notifier.go          # 通知機能（ローカル/リモート）
│   ├── daemon/
│   │   ├── server.go            # デーモンサーバー
│   │   └── client.go            # デーモンクライアント
│   ├── tui/
│   │   ├── model.go             # TUIモデル
│   │   └── styles.go            # lipglossスタイル
│   ├── worktree/
│   │   └── manager.go           # git worktree管理
│   ├── repository/
│   │   └── registry.go          # リポジトリ登録・管理
│   ├── prompt/
│   │   └── injector.go          # プロンプト自動注入
│   └── config/
│       └── config.go            # 設定管理
├── go.mod
├── go.sum
├── Makefile
├── README.md
├── design.md
├── milestone.md
└── context.md

~/.ccvalet/                      # データディレクトリ
├── config.yaml                  # 設定ファイル
├── sessions/                    # セッションデータ
├── worktrees/                   # worktree配置先（デフォルト）
│   └── <repo-name>/
│       └── <branch-name>/
└── prompts/                     # プロンプトテンプレート
    ├── default.md
    └── custom/
```

## 6. コアコンポーネント詳細

### 6.1 Session Manager

```go
type Session struct {
    ID          string
    Name        string           // 必須
    WorkDir     string
    Status      SessionStatus    // queued/creating/running/thinking/idle/permission/error/stopped
    PTY         *os.File
    Cmd         *exec.Cmd
    CreatedAt   time.Time

    // 新規フィールド
    Repository  string           // リポジトリパス（オプション）
    Branch      string           // ブランチ名（オプション）
    WorktreeDir string           // worktreeパス（オプション）
    Prompt      string           // 自動注入プロンプト

    // Runtime fields
    LastOutputTime time.Time     // idle安定性検出用
}

type SessionStatus string
const (
    StatusQueued     SessionStatus = "queued"     // キュー待機中
    StatusCreating   SessionStatus = "creating"   // worktree作成中/CC起動中
    StatusRunning    SessionStatus = "running"    // 起動直後（状態未検出）
    StatusThinking   SessionStatus = "thinking"   // 処理中
    StatusIdle       SessionStatus = "idle"       // 入力待ち
    StatusPermission SessionStatus = "permission" // 許可待ち
    StatusError      SessionStatus = "error"      // エラー
    StatusStopped    SessionStatus = "stopped"    // 停止
)

type SessionManager interface {
    Create(opts CreateOptions) (*Session, error)
    Get(id string) (*Session, error)
    List() ([]*Session, error)
    Attach(id string) error
    Detach() error
    Kill(id string) error
    GetOutput(id string, lines int) (string, error)
}

type CreateOptions struct {
    Name        string  // セッション名（省略時はブランチ名を使用）
    WorkDir     string  // 既存ディレクトリモード用
    Repository  string  // worktreeモード用
    Branch      string  // worktreeモード用（必須）
    BaseBranch  string  // 新規ブランチ作成時のベース（デフォルト: リポジトリ設定 or main）
    NewBranch   bool    // 新規ブランチを作成するか
    PromptName  string  // 注入するプロンプト名
    PromptArgs  string  // プロンプトに渡す引数（${args}に展開）
}
```

### 6.2 Repository Registry

```go
type Repository struct {
    Path        string
    Name        string   // 表示名（自動推定 or ユーザー指定）
    BaseBranch  string   // 新規ブランチ作成時のデフォルトベース（デフォルト: main）
    SetupCmds   []string // worktree作成後に実行するコマンド
    SetupScript string   // または外部スクリプトパス
}

type RepositoryRegistry interface {
    Add(path string, opts AddOptions) error
    Remove(name string) error
    List() ([]Repository, error)
    Get(name string) (*Repository, error)
}
```

### 6.3 Worktree Manager

```go
type WorktreeManager interface {
    Create(repo *Repository, branch string) (string, error)
    Delete(worktreePath string) error
    List(repo *Repository) ([]WorktreeInfo, error)
    RunSetup(worktreePath string, repo *Repository) error
}

type WorktreeInfo struct {
    Path       string
    Branch     string
    Repository string
}
```

### 6.4 Prompt Injector

```go
type PromptInjector interface {
    Inject(sessionID string, promptName string) error
    ListPrompts() ([]PromptInfo, error)
    GetPrompt(name string) (string, error)
}

type PromptInfo struct {
    Name        string
    Path        string
    Description string
}
```

セッション起動後、Claude Codeがidle状態になったタイミングでプロンプトを自動入力する。

### 6.5 Parallel Manager（並列管理）

```go
type ParallelManager interface {
    // アクティブ数（creating + running + thinking + permission）を取得
    GetActiveCount() int
    // キュー待ちタスクを取得
    GetQueuedSessions() []*Session
    // キューにタスクを追加
    Enqueue(opts CreateOptions) (*Session, error)
    // キューからタスクをキャンセル
    CancelQueued(sessionID string) error
    // スロットが空いたら自動起動
    ProcessQueue() error
}
```

**並列管理の設計:**

| 項目 | 仕様 |
|------|------|
| 並列数の定義 | `creating` + `running` + `thinking` + `permission` 状態のセッション数 |
| `idle` セッション | カウント外（いつでもattach可能） |
| `queued` セッション | カウント外（起動待ち） |
| 自動起動 | アクティブ数 < max_parallel のときキューから起動 |
| 優先度 | FIFO（先入れ先出し） |
| 永続化 | なし（デーモン再起動でクリア） |

**タスクライフサイクル:**
```
queued → creating → running → thinking ⇄ idle
                         ↓         ↓
                       error    (attach可)
```

**TUI表示:**
```
╭─ llm-mgr ─────────────────────────────────────────────────╮
│                    ● 2 thinking  ○ 1 idle  ⏳ 3 queued    │
├───────────────────────────────────────────────────────────┤
│ › ● api-refactor                               thinking   │
│     ocean-view / feature-api                              │
│                                                           │
│   ○ backend-test                                   idle   │
│     ocean-view / test-api                                 │
├─ Queue (3) ───────────────────────────────────────────────┤
│   ⏳ new-feature        ocean-view / feature-x            │
│   ⏳ bugfix-123         ocean-view / fix-123              │
╰───────────────────────────────────────────────────────────╯
 n new  c cancel-queued  k kill  enter attach  q quit
```

## 7. TUI設計

### 7.1 画面構成（ダッシュボード型）

**設計方針:** 3行表示でセッション情報を見やすく表示

```
╭─ llm-mgr ─────────────────────────────────────────────────────────────────────╮
│                                               ● 1 running  ⏳ 1 perm  ○ 1 idle │
├───────────────────────────────────────────────────────────────────────────────┤
│                                                                               │
│ › ● api-refactor                                                     running  │
│     ocean-view / feature-api                                          2h ago  │
│     ~/.ccvalet/worktrees/ocean-view/feature-api                               │
│                                                                               │
│   ⏳ frontend-fix                                                       perm  │
│     ocean-view / fix-ui                                              30m ago  │
│     ~/.ccvalet/worktrees/ocean-view/fix-ui                                    │
│                                                                               │
│   ○ test-coverage                                                      idle  │
│     (no repo)                                                         5m ago  │
│     ~/projects/tests                                                          │
│                                                                               │
╰───────────────────────────────────────────────────────────────────────────────╯
 n new  k kill  d delete  enter attach  q quit
```

**セッション表示（3行構成）:**
| 行 | 内容 |
|-----|------|
| 1行目 | カーソル + ステータスアイコン + セッション名 + ステータスラベル |
| 2行目 | リポジトリ名 / ブランチ名 + 経過時間 |
| 3行目 | ワークディレクトリ（フルパス） |

**ステータスサマリー:**
- ヘッダー右側に各ステータスのセッション数を表示
- `● running` `⏳ perm` `○ idle` 等

### 7.2 セッション作成フロー（TUI）

**設計方針:** スマートデフォルト方式 - 最小操作で起動できることを重視

**デフォルトモード:** Worktreeモード + 新規ブランチモード

```
┌─ New Session (Worktree Mode) ──────────────────────────────────┐
│                                                                │
│ Session Name: [                  ]  ← 空ならブランチ名を使用   │
│                                                                │
│ Repository:   [ocean-view      ▼]  ← インクリメンタルサーチ    │
│   > ocean-view                     （前回使用値をデフォルト）  │
│     my-project                                                 │
│     another-repo                                               │
│                                                                │
│ Branch:       [feature-login     ]  ← 自由入力（新規ブランチ） │
│   [New Branch Mode]                                            │
│                                                                │
│ Base Branch:  [origin/main     ▼]  ← インクリメンタルサーチ    │
│   > origin/main                    （ローカル＋リモート表示）  │
│     origin/develop                                             │
│                                                                │
│ Prompt:       [coding-task     ▼]  ← インクリメンタルサーチ    │
│   > coding-task                    （~/.ccvalet/prompts/配下）│
│     review-code                                                │
│   Type to search prompts... (optional)                         │
│                                                                │
│ Args:         [ログイン機能を実装  ]  ← ${args}変数に展開      │
│   Arguments to pass to the prompt (${args})                    │
│                                                                │
│ Tab: switch  Ctrl+W: toggle worktree  Ctrl+B: new branch       │
│ Enter: create  Esc: cancel                                     │
└────────────────────────────────────────────────────────────────┘
```

**入力項目:**

| 項目 | 必須 | デフォルト | 説明 |
|------|------|-----------|------|
| Session | - | ブランチ名 | セッション名（省略可） |
| Repository | ○ | 前回使用値 | 登録済みリポジトリからインクリメンタルサーチで選択 |
| Branch | ○ | - | 新規ブランチ名（自由入力）/ 既存ブランチ（ローカルのみ選択） |
| Base Branch | △ | リポジトリ設定 or main | 新規ブランチ時のベース（ローカル＋リモートから選択） |
| Prompt | - | - | プロンプトテンプレート名（インクリメンタルサーチで選択、省略可） |
| Args | - | - | プロンプト引数（`${args}` 変数に展開される、省略可） |

**モード切替:**
| キー | 説明 |
|------|------|
| Ctrl+W | Worktreeモード ⇔ 通常モード（ディレクトリ指定） |
| Ctrl+B | 新規ブランチモード ⇔ 既存ブランチモード |

**ブランチ選択の動作:**
| モード | Branch入力 | Base Branch |
|--------|-----------|-------------|
| 新規ブランチ（デフォルト） | 自由入力 | ローカル＋リモートから選択 |
| 既存ブランチ | ローカルブランチから選択 | 表示なし |

### 7.3 セッション作成の自動実行フロー

ユーザーがEnterを押した後、以下が自動実行される：

```
1. git fetch origin
   ↓
2. git worktree add ~/.ccvalet/worktrees/<repo>/<branch> \
     -b <branch> origin/<base>  (新規の場合)
   または
   git worktree add ~/.ccvalet/worktrees/<repo>/<branch> <branch>  (既存の場合)
   ↓
3. セットアップスクリプト実行（設定されている場合）
   ↓
4. Claude Codeセッション起動
   ↓
5. idle検知後、プロンプト変数展開 → PTYに注入 → Enter送信
   ↓
Claude Codeが自動で作業開始
```

## 8. CLI コマンド

### 8.1 セッション管理（既存）

```bash
ccvalet daemon start|stop|status
ccvalet new <session-name> [options]
ccvalet list
ccvalet attach <session-name>
ccvalet kill <session-name>
ccvalet ui
```

### 8.2 リポジトリ管理（新規）

```bash
# リポジトリ登録
ccvalet repository add <path> [--name <name>] [--setup-script <path>]
ccvalet repository add ~/dev/my-project --setup-script ./scripts/worktree-setup.sh

# リポジトリ一覧
ccvalet repository list

# リポジトリ削除
ccvalet repository remove <name>
```

### 8.3 Worktree管理（新規）

```bash
# worktree一覧（特定リポジトリ）
ccvalet worktree list <repo-name>

# worktree削除
ccvalet worktree delete <session-name|branch-name>
```

### 8.4 セッション作成オプション（拡張）

```bash
# 既存ディレクトリで起動（従来通り）
ccvalet new my-session --workdir ~/projects/my-project

# worktreeモードで起動（既存ブランチ）
ccvalet new --repo ocean-view --branch feature/existing

# worktreeモードで起動（新規ブランチ）
ccvalet new --repo ocean-view --branch feature/new --new-branch
ccvalet new --repo ocean-view --branch feature/new --new-branch --base develop

# セッション名省略（ブランチ名がセッション名になる）
ccvalet new --repo ocean-view --branch feature/login --new-branch
# → セッション名: feature/login

# プロンプト自動注入（引数付き）
ccvalet new --repo ocean-view --branch feature/login --new-branch \
  --prompt coding-task --args "ログイン機能を実装して"

# フル指定
ccvalet new my-session \
  --repo ocean-view \
  --branch feature/auth \
  --new-branch \
  --base main \
  --prompt coding-task \
  --args "認証機能を実装してください"
```

**オプション一覧:**

| オプション | 短縮 | 説明 |
|-----------|------|------|
| `--repo` | `-r` | リポジトリ名 |
| `--branch` | `-b` | ブランチ名 |
| `--new-branch` | `-n` | 新規ブランチを作成 |
| `--base` | | ベースブランチ（デフォルト: リポジトリ設定 or main） |
| `--workdir` | `-d` | 作業ディレクトリ（worktreeモード以外） |
| `--prompt` | `-p` | プロンプトテンプレート名 |
| `--args` | | プロンプトに渡す引数 |

### 8.5 プロンプト管理

プロンプトは `~/.ccvalet/prompts/` にMarkdownファイルとして配置。
ファイル名（拡張子なし）がプロンプト名になる。

```bash
# プロンプト配置（ユーザーが直接ファイルを作成/編集）
vim ~/.ccvalet/prompts/coding-task.md

# プロンプト一覧
ccvalet prompt list

# プロンプト内容表示
ccvalet prompt show <name>
```

## 9. 状態検知

### 9.1 アプローチ

状態検知には2つのアプローチがある：

| アプローチ | 概要 | 採用状況 |
|-----------|------|---------|
| **PTY パース** | PTY出力をパターンマッチで解析 | ✅ MVP で採用 |
| **Claude Code Hooks** | Claude Code のフック機能で状態変化を検知 | 🔄 将来の改善候補 |

### 9.2 PTY パース（現在の実装）

PTY 出力をリアルタイムでパターンマッチして状態を判定する。

**検出ロジック** (`internal/status/detector.go`):
- `recentLines`: 直近30行（permission, error 検出用）
- `lastFewLines`: 直近2行（thinking, idle 検出用）

**idle検出の安定化** (`internal/session/manager.go`):

Claude Codeは画面を高速で再描画するため、単純なパターンマッチではidle/thinkingが高頻度で切り替わる問題がある。
これを解決するため、**出力安定性ベースのidle検出**を実装：

1. PTY出力を受信するたびに `LastOutputTime` を更新
2. 別goroutine (`checkIdleStability`) が1秒ごとに以下をチェック：
   - 出力が3秒以上安定している（新しい出力がない）
   - かつ idleパターンが検出される
3. 両条件を満たした場合のみ idle に遷移

これにより：
- thinking/permission/error: パターンマッチで即時検出
- idle: 出力が3秒間安定 + パターンマッチ で確定

**パターン定義** (`internal/status/patterns.go`):
```go
var patterns = map[SessionStatus][]string{
    StatusTrust: {  // 最優先: 初回trust確認
        "can pose security risks", "Yes, proceed",
        "only use files from trusted sources",
    },
    StatusThinking: {
        "esc to interrupt",  // 処理中の確実なインジケーター
    },
    StatusIdle: {
        ">\u00a0",  // プロンプト（non-breaking space）
    },
    StatusPermission: {
        "Allow", "Deny", "[Y/n]", "[y/N]",
        "Do you want", "Would you like",
    },
    StatusError: {
        "Error:", "error:", "failed", "Exception", "panic:",
    },
}
```

**フィルタリング**:
- ANSIエスケープシーケンス除去
- ステータスバー行（`│.*│.*HH:MM:SS`）除外
- ボックス装飾（`╭╮╰╯─`）除外

### 9.3 Trust自動応答

初回ディレクトリtrust確認を検知し、自動的に "Yes, proceed" を選択する：
1. `StatusTrust` を検知（パターン: "can pose security risks", "Yes, proceed" 等）
2. 自動的にEnter（\r）をPTYに送信
3. 画面バッファをクリアして表示崩れを防止
4. 処理を継続

**注:** ツール実行許可の自動応答（オートパイロット）は `.claude/settings.local.json` で設定することを推奨。

### 9.4 PTYリサイズ対応

アタッチ中のターミナルサイズ変更をClaude Codeに伝播する：

**検出方式（2つを併用）:**
- **SIGWINCH**: 通常のターミナルリサイズで発火
- **ポーリング（500ms間隔）**: tmux等でSIGWINCHが届かない場合の対応

**プロトコル:**
```
クライアント → デーモン: \x00\x00RESIZE:cols:rows\x00\x00
デーモン: エスケープシーケンスを解析 → pty.Setsize() 実行
```

### 9.5 停止セッションの自動起動

`attach`時にセッションが停止（stopped）状態の場合、自動的に起動する：
- デーモン再起動後もセッションに再アタッチ可能
- CLI/TUI両方で動作
- 既に実行中の場合は何もしない

## 10. 通知機能

### 10.1 概要

状態変化時にデスクトップ通知を送信する機能。ローカル環境とリモートサーバー（EC2等）の両方に対応。

### 10.2 通知タイミング

| 状態遷移 | 通知タイトル | 説明 |
|---------|------------|------|
| `any → permission` | Permission Required | Claude が許可を待っている |
| `thinking → idle` | Task Complete | タスクが完了した |
| `any → error` | Error | エラーが発生した |

### 10.3 実装方式

**ローカル通知:**
- macOS: `osascript` でネイティブ通知
- Linux: `notify-send` で通知

**リモート通知（TCP）:**
- ヘッドレスサーバー（EC2等）から Mac に通知を送信
- JSON フォーマット: `{"title":"...","message":"..."}`
- `notification-subscriber.sh` と互換

### 10.4 設定

```bash
# リモート通知を有効化（デーモン起動前に設定）
export CLAUDECREW_NOTIFY_HOST=<Mac の IP>
export CLAUDECREW_NOTIFY_PORT=60000  # デフォルト値

# Mac側でリスナー起動
~/notification-subscriber.sh
```

## 11. 設定ファイル

```yaml
# ~/.ccvalet/config.yaml

# Claude Code
claude:
  command: claude
  args: []

# セッション
session:
  default_work_dir: "."
  status_check_interval: 2s
  history_lines: 100
  require_name: true  # セッション名必須

# 並列管理
parallel:
  max_parallel: 3  # 同時にthinking/permissionできるセッション数

# TUI
tui:
  theme: default
  refresh_interval: 1s

# 通知
notification:
  enabled: true
  on_permission: true
  on_complete: true
  on_error: true

# Worktree
worktree:
  base_dir: ~/.ccvalet/worktrees  # デフォルト配置先

# リポジトリ
repositories:
  - path: /path/to/repo
    name: my-project
    base_branch: main           # 新規ブランチ作成時のデフォルトベース
    setup:
      - cp ../.env .env
      - npm install

  - path: /path/to/complex-repo
    name: complex-project
    base_branch: develop        # リポジトリごとに設定可能
    setup_script: ./scripts/worktree-setup.sh

# プロンプト
prompts:
  dir: ~/.ccvalet/prompts
```

## 12. プロンプトテンプレート

### 12.1 ディレクトリ構造

```
~/.ccvalet/prompts/
├── coding-task.md     # コーディングタスク用
├── code-review.md     # コードレビュー用
├── bug-fix.md         # バグ修正用
└── custom/
    └── my-workflow.md # ユーザー定義プロンプト
```

### 12.2 テンプレート形式（Markdown）

**coding-task.md:**
```markdown
/task ${args}

## 作業ブランチ
${branch}

## リポジトリ
${repository}

## 制約
- TypeScriptで実装
- テストも書く
- 既存のコードスタイルに従う
```

**code-review.md:**
```markdown
/review ${args}

## 対象
mainブランチとの差分をレビューしてください。

## 観点
- コードの品質
- セキュリティ上の問題
- パフォーマンス
```

**bug-fix.md:**
```markdown
/fix ${args}

## ブランチ
${branch}

## 手順
1. 問題の原因を調査
2. 修正を実装
3. テストを追加
4. 動作確認
```

### 12.3 使用可能な変数

| 変数 | 説明 | 例 |
|------|------|-----|
| `${args}` | ユーザー入力の引数 | `ログイン機能を実装して` |
| `${branch}` | ブランチ名 | `feature-login` |
| `${repository}` | リポジトリ名 | `ocean-view` |
| `${session}` | セッション名 | `my-session` |
| `${workdir}` | 作業ディレクトリパス | `~/.ccvalet/worktrees/ocean-view/feature-login` |
| `${base_branch}` | ベースブランチ名 | `main` |

### 12.4 変数展開のタイミング

1. ユーザーがセッション作成時にプロンプトと引数を指定
2. セッション起動後、Claude Codeがidle状態になるのを検知
3. テンプレートを読み込み、変数を展開
4. 展開後のプロンプトをPTYに送信
5. Enterキーを自動送信してプロンプトを実行

## 13. 実装フェーズ

### Phase 1: コア機能（MVP） ✅
- [x] プロジェクト構造セットアップ
- [x] PTY管理 (creack/pty)
- [x] セッション作成・一覧・終了
- [x] シンプルなTUI (bubbletea)
- [x] 基本的な状態検知

### Phase 2: TUI強化 ✅
- [x] セッションアタッチ/デタッチ
- [x] リアルタイム状態更新
- [x] キーボードショートカット
- [x] TUI作成モードのView実装

### Phase 3: 通知・状態監視 ✅
- [x] デスクトップ通知（ローカル/リモート対応）
- [x] 状態変化の検知・通知発火
- [x] デバウンス

### Phase 4: Git Worktree連携 ✅
- [x] リポジトリ登録・管理
- [x] worktree作成・削除
- [x] セットアップスクリプト実行
- [x] CLI/TUIからの操作
- [x] ベースブランチ設定・選択
- [x] git fetch自動実行
- [x] TUI: インクリメンタルサーチ（リポジトリ・ブランチ）
- [x] TUI: worktreeモード・新規ブランチモードがデフォルト
- [x] Trust自動応答

### Phase 5: プロンプト注入 ✅
- [x] プロンプト管理（テンプレート追加・一覧・削除）
- [x] プロンプト自動注入（idle検知→変数展開→PTY送信）
- [x] 出力安定性ベースのidle検出（3秒間出力なし+パターンマッチ）

### Phase 6: 並列管理 ✅
- [x] `queued` / `creating` ステータス追加
- [x] `max_parallel` 設定（config.yaml）
- [x] TUIセッション作成のバックグラウンド化
- [x] キュー表示・キャンセル機能
- [x] 自動起動ロジック（スロット空き時）

### Phase 7: TUIリデザイン
- [ ] ダッシュボード型レイアウト
- [ ] プロンプト選択UI
- [ ] 経過時間表示

## 14. セッション・Worktree設計（v3）

### 14.1 基本原則

| 原則 | 説明 |
|------|------|
| 1 ワークディレクトリ = 1 セッション | 同じディレクトリに対して起動できるCCは1プロセスまで |
| 排他制御 | 既存セッション（状態問わず）があるディレクトリへの新規作成はエラー |
| Worktree保持 | セッション削除時、worktreeは常に残す |
| リポジトリ登録必須 | CCタスク並列化支援に集中するため、登録済みリポジトリのみ対象 |

### 14.2 セッション作成フロー

```
1. リポジトリ選択（必須）
        ↓
2. ワークディレクトリ選択（必須）
   - リポジトリ本体
   - 既存worktree
   - 新規worktree作成
        ↓ ← 「1ワークディレクトリ=1セッション」チェック
3. ブランチ選択（必須）
   - 既存ブランチ or 新規ブランチ名
   - 新規の場合: ベースブランチ選択
        ↓ ← エラーチェック（後述）
4. [新規worktreeの場合] worktree名（オプション、デフォルト: ブランチ名）
        ↓ ← 同名worktree存在チェック
5. セッション名（オプション、デフォルト: リポジトリ名+ブランチ名）
6. プロンプト（オプション）
7. プロンプト引数（プロンプト選択時のみ入力可）
        ↓
作成実行 → ツールが指定ブランチにcheckoutしてCC起動
```

### 14.3 入力項目詳細

| 項目 | 必須 | デフォルト/備考 |
|------|------|----------------|
| リポジトリ | ○ | 登録済みリポジトリから選択 |
| ワークディレクトリ | ○ | リポジトリ本体/既存worktree/新規worktree |
| ブランチ | ○ | 既存 or 新規（新規時はベースブランチ選択） |
| ベースブランチ | △ | 新規ブランチ時のみ。remoteブランチの場合はfetchしてから作成 |
| worktree名 | △ | 新規worktree時のみ。デフォルト: ブランチ名 |
| セッション名 | - | デフォルト: `リポジトリ名/ブランチ名` |
| プロンプト | - | プロンプトテンプレートから選択 |
| プロンプト引数 | - | プロンプト選択時のみ入力可 |

### 14.4 セッション開始時の動作

1. 指定されたブランチにcheckout（必要な場合）
2. checkout成功後、CCプロセスを起動
3. プロンプトが指定されていれば自動注入

### 14.5 エラーチェックとタイミング

| チェック | タイミング | 条件 | 結果 |
|----------|-----------|------|------|
| 重複ワークディレクトリ | ワークディレクトリ選択後 | 同じディレクトリに既存セッションあり | エラー |
| 未コミット変更 | ブランチ選択後 | 現在ブランチ≠選択ブランチ かつ 未コミット変更あり | エラー |
| ブランチ重複checkout | ブランチ選択後 | 同じブランチが別worktreeでcheckout済み | エラー |
| worktree名重複 | worktree名入力後 | 同名worktreeが存在 | エラー |
| 新規ブランチ重複 | ブランチ選択後 | `--new-branch`で同名ブランチが既に存在 | エラー |
| 既存ブランチ不在 | ブランチ選択後 | 既存ブランチ指定で存在しない | エラー |

### 14.6 CLI使用例

```bash
# TUIで対話的に作成（推奨）
llm-mgr new

# CLIで直接指定
llm-mgr new --repo llm-mgr --workdir ~/repos/llm-mgr --branch feature-x

# 新規worktree + 新規ブランチ
llm-mgr new --repo llm-mgr --new-worktree --branch feature-y --new-branch --base main

# 既存worktreeを使用
llm-mgr new --repo llm-mgr --workdir ~/.ccvalet/worktrees/llm-mgr/feature-x --branch feature-x

# カスタムworktree名・セッション名
llm-mgr new --repo llm-mgr --new-worktree --branch feature-z --worktree task-001 --name "タスク001"
```

### 14.7 セッション削除とクリーンアップ

```bash
# セッション削除（worktreeは残る）
llm-mgr delete <session>

# stoppedセッションを一括削除
llm-mgr cleanup stopped

# stoppedセッション + worktreeを一括削除
llm-mgr cleanup stopped --worktree

# 削除対象の確認（dry-run）
llm-mgr cleanup stopped --dry-run
```

**削除時の動作:**
- セッションのみ削除、worktreeは保持
- 未マージ/未push作業の安全を確保
- `cleanup stopped --worktree` で停止済みセッションとworktreeを一括削除

### 14.8 データ構造

```go
type Session struct {
    ID           string    `json:"id"`
    Name         string    `json:"name"`          // セッション名（カスタム可能）
    WorkDir      string    `json:"work_dir"`      // ワークディレクトリパス
    CreatedAt    time.Time `json:"created_at"`
    Status       Status    `json:"status"`

    // Worktree関連
    Repository   string `json:"repository"`            // リポジトリ名（必須）
    Branch       string `json:"branch"`                // ブランチ名（必須）
    BaseBranch   string `json:"base_branch,omitempty"` // ベースブランチ名
    WorktreeName string `json:"worktree_name,omitempty"` // worktree名（新規worktree時）
    IsNewWorktree bool  `json:"is_new_worktree,omitempty"` // 新規worktreeかどうか

    // プロンプト関連
    PromptName string `json:"prompt_name,omitempty"`
    PromptArgs string `json:"prompt_args,omitempty"`

    // Runtime fields (not persisted)
    // ...
}

type CreateOptions struct {
    // 必須
    Repository string // リポジトリ名
    WorkDir    string // ワークディレクトリパス（リポジトリ本体/既存worktree/新規worktreeパス）
    Branch     string // ブランチ名

    // オプション
    Name          string // セッション名（省略時: リポジトリ名/ブランチ名）
    NewBranch     bool   // 新規ブランチを作成するか
    BaseBranch    string // ベースブランチ（新規ブランチ時）
    IsNewWorktree bool   // 新規worktreeを作成するか
    WorktreeName  string // worktree名（新規worktree時、省略時はブランチ名）

    // プロンプト
    PromptName string
    PromptArgs string
}
```

## 15. 依存ライブラリ

```go
require (
    github.com/charmbracelet/bubbletea v0.25.0
    github.com/charmbracelet/lipgloss v0.9.0
    github.com/charmbracelet/bubbles v0.18.0
    github.com/creack/pty v1.1.21
    github.com/spf13/cobra v1.8.0
    github.com/spf13/viper v1.18.0
    github.com/go-git/go-git/v5 v5.x.x  // worktree操作用（オプション）
)
```

## 15. ビルド・配布

```makefile
# Makefile
VERSION := 0.2.0

build:
	go build -o bin/ccvalet ./cmd/ccvalet

install:
	go install ./cmd/ccvalet

release:
	GOOS=darwin GOARCH=amd64 go build -o dist/ccvalet-darwin-amd64 ./cmd/ccvalet
	GOOS=darwin GOARCH=arm64 go build -o dist/ccvalet-darwin-arm64 ./cmd/ccvalet
	GOOS=linux GOARCH=amd64 go build -o dist/ccvalet-linux-amd64 ./cmd/ccvalet
```

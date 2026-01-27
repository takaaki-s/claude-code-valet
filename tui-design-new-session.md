# **ccvalet-cli \- New Session Wizard Design (Universal Working Directory)**

## **1\. デザインコンセプト**

* **ハイブリッド・ディレクトリ戦略:** \* **Worktree Mode:** 管理ディレクトリ \~/.ccvalet-cli/worktree/ 内に分離された環境を作成（並列作業に最適）。  
  * **Direct Mode:** クローン済みのソースリポジトリをそのまま使用（小規模な修正や、ワークツリーをサポートしない環境用）。  
* **コンテキスト適応:** 選択したモードに応じて、必要な入力項目（ワークツリー名など）を動的に表示/非表示にします。  
* **パスの可視化:** 最終的にどのパスで Claude が起動するかを常にプレビューします。

## **2\. 画面構成レイアウト**

### **\[A\] ヘッダー**

* \[ NEW SESSION \] モードを表示。

### **\[B\] 進行状況バー**

* 1\. Source & Mode \-\> 2\. Config \-\> 3\. Branch

### **\[C\] 入力・選択領域**

* **Source Repository:** ベースとなるローカルリポジトリのパス。  
* **Working Mode:** \[New Worktree\] / \[Reuse Worktree\] / \[Direct (Source Dir)\] の切り替え。  
* **Branch Mode:** \[Create New\] / \[Existing Branch\] の切り替え。

## **3\. ビジュアル・シミュレーション (ASCII Art Mockup)**

### **シーン1：リポジトリと動作モードの設定**

「ワークツリーを作るか、ソースを直接使うか」を選択するフェーズです。

╭──────────────────────────────────────────────────────────────────────────────╮  
│ ccvalet-cli \> CREATE NEW SESSION \[ 14:20:05 \] │  
├──────────────────────────────────────────────────────────────────────────────┤  
│ STEP 1: REPOSITORY & MODE │  
│ ──────────────────────────────────────────────────────────────────────────── │  
│ │  
│ 1\. Source Repository (Local Base): │  
│ \> \[ \~/projects/main-api-server\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_ \] (🔍 Search...) │  
│ │  
│ 2\. Session Name: │  
│ \> \[ fix-api-latency\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_ \] │  
│ │  
│ 3\. Working Directory Mode: │  
│ (\*) New Worktree ( ) Reuse Worktree ( ) Direct (Source Dir) │  
│ │  
│ 4\. Worktree Name: │  
│ \> \[ latency-debug\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_ \] │  
│ │  
│ Target Path: (Preview) │  
│ \~/.ccvalet-cli/worktree/main-api-server/latency-debug │  
│ │  
├──────────────────────────────────────────────────────────────────────────────┤  
│ \[INFO\] New Worktree: Isolated environment for parallel tasks. │  
╰──────────────────────────────────────────────────────────────────────────────╯  
\[Tab\] Next Field \[Space\] Toggle Mode \[Esc\] Cancel \[Enter\] Next Step

### **シーン2：Direct Mode（ワークツリーを使わない）選択時**

Modeを Direct (Source Dir) に切り替えると、ワークツリー名の入力が消え、パスがソースディレクトリを指します。

╭──────────────────────────────────────────────────────────────────────────────╮  
│ ccvalet-cli \> CREATE NEW SESSION \[ 14:20:15 \] │  
├──────────────────────────────────────────────────────────────────────────────┤  
│ STEP 1: REPOSITORY & MODE │  
│ ──────────────────────────────────────────────────────────────────────────── │  
│ │  
│ 1\. Source Repository (Local Base): │  
│ \> \[ \~/projects/main-api-server\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_ \] │  
│ │  
│ 2\. Session Name: │  
│ \> \[ quick-fix-readme\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_\_ \] │  
│ │  
│ 3\. Working Directory Mode: │  
│ ( ) New Worktree ( ) Reuse Worktree (\*) Direct (Source Dir) │  
│ │  
│ │  
│ Target Path: (Preview) │  
│ \~/projects/main-api-server │  
│ │  
├──────────────────────────────────────────────────────────────────────────────┤  
│ \[WARN\] Direct Mode: Claude will modify files in the source directory. │  
╰──────────────────────────────────────────────────────────────────────────────╯  
\[Tab\] Next Field \[Space\] Toggle Mode \[Esc\] Cancel \[Enter\] Next Step

## **4\. 追加されたインタラクションの詳細**

### **1\. 動作モードの使い分け**

* **New Worktree:** \* git worktree add を実行し、\~/.ccvalet-cli/worktree/ 配下に新しいパスを作成。  
  * **メリット:** 複数の修正を同時に並列で進められる（推奨）。  
* **Reuse Existing (Worktree):**  
  * 過去に作成したワークツリーディレクトリを再利用。  
* **Direct (Source Dir):**  
  * 追加のディレクトリを作らず、Source Repository のパスでそのまま claude を起動。  
  * **メリット:** ディレクトリ作成の手間がない。  
  * **注意:** そのディレクトリを他のセッションやエディタで使用している場合、競合する可能性がある。

### **2\. ブランチ操作との組み合わせ**

* **Direct Mode \+ New Branch:**  
  * Source Repository のディレクトリに移動し、git checkout \-b \<new\_branch\> を実行してから起動。  
* **Direct Mode \+ Existing Branch:**  
  * Source Repository でそのまま git checkout \<existing\_branch\> を実行して起動。

### **3\. バリデーション**

* **Direct Mode 時の警告:** Source Repository が現在別の作業で汚れている（Dirty）場合、git checkout が失敗する可能性があるため、事前に git status を確認し警告を表示する。

### **4\. セッション管理**

* ccvalet-cli は起動時に「どのパス」で「どのモード」で動いているかをメタデータとして保存し、一覧画面で「(Direct)」や「(Worktree)」といったタグを表示できるようにします。
# **ccvalet-cli \- Visual Design Mockup (Universal Version)**

## **1\. ステータス定義とカラーパレット**

文字化けを防ぐため、標準的なUnicode記号と絵文字を採用しました。

| ステータス | アイコン | カラー | 意味 / 状態 |
| :---- | :---- | :---- | :---- |
| **thinking** | ⚡ (Thunder) | \#bb9af7 (Purple) | Claudeが思考・生成中（最高優先度） |
| **permission** | ? (Quest) | \#ff9e64 (Orange) | ユーザーの実行許可待ち（要アクション） |
| **running** | ▶ (Play) | \#7dcfff (Cyan) | コマンド実行中 / プロセス稼働中 |
| **creating** | \+ (Plus) | \#7aa2f7 (Blue) | セッション初期化・構築中 |
| **queued** | … (Dots) | \#565f89 (Gray) | 実行待ちキューに滞留中 |
| **idle** | ○ (Circle) | \#9ece6a (Green) | 入力待ち・待機中 |
| **stopped** | ■ (Square) | \#414868 (Dark) | 終了・停止状態 |

## **2\. 画面構成レイアウト**

### **\[A\] グローバル・ヘッダー**

* 左側：\[ ccvalet-cli \] ブランドロゴ。  
* 右側：現在時刻。

### **\[B\] ステータス・インジケーター (サマリー)**

* 記号と数字を組み合わせて、現在の全体状況を一行で把握。

### **\[C\] メイン・セッションリスト**

* **SESSION / PROGRESS:**  
  * 1行目：セッション名とディレクトリ/ブランチ。  
  * 2行目：インデント記号 └─ を使い、現在の具体的な作業内容を表示。

## **3\. ビジュアル・シミュレーション (ASCII Art Mockup)**

╭──────────────────────────────────────────────────────────────────────────────╮  
│ ccvalet-cli \[ 14:20:05 \] │  
├──────────────────────────────────────────────────────────────────────────────┤  
│ STATS: ⚡ 1 Thinking ? 1 Permission ▶ 2 Running ○ 2 Idle │  
├──────────────────────────────────────────────────────────────────────────────┤  
│ SESSION / CURRENT PROGRESS STATUS LAST ACTIVE │  
│ ───────────────────────────────────────────────── ──────────── ───────────── │  
│ \>auth-service (feature/jwt) ⚡ THINKING Just now │  
│ └─ Analyzing security vulnerabilities in auth.go │  
│ │  
│ web-frontend (main) ? PERMISSION 12s ago │  
│ └─ Waiting for approval: \[shell\_execute\] rm \-rf... │  
│ │  
│ data-pipeline (refactor/sql) ▶ RUNNING 2s ago │  
│ └─ Running migration: 20240520\_init\_schema... │  
│ │  
│ api-gateway (hotfix/cors) ○ IDLE 5m ago │  
│ └─ Ready for next prompt. │  
│ │  
│ infra-terraform (dev) \+ CREATING 1m ago │  
│ └─ Initializing workspace and providers... │  
│ │  
│ legacy-worker (patch-v1) ■ STOPPED 1h ago │  
│ └─ Session terminated. │  
├──────────────────────────────────────────────────────────────────────────────┤  
│ \[LOGS: auth-service\] │  
│ 14:19:50 \[SYSTEM\] Session initialized. │  
│ 14:19:55 \[CLAUDE\] I will begin by auditing the middleware implementation. │  
│ 14:20:01 \[CLAUDE\] \> Reading src/auth/middleware.go... │  
│ 14:20:03 \[CLAUDE\] ⚡ Thinking... (Potential injection point found) \_ │  
╰──────────────────────────────────────────────────────────────────────────────╯  
\[N\] New \[D\] Delete \[Enter\] Attach \[S\] Stop \[L\] Logs \[Q\] Quit
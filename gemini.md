提示いただいたコードを拝見しました。非常に綺麗に実装されていますが、TUI（Terminal User Interface）ツールをバックグラウンドで管理し、後からアタッチする仕組みにおいて、**「初回表示が欠ける」**のは非常に典型的な問題です。

原因は大きく分けて2つ考えられます。

### 1. 「初回の描画データ」が消失している（リプレイの欠如）

`claudecode` が起動した直後、彼は「Trust確認画面」を描画するためのエスケープシーケンス（画面クリアやテキスト配置命令）を PTY に書き込みます。

- **問題発生の流れ:**

1. Daemon側で `pty.StartWithSize` を実行。`claudecode` が即座に描画命令を発行。
2. この時、まだ Client が `Attach` していない、あるいは `net.Dial` して `io.Copy` が始まる前である。
3. **その瞬間の出力データは、どこにも転送されず、読み捨てられるかバッファに溜まったまま埋もれる。**
4. Client がアタッチした時には、すでに描画命令の送信は終わっており、`claudecode` は「ユーザーの入力待ち」状態で静止している。
5. 結果、Client の画面には何も表示されない（または中途半端になる）。

- **解決策:** Daemon側で **「直近の画面状態を保持するバッファ」** を持つ必要があります。簡易的には、起動から数百〜数千バイトの出力をメモリにキャッシュしておき、Client が接続した瞬間にそのキャッシュを最初に送りつける「リプレイ」処理を実装します。

---

### 2. アタッチ時の「再描画要求（SIGWINCH）」の不足

TUI アプリは通常、ウィンドウサイズが変わった時（`SIGWINCH` 受信時）に画面をフルリロード（再描画）するように設計されています。

- **問題:**
  Client がアタッチした際、`sendResize` を呼び出して Daemon 側にサイズを伝えていますが、Daemon 側で **「OSレベルの SIGWINCH シグナル」を `claudecode` プロセスに送っていますか？**
  単に PTY のサイズ（`Setsize`）を変えるだけでは、すでに起動中のアプリ（`claudecode`）がそれに気づかない場合があります。
- **解決策:**
  Daemon側でサイズ変更命令を受け取った際、以下のように子プロセスにシグナルを飛ばしてください。

```go
// Daemon側の処理イメージ
func (s *Session) Resize(rows, cols int) {
    pty.Setsize(s.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
    // 子プロセスにサイズ変更を通知して再描画を促す
    s.cmd.Process.Signal(syscall.SIGWINCH)
}

```

---

### 3. Alternate Screen Buffer の競合

Client 側のコードで `\x1b[?1049h`（Alternate Screen への切り替え）を明示的に送っています。

- **懸念点:**
  `claudecode` 自身も TUI アプリとして Alternate Screen への切り替え命令を発行します。

1. `claudecode` が起動時に `\x1b[?1049h` を出す（Daemon内のPTYに書き込まれる）。
2. Client がアタッチし、独自に `\x1b[?1049h` を出す。
3. この「二重の切り替え」や、切り替えタイミングのズレにより、物理ターミナルの状態と `claudecode` が想定している論理状態が食い違うことがあります。

---

### 修正案のアドバイス

もっとも確実な修正方法は、**「Clientが接続した瞬間に、Daemon側から子プロセスへ SIGWINCH を強制的に送る」** ことです。

1. Client が `Attach` リクエストを送る。
2. Daemon は接続を受け取ったら、まず現在のサイズで `pty.Setsize` を実行する。
3. その後、**直ちに `cmd.Process.Signal(syscall.SIGWINCH)` を実行する。**
4. これにより、`claudecode` は「あ、画面サイズが変わったな」と判断し、Trust画面を含む現在の画面を**再描画**します。

#### Client側コードのワンポイントアドバイス

```go
    // Switch to alternate screen buffer
    os.Stdout.WriteString("\x1b[?1049h") // Enter alternate screen
    os.Stdout.WriteString("\x1b[H")      // Move cursor to home

```

ここの直後に、少し（10ms〜50ms程度）待機してから I/O ストリームを開始するか、あるいは Daemon 側に「準備完了」を伝えてから `SIGWINCH` を発火させると、より安定します。

**もし Daemon 側の「Resize 命令を受け取った後の処理（SIGWINCH を送っているか）」のコードがあれば、そこも確認させていただけますか？** そこが原因である可能性が非常に高いです。

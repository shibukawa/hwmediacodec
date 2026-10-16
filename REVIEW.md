# セキュリティ・性能レビュー第2回（2026-10-16）

前回（下記、2026-10-14）が見落とした点と、前回「残課題」とした項目を対象に、コア・パーサ・各バックエンド・コンテナ・画像・配信・サンプルを再び全体監査した。再現できた不具合を修正し、回帰テストを追加した。govulncheck・staticcheck・gosec も通した。

## 修正内容

| 領域 | 問題と修正 |
| --- | --- |
| H.264 DPB | 全ピクチャを新しい長期参照に指定し続けるストリームで参照集合が無制限に増え、VA-API ではサーフェスプールが枯渇して `Send` が `ErrAgain` を返し続け `DecodeReader` がスピンしていた。適応マーキング後に短期参照を捨てても `max_num_ref_frames` を超えるときは最古の長期参照も捨てる。MMCO 3/6 の `long_term_frame_idx` と MMCO 4 の値は 16 を上限に検証する。`frame_num` の間隙は参照上限分だけ推定する（65535 フレームの間隙で 65535 サーフェスを割り当てていた）。 |
| H.264 SPS・スケーリング | クロップ量の合計が 32 bit で桁あふれし、`Crop()` が 2^32 のオフセットを返していた。パース時に拒否し、`Crop()` は 64 bit で計算する。`delta_scale` の範囲外値（IQ 行列をドライバへ渡す前）を拒否する。 |
| HEVC スライス | `num_entry_point_offsets` の上限が `PicSizeInCtbs`（最大 2^24）で、13 バイトの NAL で 64 MiB を割り当てていた。7.4.7.1 の上限（CTB 行数・タイル数）に縛り、読めた分だけ追加する。明示タイル幅の合計がピクチャを超える PPS を SPS と結び付く時点で拒否する（負の幅が `uint16` で 65535 になっていた）。`luma_offset`・`delta_chroma_offset`・`delta_poc_msb_cycle_lt` を規格の範囲で検証する。 |
| フレーム解放 | `Frame.Release` の冪等性が nil 検査と代入の 2 段階で、複数 goroutine からの同時解放でプールへ二重返却し得た。CAS で 1 回に限定する。表示順トラッカーはパラメータセットごとではなく AU ごとに 1 回だけマップを複製する。 |
| VPL・NVDEC | `Send` が出力キューの上限を見ず、`Receive` を呼ばない呼び出し元に対してフレームコピーを際限なく保持していた。他バックエンドと同じく上限で `ErrAgain` を返す。VPL のビジー待ちは ctx を見る。 |
| Media Foundation | 非同期 MFT のイベント待ちが無期限ブロックで、イベントを出さなくなったデバイスに対して `Send`・`Flush` が永久に返らなかった。ノンブロッキング取得と 5 秒の期限、ctx の確認に替えた。 |
| VA-API | `vaInitialize` 失敗時に `vaGetDisplayDRM` のディスプレイを `vaTerminate` せず、コンストラクタ呼び出しごとにリークしていた（VPL の走査も同じ）。coded buffer は合計長を先に数えて 1 回で確保し、プレフィックス不要のパケットは再コピーしない。 |
| VideoToolbox・AudioToolbox | エンコーダ出力の 4 バイト長プレフィックスをその場で start code に書き換え、非キーフレームの 2 回目のコピーをなくした。`AudioBufferList` に書く Go スライスのアドレスを `runtime.Pinner` で固定する（GC が動かせないことに頼っていた）。 |
| WebRTC | 視聴者からの PLI/FIR と参加のたびに無制限にキーフレームを要求していた（1 視聴者がストリーム全体を全キーフレームにできる）。要求を 500 ms 間隔に併合する。`PeerConnection` 数に上限がなかった。`MaxViewers`（既定 32、準備中の接続も数える）を追加し、超過・終了後は 503、答えられない offer は 400（pion のエラー文を返さない）、ICE 収集中にリクエストが切れた接続は閉じる（`AcceptContext`）。全視聴者への送信がエンコーダの goroutine 上で逐次実行され、遅い視聴者が全員のフレームを落としていた。視聴者ごとに有界キューと送信 goroutine を持ち、約 1 秒遅れた視聴者は切る。 |
| HLS | セグメントの `Cache-Control: max-age=3600` が、再起動で 1 から始まるシーケンス番号と組み合わさると古いセグメントをブラウザキャッシュから再生させた。window に載っている時間だけキャッシュ可能にする。GET/HEAD 以外は 405。 |
| サンプル・CLI | `hls`・`webrtc` の既定 listen が全インターフェース（`:8080`）で、認証のない画面配信を LAN に公開していた。既定を `127.0.0.1:8080` にし、`ReadHeaderTimeout` を設定した。`thumbnails` はエンコード失敗時に部分ファイルを残さない。CLI は出力ファイルの Close エラーを返す。`ebitenvideo` はパディング行を含む plane を `WritePixels` に渡さない。`capture.Duration` はキャプチャロックを取らない。 |
| スクリプト | 偽 VA ドライバのスクリプトが `TESTFLAGS` をコンテナ内シェルに再解釈させていた（位置引数で渡す）。知識更新スクリプトはファイルハンドルを閉じ、リポジトリ外を指す `path:` を拒否する。 |
| HEIF/AVIF | グリッド画像のキャンバスにファイル側の宣言以外の上限がなく（前回の残課題）、iloc の extent は重複して同じタイルを指せるため、131 KB のファイルが 16 GiB のキャンバスと 65,280 回のデコードを要求できた。`heif.MaxPixels`（16384²）・`MaxTiles`（4096）・`MaxInputBytes`（256 MiB）と呼び出しごとの `Limits`（`DecodeWithLimits`・`DecodeBytesWithLimits`）を追加し、`ErrTooLarge` でデコーダを開く前に拒否する。`DecodeConfig`・`DecodeInfo` は宣言サイズを報告し続ける。`length_size = 0` の iloc は extent ごとの入力コストがなく、20,000 item × 65,535 extent で `DecodeConfig` に 21.5 秒かかっていた。単一 extent 以外では拒否し、ファイル全体の extent 数に上限を置く（107 µs）。`Decode(io.Reader)`・`DecodeConfig(io.Reader)` は seek できる入力ならヘッダと必要な extent だけ読み、そうでなければ `MaxInputBytes` までで打ち切る。`DecodeBytes` の二重パースを除去。ipma の property index が 7 bit を超えるときは 15 bit 形式で書き、32767 超はエラー。 |
| 音声エンコーダ | 前回の残課題。1 分の PCM を 1 フレームで `Send` すると、出力キューの上限 `maxPending` を見るのは `Send` の前だけで、数千パケットが `Receive` まで保持されていた。`Send` はキューが満ちるまでしかエンコードせず、残りの入力は `Receive` がパケットを渡して空きができるたびに続きをエンコードする（AudioToolbox は converter を上限まで回す、WebCodecs は 16 パケット分ずつ `AudioData` を渡す）。満杯のキューへの次の `Send` は `ErrAgain`。`Flush` は残りを一度にエンコードするので、その時点でだけ 1 フレーム分だけ上限を超え得る。両バックエンドに 60 秒 PCM の回帰テストを追加。 |
| MP4 | Reader の開始位置が 0 でないと、box の位置（reader 基準）と `stco` のオフセット（ファイル基準）が混ざり、`Sample` が別の場所のバイト列を黙って返していた。全オフセットをファイル基準に揃え、読み出しは `start` を足して seek する。サンプル索引は 1 サンプル 40 バイトで上限がなく、1 バイトサンプルの 100 MB ファイルが 4 GB を要求できた。索引を 24 バイトに縮め（継続時間は次の DTS から導出、sync はソート済み索引へ）、`MaxSamples`（800 万）で拒否する。パススルーの編集リストを出力の movie time scale に再スケールしていなかった（QuickTime の 600 や ffmpeg の 48000 のソースで音声が短く切られる）。`Demuxer.MovieTimeScale` を追加し、版の昇格を含めて再スケールする。Segmenter はキーフレームが来ないと際限なく保持していた。目標長の 4 倍または `MaxSegmentBytes` を超えたらエラー。`AccessUnit` の二重コピーを 1 回にし、`PresentationLength`・`KeyframeAtOrBefore` を O(n) 走査から事前計算と二分探索に、muxer の ctts を run-length に変えた。`Track.Info` は範囲外でゼロ値を返す。 |

## 性能測定

Apple M3。前回同様、該当箇所の局所的な測定で、コーデック全体の処理速度ではない。

| 対象 | 修正前 | 修正後 |
| --- | ---: | ---: |
| mp4 `VideoTrack.AccessUnit`（1 サンプル） | 2 allocs、1,671 B | 1 alloc、809 B |
| mp4 サンプル索引（1,048,576 サンプル） | 41.9 MB（40 B/サンプル） | 25.1 MB（24 B/サンプル） |
| HEIF iloc 20,000 item × 65,535 extent の `DecodeConfig` | 21.5 s | 107 µs |
| HEIF 255×256 グリッド（131 KB、16 GiB キャンバス） | 確保を試みる | 8 ms で拒否 |
| HEVC 13 バイトのスライスヘッダ（entry point 1,677 万） | 64 MiB 確保 | 1 MiB 未満で拒否 |
| WebRTC 視聴者への送信 | エンコーダの goroutine で逐次 | 視聴者ごとの goroutine と有界キュー |

`internal/pixconv` の NV12→RGBA は 1080p で 0.78 ms（8 分割）、単コア 3.52 ms と測定し、LUT と行並列ですでに分岐のない実装だったため変更しなかった。残る余地は SIMD かコア数に応じた分割数で、Go レベルの書き換えでは測定できる差が出なかった。

## 互換性上の変更

- `net/webrtc`: `Broadcaster.MaxViewers`（既定 32）を超える接続と、終了後の接続は 503。答えられない offer は 400。`Accept` は `ErrBadOffer`・`ErrTooManyViewers`・`hwmediacodec.ErrClosed` を返す。約 1 秒遅れた視聴者は切断される。キーフレーム要求は 500 ms に 1 回まで。
- `net/hls`: GET/HEAD 以外は 405。セグメントの `Cache-Control` は window の長さまで。
- `image/heif`・`image/avif`: `MaxPixels`・`MaxTiles`・`MaxInputBytes` を超えるファイルは `ErrTooLarge` で拒否される。`DecodeWithLimits`・`DecodeBytesWithLimits` で呼び出しごとに変えられる。seek できない Reader は `MaxInputBytes` までしか読まない。
- `mediacontainer/mp4`: `MaxSamples` を超えるトラックは開けない。Segmenter はキーフレームが 4 セグメント長来ないか `MaxSegmentBytes` を超えるとエラー。`Track.Info` は範囲外でパニックせずゼロ値。パススルーの編集リストの値が変わる（正しい時間になる）。
- H.264/HEVC パーサは規格外の値（クロップ合計、`delta_scale`、`long_term_frame_idx`、タイル幅合計、entry point 数、重み付き予測のオフセット、`delta_poc_msb_cycle_lt`）を構文エラーにする。
- VPL と NVDEC のデコーダは保持フレーム 8 個で `Send` が `ErrAgain` を返す。Media Foundation エンコーダはイベントが 5 秒来ないとエラーを返す。
- `examples/hls`・`examples/webrtc` の既定 listen アドレスは `127.0.0.1:8080`。
- `go.mod` に `toolchain go1.27.2` を置いた。govulncheck が報告した標準ライブラリ net/http（HTTP/2）の脆弱性の修正版で、`GOTOOLCHAIN=auto` の環境では初回に自動取得される。

## 検証と残課題

| 検証 | 結果 |
| --- | --- |
| 4 モジュールの通常テスト | `go test -p 1 ./... ./net/webrtc/... ./ebitenvideo/... ./examples/...` が通過（go1.27.2） |
| race 検査 | 変更したパッケージと playback・ebitenvideo・net/webrtc・mp4・imageitem が `-race` で通過（全パッケージ同時実行では playback の時間条件テストが負荷で落ちることがあり、単独では通る） |
| 静的検査 | `go vet`（4 モジュール、linux/windows amd64・arm64、js/wasm）、`gofmt`、`git diff --check`、staticcheck（実バグ報告 2 件は意図的なコード）、gosec（path・コマンド指摘はテスト専用の browserexec と test helper） |
| 脆弱性 | govulncheck: 自前コードの脆弱性なし。標準ライブラリの HTTP/2 は go1.27.2 で解消 |
| クロスビルド | macOS・Linux・Windows 各 amd64/arm64、js/wasm の 7 ターゲットが通過 |
| MediaCodec・WebCodecs | `GOWORK=off go test -tags mediacodec_typecheck ./internal/mediacodec/...` と、js/wasm を Node.js 上で実行した webcodecs・codec・reorder・pixconv のテストが通過。音声テストと長フレームのテストは `scripts/browser_test.sh` で Chrome 上で実行 |
| VA-API | `GOWORK=off sh scripts/vaapi_fake_driver_test.sh` の 4 設定すべて通過（監査が勧めた「エンコードコンテキストの render target に入力サーフェスも渡す」変更は偽ドライバが拒否したため採用せず） |

- Linux/NVIDIA 系バックエンドは GPU と CPU が重ならない。VA-API エンコーダは毎フレーム `vaSyncBuffer`、VPL は `AsyncDepth = 1` で即 `SyncOperation`、NVDEC はデコードコールバック内で map とコピーを行う。Media Foundation のデコード読み戻しは staging texture 1 枚を毎フレーム `Map` する。いずれもエンジンの能力を下回る構造で、実ハードウェアのある環境でパイプライン化する課題。
- `vaapi`・`vpl` の `HWMEDIACODEC_*_DEVICE` は任意パスを `O_RDWR` で開く。環境変数を制御できる者には新しい権限ではないが、`/dev/dri/` 以外は早期に拒否してもよい。
- `net/webrtc`・`examples` は手元の loopback と pion の視聴者で確認した。ブラウザでの再接続挙動（切断された視聴者の再参加）は未確認。
- Android 実機・Windows 実機・NVIDIA・Intel・AMD の実 GPU での今回変更の実行確認は未実施。


# セキュリティ・性能レビュー（2026-10-14）

コアAPI、ビットストリーム、各OSバックエンド、コンテナ、画像、録画・再生、HLS・WebRTC、別モジュールの依存構成を確認した。再現できる不具合を修正し、境界値と失敗時の回帰テストを追加した。

## 修正内容

| 領域 | 問題と修正 |
| --- | --- |
| 共通フレーム検証 | サイズ・strideの積が整数オーバーフローすると短いバッファを受理していた。`Frame.Validate`で除算を使って各planeを検証し、全エンコーダに適用。`RGBAImage`と画素変換にも適用した。 |
| 数値・時刻 | NaN・Infやネイティブ整数型へ収まらない設定値を拒否。時刻変換は128 bitの中間積で計算し、表現範囲外は飽和する。最近値丸めとゼロ方向の切り捨てを分け、既存の丸め規則を維持した。 |
| Annex-B・ビット読み取り | 大きなNALを読む際の全体再走査・固定量の再確保を解消。AUの無制限蓄積、`SkipBits`の加算オーバーフロー、parameter-set IDの範囲を検証した。`ReadBits`はバイト単位で処理する。 |
| AAC・ADTS | ADTS長の加算オーバーフロー、途中の音声形式変更、short writeの見逃し、ES descriptorの宣言長・終端・optional fieldの境界を修正した。 |
| 表示順 | Sendが`ErrAgain`やエラーになった際にもseq・POC・CRA状態が進んでいた。成功時だけ状態を確定し、通常フレームではparameter mapを複製しない。Flush失敗時も状態を維持する。 |
| VideoToolbox・WebCodecs | 出力待ちに加えて未完了の投入もキュー上限に算入。VideoToolboxは入力tokenで完了を追跡し、同期drop・非同期callback・失敗・再生成時に予約を解放する。 |
| MediaCodec | Flushまで全フレームのPTSを保持していた履歴を未出力分だけに縮小。負のbuffer offset、長さとlayoutのオーバーフロー、奇数幅NV12のstrideを修正した。 |
| AudioToolbox | キャンセルを確認し、変換失敗後の古いPCM・PTSを破棄する。設定途中で失敗したconverterの解放、native入出力サイズの検証、要求0の入力callbackを修正した。 |
| VA-API・Media Foundation | native planeのoffset・pitch・cropをコピー前に検証。VA-APIは行ごとの手動境界検査をplane単位の検証にまとめ、ハードウェア不要のテストを追加した。 |
| MP4・IVF・HEIF/AVIF | 欠けた必須box、壊れたsample/chunk table、実ファイルを超えるmdat/sample範囲、box・extent・OBUのサイズ計算を検証。HEIFのiref version 1のcount幅を修正し、grid余白分のcanvasと後続コピーを除去した。 |
| 録画・配信 | CaptureとCloseの競合によるclosed channelへの送信を防止。MP4の共有seek位置とWebRTCのpacket送信順を同期する。HLSは入力データを所有し、windowから外れたsegmentの参照を消す。 |
| 開発環境 | examplesの古い自己依存をv0.2.0へ揃え、欠けていたnet/webrtc依存を追加。`go mod tidy`でチェックサムを生成。crossbuildスクリプトがビルド失敗を見逃す問題を修正した。 |

## 性能測定

Apple M3で4 MiBのNALを読む同一ベンチマークを実行し、旧実装はGo overlayで比較した。

| | 修正前 | 修正後 |
| --- | ---: | ---: |
| 時間 | 103.3 ms/op | 0.714 ms/op |
| 割当量 | 149.0 MB/op | 20.9 MB/op |
| 割当回数 | 80 allocs/op | 11 allocs/op |

NAL読み込み部分で約145倍。コーデック全体の処理速度を示す測定ではない。

## 互換性上の変更

- Annex-B ReaderのAUサイズは既定64 MiB。大きなAUが必要な呼び出し元は、`Next`の前に`MaxAccessUnitSize`を設定する。超過時は`ErrAccessUnitTooLarge`になる。
- 不正な寸法・stride、非有限の設定値、native型へ収まらない値を早期に拒否する。`PixelFormat.FrameSize`は不正またはint範囲外のサイズで0を返す。
- MP4 trackのtime scaleは公開APIで扱える正のint32範囲に限定する。
- HEIFのextent合計が元データ全体より大きいitemは拒否する。同じ範囲の反復参照によるメモリ増幅を防ぐため、重複extentを大量に使うファイルも対象になる。

## 検証と残課題

| 検証 | 結果 |
| --- | --- |
| 4モジュールの通常テスト | `go test -p 1 ./... ./net/webrtc/... ./ebitenvideo/... ./examples/...` が通過 |
| 静的検査 | 同じ4モジュールの`go vet`と`git diff --check`が通過 |
| クロスビルド | macOS・Linux・Windows各amd64/arm64、js/wasmの7ターゲットが通過 |
| VA-API | `GOWORK=off sh scripts/vaapi_fake_driver_test.sh`の4設定すべて通過 |
| MediaCodec | `GOWORK=off go test -tags mediacodec_typecheck ./internal/mediacodec ./internal/mediacodec/sys`が通過 |
| WebCodecs | js/wasmをGoの`go_js_wasm_exec`でNode.js上実行し、担当パッケージのテストが通過 |
| AV1 fuzz | 10秒、1,267,157入力で異常なし |
| race検査 | `go test -race -p 1 ./... ./net/webrtc/... ./ebitenvideo/... ./examples/...`が全パッケージ通過 |

サンプルプレイヤーの固定300/600tick待機は、HEAD版でもrace付きのseek後に失敗した。deadline方式で測るとHEAD版で6.12秒かかって成功したため、条件と最終assertを保ち、最大10秒のdeadlineへ変更した。通常は条件成立時にすぐ終了する。

- 単一の巨大なPCM入力から大量のpacketが生成される場合、音声出力キューは上限を超え得る。任意長PCMの受け入れとFlush・Receiveの役割を含む設計が必要で、今回は変更していない。
- 画像の展開後バイト数には固定上限を設けていない。整数計算が収まる巨大gridは大きなcanvasを確保し得るため、未信頼画像の呼び出し元は入力長を制限し、`DecodeConfig`または`DecodeInfo`で画素数を確認する必要がある。ライブラリ側の設定可能な展開メモリ上限は残課題。
- HEIF/AVIFの`Decode(io.Reader)`と`DecodeConfig(io.Reader)`は全量読み込みする。入力長の制限は呼び出し元で行う。Playbackへ渡すReaderがReadで停止し続ける場合、Closeの終了時間も保証されない。
- Android実機、Windows実機、NVIDIA・Intel・AMDの実GPU、ブラウザ上での今回変更の実行確認は未実施。クロスビルド、型検査、Node.jsでのwasmテスト、偽VAドライバのテストとは区別する。
